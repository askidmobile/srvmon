import { jsx as _jsx } from "react/jsx-runtime";
import { lazy, Suspense, useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { href, navigate, typingIn, useRoute } from "@/lib/router";
import { AddServer } from "@/pages/AddServer";
import { AgentPage } from "@/pages/Agent";
import { AlertsPage } from "@/pages/Alerts";
import { Login } from "@/pages/Login";
import { Overview } from "@/pages/Overview";
import { ServerScreen } from "@/pages/Server";
import { SettingsPage } from "@/pages/Settings";
// xterm.js is heavy: load the terminal page on demand.
const TerminalPage = lazy(() => import("@/pages/Terminal").then((m) => ({ default: m.TerminalPage })));
export function App() {
    const [me, setMe] = useState(null);
    const [failed, setFailed] = useState(false);
    const route = useRoute();
    const refresh = useCallback(() => {
        api
            .me()
            .then((m) => {
            setMe(m);
            setFailed(false);
        })
            .catch(() => setFailed(true));
    }, []);
    useEffect(refresh, [refresh]);
    const logout = useCallback(() => setMe((m) => (m ? { ...m, authenticated: false } : m)), []);
    // Global hotkeys from the mockup: [1] overview, [3] terminal, [4] agent, [5] alerts, [6] settings.
    useEffect(() => {
        const on = (e) => {
            if (typingIn(e) || e.ctrlKey || e.metaKey || e.altKey)
                return;
            if (e.key === "1")
                navigate(href.overview());
            else if (e.key === "3")
                navigate(href.terminal(route.name === "server" ? route.id : undefined));
            else if (e.key === "4")
                navigate(href.agent(route.name === "server" ? route.id : undefined));
            else if (e.key === "5")
                navigate(href.alerts());
            else if (e.key === "6")
                navigate(href.settings());
        };
        window.addEventListener("keydown", on);
        return () => window.removeEventListener("keydown", on);
    }, [route]);
    if (failed) {
        return _jsx("p", { className: "p-6 text-crit", children: "srvmon-server \u043D\u0435\u0434\u043E\u0441\u0442\u0443\u043F\u0435\u043D. \u041F\u043E\u0432\u0442\u043E\u0440\u0438\u0442\u0435 \u043F\u043E\u043F\u044B\u0442\u043A\u0443 \u043F\u043E\u0437\u0436\u0435." });
    }
    if (!me) {
        return _jsx("p", { className: "p-6 text-muted-foreground", children: "\u0437\u0430\u0433\u0440\u0443\u0437\u043A\u0430\u2026" });
    }
    if (!me.authenticated) {
        return _jsx(Login, { onSuccess: refresh });
    }
    switch (route.name) {
        case "server":
            return _jsx(ServerScreen, { id: route.id, route: route, version: me.version, onLogout: logout }, route.id);
        case "add":
            return _jsx(AddServer, { attachId: route.id, route: route, version: me.version, onLogout: logout }, route.id ?? 0);
        case "agent":
            return _jsx(AgentPage, { serverId: route.id, route: route, version: me.version, onLogout: logout }, route.id ?? 0);
        case "alerts":
            return _jsx(AlertsPage, { route: route, version: me.version, onLogout: logout });
        case "settings":
            return _jsx(SettingsPage, { route: route, version: me.version, onLogout: logout });
        case "terminal":
            return (_jsx(Suspense, { fallback: _jsx("p", { className: "p-6 text-muted-foreground", children: "\u0437\u0430\u0433\u0440\u0443\u0437\u043A\u0430 \u0442\u0435\u0440\u043C\u0438\u043D\u0430\u043B\u0430\u2026" }), children: _jsx(TerminalPage, { serverId: route.id, route: route, version: me.version, onLogout: logout }) }));
    }
    return _jsx(Overview, { route: route, version: me.version, onLogout: logout });
}
