import * as React from "react";
import { useCallback, useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  type AgentAction,
  agentApi,
  type AgentChat,
  type AgentMessage,
  type AgentStatus,
  type AgentTask,
  type AgentToolCall,
  runChat,
  toolLine,
} from "@/lib/agent";
import { ApiError } from "@/lib/api";
import { cn } from "@/lib/utils";

// Agent panel from the terminal mockup: chat / tasks / history. The agent
// runs in srvmon-ai; commands that change a server show up here as
// confirmation cards and run only after "✓ выполнить".

type Tab = "chat" | "tasks" | "history";

interface Live {
  text: string;
  tools: AgentToolCall[];
  error?: string;
}

const chatKey = (serverId?: number) => `srvmon.agent.chat.${serverId ?? "all"}`;

function loadChatId(serverId?: number): number | undefined {
  try {
    const v = Number(localStorage.getItem(chatKey(serverId)));
    return Number.isInteger(v) && v > 0 ? v : undefined;
  } catch {
    return undefined;
  }
}

function saveChatId(serverId: number | undefined, id: number | undefined) {
  try {
    if (id) localStorage.setItem(chatKey(serverId), String(id));
    else localStorage.removeItem(chatKey(serverId));
  } catch {
    // private mode: the chat is just not remembered
  }
}

// Minimal markdown: ``` blocks and `inline code`.
function Rich({ text }: { text: string }) {
  const parts = text.split(/```[a-z]*\n?/);
  return (
    <>
      {parts.map((p, i) =>
        i % 2 === 1 ? (
          <pre
            key={i}
            className="my-1 overflow-x-auto border border-border/60 bg-field px-2 py-1 text-xs text-hi [scrollbar-width:thin]"
          >
            {p.replace(/\n$/, "")}
          </pre>
        ) : (
          <span key={i} className="whitespace-pre-wrap">
            {p.split(/(`[^`\n]+`)/).map((s, j) =>
              s.startsWith("`") && s.endsWith("`") && s.length > 2 ? (
                <code key={j} className="bg-field px-1 text-hi">
                  {s.slice(1, -1)}
                </code>
              ) : (
                s
              ),
            )}
          </span>
        ),
      )}
    </>
  );
}

function ToolRow({ t }: { t: AgentToolCall }) {
  const [open, setOpen] = useState(false);
  const pending = t.done === false;
  return (
    <div className="mt-1 text-xs">
      <button
        type="button"
        className="flex w-full min-w-0 items-start gap-1.5 text-left text-muted-foreground hover:text-foreground"
        onClick={() => setOpen((o) => !o)}
        title="показать вывод"
      >
        <span className={pending ? "text-warn" : t.ok ? "text-primary" : "text-crit"}>
          {pending ? "…" : t.ok ? "✓" : "✗"}
        </span>
        <span className="min-w-0 break-all">{toolLine(t)}</span>
      </button>
      {open && t.output && (
        <pre className="mt-1 max-h-64 overflow-auto border border-border/60 bg-field px-2 py-1 text-[11px] text-foreground [scrollbar-width:thin]">
          {t.output}
        </pre>
      )}
    </div>
  );
}

function ActionCard({ a, onDone }: { a: AgentAction; onDone: () => void }) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const decide = async (d: "approve" | "reject") => {
    setBusy(true);
    setErr("");
    try {
      await agentApi.decide(a.id, d);
    } catch (e) {
      setErr(e instanceof ApiError && e.code === "already_decided" ? "уже решено" : "не удалось");
    }
    setBusy(false);
    onDone();
  };
  return (
    <div className="mt-2 border border-warn/70 bg-warn/5 p-2">
      <div className="text-xs text-warn">
        ⚠ подтвердите · {a.server}
        {a.task_id ? ` · задача #${a.task_id}` : ""}
      </div>
      <pre className="my-1 overflow-x-auto whitespace-pre-wrap break-all bg-field px-2 py-1 text-xs text-hi">
        {a.command}
      </pre>
      {a.reason && <div className="text-xs text-muted-foreground">{a.reason}</div>}
      <div className="mt-2 flex gap-2">
        <Button size="sm" disabled={busy} onClick={() => decide("approve")}>
          ✓ выполнить
        </Button>
        <Button size="sm" variant="destructive" disabled={busy} onClick={() => decide("reject")}>
          ✗ отклонить
        </Button>
        {err && <span className="text-xs text-crit">{err}</span>}
      </div>
    </div>
  );
}

