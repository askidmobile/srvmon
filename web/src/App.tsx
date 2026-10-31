import { useCallback, useEffect, useState } from "react";

import { Shell } from "@/components/Shell";
import { api, type Me } from "@/lib/api";
import { href, navigate, typingIn, useRoute } from "@/lib/router";
import { Login } from "@/pages/Login";
import { Overview } from "@/pages/Overview";
import { ServerScreen } from "@/pages/Server";

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
    case "terminal":
    case "add":
      return (
        <Shell route={route} version={me.version} onLogout={logout}>
          <p className="text-muted-foreground">
            {route.name === "add" ? "Мастер добавления сервера" : "Веб-терминал"} появится в этапе 3.{" "}
            <a href={href.overview()} className="text-primary">
              ← к обзору
            </a>
          </p>
        </Shell>
      );
  }
  return <Overview route={route} version={me.version} onLogout={logout} />;
}
