import { lazy, Suspense, useCallback, useEffect, useState } from "react";

import { api, type Me } from "@/lib/api";
import { href, navigate, typingIn, useRoute } from "@/lib/router";
import { AddServer } from "@/pages/AddServer";
import { Login } from "@/pages/Login";
import { Overview } from "@/pages/Overview";
import { ServerScreen } from "@/pages/Server";

// xterm.js is heavy: load the terminal page on demand.
const TerminalPage = lazy(() => import("@/pages/Terminal").then((m) => ({ default: m.TerminalPage })));

export function App() {
  const [me, setMe] = useState<Me | null>(null);
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

  // Global hotkeys from the mockup: [1] overview, [3] terminal.
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if (typingIn(e) || e.ctrlKey || e.metaKey || e.altKey) return;
      if (e.key === "1") navigate(href.overview());
      else if (e.key === "3") navigate(href.terminal(route.name === "server" ? route.id : undefined));
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  }, [route]);

  if (failed) {
    return <p className="p-6 text-crit">srvmon-server недоступен. Повторите попытку позже.</p>;
  }
  if (!me) {
    return <p className="p-6 text-muted-foreground">загрузка…</p>;
  }
  if (!me.authenticated) {
    return <Login onSuccess={refresh} />;
  }
  switch (route.name) {
    case "server":
      return <ServerScreen key={route.id} id={route.id} route={route} version={me.version} onLogout={logout} />;
    case "add":
      return <AddServer key={route.id ?? 0} attachId={route.id} route={route} version={me.version} onLogout={logout} />;
    case "terminal":
      return (
        <Suspense fallback={<p className="p-6 text-muted-foreground">загрузка терминала…</p>}>
          <TerminalPage serverId={route.id} route={route} version={me.version} onLogout={logout} />
        </Suspense>
      );
  }
  return <Overview route={route} version={me.version} onLogout={logout} />;
}
