import { Fragment, type ReactNode, useCallback, useEffect, useState } from "react";

import { Hotkey, Shell } from "@/components/Shell";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  api,
  ApiError,
  errorMessage,
  type Alert,
  type AlertsView,
  type Channel,
  type HeartbeatStatus,
  type NotifyChannel,
  type NotifyView,
  type Quiet,
  type Rule,
  type Severity,
} from "@/lib/api";
import { t, tn } from "@/lib/i18n";
import { href, type Route } from "@/lib/router";
import { cn } from "@/lib/utils";

const fieldCls =
  "h-9 w-full min-w-0 border border-input bg-field px-2.5 text-hi placeholder:text-muted-foreground focus-visible:outline-1 focus-visible:outline-ring disabled:opacity-60";
const cellField =
  "h-6 min-w-0 border border-input bg-field px-1.5 text-xs text-hi focus-visible:outline-1 focus-visible:outline-ring";

// How each rule kind reads in the table and which fields it has.
// dur: "for" — the condition must hold this long; "window" — counting
// window; "silent" — how long the agent is quiet; null — no duration.
// name, label and unit are English source strings, shown through t().
const KINDS: Record<
  string,
  { name: string; label: (v: number) => string; unit?: string; dur: "for" | "window" | "silent" | null }
> = {
  agent_silent: { name: "agent silent", label: () => t("agent silent"), dur: "silent" },
  cpu: { name: "cpu >", label: (v) => `cpu > ${v}%`, unit: "%", dur: "for" },
  mem: { name: "mem >", label: (v) => `mem > ${v}%`, unit: "%", dur: "for" },
  disk: { name: "disk >", label: (v) => `disk > ${v}%`, unit: "%", dur: "for" },
  container_down: { name: "container down / unhealthy", label: () => t("container down / unhealthy"), dur: "for" },
  container_restarts: {
    name: "container restarts >",
    label: (v) => `${t("container restarts >")} ${v}`,
    unit: "times",
    dur: "window",
  },
  payment_due: {
    name: "server payment due in ≤",
    label: (v) => t("server payment due in ≤ {n} days", { n: v }),
    unit: "days",
    dur: null,
  },
  fail2ban: { name: "fail2ban ban", label: () => t("fail2ban ban on the monitoring server"), dur: null },
  bruteforce: { name: "panel password guessing", label: () => t("panel password guessing"), dur: null },
  login: { name: "panel sign-in", label: () => t("panel sign-in"), dur: null },
  backup: { name: "panel backup failed", label: () => t("panel backup failed"), dur: null },
};

const NEW_DEFAULTS: Record<string, Pick<Rule, "threshold" | "duration_sec">> = {
  agent_silent: { threshold: 0, duration_sec: 300 },
  cpu: { threshold: 95, duration_sec: 600 },
  mem: { threshold: 90, duration_sec: 300 },
  disk: { threshold: 95, duration_sec: 0 },
  container_down: { threshold: 0, duration_sec: 300 },
  container_restarts: { threshold: 5, duration_sec: 3600 },
  payment_due: { threshold: 7, duration_sec: 0 },
  fail2ban: { threshold: 0, duration_sec: 0 },
  bruteforce: { threshold: 0, duration_sec: 0 },
  login: { threshold: 0, duration_sec: 0 },
  backup: { threshold: 0, duration_sec: 0 },
};

const sevCls: Record<Severity, string> = { crit: "text-crit", warn: "text-warn", info: "text-tx" };

// 300 → "5m", 5400 → "1h30m" (units in the interface language). Display keeps two units; full keeps all
// (for the editor, so saving does not shorten the value).
export function fmtDuration(sec: number, full = false): string {
  if (sec <= 0) return "─";
  const parts: string[] = [];
  for (const [n, u] of [
    [86400, "{n}d"],
    [3600, "{n}h"],
    [60, "{n}m"],
    [1, "{n}s"],
  ] as const) {
    if (sec >= n) {
      parts.push(t(u, { n: Math.floor(sec / n) }));
      sec %= n;
    }
  }
  return (full ? parts : parts.slice(0, 2)).join("");
}

// "1h30m", "1ч30м", "90s", "5m", "10" (minutes) → seconds; null when unreadable.
// Accepts the units of every interface language.
export function parseDuration(s: string): number | null {
  const t = s.trim().toLowerCase().replace(/\s+/g, "");
  if (t === "" || t === "─" || t === "-" || t === "0") return 0;
  if (/^\d+$/.test(t)) return Number(t) * 60;
  const mult: Record<string, number> = {
    д: 86400, d: 86400, 天: 86400,
    ч: 3600, h: 3600, 小时: 3600, 时: 3600,
    м: 60, m: 60, 分: 60,
    с: 1, s: 1, 秒: 1,
  };
  let total = 0;
  let rest = t;
  const re = /^(\d+)(д|d|天|ч|h|小时|时|м|m|分|с|s|秒)/;
  while (rest) {
    const m = re.exec(rest);
    if (!m) return null;
    total += Number(m[1]) * mult[m[2]];
    rest = rest.slice(m[0].length);
  }
  return total;
}

