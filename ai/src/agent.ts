// Agent sessions on top of the pi SDK. One persistent pi session per srvmon
// chat (files under <data>/sessions/chat-<id>), a throwaway in-memory
// session per scheduled task run. Built-in pi tools (bash, read, edit,
// write) are off: the only tools are the srvmon ones.
import { mkdirSync, rmSync } from "node:fs";
import { join } from "node:path";

import {
  createAgentSession,
  createExtensionRuntime,
  type AgentSession,
  type ModelRuntime,
  type ResourceLoader,
  SessionManager,
  SettingsManager,
} from "@earendil-works/pi-coding-agent";

import type { Setup } from "./setup.js";
import { diagnoseToolNames, srvmonTools, toolNames } from "./tools.js";

export type Event =
  | { t: "text"; d: string }
  | { t: "tool"; id: string; name: string; args: unknown }
  | { t: "tool_end"; id: string; ok: boolean; out: string }
  | { t: "error"; msg: string }
  | { t: "done"; text: string };

export interface AgentConfig {
  dataDir: string;
  toolsUrl: string;
  token: string;
  modelRuntime: ModelRuntime;
  setup: Setup;
}

/** The reply language, as an English name ("Russian"). */
export const DEFAULT_LANG = "English";

/** What a session is for: a chat, a scheduled task or a read-only alert diagnosis. */
export type Mode = "chat" | "task" | "diagnose";

// How srvmon itself runs on the servers: the agent needs this to read the
// state right and not suggest the wrong fix (docker on a hypervisor).
const SRVMON_AGENT_FACTS = [
  "How srvmon-agent runs (field \"agent\" of srvmon_servers / srvmon_server):",
  "- docker: containers srvmon-agent and srvmon-dockerproxy; its log is `docker logs srvmon-agent`.",
  "- systemd: a service without docker (hypervisors such as Proxmox VE): `systemctl status srvmon-agent`, `journalctl -u srvmon-agent`; binary /usr/local/lib/srvmon-agent/srvmon-agent, settings /etc/srvmon/agent.env. There are no containers on such a host, and that is expected: never suggest installing docker there. Its warnings come from journald.",
  "- none: the server has no agent, the panel reaches it over SSH only (no metrics or alerts). waiting: a token was issued but no agent pushed yet. silent: the agent stopped pushing; check its service or container.",
  "- The agent token is kept by the panel only as a hash and cannot be shown. A new one is issued in the server passport, row \"agent token\": \"reissue on the server\" or \"token for a manual install\". The manual install of the systemd agent is `curl -fsSL <panel>/agent/install.sh | sudo SRVMON_TOKEN=<token> sh` (as root without sudo; through srvmon_exec it needs full sudo for the srvmon user, the limited mode does not allow sh); give the owner this command, never invent a token.",
  "- The panel updates agents of both kinds itself over SSH; do not replace the binary or the containers by hand.",
  "- Lines in the agent's logs come from the host itself (e.g. sshd or pvedaemon authentication failures are someone guessing passwords on that host), not from srvmon.",
];

export function systemPrompt(server?: string, task: boolean | Mode = false, lang = DEFAULT_LANG): string {
  const mode: Mode = task === true ? "task" : task === false ? "chat" : task;
  if (mode === "diagnose") return diagnosePrompt(server, lang);
  const lines = [
    "You are the AI agent of the srvmon monitoring panel. Through it the owner watches their servers: metrics, docker containers, logs.",
    `Reply in ${lang || DEFAULT_LANG}, briefly and to the point, like an experienced SRE. Write commands and paths as \`code\`.`,
    "",
    "You reach servers only through the srvmon tools:",
    "- srvmon_servers / srvmon_server: state and containers from the srvmon agents (start with them, they are cheap);",
    "- srvmon_exec: a command on a server over SSH. Reads run at once. Anything that changes a server waits for the owner's confirmation in the panel; explain such commands in reason;",
    "- srvmon_tasks / srvmon_task_create / srvmon_task_delete: scheduled monitoring tasks (cron in UTC).",
    "",
    ...SRVMON_AGENT_FACTS,
    "",
    "Rules:",
    "- First find out (logs, inspect, docker events), then propose a change and ask for confirmation with one clear command.",
    "- If the owner rejected a command, do not repeat it; offer another way.",
    "- Do not make up command output or server state: if you did not check, say so.",
    "- Do not repeat secrets (tokens, passwords, keys) from output in full.",
    "- When asked to \"watch\", \"check every N minutes\" or \"send to Telegram\", create a task with srvmon_task_create; in its prompt describe what to check and when to answer with a line starting with ALERT. Write the task prompt in the language of the conversation.",
  ];
  if (mode === "task") {
    lines.push(
      "",
      "This is a scheduled task run, the owner is not in the chat. Changing commands still wait for their confirmation; do not wait for them without need.",
      "Outcome: 1–5 lines. If the owner's attention is needed, the first line starts with ALERT.",
    );
  }
  if (server) {
    lines.push("", `Chat context: server "${server}". If a question does not name a server, it is about this one.`);
  }
  return lines.join("\n");
}

