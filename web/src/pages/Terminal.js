import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useCallback, useEffect, useRef, useState } from "react";
import { AgentPanel } from "@/components/AgentPanel";
import { Hotkey, Shell } from "@/components/Shell";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { XTerm } from "@/components/XTerm";
import { api } from "@/lib/api";
import { href } from "@/lib/router";
import { cn } from "@/lib/utils";
let nextKey = 1;
export function TerminalPage({ serverId, route, version, onLogout, }) {
    const [servers, setServers] = useState(null);
    const [tabs, setTabs] = useState([]);
    const [active, setActive] = useState(0);
    const [picker, setPicker] = useState(false);
    const [size, setSize] = useState({});
    const opened = useRef(undefined);
    const [agentOpen, setAgentOpen] = useState(() => {
        try {
            return localStorage.getItem("srvmon.agent.panel") !== "0";
        }
        catch {
            return true;
        }
    });
    const askRef = useRef(null);
    // F9: show/hide the agent panel (and jump into its input).
    useEffect(() => {
        const onKey = (e) => {
            if (e.key !== "F9")
                return;
            e.preventDefault();
            setAgentOpen((o) => {
                const next = !o || document.activeElement !== askRef.current;
                try {
                    localStorage.setItem("srvmon.agent.panel", next ? "1" : "0");
                }
                catch {
                    // ignore
                }
                if (next)
                    setTimeout(() => askRef.current?.focus(), 0);
                return next;
            });
        };
        window.addEventListener("keydown", onKey);
        return () => window.removeEventListener("keydown", onKey);
    }, []);
    useEffect(() => {
        api
            .servers()
            .then(setServers)
            .catch((e) => e?.status === 401 && onLogout());
    }, [onLogout]);
    const open = useCallback((id, container) => {
        const key = nextKey++;
        setTabs((t) => [...t, { key, serverId: id, container, status: "connecting" }]);
        setActive(key);
        setPicker(false);
    }, []);
    // #/term/<id> opens a session once.
    useEffect(() => {
        if (!servers || !serverId || opened.current === serverId)
            return;
        opened.current = serverId;
        if (servers.some((s) => s.id === serverId && s.has_ssh))
            open(serverId);
        else
            setPicker(true);
    }, [servers, serverId, open]);
    useEffect(() => {
        if (servers && !serverId && tabs.length === 0)
            setPicker(true);
    }, [servers, serverId, tabs.length]);
    useEffect(() => {
        if (!picker || !tabs.length)
            return;
        const onKey = (e) => e.key === "Escape" && setPicker(false);
        window.addEventListener("keydown", onKey);
        return () => window.removeEventListener("keydown", onKey);
    }, [picker, tabs.length]);
    function close(key) {
        setTabs((t) => {
            const rest = t.filter((x) => x.key !== key);
            if (active === key)
                setActive(rest.length ? rest[rest.length - 1].key : 0);
            if (!rest.length)
                setPicker(true);
            return rest;
        });
    }
    const setStatus = useCallback((key, status) => setTabs((t) => t.map((x) => (x.key === key && x.status !== status ? { ...x, status } : x))), []);
    const byId = (id) => servers?.find((s) => s.id === id);
    const cur = tabs.find((t) => t.key === active);
    const curServer = cur ? byId(cur.serverId) : undefined;
    const withSSH = (servers ?? []).filter((s) => s.has_ssh);
    const withoutSSH = (servers ?? []).filter((s) => !s.has_ssh);
    const label = (t) => {
        const s = byId(t.serverId);
        const name = s?.name ?? `#${t.serverId}`;
        return t.container ? `${name}:${t.container}` : `${s?.ssh_user || "srvmon"}@${name}`;
    };
    return (_jsx(Shell, { route: route, version: version, onLogout: onLogout, footer: cur && curServer ? (_jsxs(_Fragment, { children: [_jsxs("span", { children: ["ssh ed25519 \u00B7 ", curServer.host, ":", curServer.port || 22] }), size[cur.key] && _jsx("span", { children: size[cur.key] }), _jsx("span", { children: "\u0447\u0435\u0440\u0435\u0437 srvmon-gateway" }), _jsx("span", { children: cur.status === "open" ? "подключено" : cur.status === "closed" ? "закрыто" : "подключение…" }), cur.status === "closed" && (_jsx("button", { type: "button", className: "text-primary", onClick: () => open(cur.serverId, cur.container), children: "\u27F3 \u043F\u0435\u0440\u0435\u043F\u043E\u0434\u043A\u043B\u044E\u0447\u0438\u0442\u044C" }))] })) : (_jsxs(_Fragment, { children: [_jsx(Hotkey, { k: "esc", children: "\u043A \u0441\u0435\u0441\u0441\u0438\u0438" }), _jsx("span", { className: "text-muted-foreground", children: "\u043D\u0435\u0442 \u043E\u0442\u043A\u0440\u044B\u0442\u044B\u0445 \u0441\u0435\u0441\u0441\u0438\u0439" })] })), footerNote: _jsxs(_Fragment, { children: [_jsx(Hotkey, { k: "F9", children: "\u0430\u0433\u0435\u043D\u0442 \u21C4 \u0442\u0435\u0440\u043C\u0438\u043D\u0430\u043B" }), " \u00B7 \u043F\u0440\u0438\u0432\u0430\u0442\u043D\u044B\u0439 \u043A\u043B\u044E\u0447 \u043D\u0435 \u043F\u043E\u043A\u0438\u0434\u0430\u0435\u0442 \u0441\u0435\u0440\u0432\u0435\u0440 \u043C\u043E\u043D\u0438\u0442\u043E\u0440\u0438\u043D\u0433\u0430"] }), children: _jsxs("div", { className: "flex min-h-0 flex-1 flex-col gap-4 lg:flex-row", children: [_jsxs("div", { className: "flex min-h-0 min-w-0 flex-1 flex-col", children: [_jsxs("div", { className: "flex items-end gap-1", children: [_jsx("div", { className: "flex min-w-0 flex-1 items-end gap-1 overflow-x-auto [scrollbar-width:thin]", children: tabs.map((t, i) => (_jsxs("div", { className: cn("flex h-8 shrink-0 items-center border border-b-0", t.key === active ? "border-primary bg-[#12361d] text-hi" : "border-border"), children: [_jsxs("button", { type: "button", className: "h-full pl-3 pr-2 whitespace-nowrap", onClick: () => setActive(t.key), children: [_jsx("span", { className: cn("mr-1.5", t.status === "open" ? "text-primary" : t.status === "closed" ? "text-crit" : "text-warn"), children: "\u25CF" }), "[", i + 1, "] ", label(t)] }), _jsx("button", { type: "button", className: "h-full pr-2.5 text-muted-foreground hover:text-crit", "aria-label": "\u0437\u0430\u043A\u0440\u044B\u0442\u044C \u0441\u0435\u0441\u0441\u0438\u044E", onClick: () => close(t.key), children: "\u00D7" })] }, t.key))) }), _jsx(Button, { variant: "outline", className: "h-8", onClick: () => setPicker((p) => !p), children: "+ \u0441\u0435\u0441\u0441\u0438\u044F" })] }), _jsxs("div", { className: cn("relative min-h-[420px] flex-1 border bg-field", tabs.length ? "border-primary" : "border-border"), children: [tabs.map((t) => (_jsx("div", { className: cn("absolute inset-0 p-2", t.key !== active && "invisible"), children: _jsx(XTerm, { serverId: t.serverId, container: t.container, active: t.key === active && !picker, onStatus: (s) => setStatus(t.key, s), onSize: (c, r) => setSize((m) => (m[t.key] === `${c}×${r}` ? m : { ...m, [t.key]: `${c}×${r}` })) }) }, t.key))), picker && (_jsx("div", { className: "absolute inset-0 z-10 overflow-auto bg-background/95 p-5", children: _jsxs(Panel, { title: "\u043D\u043E\u0432\u0430\u044F \u0441\u0435\u0441\u0441\u0438\u044F", className: "mx-auto max-w-3xl", children: [servers === null && _jsx("div", { className: "text-muted-foreground", children: "\u0437\u0430\u0433\u0440\u0443\u0437\u043A\u0430\u2026" }), servers && withSSH.length === 0 && (_jsxs("div", { className: "text-muted-foreground", children: ["\u043D\u0435\u0442 \u0441\u0435\u0440\u0432\u0435\u0440\u043E\u0432 \u0441 ssh-\u0434\u043E\u0441\u0442\u0443\u043F\u043E\u043C. \u0414\u043E\u0431\u0430\u0432\u044C\u0442\u0435 \u0441\u0435\u0440\u0432\u0435\u0440 \u0447\u0435\u0440\u0435\u0437", " ", _jsx("a", { className: "text-primary", href: href.add(), children: "\u043C\u0430\u0441\u0442\u0435\u0440" }), "."] })), _jsx("div", { className: "flex flex-col", children: withSSH.map((s) => {
                                                    const containers = (s.snapshot?.report.containers ?? []).filter((c) => c.state === "running");
                                                    return (_jsxs("div", { className: "border-t border-border/60 py-2 first:border-t-0", children: [_jsxs("div", { className: "flex flex-wrap items-center gap-3", children: [_jsx("span", { className: s.online ? "text-primary" : "text-crit", children: s.online ? "●" : "○" }), _jsx("span", { className: "text-hi", children: s.name }), _jsxs("span", { className: "text-muted-foreground", children: [s.ssh_user, "@", s.host, s.port && s.port !== 22 ? `:${s.port}` : ""] }), _jsx("span", { className: "flex-1" }), _jsx(Button, { size: "sm", onClick: () => open(s.id), children: ">_ shell" })] }), containers.length > 0 && (_jsxs("div", { className: "mt-1.5 flex flex-wrap gap-1.5 pl-6", children: [_jsx("span", { className: "text-xs text-muted-foreground", children: "docker exec:" }), containers.map((c) => (_jsx(Button, { size: "sm", variant: "outline", onClick: () => open(s.id, c.name), children: c.name }, c.id)))] }))] }, s.id));
                                                }) }), withoutSSH.length > 0 && (_jsxs("div", { className: "mt-4 text-xs text-muted-foreground", children: ["\u0431\u0435\u0437 ssh-\u0434\u043E\u0441\u0442\u0443\u043F\u0430:", " ", withoutSSH.map((s, i) => (_jsxs("span", { children: [i > 0 && ", ", _jsx("a", { className: "text-primary", href: href.add(s.id), children: s.name })] }, s.id))), " ", "\u2014 \u043D\u0430\u0436\u043C\u0438\u0442\u0435, \u0447\u0442\u043E\u0431\u044B \u043F\u043E\u0434\u043A\u043B\u044E\u0447\u0438\u0442\u044C"] })), tabs.length > 0 && (_jsx("div", { className: "mt-4", children: _jsx(Button, { variant: "ghost", size: "sm", onClick: () => setPicker(false), children: "esc \u00B7 \u0432\u0435\u0440\u043D\u0443\u0442\u044C\u0441\u044F \u043A \u0441\u0435\u0441\u0441\u0438\u0438" }) }))] }) }))] })] }), agentOpen && (_jsx(AgentPanel, { context: curServer ? { id: curServer.id, name: curServer.name } : undefined, inputRef: askRef, className: "h-[560px] lg:h-auto lg:max-h-[calc(100vh-8.5rem)] lg:min-h-[420px] lg:w-[460px] lg:shrink-0" }))] }) }));
}
