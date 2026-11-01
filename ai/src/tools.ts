// srvmon tools for the agent. They call the srvmon-server tool API; the
// agent has no other way to reach servers (no SSH keys, no shell of its
// own). Whether a command needs the owner's confirmation is decided by
// srvmon-server, not here.
import { defineTool, type ToolDefinition } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";

export interface ToolContext {
  toolsUrl: string;
  token: string;
  chatId?: number;
  taskId?: number;
  /** A read-only diagnosis of this alert: no task tools, srvmon refuses changing commands. */
  diagnoseId?: number;
}

const MAX_RESULT = 24_000;

export class ToolApiError extends Error {}

async function call(ctx: ToolContext, method: string, path: string, body: unknown, signal?: AbortSignal): Promise<unknown> {
  const res = await fetch(ctx.toolsUrl.replace(/\/$/, "") + path, {
    method,
    headers: {
      Authorization: `Bearer ${ctx.token}`,
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
    signal,
  });
  const text = await res.text();
  let data: unknown = text;
  try {
    data = JSON.parse(text);
  } catch {
    // keep text
  }
  if (!res.ok) {
    const d = data as { error?: string; hint?: string; field?: string };
    const msg = [d?.error ?? `HTTP ${res.status}`, d?.field && `field ${d.field}`, d?.hint].filter(Boolean).join(": ");
    throw new ToolApiError(msg);
  }
  return data;
}

function text(v: unknown) {
  let s = typeof v === "string" ? v : JSON.stringify(v, null, 1);
  if (s.length > MAX_RESULT) s = s.slice(0, MAX_RESULT) + "\n…(truncated)";
  return { content: [{ type: "text" as const, text: s }], details: {} };
}

interface ExecResult {
  status: "ok" | "error" | "rejected" | "expired" | "refused";
  read_only: boolean;
  exit_code?: number;
  output?: string;
  action_id?: number;
  note?: string;
}

export function formatExec(r: ExecResult): string {
  const head: string[] = [];
  switch (r.status) {
    case "rejected":
      return `The owner REJECTED the command (action #${r.action_id}). Do not repeat it without a new request; offer an alternative or ask.`;
    case "expired":
      return `Nobody confirmed the command in time (action #${r.action_id}); it did NOT run.`;
    case "refused":
      return `The command did NOT run: ${r.note ?? "refused by srvmon"}.`;
  }
  head.push(r.read_only ? "ran at once (read-only)" : `ran after confirmation (action #${r.action_id})`);
  if (r.exit_code !== undefined) head.push(`exit code ${r.exit_code}`);
  if (r.note) head.push(r.note);
  return `[${head.join(", ")}]\n${r.output ?? ""}`;
}

export function srvmonTools(ctx: ToolContext): ToolDefinition[] {
  const servers = defineTool({
    name: "srvmon_servers",
    label: "servers",
    description:
      "The srvmon servers with current metrics: online, CPU, memory, the fullest disk, containers, whether there is ssh access.",
    promptSnippet: "srvmon_servers: the servers and their state",
    parameters: Type.Object({}),
    async execute(_id, _p, signal) {
      return text(await call(ctx, "GET", "/tools/servers", undefined, signal));
    },
  });

  const server = defineTool({
    name: "srvmon_server",
    label: "server",
    description:
      "Details of one server: metrics, disks, all docker containers with status and health, OS, recent warnings from the logs.",
    promptSnippet: "srvmon_server: details of a server and its containers",
    parameters: Type.Object({ server: Type.String({ description: "server name from srvmon_servers" }) }),
    async execute(_id, p, signal) {
      return text(await call(ctx, "GET", `/tools/servers/${encodeURIComponent(p.server)}`, undefined, signal));
    },
  });

  const exec = defineTool({
    name: "srvmon_exec",
    label: "run",
    description:
      "Run a shell command on a server through the srvmon SSH gateway (user srvmon, docker group). " +
      "Read-only commands (docker ps/logs/inspect, df, free, journalctl, systemctl status, cat, grep, pipelines with | and so on) run at once. " +
      "Any other command (restart, up -d, rm, sudo, writing files, ; && > and so on) waits for the owner's confirmation in the panel, up to 15 minutes, and runs only after it. " +
      "Unquoted name patterns (* ? [) also need confirmation: to read, use find -name '*.log' or exact paths. " +
      "Timeout: 60 s for reads, 10 minutes for confirmed commands. Output is cut to 32 KB.",
    promptSnippet: "srvmon_exec: a command on a server (changes only after the owner confirms)",
    promptGuidelines: [
      "srvmon_exec: before a changing command, briefly explain in reason what it changes and why (in the language of the conversation): the owner sees it next to the confirm button.",
      "srvmon_exec: one changing command = one confirmation; do not glue unrelated changes together or hide them in pipelines.",
      "srvmon_exec: do not try to get around the confirmation (eval, base64, scripts in files and so on).",
    ],
    parameters: Type.Object({
      server: Type.String({ description: "server name" }),
      command: Type.String({ description: "command for sh -c" }),
      reason: Type.Optional(Type.String({ description: "why the command is needed (shown to the owner for confirmation)" })),
    }),
    async execute(_id, p, signal) {
      const r = (await call(
        ctx,
        "POST",
        "/tools/exec",
        {
          server: p.server,
          command: p.command,
          reason: p.reason ?? "",
          chat_id: ctx.chatId ?? 0,
          task_id: ctx.taskId ?? 0,
          diagnose_id: ctx.diagnoseId ?? 0,
        },
        signal,
      )) as ExecResult;
      const out = text(formatExec(r));
      return r.status === "ok" ? out : { ...out, isError: true };
    },
  });

  const tasks = defineTool({
    name: "srvmon_tasks",
    label: "tasks",
    description: "The agent's scheduled tasks: id, name, schedule (cron UTC), prompt, servers, notification, last result.",
    promptSnippet: "srvmon_tasks: scheduled tasks",
    parameters: Type.Object({}),
    async execute(_id, _p, signal) {
      return text(await call(ctx, "GET", "/tools/tasks", undefined, signal));
    },
  });

  const taskCreate = defineTool({
    name: "srvmon_task_create",
    label: "new task",
    description:
      "Create a monitoring task: srvmon runs the agent with this prompt on a schedule. " +
      "schedule: a 5-field cron in UTC (at most every 5 minutes, for example '*/15 * * * *') or '@every 30m'. " +
      "notify='telegram': an error or a reply starting with ALERT sends a Telegram notification.",
    promptSnippet: "srvmon_task_create: create a scheduled task",
    parameters: Type.Object({
      name: Type.String({ description: "a short name, for example restart-watch" }),
      schedule: Type.String(),
      prompt: Type.String({ description: "what to check and when to raise an alarm (a reply with ALERT)" }),
      servers: Type.Optional(Type.Array(Type.String(), { description: "server names; empty = all" })),
      notify: Type.Optional(Type.Union([Type.Literal(""), Type.Literal("telegram")])),
    }),
    async execute(_id, p, signal) {
      return text(
        await call(
          ctx,
          "POST",
          "/tools/tasks",
          { name: p.name, schedule: p.schedule, prompt: p.prompt, servers: p.servers ?? [], notify: p.notify ?? "" },
          signal,
        ),
      );
    },
  });

  const taskDelete = defineTool({
    name: "srvmon_task_delete",
    label: "delete task",
    description: "Delete a task by id.",
    parameters: Type.Object({ id: Type.Integer() }),
    async execute(_id, p, signal) {
      return text(await call(ctx, "DELETE", `/tools/tasks/${p.id}`, undefined, signal));
    },
  });

  if (ctx.diagnoseId) return [servers, server, exec] as ToolDefinition[];
  return [servers, server, exec, tasks, taskCreate, taskDelete] as ToolDefinition[];
}

export const toolNames = ["srvmon_servers", "srvmon_server", "srvmon_exec", "srvmon_tasks", "srvmon_task_create", "srvmon_task_delete"];

/** The tools of a read-only diagnosis run. */
export const diagnoseToolNames = ["srvmon_servers", "srvmon_server", "srvmon_exec"];
