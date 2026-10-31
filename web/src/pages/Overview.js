import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useEffect, useMemo, useState } from "react";
import { Hotkey, Shell } from "@/components/Shell";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { ScrollArea } from "@/components/ui/scroll-area";
import { api } from "@/lib/api";
import { href, navigate } from "@/lib/router";
import { cn, formatBytes, formatUptime } from "@/lib/utils";
function level(p) {
    if (p >= 90)
        return "text-crit";
    if (p >= 70)
        return "text-warn";
    return "text-primary";
}
// Text meter like btop: ▮▮▮▮▯▯▯▯▯▯
function Meter({ value }) {
    const cells = 10;
    const filled = Math.round((Math.min(100, Math.max(0, value)) / 100) * cells);
    return (_jsxs("span", { className: cn("whitespace-nowrap", level(value)), "aria-label": `${value.toFixed(0)}%`, children: ["▮".repeat(filled), _jsx("span", { className: "text-border", children: "▮".repeat(cells - filled) }), _jsxs("span", { className: "ml-2 inline-block w-10 text-right text-foreground", children: [value.toFixed(0), "%"] })] }));
}
function Row({ s, onDelete }) {
    const r = s.snapshot?.report;
    const memPct = r && r.host.mem_total > 0 ? ((r.host.mem_total - r.host.mem_available) / r.host.mem_total) * 100 : 0;
    const disk = r?.host.disks?.reduce((max, d) => Math.max(max, d.total ? (d.used / d.total) * 100 : 0), 0) ?? 0;
    const containers = r?.containers ?? [];
    const running = containers.filter((c) => c.state === "running").length;
    const bad = containers.filter((c) => c.state !== "running" || c.health === "unhealthy").length;
    return (_jsxs("tr", { className: "cursor-pointer border-t border-border/60 hover:bg-secondary/60 focus-visible:bg-secondary", tabIndex: 0, onClick: () => navigate(href.server(s.id)), onKeyDown: (e) => {
            const row = e.currentTarget;
            if (e.key === "Enter")
                navigate(href.server(s.id));
            else if (e.key === "ArrowDown")
                row.nextElementSibling?.focus();
            else if (e.key === "ArrowUp")
                row.previousElementSibling?.focus();
            else
                return;
            e.preventDefault();
        }, children: [_jsxs("td", { className: "py-1.5 pr-4", children: [_jsx("span", { className: s.online ? "text-primary" : "text-crit", children: s.online ? "●" : "○" }), " ", _jsx("a", { href: href.server(s.id), className: "text-hi hover:text-primary", onClick: (e) => e.stopPropagation(), children: s.name }), s.description && _jsx("div", { className: "text-xs text-muted-foreground", children: s.description })] }), _jsx("td", { className: "pr-4 text-muted-foreground", children: s.group || "—" }), _jsx("td", { className: "pr-4", children: r ? _jsx(Meter, { value: r.host.cpu_percent }) : "—" }), _jsx("td", { className: "pr-4", children: r ? _jsx(Meter, { value: memPct }) : "—" }), _jsx("td", { className: "pr-4", children: r ? _jsx(Meter, { value: disk }) : "—" }), _jsx("td", { className: "pr-4 whitespace-nowrap", children: r?.docker_ok ? (_jsxs(_Fragment, { children: [_jsx("span", { className: "text-primary", children: running }), _jsxs("span", { className: "text-muted-foreground", children: ["/", containers.length] }), bad > 0 && _jsxs("span", { className: "ml-2 text-crit", children: ["!", bad] })] })) : (_jsx("span", { className: "text-muted-foreground", children: "\u2014" })) }), _jsx("td", { className: "pr-4 whitespace-nowrap text-muted-foreground", children: r ? `${formatUptime(r.host.uptime_sec)} · ${formatBytes(r.host.mem_total)}` : "нет данных" }), _jsxs("td", { className: "whitespace-nowrap text-muted-foreground", children: [s.cost != null ? `${s.cost} ${s.currency}` : "—", s.next_payment && _jsxs("div", { className: "text-xs", children: ["\u0434\u043E ", s.next_payment] })] }), _jsxs("td", { className: "whitespace-nowrap pl-2 text-right", onClick: (e) => e.stopPropagation(), onKeyDown: (e) => e.stopPropagation(), children: [s.has_ssh ? (_jsx(Button, { size: "sm", variant: "outline", asChild: true, children: _jsx("a", { href: href.terminal(s.id), title: "\u0442\u0435\u0440\u043C\u0438\u043D\u0430\u043B", children: ">_" }) })) : (_jsx(Button, { size: "sm", variant: "ghost", asChild: true, children: _jsx("a", { href: href.add(s.id), title: "\u043F\u043E\u0434\u043A\u043B\u044E\u0447\u0438\u0442\u044C ssh-\u0434\u043E\u0441\u0442\u0443\u043F", children: "+ ssh" }) })), _jsx(Button, { size: "sm", variant: "ghost", className: "hover:text-crit", title: "\u0443\u0434\u0430\u043B\u0438\u0442\u044C", onClick: () => onDelete(s), children: "\u00D7" })] })] }));
}
function AlertsPanel() {
    const [sum, setSum] = useState(null);
    useEffect(() => {
        let alive = true;
        const load = () => api
            .alertSummary()
            .then((s) => alive && setSum(s))
            .catch(() => undefined);
        load();
        const t = setInterval(load, 15000);
        return () => {
            alive = false;
            clearInterval(t);
        };
    }, []);
    if (!sum)
        return null;
    const sev = { crit: "text-crit", warn: "text-warn", info: "text-tx" };
    return (_jsxs(Panel, { title: "\u0430\u043B\u0435\u0440\u0442\u044B", right: `${sum.open} активных · ${sum.last24h} за 24ч`, children: [sum.latest.length === 0 ? (_jsx("div", { className: "text-primary", children: "\u2713 \u0430\u043A\u0442\u0438\u0432\u043D\u044B\u0445 \u0430\u043B\u0435\u0440\u0442\u043E\u0432 \u043D\u0435\u0442" })) : (_jsx("div", { className: "flex flex-col gap-0.5", children: sum.latest.map((a) => (_jsxs("div", { className: "truncate", children: [_jsx("span", { className: "text-muted-foreground", children: new Date(a.started_at).toLocaleTimeString("ru", { hour: "2-digit", minute: "2-digit" }) }), " ", _jsx("span", { className: sev[a.severity], children: a.severity.toUpperCase() }), " ", _jsx("span", { className: "text-hi", children: a.server_name || "панель" }), " \u2014 ", a.message, a.state === "acked" && _jsx("span", { className: "text-primary", children: " \u2713" }), a.notified && _jsx("span", { className: "text-muted-foreground", children: " \u2192 tg" })] }, a.id))) })), _jsx("div", { className: "mt-2.5", children: _jsx(Button, { size: "sm", variant: "outline", asChild: true, children: _jsx("a", { href: href.alerts(), children: "\u0432\u0441\u0435 \u0430\u043B\u0435\u0440\u0442\u044B \u0438 \u043F\u0440\u0430\u0432\u0438\u043B\u0430 \u2192" }) }) })] }));
}
export function Overview({ route, version, onLogout }) {
    const [servers, setServers] = useState(null);
    const [error, setError] = useState("");
    const [group, setGroup] = useState("все");
    const [reload, setReload] = useState(0);
    const remove = async (s) => {
        if (!confirm(`Удалить ${s.name} из панели? Ключ и токен агента будут отозваны, сам сервер не трогаем.`))
            return;
        await api.deleteServer(s.id).catch(() => setError("не удалось удалить"));
        setReload((n) => n + 1);
    };
    useEffect(() => {
        let alive = true;
        const load = () => api
            .servers()
            .then((s) => alive && (setServers(s), setError("")))
            .catch((e) => {
            if (!alive)
                return;
            if (e?.status === 401)
                onLogout();
            else
                setError("нет связи с сервером");
        });
        load();
        const t = setInterval(load, 5000);
        return () => {
            alive = false;
            clearInterval(t);
        };
    }, [onLogout, reload]);
    const groups = useMemo(() => ["все", ...new Set((servers ?? []).map((s) => s.group).filter(Boolean))], [servers]);
    const visible = (servers ?? []).filter((s) => group === "все" || s.group === group);
    const online = (servers ?? []).filter((s) => s.online).length;
    return (_jsxs(Shell, { route: route, version: version, onLogout: onLogout, status: _jsxs(_Fragment, { children: [error && _jsx("span", { className: "text-crit", children: error }), _jsxs("span", { children: [_jsx("span", { className: online === (servers?.length ?? 0) ? "text-primary" : "text-warn", children: "\u25CF" }), " ", online, "/", servers?.length ?? 0, " up"] }), _jsx(Button, { size: "sm", asChild: true, children: _jsx("a", { href: href.add(), children: "+ \u0441\u0435\u0440\u0432\u0435\u0440" }) })] }), footer: _jsxs(_Fragment, { children: [_jsx(Hotkey, { k: "\u2191\u2193", children: "\u0432\u044B\u0431\u043E\u0440" }), _jsx(Hotkey, { k: "enter", children: "\u044D\u043A\u0440\u0430\u043D \u0441\u0435\u0440\u0432\u0435\u0440\u0430" }), _jsx(Hotkey, { k: "3", children: "\u0442\u0435\u0440\u043C\u0438\u043D\u0430\u043B" }), _jsx(Hotkey, { k: "5", children: "\u0430\u043B\u0435\u0440\u0442\u044B" })] }), footerNote: "\u043E\u0431\u043D\u043E\u0432\u043B\u0435\u043D\u0438\u0435 5\u0441", children: [_jsx("nav", { className: "flex flex-wrap gap-2", "aria-label": "\u0433\u0440\u0443\u043F\u043F\u044B", children: groups.map((g) => (_jsx(Button, { size: "sm", variant: g === group ? "default" : "outline", onClick: () => setGroup(g), children: g }, g))) }), _jsx(Panel, { title: "\u0441\u0435\u0440\u0432\u0435\u0440\u044B", right: "\u043E\u0431\u043D\u043E\u0432\u043B\u0435\u043D\u0438\u0435 5\u0441", children: servers && servers.length === 0 ? (_jsxs("div", { className: "py-6 text-muted-foreground", children: ["\u0441\u0435\u0440\u0432\u0435\u0440\u043E\u0432 \u043F\u043E\u043A\u0430 \u043D\u0435\u0442.", " ", _jsx("a", { className: "text-primary", href: href.add(), children: "\u0414\u043E\u0431\u0430\u0432\u044C\u0442\u0435 \u043F\u0435\u0440\u0432\u044B\u0439" }), ": \u043C\u0430\u0441\u0442\u0435\u0440 \u0441\u0433\u0435\u043D\u0435\u0440\u0438\u0440\u0443\u0435\u0442 ssh-\u043A\u043B\u044E\u0447, \u043F\u0440\u043E\u043F\u0438\u0448\u0435\u0442 \u0435\u0433\u043E \u043F\u043E \u043F\u0430\u0440\u043E\u043B\u044E \u0438 \u043F\u043E\u0441\u0442\u0430\u0432\u0438\u0442 \u0430\u0433\u0435\u043D\u0442\u0430."] })) : (_jsx(ScrollArea, { className: "w-full", children: _jsxs("table", { className: "w-full min-w-[1000px] border-collapse text-left", children: [_jsx("thead", { className: "text-xs text-muted-foreground", children: _jsxs("tr", { children: [_jsx("th", { className: "pb-2 font-normal", children: "\u0441\u0435\u0440\u0432\u0435\u0440" }), _jsx("th", { className: "pb-2 font-normal", children: "\u0433\u0440\u0443\u043F\u043F\u0430" }), _jsx("th", { className: "pb-2 font-normal", children: "cpu" }), _jsx("th", { className: "pb-2 font-normal", children: "ram" }), _jsx("th", { className: "pb-2 font-normal", children: "\u0434\u0438\u0441\u043A" }), _jsx("th", { className: "pb-2 font-normal", children: "docker" }), _jsx("th", { className: "pb-2 font-normal", children: "uptime \u00B7 ram" }), _jsx("th", { className: "pb-2 font-normal", children: "\u043E\u043F\u043B\u0430\u0442\u0430" }), _jsx("th", { className: "pb-2 font-normal" })] }) }), _jsx("tbody", { children: visible.map((s) => (_jsx(Row, { s: s, onDelete: remove }, s.id))) })] }) })) }), _jsx(AlertsPanel, {})] }));
}
