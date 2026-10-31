import { lazy, Suspense, useCallback, useEffect, useState } from "react";

import { ErrorBoundary } from "@/components/ErrorBoundary";
import { api, type Me } from "@/lib/api";
import { isLang, setLang, t, useLang } from "@/lib/i18n";
import { href, navigate, type Route, typingIn, useRoute } from "@/lib/router";
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
  const [me, setMe] = useState<Me | null>(null);
  const [failed, setFailed] = useState(false);
  const route = useRoute();
  const lang = useLang();

  const refresh = useCallback(() => {
    api
      .me()
      .then((m) => {
        if (isLang(m.lang)) setLang(m.lang);
        setMe(m);
        setFailed(false);
      })
      .catch(() => setFailed(true));
  }, []);

  useEffect(refresh, [refresh]);
  const logout = useCallback(() => setMe((m) => (m ? { ...m, authenticated: false } : m)), []);

  // Global hotkeys from the mockup: [1] overview, [3] terminal, [4] agent, [5] alerts, [6] settings.
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if (typingIn(e) || e.ctrlKey || e.metaKey || e.altKey) return;
      if (e.key === "1") navigate(href.overview());
      else if (e.key === "3") navigate(href.terminal(route.name === "server" ? route.id : undefined));
      else if (e.key === "4") navigate(href.agent(route.name === "server" ? route.id : undefined));
      else if (e.key === "5") navigate(href.alerts());
      else if (e.key === "6") navigate(href.settings());
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  }, [route]);

  if (failed) {
    return <p className="p-6 text-crit">{t("srvmon-server is unavailable. Try again later.")}</p>;
  }
  if (!me) {
    return <p className="p-6 text-muted-foreground">{t("loading…")}</p>;
  }
  if (!me.authenticated) {
    return <Login key={lang} onSuccess={refresh} />;
  }
  return (
    <ErrorBoundary resetKey={JSON.stringify(route)}>
      <Screen route={route} version={me.version} onLogout={logout} />
    </ErrorBoundary>
  );
}

function Screen({ route, version, onLogout }: { route: Route; version: string; onLogout: () => void }) {
  switch (route.name) {
    case "server":
      return <ServerScreen key={route.id} id={route.id} route={route} version={version} onLogout={onLogout} />;
    case "add":
      return <AddServer key={route.id ?? 0} attachId={route.id} route={route} version={version} onLogout={onLogout} />;
    case "agent":
      return <AgentPage key={route.id ?? 0} serverId={route.id} route={route} version={version} onLogout={onLogout} />;
    case "alerts":
      return <AlertsPage route={route} version={version} onLogout={onLogout} />;
    case "settings":
      return <SettingsPage route={route} version={version} onLogout={onLogout} />;
    case "terminal":
      return (
        <Suspense fallback={<p className="p-6 text-muted-foreground">{t("loading the terminal…")}</p>}>
          <TerminalPage serverId={route.id} route={route} version={version} onLogout={onLogout} />
        </Suspense>
      );
  }
  return <Overview route={route} version={version} onLogout={onLogout} />;
}
