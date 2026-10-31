import { useEffect, useMemo, useState } from "react";

import { needsAgentUpdate, shortVersion } from "@/components/AgentVersion";
import { Hotkey, Shell } from "@/components/Shell";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { ScrollArea } from "@/components/ui/scroll-area";
import { api, type AlertSummary, type ServerView } from "@/lib/api";
import { locale, t } from "@/lib/i18n";
import { href, navigate, type Route } from "@/lib/router";
import { cn, formatBytes, formatUptime } from "@/lib/utils";

function level(p: number) {
  if (p >= 90) return "text-crit";
  if (p >= 70) return "text-warn";
  return "text-primary";
}

// Text meter like btop: ▮▮▮▮▯▯▯▯▯▯
function Meter({ value }: { value: number }) {
  const cells = 10;
  const filled = Math.round((Math.min(100, Math.max(0, value)) / 100) * cells);
  return (
    <span className={cn("whitespace-nowrap", level(value))} aria-label={`${value.toFixed(0)}%`}>
      {"▮".repeat(filled)}
      <span className="text-border">{"▮".repeat(cells - filled)}</span>
      <span className="ml-2 inline-block w-10 text-right text-foreground">{value.toFixed(0)}%</span>
    </span>
  );
}

function Row({ s, onDelete }: { s: ServerView; onDelete: (s: ServerView) => void }) {
  const r = s.snapshot?.report;
  const memPct = r && r.host.mem_total > 0 ? ((r.host.mem_total - r.host.mem_available) / r.host.mem_total) * 100 : 0;
  const disk = r?.host.disks?.reduce((max, d) => Math.max(max, d.total ? (d.used / d.total) * 100 : 0), 0) ?? 0;
  const containers = r?.containers ?? [];
  const running = containers.filter((c) => c.state === "running").length;
  const bad = containers.filter((c) => c.state !== "running" || c.health === "unhealthy").length;
  return (
    <tr
      className="cursor-pointer border-t border-border/60 hover:bg-secondary/60 focus-visible:bg-secondary"
      tabIndex={0}
      onClick={() => navigate(href.server(s.id))}
      onKeyDown={(e) => {
        const row = e.currentTarget;
        if (e.key === "Enter") navigate(href.server(s.id));
        else if (e.key === "ArrowDown") (row.nextElementSibling as HTMLElement | null)?.focus();
        else if (e.key === "ArrowUp") (row.previousElementSibling as HTMLElement | null)?.focus();
        else return;
        e.preventDefault();
      }}
    >
      <td className="py-1.5 pr-4">
        <span className={s.online ? "text-primary" : "text-crit"}>{s.online ? "●" : "○"}</span>{" "}
        <a href={href.server(s.id)} className="text-hi hover:text-primary" onClick={(e) => e.stopPropagation()}>
          {s.name}
        </a>
        {s.agent_outdated && (
          <span
            className="ml-2 text-xs text-warn"
            title={t("agent {v}, the panel runs {latest}", {
              v: shortVersion(r?.agent_version),
              latest: shortVersion(s.agent_latest),
            })}
          >
            {s.agent_update?.state === "running" ? t("agent updating…") : t("agent outdated")}
          </span>
        )}
        {s.agent_update?.state === "failed" && s.agent_outdated && (
          <span className="ml-2 text-xs text-crit" title={s.agent_update.error}>
            {t("update failed")}
          </span>
        )}
        {s.description && <div className="text-xs text-muted-foreground">{s.description}</div>}
      </td>
      <td className="pr-4 text-muted-foreground">{s.group || "—"}</td>
      <td className="pr-4">{r ? <Meter value={r.host.cpu_percent} /> : "—"}</td>
      <td className="pr-4">{r ? <Meter value={memPct} /> : "—"}</td>
      <td className="pr-4">{r ? <Meter value={disk} /> : "—"}</td>
      <td className="pr-4 whitespace-nowrap">
        {r?.docker_ok ? (
          <>
            <span className="text-primary">{running}</span>
            <span className="text-muted-foreground">/{containers.length}</span>
            {bad > 0 && <span className="ml-2 text-crit">!{bad}</span>}
          </>
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </td>
      <td className="pr-4 whitespace-nowrap text-muted-foreground">
        {r ? `${formatUptime(r.host.uptime_sec)} · ${formatBytes(r.host.mem_total)}` : t("no data")}
      </td>
      <td className="whitespace-nowrap text-muted-foreground">
        {s.cost != null ? `${s.cost} ${s.currency}` : "—"}
        {s.next_payment && <div className="text-xs">{t("due {date}", { date: s.next_payment })}</div>}
      </td>
      <td
        className="whitespace-nowrap pl-2 text-right"
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => e.stopPropagation()}
      >
        {s.has_ssh ? (
          <Button size="sm" variant="outline" asChild>
            <a href={href.terminal(s.id)} title={t("terminal")}>
              &gt;_
            </a>
          </Button>
        ) : (
          <Button size="sm" variant="ghost" asChild>
            <a href={href.add(s.id)} title={t("connect ssh access")}>
              + ssh
            </a>
          </Button>
        )}
        <Button size="sm" variant="ghost" className="hover:text-crit" title={t("delete")} onClick={() => onDelete(s)}>
          ×
        </Button>
      </td>
    </tr>
  );
}

function AlertsPanel() {
  const [sum, setSum] = useState<AlertSummary | null>(null);
  useEffect(() => {
    let alive = true;
    const load = () =>
      api
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
  if (!sum) return null;
  const sev = { crit: "text-crit", warn: "text-warn", info: "text-tx" } as const;
  return (
    <Panel title={t("alerts")} right={t("{open} active · {day} in 24h", { open: sum.open, day: sum.last24h })}>
      {sum.latest.length === 0 ? (
        <div className="text-primary">✓ {t("no active alerts")}</div>
      ) : (
        <div className="flex flex-col gap-0.5">
          {sum.latest.map((a) => (
            <div key={a.id} className="truncate">
              <span className="text-muted-foreground">
                {new Date(a.started_at).toLocaleTimeString(locale(), { hour: "2-digit", minute: "2-digit" })}
              </span>{" "}
              <span className={sev[a.severity]}>{a.severity.toUpperCase()}</span>{" "}
              <span className="text-hi">{a.server_name || t("panel")}</span> — {a.message}
              {a.state === "acked" && <span className="text-primary"> ✓</span>}
              {a.notified && <span className="text-muted-foreground"> → tg</span>}
            </div>
          ))}
        </div>
      )}
      <div className="mt-2.5">
        <Button size="sm" variant="outline" asChild>
          <a href={href.alerts()}>{t("all alerts and rules")} →</a>
        </Button>
      </div>
    </Panel>
  );
}

export function Overview({ route, version, onLogout }: { route: Route; version: string; onLogout: () => void }) {
  const [servers, setServers] = useState<ServerView[] | null>(null);
  const [error, setError] = useState("");
  const [group, setGroup] = useState(""); // "" = all

  const [reload, setReload] = useState(0);
  const remove = async (s: ServerView) => {
    if (!confirm(t("Remove {name} from the panel? Its key and agent token are revoked; the server itself is not touched.", { name: s.name }))) return;
    await api.deleteServer(s.id).catch(() => setError(t("could not delete")));
    setReload((n) => n + 1);
  };

  useEffect(() => {
    let alive = true;
    const load = () =>
      api
        .servers()
        .then((s) => alive && (setServers(s), setError("")))
        .catch((e) => {
          if (!alive) return;
          if (e?.status === 401) onLogout();
          else setError(t("no connection to the server"));
        });
    load();
    const timer = setInterval(load, 5000);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  }, [onLogout, reload]);

  const groups = useMemo(() => ["", ...new Set((servers ?? []).map((s) => s.group).filter(Boolean))], [servers]);
  const visible = (servers ?? []).filter((s) => !group || s.group === group);
  const online = (servers ?? []).filter((s) => s.online).length;
  const outdated = (servers ?? []).filter(needsAgentUpdate).length;
  const updateAll = () => {
    api
      .updateAgents()
      .then(() => setReload((n) => n + 1))
      .catch(() => setError(t("could not start the update")));
  };

  return (
    <Shell
      route={route}
      version={version}
      onLogout={onLogout}
      status={
        <>
          {error && <span className="text-crit">{error}</span>}
          <span>
            <span className={online === (servers?.length ?? 0) ? "text-primary" : "text-warn"}>●</span> {online}/
            {servers?.length ?? 0} up
          </span>
          {outdated > 0 && (
            <Button size="sm" variant="outline" onClick={updateAll} title={t("replace outdated agents over ssh")}>
              ↑ {t("update agents ({n})", { n: outdated })}
            </Button>
          )}
          <Button size="sm" asChild>
            <a href={href.add()}>+ {t("server")}</a>
          </Button>
        </>
      }
      footer={
        <>
          <Hotkey k="↑↓">{t("select")}</Hotkey>
          <Hotkey k="enter">{t("server screen")}</Hotkey>
          <Hotkey k="3">{t("terminal")}</Hotkey>
          <Hotkey k="5">{t("alerts")}</Hotkey>
        </>
      }
      footerNote={t("refresh 5s")}
    >
      <nav className="flex flex-wrap gap-2" aria-label={t("groups")}>
        {groups.map((g) => (
          <Button key={g} size="sm" variant={g === group ? "default" : "outline"} onClick={() => setGroup(g)}>
            {g || t("all")}
          </Button>
        ))}
      </nav>

      <Panel title={t("servers")} right={t("refresh 5s")}>
        {servers && servers.length === 0 ? (
          <div className="py-6 text-muted-foreground">
            {t("No servers yet.")}{" "}
            <a className="text-primary" href={href.add()}>
              {t("Add the first one")}
            </a>
            {t(": the wizard generates an ssh key, installs it with the password and sets up the agent.")}
          </div>
        ) : (
          <ScrollArea className="w-full">
            <table className="w-full min-w-[1000px] border-collapse text-left">
              <thead className="text-xs text-muted-foreground">
                <tr>
                  <th className="pb-2 font-normal">{t("server")}</th>
                  <th className="pb-2 font-normal">{t("group")}</th>
                  <th className="pb-2 font-normal">cpu</th>
                  <th className="pb-2 font-normal">ram</th>
                  <th className="pb-2 font-normal">{t("disk")}</th>
                  <th className="pb-2 font-normal">docker</th>
                  <th className="pb-2 font-normal">uptime · ram</th>
                  <th className="pb-2 font-normal">{t("payment")}</th>
                  <th className="pb-2 font-normal" />
                </tr>
              </thead>
              <tbody>
                {visible.map((s) => (
                  <Row key={s.id} s={s} onDelete={remove} />
                ))}
              </tbody>
            </table>
          </ScrollArea>
        )}
      </Panel>

      <AlertsPanel />
    </Shell>
  );
}
