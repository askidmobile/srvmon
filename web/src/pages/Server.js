import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import * as React from "react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Hotkey, Shell } from "@/components/Shell";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { api, ApiError, historyRanges, } from "@/lib/api";
import { href, navigate, typingIn } from "@/lib/router";
import { cn, formatBytes, formatUptime } from "@/lib/utils";
const C = {
    ok: "#3ddc6e",
    okDark: "#1f8f45",
    warn: "#f2c14e",
    crit: "#ff6b5e",
    tx: "#5ab0ff",
    cached: "#9b8cff",
    muted: "#7f9c86",
};
function levelColor(p) {
    if (p >= 85)
        return C.crit;
    if (p >= 60)
        return C.warn;
    return C.ok;
}
const pct = (v) => `${v >= 10 || v === 0 ? v.toFixed(0) : v.toFixed(1)}%`;
const rate = (bps) => `${formatBytes(bps)}/s`;
function ago(iso) {
    if (!iso)
        return "—";
    const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
    if (s < 60)
        return `${s}с назад`;
    if (s < 3600)
        return `${Math.floor(s / 60)}м назад`;
    if (s < 86400)
        return `${Math.floor(s / 3600)}ч назад`;
    return `${Math.floor(s / 86400)}д назад`;
}
function ruDate(ymd) {
    const [y, m, d] = ymd.split("-");
    return y && m && d ? `${d}.${m}.${y}` : ymd;
}
function daysUntil(ymd) {
    const t = new Date(`${ymd}T00:00:00`).getTime();
    if (Number.isNaN(t))
        return null;
    const today = new Date();
    today.setHours(0, 0, 0, 0);
    return Math.round((t - today.getTime()) / 86400000);
}
// --- small building blocks ---
function Track({ value, color, className }) {
    const v = Math.min(100, Math.max(0, value));
    return (_jsx("div", { className: cn("flex h-2 min-w-0 flex-auto bg-[#16261b]", className), children: _jsx("div", { className: "h-full", style: { width: `${v}%`, background: color ?? levelColor(v) } }) }));
}
// Column graph like btop: one bar per bucket, empty buckets stay blank.
function Bars({ values, max, height, color, down = false, grid = false, label, }) {
    return (_jsx("div", { role: "img", "aria-label": label, className: cn("flex w-full gap-px", down ? "items-start" : "items-end"), style: {
            height,
            backgroundImage: grid ? "linear-gradient(to top, rgba(42,74,51,.5) 1px, transparent 1px)" : undefined,
            backgroundSize: grid ? `100% ${height / 4}px` : undefined,
        }, children: values.map((v, i) => (_jsx("div", { className: "min-w-0 flex-1", style: {
                height: v == null || max <= 0 ? 0 : `${Math.max(v > 0 ? 2 : 0, Math.min(100, (v / max) * 100))}%`,
                background: color,
            } }, i))) }));
}
function Empty({ children }) {
    return _jsx("div", { className: "py-4 text-muted-foreground", children: children });
}
const scrollBox = "overflow-auto [scrollbar-color:#2a4a33_transparent] [scrollbar-width:thin]";
// --- panels ---
function CpuPanel({ data, hist }) {
    const h = data.snapshot?.report.host;
    const d = data.detail;
    const inv = data.inventory;
    const right = [
        inv?.cpu_model,
        h && `${h.cpu_cores} vCPU`,
        h && `load ${h.load1.toFixed(2)} ${h.load5.toFixed(2)} ${h.load15.toFixed(2)}`,
        d?.temp_c != null && `${d.temp_c.toFixed(0)}°C`,
    ]
        .filter(Boolean)
        .join(" · ");
    const cols = d && d.cores.length > 16 ? 4 : 2;
    return (_jsx(Panel, { title: "cpu", right: right, children: _jsxs("div", { className: "flex flex-wrap gap-4", children: [_jsxs("div", { className: "min-w-0 flex-[999_1_380px]", children: [_jsxs("div", { className: "flex flex-wrap items-baseline justify-between gap-x-4", children: [_jsx("span", { className: "text-xl font-bold text-hi", children: h ? pct(h.cpu_percent) : "—" }), _jsxs("span", { className: "text-xs text-muted-foreground", children: [hist?.range ?? "", d &&
                                            ` · usr ${pct(d.cpu_user)} · sys ${pct(d.cpu_system)} · iowait ${pct(d.cpu_iowait)} · steal ${pct(d.cpu_steal)}`] })] }), _jsx(Bars, { values: hist?.cpu ?? [], max: 100, height: 132, color: `linear-gradient(to top, ${C.okDark}, ${C.ok})`, grid: true, label: "\u0437\u0430\u0433\u0440\u0443\u0437\u043A\u0430 cpu" })] }), _jsx("div", { className: "grid min-w-0 flex-[1_1_260px] content-start gap-x-4 gap-y-1.5 pt-1.5", style: { gridTemplateColumns: `repeat(${cols}, minmax(0, 1fr))` }, children: d ? (d.cores.map((v, i) => (_jsxs("div", { className: "grid grid-cols-[28px_minmax(0,1fr)_40px] items-center gap-2", children: [_jsxs("span", { className: "text-muted-foreground", children: ["C", i] }), _jsx(Track, { value: v }), _jsx("span", { className: "text-right", children: pct(v) })] }, i)))) : (_jsx("span", { className: "col-span-full text-xs text-muted-foreground", children: "\u0437\u0430\u0433\u0440\u0443\u0437\u043A\u0430 \u043F\u043E \u044F\u0434\u0440\u0430\u043C \u043F\u043E\u044F\u0432\u0438\u0442\u0441\u044F \u0447\u0435\u0440\u0435\u0437 \u043F\u0430\u0440\u0443 \u0441\u0435\u043A\u0443\u043D\u0434" })) })] }) }));
}
function MemRow({ name, value, total, color }) {
    const p = total > 0 ? (value / total) * 100 : 0;
    return (_jsxs("div", { className: "grid grid-cols-[96px_minmax(0,1fr)_44px_56px] items-center gap-2.5", children: [_jsx("span", { className: "truncate text-muted-foreground", title: name, children: name }), _jsx(Track, { value: p, color: color }), _jsx("span", { className: "text-right", children: pct(p) }), _jsx("span", { className: "text-right text-hi", children: formatBytes(value) })] }));
}
function MemoryPanel({ data }) {
    const h = data.snapshot?.report.host;
    if (!h)
        return _jsx(Panel, { title: "\u043F\u0430\u043C\u044F\u0442\u044C", children: _jsx(Empty, { children: "\u043D\u0435\u0442 \u0434\u0430\u043D\u043D\u044B\u0445" }) });
    const used = Math.max(0, h.mem_total - h.mem_available);
    const swapUsed = Math.max(0, h.swap_total - h.swap_free);
    const d = data.detail;
    return (_jsxs(Panel, { title: "\u043F\u0430\u043C\u044F\u0442\u044C", right: formatBytes(h.mem_total), className: "min-w-0 flex-[1_1_360px]", children: [_jsxs("div", { className: "flex flex-col gap-2", children: [_jsx(MemRow, { name: "used", value: used, total: h.mem_total, color: C.ok }), _jsx(MemRow, { name: "available", value: h.mem_available, total: h.mem_total, color: C.tx }), h.mem_cached != null && _jsx(MemRow, { name: "cached", value: h.mem_cached, total: h.mem_total, color: C.cached }), h.mem_free != null && _jsx(MemRow, { name: "free", value: h.mem_free, total: h.mem_total, color: C.muted }), h.swap_total > 0 ? (_jsx(MemRow, { name: "swap", value: swapUsed, total: h.swap_total, color: C.warn })) : (_jsx("div", { className: "text-xs text-muted-foreground", children: "swap \u0432\u044B\u043A\u043B\u044E\u0447\u0435\u043D" }))] }), _jsxs("div", { className: "mt-2.5 flex flex-col gap-1 border-t border-border pt-2", children: [(h.disks ?? []).map((disk) => {
                        const p = disk.total ? (disk.used / disk.total) * 100 : 0;
                        return (_jsxs("div", { className: "grid grid-cols-[96px_minmax(0,1fr)_44px_56px] items-center gap-2.5", children: [_jsx("span", { className: "truncate text-muted-foreground", title: `${disk.mount} (${disk.fs})`, children: disk.mount }), _jsx(Track, { value: p }), _jsx("span", { className: "text-right", children: pct(p) }), _jsx("span", { className: "text-right text-hi", title: `занято ${formatBytes(disk.used)}`, children: formatBytes(disk.total) })] }, disk.mount));
                    }), d && (_jsxs("div", { className: "text-muted-foreground", children: ["io: read ", _jsx("span", { className: "text-hi", children: rate(d.disk_read_bps) }), " \u00B7 write", " ", _jsx("span", { className: "text-hi", children: rate(d.disk_write_bps) })] }))] })] }));
}
function lastValue(xs) {
    if (!xs)
        return null;
    for (let i = xs.length - 1; i >= 0; i--)
        if (xs[i] != null)
            return xs[i];
    return null;
}
function NetPanel({ data, hist }) {
    const net = data.snapshot?.report.host.net ?? [];
    const rx = hist?.rx ?? [];
    const tx = hist?.tx ?? [];
    const peakRx = Math.max(0, ...rx.map((v) => v ?? 0));
    const peakTx = Math.max(0, ...tx.map((v) => v ?? 0));
    const max = Math.max(peakRx, peakTx, 1);
    const totalRx = net.reduce((a, n) => a + n.rx_bytes, 0);
    const totalTx = net.reduce((a, n) => a + n.tx_bytes, 0);
    const right = [net.map((n) => n.name).join(", "), data.inventory?.ips?.[0]].filter(Boolean).join(" · ");
    return (_jsxs(Panel, { title: "\u0441\u0435\u0442\u044C", right: right, className: "min-w-0 flex-[1_1_360px]", children: [_jsxs("div", { className: "flex flex-wrap justify-between gap-x-3", children: [_jsxs("span", { children: ["\u2193 rx ", _jsx("span", { className: "font-bold text-primary", children: rate(lastValue(rx) ?? 0) })] }), _jsxs("span", { className: "text-muted-foreground", children: ["\u043F\u0438\u043A ", rate(peakRx), " \u00B7 \u0432\u0441\u0435\u0433\u043E ", formatBytes(totalRx)] })] }), _jsx(Bars, { values: rx, max: max, height: 56, color: C.ok, label: "\u0432\u0445\u043E\u0434\u044F\u0449\u0438\u0439 \u0442\u0440\u0430\u0444\u0438\u043A" }), _jsx("div", { className: "border-t border-border" }), _jsx(Bars, { values: tx, max: max, height: 40, color: C.tx, down: true, label: "\u0438\u0441\u0445\u043E\u0434\u044F\u0449\u0438\u0439 \u0442\u0440\u0430\u0444\u0438\u043A" }), _jsxs("div", { className: "flex flex-wrap justify-between gap-x-3", children: [_jsxs("span", { children: ["\u2191 tx ", _jsx("span", { className: "font-bold text-tx", children: rate(lastValue(tx) ?? 0) })] }), _jsxs("span", { className: "text-muted-foreground", children: ["\u043F\u0438\u043A ", rate(peakTx), " \u00B7 \u0432\u0441\u0435\u0433\u043E ", formatBytes(totalTx), data.detail && ` · tcp ${data.detail.tcp_conns}`] })] })] }));
}
function ProcessPanel({ data, filterRef }) {
    const [filter, setFilter] = useState("");
    const [sort, setSort] = useState("cpu");
    const d = data.detail;
    const rows = useMemo(() => {
        const q = filter.trim().toLowerCase();
        const list = (d?.procs ?? []).filter((p) => !q ||
            p.name.toLowerCase().includes(q) ||
            (p.container ?? "").toLowerCase().includes(q) ||
            (p.cmd ?? "").toLowerCase().includes(q) ||
            String(p.pid) === q);
        const by = {
            cpu: (a, b) => b.cpu - a.cpu || b.mem - a.mem,
            mem: (a, b) => b.mem - a.mem,
            pid: (a, b) => a.pid - b.pid,
            name: (a, b) => a.name.localeCompare(b.name),
        };
        return [...list].sort(by[sort]);
    }, [d, filter, sort]);
    const th = (key, label, right = false) => (_jsx("th", { className: cn("px-1.5 py-1 font-normal whitespace-nowrap", right && "text-right"), children: _jsxs("button", { type: "button", className: cn("cursor-pointer", sort === key ? "text-primary" : "hover:text-primary"), onClick: () => setSort(key), children: [label, sort === key && " ▾"] }) }));
    return (_jsxs(Panel, { title: "\u043F\u0440\u043E\u0446\u0435\u0441\u0441\u044B", right: d ? `сорт: ${sort} · ${d.procs_total} всего · топ ${d.procs.length}` : undefined, className: "flex min-w-0 flex-[1_1_380px] flex-col", children: [_jsxs("div", { className: "mb-2 flex gap-2", children: [_jsx("label", { htmlFor: "proc-filter", className: "self-center text-muted-foreground", children: "/" }), _jsx("input", { id: "proc-filter", ref: filterRef, value: filter, onChange: (e) => setFilter(e.target.value), onKeyDown: (e) => {
                            if (e.key === "Escape") {
                                setFilter("");
                                e.currentTarget.blur();
                                e.stopPropagation();
                            }
                        }, placeholder: "\u0444\u0438\u043B\u044C\u0442\u0440: \u0438\u043C\u044F, \u043A\u043E\u043D\u0442\u0435\u0439\u043D\u0435\u0440, pid", className: "h-[26px] min-w-0 flex-auto border border-input bg-field px-2 text-hi placeholder:text-muted-foreground" })] }), d ? (_jsxs("div", { className: cn(scrollBox, "max-h-[560px] flex-auto"), children: [_jsxs("table", { className: "w-full border-collapse text-left", children: [_jsx("thead", { className: "sticky top-0 bg-background text-muted-foreground", children: _jsxs("tr", { className: "border-b border-border", children: [th("pid", "pid", true), th("name", "программа"), _jsx("th", { className: "px-1.5 py-1 font-normal", children: "\u043A\u043E\u043D\u0442." }), th("cpu", "cpu", true), th("mem", "mem", true)] }) }), _jsx("tbody", { children: rows.map((p) => (_jsxs("tr", { className: "hover:bg-secondary/60", children: [_jsx("td", { className: "px-1.5 text-right text-muted-foreground", children: p.pid }), _jsx("td", { className: "max-w-[130px] truncate px-1.5 text-hi", title: `${p.cmd || p.name}${p.user ? `\nuser: ${p.user}` : ""}`, children: p.name }), _jsx("td", { className: "max-w-[90px] truncate px-1.5 text-muted-foreground", title: p.container, children: p.container || "─" }), _jsx("td", { className: "px-1.5 text-right", style: { color: levelColor(p.cpu / Math.max(1, d.cores.length) * 4) }, children: p.cpu.toFixed(1) }), _jsx("td", { className: "px-1.5 text-right whitespace-nowrap", children: formatBytes(p.mem) })] }, p.pid))) })] }), rows.length === 0 && _jsx(Empty, { children: "\u043D\u0438\u0447\u0435\u0433\u043E \u043D\u0435 \u043D\u0430\u0439\u0434\u0435\u043D\u043E" })] })) : (_jsx(Empty, { children: data.online
                    ? "агент переключается в детальный режим, процессы появятся через пару секунд"
                    : "сервер не на связи" }))] }));
}
function containerRank(c) {
    if (c.state !== "running" || c.health === "unhealthy")
        return 0;
    if (c.health === "starting" || (c.restarts ?? 0) > 0)
        return 1;
    return 2;
}
function DockerPanel({ data, onLogs }) {
    const r = data.snapshot?.report;
    const list = useMemo(() => [...(r?.containers ?? [])].sort((a, b) => containerRank(a) - containerRank(b) || a.name.localeCompare(b.name)), [r]);
    if (!r?.docker_ok) {
        return (_jsx(Panel, { title: "docker", children: _jsx(Empty, { children: "docker \u043D\u0435\u0434\u043E\u0441\u0442\u0443\u043F\u0435\u043D \u0430\u0433\u0435\u043D\u0442\u0443 (\u043D\u0435\u0442 /var/run/docker.sock) \u0438\u043B\u0438 \u043D\u0435 \u0443\u0441\u0442\u0430\u043D\u043E\u0432\u043B\u0435\u043D" }) }));
    }
    const running = list.filter((c) => c.state === "running").length;
    const projects = [...new Set(list.map((c) => c.compose).filter(Boolean))];
    const right = [`${running} running`, `${list.length - running} stopped`, projects.length > 0 && `compose: ${projects.join(", ")}`]
        .filter(Boolean)
        .join(" · ");
    return (_jsx(Panel, { title: "docker", right: right, children: _jsxs("div", { className: scrollBox, children: [_jsxs("table", { className: "w-full min-w-[960px] border-collapse text-left", children: [_jsx("thead", { className: "text-muted-foreground", children: _jsx("tr", { className: "border-b border-border", children: ["st", "имя", "образ", "статус", "cpu", "mem", "порты", "рест.", "health", ""].map((h, i) => (_jsx("th", { className: cn("px-1.5 py-1 font-normal", (h === "mem" || h === "рест.") && "text-right"), children: h }, i))) }) }), _jsx("tbody", { children: list.map((c) => {
                                const bad = c.state !== "running" || c.health === "unhealthy";
                                const warn = !bad && (c.health === "starting" || (c.restarts ?? 0) > 0);
                                return (_jsxs("tr", { className: "hover:bg-secondary/60", children: [_jsx("td", { className: cn("px-1.5", bad ? "text-crit" : warn ? "text-warn" : "text-primary"), children: bad ? "○" : warn ? "▲" : "●" }), _jsx("td", { className: "px-1.5 whitespace-nowrap text-hi", children: c.name }), _jsx("td", { className: "max-w-[280px] truncate px-1.5 text-muted-foreground", title: c.image, children: c.image }), _jsx("td", { className: "px-1.5 whitespace-nowrap", children: c.status }), _jsx("td", { className: "px-1.5", children: c.cpu_percent != null ? (_jsxs("div", { className: "flex w-[120px] items-center gap-1.5", children: [_jsx(Track, { value: c.cpu_percent }), _jsx("span", { className: "w-10 text-right", children: pct(c.cpu_percent) })] })) : (_jsx("span", { className: "text-muted-foreground", children: "\u2500" })) }), _jsx("td", { className: "px-1.5 text-right whitespace-nowrap", children: c.mem_bytes != null && c.state === "running" ? formatBytes(c.mem_bytes) : "─" }), _jsx("td", { className: "max-w-[200px] truncate px-1.5 text-muted-foreground", title: c.ports?.join(", "), children: c.ports?.length ? c.ports.join(", ") : "─" }), _jsx("td", { className: cn("px-1.5 text-right", (c.restarts ?? 0) > 0 && "text-warn"), children: c.restarts ?? "─" }), _jsx("td", { className: cn("px-1.5", c.health === "healthy" && "text-primary", c.health === "starting" && "text-warn", c.health === "unhealthy" && "text-crit", !c.health && "text-muted-foreground"), children: c.health || "─" }), _jsx("td", { className: "px-1.5", children: _jsx(Button, { size: "sm", variant: "outline", onClick: () => onLogs(c.name), title: "\u043F\u043E\u043A\u0430\u0437\u0430\u0442\u044C \u0441\u0442\u0440\u043E\u043A\u0438 \u044D\u0442\u043E\u0433\u043E \u043A\u043E\u043D\u0442\u0435\u0439\u043D\u0435\u0440\u0430 \u0432 \u043B\u043E\u0433\u0430\u0445", children: "\u043B\u043E\u0433\u0438" }) })] }, c.id));
                            }) })] }), list.length === 0 && _jsx(Empty, { children: "\u043A\u043E\u043D\u0442\u0435\u0439\u043D\u0435\u0440\u043E\u0432 \u043D\u0435\u0442" })] }) }));
}
function fmtLogTime(iso) {
    const d = new Date(iso);
    const t = d.toLocaleTimeString("ru-RU", { hour12: false });
    return d.toDateString() === new Date().toDateString()
        ? t
        : `${d.toLocaleDateString("ru-RU", { day: "2-digit", month: "2-digit" })} ${t}`;
}
function LogsPanel({ logs, source, setSource, watching, panelRef, }) {
    const [level, setLevel] = useState("all");
    const hourAgo = Date.now() - 3600_000;
    const errHour = logs.filter((l) => l.level === "ERR" && new Date(l.ts).getTime() > hourAgo).length;
    const rows = logs.filter((l) => (level === "all" || l.level === level) && (!source || l.source === source));
    return (_jsx("div", { ref: panelRef, className: "min-w-0 flex-[999_1_760px]", children: _jsxs(Panel, { title: "\u043B\u043E\u0433\u0438", right: `syslog + docker · warn и выше · ошибок за час: ${errHour}`, className: "h-full", children: [_jsxs("div", { className: "mb-2 flex flex-wrap items-center gap-2", children: [["all", "ERR", "WARN"].map((l) => (_jsx(Button, { size: "sm", variant: level === l ? "default" : "outline", onClick: () => setLevel(l), children: l === "all" ? "все" : l }, l))), source && (_jsxs(Button, { size: "sm", variant: "outline", onClick: () => setSource(""), title: "\u0441\u0431\u0440\u043E\u0441\u0438\u0442\u044C \u0444\u0438\u043B\u044C\u0442\u0440 \u0438\u0441\u0442\u043E\u0447\u043D\u0438\u043A\u0430", children: ["\u0438\u0441\u0442\u043E\u0447\u043D\u0438\u043A: ", source, " \u2715"] }))] }), _jsxs("div", { className: cn(scrollBox, "max-h-[360px]"), children: [_jsxs("table", { className: "w-full min-w-[640px] border-collapse text-left", children: [_jsx("thead", { className: "sticky top-0 bg-background text-muted-foreground", children: _jsxs("tr", { className: "border-b border-border", children: [_jsx("th", { className: "px-1.5 py-1 font-normal", children: "\u0432\u0440\u0435\u043C\u044F" }), _jsx("th", { className: "px-1.5 py-1 font-normal", children: "\u0443\u0440." }), _jsx("th", { className: "px-1.5 py-1 font-normal", children: "\u0438\u0441\u0442\u043E\u0447\u043D\u0438\u043A" }), _jsx("th", { className: "px-1.5 py-1 font-normal", children: "\u0441\u043E\u043E\u0431\u0449\u0435\u043D\u0438\u0435" })] }) }), _jsx("tbody", { children: rows.map((l, i) => (_jsxs("tr", { className: "align-top hover:bg-secondary/60", children: [_jsx("td", { className: "px-1.5 whitespace-nowrap text-muted-foreground", children: fmtLogTime(l.ts) }), _jsx("td", { className: cn("px-1.5", l.level === "ERR" ? "text-crit" : "text-warn"), children: l.level }), _jsx("td", { className: "px-1.5 whitespace-nowrap", children: _jsx("button", { type: "button", className: "cursor-pointer hover:text-primary", onClick: () => setSource(l.source), children: l.source }) }), _jsx("td", { className: "px-1.5 break-all", children: l.msg })] }, `${l.ts}-${i}`))) })] }), rows.length === 0 && (_jsx(Empty, { children: logs.length === 0
                                ? watching
                                    ? "предупреждений и ошибок не найдено (агент читает /var/log/syslog и логи контейнеров)"
                                    : "логи собираются, пока экран открыт"
                                : "нет строк под фильтр" }))] })] }) }));
}
function cardOf(s) {
    return {
        description: s.description,
        project: s.project,
        group: s.group,
        tags: s.tags,
        provider: s.provider,
        plan: s.plan,
        cost: s.cost ?? null,
        currency: s.currency,
        next_payment: s.next_payment,
    };
}
const fieldErrors = {
    description: "описание слишком длинное",
    project: "проект: до 64 символов",
    group: "группа: до 64 символов",
    provider: "провайдер: до 64 символов",
    plan: "тариф: до 128 символов",
    currency: "валюта: до 8 символов",
    next_payment: "дата оплаты в формате ГГГГ-ММ-ДД",
    cost: "стоимость должна быть числом ≥ 0",
    tags: "теги: до 20 штук, без пробелов, до 32 символов",
};
function PassportForm({ data, onDone }) {
    const [card, setCard] = useState(() => cardOf(data));
    const [tags, setTags] = useState(() => data.tags.map((t) => `#${t}`).join(" "));
    const [cost, setCost] = useState(() => (data.cost != null ? String(data.cost) : ""));
    const [error, setError] = useState("");
    const [busy, setBusy] = useState(false);
    const field = (key, label, opts = {}) => (_jsxs(_Fragment, { children: [_jsx("label", { htmlFor: `pp-${key}`, className: "self-center text-muted-foreground", children: label }), _jsx("input", { id: `pp-${key}`, type: opts.type ?? "text", placeholder: opts.placeholder, value: String(card[key] ?? ""), onChange: (e) => setCard({ ...card, [key]: e.target.value }), className: "h-7 min-w-0 border border-input bg-field px-2 text-hi placeholder:text-muted-foreground" })] }));
    const save = async (e) => {
        e.preventDefault();
        const c = cost.trim().replace(",", ".");
        const parsed = c === "" ? null : Number(c);
        if (parsed != null && !Number.isFinite(parsed)) {
            setError(fieldErrors.cost);
            return;
        }
        setBusy(true);
        setError("");
        try {
            await api.updateCard(data.id, {
                ...card,
                cost: parsed,
                tags: tags.split(/[\s,]+/).map((t) => t.replace(/^#/, "")).filter(Boolean),
            });
            onDone(true);
        }
        catch (err) {
            setError(err instanceof ApiError && err.status === 400 ? "проверьте поля формы" : "не удалось сохранить");
        }
        finally {
            setBusy(false);
        }
    };
    return (_jsxs("form", { onSubmit: save, className: "flex flex-col gap-3", children: [_jsxs("div", { className: "grid grid-cols-[120px_minmax(0,1fr)] gap-x-3 gap-y-1.5", children: [field("description", "описание"), field("project", "проект"), field("group", "группа", { placeholder: "prod / staging / infra" }), _jsx("label", { htmlFor: "pp-tags", className: "self-center text-muted-foreground", children: "\u0442\u0435\u0433\u0438" }), _jsx("input", { id: "pp-tags", value: tags, onChange: (e) => setTags(e.target.value), placeholder: "#db #public", className: "h-7 min-w-0 border border-input bg-field px-2 text-hi placeholder:text-muted-foreground" }), field("provider", "провайдер", { placeholder: "Hetzner" }), field("plan", "тариф", { placeholder: "CPX31" }), _jsx("label", { htmlFor: "pp-cost", className: "self-center text-muted-foreground", children: "\u0441\u0442\u043E\u0438\u043C\u043E\u0441\u0442\u044C" }), _jsxs("div", { className: "flex gap-2", children: [_jsx("input", { id: "pp-cost", inputMode: "decimal", value: cost, onChange: (e) => setCost(e.target.value), placeholder: "15.90", className: "h-7 min-w-0 flex-auto border border-input bg-field px-2 text-hi placeholder:text-muted-foreground" }), _jsx("input", { "aria-label": "\u0432\u0430\u043B\u044E\u0442\u0430", value: card.currency, onChange: (e) => setCard({ ...card, currency: e.target.value }), placeholder: "EUR", className: "h-7 w-16 border border-input bg-field px-2 text-hi placeholder:text-muted-foreground" })] }), field("next_payment", "след. оплата", { type: "date" })] }), error && _jsx("div", { className: "text-crit", children: error }), _jsxs("div", { className: "flex gap-2", children: [_jsx(Button, { type: "submit", size: "sm", disabled: busy, children: "\u0441\u043E\u0445\u0440\u0430\u043D\u0438\u0442\u044C" }), _jsx(Button, { type: "button", size: "sm", variant: "outline", onClick: () => onDone(false), children: "\u043E\u0442\u043C\u0435\u043D\u0430" })] })] }));
}
function Passport({ data, editing, setEditing, onSaved, }) {
    const inv = data.inventory;
    const r = data.snapshot?.report;
    const days = data.next_payment ? daysUntil(data.next_payment) : null;
    const kv = [
        ["описание", data.description || "—"],
        ["проект", data.project || "—"],
        ["группа", data.group || "—"],
        ["теги", data.tags.length ? data.tags.map((t) => `#${t}`).join(" ") : "—"],
        ["провайдер", data.provider || "—"],
        ["тариф", data.plan || "—"],
        [
            "стоимость",
            data.cost != null ? (_jsxs("span", { className: "text-hi", children: [data.cost, " ", data.currency] })) : ("—"),
        ],
        [
            "след. оплата",
            data.next_payment ? (_jsxs("span", { children: [ruDate(data.next_payment), days != null && (_jsxs("span", { className: cn("ml-1", days <= 3 ? "text-warn" : "text-muted-foreground"), children: ["\u00B7 ", days >= 0 ? `через ${days} дн` : `просрочено ${-days} дн`] }))] })) : ("—"),
        ],
        ["ssh", data.host ? `${data.host}:${data.port}` : "не настроен"],
        ["ос", inv?.os || "—"],
        ["ядро", inv?.kernel || "—"],
        ["docker", inv?.docker_version || (r?.docker_ok ? "есть" : "—")],
        ["ip", inv?.ips?.join(", ") || "—"],
        ["агент", r ? `${r.agent_version} · push ${ago(data.snapshot?.received)}` : "не подключён"],
    ];
    return (_jsx(Panel, { title: "\u043F\u0430\u0441\u043F\u043E\u0440\u0442", right: editing ? ("редактирование") : (_jsx("button", { type: "button", className: "cursor-pointer hover:text-primary", onClick: () => setEditing(true), children: "e \u0438\u0437\u043C\u0435\u043D\u0438\u0442\u044C" })), className: "min-w-0 flex-[1_1_380px]", children: editing ? (_jsx(PassportForm, { data: data, onDone: (saved) => {
                setEditing(false);
                if (saved)
                    onSaved();
            } })) : (_jsx("div", { className: "grid grid-cols-[120px_minmax(0,1fr)] gap-x-3 gap-y-1", children: kv.map(([k, v]) => (_jsxs(React.Fragment, { children: [_jsx("span", { className: "text-muted-foreground", children: k }), _jsx("span", { className: "min-w-0 break-words", children: v })] }, k))) })) }));
}
// --- page ---
const historyEvery = { "15m": 5000, "1h": 15000, "6h": 60000, "24h": 60000, "7d": 120000 };
function loadRange() {
    try {
        const v = localStorage.getItem("srvmon.range");
        if (v && historyRanges.includes(v))
            return v;
    }
    catch {
        /* storage unavailable */
    }
    return "15m";
}
export function ServerScreen({ id, route, version, onLogout, }) {
    const [data, setData] = useState(null);
    const [hist, setHist] = useState(null);
    const [error, setError] = useState("");
    const [missing, setMissing] = useState(false);
    const [range, setRangeState] = useState(loadRange);
    const [editing, setEditing] = useState(false);
    const [logSource, setLogSource] = useState("");
    const filterRef = useRef(null);
    const logsRef = useRef(null);
    const setRange = useCallback((r) => {
        setRangeState(r);
        try {
            localStorage.setItem("srvmon.range", r);
        }
        catch {
            /* ignore */
        }
    }, []);
    const fail = useCallback((e) => {
        if (e instanceof ApiError && e.status === 401)
            onLogout();
        else if (e instanceof ApiError && e.status === 404)
            setMissing(true);
        else
            setError("нет связи с сервером");
    }, [onLogout]);
    const load = useCallback(() => api
        .server(id)
        .then((d) => {
        setData(d);
        setError("");
    })
        .catch(fail), [id, fail]);
    // The screen polls every 2 s; while it does, the agent sends detailed data.
    useEffect(() => {
        let alive = true;
        const tick = () => alive && load();
        tick();
        const t = setInterval(tick, 2000);
        return () => {
            alive = false;
            clearInterval(t);
        };
    }, [load]);
    useEffect(() => {
        let alive = true;
        const tick = () => api
            .history(id, range)
            .then((h) => alive && setHist(h))
            .catch(() => undefined);
        tick();
        const t = setInterval(tick, historyEvery[range]);
        return () => {
            alive = false;
            clearInterval(t);
        };
    }, [id, range]);
    const showLogs = useCallback((name) => {
        setLogSource(name);
        logsRef.current?.scrollIntoView({ behavior: "smooth", block: "start" });
    }, []);
    useEffect(() => {
        const on = (e) => {
            if (typingIn(e) || e.ctrlKey || e.metaKey || e.altKey)
                return;
            const i = historyRanges.indexOf(range);
            switch (e.key) {
                case "Escape":
                    if (editing)
                        setEditing(false);
                    else
                        navigate(href.overview());
                    break;
                case "t":
                    navigate(href.terminal(id));
                    break;
                case "p":
                    filterRef.current?.focus();
                    break;
                case "l":
                    logsRef.current?.scrollIntoView({ behavior: "smooth", block: "start" });
                    break;
                case "e":
                    setEditing(true);
                    break;
                case "+":
                case "=":
                    setRange(historyRanges[Math.min(historyRanges.length - 1, i + 1)]);
                    break;
                case "-":
                    setRange(historyRanges[Math.max(0, i - 1)]);
                    break;
                default:
                    return;
            }
            e.preventDefault();
        };
        window.addEventListener("keydown", on);
        return () => window.removeEventListener("keydown", on);
    }, [id, range, editing, setRange]);
    const r = data?.snapshot?.report;
    const status = data && (_jsx("span", { className: data.online ? "text-primary" : "text-crit", children: data.online ? "● online" : "○ offline" }));
    let body;
    if (missing) {
        body = (_jsxs("p", { className: "text-muted-foreground", children: ["\u0421\u0435\u0440\u0432\u0435\u0440 \u043D\u0435 \u043D\u0430\u0439\u0434\u0435\u043D.", " ", _jsx("a", { href: href.overview(), className: "text-primary", children: "\u2190 \u043A \u043E\u0431\u0437\u043E\u0440\u0443" })] }));
    }
    else if (!data) {
        body = _jsx("p", { className: "text-muted-foreground", children: error || "загрузка…" });
    }
    else {
        const ip = data.inventory?.ips?.[0] || data.host;
        const facts = [
            ip,
            data.inventory?.os,
            data.provider,
            data.cost != null && `${data.cost} ${data.currency}`,
            data.next_payment && `оплата ${ruDate(data.next_payment).slice(0, 5)}`,
        ].filter(Boolean);
        body = (_jsxs(_Fragment, { children: [_jsxs("div", { className: "flex flex-wrap items-center gap-x-4 gap-y-2.5", children: [_jsxs("span", { className: "text-muted-foreground", children: [_jsx("a", { href: href.overview(), className: "text-primary hover:text-primary-hover", children: "\u0441\u0435\u0440\u0432\u0435\u0440\u044B" }), data.group && ` / ${data.group}`, data.project && ` / ${data.project}`, " /"] }), _jsx("h1", { className: "text-lg font-bold text-hi", children: data.name }), data.online ? (_jsxs("span", { className: "text-primary", children: ["\u25CF online ", r ? formatUptime(r.host.uptime_sec) : ""] })) : (_jsxs("span", { className: "text-crit", children: ["\u25CB offline \u00B7 \u043F\u043E\u0441\u043B\u0435\u0434\u043D\u0438\u0439 push ", ago(data.snapshot?.received ?? data.last_seen)] })), facts.length > 0 && _jsx("span", { className: "text-muted-foreground", children: facts.join(" · ") }), data.tags.length > 0 && (_jsx("span", { children: data.tags.map((t) => (_jsxs("span", { className: "mr-1 inline-block border border-border px-1.5 text-xs leading-[17px] text-[#9fc3a7]", children: ["#", t] }, t))) })), _jsx("span", { className: "flex-auto" }), _jsx(Button, { asChild: true, children: _jsx("a", { href: href.terminal(id), children: ">_ \u0442\u0435\u0440\u043C\u0438\u043D\u0430\u043B" }) }), _jsx(Button, { variant: "outline", onClick: () => setEditing(true), children: "\u270E \u043F\u0430\u0441\u043F\u043E\u0440\u0442" }), _jsx("span", { className: "flex items-center gap-1", role: "group", "aria-label": "\u043E\u043A\u043D\u043E \u0433\u0440\u0430\u0444\u0438\u043A\u043E\u0432", children: historyRanges.map((x) => (_jsx(Button, { size: "sm", variant: x === range ? "default" : "outline", onClick: () => setRange(x), children: x }, x))) })] }), error && _jsx("div", { className: "text-crit", children: error }), _jsxs("div", { className: "flex flex-wrap items-stretch gap-x-4 gap-y-5", children: [_jsxs("div", { className: "flex min-w-0 flex-[999_1_760px] flex-col gap-5", children: [_jsx(CpuPanel, { data: data, hist: hist }), _jsxs("div", { className: "flex flex-wrap gap-x-4 gap-y-5", children: [_jsx(MemoryPanel, { data: data }), _jsx(NetPanel, { data: data, hist: hist })] })] }), _jsx(ProcessPanel, { data: data, filterRef: filterRef })] }), _jsx(DockerPanel, { data: data, onLogs: showLogs }), _jsxs("div", { className: "flex flex-wrap gap-x-4 gap-y-5", children: [_jsx(LogsPanel, { logs: data.logs, source: logSource, setSource: setLogSource, watching: !!data.detail, panelRef: logsRef }), _jsx(Passport, { data: data, editing: editing, setEditing: setEditing, onSaved: load })] })] }));
    }
    return (_jsx(Shell, { route: route, version: version, onLogout: onLogout, status: status, footer: _jsxs(_Fragment, { children: [_jsx(Hotkey, { k: "t", children: "\u0442\u0435\u0440\u043C\u0438\u043D\u0430\u043B" }), _jsx(Hotkey, { k: "p", children: "\u043F\u0440\u043E\u0446\u0435\u0441\u0441\u044B" }), _jsx(Hotkey, { k: "l", children: "\u043B\u043E\u0433\u0438" }), _jsx(Hotkey, { k: "e", children: "\u043F\u0430\u0441\u043F\u043E\u0440\u0442" }), _jsx(Hotkey, { k: "+/-", children: "\u043E\u043A\u043D\u043E \u0433\u0440\u0430\u0444\u0438\u043A\u043E\u0432" }), _jsx(Hotkey, { k: "esc", children: "\u043D\u0430\u0437\u0430\u0434" })] }), footerNote: "live \u00B7 \u0434\u0435\u0442\u0430\u043B\u044C\u043D\u044B\u0435 \u043C\u0435\u0442\u0440\u0438\u043A\u0438 \u0448\u043B\u044E\u0442\u0441\u044F, \u043F\u043E\u043A\u0430 \u044D\u043A\u0440\u0430\u043D \u043E\u0442\u043A\u0440\u044B\u0442", children: body }));
}