function when(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  const p = (n: number) => String(n).padStart(2, "0");
  const today = new Date().toDateString() === d.toDateString();
  return `${today ? "" : `${p(d.getDate())}.${p(d.getMonth() + 1)} `}${p(d.getHours())}:${p(d.getMinutes())}`;
}

function durLabel(r: Pick<Rule, "kind" | "duration_sec">): string {
  const k = KINDS[r.kind];
  if (!k?.dur || r.duration_sec <= 0) return "─";
  return (k.dur === "silent" ? "> " : "") + fmtDuration(r.duration_sec);
}

// --- active alerts and history ---

// Alerts about a server state the AI agent can look at.
function diagnosable(a: Alert): boolean {
  return a.server_id > 0 && a.kind !== "payment_due";
}

const diagState: Record<string, string> = {
  queued: "queued",
  running: "the agent is looking…",
  error: "failed",
  skipped: "skipped",
};

function DiagnosisRow({ d, cols }: { d: NonNullable<Alert["diagnosis"]>; cols: number }) {
  const status = d.state === "done" ? "" : t(diagState[d.state] ?? d.state);
  return (
    <tr className="align-top">
      <td />
      <td colSpan={cols - 1} className="pb-2 pr-3">
        <div className="border-l-2 border-primary/60 pl-2.5 text-sm">
          <div className="text-xs text-muted-foreground">
            🩺 {t("AI diagnosis")}
            {d.trigger === "manual" ? ` · ${t("manual")}` : ""}
            {d.tools > 0 ? ` · ${tn(d.tools, "{n} command", "{n} commands")}` : ""}
            {status && <span className={d.state === "error" ? "text-crit" : "text-warn"}> · {status}</span>}
          </div>
          {d.text && <div className="whitespace-pre-wrap break-words text-tx">{d.text}</div>}
        </div>
      </td>
    </tr>
  );
}

function AlertRow({ a, onChange }: { a: Alert; onChange: () => void }) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const act = async (f: () => Promise<unknown>, fallback = t("failed")) => {
    setBusy(true);
    setErr("");
    await f().catch((e) => setErr(errorMessage(e, fallback)));
    setBusy(false);
    onChange();
  };
  const running = a.diagnosis?.state === "queued" || a.diagnosis?.state === "running";
  return (
    <>
      <tr className="border-t border-border/60 align-top">
        <td className="py-1.5 pr-3 whitespace-nowrap text-muted-foreground">{when(a.started_at)}</td>
        <td className={cn("pr-3 uppercase", sevCls[a.severity])}>{a.severity}</td>
        <td className="pr-3 whitespace-nowrap">
          {a.server_id ? (
            <a className="text-hi hover:text-primary" href={href.server(a.server_id)}>
              {a.server_name}
            </a>
          ) : (
            <span className="text-hi">{a.server_name || t("panel")}</span>
          )}
        </td>
        <td className="pr-3">
          {a.message}
          {a.channel === "tg" && <span className="text-muted-foreground">{a.notified ? ` → ${t("sent")}` : ""}</span>}
        </td>
        <td className="pr-3 whitespace-nowrap text-muted-foreground">
          {a.state === "acked" ? <span className="text-primary">✓ ack</span> : <span className={sevCls[a.severity]}>●</span>}
        </td>
        <td className="whitespace-nowrap text-right">
          {a.state === "firing" && (
            <Button size="sm" variant="outline" disabled={busy} onClick={() => act(() => api.ackAlert(a.id))}>
              ack
            </Button>
          )}{" "}
          {diagnosable(a) && (
            <Button
              size="sm"
              variant="ghost"
              disabled={busy || running}
              title={t("the AI agent collects logs read-only and explains the likely cause")}
              onClick={() => act(() => api.diagnoseAlert(a.id), t("could not start the diagnosis"))}
            >
              {t("diagnose")}
            </Button>
          )}{" "}
          <Button
            size="sm"
            variant="ghost"
            disabled={busy}
            title={t("no notifications for this alert for an hour")}
            onClick={() => act(() => api.silenceAlert(a.id, 60))}
          >
            {t("mute 1h")}
          </Button>
          {err && <div className="pt-1 text-xs text-crit">{err}</div>}
        </td>
      </tr>
      {a.diagnosis && <DiagnosisRow d={a.diagnosis} cols={6} />}
    </>
  );
}

function stateText(a: Alert): ReactNode {
  switch (a.state) {
    case "event":
      return <span className="text-muted-foreground">{t("event")}</span>;
    case "resolved": {
      const d = a.resolved_at ? (Date.parse(a.resolved_at) - Date.parse(a.started_at)) / 1000 : 0;
      return <span className="text-primary">resolved {d > 0 ? fmtDuration(Math.round(d)) : ""}</span>;
    }
    case "acked":
      return <span className="text-primary">✓ ack</span>;
  }
  return <span className={sevCls[a.severity]}>firing</span>;
}

// --- rules ---

