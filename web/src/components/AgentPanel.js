import { jsx as _jsx, Fragment as _Fragment, jsxs as _jsxs } from "react/jsx-runtime";
import { useCallback, useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { agentApi, runChat, toolLine, } from "@/lib/agent";
import { ApiError } from "@/lib/api";
import { href } from "@/lib/router";
import { cn } from "@/lib/utils";
const chatKey = (serverId) => `srvmon.agent.chat.${serverId ?? "all"}`;
function loadChatId(serverId) {
    try {
        const v = Number(localStorage.getItem(chatKey(serverId)));
        return Number.isInteger(v) && v > 0 ? v : undefined;
    }
    catch {
        return undefined;
    }
}
function saveChatId(serverId, id) {
    try {
        if (id)
            localStorage.setItem(chatKey(serverId), String(id));
        else
            localStorage.removeItem(chatKey(serverId));
    }
    catch {
        // private mode: the chat is just not remembered
    }
}
// Minimal markdown: ``` blocks and `inline code`.
function Rich({ text }) {
    const parts = text.split(/```[a-z]*\n?/);
    return (_jsx(_Fragment, { children: parts.map((p, i) => i % 2 === 1 ? (_jsx("pre", { className: "my-1 overflow-x-auto border border-border/60 bg-field px-2 py-1 text-xs text-hi [scrollbar-width:thin]", children: p.replace(/\n$/, "") }, i)) : (_jsx("span", { className: "whitespace-pre-wrap", children: p.split(/(`[^`\n]+`)/).map((s, j) => s.startsWith("`") && s.endsWith("`") && s.length > 2 ? (_jsx("code", { className: "bg-field px-1 text-hi", children: s.slice(1, -1) }, j)) : (s)) }, i))) }));
}
function ToolRow({ t }) {
    const [open, setOpen] = useState(false);
    const pending = t.done === false;
    return (_jsxs("div", { className: "mt-1 text-xs", children: [_jsxs("button", { type: "button", className: "flex w-full min-w-0 items-start gap-1.5 text-left text-muted-foreground hover:text-foreground", onClick: () => setOpen((o) => !o), title: "\u043F\u043E\u043A\u0430\u0437\u0430\u0442\u044C \u0432\u044B\u0432\u043E\u0434", children: [_jsx("span", { className: pending ? "text-warn" : t.ok ? "text-primary" : "text-crit", children: pending ? "…" : t.ok ? "✓" : "✗" }), _jsx("span", { className: "min-w-0 break-all", children: toolLine(t) })] }), open && t.output && (_jsx("pre", { className: "mt-1 max-h-64 overflow-auto border border-border/60 bg-field px-2 py-1 text-[11px] text-foreground [scrollbar-width:thin]", children: t.output }))] }));
}
function ActionCard({ a, onDone }) {
    const [busy, setBusy] = useState(false);
    const [err, setErr] = useState("");
    const decide = async (d) => {
        setBusy(true);
        setErr("");
        try {
            await agentApi.decide(a.id, d);
        }
        catch (e) {
            setErr(e instanceof ApiError && e.code === "already_decided" ? "уже решено" : "не удалось");
        }
        setBusy(false);
        onDone();
    };
    return (_jsxs("div", { className: "mt-2 border border-warn/70 bg-warn/5 p-2", children: [_jsxs("div", { className: "text-xs text-warn", children: ["\u26A0 \u043F\u043E\u0434\u0442\u0432\u0435\u0440\u0434\u0438\u0442\u0435 \u00B7 ", a.server, a.task_id ? ` · задача #${a.task_id}` : ""] }), _jsx("pre", { className: "my-1 overflow-x-auto whitespace-pre-wrap break-all bg-field px-2 py-1 text-xs text-hi", children: a.command }), a.reason && _jsx("div", { className: "text-xs text-muted-foreground", children: a.reason }), _jsxs("div", { className: "mt-2 flex gap-2", children: [_jsx(Button, { size: "sm", disabled: busy, onClick: () => decide("approve"), children: "\u2713 \u0432\u044B\u043F\u043E\u043B\u043D\u0438\u0442\u044C" }), _jsx(Button, { size: "sm", variant: "destructive", disabled: busy, onClick: () => decide("reject"), children: "\u2717 \u043E\u0442\u043A\u043B\u043E\u043D\u0438\u0442\u044C" }), err && _jsx("span", { className: "text-xs text-crit", children: err })] })] }));
}
function MessageView({ m }) {
    if (m.role === "user") {
        return (_jsxs("div", { className: "border-l-2 border-primary pl-2", children: [_jsx("span", { className: "text-primary", children: "\u0432\u044B \u203A" }), " ", _jsx("span", { className: "whitespace-pre-wrap", children: m.text })] }));
    }
    return (_jsxs("div", { className: "border-l-2 border-border pl-2", children: [_jsx("span", { className: "text-hi", children: "\u0430\u0433\u0435\u043D\u0442 \u203A" }), m.tools.length > 0 && (_jsx("div", { className: "mb-1", children: m.tools.map((t) => (_jsx(ToolRow, { t: t }, t.id))) })), " ", m.text ? _jsx(Rich, { text: m.text }) : null, m.error && _jsxs("div", { className: "mt-1 text-xs text-crit", children: ["\u2717 ", m.error] })] }));
}
function fmtTime(s) {
    if (!s)
        return "—";
    const d = new Date(s);
    return `${d.toISOString().slice(5, 10)} ${d.toISOString().slice(11, 16)}`;
}
function TasksTab({ contextServer, onChanged }) {
    const [tasks, setTasks] = useState(null);
    const [form, setForm] = useState(false);
    const [name, setName] = useState("");
    const [schedule, setSchedule] = useState("*/15 * * * *");
    const [prompt, setPrompt] = useState("");
    const [servers, setServers] = useState(contextServer ?? "");
    const [tg, setTg] = useState(true);
    const [err, setErr] = useState("");
    const [open, setOpen] = useState(null);
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
    const fieldMsg = {
        name: "имя: 1–64 символа",
        schedule: "расписание: cron из 5 полей в UTC, не чаще раза в 5 минут, или @every 30m",
        prompt: "промпт обязателен",
        servers: "неизвестный сервер",
    };
    async function create(e) {
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
        }
        catch (e) {
            setErr(e instanceof ApiError ? (fieldMsg[e.code] ?? e.code) : "ошибка");
        }
    }
    return (_jsxs("div", { className: "flex flex-col gap-2", children: [_jsxs("div", { className: "flex items-center gap-2", children: [_jsx("span", { className: "flex-1 text-xs text-muted-foreground", children: "\u0430\u0433\u0435\u043D\u0442 \u0437\u0430\u043F\u0443\u0441\u043A\u0430\u0435\u0442\u0441\u044F \u043F\u043E \u0440\u0430\u0441\u043F\u0438\u0441\u0430\u043D\u0438\u044E (UTC); \u0438\u0437\u043C\u0435\u043D\u0435\u043D\u0438\u044F \u043D\u0430 \u0441\u0435\u0440\u0432\u0435\u0440\u0430\u0445 \u2014 \u0442\u043E\u043B\u044C\u043A\u043E \u043F\u043E\u0441\u043B\u0435 \u043F\u043E\u0434\u0442\u0432\u0435\u0440\u0436\u0434\u0435\u043D\u0438\u044F" }), _jsx(Button, { size: "sm", variant: "outline", onClick: () => setForm((f) => !f), children: form ? "отмена" : "+ задача" })] }), form && (_jsxs("form", { onSubmit: create, className: "flex flex-col gap-2 border border-border p-2", children: [_jsx(Input, { className: "h-8", placeholder: "\u0438\u043C\u044F, \u043D\u0430\u043F\u0440\u0438\u043C\u0435\u0440 restart-watch", value: name, onChange: (e) => setName(e.target.value) }), _jsx(Input, { className: "h-8", placeholder: "*/15 * * * *", value: schedule, onChange: (e) => setSchedule(e.target.value) }), _jsx("textarea", { className: "min-h-20 w-full border border-input bg-field px-3 py-2 text-hi placeholder:text-muted-foreground", placeholder: "\u0447\u0442\u043E \u043F\u0440\u043E\u0432\u0435\u0440\u044F\u0442\u044C \u0438 \u043A\u043E\u0433\u0434\u0430 \u043F\u043E\u0434\u043D\u0438\u043C\u0430\u0442\u044C \u0442\u0440\u0435\u0432\u043E\u0433\u0443", value: prompt, onChange: (e) => setPrompt(e.target.value) }), _jsx(Input, { className: "h-8", placeholder: "\u0441\u0435\u0440\u0432\u0435\u0440\u044B \u0447\u0435\u0440\u0435\u0437 \u0437\u0430\u043F\u044F\u0442\u0443\u044E, \u043F\u0443\u0441\u0442\u043E = \u0432\u0441\u0435", value: servers, onChange: (e) => setServers(e.target.value) }), _jsxs("label", { className: "flex items-center gap-2 text-xs", children: [_jsx("input", { type: "checkbox", checked: tg, onChange: (e) => setTg(e.target.checked) }), " \u0432 Telegram \u043F\u0440\u0438 ALERT \u0438\u043B\u0438 \u043E\u0448\u0438\u0431\u043A\u0435"] }), err && _jsx("div", { className: "text-xs text-crit", children: err }), _jsx(Button, { size: "sm", type: "submit", children: "\u0441\u043E\u0437\u0434\u0430\u0442\u044C" })] })), tasks === null && _jsx("div", { className: "text-muted-foreground", children: "\u0437\u0430\u0433\u0440\u0443\u0437\u043A\u0430\u2026" }), tasks?.length === 0 && !form && (_jsx("div", { className: "text-muted-foreground", children: "\u0437\u0430\u0434\u0430\u0447 \u043D\u0435\u0442. \u041F\u043E\u043F\u0440\u043E\u0441\u0438\u0442\u0435 \u0430\u0433\u0435\u043D\u0442\u0430: \u00AB\u043A\u0430\u0436\u0434\u044B\u0435 15 \u043C\u0438\u043D\u0443\u0442 \u043F\u0440\u043E\u0432\u0435\u0440\u044F\u0439 \u0440\u0435\u0441\u0442\u0430\u0440\u0442\u044B \u043A\u043E\u043D\u0442\u0435\u0439\u043D\u0435\u0440\u043E\u0432\u00BB." })), tasks?.map((t) => (_jsxs("div", { className: cn("border border-border p-2", !t.enabled && "opacity-60"), children: [_jsxs("div", { className: "flex flex-wrap items-center gap-2", children: [_jsxs("span", { className: "text-primary", children: ["#", t.id] }), _jsx("span", { className: "text-hi", children: t.name }), _jsx("span", { className: "text-xs text-muted-foreground", children: t.schedule }), t.notify === "telegram" && _jsx("span", { className: "text-xs text-tx", children: "tg" }), _jsx("span", { className: "flex-1" }), _jsx("span", { className: cn("text-xs", t.last_status === "ok" ? "text-primary" : t.last_status === "error" ? "text-crit" : "text-warn"), children: t.last_status === "running" ? "выполняется…" : t.last_status || "не запускалась" })] }), _jsxs("div", { className: "mt-1 text-xs text-muted-foreground", children: [t.servers.length ? t.servers.join(", ") : "все серверы", " \u00B7 \u0441\u043B\u0435\u0434. ", fmtTime(t.next_run_at), " \u00B7 \u043F\u043E\u0441\u043B.", " ", fmtTime(t.last_run_at)] }), t.last_result && (_jsx("button", { type: "button", className: "mt-1 block w-full text-left text-xs", onClick: () => setOpen(open === t.id ? null : t.id), children: _jsx("span", { className: cn("whitespace-pre-wrap", open !== t.id && "line-clamp-2"), children: t.last_result }) })), open === t.id && (_jsxs("div", { className: "mt-1 text-xs text-muted-foreground whitespace-pre-wrap", children: ["\u043F\u0440\u043E\u043C\u043F\u0442: ", t.prompt] })), _jsxs("div", { className: "mt-2 flex gap-2", children: [_jsx(Button, { size: "sm", variant: "outline", disabled: t.last_status === "running", onClick: () => agentApi.runTask(t.id).finally(load), children: "\u25B6 \u0437\u0430\u043F\u0443\u0441\u0442\u0438\u0442\u044C" }), _jsx(Button, { size: "sm", variant: "outline", onClick: () => agentApi.updateTask(t.id, { enabled: !t.enabled }).finally(load), children: t.enabled ? "выкл" : "вкл" }), _jsx(Button, { size: "sm", variant: "ghost", onClick: () => confirm(`Удалить задачу ${t.name}?`) && agentApi.deleteTask(t.id).finally(load), children: "\u0443\u0434\u0430\u043B\u0438\u0442\u044C" })] })] }, t.id)))] }));
}
function HistoryTab({ current, onOpen }) {
    const [chats, setChats] = useState(null);
    const load = useCallback(() => {
        agentApi
            .chats()
            .then(setChats)
            .catch(() => setChats([]));
    }, []);
    useEffect(load, [load]);
    return (_jsxs("div", { className: "flex flex-col", children: [chats === null && _jsx("div", { className: "text-muted-foreground", children: "\u0437\u0430\u0433\u0440\u0443\u0437\u043A\u0430\u2026" }), chats?.length === 0 && _jsx("div", { className: "text-muted-foreground", children: "\u0447\u0430\u0442\u043E\u0432 \u043F\u043E\u043A\u0430 \u043D\u0435\u0442" }), chats?.map((c) => (_jsxs("div", { className: cn("flex items-center gap-2 border-t border-border/60 py-1.5 first:border-t-0", c.id === current && "text-primary"), children: [_jsxs("button", { type: "button", className: "min-w-0 flex-1 truncate text-left hover:text-primary", onClick: () => onOpen(c), children: [c.running ? "● " : "", c.title || "без названия"] }), _jsxs("span", { className: "shrink-0 text-xs text-muted-foreground", children: [c.server ?? "все", " \u00B7 ", fmtTime(c.updated_at)] }), _jsx("button", { type: "button", className: "shrink-0 text-xs text-muted-foreground hover:text-crit", "aria-label": "\u0443\u0434\u0430\u043B\u0438\u0442\u044C \u0447\u0430\u0442", onClick: () => confirm("Удалить чат?") && agentApi.deleteChat(c.id).finally(load), children: "\u00D7" })] }, c.id)))] }));
}
export function AgentPanel({ context, className, inputRef, }) {
    const [status, setStatus] = useState(null);
    const [tab, setTab] = useState("chat");
    const [chatId, setChatId] = useState(() => loadChatId(context?.id));
    const [chat, setChat] = useState(null);
    const [messages, setMessages] = useState([]);
    const [live, setLive] = useState(null);
    const [pending, setPending] = useState([]);
    const [text, setText] = useState("");
    // Model for a chat that does not exist yet ("" = default).
    const [pickModel, setPickModel] = useState("");
    const [taskCount, setTaskCount] = useState(null);
    const [err, setErr] = useState("");
    const scroller = useRef(null);
    // A chat created by send() is already on screen: do not reload it.
    const created = useRef(undefined);
    // The chat on screen. Callbacks of a run or a load for another chat
    // (the user switched while it was in flight) must not touch the view.
    const active = useRef(chatId);
    active.current = chatId;
    const ctxId = context?.id;
    useEffect(() => {
        const load = () => agentApi
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
    const loadChat = useCallback(async (id) => {
        try {
            const r = await agentApi.chat(id);
            if (active.current !== id)
                return null;
            setChat(r.chat);
            setMessages(r.messages);
            return r.chat;
        }
        catch (e) {
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
        if (!chatId || !chat?.running || live)
            return;
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
        if (el)
            el.scrollTop = el.scrollHeight;
    }, [messages, live, pending.length, tab]);
    async function send(e) {
        e?.preventDefault();
        const msg = text.trim();
        if (!msg || live)
            return;
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
                    if (withModel && active.current === id)
                        setChat(withModel);
                }
            }
        }
        catch {
            setErr("не удалось создать чат");
            return;
        }
        setText("");
        setMessages((m) => [...m, { id: -Date.now(), role: "user", text: msg, tools: [], ts: new Date().toISOString() }]);
        const cur = { text: "", tools: [] };
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
                        if (ev.text)
                            cur.text = ev.text;
                        break;
                }
                show();
            });
        }
        catch (e) {
            if (active.current === runId)
                setErr(e instanceof ApiError && e.code === "busy" ? "агент ещё отвечает в этом чате" : "нет связи с агентом");
        }
        if (active.current === runId) {
            await loadChat(runId);
            if (active.current === runId)
                setLive(null);
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
    const changeModel = (m) => {
        if (m === (status?.model ?? ""))
            m = "";
        if (!chatId)
            return setPickModel(m);
        agentApi
            .setChatModel(chatId, m)
            .then((c) => active.current === c.id && setChat(c))
            .catch(() => setErr("модель недоступна: обновите список в настройках"));
    };
    const ctxName = chat?.server ?? context?.name ?? "все";
    return (_jsxs("aside", { className: cn("relative flex min-h-0 flex-col border border-border px-3 pt-4 pb-3", className), children: [_jsxs("h2", { className: "absolute -top-2.5 left-3 max-w-[calc(100%-1.5rem)] truncate bg-background px-1.5 text-sm leading-5 text-primary", children: ["\u0430\u0433\u0435\u043D\u0442 \u2500\u2500 ", model, " \u00B7 \u043A\u043E\u043D\u0442\u0435\u043A\u0441\u0442: ", ctxName] }), _jsxs("div", { className: "flex flex-wrap items-center gap-1.5", children: [_jsx(Button, { size: "sm", variant: tab === "chat" ? "default" : "outline", onClick: () => setTab("chat"), children: "\u0447\u0430\u0442" }), _jsxs(Button, { size: "sm", variant: tab === "tasks" ? "default" : "outline", onClick: () => setTab("tasks"), children: ["\u0437\u0430\u0434\u0430\u0447\u0438", taskCount !== null ? ` (${taskCount})` : ""] }), _jsx(Button, { size: "sm", variant: tab === "history" ? "default" : "outline", onClick: () => setTab("history"), children: "\u0438\u0441\u0442\u043E\u0440\u0438\u044F" }), _jsx("span", { className: "flex-1" }), pending.length > 0 && _jsxs("span", { className: "text-xs text-warn", children: ["\u26A0 ", pending.length] }), models.length > 1 && (_jsx("select", { className: "h-7 max-w-40 min-w-0 border border-input bg-field px-1 text-xs text-hi", value: shownModel ?? "", onChange: (e) => changeModel(e.target.value), disabled: !!live, title: "\u043C\u043E\u0434\u0435\u043B\u044C \u044D\u0442\u043E\u0433\u043E \u0447\u0430\u0442\u0430", "aria-label": "\u043C\u043E\u0434\u0435\u043B\u044C \u0447\u0430\u0442\u0430", children: models.map((m) => (_jsxs("option", { value: m, children: [m === status?.model ? "★ " : "", m] }, m))) })), _jsx(Button, { size: "sm", variant: "ghost", onClick: newChat, title: "\u043D\u043E\u0432\u044B\u0439 \u0447\u0430\u0442", children: "+ \u0447\u0430\u0442" })] }), status && !status.enabled && (_jsx("div", { className: "mt-3 text-xs text-muted-foreground", children: "\u0410\u0433\u0435\u043D\u0442 \u0432\u044B\u043A\u043B\u044E\u0447\u0435\u043D: \u043D\u0435\u0442 \u043A\u043E\u043D\u0442\u0435\u0439\u043D\u0435\u0440\u0430 srvmon-ai (SRVMON_AI_URL / SRVMON_AI_TOKEN)." })), status?.enabled && status.ready === false && (_jsxs("div", { className: "mt-3 text-xs text-warn", children: ["\u0430\u0433\u0435\u043D\u0442 \u043D\u0435 \u0433\u043E\u0442\u043E\u0432: ", status.error ?? "srvmon-ai недоступен", " \u00B7", " ", _jsx("a", { className: "text-primary underline-offset-2 hover:underline", href: href.settings(), children: "\u043D\u0430\u0441\u0442\u0440\u043E\u0439\u043A\u0438 \u2192 AI" })] })), _jsxs("div", { ref: scroller, className: "mt-3 min-h-0 flex-1 overflow-y-auto pr-1 [scrollbar-width:thin]", children: [tab === "chat" && (_jsxs("div", { className: "flex flex-col gap-3", children: [otherPending.map((a) => (_jsx(ActionCard, { a: a, onDone: loadPending }, a.id))), messages.length === 0 && !live && (_jsx("div", { className: "text-xs text-muted-foreground", children: "\u0421\u043F\u0440\u043E\u0441\u0438\u0442\u0435 \u043E \u0441\u0435\u0440\u0432\u0435\u0440\u0430\u0445 \u0438\u043B\u0438 \u043F\u043E\u0441\u0442\u0430\u0432\u044C\u0442\u0435 \u0437\u0430\u0434\u0430\u0447\u0443. \u0410\u0433\u0435\u043D\u0442 \u0447\u0438\u0442\u0430\u0435\u0442 \u043C\u0435\u0442\u0440\u0438\u043A\u0438, \u043B\u043E\u0433\u0438 \u0438 \u0432\u044B\u0432\u043E\u0434 \u043A\u043E\u043C\u0430\u043D\u0434 \u0441\u0430\u043C; \u0432\u0441\u0451, \u0447\u0442\u043E \u043C\u0435\u043D\u044F\u0435\u0442 \u0441\u0435\u0440\u0432\u0435\u0440, \u043E\u043D \u043F\u0440\u0435\u0434\u043B\u043E\u0436\u0438\u0442, \u0430 \u0432\u044B\u043F\u043E\u043B\u043D\u0438\u0442\u0441\u044F \u043E\u043D\u043E \u0442\u043E\u043B\u044C\u043A\u043E \u043F\u043E\u0441\u043B\u0435 \u0432\u0430\u0448\u0435\u0433\u043E \u00AB\u2713 \u0432\u044B\u043F\u043E\u043B\u043D\u0438\u0442\u044C\u00BB." })), messages.map((m) => (_jsx(MessageView, { m: m }, m.id))), live && (_jsx(MessageView, { m: { role: "assistant", ...live, text: live.text || (live.tools.length ? "" : "…") } })), chat?.running && !live && _jsx("div", { className: "text-xs text-warn", children: "\u0430\u0433\u0435\u043D\u0442 \u043E\u0442\u0432\u0435\u0447\u0430\u0435\u0442\u2026" }), chatPending.map((a) => (_jsx(ActionCard, { a: a, onDone: loadPending }, a.id)))] })), tab === "tasks" && _jsx(TasksTab, { contextServer: context?.name, onChanged: setTaskCount }), tab === "history" && (_jsx(HistoryTab, { current: chatId, onOpen: (c) => {
                            setChatId(c.id);
                            setTab("chat");
                        } }))] }), tab === "chat" && (_jsxs("form", { onSubmit: send, className: "mt-3 flex items-center gap-2", children: [_jsx("label", { htmlFor: "agent-ask", className: "text-muted-foreground", children: "\u203A" }), _jsx(Input, { id: "agent-ask", ref: inputRef, className: "h-10 min-w-0 flex-1", placeholder: "\u0441\u043F\u0440\u043E\u0441\u0438\u0442\u044C \u0430\u0433\u0435\u043D\u0442\u0430 \u0438\u043B\u0438 \u043F\u043E\u0441\u0442\u0430\u0432\u0438\u0442\u044C \u0437\u0430\u0434\u0430\u0447\u0443\u2026", value: text, maxLength: 4000, disabled: disabled, onChange: (e) => setText(e.target.value) }), live && chatId ? (_jsx(Button, { type: "button", variant: "destructive", className: "h-10", onClick: () => void agentApi.abort(chatId), "aria-label": "\u041E\u0441\u0442\u0430\u043D\u043E\u0432\u0438\u0442\u044C", children: "\u25A0" })) : (_jsx(Button, { type: "submit", className: "h-10", disabled: disabled || !text.trim(), "aria-label": "\u041E\u0442\u043F\u0440\u0430\u0432\u0438\u0442\u044C", children: "\u23CE" }))] })), err && _jsx("div", { className: "mt-1 text-xs text-crit", children: err })] }));
}
