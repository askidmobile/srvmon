import { type ReactNode, useCallback, useEffect, useState } from "react";

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
  type NotifyView,
  type Quiet,
  type Rule,
  type Severity,
} from "@/lib/api";
import { href, type Route } from "@/lib/router";
import { cn } from "@/lib/utils";

const fieldCls =
  "h-9 w-full min-w-0 border border-input bg-field px-2.5 text-hi placeholder:text-muted-foreground focus-visible:outline-1 focus-visible:outline-ring disabled:opacity-60";
const cellField =
  "h-6 min-w-0 border border-input bg-field px-1.5 text-xs text-hi focus-visible:outline-1 focus-visible:outline-ring";

// How each rule kind reads in the table and which fields it has.
// dur: "for" — the condition must hold this long; "window" — counting
// window; "silent" — how long the agent is quiet; null — no duration.
const KINDS: Record<
  string,
  { name: string; label: (t: number) => string; unit?: string; dur: "for" | "window" | "silent" | null }
> = {
  agent_silent: { name: "агент молчит", label: () => "агент молчит", dur: "silent" },
  cpu: { name: "cpu >", label: (t) => `cpu > ${t}%`, unit: "%", dur: "for" },
  mem: { name: "mem >", label: (t) => `mem > ${t}%`, unit: "%", dur: "for" },
  disk: { name: "disk >", label: (t) => `disk > ${t}%`, unit: "%", dur: "for" },
  container_down: { name: "контейнер упал / unhealthy", label: () => "контейнер упал / unhealthy", dur: "for" },
  container_restarts: {
    name: "рестартов контейнера >",
    label: (t) => `рестартов контейнера > ${t}`,
    unit: "раз",
    dur: "window",
  },
  payment_due: { name: "оплата сервера через ≤", label: (t) => `оплата сервера через ≤ ${t} дн.`, unit: "дн.", dur: null },
  fail2ban: { name: "бан fail2ban", label: () => "бан fail2ban на сервере мониторинга", dur: null },
  bruteforce: { name: "подбор пароля к панели", label: () => "подбор пароля к панели", dur: null },
  login: { name: "вход в панель", label: () => "вход в панель", dur: null },
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
};

const sevCls: Record<Severity, string> = { crit: "text-crit", warn: "text-warn", info: "text-tx" };

// 300 → "5м", 5400 → "1ч30м". Display keeps two units; full keeps all
// (for the editor, so saving does not shorten the value).
export function fmtDuration(sec: number, full = false): string {
  if (sec <= 0) return "─";
  const parts: string[] = [];
  for (const [n, u] of [
    [86400, "д"],
    [3600, "ч"],
    [60, "м"],
    [1, "с"],
  ] as const) {
    if (sec >= n) {
      parts.push(`${Math.floor(sec / n)}${u}`);
      sec %= n;
    }
  }
  return (full ? parts : parts.slice(0, 2)).join("");
}