function RuleEditor({
  rule,
  isNew,
  onDone,
}: {
  rule: Rule;
  isNew: boolean;
  onDone: (changed: boolean) => void;
}) {
  const [r, setR] = useState(rule);
  const [thr, setThr] = useState(String(rule.threshold));
  const [dur, setDur] = useState(rule.duration_sec ? fmtDuration(rule.duration_sec, true) : "");
  const [err, setErr] = useState("");
  const k = KINDS[r.kind];

  const changeKind = (kind: string) => {
    const d = NEW_DEFAULTS[kind];
    setR({ ...r, kind, ...d });
    setThr(String(d.threshold));
    setDur(d.duration_sec ? fmtDuration(d.duration_sec, true) : "");
  };

  const save = async () => {
    const duration = k.dur ? parseDuration(dur) : 0;
    if (duration === null) {
      setErr(t("duration: for example 5m, 1h, 30s"));
      return;
    }
    const threshold = k.unit ? Number(thr.replace(",", ".")) : 0;
    if (!Number.isFinite(threshold)) {
      setErr(t("the threshold must be a number"));
      return;
    }
    const next = { ...r, threshold, duration_sec: duration };
    try {
      if (isNew) await api.createRule(next);
      else await api.updateRule(next);
      onDone(true);
    } catch (e) {
      setErr(errorMessage(e, t("not saved")));
    }
  };

  return (
    <>
      <tr className="border-t border-border/60 bg-secondary/40">
        <td className="py-1.5 pr-2">
          <button type="button" className={cn("whitespace-pre", r.enabled ? "text-primary" : "text-muted-foreground")} onClick={() => setR({ ...r, enabled: !r.enabled })}>
            {r.enabled ? "[x]" : "[ ]"}
          </button>
        </td>
        <td className="pr-2">
          <div className="flex flex-wrap items-center gap-1.5">
            {isNew ? (
              <select className={cellField} value={r.kind} onChange={(e) => changeKind(e.target.value)} aria-label={t("type")}>
                {Object.entries(KINDS).map(([kind, m]) => (
                  <option key={kind} value={kind}>
                    {t(m.name)}
                  </option>
                ))}
              </select>
            ) : (
              <span>{t(k.name)}</span>
            )}
            {k.unit && (
              <>
                <input
                  className={cn(cellField, "w-16")}
                  inputMode="decimal"
                  value={thr}
                  onChange={(e) => setThr(e.target.value)}
                  aria-label={t("threshold")}
                />
                <span className="text-xs text-muted-foreground">{t(k.unit)}</span>
              </>
            )}
          </div>
        </td>
        <td className="pr-2">
          {k.dur ? (
            <input
              className={cn(cellField, "w-20")}
              value={dur}
              placeholder={k.dur === "for" ? t("at once") : t("{n}m", { n: 5 })}
              onChange={(e) => setDur(e.target.value)}
              aria-label={t("duration")}
            />
          ) : (
            "─"
          )}
        </td>
        <td className="pr-2">
          <select
            className={cn(cellField, sevCls[r.severity])}
            value={r.severity}
            onChange={(e) => setR({ ...r, severity: e.target.value as Severity })}
            aria-label={t("level")}
          >
            <option value="crit">crit</option>
            <option value="warn">warn</option>
            <option value="info">info</option>
          </select>
        </td>
        <td className="pr-2">
          <select
            className={cellField}
            value={r.channel}
            onChange={(e) => setR({ ...r, channel: e.target.value as Channel })}
            aria-label={t("channel")}
          >
            <option value="tg">notify</option>
            <option value="web">web</option>
          </select>
        </td>
        <td className="whitespace-nowrap text-right">
          <Button size="sm" onClick={save}>
            {t("ok")}
          </Button>{" "}
          <Button size="sm" variant="ghost" onClick={() => onDone(false)}>
            {t("cancel")}
          </Button>
        </td>
      </tr>
      {err && (
        <tr>
          <td />
          <td colSpan={5} className="pb-1.5 text-xs text-crit">
            {err}
          </td>
        </tr>
      )}
    </>
  );
}

