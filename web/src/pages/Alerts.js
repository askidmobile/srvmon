import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useCallback, useEffect, useState } from "react";
import { Hotkey, Shell } from "@/components/Shell";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { ScrollArea } from "@/components/ui/scroll-area";
import { api, ApiError, errorMessage, } from "@/lib/api";
import { href } from "@/lib/router";
import { cn } from "@/lib/utils";
const fieldCls = "h-9 w-full min-w-0 border border-input bg-field px-2.5 text-hi placeholder:text-muted-foreground focus-visible:outline-1 focus-visible:outline-ring disabled:opacity-60";
const cellField = "h-6 min-w-0 border border-input bg-field px-1.5 text-xs text-hi focus-visible:outline-1 focus-visible:outline-ring";
// How each rule kind reads in the table and which fields it has.
// dur: "for" — the condition must hold this long; "window" — counting
// window; "silent" — how long the agent is quiet; null — no duration.
const KINDS = {
    agent_silent: { name: "агент молчит", label: () => "агент молчит", dur: "silent" },
    cpu: { name: "cpu >", label: (t) => `cpu > ${t}%`, unit: "%", dur: "for" },
    mem: { name: "mem >", label: (t) => `mem > ${t}%`, unit: "%", dur: "for" },
    disk: { name: "disk >", label: (t) => `disk > ${t}%`, unit: "%", dur: "for" },
    container_down: { name: "контейнер упал / unhealthy", label: () => "контейнер упал / unhealthy", dur: "for" },
    container_restarts: {
        name: "рестартов контейнера >",
        label: (t) => `рестартов контейнера > ${t}`,
        unit: "раз",
        dur: "window",
    },
    payment_due: { name: "оплата сервера через ≤", label: (t) => `оплата сервера через ≤ ${t} дн.`, unit: "дн.", dur: null },
    fail2ban: { name: "бан fail2ban", label: () => "бан fail2ban на сервере мониторинга", dur: null },
    bruteforce: { name: "подбор пароля к панели", label: () => "подбор пароля к панели", dur: null },
    login: { name: "вход в панель", label: () => "вход в панель", dur: null },
};
const NEW_DEFAULTS = {
    agent_silent: { threshold: 0, duration_sec: 300 },
    cpu: { threshold: 95, duration_sec: 600 },
    mem: { threshold: 90, duration_sec: 300 },
    disk: { threshold: 95, duration_sec: 0 },
    container_down: { threshold: 0, duration_sec: 300 },
    container_restarts: { threshold: 5, duration_sec: 3600 },
    payment_due: { threshold: 7, duration_sec: 0 },
    fail2ban: { threshold: 0, duration_sec: 0 },
    bruteforce: { threshold: 0, duration_sec: 0 },
    login: { threshold: 0, duration_sec: 0 },
};
const sevCls = { crit: "text-crit", warn: "text-warn", info: "text-tx" };
// 300 → "5м", 5400 → "1ч30м". Display keeps two units; full keeps all
// (for the editor, so saving does not shorten the value).
export function fmtDuration(sec, full = false) {
    if (sec <= 0)
        return "─";
    const parts = [];
    for (const [n, u] of [
        [86400, "д"],
        [3600, "ч"],
        [60, "м"],
        [1, "с"],
    ]) {
        if (sec >= n) {
            parts.push(`${Math.floor(sec / n)}${u}`);
            sec %= n;
        }
    }
    return (full ? parts : parts.slice(0, 2)).join("");
}
// "1ч30м", "90s", "5m", "10" (minutes) → seconds; null when unreadable.
export function parseDuration(s) {
    const t = s.trim().toLowerCase().replace(/\s+/g, "");
    if (t === "" || t === "─" || t === "-" || t === "0")
        return 0;
    if (/^\d+$/.test(t))
        return Number(t) * 60;
    const mult = { д: 86400, d: 86400, ч: 3600, h: 3600, м: 60, m: 60, с: 1, s: 1 };
    let total = 0;
    let rest = t;
    const re = /^(\d+)(д|d|ч|h|м|m|с|s)/;
    while (rest) {
        const m = re.exec(rest);
        if (!m)
            return null;
        total += Number(m[1]) * mult[m[2]];
        rest = rest.slice(m[0].length);
    }
    return total;
}
function when(iso) {
    if (!iso)
        return "";
    const d = new Date(iso);
    const p = (n) => String(n).padStart(2, "0");
    const today = new Date().toDateString() === d.toDateString();
    return `${today ? "" : `${p(d.getDate())}.${p(d.getMonth() + 1)} `}${p(d.getHours())}:${p(d.getMinutes())}`;
}
function durLabel(r) {
    const k = KINDS[r.kind];
    if (!k?.dur || r.duration_sec <= 0)
        return "─";
    return (k.dur === "silent" ? "> " : "") + fmtDuration(r.duration_sec);
}
// --- active alerts and history ---
function AlertRow({ a, onChange }) {
    const [busy, setBusy] = useState(false);
    const act = async (f) => {
        setBusy(true);
        await f().catch(() => undefined);
        setBusy(false);
        onChange();
    };
    return (_jsxs("tr", { className: "border-t border-border/60 align-top", children: [_jsx("td", { className: "py-1.5 pr-3 whitespace-nowrap text-muted-foreground", children: when(a.started_at) }), _jsx("td", { className: cn("pr-3 uppercase", sevCls[a.severity]), children: a.severity }), _jsx("td", { className: "pr-3 whitespace-nowrap", children: a.server_id ? (_jsx("a", { className: "text-hi hover:text-primary", href: href.server(a.server_id), children: a.server_name })) : (_jsx("span", { className: "text-hi", children: a.server_name || "панель" })) }), _jsxs("td", { className: "pr-3", children: [a.message, a.channel === "tg" && _jsx("span", { className: "text-muted-foreground", children: a.notified ? " → tg" : "" })] }), _jsx("td", { className: "pr-3 whitespace-nowrap text-muted-foreground", children: a.state === "acked" ? _jsx("span", { className: "text-primary", children: "\u2713 ack" }) : _jsx("span", { className: sevCls[a.severity], children: "\u25CF" }) }), _jsxs("td", { className: "whitespace-nowrap text-right", children: [a.state === "firing" && (_jsx(Button, { size: "sm", variant: "outline", disabled: busy, onClick: () => act(() => api.ackAlert(a.id)), children: "ack" })), " ", _jsx(Button, { size: "sm", variant: "ghost", disabled: busy, title: "\u043D\u0435 \u043F\u0440\u0438\u0441\u044B\u043B\u0430\u0442\u044C \u0443\u0432\u0435\u0434\u043E\u043C\u043B\u0435\u043D\u0438\u044F \u043F\u043E \u044D\u0442\u043E\u043C\u0443 \u0430\u043B\u0435\u0440\u0442\u0443 \u0447\u0430\u0441", onClick: () => act(() => api.silenceAlert(a.id, 60)), children: "\u0442\u0438\u0448\u0438\u043D\u0430 1\u0447" })] })] }));
}
function stateText(a) {
    switch (a.state) {
        case "event":
            return _jsx("span", { className: "text-muted-foreground", children: "\u0441\u043E\u0431\u044B\u0442\u0438\u0435" });
        case "resolved": {
            const d = a.resolved_at ? (Date.parse(a.resolved_at) - Date.parse(a.started_at)) / 1000 : 0;
            return _jsxs("span", { className: "text-primary", children: ["resolved ", d > 0 ? fmtDuration(Math.round(d)) : ""] });
        }
        case "acked":
            return _jsx("span", { className: "text-primary", children: "\u2713 ack" });
    }
    return _jsx("span", { className: sevCls[a.severity], children: "firing" });
}
// --- rules ---
function RuleEditor({ rule, isNew, onDone, }) {
    const [r, setR] = useState(rule);
    const [thr, setThr] = useState(String(rule.threshold));
    const [dur, setDur] = useState(rule.duration_sec ? fmtDuration(rule.duration_sec, true) : "");
    const [err, setErr] = useState("");
    const k = KINDS[r.kind];
    const changeKind = (kind) => {
        const d = NEW_DEFAULTS[kind];
        setR({ ...r, kind, ...d });
        setThr(String(d.threshold));
        setDur(d.duration_sec ? fmtDuration(d.duration_sec, true) : "");
    };
    const save = async () => {
        const duration = k.dur ? parseDuration(dur) : 0;
        if (duration === null) {
            setErr("длительность: например 5м, 1ч, 30с");
            return;
        }
        const threshold = k.unit ? Number(thr.replace(",", ".")) : 0;
        if (!Number.isFinite(threshold)) {
            setErr("порог — число");
            return;
        }
        const next = { ...r, threshold, duration_sec: duration };
        try {
            if (isNew)
                await api.createRule(next);
            else
                await api.updateRule(next);
            onDone(true);
        }
        catch (e) {
            setErr(errorMessage(e, "не сохранилось"));
        }
    };
    return (_jsxs(_Fragment, { children: [_jsxs("tr", { className: "border-t border-border/60 bg-secondary/40", children: [_jsx("td", { className: "py-1.5 pr-2", children: _jsx("button", { type: "button", className: r.enabled ? "text-primary" : "text-muted-foreground", onClick: () => setR({ ...r, enabled: !r.enabled }), children: r.enabled ? "[x]" : "[ ]" }) }), _jsx("td", { className: "pr-2", children: _jsxs("div", { className: "flex flex-wrap items-center gap-1.5", children: [isNew ? (_jsx("select", { className: cellField, value: r.kind, onChange: (e) => changeKind(e.target.value), "aria-label": "\u0442\u0438\u043F", children: Object.entries(KINDS).map(([kind, m]) => (_jsx("option", { value: kind, children: m.name }, kind))) })) : (_jsx("span", { children: k.name })), k.unit && (_jsxs(_Fragment, { children: [_jsx("input", { className: cn(cellField, "w-16"), inputMode: "decimal", value: thr, onChange: (e) => setThr(e.target.value), "aria-label": "\u043F\u043E\u0440\u043E\u0433" }), _jsx("span", { className: "text-xs text-muted-foreground", children: k.unit })] }))] }) }), _jsx("td", { className: "pr-2", children: k.dur ? (_jsx("input", { className: cn(cellField, "w-20"), value: dur, placeholder: k.dur === "for" ? "сразу" : "5м", onChange: (e) => setDur(e.target.value), "aria-label": "\u0434\u043B\u0438\u0442\u0435\u043B\u044C\u043D\u043E\u0441\u0442\u044C" })) : ("─") }), _jsx("td", { className: "pr-2", children: _jsxs("select", { className: cn(cellField, sevCls[r.severity]), value: r.severity, onChange: (e) => setR({ ...r, severity: e.target.value }), "aria-label": "\u0443\u0440\u043E\u0432\u0435\u043D\u044C", children: [_jsx("option", { value: "crit", children: "crit" }), _jsx("option", { value: "warn", children: "warn" }), _jsx("option", { value: "info", children: "info" })] }) }), _jsx("td", { className: "pr-2", children: _jsxs("select", { className: cellField, value: r.channel, onChange: (e) => setR({ ...r, channel: e.target.value }), "aria-label": "\u043A\u0430\u043D\u0430\u043B", children: [_jsx("option", { value: "tg", children: "tg" }), _jsx("option", { value: "web", children: "web" })] }) }), _jsxs("td", { className: "whitespace-nowrap text-right", children: [_jsx(Button, { size: "sm", onClick: save, children: "\u043E\u043A" }), " ", _jsx(Button, { size: "sm", variant: "ghost", onClick: () => onDone(false), children: "\u043E\u0442\u043C\u0435\u043D\u0430" })] })] }), err && (_jsxs("tr", { children: [_jsx("td", {}), _jsx("td", { colSpan: 5, className: "pb-1.5 text-xs text-crit", children: err })] }))] }));
}
function RulesPanel() {
    const [rules, setRules] = useState(null);
    const [editing, setEditing] = useState(null);
    const [err, setErr] = useState("");
    const load = useCallback(() => {
        api
            .rules()
            .then(setRules)
            .catch(() => setErr("не удалось загрузить правила"));
    }, []);
    useEffect(load, [load]);
    const toggle = async (r) => {
        try {
            await api.updateRule({ ...r, enabled: !r.enabled });
            load();
        }
        catch (e) {
            setErr(errorMessage(e, "не сохранилось"));
        }
    };
    const remove = async (r) => {
        if (!confirm(`Удалить правило «${KINDS[r.kind]?.label(r.threshold) ?? r.kind}»?`))
            return;
        await api.deleteRule(r.id).catch(() => setErr("не удалось удалить"));
        load();
    };
    const done = (changed) => {
        setEditing(null);
        if (changed)
            load();
    };
    return (_jsxs(Panel, { title: "\u043F\u0440\u0430\u0432\u0438\u043B\u0430 \u0430\u043B\u0435\u0440\u0442\u043E\u0432", right: "\u043F\u0440\u043E\u0432\u0435\u0440\u043A\u0430 \u043A\u0430\u0436\u0434\u044B\u0435 15\u0441", children: [_jsx(ScrollArea, { className: "w-full", children: _jsxs("table", { className: "w-full min-w-[560px] border-collapse text-left", children: [_jsx("thead", { className: "text-xs text-muted-foreground", children: _jsxs("tr", { children: [_jsx("th", { className: "pb-2 font-normal", children: "\u0432\u043A\u043B" }), _jsx("th", { className: "pb-2 font-normal", children: "\u0443\u0441\u043B\u043E\u0432\u0438\u0435" }), _jsx("th", { className: "pb-2 font-normal", children: "\u0434\u043B\u0438\u0442." }), _jsx("th", { className: "pb-2 font-normal", children: "\u0443\u0440\u043E\u0432\u0435\u043D\u044C" }), _jsx("th", { className: "pb-2 font-normal", children: "\u043A\u0430\u043D\u0430\u043B" }), _jsx("th", { className: "pb-2 font-normal" })] }) }), _jsxs("tbody", { children: [(rules ?? []).map((r) => editing === r.id ? (_jsx(RuleEditor, { rule: r, isNew: false, onDone: done }, r.id)) : (_jsxs("tr", { className: "border-t border-border/60", children: [_jsx("td", { className: "py-1.5 pr-2", children: _jsx("button", { type: "button", className: r.enabled ? "text-primary" : "text-muted-foreground", title: r.enabled ? "выключить" : "включить", onClick: () => toggle(r), children: r.enabled ? "[x]" : "[ ]" }) }), _jsx("td", { className: cn("pr-2", !r.enabled && "text-muted-foreground"), children: KINDS[r.kind]?.label(r.threshold) ?? r.kind }), _jsx("td", { className: "pr-2 text-muted-foreground", children: durLabel(r) }), _jsx("td", { className: cn("pr-2", sevCls[r.severity]), children: r.severity }), _jsx("td", { className: "pr-2 text-muted-foreground", children: r.channel }), _jsxs("td", { className: "whitespace-nowrap text-right", children: [_jsx(Button, { size: "sm", variant: "ghost", onClick: () => setEditing(r.id), children: "\u0438\u0437\u043C." }), _jsx(Button, { size: "sm", variant: "ghost", className: "hover:text-crit", title: "\u0443\u0434\u0430\u043B\u0438\u0442\u044C", onClick: () => remove(r), children: "\u00D7" })] })] }, r.id))), editing === "new" && (_jsx(RuleEditor, { rule: { id: 0, kind: "cpu", enabled: true, severity: "warn", channel: "tg", ...NEW_DEFAULTS.cpu }, isNew: true, onDone: done }))] })] }) }), err && _jsx("div", { className: "mt-2 text-sm text-crit", children: err }), _jsxs("div", { className: "mt-3 flex flex-wrap items-center gap-3", children: [_jsx(Button, { variant: "outline", disabled: editing !== null, onClick: () => setEditing("new"), children: "+ \u043F\u0440\u0430\u0432\u0438\u043B\u043E" }), _jsx(QuietHours, {})] }), _jsx("p", { className: "mt-2 text-xs text-muted-foreground", children: "tg \u2014 \u0443\u0432\u0435\u0434\u043E\u043C\u043B\u0435\u043D\u0438\u0435 \u0432 Telegram \u0438 \u043D\u0430 \u043F\u0430\u043D\u0435\u043B\u0438, web \u2014 \u0442\u043E\u043B\u044C\u043A\u043E \u043D\u0430 \u043F\u0430\u043D\u0435\u043B\u0438. \u041A\u043E\u043D\u0442\u0435\u0439\u043D\u0435\u0440\u044B, \u043E\u0441\u0442\u0430\u043D\u043E\u0432\u043B\u0435\u043D\u043D\u044B\u0435 \u0441 \u043A\u043E\u0434\u043E\u043C 0, \u0430\u043B\u0435\u0440\u0442\u043E\u043C \u043D\u0435 \u0441\u0447\u0438\u0442\u0430\u044E\u0442\u0441\u044F." })] }));
}
function QuietHours() {
    const [q, setQ] = useState(null);
    const [saved, setSaved] = useState("");
    useEffect(() => {
        api
            .notify()
            .then((n) => setQ(n.quiet))
            .catch(() => undefined);
    }, []);
    if (!q)
        return null;
    const save = async () => {
        try {
            setQ(await api.saveQuiet(q));
            setSaved("сохранено");
        }
        catch (e) {
            setSaved(errorMessage(e, "ошибка"));
        }
        setTimeout(() => setSaved(""), 3000);
    };
    return (_jsxs("div", { className: "flex flex-wrap items-center gap-1.5 text-sm text-muted-foreground", children: [_jsx("span", { children: "\u0442\u0438\u0448\u0438\u043D\u0430" }), _jsx("input", { type: "time", className: cn(cellField, "h-7"), value: q.from, onChange: (e) => setQ({ ...q, from: e.target.value }), "aria-label": "\u0441" }), _jsx("span", { children: "\u2013" }), _jsx("input", { type: "time", className: cn(cellField, "h-7"), value: q.to, onChange: (e) => setQ({ ...q, to: e.target.value }), "aria-label": "\u0434\u043E" }), _jsx("input", { className: cn(cellField, "h-7 w-36"), value: q.tz, placeholder: "Europe/Moscow", onChange: (e) => setQ({ ...q, tz: e.target.value }), "aria-label": "\u0447\u0430\u0441\u043E\u0432\u043E\u0439 \u043F\u043E\u044F\u0441" }), _jsx("span", { children: "\u043A\u0440\u043E\u043C\u0435 crit" }), _jsx(Button, { size: "sm", variant: "outline", onClick: save, children: "\u0441\u043E\u0445\u0440\u0430\u043D\u0438\u0442\u044C" }), saved && _jsx("span", { className: "text-xs", children: saved })] }));
}
// --- Telegram ---
function TelegramPanel() {
    const [n, setN] = useState(null);
    const [token, setToken] = useState("");
    const [chat, setChat] = useState("");
    const [msg, setMsg] = useState(null);
    const [busy, setBusy] = useState(false);
    const load = useCallback((resetChat) => api
        .notify()
        .then((v) => {
        setN(v);
        if (resetChat)
            setChat(v.telegram.chat_id);
    })
        .catch(() => undefined), []);
    useEffect(() => {
        load(true);
        const t = setInterval(() => load(false), 5000);
        return () => clearInterval(t);
    }, [load]);
    const run = async (f, ok) => {
        setBusy(true);
        setMsg(null);
        try {
            await f();
            setMsg({ ok: true, text: ok });
            setToken("");
            await load(true);
        }
        catch (e) {
            setMsg({ ok: false, text: e instanceof ApiError && e.code === "no_master_key" ? "нет мастер-ключа на сервере" : errorMessage(e, "ошибка") });
        }
        setBusy(false);
    };
    if (!n)
        return _jsx(Panel, { title: "telegram-\u0431\u043E\u0442", children: "\u0437\u0430\u0433\u0440\u0443\u0437\u043A\u0430\u2026" });
    const tg = n.telegram;
    const status = !tg.configured ? (_jsx("span", { className: "text-muted-foreground", children: "\u25CB \u043D\u0435 \u043D\u0430\u0441\u0442\u0440\u043E\u0435\u043D" })) : tg.connected ? (_jsxs("span", { className: "text-primary", children: ["\u25CF \u043F\u043E\u0434\u043A\u043B\u044E\u0447\u0451\u043D", tg.bot_name ? ` · @${tg.bot_name}` : ""] })) : tg.last_error ? (_jsxs("span", { className: "text-crit", children: ["\u2715 ", tg.last_error] })) : (_jsx("span", { className: "text-warn", children: "\u2026 \u043F\u043E\u0434\u043A\u043B\u044E\u0447\u0430\u0435\u0442\u0441\u044F" }));
    return (_jsx(Panel, { title: "telegram-\u0431\u043E\u0442", children: _jsxs("div", { className: "flex flex-col gap-2.5", children: [_jsxs("div", { className: "flex flex-col gap-1", children: [_jsx("label", { className: "text-sm text-muted-foreground", htmlFor: "tg-token", children: "bot token" }), _jsx("input", { id: "tg-token", type: "password", autoComplete: "off", className: fieldCls, value: token, placeholder: tg.configured ? "сохранён · оставьте пустым, чтобы не менять" : "123456789:AA…", onChange: (e) => setToken(e.target.value) })] }), _jsxs("div", { className: "flex flex-col gap-1", children: [_jsx("label", { className: "text-sm text-muted-foreground", htmlFor: "tg-chat", children: "chat id (\u0431\u043E\u0442 \u043E\u0442\u0432\u0435\u0447\u0430\u0435\u0442 \u0442\u043E\u043B\u044C\u043A\u043E \u044D\u0442\u043E\u043C\u0443 \u0447\u0430\u0442\u0443)" }), _jsx("input", { id: "tg-chat", className: fieldCls, inputMode: "numeric", value: chat, placeholder: "\u043D\u0430\u043F\u0438\u0448\u0438\u0442\u0435 \u0431\u043E\u0442\u0443 /start \u2014 id \u043F\u043E\u044F\u0432\u0438\u0442\u0441\u044F \u043D\u0438\u0436\u0435", onChange: (e) => setChat(e.target.value) })] }), tg.pending_chat && (_jsxs("div", { className: "flex flex-wrap items-center gap-2 text-sm", children: [_jsx("span", { className: "text-muted-foreground", children: "\u0431\u043E\u0442\u0443 \u043D\u0430\u043F\u0438\u0441\u0430\u043B" }), _jsxs("span", { className: "text-hi", children: [tg.pending_chat.title || "чат", " \u00B7 ", tg.pending_chat.id] }), _jsx(Button, { size: "sm", variant: "outline", disabled: busy, onClick: () => run(() => api.saveTelegram(tg.pending_chat.id), "чат сохранён"), children: "\u0438\u0441\u043F\u043E\u043B\u044C\u0437\u043E\u0432\u0430\u0442\u044C \u044D\u0442\u043E\u0442 \u0447\u0430\u0442" })] })), _jsxs("div", { className: "text-sm", children: [_jsx("span", { className: "text-muted-foreground", children: "\u0441\u0442\u0430\u0442\u0443\u0441" }), " ", status, tg.last_message && _jsxs("span", { className: "text-muted-foreground", children: [" \u00B7 \u043F\u043E\u0441\u043B\u0435\u0434\u043D\u044F\u044F \u043A\u043E\u043C\u0430\u043D\u0434\u0430 ", when(tg.last_message)] }), tg.last_sent && _jsxs("span", { className: "text-muted-foreground", children: [" \u00B7 \u043E\u0442\u043F\u0440\u0430\u0432\u043B\u0435\u043D\u043E ", when(tg.last_sent)] })] }), _jsxs("div", { className: "text-sm text-muted-foreground", children: ["\u043A\u043E\u043C\u0430\u043D\u0434\u044B: ", _jsx("span", { className: "text-hi", children: "/status" }), " \u00B7 ", _jsx("span", { className: "text-hi", children: "/server <\u0438\u043C\u044F>" }), " \u00B7", " ", _jsx("span", { className: "text-hi", children: "/alerts" }), " \u00B7 ", _jsx("span", { className: "text-hi", children: "/ack <id>" }), " \u00B7", " ", _jsx("span", { className: "text-hi", children: "/bills" }), " \u00B7 ", _jsx("span", { className: "text-hi", children: "/ask <\u0432\u043E\u043F\u0440\u043E\u0441>" })] }), _jsxs("div", { className: "flex flex-wrap gap-2", children: [_jsx(Button, { disabled: busy, onClick: () => run(() => api.saveTelegram(chat.trim(), token.trim() || undefined), "сохранено"), children: "\u0441\u043E\u0445\u0440\u0430\u043D\u0438\u0442\u044C" }), _jsx(Button, { variant: "outline", disabled: busy || !tg.configured || !tg.chat_id, onClick: () => run(api.testTelegram, "тест отправлен"), children: "\u043E\u0442\u043F\u0440\u0430\u0432\u0438\u0442\u044C \u0442\u0435\u0441\u0442" }), tg.configured && (_jsx(Button, { variant: "ghost", className: "hover:text-crit", disabled: busy, onClick: () => confirm("Отключить бота? Токен будет удалён.") && run(() => api.saveTelegram(chat.trim(), ""), "бот отключён"), children: "\u043E\u0442\u043A\u043B\u044E\u0447\u0438\u0442\u044C" }))] }), msg && _jsx("div", { className: cn("text-sm", msg.ok ? "text-primary" : "text-crit"), children: msg.text }), !tg.configured && (_jsxs("ol", { className: "list-decimal space-y-0.5 pl-5 text-xs text-muted-foreground", children: [_jsxs("li", { children: ["\u0412 Telegram \u043E\u0442\u043A\u0440\u043E\u0439\u0442\u0435 ", _jsx("span", { className: "text-hi", children: "@BotFather" }), " \u2192 ", _jsx("span", { className: "text-hi", children: "/newbot" }), ", \u0437\u0430\u0434\u0430\u0439\u0442\u0435 \u0438\u043C\u044F \u0438 username (\u043D\u0430 \u00ABbot\u00BB)."] }), _jsx("li", { children: "\u0421\u043A\u043E\u043F\u0438\u0440\u0443\u0439\u0442\u0435 \u0442\u043E\u043A\u0435\u043D \u0432 \u043F\u043E\u043B\u0435 \u0432\u044B\u0448\u0435 \u0438 \u043D\u0430\u0436\u043C\u0438\u0442\u0435 \u00AB\u0441\u043E\u0445\u0440\u0430\u043D\u0438\u0442\u044C\u00BB." }), _jsxs("li", { children: ["\u041D\u0430\u043F\u0438\u0448\u0438\u0442\u0435 \u0441\u0432\u043E\u0435\u043C\u0443 \u0431\u043E\u0442\u0443 ", _jsx("span", { className: "text-hi", children: "/start" }), " \u0438 \u043D\u0430\u0436\u043C\u0438\u0442\u0435 \u00AB\u0438\u0441\u043F\u043E\u043B\u044C\u0437\u043E\u0432\u0430\u0442\u044C \u044D\u0442\u043E\u0442 \u0447\u0430\u0442\u00BB."] })] }))] }) }));
}
// --- page ---
export function AlertsPage({ route, version, onLogout }) {
    const [view, setView] = useState(null);
    const [error, setError] = useState("");
    const [allHistory, setAllHistory] = useState(false);
    const load = useCallback(() => {
        api
            .alerts()
            .then((v) => {
            setView(v);
            setError("");
        })
            .catch((e) => {
            if (e?.status === 401)
                onLogout();
            else
                setError("нет связи с сервером");
        });
    }, [onLogout]);
    useEffect(() => {
        load();
        const t = setInterval(load, 10000);
        return () => clearInterval(t);
    }, [load]);
    const history = (view?.recent ?? []).slice(0, allHistory ? undefined : 30);
    return (_jsxs(Shell, { route: route, version: version, onLogout: onLogout, status: error && _jsx("span", { className: "text-crit", children: error }), footer: _jsxs(_Fragment, { children: [_jsx(Hotkey, { k: "1", children: "\u043E\u0431\u0437\u043E\u0440" }), _jsx(Hotkey, { k: "5", children: "\u0430\u043B\u0435\u0440\u0442\u044B" })] }), footerNote: "\u043E\u0431\u043D\u043E\u0432\u043B\u0435\u043D\u0438\u0435 10\u0441", children: [_jsx(Panel, { title: "\u0430\u043A\u0442\u0438\u0432\u043D\u044B\u0435 \u0430\u043B\u0435\u0440\u0442\u044B", right: view ? `${view.open.length} активных · ${view.last24h} за 24ч` : undefined, accent: !!view?.open.some((a) => a.severity === "crit" && a.state === "firing"), children: view && view.open.length === 0 ? (_jsx("div", { className: "py-2 text-primary", children: "\u2713 \u0432\u0441\u0451 \u0441\u043F\u043E\u043A\u043E\u0439\u043D\u043E" })) : (_jsx(ScrollArea, { className: "w-full", children: _jsx("table", { className: "w-full min-w-[720px] border-collapse text-left", children: _jsx("tbody", { children: (view?.open ?? []).map((a) => (_jsx(AlertRow, { a: a, onChange: load }, a.id))) }) }) })) }), _jsxs("div", { className: "grid grid-cols-1 gap-x-6 gap-y-7 xl:grid-cols-2", children: [_jsx(RulesPanel, {}), _jsx(TelegramPanel, {})] }), _jsxs(Panel, { title: "\u0438\u0441\u0442\u043E\u0440\u0438\u044F", right: "\u0441\u043E\u0431\u044B\u0442\u0438\u044F \u0438 \u0430\u043B\u0435\u0440\u0442\u044B \u0437\u0430 90 \u0434\u043D\u0435\u0439", children: [history.length === 0 ? (_jsx("div", { className: "py-2 text-muted-foreground", children: "\u043F\u043E\u043A\u0430 \u043F\u0443\u0441\u0442\u043E" })) : (_jsx(ScrollArea, { className: "w-full", children: _jsx("table", { className: "w-full min-w-[720px] border-collapse text-left", children: _jsx("tbody", { children: history.map((a) => (_jsxs("tr", { className: "border-t border-border/60 align-top", children: [_jsx("td", { className: "py-1 pr-3 whitespace-nowrap text-muted-foreground", children: when(a.started_at) }), _jsx("td", { className: cn("pr-3 uppercase", sevCls[a.severity]), children: a.severity }), _jsx("td", { className: "pr-3 whitespace-nowrap text-hi", children: a.server_name || "панель" }), _jsxs("td", { className: "pr-3", children: [a.message, a.notified && _jsx("span", { className: "text-muted-foreground", children: " \u2192 tg" })] }), _jsx("td", { className: "whitespace-nowrap text-right text-sm", children: stateText(a) })] }, a.id))) }) }) })), !allHistory && (view?.recent.length ?? 0) > 30 && (_jsxs(Button, { className: "mt-2", size: "sm", variant: "outline", onClick: () => setAllHistory(true), children: ["\u043F\u043E\u043A\u0430\u0437\u0430\u0442\u044C \u0432\u0441\u0435 (", view?.recent.length, ")"] }))] })] }));
}
