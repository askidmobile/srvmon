import { useEffect, useMemo, useState } from "react";

import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { ScrollArea } from "@/components/ui/scroll-area";
import { api, type ServerView } from "@/lib/api";
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

function Row({ s }: { s: ServerView }) {
  const r = s.snapshot?.report;
  const memPct = r && r.host.mem_total > 0 ? ((r.host.mem_total - r.host.mem_available) / r.host.mem_total) * 100 : 0;
  const disk = r?.host.disks?.reduce((max, d) => Math.max(max, d.total ? (d.used / d.total) * 100 : 0), 0) ?? 0;
  const containers = r?.containers ?? [];
  const running = containers.filter((c) => c.state === "running").length;
  const bad = containers.filter((c) => c.state !== "running" || c.health === "unhealthy").length;
  return (
    <tr className="border-t border-border/60 hover:bg-secondary/60">
      <td className="py-1.5 pr-4">
        <span className={s.online ? "text-primary" : "text-crit"}>{s.online ? "●" : "○"}</span>{" "}
        <span className="text-hi">{s.name}</span>
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
        {r ? `${formatUptime(r.host.uptime_sec)} · ${formatBytes(r.host.mem_total)}` : "нет данных"}
      </td>
      <td className="whitespace-nowrap text-muted-foreground">
        {s.cost != null ? `${s.cost} ${s.currency}` : "—"}
        {s.next_payment && <div className="text-xs">до {s.next_payment}</div>}
      </td>
    </tr>
  );
}

export function Overview({ version, onLogout }: { version: string; onLogout: () => void }) {
  const [servers, setServers] = useState<ServerView[] | null>(null);
  const [error, setError] = useState("");
  const [group, setGroup] = useState("все");

  useEffect(() => {
    let alive = true;
    const load = () =>
      api
        .servers()
        .then((s) => alive && (setServers(s), setError("")))
        .catch((e) => {
          if (!alive) return;
          if (e?.status === 401) onLogout();
          else setError("нет связи с сервером");
        });
    load();
    const t = setInterval(load, 5000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [onLogout]);

  const groups = useMemo(() => ["все", ...new Set((servers ?? []).map((s) => s.group).filter(Boolean))], [servers]);
  const visible = (servers ?? []).filter((s) => group === "все" || s.group === group);
  const online = (servers ?? []).filter((s) => s.online).length;

  return (
    <div className="mx-auto flex min-h-screen max-w-[1600px] flex-col gap-5 p-4 sm:p-6">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <span className="font-bold text-primary">srvmon</span>
          <span className="text-muted-foreground">
            серверов {servers?.length ?? 0} · онлайн <span className="text-primary">{online}</span>
          </span>
        </div>
        <div className="flex items-center gap-3">
          {error && <span className="text-crit">{error}</span>}
          <span className="text-xs text-muted-foreground">v{version}</span>
          <Button
            variant="outline"
            onClick={async () => {
              await api.logout().catch(() => undefined);
              onLogout();
            }}
          >
            выйти
          </Button>
        </div>
      </header>

      <nav className="flex flex-wrap gap-2" aria-label="группы">
        {groups.map((g) => (
          <Button key={g} size="sm" variant={g === group ? "default" : "outline"} onClick={() => setGroup(g)}>
            {g}
          </Button>
        ))}
      </nav>

      <Panel title="серверы" right="обновление 5с">
        {servers && servers.length === 0 ? (
          <div className="py-6 text-muted-foreground">
            серверов пока нет. Добавьте первый на сервере мониторинга:
            <pre className="mt-3 overflow-x-auto text-foreground">
              docker compose exec server srvmon-server add-server -name my-server -group prod
            </pre>
            и запустите агент по инструкции из deploy/agent.
          </div>
        ) : (
          <ScrollArea className="w-full">
            <table className="w-full min-w-[1000px] border-collapse text-left">
              <thead className="text-xs text-muted-foreground">
                <tr>
                  <th className="pb-2 font-normal">сервер</th>
                  <th className="pb-2 font-normal">группа</th>
                  <th className="pb-2 font-normal">cpu</th>
                  <th className="pb-2 font-normal">ram</th>
                  <th className="pb-2 font-normal">диск</th>
                  <th className="pb-2 font-normal">docker</th>
                  <th className="pb-2 font-normal">uptime · ram</th>
                  <th className="pb-2 font-normal">оплата</th>
                </tr>
              </thead>
              <tbody>
                {visible.map((s) => (
                  <Row key={s.id} s={s} />
                ))}
              </tbody>
            </table>
          </ScrollArea>
        )}
      </Panel>
    </div>
  );
}
