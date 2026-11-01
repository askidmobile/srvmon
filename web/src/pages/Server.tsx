import * as React from "react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { AgentVersion } from "@/components/AgentVersion";
import { SudoMode } from "@/components/SudoMode";
import { Hotkey, Shell } from "@/components/Shell";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import {
  api,
  ApiError,
  historyRanges,
  type Container,
  type ContainerHistory,
  type DiskHealth,
  type History,
  type HistoryRange,
  type LogLine,
  type Process,
  type ServerCard,
  type ServerDetailView,
} from "@/lib/api";
import { locale, t, tn } from "@/lib/i18n";
import { href, navigate, typingIn, type Route } from "@/lib/router";
import { cn, formatBytes, formatUptime } from "@/lib/utils";

const C = {
  ok: "#3ddc6e",
  okDark: "#1f8f45",
  warn: "#f2c14e",
  crit: "#ff6b5e",
  tx: "#5ab0ff",
  cached: "#9b8cff",
  muted: "#7f9c86",
};

function levelColor(p: number) {
  if (p >= 85) return C.crit;
  if (p >= 60) return C.warn;
  return C.ok;
}

const pct = (v: number) => `${v >= 10 || v === 0 ? v.toFixed(0) : v.toFixed(1)}%`;
const rate = (bps: number) => `${formatBytes(bps)}/s`;

function ago(iso?: string): string {
  if (!iso) return "—";
  const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
  if (s < 60) return t("{n}s ago", { n: s });
  if (s < 3600) return t("{n}m ago", { n: Math.floor(s / 60) });
  if (s < 86400) return t("{n}h ago", { n: Math.floor(s / 3600) });
  return t("{n}d ago", { n: Math.floor(s / 86400) });
}

// A YYYY-MM-DD date in the interface locale; short drops the year.
function fmtDate(ymd: string, short = false): string {
  const [y, m, d] = ymd.split("-").map(Number);
  if (!y || !m || !d) return ymd;
  const opts: Intl.DateTimeFormatOptions = short
    ? { day: "2-digit", month: "2-digit" }
    : { day: "2-digit", month: "2-digit", year: "numeric" };
  return new Date(y, m - 1, d).toLocaleDateString(locale(), opts);
}

function daysUntil(ymd: string): number | null {
  const t = new Date(`${ymd}T00:00:00`).getTime();
  if (Number.isNaN(t)) return null;
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  return Math.round((t - today.getTime()) / 86400000);
}

// --- small building blocks ---

function Track({ value, color, className }: { value: number; color?: string; className?: string }) {
  const v = Math.min(100, Math.max(0, value));
  return (
    <div className={cn("flex h-2 min-w-0 flex-auto bg-[#16261b]", className)}>
      <div className="h-full" style={{ width: `${v}%`, background: color ?? levelColor(v) }} />
    </div>
  );
}

// Column graph like btop: one bar per bucket, empty buckets stay blank.
function Bars({
  values,
  max,
  height,
  color,
  down = false,
  grid = false,
  label,
}: {
  values: (number | null)[];
  max: number;
  height: number;
  color: string;
  down?: boolean;
  grid?: boolean;
  label: string;
}) {
  return (
    <div
      role="img"
      aria-label={label}
      className={cn("flex w-full gap-px", down ? "items-start" : "items-end")}
      style={{
        height,
        backgroundImage: grid ? "linear-gradient(to top, rgba(42,74,51,.5) 1px, transparent 1px)" : undefined,
        backgroundSize: grid ? `100% ${height / 4}px` : undefined,
      }}
    >
      {values.map((v, i) => (
        <div
          key={i}
          className="min-w-0 flex-1"
          style={{
            height: v == null || max <= 0 ? 0 : `${Math.max(v > 0 ? 2 : 0, Math.min(100, (v / max) * 100))}%`,
            background: color,
          }}
        />
      ))}
    </div>
  );
}

function Empty({ children }: { children: React.ReactNode }) {
  return <div className="py-4 text-muted-foreground">{children}</div>;
}

const scrollBox = "overflow-auto [scrollbar-color:#2a4a33_transparent] [scrollbar-width:thin]";

// --- panels ---

function CpuPanel({ data, hist }: { data: ServerDetailView; hist: History | null }) {
  const h = data.snapshot?.report.host;
  const d = data.detail;
  const inv = data.inventory;
  const right = [
    inv?.cpu_model,
    h && `${h.cpu_cores} vCPU`,
    h && `load ${h.load1.toFixed(2)} ${h.load5.toFixed(2)} ${h.load15.toFixed(2)}`,
    d?.temp_c != null && `${d.temp_c.toFixed(0)}°C`,
  ]
    .filter(Boolean)
    .join(" · ");
  const cols = d && d.cores.length > 16 ? 4 : 2;
  return (
    <Panel title="cpu" right={right}>
      <div className="flex flex-wrap gap-4">
        <div className="min-w-0 flex-[999_1_380px]">
          <div className="flex flex-wrap items-baseline justify-between gap-x-4">
            <span className="text-xl font-bold text-hi">{h ? pct(h.cpu_percent) : "—"}</span>
            <span className="text-xs text-muted-foreground">
              {hist?.range ?? ""}
              {d &&
                ` · usr ${pct(d.cpu_user)} · sys ${pct(d.cpu_system)} · iowait ${pct(d.cpu_iowait)} · steal ${pct(d.cpu_steal)}`}
            </span>
          </div>
          <Bars
            values={hist?.cpu ?? []}
            max={100}
            height={132}
            color={`linear-gradient(to top, ${C.okDark}, ${C.ok})`}
            grid
            label={t("cpu load")}
          />
        </div>
        <div
          className="grid min-w-0 flex-[1_1_260px] content-start gap-x-4 gap-y-1.5 pt-1.5"
          style={{ gridTemplateColumns: `repeat(${cols}, minmax(0, 1fr))` }}
        >
          {d ? (
            d.cores.map((v, i) => (
              <div key={i} className="grid grid-cols-[28px_minmax(0,1fr)_40px] items-center gap-2">
                <span className="text-muted-foreground">C{i}</span>
                <Track value={v} />
                <span className="text-right">{pct(v)}</span>
              </div>
            ))
          ) : (
            <span className="col-span-full text-xs text-muted-foreground">{t("per-core load shows up in a couple of seconds")}</span>
          )}
        </div>
      </div>
    </Panel>
  );
}