function RulesPanel() {
  const [rules, setRules] = useState<Rule[] | null>(null);
  const [editing, setEditing] = useState<number | "new" | null>(null);
  const [err, setErr] = useState("");

  const load = useCallback(() => {
    api
      .rules()
      .then(setRules)
      .catch(() => setErr(t("could not load the rules")));
  }, []);
  useEffect(load, [load]);

  const toggle = async (r: Rule) => {
    try {
      await api.updateRule({ ...r, enabled: !r.enabled });
      load();
    } catch (e) {
      setErr(errorMessage(e, t("not saved")));
    }
  };
  const remove = async (r: Rule) => {
    if (!confirm(t("Delete the rule “{rule}”?", { rule: KINDS[r.kind]?.label(r.threshold) ?? r.kind }))) return;
    await api.deleteRule(r.id).catch(() => setErr(t("could not delete")));
    load();
  };
  const done = (changed: boolean) => {
    setEditing(null);
    if (changed) load();
  };

  return (
    <Panel title={t("alert rules")} right={t("checked every 15s")}>
      <ScrollArea className="w-full">
        <table className="w-full min-w-[560px] border-collapse text-left">
          <thead className="text-xs text-muted-foreground">
            <tr>
              <th className="pb-2 font-normal">{t("on")}</th>
              <th className="pb-2 font-normal">{t("condition")}</th>
              <th className="pb-2 font-normal">{t("dur.")}</th>
              <th className="pb-2 font-normal">{t("level")}</th>
              <th className="pb-2 font-normal">{t("channel")}</th>
              <th className="pb-2 font-normal" />
            </tr>
          </thead>
          <tbody>
            {(rules ?? []).map((r) =>
              editing === r.id ? (
                <RuleEditor key={r.id} rule={r} isNew={false} onDone={done} />
              ) : (
                <tr key={r.id} className="border-t border-border/60">
                  <td className="py-1.5 pr-2">
                    <button
                      type="button"
                      className={cn("whitespace-pre", r.enabled ? "text-primary" : "text-muted-foreground")}
                      title={r.enabled ? t("turn off") : t("turn on")}
                      onClick={() => toggle(r)}
                    >
                      {r.enabled ? "[x]" : "[ ]"}
                    </button>
                  </td>
                  <td className={cn("pr-2", !r.enabled && "text-muted-foreground")}>
                    {KINDS[r.kind]?.label(r.threshold) ?? r.kind}
                  </td>
                  <td className="pr-2 text-muted-foreground">{durLabel(r)}</td>
                  <td className={cn("pr-2", sevCls[r.severity])}>{r.severity}</td>
                  <td className="pr-2 text-muted-foreground">{r.channel === "tg" ? "notify" : r.channel}</td>
                  <td className="whitespace-nowrap text-right">
                    <Button size="sm" variant="ghost" onClick={() => setEditing(r.id)}>
                      {t("edit")}
                    </Button>
                    <Button size="sm" variant="ghost" className="hover:text-crit" title={t("delete")} onClick={() => remove(r)}>
                      ×
                    </Button>
                  </td>
                </tr>
              ),
            )}
            {editing === "new" && (
              <RuleEditor
                rule={{ id: 0, kind: "cpu", enabled: true, severity: "warn", channel: "tg", ...NEW_DEFAULTS.cpu }}
                isNew
                onDone={done}
              />
            )}
          </tbody>
        </table>
      </ScrollArea>
      {err && <div className="mt-2 text-sm text-crit">{err}</div>}
      <div className="mt-3 flex flex-wrap items-center gap-3">
        <Button variant="outline" disabled={editing !== null} onClick={() => setEditing("new")}>
          + {t("rule")}
        </Button>
        <QuietHours />
      </div>
      <p className="mt-2 text-xs text-muted-foreground">
        {t("notify: in the panel, in Telegram and in the notification channels; web: only in the panel. Containers stopped with exit code 0 do not count as alerts.")}
      </p>
    </Panel>
  );
}

function QuietHours() {
  const [q, setQ] = useState<Quiet | null>(null);
  const [saved, setSaved] = useState("");
  useEffect(() => {
    api
      .notify()
      .then((n) => setQ(n.quiet))
      .catch(() => undefined);
  }, []);
  if (!q) return null;
  const save = async () => {
    try {
      setQ(await api.saveQuiet(q));
      setSaved(t("saved"));
    } catch (e) {
      setSaved(errorMessage(e, t("error")));
    }
    setTimeout(() => setSaved(""), 3000);
  };
  return (
    <div className="flex flex-wrap items-center gap-1.5 text-sm text-muted-foreground">
      <span>{t("quiet hours")}</span>
      <input type="time" className={cn(cellField, "h-7")} value={q.from} onChange={(e) => setQ({ ...q, from: e.target.value })} aria-label={t("from")} />
      <span>–</span>
      <input type="time" className={cn(cellField, "h-7")} value={q.to} onChange={(e) => setQ({ ...q, to: e.target.value })} aria-label={t("to")} />
      <input
        className={cn(cellField, "h-7 w-36")}
        value={q.tz}
        placeholder="Europe/Moscow"
        onChange={(e) => setQ({ ...q, tz: e.target.value })}
        aria-label={t("time zone")}
      />
      <span>{t("except crit")}</span>
      <Button size="sm" variant="outline" onClick={save}>
        {t("save")}
      </Button>
      {saved && <span className="text-xs">{saved}</span>}
    </div>
  );
}

// --- Telegram ---