function MessageView({ m }: { m: { role: string; text: string; tools: AgentToolCall[]; error?: string } }) {
  if (m.role === "user") {
    return (
      <div className="border-l-2 border-primary pl-2">
        <span className="text-primary">вы ›</span> <span className="whitespace-pre-wrap">{m.text}</span>
      </div>
    );
  }
  return (
    <div className="border-l-2 border-border pl-2">
      <span className="text-hi">агент ›</span>
      {m.tools.length > 0 && (
        <div className="mb-1">
          {m.tools.map((t) => (
            <ToolRow key={t.id} t={t} />
          ))}
        </div>
      )}{" "}
      {m.text ? <Rich text={m.text} /> : null}
      {m.error && <div className="mt-1 text-xs text-crit">✗ {m.error}</div>}
    </div>
  );
}

function fmtTime(s?: string) {
  if (!s) return "—";
  const d = new Date(s);
  return `${d.toISOString().slice(5, 10)} ${d.toISOString().slice(11, 16)}`;
}

function TasksTab({ contextServer, onChanged }: { contextServer?: string; onChanged: (n: number) => void }) {
  const [tasks, setTasks] = useState<AgentTask[] | null>(null);
  const [form, setForm] = useState(false);
  const [name, setName] = useState("");
  const [schedule, setSchedule] = useState("*/15 * * * *");
  const [prompt, setPrompt] = useState("");
  const [servers, setServers] = useState(contextServer ?? "");
  const [tg, setTg] = useState(true);
  const [err, setErr] = useState("");
  const [open, setOpen] = useState<number | null>(null);

  const load = useCallback(() => {
    agentApi
      .tasks()
      .then((t) => {
        setTasks(t);
        onChanged(t.length);
      })
      .catch(() => setTasks([]));
  }, [onChanged]);
  useEffect(() => {
    load();
    const t = setInterval(load, 10_000);
    return () => clearInterval(t);
  }, [load]);

  const fieldMsg: Record<string, string> = {
    name: "имя: 1–64 символа",
    schedule: "расписание: cron из 5 полей в UTC, не чаще раза в 5 минут, или @every 30m",
    prompt: "промпт обязателен",
    servers: "неизвестный сервер",
  };

  async function create(e: React.FormEvent) {
    e.preventDefault();
    setErr("");
    try {
      await agentApi.newTask({
        name,
        schedule,
        prompt,
        servers: servers
          .split(/[,\s]+/)
          .map((s) => s.trim())
          .filter(Boolean),
        notify: tg ? "telegram" : "",
      });
      setForm(false);
      setName("");
      setPrompt("");
      load();
    } catch (e) {
      setErr(e instanceof ApiError ? (fieldMsg[e.code] ?? e.code) : "ошибка");
    }
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2">
        <span className="flex-1 text-xs text-muted-foreground">
          агент запускается по расписанию (UTC); изменения на серверах — только после подтверждения
        </span>
        <Button size="sm" variant="outline" onClick={() => setForm((f) => !f)}>
          {form ? "отмена" : "+ задача"}
        </Button>
      </div>
      {form && (
        <form onSubmit={create} className="flex flex-col gap-2 border border-border p-2">
          <Input
            className="h-8"
            placeholder="имя, например restart-watch"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <Input
            className="h-8"
            placeholder="*/15 * * * *"
            value={schedule}
            onChange={(e) => setSchedule(e.target.value)}
          />
          <textarea
            className="min-h-20 w-full border border-input bg-field px-3 py-2 text-hi placeholder:text-muted-foreground"
            placeholder="что проверять и когда поднимать тревогу"
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
          />
          <Input
            className="h-8"
            placeholder="серверы через запятую, пусто = все"
            value={servers}
            onChange={(e) => setServers(e.target.value)}
          />
          <label className="flex items-center gap-2 text-xs">
            <input type="checkbox" checked={tg} onChange={(e) => setTg(e.target.checked)} /> в Telegram при ALERT или
            ошибке
          </label>
          {err && <div className="text-xs text-crit">{err}</div>}
          <Button size="sm" type="submit">
            создать
          </Button>
        </form>
      )}
      {tasks === null && <div className="text-muted-foreground">загрузка…</div>}
      {tasks?.length === 0 && !form && (
        <div className="text-muted-foreground">
          задач нет. Попросите агента: «каждые 15 минут проверяй рестарты контейнеров».
        </div>
      )}
      {tasks?.map((t) => (
        <div key={t.id} className={cn("border border-border p-2", !t.enabled && "opacity-60")}>
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-primary">#{t.id}</span>
            <span className="text-hi">{t.name}</span>
            <span className="text-xs text-muted-foreground">{t.schedule}</span>
            {t.notify === "telegram" && <span className="text-xs text-tx">tg</span>}
            <span className="flex-1" />
            <span
              className={cn(
                "text-xs",
                t.last_status === "ok" ? "text-primary" : t.last_status === "error" ? "text-crit" : "text-warn",
              )}
            >
              {t.last_status === "running" ? "выполняется…" : t.last_status || "не запускалась"}
            </span>
          </div>
          <div className="mt-1 text-xs text-muted-foreground">
            {t.servers.length ? t.servers.join(", ") : "все серверы"} · след. {fmtTime(t.next_run_at)} · посл.{" "}
            {fmtTime(t.last_run_at)}
          </div>
          {t.last_result && (
            <button
              type="button"
              className="mt-1 block w-full text-left text-xs"
              onClick={() => setOpen(open === t.id ? null : t.id)}
            >
              <span className={cn("whitespace-pre-wrap", open !== t.id && "line-clamp-2")}>{t.last_result}</span>
            </button>
          )}
          {open === t.id && (
            <div className="mt-1 text-xs text-muted-foreground whitespace-pre-wrap">промпт: {t.prompt}</div>
          )}
          <div className="mt-2 flex gap-2">
            <Button
              size="sm"
              variant="outline"
              disabled={t.last_status === "running"}
              onClick={() => agentApi.runTask(t.id).finally(load)}
            >
              ▶ запустить
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() => agentApi.updateTask(t.id, { enabled: !t.enabled }).finally(load)}
            >
              {t.enabled ? "выкл" : "вкл"}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => confirm(`Удалить задачу ${t.name}?`) && agentApi.deleteTask(t.id).finally(load)}
            >
              удалить
            </Button>
          </div>
        </div>
      ))}
    </div>
  );
}

