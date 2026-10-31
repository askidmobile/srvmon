import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useCallback, useEffect, useState } from "react";
import { AgentPanel } from "@/components/AgentPanel";
import { Hotkey, Shell } from "@/components/Shell";
import { Panel } from "@/components/ui/panel";
import { agentApi } from "@/lib/agent";
import { api } from "@/lib/api";
import { href, navigate } from "@/lib/router";
import { cn } from "@/lib/utils";
const stateLabel = {
    pending: "ждёт",
    approved: "выполняется",
    rejected: "отклонено",
    expired: "истекло",
    done: "выполнено",
    failed: "ошибка",
};
// Journal of commands the agent asked to run with confirmation.
function ActionsLog() {
    const [list, setList] = useState(null);
    const [open, setOpen] = useState(null);
    const load = useCallback(() => {
        agentApi
            .actions()
            .then(setList)
            .catch(() => setList([]));
    }, []);
    useEffect(() => {
        load();
        const t = setInterval(load, 5000);
        return () => clearInterval(t);
    }, [load]);
    return (_jsx(Panel, { title: "\u0436\u0443\u0440\u043D\u0430\u043B \u043F\u043E\u0434\u0442\u0432\u0435\u0440\u0436\u0434\u0435\u043D\u0438\u0439", className: "flex min-h-0 flex-col", children: _jsxs("div", { className: "min-h-0 flex-1 overflow-y-auto pr-1 [scrollbar-width:thin]", children: [list === null && _jsx("div", { className: "text-muted-foreground", children: "\u0437\u0430\u0433\u0440\u0443\u0437\u043A\u0430\u2026" }), list?.length === 0 && (_jsx("div", { className: "text-xs text-muted-foreground", children: "\u0417\u0434\u0435\u0441\u044C \u043F\u043E\u044F\u0432\u044F\u0442\u0441\u044F \u043A\u043E\u043C\u0430\u043D\u0434\u044B, \u043A\u043E\u0442\u043E\u0440\u044B\u0435 \u0430\u0433\u0435\u043D\u0442 \u0432\u044B\u043F\u043E\u043B\u043D\u044F\u043B \u043F\u043E\u0441\u043B\u0435 \u0432\u0430\u0448\u0435\u0433\u043E \u043F\u043E\u0434\u0442\u0432\u0435\u0440\u0436\u0434\u0435\u043D\u0438\u044F. \u0427\u0442\u0435\u043D\u0438\u0435 (\u043B\u043E\u0433\u0438, docker ps, df\u2026) \u0432\u044B\u043F\u043E\u043B\u043D\u044F\u0435\u0442\u0441\u044F \u0431\u0435\u0437 \u043F\u043E\u0434\u0442\u0432\u0435\u0440\u0436\u0434\u0435\u043D\u0438\u044F \u0438 \u043F\u0438\u0448\u0435\u0442\u0441\u044F \u0432 \u0430\u0443\u0434\u0438\u0442." })), list?.map((a) => (_jsxs("div", { className: "border-t border-border/60 py-1.5 text-xs first:border-t-0", children: [_jsxs("button", { type: "button", className: "flex w-full items-center gap-2 text-left", onClick: () => setOpen(open === a.id ? null : a.id), children: [_jsx("span", { className: cn("w-20 shrink-0", a.state === "done"
                                        ? "text-primary"
                                        : a.state === "pending" || a.state === "approved"
                                            ? "text-warn"
                                            : "text-crit"), children: stateLabel[a.state] }), _jsx("span", { className: "shrink-0 text-hi", children: a.server }), _jsx("span", { className: "min-w-0 flex-1 truncate text-muted-foreground", children: a.command }), _jsx("span", { className: "shrink-0 text-muted-foreground", children: a.created_at.slice(5, 16).replace("T", " ") })] }), open === a.id && (_jsxs("div", { className: "mt-1 pl-22", children: [a.reason && _jsx("div", { className: "text-muted-foreground", children: a.reason }), _jsx("pre", { className: "mt-1 whitespace-pre-wrap break-all bg-field px-2 py-1 text-hi", children: a.command }), a.decided_by && _jsxs("div", { className: "mt-1 text-muted-foreground", children: ["\u0440\u0435\u0448\u0435\u043D\u0438\u0435: ", a.decided_by] }), a.exit_code !== undefined && _jsxs("div", { className: "text-muted-foreground", children: ["\u043A\u043E\u0434 \u0432\u044B\u0445\u043E\u0434\u0430 ", a.exit_code] }), a.output && (_jsx("pre", { className: "mt-1 max-h-60 overflow-auto bg-field px-2 py-1 text-foreground [scrollbar-width:thin]", children: a.output }))] }))] }, a.id)))] }) }));
}
export function AgentPage({ serverId, route, version, onLogout, }) {
    const [servers, setServers] = useState([]);
    useEffect(() => {
        api
            .servers()
            .then(setServers)
            .catch((e) => e?.status === 401 && onLogout());
    }, [onLogout]);
    const ctx = servers.find((s) => s.id === serverId);
    return (_jsxs(Shell, { route: route, version: version, onLogout: onLogout, footer: _jsxs(_Fragment, { children: [_jsx(Hotkey, { k: "3", children: "\u0442\u0435\u0440\u043C\u0438\u043D\u0430\u043B" }), _jsx("span", { className: "text-muted-foreground", children: "\u0430\u0433\u0435\u043D\u0442 \u0440\u0430\u0431\u043E\u0442\u0430\u0435\u0442 \u0447\u0435\u0440\u0435\u0437 srvmon-gateway, \u043A\u043B\u044E\u0447\u0435\u0439 \u0443 \u043D\u0435\u0433\u043E \u043D\u0435\u0442" })] }), footerNote: "\u0438\u0437\u043C\u0435\u043D\u0435\u043D\u0438\u044F \u043D\u0430 \u0441\u0435\u0440\u0432\u0435\u0440\u0430\u0445 \u2014 \u0442\u043E\u043B\u044C\u043A\u043E \u043F\u043E\u0441\u043B\u0435 \u0432\u0430\u0448\u0435\u0433\u043E \u043F\u043E\u0434\u0442\u0432\u0435\u0440\u0436\u0434\u0435\u043D\u0438\u044F", children: [_jsxs("div", { className: "flex items-center gap-3 text-xs", children: [_jsx("label", { htmlFor: "agent-ctx", className: "text-muted-foreground", children: "\u043A\u043E\u043D\u0442\u0435\u043A\u0441\u0442:" }), _jsxs("select", { id: "agent-ctx", className: "h-7 border border-border bg-field px-2 text-hi", value: serverId ?? 0, onChange: (e) => navigate(href.agent(Number(e.target.value) || undefined)), children: [_jsx("option", { value: 0, children: "\u0432\u0441\u0435 \u0441\u0435\u0440\u0432\u0435\u0440\u044B" }), servers.map((s) => (_jsx("option", { value: s.id, children: s.name }, s.id)))] })] }), _jsxs("div", { className: "flex min-h-0 flex-1 flex-col gap-4 lg:flex-row", children: [_jsx(AgentPanel, { context: ctx ? { id: ctx.id, name: ctx.name } : undefined, className: "h-[600px] min-w-0 lg:h-[calc(100vh-10rem)] lg:flex-1" }), _jsx("div", { className: "flex min-h-0 flex-col lg:h-[calc(100vh-10rem)] lg:w-[460px] lg:shrink-0", children: _jsx(ActionsLog, {}) })] })] }));
}
