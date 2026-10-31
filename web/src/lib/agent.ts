import { ApiError } from "@/lib/api";

// AI agent API (stage 5). Chats stream as NDJSON from POST .../run.

export interface AgentStatus {
  enabled: boolean;
  ready?: boolean;
  provider?: string;
  model?: string;
  error?: string;
  pending?: number;
}

export interface AgentChat {
  id: number;
  server_id?: number;
  server?: string;
  title: string;
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
  const data = await res.json().catch(() => ({}));
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
export function toolLine(t: Pick<AgentToolCall, "name" | "args">): string {
  const a = t.args ?? {};
  const s = (k: string) => (typeof a[k] === "string" ? (a[k] as string) : "");
  switch (t.name) {
    case "srvmon_exec":
      return `${s("server")} $ ${s("command")}`;
    case "srvmon_servers":
      return "серверы";
    case "srvmon_server":
      return `сервер ${s("server")}`;
    case "srvmon_tasks":
      return "задачи";
    case "srvmon_task_create":
      return `новая задача ${s("name")} · ${s("schedule")}`;
    case "srvmon_task_delete":
      return `удалить задачу #${String(a.id ?? "")}`;
  }
  return t.name;
}
