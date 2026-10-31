// srvmon-ai: the AI agent container. It listens only on the compose network
// and answers srvmon-server (never the browser). The model setup (provider,
// address, key, models) is set in the panel (settings → AI): srvmon posts it
// to /v1/config and hands it over at start (GET /tools/ai-config). Until
// then the variables below are the defaults:
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

import { Agents, type AgentConfig, DEFAULT_LANG, type Event } from "./agent.js";
import { type AIConfig, Setup } from "./setup.js";

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
  if (!started) emit({ t: "error", msg: "empty reply" });
  res.end();
}

export function status(setup: Setup): Status {
  // Until the saved settings arrive, the environment defaults may name
  // another provider: runs wait rather than go to the wrong one.
  const error = setup.loading ? "waiting for the settings from srvmon" : setup.error;
  return { ready: !error && !!setup.provider, provider: setup.provider, model: setup.modelId, error, version };
}

function assistantText(m: { content?: { type: string; text?: string }[] }) {
  return (m.content ?? [])
    .filter((c) => c.type === "text")
    .map((c) => c.text ?? "")
    .join("")
    .trim();
}

export function createApp(cfg: AgentConfig) {
  const agents = new Agents(cfg);
  const { setup, modelRuntime } = cfg;
  return createServer(async (req, res) => {
    try {
      const url = new URL(req.url ?? "/", "http://ai");
      if (url.pathname === "/healthz") return json(res, 200, { status: "ok" });
      if (!authorized(req, cfg.token)) return json(res, 401, { error: "unauthorized" });
      const st = status(setup);
      const route = `${req.method} ${url.pathname}`;
      switch (route) {
        case "GET /v1/status":
          return json(res, 200, st);
        case "GET /v1/providers":
          return json(res, 200, setup.providers());
        case "POST /v1/config": {
          // Same limit as srvmon's settings body: up to 500 model ids.
          const body = (await readBody(req, 256 << 10)) as AIConfig;
          try {
            await setup.apply(body);
          } catch (err) {
            return json(res, 400, { error: err instanceof Error ? err.message : String(err) });
          }
          agents.reconfigured();
          console.log(JSON.stringify({ msg: "srvmon-ai configured", provider: setup.provider, model: setup.modelId, error: setup.error }));
          return json(res, 200, status(setup));
        }
        case "POST /v1/models": {
          const body = (await readBody(req)) as { provider?: string; base_url?: string; api?: string; api_key?: string };
          if (!body.provider) return json(res, 400, { error: "bad_provider" });
          return json(res, 200, await setup.listModels({ ...body, provider: body.provider }));
        }
        case "POST /v1/test": {
          const body = (await readBody(req)) as { model?: string };
          const model = setup.resolve(body.model);
          if (!model) return json(res, 200, { ok: false, error: st.error ?? "model not configured" });
          const t0 = Date.now();
          try {
            const reply = await modelRuntime.completeSimple(
              model,
              { messages: [{ role: "user", content: "Connection check. Answer with one word: works.", timestamp: Date.now() }] },
              { maxTokens: 1024, signal: AbortSignal.timeout(45_000) },
            );
            const r = reply as unknown as { stopReason?: string; errorMessage?: string; content?: { type: string; text?: string }[] };
            if (r.stopReason === "error" || r.stopReason === "aborted") {
              return json(res, 200, { ok: false, model: model.id, error: r.errorMessage || "model error", ms: Date.now() - t0 });
            }
            return json(res, 200, { ok: true, model: model.id, text: assistantText(r).slice(0, 200), ms: Date.now() - t0 });
          } catch (err) {
            return json(res, 200, { ok: false, model: model.id, error: err instanceof Error ? err.message : String(err) });
          }
        }
      }

      const chat = url.pathname.match(/^\/v1\/chats\/(\d+)(\/run)?$/);
      if (chat && req.method === "DELETE" && !chat[2]) {
        agents.forget(Number(chat[1]));
        return json(res, 200, { ok: true });
      }
      const isRun = req.method === "POST" && ((chat && chat[2]) || url.pathname === "/v1/run");
      if (!isRun) return json(res, 404, { error: "not_found" });
      if (!st.ready) return json(res, 503, { error: st.error ?? "not_ready" });
      const body = (await readBody(req)) as { text?: string; server?: string; task_id?: number; model?: string; lang?: string };
      const text = (body.text ?? "").trim();
      if (!text) return json(res, 400, { error: "bad_text" });
      if (chat) {
        const id = Number(chat[1]);
        return await stream(req, res, (emit, signal) =>
          agents.runChat(id, text, body.server || undefined, body.model ?? "", emit, signal, replyLang(body.lang)),
        );
      }
      return await stream(req, res, async (emit, signal) => {
        await agents.runTask(body.task_id ?? 0, text, emit, signal, replyLang(body.lang));
        return true;
      });
    } catch (err) {
      console.error("request failed", err);
      if (!res.headersSent) json(res, 500, { error: "internal" });
      else res.end();
    }
  });
}