function MemRow({ name, value, total, color }: { name: string; value: number; total: number; color: string }) {
  const p = total > 0 ? (value / total) * 100 : 0;
  return (
    <div className="grid grid-cols-[96px_minmax(0,1fr)_44px_56px] items-center gap-2.5">
      <span className="truncate text-muted-foreground" title={name}>
        {name}
      </span>
      <Track value={p} color={color} />
      <span className="text-right">{pct(p)}</span>
      <span className="text-right text-hi">{formatBytes(value)}</span>
    </div>
  );
}

function MemoryPanel({ data }: { data: ServerDetailView }) {
  const h = data.snapshot?.report.host;
  if (!h) return <Panel title={t("memory")}>{<Empty>{t("no data")}</Empty>}</Panel>;
  const used = Math.max(0, h.mem_total - h.mem_available);
  const swapUsed = Math.max(0, h.swap_total - h.swap_free);
  const d = data.detail;
  return (
    <Panel title={t("memory")} right={formatBytes(h.mem_total)} className="min-w-0 flex-[1_1_360px]">
      <div className="flex flex-col gap-2">
        <MemRow name="used" value={used} total={h.mem_total} color={C.ok} />
        <MemRow name="available" value={h.mem_available} total={h.mem_total} color={C.tx} />
        {h.mem_cached != null && <MemRow name="cached" value={h.mem_cached} total={h.mem_total} color={C.cached} />}
        {h.mem_free != null && <MemRow name="free" value={h.mem_free} total={h.mem_total} color={C.muted} />}
        {h.swap_total > 0 ? (
          <MemRow name="swap" value={swapUsed} total={h.swap_total} color={C.warn} />
        ) : (
          <div className="text-xs text-muted-foreground">{t("swap is off")}</div>
        )}
      </div>
      <div className="mt-2.5 flex flex-col gap-1 border-t border-border pt-2">
        {(h.disks ?? []).map((disk) => {
          const p = disk.total ? (disk.used / disk.total) * 100 : 0;
          const full = data.disk_full_hours?.[disk.mount];
          return (
            <React.Fragment key={disk.mount}>
              <div className="grid grid-cols-[96px_minmax(0,1fr)_44px_56px] items-center gap-2.5">
                <span className="truncate text-muted-foreground" title={`${disk.mount} (${disk.fs})`}>
                  {disk.mount}
                </span>
                <Track value={p} />
                <span className="text-right">{pct(p)}</span>
                <span className="text-right text-hi" title={t("used {size}", { size: formatBytes(disk.used) })}>
                  {formatBytes(disk.total)}
                </span>
              </div>
              {full != null && (
                <div
                  className={cn("pl-[106px] text-xs", full < 24 ? "text-crit" : full < 24 * 7 ? "text-warn" : "text-muted-foreground")}
                  title={t("linear forecast from the growth over the last 6 hours")}
                >
                  └ {t("full in ~{time}", { time: fmtHours(full) })}
                </div>
              )}
            </React.Fragment>
          );
        })}
        {d && (
          <div className="text-muted-foreground">
            io: read <span className="text-hi">{rate(d.disk_read_bps)}</span> · write{" "}
            <span className="text-hi">{rate(d.disk_write_bps)}</span>
          </div>
        )}
      </div>
    </Panel>
  );
}

// Hours as "45m", "13h", "3d 4h".
function fmtHours(h: number): string {
  if (h < 1) return t("{n}m", { n: Math.max(1, Math.round(h * 60)) });
  if (h < 48) return t("{n}h", { n: Math.round(h) });
  const d = Math.floor(h / 24);
  const rest = Math.round(h - d * 24);
  return t("{n}d", { n: d }) + (rest ? " " + t("{n}h", { n: rest }) : "");
}

function lastValue(xs: (number | null)[] | undefined): number | null {
  if (!xs) return null;
  for (let i = xs.length - 1; i >= 0; i--) if (xs[i] != null) return xs[i];
  return null;
}

function NetPanel({ data, hist }: { data: ServerDetailView; hist: History | null }) {
  const net = data.snapshot?.report.host.net ?? [];
  const rx = hist?.rx ?? [];
  const tx = hist?.tx ?? [];
  const peakRx = Math.max(0, ...rx.map((v) => v ?? 0));
  const peakTx = Math.max(0, ...tx.map((v) => v ?? 0));
  const max = Math.max(peakRx, peakTx, 1);
  const totalRx = net.reduce((a, n) => a + n.rx_bytes, 0);
  const totalTx = net.reduce((a, n) => a + n.tx_bytes, 0);
  const right = [net.map((n) => n.name).join(", "), data.inventory?.ips?.[0]].filter(Boolean).join(" · ");
  return (
    <Panel title={t("network")} right={right} className="min-w-0 flex-[1_1_360px]">
      <div className="flex flex-wrap justify-between gap-x-3">
        <span>
          ↓ rx <span className="font-bold text-primary">{rate(lastValue(rx) ?? 0)}</span>
        </span>
        <span className="text-muted-foreground">
          {t("peak {rate} · total {size}", { rate: rate(peakRx), size: formatBytes(totalRx) })}
        </span>
      </div>
      <Bars values={rx} max={max} height={56} color={C.ok} label={t("incoming traffic")} />
      <div className="border-t border-border" />
      <Bars values={tx} max={max} height={40} color={C.tx} down label={t("outgoing traffic")} />
      <div className="flex flex-wrap justify-between gap-x-3">
        <span>
          ↑ tx <span className="font-bold text-tx">{rate(lastValue(tx) ?? 0)}</span>
        </span>
        <span className="text-muted-foreground">
          {t("peak {rate} · total {size}", { rate: rate(peakTx), size: formatBytes(totalTx) })}
          {data.detail && ` · tcp ${data.detail.tcp_conns}`}
        </span>
      </div>
    </Panel>
  );
}

