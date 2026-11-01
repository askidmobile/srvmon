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
import { t } from "@/lib/i18n";
import { href } from "@/lib/router";
import { cn } from "@/lib/utils";

// Agent panel from the terminal mockup: chat / tasks / history. The agent
// runs in srvmon-ai; commands that change a server show up here as
// confirmation cards and run only after "✓ run".

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

function ToolRow({ t: call }: { t: AgentToolCall }) {
  const [open, setOpen] = useState(false);
  const pending = call.done === false;
  return (
    <div className="mt-1 text-xs">
      <button
        type="button"
        className="flex w-full min-w-0 items-start gap-1.5 text-left text-muted-foreground hover:text-foreground"
        onClick={() => setOpen((o) => !o)}
        title={t("show the output")}
      >
        <span className={pending ? "text-warn" : call.ok ? "text-primary" : "text-crit"}>
          {pending ? "…" : call.ok ? "✓" : "✗"}
        </span>
        <span className="min-w-0 break-all">{toolLine(call)}</span>
      </button>
      {open && call.output && (
        <pre className="mt-1 max-h-64 overflow-auto border border-border/60 bg-field px-2 py-1 text-[11px] text-foreground [scrollbar-width:thin]">
          {call.output}
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
      setErr(e instanceof ApiError && e.code === "already_decided" ? t("already decided") : t("failed"));
    }
    setBusy(false);
    onDone();
  };
  return (
    <div className="mt-2 border border-warn/70 bg-warn/5 p-2">
      <div className="text-xs text-warn">
        ⚠ {t("confirm")} · {a.server}
        {a.task_id ? ` · ${t("task #{id}", { id: a.task_id })}` : ""}
        {a.source?.startsWith("mcp:") ? ` · MCP ${a.source.slice(4)}` : ""}
      </div>
      <pre className="my-1 overflow-x-auto whitespace-pre-wrap break-all bg-field px-2 py-1 text-xs text-hi">
        {a.command}
      </pre>
      {a.reason && <div className="text-xs text-muted-foreground">{a.reason}</div>}
      <div className="mt-2 flex gap-2">
        <Button size="sm" disabled={busy} onClick={() => decide("approve")}>
          ✓ {t("run")}
        </Button>
        <Button size="sm" variant="destructive" disabled={busy} onClick={() => decide("reject")}>
          ✗ {t("reject")}
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
        <span className="text-primary">{t("you")} ›</span> <span className="whitespace-pre-wrap">{m.text}</span>
      </div>
    );
  }
  return (
    <div className="border-l-2 border-border pl-2">
      <span className="text-hi">{t("agent")} ›</span>
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
    name: t("name: 1–64 characters"),
    schedule: t("schedule: 5-field cron in UTC, at most every 5 minutes, or @every 30m"),
    prompt: t("a prompt is required"),
    servers: t("unknown server"),
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
      setErr(e instanceof ApiError ? (fieldMsg[e.code] ?? e.code) : t("error"));
    }
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2">
        <span className="flex-1 text-xs text-muted-foreground">
          {t("the agent runs on a schedule (UTC); changes to servers only after confirmation")}
        </span>
        <Button size="sm" variant="outline" onClick={() => setForm((f) => !f)}>
          {form ? t("cancel") : `+ ${t("task")}`}
        </Button>
      </div>
      {form && (
        <form onSubmit={create} className="flex flex-col gap-2 border border-border p-2">
          <Input
            className="h-8"
            placeholder={t("name, for example restart-watch")}
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
            placeholder={t("what to check and when to raise an alarm")}
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
          />
          <Input
            className="h-8"
            placeholder={t("servers, comma-separated; empty = all")}
            value={servers}
            onChange={(e) => setServers(e.target.value)}
          />
          <label className="flex items-center gap-2 text-xs">
            <input type="checkbox" checked={tg} onChange={(e) => setTg(e.target.checked)} />{" "}
            {t("to Telegram on ALERT or error")}
          </label>
          {err && <div className="text-xs text-crit">{err}</div>}
          <Button size="sm" type="submit">
            {t("create")}
          </Button>
        </form>
      )}
      {tasks === null && <div className="text-muted-foreground">{t("loading…")}</div>}
      {tasks?.length === 0 && !form && (
        <div className="text-muted-foreground">
          {t("No tasks. Ask the agent: “check container restarts every 15 minutes”.")}
        </div>
      )}
      {tasks?.map((task) => (
        <div key={task.id} className={cn("border border-border p-2", !task.enabled && "opacity-60")}>
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-primary">#{task.id}</span>
            <span className="text-hi">{task.name}</span>
            <span className="text-xs text-muted-foreground">{task.schedule}</span>
            {task.notify === "telegram" && <span className="text-xs text-tx">tg</span>}
            <span className="flex-1" />
            <span
              className={cn(
                "text-xs",
                task.last_status === "ok" ? "text-primary" : task.last_status === "error" ? "text-crit" : "text-warn",
              )}
            >
              {task.last_status === "running" ? t("running…") : task.last_status || t("never run")}
            </span>
          </div>
          <div className="mt-1 text-xs text-muted-foreground">
            {task.servers.length ? task.servers.join(", ") : t("all servers")} · {t("next run")} {fmtTime(task.next_run_at)} · {t("last run")}{" "}
            {fmtTime(task.last_run_at)}
          </div>
          {task.last_result && (
            <button
              type="button"
              className="mt-1 block w-full text-left text-xs"
              onClick={() => setOpen(open === task.id ? null : task.id)}
            >
              <span className={cn("whitespace-pre-wrap", open !== task.id && "line-clamp-2")}>{task.last_result}</span>
            </button>
          )}
          {open === task.id && (
            <div className="mt-1 text-xs text-muted-foreground whitespace-pre-wrap">{t("prompt:")} {task.prompt}</div>
          )}
          <div className="mt-2 flex gap-2">
            <Button
              size="sm"
              variant="outline"
              disabled={task.last_status === "running"}
              onClick={() => agentApi.runTask(task.id).finally(load)}
            >
              ▶ {t("start")}
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() => agentApi.updateTask(task.id, { enabled: !task.enabled }).finally(load)}
            >
              {task.enabled ? t("disable") : t("enable")}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => confirm(t("Delete task {name}?", { name: task.name })) && agentApi.deleteTask(task.id).finally(load)}
            >
              {t("delete")}
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
      {chats === null && <div className="text-muted-foreground">{t("loading…")}</div>}
      {chats?.length === 0 && <div className="text-muted-foreground">{t("no chats yet")}</div>}
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
            {c.title || t("untitled")}
          </button>
          <span className="shrink-0 text-xs text-muted-foreground">
            {c.server ?? t("all")} · {fmtTime(c.updated_at)}
          </span>
          <button
            type="button"
            className="shrink-0 text-xs text-muted-foreground hover:text-crit"
            aria-label={t("delete chat")}
            onClick={() => confirm(t("Delete the chat?")) && agentApi.deleteChat(c.id).finally(load)}
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
  // Model for a chat that does not exist yet ("" = default).
  const [pickModel, setPickModel] = useState("");
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
    if (chatId && chatId === created.current) {
      // Only the first render after send() created it: later visits load it.
      created.current = undefined;
      return;
    }
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
        if (pickModel) {
          const withModel = await agentApi.setChatModel(id, pickModel).catch(() => null);
          if (withModel && active.current === id) setChat(withModel);
        }
      }
    } catch {
      setErr(t("could not create a chat"));
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
      if (active.current === runId) setErr(e instanceof ApiError && e.code === "busy" ? t("the agent is still replying in this chat") : t("no connection to the agent"));
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
  const chatModel = chatId ? (chat?.model ?? "") : pickModel;
  const shownModel = chatModel || status?.model;
  const model = shownModel ? `pi · ${shownModel}` : "pi";
  const models = status?.models ?? [];
  const changeModel = (m: string) => {
    if (m === (status?.model ?? "")) m = "";
    if (!chatId) return setPickModel(m);
    agentApi
      .setChatModel(chatId, m)
      .then((c) => active.current === c.id && setChat(c))
      .catch(() => setErr(t("the model is unavailable: refresh the list in settings")));
  };
  const ctxName = chat?.server ?? context?.name ?? t("all");

  return (
    <aside className={cn("relative flex min-h-0 flex-col border border-border px-3 pt-4 pb-3", className)}>
      <h2 className="absolute -top-2.5 left-3 max-w-[calc(100%-1.5rem)] truncate bg-background px-1.5 text-sm leading-5 text-primary">
        {t("agent")} ── {model} · {t("context:")} {ctxName}
      </h2>
      <div className="flex flex-wrap items-center gap-1.5">
        <Button size="sm" variant={tab === "chat" ? "default" : "outline"} onClick={() => setTab("chat")}>
          {t("chat")}
        </Button>
        <Button size="sm" variant={tab === "tasks" ? "default" : "outline"} onClick={() => setTab("tasks")}>
          {t("tasks")}
          {taskCount !== null ? ` (${taskCount})` : ""}
        </Button>
        <Button size="sm" variant={tab === "history" ? "default" : "outline"} onClick={() => setTab("history")}>
          {t("history")}
        </Button>
        <span className="flex-1" />
        {pending.length > 0 && <span className="text-xs text-warn">⚠ {pending.length}</span>}
        {models.length > 1 && (
          <select
            className="h-7 max-w-40 min-w-0 border border-input bg-field px-1 text-xs text-hi"
            value={shownModel ?? ""}
            onChange={(e) => changeModel(e.target.value)}
            disabled={!!live}
            title={t("model of this chat")}
            aria-label={t("chat model")}
          >
            {models.map((m) => (
              <option key={m} value={m}>
                {m === status?.model ? "★ " : ""}
                {m}
              </option>
            ))}
          </select>
        )}
        <Button size="sm" variant="ghost" onClick={newChat} title={t("new chat")}>
          + {t("chat")}
        </Button>
      </div>

      {status && !status.enabled && (
        <div className="mt-3 text-xs text-muted-foreground">
          {t("The agent is off: no srvmon-ai container (SRVMON_AI_URL / SRVMON_AI_TOKEN).")}
        </div>
      )}
      {status?.enabled && status.ready === false && (
        <div className="mt-3 text-xs text-warn">
          {t("the agent is not ready:")} {status.error ?? t("srvmon-ai is unavailable")} ·{" "}
          <a className="text-primary underline-offset-2 hover:underline" href={href.settings()}>
            {t("settings → AI")}
          </a>
        </div>
      )}

      <div ref={scroller} className="mt-3 min-h-0 flex-1 overflow-y-auto pr-1 [scrollbar-width:thin]">
        {tab === "chat" && (
          <div className="flex flex-col gap-3">
            {otherPending.map((a) => (
              <ActionCard key={a.id} a={a} onDone={loadPending} />
            ))}
            {messages.length === 0 && !live && (
              <div className="text-xs text-muted-foreground">
                {t(
                  "Ask about your servers or give a task. The agent reads metrics, logs and command output on its own; anything that changes a server it proposes, and it runs only after your “✓ run”.",
                )}
              </div>
            )}
            {messages.map((m) => (
              <MessageView key={m.id} m={m} />
            ))}
            {live && (
              <MessageView m={{ role: "assistant", ...live, text: live.text || (live.tools.length ? "" : "…") }} />
            )}
            {chat?.running && !live && <div className="text-xs text-warn">{t("the agent is replying…")}</div>}
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
            placeholder={t("ask the agent or give a task…")}
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
              aria-label={t("Stop")}
            >
              ■
            </Button>
          ) : (
            <Button type="submit" className="h-10" disabled={disabled || !text.trim()} aria-label={t("Send")}>
              ⏎
            </Button>
          )}
        </form>
      )}
      {err && <div className="mt-1 text-xs text-crit">{err}</div>}
    </aside>
  );
}