function HistoryTab({ current, onOpen }: { current?: number; onOpen: (c: AgentChat) => void }) {
  const [chats, setChats] = useState<AgentChat[] | null>(null);
  const load = useCallback(() => {
    agentApi
      .chats()
      .then(setChats)
      .catch(() => setChats([]));
  }, []);
  useEffect(load, [load]);
  return (
    <div className="flex flex-col">
      {chats === null && <div className="text-muted-foreground">загрузка…</div>}
      {chats?.length === 0 && <div className="text-muted-foreground">чатов пока нет</div>}
      {chats?.map((c) => (
        <div
          key={c.id}
          className={cn(
            "flex items-center gap-2 border-t border-border/60 py-1.5 first:border-t-0",
            c.id === current && "text-primary",
          )}
        >
          <button
            type="button"
            className="min-w-0 flex-1 truncate text-left hover:text-primary"
            onClick={() => onOpen(c)}
          >
            {c.running ? "● " : ""}
            {c.title || "без названия"}
          </button>
          <span className="shrink-0 text-xs text-muted-foreground">
            {c.server ?? "все"} · {fmtTime(c.updated_at)}
          </span>
          <button
            type="button"
            className="shrink-0 text-xs text-muted-foreground hover:text-crit"
            aria-label="удалить чат"
            onClick={() => confirm("Удалить чат?") && agentApi.deleteChat(c.id).finally(load)}
          >
            ×
          </button>
        </div>
      ))}
    </div>
  );
}