function TelegramPanel() {
  const [n, setN] = useState<NotifyView | null>(null);
  const [token, setToken] = useState("");
  const [chat, setChat] = useState("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(
    (resetChat: boolean) =>
      api
        .notify()
        .then((v) => {
          setN(v);
          if (resetChat) setChat(v.telegram.chat_id);
        })
        .catch(() => undefined),
    [],
  );
  useEffect(() => {
    load(true);
    const t = setInterval(() => load(false), 5000);
    return () => clearInterval(t);
  }, [load]);

  const run = async (f: () => Promise<unknown>, ok: string) => {
    setBusy(true);
    setMsg(null);
    try {
      await f();
      setMsg({ ok: true, text: ok });
      setToken("");
      await load(true);
    } catch (e) {
      setMsg({ ok: false, text: e instanceof ApiError && e.code === "no_master_key" ? t("no master key on the server") : errorMessage(e, t("error")) });
    }
    setBusy(false);
  };

  if (!n) return <Panel title={t("telegram bot")}>{t("loading…")}</Panel>;
  const tg = n.telegram;
  const status = !tg.configured ? (
    <span className="text-muted-foreground">○ {t("not set up")}</span>
  ) : tg.connected ? (
    <span className="text-primary">● {t("connected")}
      {tg.bot_name ? ` · @${tg.bot_name}` : ""}</span>
  ) : tg.last_error ? (
    <span className="text-crit">✕ {tg.last_error}</span>
  ) : (
    <span className="text-warn">… {t("connecting")}</span>
  );

  return (
    <Panel title={t("telegram bot")}>
      <div className="flex flex-col gap-2.5">
        <div className="flex flex-col gap-1">
          <label className="text-sm text-muted-foreground" htmlFor="tg-token">
            bot token
          </label>
          <input
            id="tg-token"
            type="password"
            autoComplete="off"
            className={fieldCls}
            value={token}
            placeholder={tg.configured ? t("saved · leave empty to keep it") : "123456789:AA…"}
            onChange={(e) => setToken(e.target.value)}
          />
        </div>
        <div className="flex flex-col gap-1">
          <label className="text-sm text-muted-foreground" htmlFor="tg-chat">
            {t("chat id (the bot replies to this chat only)")}
          </label>
          <input
            id="tg-chat"
            className={fieldCls}
            inputMode="numeric"
            value={chat}
            placeholder={t("send /start to the bot, the id shows up below")}
            onChange={(e) => setChat(e.target.value)}
          />
        </div>
        {tg.pending_chat && (
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="text-muted-foreground">{t("wrote to the bot:")}</span>
            <span className="text-hi">
              {tg.pending_chat.title || t("chat")} · {tg.pending_chat.id}
            </span>
            <Button
              size="sm"
              variant="outline"
              disabled={busy}
              onClick={() => run(() => api.saveTelegram(tg.pending_chat!.id), t("chat saved"))}
            >
              {t("use this chat")}
            </Button>
          </div>
        )}
        <div className="text-sm">
          <span className="text-muted-foreground">{t("status")}</span> {status}
          {tg.last_message && <span className="text-muted-foreground"> · {t("last command")} {when(tg.last_message)}</span>}
          {tg.last_sent && <span className="text-muted-foreground"> · {t("sent")} {when(tg.last_sent)}</span>}
        </div>
        <div className="text-sm text-muted-foreground">
          {t("commands:")} <span className="text-hi">/status</span> · <span className="text-hi">/server &lt;{t("name")}&gt;</span> ·{" "}
          <span className="text-hi">/alerts</span> · <span className="text-hi">/ack &lt;id&gt;</span> ·{" "}
          <span className="text-hi">/bills</span> · <span className="text-hi">/ask &lt;{t("question")}&gt;</span>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button
            disabled={busy}
            onClick={() => run(() => api.saveTelegram(chat.trim(), token.trim() || undefined), t("saved"))}
          >
            {t("save")}
          </Button>
          <Button variant="outline" disabled={busy || !tg.configured || !tg.chat_id} onClick={() => run(api.testTelegram, t("test sent"))}>
            {t("send a test")}
          </Button>
          {tg.configured && (
            <Button
              variant="ghost"
              className="hover:text-crit"
              disabled={busy}
              onClick={() =>
                confirm(t("Disconnect the bot? The token will be deleted.")) &&
                run(() => api.saveTelegram(chat.trim(), ""), t("bot disconnected"))
              }
            >
              {t("disconnect")}
            </Button>
          )}
        </div>
        {msg && <div className={cn("text-sm", msg.ok ? "text-primary" : "text-crit")}>{msg.text}</div>}
        {!tg.configured && (
          <ol className="list-decimal space-y-0.5 pl-5 text-xs text-muted-foreground">
            <li>
              {t("In Telegram open")} <span className="text-hi">@BotFather</span> → <span className="text-hi">/newbot</span>,{" "}
              {t("set a name and a username (ending in “bot”).")}
            </li>
            <li>{t("Copy the token into the field above and press “save”.")}</li>
            <li>
              {t("Send your bot")} <span className="text-hi">/start</span> {t("and press “use this chat”.")}
            </li>
          </ol>
        )}
      </div>
    </Panel>
  );
}

// --- dead man's switch ---