function diagnosePrompt(server: string | undefined, lang: string): string {
  const lines = [
    "You are the AI agent of the srvmon monitoring panel. An alert fired and you investigate it before the owner looks: this is a READ-ONLY diagnosis.",
    `Reply in ${lang || DEFAULT_LANG}, like an experienced SRE: short plain text, commands and paths as \`code\`, no tables or headings.`,
    "",
    "Tools: srvmon_servers / srvmon_server (state from the srvmon agents, cheap: start with them) and srvmon_exec (a read-only command on a server over SSH).",
    "",
    ...SRVMON_AGENT_FACTS,
    "",
    "Rules:",
    "- You cannot change anything. srvmon refuses every command that is not read-only; nobody will confirm it. Do not try other forms of a changing command.",
    "- The number of commands is limited: pick targeted ones (tail logs, do not dump them).",
    "- Do not make up output or state: if you did not check something, say so.",
    "- Do not repeat secrets (tokens, passwords, keys) from output.",
    "- Suggest the fix as a command the owner can run; you do not run it.",
  ];
  if (server) lines.push("", `The alert is about server "${server}".`);
  return lines.join("\n");
}

function loader(prompt: string): ResourceLoader {
  return {
    getExtensions: () => ({ extensions: [], errors: [], runtime: createExtensionRuntime() }),
    getSkills: () => ({ skills: [], diagnostics: [] }),
    getPrompts: () => ({ prompts: [], diagnostics: [] }),
    getThemes: () => ({ themes: [], diagnostics: [] }),
    getAgentsFiles: () => ({ agentsFiles: [] }),
    getSystemPrompt: () => prompt,
    getSystemPromptSource: () => undefined,
    getAppendSystemPrompt: () => [],
    getAppendSystemPromptSources: () => [],
    extendResources: () => {},
    reload: async () => {},
  } as ResourceLoader;
}

interface Entry {
  session: AgentSession;
  server?: string;
  model: string; // requested model id ("" = default)
  lang: string; // reply language the session was opened with
  gen: number; // setup generation the session was opened with
  busy: boolean;
  used: number;
}

const MAX_OPEN = 20;

export class Agents {
  private chats = new Map<number, Entry>();
  constructor(private cfg: AgentConfig) {}

  private chatDir(id: number) {
    return join(this.cfg.dataDir, "sessions", `chat-${id}`);
  }

  private async create(
    sessionManager: SessionManager,
    prompt: string,
    modelId: string,
    chatId?: number,
    taskId?: number,
    diagnoseId?: number,
  ) {
    const cwd = this.cfg.dataDir;
    const model = this.cfg.setup.resolve(modelId);
    if (!model) throw new Error("model not configured: open settings → AI");
    const { session } = await createAgentSession({
      cwd,
      agentDir: join(this.cfg.dataDir, "pi"),
      model,
      thinkingLevel: model.reasoning ? this.cfg.setup.thinking : "off",
      modelRuntime: this.cfg.modelRuntime,
      resourceLoader: loader(prompt),
      tools: diagnoseId ? diagnoseToolNames : toolNames,
      customTools: srvmonTools({ toolsUrl: this.cfg.toolsUrl, token: this.cfg.token, chatId, taskId, diagnoseId }),
      sessionManager,
      settingsManager: SettingsManager.inMemory({
        compaction: { enabled: true },
        retry: { enabled: true, maxRetries: 2 },
      }),
    });
    return session;
  }

  private async chat(id: number, server: string | undefined, model: string, lang: string): Promise<Entry> {
    const cur = this.chats.get(id);
    const gen = this.cfg.setup.generation;
    if (cur && cur.server === server && cur.model === model && cur.lang === lang && cur.gen === gen) return cur;
    if (cur) {
      // Server, model, language or settings changed: reopen (the history stays on disk).
      cur.session.dispose();
      this.chats.delete(id);
    }
    const dir = this.chatDir(id);
    mkdirSync(dir, { recursive: true });
    const session = await this.create(SessionManager.continueRecent(this.cfg.dataDir, dir), systemPrompt(server, false, lang), model, id);
    const e: Entry = { session, server, model, lang, gen, busy: false, used: Date.now() };
    this.chats.set(id, e);
    this.evict(id);
    return e;
  }

