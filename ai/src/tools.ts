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
    const msg = [d?.error ?? `HTTP ${res.status}`, d?.field && `поле ${d.field}`, d?.hint].filter(Boolean).join(": ");
    throw new ToolApiError(msg);
  }
  return data;
}

function text(v: unknown) {
  let s = typeof v === "string" ? v : JSON.stringify(v, null, 1);
  if (s.length > MAX_RESULT) s = s.slice(0, MAX_RESULT) + "\n…(обрезано)";
  return { content: [{ type: "text" as const, text: s }], details: {} };
}

interface ExecResult {
  status: "ok" | "error" | "rejected" | "expired";
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
      return `Владелец ОТКЛОНИЛ команду (действие #${r.action_id}). Не повторяй её без нового запроса; предложи альтернативу или спроси.`;
    case "expired":
      return `Команду никто не подтвердил вовремя (действие #${r.action_id}), она НЕ выполнена.`;
  }
  head.push(r.read_only ? "выполнено сразу (только чтение)" : `выполнено после подтверждения (действие #${r.action_id})`);
  if (r.exit_code !== undefined) head.push(`код выхода ${r.exit_code}`);
  if (r.note) head.push(r.note);
  return `[${head.join(", ")}]\n${r.output ?? ""}`;
}

export function srvmonTools(ctx: ToolContext): ToolDefinition[] {
  const servers = defineTool({
    name: "srvmon_servers",
    label: "серверы",
    description:
      "Список серверов srvmon с текущими метриками: онлайн, CPU, память, максимальная занятость диска, контейнеры, есть ли ssh-доступ.",
    promptSnippet: "srvmon_servers: список серверов и их состояние",
    parameters: Type.Object({}),
    async execute(_id, _p, signal) {
      return text(await call(ctx, "GET", "/tools/servers", undefined, signal));
    },
  });

  const server = defineTool({
    name: "srvmon_server",
    label: "сервер",
    description:
      "Подробности одного сервера: метрики, диски, все docker-контейнеры со статусом и health, ОС, последние предупреждения из логов.",
    promptSnippet: "srvmon_server: подробности сервера и его контейнеров",
    parameters: Type.Object({ server: Type.String({ description: "имя сервера из srvmon_servers" }) }),
    async execute(_id, p, signal) {
      return text(await call(ctx, "GET", `/tools/servers/${encodeURIComponent(p.server)}`, undefined, signal));
    },
  });

  const exec = defineTool({
    name: "srvmon_exec",
    label: "выполнить",
    description:
      "Выполнить shell-команду на сервере через SSH-шлюз srvmon (пользователь srvmon, группа docker). " +
      "Команды только для чтения (docker ps/logs/inspect, df, free, journalctl, systemctl status, cat, grep, конвейеры через | и т.п.) выполняются сразу. " +
      "Любая другая команда (restart, up -d, rm, sudo, запись в файлы, ; && > и т.п.) ждёт подтверждения владельца в панели — до 15 минут — и выполняется только после него. " +
      "Шаблоны имён (* ? [) без кавычек тоже требуют подтверждения: для чтения используй find -name '*.log' или конкретные пути. " +
      "Таймаут: 60 с для чтения, 10 минут для подтверждённых команд. Вывод обрезается до 32 КБ.",
    promptSnippet: "srvmon_exec: команда на сервере (изменения — после подтверждения владельца)",
    promptGuidelines: [
      "srvmon_exec: перед изменяющей командой коротко объясни в reason, что и зачем она меняет — владелец видит это рядом с кнопкой подтверждения.",
      "srvmon_exec: одна изменяющая команда = одно подтверждение; не склеивай несвязанные изменения и не прячь их в конвейеры.",
      "srvmon_exec: не пытайся обойти подтверждение (eval, base64, скрипты в файлах и т.п.).",
    ],
    parameters: Type.Object({
      server: Type.String({ description: "имя сервера" }),
      command: Type.String({ description: "команда для sh -c" }),
      reason: Type.Optional(Type.String({ description: "зачем нужна команда (для подтверждения владельцем)" })),
    }),
    async execute(_id, p, signal) {
      const r = (await call(
        ctx,
        "POST",
        "/tools/exec",
        { server: p.server, command: p.command, reason: p.reason ?? "", chat_id: ctx.chatId ?? 0, task_id: ctx.taskId ?? 0 },
        signal,
      )) as ExecResult;
      const out = text(formatExec(r));
      return r.status === "ok" ? out : { ...out, isError: true };
    },
  });

  const tasks = defineTool({
    name: "srvmon_tasks",
    label: "задачи",
    description: "Список задач агента по расписанию: id, имя, расписание (cron UTC), промпт, серверы, уведомление, последний результат.",
    promptSnippet: "srvmon_tasks: задачи по расписанию",
    parameters: Type.Object({}),
    async execute(_id, _p, signal) {
      return text(await call(ctx, "GET", "/tools/tasks", undefined, signal));
    },
  });

  const taskCreate = defineTool({
    name: "srvmon_task_create",
    label: "новая задача",
    description:
      "Создать задачу мониторинга: srvmon будет по расписанию запускать агента с этим промптом. " +
      "schedule — cron из 5 полей в UTC (не чаще раза в 5 минут, например '*/15 * * * *') или '@every 30m'. " +
      "notify='telegram' — при ошибке или ответе, начинающемся с ALERT, придёт уведомление в Telegram.",
    promptSnippet: "srvmon_task_create: создать задачу по расписанию",
    parameters: Type.Object({
      name: Type.String({ description: "короткое имя, например restart-watch" }),
      schedule: Type.String(),
      prompt: Type.String({ description: "что проверять и когда поднимать тревогу (ответ с ALERT)" }),
      servers: Type.Optional(Type.Array(Type.String(), { description: "имена серверов; пусто = все" })),
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
    label: "удалить задачу",
    description: "Удалить задачу по id.",
    parameters: Type.Object({ id: Type.Integer() }),
    async execute(_id, p, signal) {
      return text(await call(ctx, "DELETE", `/tools/tasks/${p.id}`, undefined, signal));
    },
  });

  return [servers, server, exec, tasks, taskCreate, taskDelete] as ToolDefinition[];
}

export const toolNames = ["srvmon_servers", "srvmon_server", "srvmon_exec", "srvmon_tasks", "srvmon_task_create", "srvmon_task_delete"];