// "1ч30м", "90s", "5m", "10" (minutes) → seconds; null when unreadable.
export function parseDuration(s: string): number | null {
  const t = s.trim().toLowerCase().replace(/\s+/g, "");
  if (t === "" || t === "─" || t === "-" || t === "0") return 0;
  if (/^\d+$/.test(t)) return Number(t) * 60;
  const mult: Record<string, number> = { д: 86400, d: 86400, ч: 3600, h: 3600, м: 60, m: 60, с: 1, s: 1 };
  let total = 0;
  let rest = t;
  const re = /^(\d+)(д|d|ч|h|м|m|с|s)/;
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

function AlertRow({ a, onChange }: { a: Alert; onChange: () => void }) {
  const [busy, setBusy] = useState(false);
  const act = async (f: () => Promise<unknown>) => {
    setBusy(true);
    await f().catch(() => undefined);
    setBusy(false);
    onChange();
  };
  return (
    <tr className="border-t border-border/60 align-top">
      <td className="py-1.5 pr-3 whitespace-nowrap text-muted-foreground">{when(a.started_at)}</td>
      <td className={cn("pr-3 uppercase", sevCls[a.severity])}>{a.severity}</td>
      <td className="pr-3 whitespace-nowrap">
        {a.server_id ? (
          <a className="text-hi hover:text-primary" href={href.server(a.server_id)}>
            {a.server_name}
          </a>
        ) : (
          <span className="text-hi">{a.server_name || "панель"}</span>
        )}
      </td>
      <td className="pr-3">
        {a.message}
        {a.channel === "tg" && <span className="text-muted-foreground">{a.notified ? " → tg" : ""}</span>}
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
        <Button
          size="sm"
          variant="ghost"
          disabled={busy}
          title="не присылать уведомления по этому алерту час"
          onClick={() => act(() => api.silenceAlert(a.id, 60))}
        >
          тишина 1ч
        </Button>
      </td>
    </tr>
  );
}

function stateText(a: Alert): ReactNode {
  switch (a.state) {
    case "event":
      return <span className="text-muted-foreground">событие</span>;
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
      setErr("длительность: например 5м, 1ч, 30с");
      return;
    }
    const threshold = k.unit ? Number(thr.replace(",", ".")) : 0;
    if (!Number.isFinite(threshold)) {
      setErr("порог — число");
      return;
    }
    const next = { ...r, threshold, duration_sec: duration };
    try {
      if (isNew) await api.createRule(next);
      else await api.updateRule(next);
      onDone(true);
    } catch (e) {
      setErr(errorMessage(e, "не сохранилось"));
    }
  };

  return (
    <>
      <tr className="border-t border-border/60 bg-secondary/40">
        <td className="py-1.5 pr-2">
          <button type="button" className={r.enabled ? "text-primary" : "text-muted-foreground"} onClick={() => setR({ ...r, enabled: !r.enabled })}>
            {r.enabled ? "[x]" : "[ ]"}
          </button>
        </td>
        <td className="pr-2">
          <div className="flex flex-wrap items-center gap-1.5">
            {isNew ? (
              <select className={cellField} value={r.kind} onChange={(e) => changeKind(e.target.value)} aria-label="тип">
                {Object.entries(KINDS).map(([kind, m]) => (
                  <option key={kind} value={kind}>
                    {m.name}
                  </option>
                ))}
              </select>
            ) : (
              <span>{k.name}</span>
            )}
            {k.unit && (
              <>
                <input
                  className={cn(cellField, "w-16")}
                  inputMode="decimal"
                  value={thr}
                  onChange={(e) => setThr(e.target.value)}
                  aria-label="порог"
                />
                <span className="text-xs text-muted-foreground">{k.unit}</span>
              </>
            )}
          </div>
        </td>
        <td className="pr-2">
          {k.dur ? (
            <input
              className={cn(cellField, "w-20")}
              value={dur}
              placeholder={k.dur === "for" ? "сразу" : "5м"}
              onChange={(e) => setDur(e.target.value)}
              aria-label="длительность"
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
            aria-label="уровень"
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
            aria-label="канал"
          >
            <option value="tg">tg</option>
            <option value="web">web</option>
          </select>
        </td>
        <td className="whitespace-nowrap text-right">
          <Button size="sm" onClick={save}>
            ок
          </Button>{" "}
          <Button size="sm" variant="ghost" onClick={() => onDone(false)}>
            отмена
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
      .catch(() => setErr("не удалось загрузить правила"));
  }, []);
  useEffect(load, [load]);

  const toggle = async (r: Rule) => {
    try {
      await api.updateRule({ ...r, enabled: !r.enabled });
      load();
    } catch (e) {
      setErr(errorMessage(e, "не сохранилось"));
    }
  };
  const remove = async (r: Rule) => {
    if (!confirm(`Удалить правило «${KINDS[r.kind]?.label(r.threshold) ?? r.kind}»?`)) return;
    await api.deleteRule(r.id).catch(() => setErr("не удалось удалить"));
    load();
  };
  const done = (changed: boolean) => {
    setEditing(null);
    if (changed) load();
  };

  return (
    <Panel title="правила алертов" right="проверка каждые 15с">
      <ScrollArea className="w-full">
        <table className="w-full min-w-[560px] border-collapse text-left">
          <thead className="text-xs text-muted-foreground">
            <tr>
              <th className="pb-2 font-normal">вкл</th>
              <th className="pb-2 font-normal">условие</th>
              <th className="pb-2 font-normal">длит.</th>
              <th className="pb-2 font-normal">уровень</th>
              <th className="pb-2 font-normal">канал</th>
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
                      className={r.enabled ? "text-primary" : "text-muted-foreground"}
                      title={r.enabled ? "выключить" : "включить"}
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
                  <td className="pr-2 text-muted-foreground">{r.channel}</td>
                  <td className="whitespace-nowrap text-right">
                    <Button size="sm" variant="ghost" onClick={() => setEditing(r.id)}>
                      изм.
                    </Button>
                    <Button size="sm" variant="ghost" className="hover:text-crit" title="удалить" onClick={() => remove(r)}>
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
          + правило
        </Button>
        <QuietHours />
      </div>
      <p className="mt-2 text-xs text-muted-foreground">
        tg — уведомление в Telegram и на панели, web — только на панели. Контейнеры, остановленные с кодом 0, алертом
        не считаются.
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
      setSaved("сохранено");
    } catch (e) {
      setSaved(errorMessage(e, "ошибка"));
    }
    setTimeout(() => setSaved(""), 3000);
  };
  return (
    <div className="flex flex-wrap items-center gap-1.5 text-sm text-muted-foreground">
      <span>тишина</span>
      <input type="time" className={cn(cellField, "h-7")} value={q.from} onChange={(e) => setQ({ ...q, from: e.target.value })} aria-label="с" />
      <span>–</span>
      <input type="time" className={cn(cellField, "h-7")} value={q.to} onChange={(e) => setQ({ ...q, to: e.target.value })} aria-label="до" />
      <input
        className={cn(cellField, "h-7 w-36")}
        value={q.tz}
        placeholder="Europe/Moscow"
        onChange={(e) => setQ({ ...q, tz: e.target.value })}
        aria-label="часовой пояс"
      />
      <span>кроме crit</span>
      <Button size="sm" variant="outline" onClick={save}>
        сохранить
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
      setMsg({ ok: false, text: e instanceof ApiError && e.code === "no_master_key" ? "нет мастер-ключа на сервере" : errorMessage(e, "ошибка") });
    }
    setBusy(false);
  };

  if (!n) return <Panel title="telegram-бот">загрузка…</Panel>;
  const tg = n.telegram;
  const status = !tg.configured ? (
    <span className="text-muted-foreground">○ не настроен</span>
  ) : tg.connected ? (
    <span className="text-primary">● подключён{tg.bot_name ? ` · @${tg.bot_name}` : ""}</span>
  ) : tg.last_error ? (
    <span className="text-crit">✕ {tg.last_error}</span>
  ) : (
    <span className="text-warn">… подключается</span>
  );

  return (
    <Panel title="telegram-бот">
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
            placeholder={tg.configured ? "сохранён · оставьте пустым, чтобы не менять" : "123456789:AA…"}
            onChange={(e) => setToken(e.target.value)}
          />
        </div>
        <div className="flex flex-col gap-1">
          <label className="text-sm text-muted-foreground" htmlFor="tg-chat">
            chat id (бот отвечает только этому чату)
          </label>
          <input
            id="tg-chat"
            className={fieldCls}
            inputMode="numeric"
            value={chat}
            placeholder="напишите боту /start — id появится ниже"
            onChange={(e) => setChat(e.target.value)}
          />
        </div>
        {tg.pending_chat && (
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="text-muted-foreground">боту написал</span>
            <span className="text-hi">
              {tg.pending_chat.title || "чат"} · {tg.pending_chat.id}
            </span>
            <Button
              size="sm"
              variant="outline"
              disabled={busy}
              onClick={() => run(() => api.saveTelegram(tg.pending_chat!.id), "чат сохранён")}
            >
              использовать этот чат
            </Button>
          </div>
        )}
        <div className="text-sm">
          <span className="text-muted-foreground">статус</span> {status}
          {tg.last_message && <span className="text-muted-foreground"> · последняя команда {when(tg.last_message)}</span>}
          {tg.last_sent && <span className="text-muted-foreground"> · отправлено {when(tg.last_sent)}</span>}
        </div>
        <div className="text-sm text-muted-foreground">
          команды: <span className="text-hi">/status</span> · <span className="text-hi">/server &lt;имя&gt;</span> ·{" "}
          <span className="text-hi">/alerts</span> · <span className="text-hi">/ack &lt;id&gt;</span> ·{" "}
          <span className="text-hi">/bills</span> · <span className="text-hi">/ask &lt;вопрос&gt;</span>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button
            disabled={busy}
            onClick={() => run(() => api.saveTelegram(chat.trim(), token.trim() || undefined), "сохранено")}
          >
            сохранить
          </Button>
          <Button variant="outline" disabled={busy || !tg.configured || !tg.chat_id} onClick={() => run(api.testTelegram, "тест отправлен")}>
            отправить тест
          </Button>
          {tg.configured && (
            <Button
              variant="ghost"
              className="hover:text-crit"
              disabled={busy}
              onClick={() => confirm("Отключить бота? Токен будет удалён.") && run(() => api.saveTelegram(chat.trim(), ""), "бот отключён")}
            >
              отключить
            </Button>
          )}
        </div>
        {msg && <div className={cn("text-sm", msg.ok ? "text-primary" : "text-crit")}>{msg.text}</div>}
        {!tg.configured && (
          <ol className="list-decimal space-y-0.5 pl-5 text-xs text-muted-foreground">
            <li>
              В Telegram откройте <span className="text-hi">@BotFather</span> → <span className="text-hi">/newbot</span>,
              задайте имя и username (на «bot»).
            </li>
            <li>Скопируйте токен в поле выше и нажмите «сохранить».</li>
            <li>
              Напишите своему боту <span className="text-hi">/start</span> и нажмите «использовать этот чат».
            </li>
          </ol>
        )}
      </div>
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
        else setError("нет связи с сервером");
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
          <Hotkey k="1">обзор</Hotkey>
          <Hotkey k="5">алерты</Hotkey>
        </>
      }
      footerNote="обновление 10с"
    >
      <Panel
        title="активные алерты"
        right={view ? `${view.open.length} активных · ${view.last24h} за 24ч` : undefined}
        accent={!!view?.open.some((a) => a.severity === "crit" && a.state === "firing")}
      >
        {view && view.open.length === 0 ? (
          <div className="py-2 text-primary">✓ всё спокойно</div>
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
        <TelegramPanel />
      </div>

      <Panel title="история" right="события и алерты за 90 дней">
        {history.length === 0 ? (
          <div className="py-2 text-muted-foreground">пока пусто</div>
        ) : (
          <ScrollArea className="w-full">
            <table className="w-full min-w-[720px] border-collapse text-left">
              <tbody>
                {history.map((a) => (
                  <tr key={a.id} className="border-t border-border/60 align-top">
                    <td className="py-1 pr-3 whitespace-nowrap text-muted-foreground">{when(a.started_at)}</td>
                    <td className={cn("pr-3 uppercase", sevCls[a.severity])}>{a.severity}</td>
                    <td className="pr-3 whitespace-nowrap text-hi">{a.server_name || "панель"}</td>
                    <td className="pr-3">
                      {a.message}
                      {a.notified && <span className="text-muted-foreground"> → tg</span>}
                    </td>
                    <td className="whitespace-nowrap text-right text-sm">{stateText(a)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </ScrollArea>
        )}
        {!allHistory && (view?.recent.length ?? 0) > 30 && (
          <Button className="mt-2" size="sm" variant="outline" onClick={() => setAllHistory(true)}>
            показать все ({view?.recent.length})
          </Button>
        )}
      </Panel>
    </Shell>
  );
}
