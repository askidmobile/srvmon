import * as React from "react";
import { useState } from "react";

import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { api, errorMessage, type CheckState, type ChecksView, type DomainInfo } from "@/lib/api";
import { locale, t, tn } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { fmtDuration } from "@/pages/Alerts";

// Whole days until an instant (negative once it passed), as the server counts.
export function daysLeft(iso: string): number {
  return Math.floor((new Date(iso).getTime() - Date.now()) / 86400000);
}

function fmtDay(iso: string): string {
  return new Date(iso).toLocaleDateString(locale(), { day: "2-digit", month: "2-digit", year: "numeric" });
}

function ago(iso?: string): string {
  if (!iso) return "─";
  const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
  if (s < 60) return t("{n}s ago", { n: s });
  if (s < 3600) return t("{n}m ago", { n: Math.floor(s / 60) });
  return t("{n}h ago", { n: Math.floor(s / 3600) });
}

// "in 12 days" / "3 days ago", coloured by how soon.
export function Expiry({ iso, warn }: { iso: string; warn: number }) {
  const d = daysLeft(iso);
  return (
    <span className={cn(d < 0 ? "text-crit" : d <= warn ? "text-warn" : "text-muted-foreground")}>
      {fmtDay(iso)} · {d >= 0 ? tn(d, "in {n} day", "in {n} days") : tn(-d, "{n} day ago", "{n} days ago")}
    </span>
  );
}

function CheckRow({ c }: { c: CheckState }) {
  const r = c.result;
  const state = !r ? (
    <span className="text-muted-foreground">…</span>
  ) : r.up ? (
    <span className="text-primary">●</span>
  ) : (
    <span className="text-crit">○</span>
  );
  let status: React.ReactNode = <span className="text-muted-foreground">{t("waiting for the first check")}</span>;
  if (r?.up) status = r.status ? `HTTP ${r.status}` : t("port open");
  else if (r)
    status = (
      <span className="text-crit">
        {r.error || t("down")}
        {r.down_since && (
          <span className="text-muted-foreground">
            {" "}
            · {t("for {time}", { time: fmtDuration(Math.round((Date.now() - new Date(r.down_since).getTime()) / 1000)) })}
          </span>
        )}
      </span>
    );
  return (
    <tr className="hover:bg-secondary/60">
      <td className="px-1.5">{state}</td>
      <td className="max-w-[420px] truncate px-1.5 text-hi" title={c.target}>
        {c.kind === "http" ? (
          <a href={c.target} target="_blank" rel="noreferrer noopener" className="hover:text-primary">
            {c.target}
          </a>
        ) : (
          c.target
        )}
      </td>
      <td className="px-1.5">{status}</td>
      <td className="px-1.5 text-right whitespace-nowrap">{r?.up ? `${r.latency_ms} ms` : "─"}</td>
      <td className="px-1.5 whitespace-nowrap" title={r?.cert ? `${r.cert.subject} · ${r.cert.issuer}` : undefined}>
        {r?.cert ? <Expiry iso={r.cert.not_after} warn={14} /> : <span className="text-muted-foreground">─</span>}
      </td>
      <td className="px-1.5 whitespace-nowrap text-muted-foreground">{ago(r?.checked_at)}</td>
    </tr>
  );
}

export function DomainLine({ d }: { d: DomainInfo }) {
  return (
    <span title={d.error}>
      <span className="text-hi">{d.domain}</span>{" "}
      {d.expires ? (
        <Expiry iso={d.expires} warn={30} />
      ) : (
        <span className="text-muted-foreground">{d.error ? t("expiry unknown: {err}", { err: d.error }) : t("expiry unknown")}</span>
      )}
    </span>
  );
}

function ChecksForm({
  serverId,
  view,
  onDone,
}: {
  serverId: number;
  view: ChecksView;
  onDone: (v: ChecksView | null) => void;
}) {
  const [text, setText] = useState(() => view.checks.map((c) => c.target).join("\n"));
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      onDone(await api.saveChecks(serverId, text.split("\n")));
    } catch (err) {
      setError(errorMessage(err, t("could not save")));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form onSubmit={save} className="flex flex-col gap-2">
      <label htmlFor="checks-targets" className="text-muted-foreground">
        {t("One target per line: https://site/path (status and certificate), tls://host:port (certificate), host:port (TCP port).")}
      </label>
      <textarea
        id="checks-targets"
        value={text}
        onChange={(e) => setText(e.target.value)}
        rows={Math.min(12, Math.max(4, text.split("\n").length + 1))}
        placeholder={"https://example.com/health\ntls://mail.example.com:993\ndb.example.com:5432"}
        spellCheck={false}
        className="min-w-0 border border-input bg-field px-2 py-1.5 text-hi placeholder:text-muted-foreground focus-visible:outline-1 focus-visible:outline-ring"
      />
      {error && <div className="text-crit">{error}</div>}
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={busy}>
          {t("save")}
        </Button>
        <Button type="button" size="sm" variant="outline" onClick={() => onDone(null)}>
          {t("cancel")}
        </Button>
      </div>
    </form>
  );
}

export function ChecksPanel({
  serverId,
  view,
  onChange,
}: {
  serverId: number;
  view: ChecksView | null;
  onChange: (v: ChecksView) => void;
}) {
  const [editing, setEditing] = useState(false);
  const list = view?.checks ?? [];
  const down = list.filter((c) => c.result && !c.result.up).length;
  const right = editing ? (
    t("editing")
  ) : (
    <button type="button" className="cursor-pointer hover:text-primary" onClick={() => setEditing(true)}>
      {list.length > 0 && (
        <span className={down > 0 ? "text-crit" : "text-primary"}>
          {down > 0 ? t("{n} down", { n: down }) : t("all up")}
        </span>
      )}
      {list.length > 0 && " · "}✎ {t("edit")}
    </button>
  );
  return (
    <Panel title={t("checks")} right={right}>
      {editing && view ? (
        <ChecksForm
          serverId={serverId}
          view={view}
          onDone={(v) => {
            setEditing(false);
            if (v) onChange(v);
          }}
        />
      ) : list.length === 0 ? (
        <div className="flex flex-wrap items-center gap-3 text-muted-foreground">
          <span>
            {t(
              "No checks. Add the server's sites and ports: the panel checks them every minute and alerts when they are down, when a certificate or a domain is about to expire.",
            )}
          </span>
          <Button size="sm" variant="outline" onClick={() => setEditing(true)} disabled={!view}>
            + {t("add")}
          </Button>
        </div>
      ) : (
        <div className="flex flex-col gap-3">
          <div className="overflow-auto">
            <table className="w-full min-w-[760px] border-collapse text-left">
              <thead className="text-muted-foreground">
                <tr className="border-b border-border">
                  {["st", "target", "status", "latency", "certificate", "checked"].map((h, i) => (
                    <th key={h} className={cn("px-1.5 py-1 font-normal", i === 3 && "text-right")}>
                      {t(h)}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {list.map((c) => (
                  <CheckRow key={c.id} c={c} />
                ))}
              </tbody>
            </table>
          </div>
          {view && view.domains.length > 0 && (
            <div className="flex flex-wrap gap-x-6 gap-y-1">
              <span className="text-muted-foreground">{t("domains")}:</span>
              {view.domains.map((d) => (
                <DomainLine key={d.domain} d={d} />
              ))}
            </div>
          )}
        </div>
      )}
    </Panel>
  );
}