type ProcSort = "cpu" | "mem" | "pid" | "name";

function ProcessPanel({ data, filterRef }: { data: ServerDetailView; filterRef: React.RefObject<HTMLInputElement | null> }) {
  const [filter, setFilter] = useState("");
  const [sort, setSort] = useState<ProcSort>("cpu");
  const d = data.detail;
  const rows = useMemo(() => {
    const q = filter.trim().toLowerCase();
    const list = (d?.procs ?? []).filter(
      (p) =>
        !q ||
        p.name.toLowerCase().includes(q) ||
        (p.container ?? "").toLowerCase().includes(q) ||
        (p.cmd ?? "").toLowerCase().includes(q) ||
        String(p.pid) === q,
    );
    const by: Record<ProcSort, (a: Process, b: Process) => number> = {
      cpu: (a, b) => b.cpu - a.cpu || b.mem - a.mem,
      mem: (a, b) => b.mem - a.mem,
      pid: (a, b) => a.pid - b.pid,
      name: (a, b) => a.name.localeCompare(b.name),
    };
    return [...list].sort(by[sort]);
  }, [d, filter, sort]);
  const th = (key: ProcSort, label: string, right = false) => (
    <th className={cn("px-1.5 py-1 font-normal whitespace-nowrap", right && "text-right")}>
      <button
        type="button"
        className={cn("cursor-pointer", sort === key ? "text-primary" : "hover:text-primary")}
        onClick={() => setSort(key)}
      >
        {label}
        {sort === key && " ▾"}
      </button>
    </th>
  );
  return (
    <Panel
      title={t("processes")}
      right={d ? t("sort: {sort} · {total} total · top {top}", { sort, total: d.procs_total, top: d.procs.length }) : undefined}
      className="flex min-w-0 flex-[1_1_380px] flex-col"
    >
      <div className="mb-2 flex gap-2">
        <label htmlFor="proc-filter" className="self-center text-muted-foreground">
          /
        </label>
        <input
          id="proc-filter"
          ref={filterRef}
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Escape") {
              setFilter("");
              e.currentTarget.blur();
              e.stopPropagation();
            }
          }}
          placeholder={t("filter: name, container, pid")}
          className="h-[26px] min-w-0 flex-auto border border-input bg-field px-2 text-hi placeholder:text-muted-foreground"
        />
      </div>
      {d ? (
        <div className={cn(scrollBox, "max-h-[560px] flex-auto")}>
          <table className="w-full border-collapse text-left">
            <thead className="sticky top-0 bg-background text-muted-foreground">
              <tr className="border-b border-border">
                {th("pid", "pid", true)}
                {th("name", t("program"))}
                <th className="px-1.5 py-1 font-normal">{t("cont.")}</th>
                {th("cpu", "cpu", true)}
                {th("mem", "mem", true)}
              </tr>
            </thead>
            <tbody>
              {rows.map((p) => (
                <tr key={p.pid} className="hover:bg-secondary/60">
                  <td className="px-1.5 text-right whitespace-nowrap text-muted-foreground">{p.pid}</td>
                  <td
                    className="w-full max-w-0 truncate px-1.5 text-hi"
                    title={`${p.cmd || p.name}${p.user ? `\nuser: ${p.user}` : ""}`}
                  >
                    {p.name}
                  </td>
                  <td className="max-w-[72px] truncate px-1.5 text-muted-foreground" title={p.container}>
                    {p.container || "─"}
                  </td>
                  <td className="px-1.5 text-right whitespace-nowrap" style={{ color: levelColor(p.cpu / Math.max(1, d.cores.length) * 4) }}>
                    {p.cpu.toFixed(1)}
                  </td>
                  <td className="px-1.5 text-right whitespace-nowrap">{formatBytes(p.mem)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {rows.length === 0 && <Empty>{t("nothing found")}</Empty>}
        </div>
      ) : (
        <Empty>
          {data.online
            ? t("the agent is switching to detailed mode, processes show up in a couple of seconds")
            : t("the server is offline")}
        </Empty>
      )}
    </Panel>
  );
}

function containerRank(c: Container) {
  if (c.state !== "running" || c.health === "unhealthy") return 0;
  if (c.health === "starting" || (c.restarts ?? 0) > 0) return 1;
  return 2;
}

const containerRanges: HistoryRange[] = ["1h", "6h", "24h", "7d"];

// CPU and memory of one container over a window, from VictoriaMetrics.
function ContainerChart({ serverId, name }: { serverId: number; name: string }) {
  const [range, setRange] = useState<HistoryRange>("24h");
  const [hist, setHist] = useState<ContainerHistory | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let alive = true;
    const tick = () =>
      api
        .containerHistory(serverId, name, range)
        .then((h) => {
          if (!alive) return;
          setHist(h);
          setError("");
        })
        .catch(() => alive && setError(t("no connection to the metrics storage")));
    tick();
    const id = setInterval(tick, 60000);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, [serverId, name, range]);
  const cpu = hist?.cpu ?? [];
  const mem = hist?.mem ?? [];
  const nums = (xs: (number | null)[]) => xs.filter((v): v is number => v != null);
  const cpuMax = Math.max(100, ...nums(cpu));
  const memMax = Math.max(1, ...nums(mem));
  const empty = hist && nums(cpu).length === 0 && nums(mem).length === 0;
  const stat = (xs: (number | null)[], f: (v: number) => string) => {
    const v = nums(xs);
    if (v.length === 0) return "─";
    return `${t("avg")} ${f(v.reduce((a, b) => a + b, 0) / v.length)} · ${t("max")} ${f(Math.max(...v))}`;
  };
  return (
    <div className="flex flex-col gap-2 border-l-2 border-primary/60 py-2 pl-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="text-hi">{name}</span>
        <span className="flex items-center gap-1" role="group" aria-label={t("chart window")}>
          {containerRanges.map((x) => (
            <Button key={x} size="sm" variant={x === range ? "default" : "outline"} onClick={() => setRange(x)}>
              {x}
            </Button>
          ))}
        </span>
        {error && <span className="text-crit">{error}</span>}
      </div>
      {empty ? (
        <span className="text-muted-foreground">{t("no history yet: it is written by agents of this version and newer")}</span>
      ) : (
        <div className="grid gap-x-6 gap-y-2 md:grid-cols-2">
          <div className="min-w-0">
            <div className="flex justify-between text-xs text-muted-foreground">
              <span>cpu · {t("% of one core")}</span>
              <span>{stat(cpu, pct)}</span>
            </div>
            <Bars values={cpu} max={cpuMax} height={64} color={C.ok} grid label={t("container cpu")} />
          </div>
          <div className="min-w-0">
            <div className="flex justify-between text-xs text-muted-foreground">
              <span>mem</span>
              <span>{stat(mem, formatBytes)}</span>
            </div>
            <Bars values={mem} max={memMax} height={64} color={C.cached} grid label={t("container memory")} />
          </div>
        </div>
      )}
    </div>
  );
}

function DockerPanel({ data, onLogs }: { data: ServerDetailView; onLogs: (name: string) => void }) {
  const r = data.snapshot?.report;
  const [open, setOpen] = useState("");
  const list = useMemo(
    () => [...(r?.containers ?? [])].sort((a, b) => containerRank(a) - containerRank(b) || a.name.localeCompare(b.name)),
    [r],
  );
  if (!r?.docker_ok) {
    return (
      <Panel title="docker">
        <Empty>{t("docker is not available to the agent (no /var/run/docker.sock) or not installed")}</Empty>
      </Panel>
    );
  }
  const running = list.filter((c) => c.state === "running").length;
  const projects = [...new Set(list.map((c) => c.compose).filter(Boolean))];
  const right = [`${running} running`, `${list.length - running} stopped`, projects.length > 0 && `compose: ${projects.join(", ")}`]
    .filter(Boolean)
    .join(" · ");
  return (
    <Panel title="docker" right={right}>
      <div className={scrollBox}>
        <table className="w-full min-w-[960px] border-collapse text-left">
          <thead className="text-muted-foreground">
            <tr className="border-b border-border">
              {["st", "name", "image", "status", "cpu", "mem", "ports", "rest.", "health", ""].map((h, i) => (
                <th key={i} className={cn("px-1.5 py-1 font-normal", (h === "mem" || h === "rest.") && "text-right")}>
                  {h && t(h)}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {list.map((c) => {
              const bad = c.state !== "running" || c.health === "unhealthy";
              const warn = !bad && (c.health === "starting" || (c.restarts ?? 0) > 0);
              return (
                <React.Fragment key={c.id}>
                <tr className="hover:bg-secondary/60">
                  <td className={cn("px-1.5", bad ? "text-crit" : warn ? "text-warn" : "text-primary")}>
                    {bad ? "○" : warn ? "▲" : "●"}
                  </td>
                  <td className="px-1.5 whitespace-nowrap text-hi">
                    <button
                      type="button"
                      className="cursor-pointer text-left hover:text-primary"
                      aria-expanded={open === c.name}
                      title={t("cpu and memory history")}
                      onClick={() => setOpen(open === c.name ? "" : c.name)}
                    >
                      {open === c.name ? "▾" : "▸"} {c.name}
                    </button>
                  </td>
                  <td className="max-w-[280px] truncate px-1.5 text-muted-foreground" title={c.image}>
                    {c.image}
                  </td>
                  <td className="px-1.5 whitespace-nowrap">{c.status}</td>
                  <td className="px-1.5">
                    {c.cpu_percent != null ? (
                      <div className="flex w-[120px] items-center gap-1.5">
                        <Track value={c.cpu_percent} />
                        <span className="w-10 text-right">{pct(c.cpu_percent)}</span>
                      </div>
                    ) : (
                      <span className="text-muted-foreground">─</span>
                    )}
                  </td>
                  <td className="px-1.5 text-right whitespace-nowrap">
                    {c.mem_bytes != null && c.state === "running" ? formatBytes(c.mem_bytes) : "─"}
                  </td>
                  <td className="max-w-[200px] truncate px-1.5 text-muted-foreground" title={c.ports?.join(", ")}>
                    {c.ports?.length ? c.ports.join(", ") : "─"}
                  </td>
                  <td className={cn("px-1.5 text-right", (c.restarts ?? 0) > 0 && "text-warn")}>{c.restarts ?? "─"}</td>
                  <td
                    className={cn(
                      "px-1.5",
                      c.health === "healthy" && "text-primary",
                      c.health === "starting" && "text-warn",
                      c.health === "unhealthy" && "text-crit",
                      !c.health && "text-muted-foreground",
                    )}
                  >
                    {c.health || "─"}
                  </td>
                  <td className="px-1.5">
                    <Button size="sm" variant="outline" onClick={() => onLogs(c.name)} title={t("show this container's lines in the logs")}>
                      {t("logs")}
                    </Button>
                  </td>
                </tr>
                {open === c.name && (
                  <tr>
                    <td colSpan={10} className="px-1.5">
                      <ContainerChart serverId={data.id} name={c.name} />
                    </td>
                  </tr>
                )}
                </React.Fragment>
              );
            })}
          </tbody>
        </table>
        {list.length === 0 && <Empty>{t("no containers")}</Empty>}
      </div>
    </Panel>
  );
}

// --- disk health ---

function smartBad(d: DiskHealth): boolean {
  return (
    d.passed === false ||
    !!d.critical_warning ||
    (d.pending ?? 0) > 0 ||
    (d.uncorrectable ?? 0) > 0 ||
    (d.percent_used ?? 0) >= 100 ||
    (!!d.spare_threshold && d.available_spare != null && d.available_spare <= d.spare_threshold)
  );
}

function smartWarn(d: DiskHealth): boolean {
  return (d.reallocated ?? 0) > 0 || (d.media_errors ?? 0) > 0 || (d.percent_used ?? 0) >= 80 || (d.temp_c ?? 0) >= 60;
}

// Why a disk was not read (DiskHealth.error), as English source strings.
const smartErrors: Record<string, string> = {
  standby: "asleep (standby), not woken up",
  unsupported: "no S.M.A.R.T. through this controller (virtual disk or RAID)",
  permission: "no access: the agent container lacks device permissions",
};

function SmartPanel({ data, onChanged }: { data: ServerDetailView; onChanged: () => void }) {
  const disks = data.smart ?? [];
  const on = !!data.inventory?.smart;
  const updating = data.agent_update?.state === "running";
  const [error, setError] = useState("");
  const toggle = (enabled: boolean) => {
    if (
      enabled &&
      !confirm(
        t(
          "The agent will be restarted with raw read access to the disks (CAP_SYS_RAWIO, CAP_SYS_ADMIN, block devices read-only) and will read S.M.A.R.T. every 30 minutes. Sleeping disks are not woken up. Continue?",
        ),
      )
    )
      return;
    setError("");
    api
      .agentSmart(data.id, enabled)
      .then(onChanged)
      .catch((e) => setError(e?.code === "update_running" ? t("already updating") : t("could not start the update")));
  };
  const right = data.smart_at ? t("read {ago} · every 30 min", { ago: ago(data.smart_at) }) : undefined;
  let body: React.ReactNode;
  if (disks.length > 0) {
    body = (
      <div className={scrollBox}>
        <table className="w-full min-w-[760px] border-collapse text-left">
          <thead className="text-muted-foreground">
            <tr className="border-b border-border">
              {["st", "disk", "model", "health", "temp", "power on", "wear / sectors"].map((h) => (
                <th key={h} className="px-1.5 py-1 font-normal">
                  {t(h)}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {disks.map((d) => {
              const bad = smartBad(d);
              const warn = !bad && smartWarn(d);
              const facts =
                d.type === "nvme"
                  ? [
                      d.percent_used != null && t("used {n}%", { n: d.percent_used }),
                      d.available_spare != null && t("spare {n}%", { n: d.available_spare }),
                      d.media_errors != null && t("media errors {n}", { n: d.media_errors }),
                    ]
                  : [
                      d.reallocated != null && `realloc ${d.reallocated}`,
                      d.pending != null && `pending ${d.pending}`,
                      d.uncorrectable != null && `uncorr ${d.uncorrectable}`,
                    ];
              const st = d.error ? "text-muted-foreground" : bad ? "text-crit" : warn ? "text-warn" : "text-primary";
              return (
                <tr key={d.device} className="hover:bg-secondary/60">
                  <td className={cn("px-1.5", st)}>{d.error ? "·" : bad ? "○" : warn ? "▲" : "●"}</td>
                  <td className="px-1.5 whitespace-nowrap text-hi">{d.device}</td>
                  <td className="max-w-[260px] truncate px-1.5 text-muted-foreground" title={d.serial ? `${d.model} · ${d.serial}` : d.model}>
                    {d.model || "─"}
                  </td>
                  {d.error ? (
                    <td colSpan={4} className="px-1.5 text-muted-foreground">
                      {t(smartErrors[d.error] ?? d.error)}
                    </td>
                  ) : (
                    <>
                      <td className={cn("px-1.5", d.passed === false ? "text-crit" : d.passed ? "text-primary" : "text-muted-foreground")}>
                        {d.passed == null ? "─" : d.passed ? "PASSED" : "FAILED"}
                        {!!d.critical_warning && <span className="text-crit"> · warn 0x{d.critical_warning.toString(16)}</span>}
                      </td>
                      <td className={cn("px-1.5", (d.temp_c ?? 0) >= 60 && "text-warn")}>{d.temp_c != null ? `${d.temp_c}°C` : "─"}</td>
                      <td className="px-1.5 whitespace-nowrap">{d.power_on_hours != null ? fmtHours(d.power_on_hours) : "─"}</td>
                      <td className="px-1.5 whitespace-nowrap">{facts.filter(Boolean).join(" · ") || "─"}</td>
                    </>
                  )}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    );
  } else if (on) {
    body = <Empty>{t("S.M.A.R.T. is on: waiting for the first read of the disks")}</Empty>;
  } else {
    body = (
      <div className="flex flex-col gap-1 py-2 text-muted-foreground">
        <span>
          {t("S.M.A.R.T. is off. It needs raw read access to the disks, so it is enabled per server; otherwise the agent stays unprivileged.")}
        </span>
        {!data.has_ssh && (
          <span>{t("No ssh access from the panel: set SRVMON_SMART=1 and the device options of deploy/agent/docker-compose.yml on the server.")}</span>
        )}
      </div>
    );
  }
  return (
    <Panel title={t("disk health · S.M.A.R.T.")} right={right}>
      {body}
      {data.has_ssh && (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          {updating ? (
            <span className="text-warn">{t("updating…")}</span>
          ) : on ? (
            <Button size="sm" variant="outline" onClick={() => toggle(false)}>
              {t("turn S.M.A.R.T. off")}
            </Button>
          ) : (
            <Button size="sm" variant="outline" onClick={() => toggle(true)}>
              {t("turn S.M.A.R.T. on")}
            </Button>
          )}
          {data.agent_update?.state === "failed" && (
            <span className="text-crit">{t("update failed: {error}", { error: data.agent_update.error ?? "" })}</span>
          )}
          {error && <span className="text-crit">{error}</span>}
        </div>
      )}
    </Panel>
  );
}

function fmtLogTime(iso: string): string {
  const d = new Date(iso);
  const time = d.toLocaleTimeString(locale(), { hour12: false });
  return d.toDateString() === new Date().toDateString()
    ? time
    : `${d.toLocaleDateString(locale(), { day: "2-digit", month: "2-digit" })} ${time}`;
}

function LogsPanel({
  logs,
  source,
  setSource,
  watching,
  panelRef,
}: {
  logs: LogLine[];
  source: string;
  setSource: (s: string) => void;
  watching: boolean;
  panelRef: React.RefObject<HTMLDivElement | null>;
}) {
  const [level, setLevel] = useState<"all" | "ERR" | "WARN">("all");
  const hourAgo = Date.now() - 3600_000;
  const errHour = logs.filter((l) => l.level === "ERR" && new Date(l.ts).getTime() > hourAgo).length;
  const rows = logs.filter((l) => (level === "all" || l.level === level) && (!source || l.source === source));
  return (
    <div ref={panelRef} className="min-w-0 flex-[999_1_760px]">
      <Panel title={t("logs")} right={t("syslog + docker · warn and up · errors in the last hour: {n}", { n: errHour })} className="h-full">
        <div className="mb-2 flex flex-wrap items-center gap-2">
          {(["all", "ERR", "WARN"] as const).map((l) => (
            <Button key={l} size="sm" variant={level === l ? "default" : "outline"} onClick={() => setLevel(l)}>
              {l === "all" ? t("all") : l}
            </Button>
          ))}
          {source && (
            <Button size="sm" variant="outline" onClick={() => setSource("")} title={t("clear the source filter")}>
              {t("source:")} {source} ✕
            </Button>
          )}
        </div>
        <div className={cn(scrollBox, "max-h-[360px]")}>
          <table className="w-full min-w-[640px] border-collapse text-left">
            <thead className="sticky top-0 bg-background text-muted-foreground">
              <tr className="border-b border-border">
                <th className="px-1.5 py-1 font-normal">{t("time")}</th>
                <th className="px-1.5 py-1 font-normal">{t("lvl")}</th>
                <th className="px-1.5 py-1 font-normal">{t("source")}</th>
                <th className="px-1.5 py-1 font-normal">{t("message")}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((l, i) => (
                <tr key={`${l.ts}-${i}`} className="align-top hover:bg-secondary/60">
                  <td className="px-1.5 whitespace-nowrap text-muted-foreground">{fmtLogTime(l.ts)}</td>
                  <td className={cn("px-1.5", l.level === "ERR" ? "text-crit" : "text-warn")}>{l.level}</td>
                  <td className="px-1.5 whitespace-nowrap">
                    <button type="button" className="cursor-pointer hover:text-primary" onClick={() => setSource(l.source)}>
                      {l.source}
                    </button>
                  </td>
                  <td className="px-1.5 break-all">{l.msg}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {rows.length === 0 && (
            <Empty>
              {logs.length === 0
                ? watching
                  ? t("no warnings or errors found (the agent reads /var/log/syslog and container logs)")
                  : t("logs are collected while this screen is open")
                : t("no lines match the filter")}
            </Empty>
          )}
        </div>
      </Panel>
    </div>
  );
}

function cardOf(s: ServerDetailView): ServerCard {
  return {
    description: s.description,
    project: s.project,
    group: s.group,
    tags: s.tags,
    provider: s.provider,
    plan: s.plan,
    cost: s.cost ?? null,
    currency: s.currency,
    next_payment: s.next_payment,
  };
}

const fieldErrors: Record<string, string> = {
  description: "the description is too long",
  project: "project: up to 64 characters",
  group: "group: up to 64 characters",
  provider: "provider: up to 64 characters",
  plan: "plan: up to 128 characters",
  currency: "currency: up to 8 characters",
  next_payment: "payment date as YYYY-MM-DD",
  cost: "the cost must be a number ≥ 0",
  tags: "tags: up to 20, no spaces, up to 32 characters each",
};

function PassportForm({ data, onDone }: { data: ServerDetailView; onDone: (saved: boolean) => void }) {
  const [card, setCard] = useState(() => cardOf(data));
  const [tags, setTags] = useState(() => data.tags.map((t) => `#${t}`).join(" "));
  const [cost, setCost] = useState(() => (data.cost != null ? String(data.cost) : ""));
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const field = (key: keyof ServerCard, label: string, opts: { type?: string; placeholder?: string } = {}) => (
    <>
      <label htmlFor={`pp-${key}`} className="self-center text-muted-foreground">
        {label}
      </label>
      <input
        id={`pp-${key}`}
        type={opts.type ?? "text"}
        placeholder={opts.placeholder}
        value={String(card[key] ?? "")}
        onChange={(e) => setCard({ ...card, [key]: e.target.value })}
        className="h-7 min-w-0 border border-input bg-field px-2 text-hi placeholder:text-muted-foreground"
      />
    </>
  );
  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    const c = cost.trim().replace(",", ".");
    const parsed = c === "" ? null : Number(c);
    if (parsed != null && !Number.isFinite(parsed)) {
      setError(t(fieldErrors.cost));
      return;
    }
    setBusy(true);
    setError("");
    try {
      await api.updateCard(data.id, {
        ...card,
        cost: parsed,
        tags: tags.split(/[\s,]+/).map((t) => t.replace(/^#/, "")).filter(Boolean),
      });
      onDone(true);
    } catch (err) {
      setError(err instanceof ApiError && err.status === 400 ? t("check the form fields") : t("could not save"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form onSubmit={save} className="flex flex-col gap-3">
      <div className="grid grid-cols-[120px_minmax(0,1fr)] gap-x-3 gap-y-1.5">
        {field("description", t("description"))}
        {field("project", t("project"))}
        {field("group", t("group"), { placeholder: "prod / staging / infra" })}
        <label htmlFor="pp-tags" className="self-center text-muted-foreground">
          {t("tags")}
        </label>
        <input
          id="pp-tags"
          value={tags}
          onChange={(e) => setTags(e.target.value)}
          placeholder="#db #public"
          className="h-7 min-w-0 border border-input bg-field px-2 text-hi placeholder:text-muted-foreground"
        />
        {field("provider", t("provider"), { placeholder: "Hetzner" })}
        {field("plan", t("plan"), { placeholder: "CPX31" })}
        <label htmlFor="pp-cost" className="self-center text-muted-foreground">
          {t("cost")}
        </label>
        <div className="flex gap-2">
          <input
            id="pp-cost"
            inputMode="decimal"
            value={cost}
            onChange={(e) => setCost(e.target.value)}
            placeholder="15.90"
            className="h-7 min-w-0 flex-auto border border-input bg-field px-2 text-hi placeholder:text-muted-foreground"
          />
          <input
            aria-label={t("currency")}
            value={card.currency}
            onChange={(e) => setCard({ ...card, currency: e.target.value })}
            placeholder="EUR"
            className="h-7 w-16 border border-input bg-field px-2 text-hi placeholder:text-muted-foreground"
          />
        </div>
        {field("next_payment", t("next payment"), { type: "date" })}
      </div>
      {error && <div className="text-crit">{error}</div>}
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={busy}>
          {t("save")}
        </Button>
        <Button type="button" size="sm" variant="outline" onClick={() => onDone(false)}>
          {t("cancel")}
        </Button>
      </div>
    </form>
  );
}

function Passport({
  data,
  editing,
  setEditing,
  onSaved,
}: {
  data: ServerDetailView;
  editing: boolean;
  setEditing: (v: boolean) => void;
  onSaved: () => void;
}) {
  const inv = data.inventory;
  const r = data.snapshot?.report;
  const days = data.next_payment ? daysUntil(data.next_payment) : null;
  const kv: [string, React.ReactNode][] = [
    [t("description"), data.description || "—"],
    [t("project"), data.project || "—"],
    [t("group"), data.group || "—"],
    [t("tags"), data.tags.length ? data.tags.map((tag) => `#${tag}`).join(" ") : "—"],
    [t("provider"), data.provider || "—"],
    [t("plan"), data.plan || "—"],
    [
      t("cost"),
      data.cost != null ? (
        <span className="text-hi">
          {data.cost} {data.currency}
        </span>
      ) : (
        "—"
      ),
    ],
    [
      t("next payment"),
      data.next_payment ? (
        <span>
          {fmtDate(data.next_payment)}
          {days != null && (
            <span className={cn("ml-1", days <= 3 ? "text-warn" : "text-muted-foreground")}>
              · {days >= 0 ? tn(days, "in {n} day", "in {n} days") : tn(-days, "{n} day overdue", "{n} days overdue")}
            </span>
          )}
        </span>
      ) : (
        "—"
      ),
    ],
    ["ssh", data.host ? `${data.host}:${data.port}` : t("not set up")],
    ...(data.has_ssh ? ([["sudo", <SudoMode key="sudo" id={data.id} />]] as [string, React.ReactNode][]) : []),
    [t("os"), inv?.os || "—"],
    [t("kernel"), inv?.kernel || "—"],
    ["docker", inv?.docker_version || (r?.docker_ok ? t("yes") : "—")],
    ["ip", inv?.ips?.join(", ") || "—"],
    [
      t("agent"),
      <span key="agent">
        <AgentVersion s={data} onStarted={onSaved} />
        {r && <span className="text-muted-foreground"> · push {ago(data.snapshot?.received)}</span>}
      </span>,
    ],
  ];
  return (
    <Panel
      title={t("passport")}
      right={
        editing ? (
          t("editing")
        ) : (
          <button type="button" className="cursor-pointer hover:text-primary" onClick={() => setEditing(true)}>
            e {t("edit")}
          </button>
        )
      }
      className="min-w-0 flex-[1_1_380px]"
    >
      {editing ? (
        <PassportForm
          data={data}
          onDone={(saved) => {
            setEditing(false);
            if (saved) onSaved();
          }}
        />
      ) : (
        <div className="grid grid-cols-[120px_minmax(0,1fr)] gap-x-3 gap-y-1">
          {kv.map(([k, v]) => (
            <React.Fragment key={k}>
              <span className="text-muted-foreground">{k}</span>
              <span className="min-w-0 break-words">{v}</span>
            </React.Fragment>
          ))}
        </div>
      )}
    </Panel>
  );
}

// --- page ---

const historyEvery: Record<HistoryRange, number> = { "15m": 5000, "1h": 15000, "6h": 60000, "24h": 60000, "7d": 120000 };

function loadRange(): HistoryRange {
  try {
    const v = localStorage.getItem("srvmon.range") as HistoryRange | null;
    if (v && historyRanges.includes(v)) return v;
  } catch {
    /* storage unavailable */
  }
  return "15m";
}

export function ServerScreen({
  id,
  route,
  version,
  onLogout,
}: {
  id: number;
  route: Route;
  version: string;
  onLogout: () => void;
}) {
  const [data, setData] = useState<ServerDetailView | null>(null);
  const [hist, setHist] = useState<History | null>(null);
  const [error, setError] = useState("");
  const [missing, setMissing] = useState(false);
  const [range, setRangeState] = useState<HistoryRange>(loadRange);
  const [editing, setEditing] = useState(false);
  const [logSource, setLogSource] = useState("");
  const filterRef = useRef<HTMLInputElement>(null);
  const logsRef = useRef<HTMLDivElement>(null);

  const setRange = useCallback((r: HistoryRange) => {
    setRangeState(r);
    try {
      localStorage.setItem("srvmon.range", r);
    } catch {
      /* ignore */
    }
  }, []);

  const fail = useCallback(
    (e: unknown) => {
      if (e instanceof ApiError && e.status === 401) onLogout();
      else if (e instanceof ApiError && e.status === 404) setMissing(true);
      else setError(t("no connection to the server"));
    },
    [onLogout],
  );

  const load = useCallback(
    () =>
      api
        .server(id)
        .then((d) => {
          setData(d);
          setError("");
        })
        .catch(fail),
    [id, fail],
  );

  // The screen polls every 2 s; while it does, the agent sends detailed data.
  useEffect(() => {
    let alive = true;
    const tick = () => alive && load();
    tick();
    const t = setInterval(tick, 2000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [load]);

  useEffect(() => {
    let alive = true;
    const tick = () =>
      api
        .history(id, range)
        .then((h) => alive && setHist(h))
        .catch(() => undefined);
    tick();
    const t = setInterval(tick, historyEvery[range]);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [id, range]);

  const showLogs = useCallback((name: string) => {
    setLogSource(name);
    logsRef.current?.scrollIntoView({ behavior: "smooth", block: "start" });
  }, []);

  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if (typingIn(e) || e.ctrlKey || e.metaKey || e.altKey) return;
      const i = historyRanges.indexOf(range);
      switch (e.key) {
        case "Escape":
          if (editing) setEditing(false);
          else navigate(href.overview());
          break;
        case "t":
          navigate(href.terminal(id));
          break;
        case "p":
          filterRef.current?.focus();
          break;
        case "l":
          logsRef.current?.scrollIntoView({ behavior: "smooth", block: "start" });
          break;
        case "e":
          setEditing(true);
          break;
        case "+":
        case "=":
          setRange(historyRanges[Math.min(historyRanges.length - 1, i + 1)]);
          break;
        case "-":
          setRange(historyRanges[Math.max(0, i - 1)]);
          break;
        default:
          return;
      }
      e.preventDefault();
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  }, [id, range, editing, setRange]);

  const r = data?.snapshot?.report;
  const status = data && (
    <span className={data.online ? "text-primary" : "text-crit"}>
      {data.online ? "● online" : "○ offline"}
    </span>
  );

  let body: React.ReactNode;
  if (missing) {
    body = (
      <p className="text-muted-foreground">
        {t("Server not found.")}{" "}
        <a href={href.overview()} className="text-primary">
          ← {t("to the overview")}
        </a>
      </p>
    );
  } else if (!data) {
    body = <p className="text-muted-foreground">{error || t("loading…")}</p>;
  } else {
    const ip = data.inventory?.ips?.[0] || data.host;
    const facts = [
      ip,
      data.inventory?.os,
      data.provider,
      data.cost != null && `${data.cost} ${data.currency}`,
      data.next_payment && t("payment {date}", { date: fmtDate(data.next_payment, true) }),
    ].filter(Boolean);
    body = (
      <>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2.5">
          <span className="text-muted-foreground">
            <a href={href.overview()} className="text-primary hover:text-primary-hover">
              {t("servers")}
            </a>
            {data.group && ` / ${data.group}`}
            {data.project && ` / ${data.project}`} /
          </span>
          <h1 className="text-lg font-bold text-hi">{data.name}</h1>
          {data.online ? (
            <span className="text-primary">● online {r ? formatUptime(r.host.uptime_sec) : ""}</span>
          ) : (
            <span className="text-crit">○ offline · {t("last push")} {ago(data.snapshot?.received ?? data.last_seen)}</span>
          )}
          {facts.length > 0 && <span className="text-muted-foreground">{facts.join(" · ")}</span>}
          {data.tags.length > 0 && (
            <span>
              {data.tags.map((tag) => (
                <span key={tag} className="mr-1 inline-block border border-border px-1.5 text-xs leading-[17px] text-[#9fc3a7]">
                  #{tag}
                </span>
              ))}
            </span>
          )}
          <span className="flex-auto" />
          <Button asChild>
            <a href={href.terminal(id)}>&gt;_ {t("terminal")}</a>
          </Button>
          <Button variant="outline" onClick={() => setEditing(true)}>
            ✎ {t("passport")}
          </Button>
          <span className="flex items-center gap-1" role="group" aria-label={t("chart window")}>
            {historyRanges.map((x) => (
              <Button key={x} size="sm" variant={x === range ? "default" : "outline"} onClick={() => setRange(x)}>
                {x}
              </Button>
            ))}
          </span>
        </div>
        {error && <div className="text-crit">{error}</div>}

        <div className="flex flex-wrap items-stretch gap-x-4 gap-y-5">
          <div className="flex min-w-0 flex-[999_1_760px] flex-col gap-5">
            <CpuPanel data={data} hist={hist} />
            <div className="flex flex-wrap gap-x-4 gap-y-5">
              <MemoryPanel data={data} />
              <NetPanel data={data} hist={hist} />
            </div>
          </div>
          <ProcessPanel data={data} filterRef={filterRef} />
        </div>

        <DockerPanel data={data} onLogs={showLogs} />
        <SmartPanel data={data} onChanged={load} />

        <div className="flex flex-wrap gap-x-4 gap-y-5">
          <LogsPanel
            logs={data.logs}
            source={logSource}
            setSource={setLogSource}
            watching={!!data.detail}
            panelRef={logsRef}
          />
          <Passport data={data} editing={editing} setEditing={setEditing} onSaved={load} />
        </div>
      </>
    );
  }

  return (
    <Shell
      route={route}
      version={version}
      onLogout={onLogout}
      status={status}
      footer={
        <>
          <Hotkey k="t">{t("terminal")}</Hotkey>
          <Hotkey k="p">{t("processes")}</Hotkey>
          <Hotkey k="l">{t("logs")}</Hotkey>
          <Hotkey k="e">{t("passport")}</Hotkey>
          <Hotkey k="+/-">{t("chart window")}</Hotkey>
          <Hotkey k="esc">{t("back")}</Hotkey>
        </>
      }
      footerNote={t("live · detailed metrics are sent while this screen is open")}
    >
      {body}
    </Shell>
  );
}