/** The reply language srvmon asked for: an English language name, else English. */
export function replyLang(v: unknown): string {
  return typeof v === "string" && /^[A-Za-z][A-Za-z ]{1,39}$/.test(v) ? v : DEFAULT_LANG;
}

/** Asks srvmon for the setup saved in the panel, retrying until it answers. */
async function pullConfig(toolsUrl: string, token: string, setup: Setup, onApplied: () => void) {
  for (let wait = 2_000; ; wait = Math.min(wait * 2, 60_000)) {
    // srvmon may push newer settings while this fetch is under way; then
    // what the fetch returns is older and must not win.
    const gen = setup.generation;
    try {
      const res = await fetch(`${toolsUrl}/tools/ai-config`, {
        headers: { Authorization: `Bearer ${token}` },
        signal: AbortSignal.timeout(10_000),
      });
      if (res.status === 204) {
        setup.loading = false; // nothing saved: keep the environment defaults
        return;
      }
      if (res.ok) {
        const cfg = (await res.json()) as AIConfig;
        if (setup.generation !== gen || !setup.loading) {
          setup.loading = false;
          return;
        }
        try {
          await setup.apply(cfg);
          onApplied();
        } catch (err) {
          setup.error = `the saved settings did not apply: ${err instanceof Error ? err.message : err}`;
        }
        setup.loading = false;
        console.log(JSON.stringify({ msg: "srvmon-ai: settings from srvmon", provider: setup.provider, model: setup.modelId, error: setup.error }));
        return;
      }
      console.error(`srvmon-ai: srvmon answered ${res.status} for the settings`);
    } catch (err) {
      console.error("srvmon-ai: settings not loaded yet:", err instanceof Error ? err.message : err);
    }
    await new Promise((r) => setTimeout(r, wait));
  }
}

async function main() {
  const token = process.env.SRVMON_AI_TOKEN ?? "";
  if (token.length < 32) {
    console.error("srvmon-ai: SRVMON_AI_TOKEN (32+ chars) is required");
    process.exit(1);
  }
  const dataDir = process.env.SRVMON_AI_DATA ?? "/data";
  const port = Number(process.env.SRVMON_AI_LISTEN || 8090);
  const toolsUrl = process.env.SRVMON_TOOLS_URL || "http://server:8081";

  const modelRuntime = await ModelRuntime.create({
    authPath: join(dataDir, "pi", "auth.json"),
    modelsPath: join(dataDir, "pi", "models.json"),
  });
  const setup = new Setup(modelRuntime);
  try {
    await setup.apply({
      provider: process.env.SRVMON_AI_PROVIDER || "deepseek",
      model: process.env.SRVMON_AI_MODEL || "deepseek-v4-pro",
      api_key: process.env.SRVMON_AI_API_KEY,
      thinking: process.env.SRVMON_AI_THINKING || "low",
    });
  } catch (err) {
    setup.error = err instanceof Error ? err.message : String(err);
  }

  setup.loading = true;
  const server = createApp({ dataDir, toolsUrl, token, modelRuntime, setup });
  server.requestTimeout = 0; // runs stream for minutes
  server.listen(port, () => console.log(JSON.stringify({ msg: "srvmon-ai started", port, ...status(setup) })));
  void pullConfig(toolsUrl, token, setup, () => undefined);
  const stop = () => server.close(() => process.exit(0));
  process.on("SIGTERM", stop);
  process.on("SIGINT", stop);
}

if (process.argv[1] && import.meta.url === new URL(`file://${process.argv[1]}`).href) {
  void main();
}