  /** Closes the least recently used idle sessions, never `keep` (about to run). */
  private evict(keep: number) {
    if (this.chats.size <= MAX_OPEN) return;
    const idle = [...this.chats.entries()].filter(([k, e]) => !e.busy && k !== keep).sort((a, b) => a[1].used - b[1].used);
    for (const [id, e] of idle.slice(0, this.chats.size - MAX_OPEN)) {
      e.session.dispose();
      this.chats.delete(id);
    }
  }

  isBusy(id: number) {
    return this.chats.get(id)?.busy ?? false;
  }

  /** Runs one chat turn. Returns false when the chat is busy. */
  async runChat(
    id: number,
    text: string,
    server: string | undefined,
    model: string,
    emit: (e: Event) => void,
    signal: AbortSignal,
    lang = DEFAULT_LANG,
  ) {
    if (this.isBusy(id)) return false;
    const e = await this.chat(id, server, model, lang);
    if (e.busy) return false;
    e.busy = true;
    e.used = Date.now();
    try {
      await drive(e.session, text, emit, signal);
    } finally {
      e.busy = false;
      e.used = Date.now();
    }
    return true;
  }

  /** Runs a scheduled task in a throwaway session. */
  async runTask(taskId: number, text: string, emit: (e: Event) => void, signal: AbortSignal, lang = DEFAULT_LANG) {
    const session = await this.create(SessionManager.inMemory(this.cfg.dataDir), systemPrompt(undefined, true, lang), "", undefined, taskId);
    try {
      await drive(session, text, emit, signal);
    } finally {
      session.dispose();
    }
  }

  /** Runs a read-only diagnosis of an alert in a throwaway session. */
  async runDiagnosis(
    alertId: number,
    text: string,
    server: string | undefined,
    model: string,
    emit: (e: Event) => void,
    signal: AbortSignal,
    lang = DEFAULT_LANG,
  ) {
    const session = await this.create(
      SessionManager.inMemory(this.cfg.dataDir),
      systemPrompt(server, "diagnose", lang),
      model,
      undefined,
      undefined,
      alertId,
    );
    try {
      await drive(session, text, emit, signal);
    } finally {
      session.dispose();
    }
  }

  /** Closes idle sessions opened with older settings. */
  reconfigured() {
    for (const [id, e] of this.chats) {
      if (!e.busy && e.gen !== this.cfg.setup.generation) {
        e.session.dispose();
        this.chats.delete(id);
      }
    }
  }

  forget(id: number) {
    const e = this.chats.get(id);
    if (e) {
      void e.session.abort().finally(() => e.session.dispose());
      this.chats.delete(id);
    }
    rmSync(this.chatDir(id), { recursive: true, force: true });
  }
}

function resultText(result: unknown): string {
  const r = result as { content?: { type: string; text?: string }[] } | undefined;
  const s = (r?.content ?? [])
    .filter((c) => c.type === "text")
    .map((c) => c.text ?? "")
    .join("\n");
  return s.length > 4000 ? s.slice(0, 4000) + "\n…" : s;
}

/** Prompts the session and translates pi events into srvmon events. */
export async function drive(session: AgentSession, text: string, emit: (e: Event) => void, signal: AbortSignal) {
  let lastError = "";
  const unsubscribe = session.subscribe((ev) => {
    switch (ev.type) {
      case "message_update":
        if (ev.assistantMessageEvent.type === "text_delta") emit({ t: "text", d: ev.assistantMessageEvent.delta });
        break;
      case "tool_execution_start":
        emit({ t: "tool", id: ev.toolCallId, name: ev.toolName, args: ev.args });
        break;
      case "tool_execution_end":
        emit({ t: "tool_end", id: ev.toolCallId, ok: !ev.isError, out: resultText(ev.result) });
        break;
      case "message_end": {
        const m = ev.message as { role?: string; stopReason?: string; errorMessage?: string };
        if (m.role === "assistant" && m.stopReason === "error") lastError = m.errorMessage || "model error";
        else if (m.role === "assistant") lastError = "";
        break;
      }
    }
  });
  const onAbort = () => void session.abort();
  signal.addEventListener("abort", onAbort, { once: true });
  try {
    // Stopped while the session was being created: abort events are not
    // replayed to the listener above, so do not start the prompt at all.
    if (signal.aborted) {
      emit({ t: "error", msg: "stopped" });
      return;
    }
    await session.prompt(text);
    if (signal.aborted) emit({ t: "error", msg: "stopped" });
    else if (lastError) emit({ t: "error", msg: lastError });
    else emit({ t: "done", text: session.getLastAssistantText() ?? "" });
  } catch (err) {
    emit({ t: "error", msg: err instanceof Error ? err.message : String(err) });
  } finally {
    signal.removeEventListener("abort", onAbort);
    unsubscribe();
  }
}
