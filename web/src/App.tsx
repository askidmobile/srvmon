import { useCallback, useEffect, useState } from "react";

import { api, type Me } from "@/lib/api";
import { Login } from "@/pages/Login";
import { Overview } from "@/pages/Overview";

export function App() {
  const [me, setMe] = useState<Me | null>(null);
  const [failed, setFailed] = useState(false);

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
  return <Overview version={me.version} onLogout={logout} />;
}