function HeartbeatPanel() {
  const [hb, setHb] = useState<HeartbeatStatus | null | undefined>(undefined);
  const [url, setUrl] = useState("");
  const [fail, setFail] = useState(true);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(
    (resetFail: boolean) =>
      api
        .notify()
        .then((v) => {
          setHb(v.heartbeat ?? null);
          if (resetFail && v.heartbeat) setFail(v.heartbeat.fail_endpoint);
        })
        .catch(() => undefined),
    [],
  );
  useEffect(() => {
    load(true);
    const t = setInterval(() => load(false), 10000);
    return () => clearInterval(t);
  }, [load]);

  const run = async (f: () => Promise<unknown>, ok: string) => {
    setBusy(true);
    setMsg(null);
    try {
      await f();
      setMsg({ ok: true, text: ok });
      setUrl("");
      await load(true);
    } catch (e) {
      setMsg({ ok: false, text: e instanceof ApiError && e.code === "no_master_key" ? t("no master key on the server") : errorMessage(e, t("error")) });
    }
    setBusy(false);
  };

  const title = t("panel heartbeat");
  if (hb === undefined) return <Panel title={title}>{t("loading…")}</Panel>;
  if (hb === null) return <Panel title={title}>{t("unavailable")}</Panel>;

  const status = !hb.configured ? (
    <span className="text-muted-foreground">○ {t("not set up")}</span>
  ) : hb.last_result === "error" ? (
    <span className="text-crit">✕ {hb.last_error}</span>
  ) : hb.last_result === "fail" ? (
    <span className="text-crit">✕ {t("problems reported")}</span>
  ) : hb.last_result === "skipped" ? (
    <span className="text-warn">… {t("ping skipped: problems")}</span>
  ) : hb.last_result === "ok" ? (
    <span className="text-primary">● {t("ok")}</span>
  ) : (
    <span className="text-warn">… {t("waiting for the first ping")}</span>
  );

  return (
    <Panel title={title} right={t("every 1 min")}>
      <div className="flex flex-col gap-2.5">
        <div className="text-sm text-muted-foreground">
          {t("If the panel goes down, the pings stop and the external service alerts you by email or its own channels.")}
        </div>
        <div className="flex flex-col gap-1">
          <label className="text-sm text-muted-foreground" htmlFor="hb-url">
            {t("ping URL")}
          </label>
          <input
            id="hb-url"
            type="password"
            autoComplete="off"
            className={fieldCls}
            value={url}
            placeholder={hb.configured ? `${hb.target} · ${t("leave empty to keep it")}` : "https://hc-ping.com/<uuid>"}
            onChange={(e) => setUrl(e.target.value)}
          />
        </div>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={fail} onChange={(e) => setFail(e.target.checked)} />
          <span>
            {t("report problems to")} <span className="text-hi">URL/fail</span>{" "}
            <span className="text-muted-foreground">{t("(healthchecks.io; off: skip the ping instead)")}</span>
          </span>
        </label>
        <div className="text-sm">
          <span className="text-muted-foreground">{t("status")}</span> {status}
          {hb.last_ping && <span className="text-muted-foreground"> · {t("last ping")} {when(hb.last_ping)}</span>}
        </div>
        {hb.problems && hb.problems.length > 0 && (
          <ul className="text-sm text-crit">
            {hb.problems.map((p) => (
              <li key={p}>✕ {p}</li>
            ))}
          </ul>
        )}
        <div className="flex flex-wrap gap-2">
          <Button disabled={busy} onClick={() => run(() => api.saveHeartbeat(fail, url.trim() || undefined), t("saved"))}>
            {t("save")}
          </Button>
          <Button variant="outline" disabled={busy || !hb.configured} onClick={() => run(api.testHeartbeat, t("ping sent"))}>
            {t("ping now")}
          </Button>
          {hb.configured && (
            <Button
              variant="ghost"
              className="hover:text-crit"
              disabled={busy}
              onClick={() =>
                confirm(t("Turn the heartbeat off? The external check will report the panel as down.")) &&
                run(() => api.saveHeartbeat(fail, ""), t("heartbeat off"))
              }
            >
              {t("turn off")}
            </Button>
          )}
        </div>
        {msg && <div className={cn("text-sm", msg.ok ? "text-primary" : "text-crit")}>{msg.text}</div>}
        {!hb.configured && (
          <ol className="list-decimal space-y-0.5 pl-5 text-xs text-muted-foreground">
            <li>
              {t("On")} <span className="text-hi">healthchecks.io</span> {t("add a check: period 1 minute, grace 5 minutes.")}
            </li>
            <li>{t("Copy its ping URL into the field above and press “save”.")}</li>
            <li>{t("In the check's integrations pick where to be alerted: email, Telegram and others.")}</li>
          </ol>
        )}
      </div>
    </Panel>
  );
}

// --- Shoutrrr channels ---

// URL patterns shown as hints; the full list is in the Shoutrrr docs.
const URL_EXAMPLES: [string, string][] = [
  ["email", "smtp://user:password@smtp.example.com:587/?from=srvmon@example.com&to=me@example.com"],
  ["ntfy", "ntfy://ntfy.sh/my-secret-topic"],
  ["Discord", "discord://token@webhook-id"],
  ["Gotify", "gotify://gotify.example.com/app-token"],
  ["Slack", "slack://hook:T00000000-B00000000-XXXXXXXXXXXXXXXXXXXXXXXX@webhook"],
  ["Matrix", "matrix://user:password@matrix.example.com/?rooms=!room:example.com"],
  ["webhook", "generic+https://hooks.example.com/srvmon"],
];

const SEVERITIES: Severity[] = ["crit", "warn", "info"];