export function AgentPanel({
  context,
  className,
  inputRef,
}: {
  context?: { id: number; name: string };
  className?: string;
  inputRef?: React.Ref<HTMLInputElement>;
}) {
  const [status, setStatus] = useState<AgentStatus | null>(null);
  const [tab, setTab] = useState<Tab>("chat");
  const [chatId, setChatId] = useState<number | undefined>(() => loadChatId(context?.id));
  const [chat, setChat] = useState<AgentChat | null>(null);
  const [messages, setMessages] = useState<AgentMessage[]>([]);
  const [live, setLive] = useState<Live | null>(null);
  const [pending, setPending] = useState<AgentAction[]>([]);
  const [text, setText] = useState("");
  const [taskCount, setTaskCount] = useState<number | null>(null);
  const [err, setErr] = useState("");
  const scroller = useRef<HTMLDivElement>(null);
  // A chat created by send() is already on screen: do not reload it.
  const created = useRef<number | undefined>(undefined);
  // The chat on screen. Callbacks of a run or a load for another chat
  // (the user switched while it was in flight) must not touch the view.
  const active = useRef<number | undefined>(chatId);
  active.current = chatId;
  const ctxId = context?.id;

  useEffect(() => {
    const load = () =>
      agentApi
        .status()
        .then(setStatus)
        .catch(() => setStatus({ enabled: false }));
    load();
    const t = setInterval(load, 30_000);
    return () => clearInterval(t);
  }, []);
  useEffect(() => {
    agentApi
      .tasks()
      .then((t) => setTaskCount(t.length))
      .catch(() => undefined);
  }, []);

  // Switching the server context switches to that server's chat.
  useEffect(() => {
    setChatId(loadChatId(ctxId));
  }, [ctxId]);

  const loadChat = useCallback(async (id: number) => {
    try {
      const r = await agentApi.chat(id);
      if (active.current !== id) return null;
      setChat(r.chat);
      setMessages(r.messages);
      return r.chat;
    } catch (e) {
      if (e instanceof ApiError && e.status === 404 && active.current === id) {
        setChat(null);
        setMessages([]);
        setChatId(undefined);
      }
      return null;
    }
  }, []);

  useEffect(() => {
    if (chatId && chatId === created.current) return;
    setLive(null);
    if (!chatId) {
      setChat(null);
      setMessages([]);
      return;
    }
    void loadChat(chatId);
  }, [chatId, loadChat]);

  // A reply running in another tab (or after a reload): poll until done.
  useEffect(() => {
    if (!chatId || !chat?.running || live) return;
    const t = setInterval(() => void loadChat(chatId), 3000);
    return () => clearInterval(t);
  }, [chatId, chat?.running, live, loadChat]);

  const loadPending = useCallback(() => {
    agentApi
      .actions("pending")
      .then(setPending)
      .catch(() => undefined);
  }, []);
  useEffect(() => {
    loadPending();
    const t = setInterval(() => document.visibilityState === "visible" && loadPending(), 2500);
    return () => clearInterval(t);
  }, [loadPending]);

  useEffect(() => {
    const el = scroller.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [messages, live, pending.length, tab]);

  async function send(e?: React.FormEvent) {
    e?.preventDefault();
    const msg = text.trim();
    if (!msg || live) return;
    setErr("");
    let id = chatId;
    try {
      if (!id) {
        const c = await agentApi.newChat(ctxId);
        id = c.id;
        created.current = id;
        active.current = id;
        setChat(c);
        saveChatId(ctxId, id);
        setChatId(id);
      }
    } catch {
      setErr("не удалось создать чат");
      return;
    }
    setText("");
    setMessages((m) => [...m, { id: -Date.now(), role: "user", text: msg, tools: [], ts: new Date().toISOString() }]);
    const cur: Live = { text: "", tools: [] };
    const runId = id;
    const show = () => active.current === runId && setLive({ ...cur });
    show();
    try {
      await runChat(id, msg, (ev) => {
        switch (ev.t) {
          case "text":
            cur.text += ev.d;
            break;
          case "tool":
            cur.tools = [...cur.tools, { id: ev.id, name: ev.name, args: ev.args, ok: false, done: false }];
            loadPending();
            break;
          case "tool_end":
            cur.tools = cur.tools.map((t) => (t.id === ev.id ? { ...t, ok: ev.ok, output: ev.out, done: true } : t));
            break;
          case "error":
            cur.error = ev.msg;
            break;
          case "done":
            if (ev.text) cur.text = ev.text;
            break;
        }
        show();
      });
    } catch (e) {
      if (active.current === runId) setErr(e instanceof ApiError && e.code === "busy" ? "агент ещё отвечает в этом чате" : "нет связи с агентом");
    }
    if (active.current === runId) {
      await loadChat(runId);
      if (active.current === runId) setLive(null);
    }
    loadPending();
  }

  function newChat() {
    saveChatId(ctxId, undefined);
    setChatId(undefined);
    setTab("chat");
  }

  const chatPending = pending.filter((a) => a.chat_id && a.chat_id === chatId);
  const otherPending = pending.filter((a) => !a.chat_id || a.chat_id !== chatId);
  const disabled = status !== null && (!status.enabled || status.ready === false);
  const model = status?.model ? `pi · ${status.model}` : "pi";
  const ctxName = chat?.server ?? context?.name ?? "все";

  return (
    <aside className={cn("relative flex min-h-0 flex-col border border-border px-3 pt-4 pb-3", className)}>
      <h2 className="absolute -top-2.5 left-3 max-w-[calc(100%-1.5rem)] truncate bg-background px-1.5 text-sm leading-5 text-primary">
        агент ── {model} · контекст: {ctxName}
      </h2>
      <div className="flex flex-wrap items-center gap-1.5">
        <Button size="sm" variant={tab === "chat" ? "default" : "outline"} onClick={() => setTab("chat")}>
          чат
        </Button>
        <Button size="sm" variant={tab === "tasks" ? "default" : "outline"} onClick={() => setTab("tasks")}>
          задачи{taskCount !== null ? ` (${taskCount})` : ""}
        </Button>
        <Button size="sm" variant={tab === "history" ? "default" : "outline"} onClick={() => setTab("history")}>
          история
        </Button>
        <span className="flex-1" />
        {pending.length > 0 && <span className="text-xs text-warn">⚠ {pending.length}</span>}
        <Button size="sm" variant="ghost" onClick={newChat} title="новый чат">
          + чат
        </Button>
      </div>

      {status && !status.enabled && (
        <div className="mt-3 text-xs text-muted-foreground">
          Агент выключен: нет контейнера srvmon-ai (SRVMON_AI_URL / SRVMON_AI_TOKEN).
        </div>
      )}
      {status?.enabled && status.ready === false && (
        <div className="mt-3 text-xs text-warn">агент не готов: {status.error ?? "srvmon-ai недоступен"}</div>
      )}

      <div ref={scroller} className="mt-3 min-h-0 flex-1 overflow-y-auto pr-1 [scrollbar-width:thin]">
        {tab === "chat" && (
          <div className="flex flex-col gap-3">
            {otherPending.map((a) => (
              <ActionCard key={a.id} a={a} onDone={loadPending} />
            ))}
            {messages.length === 0 && !live && (
              <div className="text-xs text-muted-foreground">
                Спросите о серверах или поставьте задачу. Агент читает метрики, логи и вывод команд сам; всё, что меняет
                сервер, он предложит, а выполнится оно только после вашего «✓ выполнить».
              </div>
            )}
            {messages.map((m) => (
              <MessageView key={m.id} m={m} />
            ))}
            {live && (
              <MessageView m={{ role: "assistant", ...live, text: live.text || (live.tools.length ? "" : "…") }} />
            )}
            {chat?.running && !live && <div className="text-xs text-warn">агент отвечает…</div>}
            {chatPending.map((a) => (
              <ActionCard key={a.id} a={a} onDone={loadPending} />
            ))}
          </div>
        )}
        {tab === "tasks" && <TasksTab contextServer={context?.name} onChanged={setTaskCount} />}
        {tab === "history" && (
          <HistoryTab
            current={chatId}
            onOpen={(c) => {
              setChatId(c.id);
              setTab("chat");
            }}
          />
        )}
      </div>

      {tab === "chat" && (
        <form onSubmit={send} className="mt-3 flex items-center gap-2">
          <label htmlFor="agent-ask" className="text-muted-foreground">
            ›
          </label>
          <Input
            id="agent-ask"
            ref={inputRef}
            className="h-10 min-w-0 flex-1"
            placeholder="спросить агента или поставить задачу…"
            value={text}
            maxLength={4000}
            disabled={disabled}
            onChange={(e) => setText(e.target.value)}
          />
          {live && chatId ? (
            <Button
              type="button"
              variant="destructive"
              className="h-10"
              onClick={() => void agentApi.abort(chatId)}
              aria-label="Остановить"
            >
              ■
            </Button>
          ) : (
            <Button type="submit" className="h-10" disabled={disabled || !text.trim()} aria-label="Отправить">
              ⏎
            </Button>
          )}
        </form>
      )}
      {err && <div className="mt-1 text-xs text-crit">{err}</div>}
    </aside>
  );
}
