// srvmon-ai: the AI agent container. It listens only on the compose network
// and answers srvmon-server (never the browser). Configuration:
//
//   SRVMON_AI_TOKEN      shared bearer token (both directions), required
//   SRVMON_TOOLS_URL     srvmon-server tool API, default http://server:8081
//   SRVMON_AI_PROVIDER   pi provider id, default deepseek (anthropic, openai, openrouter, ...)
//   SRVMON_AI_MODEL      model id, default deepseek-v4-pro
//   SRVMON_AI_API_KEY    provider API key (or the provider's own env var, e.g. DEEPSEEK_API_KEY)
//   SRVMON_AI_THINKING   off | minimal | low | medium | high, default low
//   SRVMON_AI_LISTEN     port, default 8090
//   SRVMON_AI_DATA       data dir, default /data
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { timingSafeEqual } from "node:crypto";
import { join } from "node:path";

import { ModelRuntime } from "@earendil-works/pi-coding-agent";

import { Agents, type AgentConfig, type Event } from "./agent.js";

const version = process.env.SRVMON_VERSION ?? "dev";

interface Status {
  ready: boolean;
  provider: string;
  model: string;
  error?: string;
  version: string;
}

function json(res: ServerResponse, status: number, body: unknown) {
  res.writeHead(status, { "Content-Type": "application/json", "Cache-Control": "no-store" });
  res.end(JSON.stringify(body));
}

async function readBody(req: IncomingMessage, limit = 64 << 10): Promise<unknown> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const c of req) {
    size += (c as Buffer).length;
    if (size > limit) throw new Error("body too large");
    chunks.push(c as Buffer);
  }
  return chunks.length ? JSON.parse(Buffer.concat(chunks).toString("utf8")) : {};
}

function authorized(req: IncomingMessage, token: string): boolean {
  const got = Buffer.from(req.headers.authorization ?? "");
  const want = Buffer.from(`Bearer ${token}`);
  return got.length === want.length && timingSafeEqual(got, want);
}

/** Streams a run as NDJSON; closing the request aborts the run. */
async function stream(req: IncomingMessage, res: ServerResponse, run: (emit: (e: Event) => void, signal: AbortSignal) => Promise<boolean>) {
  const ac = new AbortController();
  res.on("close", () => {
    if (!res.writableFinished) ac.abort();
  });
  let started = false;
  const emit = (e: Event) => {
    if (!started) {
      res.writeHead(200, { "Content-Type": "application/x-ndjson", "Cache-Control": "no-store" });
      started = true;
    }
    if (!res.writableEnded) res.write(JSON.stringify(e) + "\n");
  };
  const ok = await run(emit, ac.signal);
  if (!ok && !started) return json(res, 409, { error: "busy" });
  if (!started) emit({ t: "error", msg: "пустой ответ" });
  res.end();
}

export function createApp(cfg: AgentConfig & { token: string }, status: () => Status) {
  const agents = new Agents(cfg);
  return createServer(async (req, res) => {
    try {
      const url = new URL(req.url ?? "/", "http://ai");
      if (url.pathname === "/healthz") return json(res, 200, { status: "ok" });
      if (!authorized(req, cfg.token)) return json(res, 401, { error: "unauthorized" });
      const st = status();
      if (req.method === "GET" && url.pathname === "/v1/status") return json(res, 200, st);

      const chat = url.pathname.match(/^\/v1\/chats\/(\d+)(\/run)?$/);
      if (chat && req.method === "DELETE" && !chat[2]) {
        agents.forget(Number(chat[1]));
        return json(res, 200, { ok: true });
      }
      const isRun = req.method === "POST" && ((chat && chat[2]) || url.pathname === "/v1/run");
      if (!isRun) return json(res, 404, { error: "not_found" });
      if (!st.ready) return json(res, 503, { error: st.error ?? "not_ready" });
      const body = (await readBody(req)) as { text?: string; server?: string; task_id?: number };
      const text = (body.text ?? "").trim();
      if (!text) return json(res, 400, { error: "bad_text" });
      if (chat) {
        const id = Number(chat[1]);
        return await stream(req, res, (emit, signal) => agents.runChat(id, text, body.server || undefined, emit, signal));
      }
      return await stream(req, res, async (emit, signal) => {
        await agents.runTask(body.task_id ?? 0, text, emit, signal);
        return true;
      });
    } catch (err) {
      console.error("request failed", err);
      if (!res.headersSent) json(res, 500, { error: "internal" });
      else res.end();
    }
  });
}

async function main() {
  const token = process.env.SRVMON_AI_TOKEN ?? "";
  if (token.length < 32) {
    console.error("srvmon-ai: SRVMON_AI_TOKEN (32+ chars) is required");
    process.exit(1);
  }
  const dataDir = process.env.SRVMON_AI_DATA ?? "/data";
  const provider = process.env.SRVMON_AI_PROVIDER || "deepseek";
  const modelId = process.env.SRVMON_AI_MODEL || "deepseek-v4-pro";
  const thinking = (process.env.SRVMON_AI_THINKING || "low") as AgentConfig["thinking"];
  const port = Number(process.env.SRVMON_AI_LISTEN || 8090);

  const modelRuntime = await ModelRuntime.create({
    authPath: join(dataDir, "pi", "auth.json"),
    modelsPath: join(dataDir, "pi", "models.json"),
  });
  const key = process.env.SRVMON_AI_API_KEY;
  if (key) await modelRuntime.setRuntimeApiKey(provider, key);
  const model = modelRuntime.getModel(provider, modelId);

  let error: string | undefined;
  if (!model) error = `модель ${provider}/${modelId} не найдена (SRVMON_AI_PROVIDER / SRVMON_AI_MODEL)`;
  else {
    const available = await modelRuntime.getAvailable(provider).catch(() => []);
    if (!available.some((m: { id: string }) => m.id === modelId)) {
      error = `нет API-ключа для ${provider}: задайте секрет AI_API_KEY (SRVMON_AI_API_KEY)`;
    }
  }
  const status = (): Status => ({ ready: !error, provider, model: modelId, error, version });

  const server = createApp(
    {
      dataDir,
      toolsUrl: process.env.SRVMON_TOOLS_URL || "http://server:8081",
      token,
      modelRuntime,
      model,
      thinking,
    },
    status,
  );
  server.requestTimeout = 0; // runs stream for minutes
  server.listen(port, () =>
    console.log(JSON.stringify({ msg: "srvmon-ai started", port, provider, model: modelId, ready: !error, error })),
  );
  const stop = () => server.close(() => process.exit(0));
  process.on("SIGTERM", stop);
  process.on("SIGINT", stop);
}

if (process.argv[1] && import.meta.url === new URL(`file://${process.argv[1]}`).href) {
  void main();
}
