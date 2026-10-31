import { useCallback, useEffect, useRef, useState } from "react";

import { Hotkey, Shell } from "@/components/Shell";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { type TermStatus, XTerm } from "@/components/XTerm";
import { api, type ServerView } from "@/lib/api";
import { href, type Route } from "@/lib/router";
import { cn } from "@/lib/utils";

interface Tab {
  key: number;
  serverId: number;
  container?: string;
  status: TermStatus;
}

let nextKey = 1;

export function TerminalPage({
  serverId,
  route,
  version,
  onLogout,
}: {
  serverId?: number;
  route: Route;
  version: string;
  onLogout: () => void;
}) {
  const [servers, setServers] = useState<ServerView[] | null>(null);
  const [tabs, setTabs] = useState<Tab[]>([]);
  const [active, setActive] = useState(0);
  const [picker, setPicker] = useState(false);
  const [size, setSize] = useState<Record<number, string>>({});
  const opened = useRef<number | undefined>(undefined);

  useEffect(() => {
    api
      .servers()
      .then(setServers)
      .catch((e) => e?.status === 401 && onLogout());
  }, [onLogout]);

  const open = useCallback((id: number, container?: string) => {
    const key = nextKey++;
    setTabs((t) => [...t, { key, serverId: id, container, status: "connecting" }]);
    setActive(key);
    setPicker(false);
  }, []);

  // #/term/<id> opens a session once.
  useEffect(() => {
    if (!servers || !serverId || opened.current === serverId) return;
    opened.current = serverId;
    if (servers.some((s) => s.id === serverId && s.has_ssh)) open(serverId);
    else setPicker(true);
  }, [servers, serverId, open]);

  useEffect(() => {
    if (servers && !serverId && tabs.length === 0) setPicker(true);
  }, [servers, serverId, tabs.length]);

  useEffect(() => {
    if (!picker || !tabs.length) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setPicker(false);
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [picker, tabs.length]);

  function close(key: number) {
    setTabs((t) => {
      const rest = t.filter((x) => x.key !== key);
      if (active === key) setActive(rest.length ? rest[rest.length - 1].key : 0);
      if (!rest.length) setPicker(true);
      return rest;
    });
  }

  const setStatus = useCallback(
    (key: number, status: TermStatus) =>
      setTabs((t) => t.map((x) => (x.key === key && x.status !== status ? { ...x, status } : x))),
    [],
  );

  const byId = (id: number) => servers?.find((s) => s.id === id);
  const cur = tabs.find((t) => t.key === active);
  const curServer = cur ? byId(cur.serverId) : undefined;
  const withSSH = (servers ?? []).filter((s) => s.has_ssh);
  const withoutSSH = (servers ?? []).filter((s) => !s.has_ssh);

  const label = (t: Tab) => {
    const s = byId(t.serverId);
    const name = s?.name ?? `#${t.serverId}`;
    return t.container ? `${name}:${t.container}` : `${s?.ssh_user || "srvmon"}@${name}`;
  };

  return (
    <Shell
      route={route}
      version={version}
      onLogout={onLogout}
      footer={
        cur && curServer ? (
          <>
            <span>
              ssh ed25519 · {curServer.host}:{curServer.port || 22}
            </span>
            {size[cur.key] && <span>{size[cur.key]}</span>}
            <span>через srvmon-gateway</span>
            <span>{cur.status === "open" ? "подключено" : cur.status === "closed" ? "закрыто" : "подключение…"}</span>
            {cur.status === "closed" && (
              <button type="button" className="text-primary" onClick={() => open(cur.serverId, cur.container)}>
                ⟳ переподключить
              </button>
            )}
          </>
        ) : (
          <>
            <Hotkey k="esc">к сессии</Hotkey>
            <span className="text-muted-foreground">нет открытых сессий</span>
          </>
        )
      }
      footerNote="приватный ключ не покидает сервер мониторинга"
    >
      <div className="flex min-h-0 flex-1 flex-col">
        <div className="flex items-end gap-1">
          <div className="flex min-w-0 flex-1 items-end gap-1 overflow-x-auto [scrollbar-width:thin]">
            {tabs.map((t, i) => (
              <div
                key={t.key}
                className={cn(
                  "flex h-8 shrink-0 items-center border border-b-0",
                  t.key === active ? "border-primary bg-[#12361d] text-hi" : "border-border",
                )}
              >
                <button type="button" className="h-full pl-3 pr-2 whitespace-nowrap" onClick={() => setActive(t.key)}>
                  <span
                    className={cn(
                      "mr-1.5",
                      t.status === "open" ? "text-primary" : t.status === "closed" ? "text-crit" : "text-warn",
                    )}
                  >
                    ●
                  </span>
                  [{i + 1}] {label(t)}
                </button>
                <button
                  type="button"
                  className="h-full pr-2.5 text-muted-foreground hover:text-crit"
                  aria-label="закрыть сессию"
                  onClick={() => close(t.key)}
                >
                  ×
                </button>
              </div>
            ))}
          </div>
          <Button variant="outline" className="h-8" onClick={() => setPicker((p) => !p)}>
            + сессия
          </Button>
        </div>

        <div
          className={cn(
            "relative min-h-[420px] flex-1 border bg-field",
            tabs.length ? "border-primary" : "border-border",
          )}
        >
          {tabs.map((t) => (
            <div key={t.key} className={cn("absolute inset-0 p-2", t.key !== active && "invisible")}>
              <XTerm
                serverId={t.serverId}
                container={t.container}
                active={t.key === active && !picker}
                onStatus={(s) => setStatus(t.key, s)}
                onSize={(c, r) => setSize((m) => (m[t.key] === `${c}×${r}` ? m : { ...m, [t.key]: `${c}×${r}` }))}
              />
            </div>
          ))}

          {picker && (
            <div className="absolute inset-0 z-10 overflow-auto bg-background/95 p-5">
              <Panel title="новая сессия" className="mx-auto max-w-3xl">
                {servers === null && <div className="text-muted-foreground">загрузка…</div>}
                {servers && withSSH.length === 0 && (
                  <div className="text-muted-foreground">
                    нет серверов с ssh-доступом. Добавьте сервер через{" "}
                    <a className="text-primary" href={href.add()}>
                      мастер
                    </a>
                    .
                  </div>
                )}
                <div className="flex flex-col">
                  {withSSH.map((s) => {
                    const containers = (s.snapshot?.report.containers ?? []).filter((c) => c.state === "running");
                    return (
                      <div key={s.id} className="border-t border-border/60 py-2 first:border-t-0">
                        <div className="flex flex-wrap items-center gap-3">
                          <span className={s.online ? "text-primary" : "text-crit"}>{s.online ? "●" : "○"}</span>
                          <span className="text-hi">{s.name}</span>
                          <span className="text-muted-foreground">
                            {s.ssh_user}@{s.host}
                            {s.port && s.port !== 22 ? `:${s.port}` : ""}
                          </span>
                          <span className="flex-1" />
                          <Button size="sm" onClick={() => open(s.id)}>
                            &gt;_ shell
                          </Button>
                        </div>
                        {containers.length > 0 && (
                          <div className="mt-1.5 flex flex-wrap gap-1.5 pl-6">
                            <span className="text-xs text-muted-foreground">docker exec:</span>
                            {containers.map((c) => (
                              <Button key={c.id} size="sm" variant="outline" onClick={() => open(s.id, c.name)}>
                                {c.name}
                              </Button>
                            ))}
                          </div>
                        )}
                      </div>
                    );
                  })}
                </div>
                {withoutSSH.length > 0 && (
                  <div className="mt-4 text-xs text-muted-foreground">
                    без ssh-доступа:{" "}
                    {withoutSSH.map((s, i) => (
                      <span key={s.id}>
                        {i > 0 && ", "}
                        <a className="text-primary" href={href.add(s.id)}>
                          {s.name}
                        </a>
                      </span>
                    ))}{" "}
                    — нажмите, чтобы подключить
                  </div>
                )}
                {tabs.length > 0 && (
                  <div className="mt-4">
                    <Button variant="ghost" size="sm" onClick={() => setPicker(false)}>
                      esc · вернуться к сессии
                    </Button>
                  </div>
                )}
              </Panel>
            </div>
          )}
        </div>
      </div>
    </Shell>
  );
}
