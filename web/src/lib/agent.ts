import { ApiError, readBody } from "@/lib/api";
import { t } from "@/lib/i18n";

// AI agent API (stage 5). Chats stream as NDJSON from POST .../run.

export interface AgentStatus {
  enabled: boolean;
  ready?: boolean;
  provider?: string;
  model?: string;
  error?: string;
  pending?: number;
  models?: string[]; // picked in settings; a chat may switch between them
}

export interface AIStatus {
  ready: boolean;
  provider: string;
  model: string;
  error?: string;
}

export interface AISettings {
  saved: boolean;
  provider: string;
  base_url?: string;
  api?: string;
  model: string;
  thinking: string;
  models: string[];
  updated_at?: string;
  key_set: boolean;
  key_hint?: string;
  keys: string[];
  status?: AIStatus;
  error?: string;
}

export interface AISettingsInput {
  provider: string;
  base_url: string;
  api: string;
  model: string;
  thinking: string;
  models: string[];
  api_key?: string; // absent: keep, "": remove
}

export interface AIProvider {
  id: string;
  name: string;
  base_url?: string;
  models: number;
}

export interface AIModel {
  id: string;
  name?: string;
  catalog: boolean;
  live: boolean;
  reasoning?: boolean;
  context?: number;
}

export interface AIModelList {
  models: AIModel[];
  live: boolean;
  error?: string;
}

export interface AITest {
  ok: boolean;
  model?: string;
  text?: string;
  ms?: number;
  error?: string;
}

export interface AgentChat {
  id: number;
  server_id?: number;
  server?: string;
  title: string;
  model: string; // "" = default
  created_at: string;
  updated_at: string;
  running: boolean;
}

export interface AgentToolCall {
  id: string;
  name: string;
  args?: Record<string, unknown>;
  ok: boolean;
  output?: string;
  done?: boolean; // live only
}

export interface AgentMessage {
  id: number;
  role: "user" | "assistant";
  text: string;
  tools: AgentToolCall[];
  error?: string;
  ts: string;
}

export interface AgentAction {
  id: number;
  chat_id?: number;
  task_id?: number;
  server_id: number;
  server: string;
  command: string;
  reason: string;
  state: "pending" | "approved" | "rejected" | "expired" | "done" | "failed";
  created_at: string;
  decided_at?: string;
  decided_by?: string;
  exit_code?: number;
  output?: string;
}

export interface AgentTask {
  id: number;
  name: string;
  schedule: string;
  prompt: string;
  servers: string[];
  notify: "" | "telegram";
  enabled: boolean;
  created_at: string;
  next_run_at?: string;
  last_run_at?: string;
  last_status: "" | "ok" | "error" | "running";
  last_result: string;
}

export type TaskInput = Partial<Pick<AgentTask, "name" | "schedule" | "prompt" | "servers" | "notify" | "enabled">>;

export type AgentEvent =
  | { t: "start" }
  | { t: "text"; d: string }
  | { t: "tool"; id: string; name: string; args?: Record<string, unknown> }
  | { t: "tool_end"; id: string; ok: boolean; out?: string }
  | { t: "error"; msg: string }
  | { t: "done"; text: string };

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const data = await readBody(res);
  if (!res.ok)
    throw new ApiError(
      res.status,
      (data as { error?: string; field?: string }).field ?? (data as { error?: string }).error ?? "error",
    );
  return data as T;
}

export const agentApi = {
  status: () => req<AgentStatus>("GET", "/api/agent/status"),
  chats: () => req<AgentChat[]>("GET", "/api/agent/chats"),
  newChat: (serverId?: number) => req<AgentChat>("POST", "/api/agent/chats", serverId ? { server_id: serverId } : {}),
  chat: (id: number) => req<{ chat: AgentChat; messages: AgentMessage[] }>("GET", `/api/agent/chats/${id}`),
  deleteChat: (id: number) => req<unknown>("DELETE", `/api/agent/chats/${id}`),
  abort: (id: number) => req<unknown>("POST", `/api/agent/chats/${id}/abort`),
  actions: (state?: AgentAction["state"]) =>
    req<AgentAction[]>("GET", `/api/agent/actions${state ? `?state=${state}` : ""}`),
  decide: (id: number, decision: "approve" | "reject") =>
    req<AgentAction>("POST", `/api/agent/actions/${id}/${decision}`),
  tasks: () => req<AgentTask[]>("GET", "/api/agent/tasks"),
  newTask: (t: TaskInput) => req<AgentTask>("POST", "/api/agent/tasks", t),
  updateTask: (id: number, t: TaskInput) => req<AgentTask>("PATCH", `/api/agent/tasks/${id}`, t),
  deleteTask: (id: number) => req<unknown>("DELETE", `/api/agent/tasks/${id}`),
  runTask: (id: number) => req<unknown>("POST", `/api/agent/tasks/${id}/run`),
  setChatModel: (id: number, model: string) => req<AgentChat>("PUT", `/api/agent/chats/${id}/model`, { model }),
  settings: () => req<AISettings>("GET", "/api/agent/settings"),
  saveSettings: (s: AISettingsInput) => req<AISettings>("PUT", "/api/agent/settings", s),
  providers: () => req<AIProvider[]>("GET", "/api/agent/providers"),
  models: (s: Pick<AISettingsInput, "provider" | "base_url" | "api" | "api_key">) =>
    req<AIModelList>("POST", "/api/agent/models", s),
  test: (model?: string) => req<AITest>("POST", "/api/agent/test", { model: model ?? "" }),
};

/** Sends a message and calls onEvent for each streamed event. */
export async function runChat(chatId: number, text: string, onEvent: (e: AgentEvent) => void): Promise<void> {
  const res = await fetch(`/api/agent/chats/${chatId}/run`, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ text }),
  });
  if (!res.ok || !res.body) {
    const data = await res.json().catch(() => ({}));
    throw new ApiError(res.status, (data as { error?: string }).error ?? "error");
  }
  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = "";
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    let nl: number;
    while ((nl = buf.indexOf("\n")) >= 0) {
      const line = buf.slice(0, nl).trim();
      buf = buf.slice(nl + 1);
      if (!line) continue;
      try {
        onEvent(JSON.parse(line) as AgentEvent);
      } catch {
        // skip a malformed line
      }
    }
  }
}

/** One-line summary of a tool call for the transcript. */
export function toolLine(call: Pick<AgentToolCall, "name" | "args">): string {
  const a = call.args ?? {};
  const s = (k: string) => (typeof a[k] === "string" ? (a[k] as string) : "");
  switch (call.name) {
    case "srvmon_exec":
      return `${s("server")} $ ${s("command")}`;
    case "srvmon_servers":
      return t("servers");
    case "srvmon_server":
      return t("server {name}", { name: s("server") });
    case "srvmon_tasks":
      return t("tasks");
    case "srvmon_task_create":
      return t("new task {name} · {schedule}", { name: s("name"), schedule: s("schedule") });
    case "srvmon_task_delete":
      return t("delete task #{id}", { id: String(a.id ?? "") });
  }
  return call.name;
}
