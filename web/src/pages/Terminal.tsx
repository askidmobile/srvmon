import { useCallback, useEffect, useRef, useState } from "react";

import { AgentPanel } from "@/components/AgentPanel";
import { FilesPanel } from "@/components/FilesPanel";
import { Hotkey, Shell } from "@/components/Shell";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { type TermStatus, XTerm } from "@/components/XTerm";
import { api, type ServerView } from "@/lib/api";
import { t } from "@/lib/i18n";
import { href, type Route } from "@/lib/router";
import { cn } from "@/lib/utils";

// One SSH session inside a tab.
interface Pane {
  key: number;
  serverId: number;
  container?: string;
  status: TermStatus;
  rec: boolean;
}

// A tab holds one session or up to MAX_PANES side by side ("row") or
// stacked ("col").
interface Tab {
  key: number;
  panes: Pane[];
  dir: "row" | "col";
  active: number; // pane key
}

type Side = "agent" | "files" | null;

const MAX_PANES = 4;
const SIDE_KEY = "srvmon.term.side";

let nextKey = 1;

function newPane(serverId: number, container?: string): Pane {
  return { key: nextKey++, serverId, container, status: "connecting", rec: false };
}

function initialSide(): Side {
  try {
    const v = localStorage.getItem(SIDE_KEY);
    if (v === "files" || v === "none") return v === "none" ? null : v;
    // Before the files panel this key held the agent panel only.
    if (localStorage.getItem("srvmon.agent.panel") === "0") return null;
  } catch {
    /* storage blocked */
  }
  return "agent";
}

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
  const [side, setSideState] = useState<Side>(initialSide);
  const askRef = useRef<HTMLInputElement>(null);

  const setSide = useCallback((next: Side) => {
    setSideState(next);
    try {
      localStorage.setItem(SIDE_KEY, next ?? "none");
    } catch {
      // ignore
    }
  }, []);

  // F9: agent panel (and jump into its input), F8: files.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "F9") {
        e.preventDefault();
        setSideState((s) => {
          const next: Side = s !== "agent" || document.activeElement !== askRef.current ? "agent" : null;
          try {
            localStorage.setItem(SIDE_KEY, next ?? "none");
          } catch {
            // ignore
          }
          if (next) setTimeout(() => askRef.current?.focus(), 0);
          return next;
        });
      } else if (e.key === "F8") {
        e.preventDefault();
        setSideState((s) => {
          const next: Side = s === "files" ? null : "files";
          try {
            localStorage.setItem(SIDE_KEY, next ?? "none");
          } catch {
            // ignore
          }
          return next;
        });
      }
    };
    // Capture phase: xterm stops propagation of the keys it handles.
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, []);

  useEffect(() => {
    api
      .servers()
      .then(setServers)
      .catch((e) => e?.status === 401 && onLogout());
  }, [onLogout]);

  const open = useCallback((id: number, container?: string) => {
    const pane = newPane(id, container);
    const key = nextKey++;
    setTabs((t) => [...t, { key, panes: [pane], dir: "row", active: pane.key }]);
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

  function closeTab(key: number) {
    setTabs((t) => {
      const rest = t.filter((x) => x.key !== key);
      if (active === key) setActive(rest.length ? rest[rest.length - 1].key : 0);
      if (!rest.length) setPicker(true);
      return rest;
    });
  }

  function closePane(tabKey: number, paneKey: number) {
    const tab = tabs.find((x) => x.key === tabKey);
    if (!tab) return;
    if (tab.panes.length <= 1) {
      closeTab(tabKey);
      return;
    }
    setTabs((t) =>
      t.map((x) => {
        if (x.key !== tabKey) return x;
        const panes = x.panes.filter((p) => p.key !== paneKey);
        return { ...x, panes, active: x.active === paneKey ? panes[panes.length - 1].key : x.active };
      }),
    );
  }

  // Splits the active tab with a new session to the same server/container.
  function split(dir: "row" | "col") {
    setTabs((t) =>
      t.map((x) => {
        if (x.key !== active || x.panes.length >= MAX_PANES) return x;
        const cur = x.panes.find((p) => p.key === x.active) ?? x.panes[0];
        const pane = newPane(cur.serverId, cur.container);
        return { ...x, dir, panes: [...x.panes, pane], active: pane.key };
      }),
    );
  }

  const updatePane = useCallback(
    (paneKey: number, f: (p: Pane) => Pane) =>
      setTabs((t) => {
        let changed = false;
        const next = t.map((x) => {
          if (!x.panes.some((p) => p.key === paneKey)) return x;
          const panes = x.panes.map((p) => {
            if (p.key !== paneKey) return p;
            const n = f(p);
            if (n !== p) changed = true;
            return n;
          });
          return changed ? { ...x, panes } : x;
        });
        return changed ? next : t;
      }),
    [],
  );

  const focusPane = useCallback(
    (tabKey: number, paneKey: number) =>
      setTabs((t) => (t.some((x) => x.key === tabKey && x.active !== paneKey) ? t.map((x) => (x.key === tabKey ? { ...x, active: paneKey } : x)) : t)),
    [],
  );

  const reconnect = (tabKey: number, pane: Pane) =>
    setTabs((t) =>
      t.map((x) => {
        if (x.key !== tabKey) return x;
        const fresh = newPane(pane.serverId, pane.container);
        return { ...x, panes: x.panes.map((p) => (p.key === pane.key ? fresh : p)), active: fresh.key };
      }),
    );

  const byId = (id: number) => servers?.find((s) => s.id === id);
  const curTab = tabs.find((t) => t.key === active);
  const cur = curTab?.panes.find((p) => p.key === curTab.active) ?? curTab?.panes[0];
  const curServer = cur ? byId(cur.serverId) : undefined;
  const withSSH = (servers ?? []).filter((s) => s.has_ssh);
  const withoutSSH = (servers ?? []).filter((s) => !s.has_ssh);

  const label = (p: Pane) => {
    const s = byId(p.serverId);
    const name = s?.name ?? `#${p.serverId}`;
    return p.container ? `${name}:${p.container}` : `${s?.ssh_user || "srvmon"}@${name}`;
  };
  const dot = (s: TermStatus) => (s === "open" ? "text-primary" : s === "closed" ? "text-crit" : "text-warn");

  return (
    <Shell
      route={route}
      version={version}
      onLogout={onLogout}
      footer={
        cur && curServer && curTab ? (
          <>
            <span>
              ssh ed25519 · {curServer.host}:{curServer.port || 22}
            </span>
            {size[cur.key] && <span>{size[cur.key]}</span>}
            <span>{t("via srvmon-gateway")}</span>
            <span>{cur.status === "open" ? t("connected") : cur.status === "closed" ? t("closed") : t("connecting…")}</span>
            {cur.rec && (
              <a className="text-crit" href={href.recordings()} title={t("the session is recorded (output only)")}>
                ● REC
              </a>
            )}
            {cur.status === "closed" && (
              <button type="button" className="text-primary" onClick={() => reconnect(curTab.key, cur)}>
                ⟳ {t("reconnect")}
              </button>
            )}
          </>
        ) : (
          <>
            <Hotkey k="esc">{t("to the session")}</Hotkey>
            <span className="text-muted-foreground">{t("no open sessions")}</span>
          </>
        )
      }
      footerNote={
        <>
          <Hotkey k="F9">{t("agent ⇄ terminal")}</Hotkey> <Hotkey k="F8">{t("files")}</Hotkey> ·{" "}
          {t("the private key never leaves the monitoring server")}
        </>
      }
    >
      <div className="flex min-h-0 flex-1 flex-col gap-4 lg:flex-row">
        <div className="flex min-h-0 min-w-0 flex-1 flex-col">
          <div className="flex items-end gap-1">
            <div className="flex min-w-0 flex-1 items-end gap-1 overflow-x-auto [scrollbar-width:thin]">
              {tabs.map((tab, i) => {
                const ap = tab.panes.find((p) => p.key === tab.active) ?? tab.panes[0];
                return (
                  <div
                    key={tab.key}
                    className={cn(
                      "flex h-8 shrink-0 items-center border border-b-0",
                      tab.key === active ? "border-primary bg-[#12361d] text-hi" : "border-border",
                    )}
                  >
                    <button type="button" className="h-full pl-3 pr-2 whitespace-nowrap" onClick={() => setActive(tab.key)}>
                      <span className={cn("mr-1.5", dot(ap.status))}>●</span>[{i + 1}] {label(ap)}
                      {tab.panes.length > 1 && <span className="ml-1 text-muted-foreground">+{tab.panes.length - 1}</span>}
                      {tab.panes.some((p) => p.rec) && <span className="ml-1.5 text-xs text-crit">REC</span>}
                    </button>
                    <button
                      type="button"
                      className="h-full pr-2.5 text-muted-foreground hover:text-crit"
                      aria-label={t("close the session")}
                      onClick={() => closeTab(tab.key)}
                    >
                      ×
                    </button>
                  </div>
                );
              })}
            </div>
            {curTab && (
              <>
                <Button
                  variant="outline"
                  className="h-8 px-2"
                  title={t("split right")}
                  aria-label={t("split right")}
                  disabled={curTab.panes.length >= MAX_PANES}
                  onClick={() => split("row")}
                >
                  ◫
                </Button>
                <Button
                  variant="outline"
                  className="h-8 px-2"
                  title={t("split down")}
                  aria-label={t("split down")}
                  disabled={curTab.panes.length >= MAX_PANES}
                  onClick={() => split("col")}
                >
                  ⊟
                </Button>
              </>
            )}
            <Button
              variant={side === "files" ? "default" : "outline"}
              className="h-8"
              title="F8"
              onClick={() => setSide(side === "files" ? null : "files")}
              disabled={!curServer}
            >
              {t("files")}
            </Button>
            <Button variant="outline" className="h-8" asChild>
              <a href={href.recordings()}>{t("recordings")}</a>
            </Button>
            <Button variant="outline" className="h-8" onClick={() => setPicker((p) => !p)}>
              + {t("session")}
            </Button>
          </div>

          <div
            className={cn(
              "relative min-h-[420px] flex-1 border bg-field",
              tabs.length ? "border-primary" : "border-border",
            )}
          >
            {tabs.map((tab) => (
              <div
                key={tab.key}
                className={cn(
                  "absolute inset-0 flex gap-px bg-border",
                  tab.dir === "col" ? "flex-col" : "flex-row",
                  tab.key !== active && "invisible",
                )}
              >
                {tab.panes.map((p) => {
                  const isActive = tab.key === active && p.key === tab.active;
                  return (
                    <div
                      key={p.key}
                      className={cn(
                        "flex min-h-0 min-w-0 flex-1 flex-col bg-field",
                        tab.panes.length > 1 && isActive && "outline outline-1 -outline-offset-1 outline-primary",
                      )}
                      onMouseDown={() => focusPane(tab.key, p.key)}
                    >
                      {tab.panes.length > 1 && (
                        <div className="flex h-6 shrink-0 items-center gap-2 border-b border-border/60 px-2 text-xs">
                          <span className={dot(p.status)}>●</span>
                          <span className={cn("truncate", isActive ? "text-hi" : "text-muted-foreground")}>{label(p)}</span>
                          {p.rec && <span className="text-crit">REC</span>}
                          <span className="flex-1" />
                          {p.status === "closed" && (
                            <button type="button" className="text-primary" onClick={() => reconnect(tab.key, p)}>
                              ⟳
                            </button>
                          )}
                          <button
                            type="button"
                            className="text-muted-foreground hover:text-crit"
                            aria-label={t("close the pane")}
                            onClick={() => closePane(tab.key, p.key)}
                          >
                            ×
                          </button>
                        </div>
                      )}
                      <div className="min-h-0 flex-1 p-2">
                        <XTerm
                          serverId={p.serverId}
                          container={p.container}
                          active={isActive && !picker}
                          onStatus={(s) => updatePane(p.key, (x) => (x.status === s ? x : { ...x, status: s }))}
                          onSize={(c, r) =>
                            setSize((m) => (m[p.key] === `${c}×${r}` ? m : { ...m, [p.key]: `${c}×${r}` }))
                          }
                          onRec={() => updatePane(p.key, (x) => (x.rec ? x : { ...x, rec: true }))}
                          onFocus={() => focusPane(tab.key, p.key)}
                        />
                      </div>
                    </div>
                  );
                })}
              </div>
            ))}

            {picker && (
              <div className="absolute inset-0 z-10 overflow-auto bg-background/95 p-5">
                <Panel title={t("new session")} className="mx-auto max-w-3xl">
                  {servers === null && <div className="text-muted-foreground">{t("loading…")}</div>}
                  {servers && withSSH.length === 0 && (
                    <div className="text-muted-foreground">
                      {t("No servers with ssh access. Add one with the")}{" "}
                      <a className="text-primary" href={href.add()}>
                        {t("wizard")}
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
                      {t("without ssh access:")}{" "}
                      {withoutSSH.map((s, i) => (
                        <span key={s.id}>
                          {i > 0 && ", "}
                          <a className="text-primary" href={href.add(s.id)}>
                            {s.name}
                          </a>
                        </span>
                      ))}{" "}
                      — {t("click to connect")}
                    </div>
                  )}
                  {tabs.length > 0 && (
                    <div className="mt-4">
                      <Button variant="ghost" size="sm" onClick={() => setPicker(false)}>
                        esc · {t("back to the session")}
                      </Button>
                    </div>
                  )}
                </Panel>
              </div>
            )}
          </div>
        </div>
        {side === "agent" && (
          <AgentPanel
            context={curServer ? { id: curServer.id, name: curServer.name } : undefined}
            inputRef={askRef}
            className="h-[560px] lg:h-auto lg:max-h-[calc(100vh-8.5rem)] lg:min-h-[420px] lg:w-[460px] lg:shrink-0"
          />
        )}
        {side === "files" && curServer && (
          <FilesPanel
            key={curServer.id}
            serverId={curServer.id}
            serverName={curServer.name}
            className="h-[560px] lg:h-auto lg:max-h-[calc(100vh-8.5rem)] lg:min-h-[420px] lg:w-[460px] lg:shrink-0"
          />
        )}
      </div>
    </Shell>
  );
}
