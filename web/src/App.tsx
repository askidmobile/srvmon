import { lazy, Suspense, useCallback, useEffect, useState } from "react";

import { api, type Me } from "@/lib/api";
import { useHashPath } from "@/lib/router";
import { AddServer } from "@/pages/AddServer";
import { Login } from "@/pages/Login";
import { Overview } from "@/pages/Overview";

// xterm.js is heavy: load the terminal page on demand.
const TerminalPage = lazy(() => import("@/pages/Terminal").then((m) => ({ default: m.TerminalPage })));

export function App() {
  const [me, setMe] = useState<Me | null>(null);
  const [failed, setFailed] = useState(false);
  const path = useHashPath();

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

  if (failed) {
    return <p className="p-6 text-crit">srvmon-server недоступен. Повторите попытку позже.</p>;
  }
  if (!me) {
    return <p className="p-6 text-muted-foreground">загрузка…</p>;
  }
  if (!me.authenticated) {
    return <Login onSuccess={refresh} />;
  }
  const add = path.match(/^\/add(?:\/(\d+))?$/);
  if (add) return <AddServer key={path} attachId={add[1] ? Number(add[1]) : undefined} onLogout={logout} />;
  const term = path.match(/^\/term(?:\/(\d+))?$/);
  if (term)
    return (
      <Suspense fallback={<p className="p-6 text-muted-foreground">загрузка терминала…</p>}>
        <TerminalPage serverId={term[1] ? Number(term[1]) : undefined} onLogout={logout} />
      </Suspense>
    );
  return <Overview version={me.version} onLogout={logout} />;
}