function ChannelRow({ c, busy, run }: { c: NotifyChannel; busy: boolean; run: (f: () => Promise<unknown>, ok: string) => void }) {
  const input = { name: c.name, min_severity: c.min_severity, enabled: c.enabled };
  return (
    <tr className="border-t border-border/60 align-top">
      <td className="py-1 pr-3">
<button
          type="button"
          className={cn("whitespace-pre", c.enabled ? "text-primary" : "text-muted-foreground")}
          title={c.enabled ? t("turn off") : t("turn on")}
          disabled={busy}
          onClick={() => run(() => api.updateChannel(c.id, { ...input, enabled: !c.enabled }), c.enabled ? t("channel off") : t("channel on"))}
        >
          {c.enabled ? "[x]" : "[ ]"}
        </button>{" "}
        <span className={c.enabled ? "text-hi" : "text-muted-foreground"}>{c.name}</span>
      </td>
      <td className="pr-3 whitespace-nowrap text-muted-foreground">
        {c.service}
        {c.target && ` · ${c.target}`}
      </td>
      <td className="pr-3">
        <select
          className={cellField}
          value={c.min_severity}
          disabled={busy}
          aria-label={t("minimum severity")}
          onChange={(e) => run(() => api.updateChannel(c.id, { ...input, min_severity: e.target.value as Severity }), t("saved"))}
        >
          {SEVERITIES.map((s) => (
            <option key={s} value={s}>
              {s === "info" ? s : `${s}+`}
            </option>
          ))}
        </select>
      </td>
      <td className="pr-3 text-sm">
        {c.last_error ? (
          <span className="text-crit">✕ {c.last_error}</span>
        ) : c.last_sent ? (
          <span className="text-primary">✓ {when(c.last_sent)}</span>
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </td>
      <td className="whitespace-nowrap text-right">
        <Button size="sm" variant="outline" disabled={busy} onClick={() => run(() => api.testChannel(c.id), t("test sent"))}>
          {t("test")}
        </Button>{" "}
        <Button
          size="sm"
          variant="ghost"
          className="hover:text-crit"
          disabled={busy}
          onClick={() => confirm(t("Delete the channel “{name}”?", { name: c.name })) && run(() => api.deleteChannel(c.id), t("channel deleted"))}
        >
          ✕
        </Button>
      </td>
    </tr>
  );
}

function ChannelsPanel() {
  const [n, setN] = useState<NotifyView | null>(null);
  const [name, setName] = useState("");
  const [url, setURL] = useState("");
  const [sev, setSev] = useState<Severity>("warn");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [help, setHelp] = useState(false);

  const load = useCallback(
    () =>
      api
        .notify()
        .then(setN)
        .catch(() => undefined),
    [],
  );
  useEffect(() => {
    load();
    const t = setInterval(load, 10000);
    return () => clearInterval(t);
  }, [load]);

  const run = async (f: () => Promise<unknown>, ok: string) => {
    setBusy(true);
    setMsg(null);
    try {
      await f();
      setMsg({ ok: true, text: ok });
      await load();
      return true;
    } catch (e) {
      setMsg({ ok: false, text: e instanceof ApiError && e.code === "no_master_key" ? t("no master key on the server") : errorMessage(e, t("error")) });
      await load();
      return false;
    } finally {
      setBusy(false);
    }
  };

  const add = async () => {
    if (await run(() => api.createChannel({ name: name.trim(), url: url.trim(), min_severity: sev, enabled: true }), t("channel added"))) {
      setName("");
      setURL("");
    }
  };

  const channels = n?.channels ?? [];
  return (
    <Panel title={t("notification channels")} right={t("email, ntfy, Discord, Gotify, webhooks… via Shoutrrr")}>
      {!n ? (
        t("loading…")
      ) : (
        <div className="flex flex-col gap-3">
          {channels.length > 0 && (
            <ScrollArea className="w-full">
              <table className="w-full min-w-[640px] border-collapse text-left">
                <tbody>
                  {channels.map((c) => (
                    <ChannelRow key={c.id} c={c} busy={busy} run={run} />
                  ))}
                </tbody>
              </table>
            </ScrollArea>
          )}
          {!n.channels_ok ? (
            <div className="text-sm text-crit">{t("no master key on the server")}</div>
          ) : (
            <div className="grid grid-cols-1 gap-2 md:grid-cols-[minmax(0,12rem)_minmax(0,1fr)_auto_auto]">
              <input
                className={fieldCls}
                value={name}
                placeholder={t("name, e.g. mail")}
                aria-label={t("name")}
                maxLength={60}
                onChange={(e) => setName(e.target.value)}
              />
              <input
                className={fieldCls}
                value={url}
                type="password"
                autoComplete="off"
                spellCheck={false}
                placeholder="ntfy://ntfy.sh/my-topic"
                aria-label="URL"
                onChange={(e) => setURL(e.target.value)}
              />
              <select className={fieldCls} value={sev} aria-label={t("minimum severity")} onChange={(e) => setSev(e.target.value as Severity)}>
                {SEVERITIES.map((s) => (
                  <option key={s} value={s}>
                    {s === "info" ? t("everything") : `${s}+`}
                  </option>
                ))}
              </select>
              <Button disabled={busy || !name.trim() || !url.trim()} onClick={add}>
                + {t("channel")}
              </Button>
            </div>
          )}
          {msg && <div className={cn("text-sm", msg.ok ? "text-primary" : "text-crit")}>{msg.text}</div>}
          <p className="text-xs text-muted-foreground">
            {t("Every alert with the “notify” channel goes to Telegram and to each enabled channel whose minimum severity it reaches. The URL holds the credentials: it is stored encrypted and is not shown again.")}{" "}
            <button type="button" className="text-hi underline-offset-2 hover:underline" onClick={() => setHelp(!help)}>
              {help ? t("hide examples") : t("URL examples")}
            </button>
          </p>
          {help && (
            <div className="flex flex-col gap-1 text-xs">
              {URL_EXAMPLES.map(([label, u]) => (
                <div key={label} className="flex flex-wrap gap-x-2">
                  <span className="w-16 shrink-0 text-muted-foreground">{label}</span>
                  <code className="break-all text-hi">{u}</code>
                </div>
              ))}
              <div className="mt-1 text-muted-foreground">
                {t("All services and their parameters:")}{" "}
                <a className="text-hi underline" href="https://shoutrrr.nickfedor.com/" target="_blank" rel="noreferrer noopener">
                  shoutrrr.nickfedor.com
                </a>{" "}
                · {n.services.join(", ")}
              </div>
            </div>
          )}
        </div>
      )}
    </Panel>
  );
}

// --- page ---

export function AlertsPage({ route, version, onLogout }: { route: Route; version: string; onLogout: () => void }) {
  const [view, setView] = useState<AlertsView | null>(null);
  const [error, setError] = useState("");
  const [allHistory, setAllHistory] = useState(false);

  const load = useCallback(() => {
    api
      .alerts()
      .then((v) => {
        setView(v);
        setError("");
      })
      .catch((e) => {
        if (e?.status === 401) onLogout();
        else setError(t("no connection to the server"));
      });
  }, [onLogout]);
  useEffect(() => {
    load();
    const t = setInterval(load, 10000);
    return () => clearInterval(t);
  }, [load]);

  const history = (view?.recent ?? []).slice(0, allHistory ? undefined : 30);

  return (
    <Shell
      route={route}
      version={version}
      onLogout={onLogout}
      status={error && <span className="text-crit">{error}</span>}
      footer={
        <>
          <Hotkey k="1">{t("overview")}</Hotkey>
          <Hotkey k="5">{t("alerts")}</Hotkey>
        </>
      }
      footerNote={t("refresh 10s")}
    >
      <Panel
        title={t("active alerts")}
        right={view ? t("{open} active · {day} in 24h", { open: view.open.length, day: view.last24h }) : undefined}
        accent={!!view?.open.some((a) => a.severity === "crit" && a.state === "firing")}
      >
        {view && view.open.length === 0 ? (
          <div className="py-2 text-primary">✓ {t("all quiet")}</div>
        ) : (
          <ScrollArea className="w-full">
            <table className="w-full min-w-[720px] border-collapse text-left">
              <tbody>
                {(view?.open ?? []).map((a) => (
                  <AlertRow key={a.id} a={a} onChange={load} />
                ))}
              </tbody>
            </table>
          </ScrollArea>
        )}
      </Panel>

      <div className="grid grid-cols-1 gap-x-6 gap-y-7 xl:grid-cols-2">
        <RulesPanel />
        <div className="flex flex-col gap-y-7">
          <TelegramPanel />
          <HeartbeatPanel />
        </div>
      </div>

      <ChannelsPanel />

      <Panel title={t("history")} right={t("events and alerts for 90 days")}>
        {history.length === 0 ? (
          <div className="py-2 text-muted-foreground">{t("empty so far")}</div>
        ) : (
          <ScrollArea className="w-full">
            <table className="w-full min-w-[720px] border-collapse text-left">
              <tbody>
                {history.map((a) => (
                  <Fragment key={a.id}>
                    <tr className="border-t border-border/60 align-top">
                      <td className="py-1 pr-3 whitespace-nowrap text-muted-foreground">{when(a.started_at)}</td>
                      <td className={cn("pr-3 uppercase", sevCls[a.severity])}>{a.severity}</td>
                      <td className="pr-3 whitespace-nowrap text-hi">{a.server_name || t("panel")}</td>
                      <td className="pr-3">
                        {a.message}
                        {a.notified && <span className="text-muted-foreground"> → {t("sent")}</span>}
                      </td>
                      <td className="whitespace-nowrap text-right text-sm">{stateText(a)}</td>
                    </tr>
                    {a.diagnosis && a.state === "resolved" && <DiagnosisRow d={a.diagnosis} cols={5} />}
                  </Fragment>
                ))}
              </tbody>
            </table>
          </ScrollArea>
        )}
        {!allHistory && (view?.recent.length ?? 0) > 30 && (
          <Button className="mt-2" size="sm" variant="outline" onClick={() => setAllHistory(true)}>
            {t("show all")} ({view?.recent.length})
          </Button>
        )}
      </Panel>
    </Shell>
  );
}
