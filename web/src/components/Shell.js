import { jsxs as _jsxs, jsx as _jsx } from "react/jsx-runtime";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { href } from "@/lib/router";
import { cn } from "@/lib/utils";
const nav = [
    { key: "1", label: "обзор", to: href.overview(), match: ["overview"] },
    { key: "2", label: "серверы", match: ["server", "add"] },
    { key: "3", label: "терминал", to: href.terminal(), match: ["terminal"] },
    { key: "4", label: "агент", to: href.agent(), match: ["agent"] },
    { key: "5", label: "алерты", to: href.alerts(), match: ["alerts"] },
    { key: "6", label: "настройки", to: href.settings(), match: ["settings"] },
];
function Clock() {
    const [now, setNow] = useState(() => new Date());
    useEffect(() => {
        const t = setInterval(() => setNow(new Date()), 1000);
        return () => clearInterval(t);
    }, []);
    return _jsxs("span", { className: "text-muted-foreground", children: [now.toISOString().slice(11, 19), " UTC"] });
}
// "▲ 2 алерта · tg: ok" in the header of every page.
function AlertBadge() {
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
    const tg = { ok: "text-muted-foreground", off: "text-muted-foreground/60", error: "text-crit" }[sum.telegram];
    return (_jsxs("a", { href: href.alerts(), className: "flex gap-3 whitespace-nowrap hover:opacity-80", children: [sum.open > 0 ? (_jsxs("span", { className: sum.crit > 0 ? "text-crit" : "text-warn", children: ["\u25B2 ", sum.open, " ", plural(sum.open, "алерт", "алерта", "алертов")] })) : (_jsx("span", { className: "text-muted-foreground", children: "\u0430\u043B\u0435\u0440\u0442\u043E\u0432 \u043D\u0435\u0442" })), _jsxs("span", { className: tg, children: ["tg: ", sum.telegram === "ok" ? "ok" : sum.telegram === "off" ? "выкл" : "ошибка"] })] }));
}
export function plural(n, one, few, many) {
    const m10 = n % 10;
    const m100 = n % 100;
    if (m10 === 1 && m100 !== 11)
        return one;
    if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14))
        return few;
    return many;
}
export function Hotkey({ k, children }) {
    return (_jsxs("span", { className: "whitespace-nowrap", children: [_jsx("span", { className: "bg-primary px-1 text-primary-foreground", children: k }), " ", children] }));
}
// Page frame from the mockup: header with numbered navigation, content and
// a footer with hotkeys.
export function Shell({ route, version, onLogout, status, footer, footerNote, children, }) {
    return (_jsxs("div", { className: "flex min-h-screen flex-col", children: [_jsxs("header", { className: "flex flex-wrap items-center gap-x-6 gap-y-2 border-b border-border px-4 py-2.5 sm:px-5", children: [_jsx("a", { href: href.overview(), className: "font-bold text-primary", children: "\u258Csrvmon" }), _jsx("nav", { className: "flex flex-1 flex-wrap gap-1", "aria-label": "\u0440\u0430\u0437\u0434\u0435\u043B\u044B", children: nav.map((n) => {
                            const on = n.match.includes(route.name);
                            const cls = cn("px-2 py-0.5", on ? "bg-primary text-primary-foreground" : "text-primary hover:text-primary-hover");
                            return n.to ? (_jsxs("a", { href: n.to, className: cls, "aria-current": on ? "page" : undefined, children: ["[", n.key, "] ", n.label] }, n.key)) : (_jsxs("span", { className: on ? cls : "px-2 py-0.5 text-muted-foreground/60", title: on ? undefined : "скоро", children: ["[", n.key, "] ", n.label] }, n.key));
                        }) }), status, _jsx(AlertBadge, {}), _jsx(Clock, {}), _jsxs("span", { className: "text-xs text-muted-foreground", children: ["v", version.slice(0, 12)] }), _jsx(Button, { variant: "outline", size: "sm", onClick: async () => {
                            await api.logout().catch(() => undefined);
                            onLogout();
                        }, children: "\u0432\u044B\u0439\u0442\u0438" })] }), _jsx("main", { className: "flex flex-1 flex-col gap-5 px-4 py-4 sm:px-5", children: children }), (footer || footerNote) && (_jsxs("footer", { className: "flex flex-wrap gap-x-4 gap-y-1 border-t border-border px-4 py-1.5 text-xs sm:px-5", children: [footer, footerNote && _jsx("span", { className: "ml-auto text-muted-foreground", children: footerNote })] }))] }));
}
