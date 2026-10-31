// End-to-end test of srvmon-ai with pi's faux provider: a scripted model
// calls srvmon_exec, the fake tool API answers, the stream reaches the
// client as srvmon events.
import assert from "node:assert/strict";
import { createServer, type Server } from "node:http";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { fauxAssistantMessage, fauxProvider, fauxText, fauxToolCall } from "@earendil-works/pi-ai";
import { ModelRuntime } from "@earendil-works/pi-coding-agent";

import { createApp } from "./main.js";
import { formatExec } from "./tools.js";

const token = "t".repeat(40);

function listen(s: Server): Promise<string> {
  return new Promise((resolve) =>
    s.listen(0, "127.0.0.1", () => {
      const a = s.address();
      resolve(`http://127.0.0.1:${typeof a === "object" && a ? a.port : 0}`);
    }),
  );
}

test("chat run streams text and srvmon tool calls", async () => {
  const calls: { path: string; auth?: string; body: unknown }[] = [];
  const tools = createServer(async (req, res) => {
    let raw = "";
    for await (const c of req) raw += c;
    calls.push({ path: req.url ?? "", auth: req.headers.authorization, body: raw ? JSON.parse(raw) : undefined });
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ status: "ok", read_only: true, exit_code: 0, output: "crm-api Up 6 days\n" }));
  });
  const toolsUrl = await listen(tools);

  const dataDir = mkdtempSync(join(tmpdir(), "srvmon-ai-"));
  const modelRuntime = await ModelRuntime.create({ authPath: join(dataDir, "auth.json"), modelsPath: join(dataDir, "models.json") });
  const faux = fauxProvider({ provider: "faux", models: [{ id: "faux-1" }] });
  modelRuntime.registerNativeProvider(faux.provider);
  faux.setResponses([
    fauxAssistantMessage([fauxText("Смотрю контейнеры. "), fauxToolCall("srvmon_exec", { server: "prod-api-01", command: "docker ps" })], {
      stopReason: "toolUse",
    }),
    fauxAssistantMessage("Все контейнеры работают."),
  ]);

  const app = createApp({ dataDir, toolsUrl, token, modelRuntime, model: faux.getModel(), thinking: "off" }, () => ({
    ready: true,
    provider: "faux",
    model: "faux-1",
    version: "test",
  }));
  const base = await listen(app);
  try {
    const unauth = await fetch(`${base}/v1/status`);
    assert.equal(unauth.status, 401);

    const res = await fetch(`${base}/v1/chats/7/run`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: JSON.stringify({ text: "что с контейнерами?", server: "prod-api-01" }),
    });
    assert.equal(res.status, 200);
    const events = (await res.text())
      .trim()
      .split("\n")
      .map((l) => JSON.parse(l));
    const types = events.map((e) => e.t);
    assert.ok(types.includes("tool") && types.includes("tool_end"), types.join(","));
    assert.equal(types.at(-1), "done");
    assert.equal(events.at(-1).text, "Все контейнеры работают.");
    const toolEnd = events.find((e) => e.t === "tool_end");
    assert.ok(toolEnd.ok);
    assert.match(toolEnd.out, /только чтение/);

    assert.equal(calls.length, 1);
    assert.equal(calls[0].path, "/tools/exec");
    assert.equal(calls[0].auth, `Bearer ${token}`);
    assert.deepEqual(calls[0].body, { server: "prod-api-01", command: "docker ps", reason: "", chat_id: 7, task_id: 0 });
  } finally {
    app.close();
    tools.close();
  }
});

test("rejected command tells the model not to retry", () => {
  assert.match(formatExec({ status: "rejected", read_only: false, action_id: 3 }), /ОТКЛОНИЛ/);
  assert.match(formatExec({ status: "ok", read_only: false, action_id: 3, exit_code: 0, output: "x" }), /после подтверждения/);
});
