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

import { srvmonTools, toolNames } from "./tools.js";

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
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  model: any;
  thinking: "off" | "minimal" | "low" | "medium" | "high";
}

export function systemPrompt(server?: string, task = false): string {
  const lines = [
    "Ты — AI-агент панели мониторинга srvmon. Владелец следит через неё за своими серверами: метрики, docker-контейнеры, логи.",
    "Отвечай по-русски, коротко и по делу, как опытный SRE. Команды и пути пиши в `коде`.",
    "",
    "Доступ к серверам только через инструменты srvmon:",
    "- srvmon_servers / srvmon_server — состояние и контейнеры по данным агентов srvmon (начинай с них, это дёшево);",
    "- srvmon_exec — команда на сервере по SSH. Чтение выполняется сразу. Всё, что меняет сервер, ждёт подтверждения владельца в панели; объясняй такие команды в reason;",
    "- srvmon_tasks / srvmon_task_create / srvmon_task_delete — задачи мониторинга по расписанию (cron в UTC).",
    "",
    "Правила:",
    "- Сначала разберись (логи, inspect, события docker), потом предлагай изменение и проси подтверждение одной понятной командой.",
    "- Если владелец отклонил команду, не повторяй её; предложи другой путь.",
    "- Не выдумывай вывод команд и состояние серверов: если не проверял — скажи.",
    "- Секреты (токены, пароли, ключи) из вывода не пересказывай целиком.",
    "- Когда просят «следить», «проверять каждые N минут», «присылать в телеграм» — создай задачу srvmon_task_create; в её промпте опиши, что проверять и в каком случае ответить строкой, начинающейся с ALERT.",
  ];
  if (task) {
    lines.push(
      "",
      "Сейчас запуск задачи по расписанию, владелец не в чате. Изменяющие команды всё равно ждут его подтверждения; не жди их без нужды.",
      "Итог: 1–5 строк. Если нужно внимание владельца — первая строка начинается с ALERT.",
    );
  }
  if (server) {
    lines.push("", `Контекст чата: сервер «${server}». Если вопрос не уточняет сервер, речь о нём.`);
  }
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

  private async create(sessionManager: SessionManager, prompt: string, chatId?: number, taskId?: number) {
    const cwd = this.cfg.dataDir;
    const { session } = await createAgentSession({
      cwd,
      agentDir: join(this.cfg.dataDir, "pi"),
      model: this.cfg.model,
      thinkingLevel: this.cfg.thinking,
      modelRuntime: this.cfg.modelRuntime,
      resourceLoader: loader(prompt),
      tools: toolNames,
      customTools: srvmonTools({ toolsUrl: this.cfg.toolsUrl, token: this.cfg.token, chatId, taskId }),
      sessionManager,
      settingsManager: SettingsManager.inMemory({
        compaction: { enabled: true },
        retry: { enabled: true, maxRetries: 2 },
      }),
    });
    return session;
  }

  private async chat(id: number, server?: string): Promise<Entry> {
    const cur = this.chats.get(id);
    if (cur && cur.server === server) return cur;
    if (cur) {
      // The chat's server changed: reopen with the new system prompt.
      cur.session.dispose();
      this.chats.delete(id);
    }
    const dir = this.chatDir(id);
    mkdirSync(dir, { recursive: true });
    const session = await this.create(SessionManager.continueRecent(this.cfg.dataDir, dir), systemPrompt(server), id);
    const e: Entry = { session, server, busy: false, used: Date.now() };
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
  async runChat(id: number, text: string, server: string | undefined, emit: (e: Event) => void, signal: AbortSignal) {
    if (this.isBusy(id)) return false;
    const e = await this.chat(id, server);
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
  async runTask(taskId: number, text: string, emit: (e: Event) => void, signal: AbortSignal) {
    const session = await this.create(SessionManager.inMemory(this.cfg.dataDir), systemPrompt(undefined, true), undefined, taskId);
    try {
      await drive(session, text, emit, signal);
    } finally {
      session.dispose();
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
        if (m.role === "assistant" && m.stopReason === "error") lastError = m.errorMessage || "ошибка модели";
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
      emit({ t: "error", msg: "остановлено" });
      return;
    }
    await session.prompt(text);
    if (signal.aborted) emit({ t: "error", msg: "остановлено" });
    else if (lastError) emit({ t: "error", msg: lastError });
    else emit({ t: "done", text: session.getLastAssistantText() ?? "" });
  } catch (err) {
    emit({ t: "error", msg: err instanceof Error ? err.message : String(err) });
  } finally {
    signal.removeEventListener("abort", onAbort);
    unsubscribe();
  }
}
