import { useEffect, useMemo, useRef, useState } from "react";

import { AppHeader } from "@/components/AppHeader";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  api,
  ApiError,
  type OnboardLine,
  type ProbeResult,
  type ServerCard,
  type ServerView,
} from "@/lib/api";
import { navigate } from "@/lib/router";
import { cn } from "@/lib/utils";

const fieldCls =
  "h-9 w-full min-w-0 border border-input bg-field px-2.5 text-hi placeholder:text-muted-foreground focus-visible:outline-1 focus-visible:outline-ring disabled:opacity-60";

const GROUPS = ["prod", "staging", "infra", "личные"];
const PROVIDERS = ["Hetzner", "Selectel", "Timeweb", "Aeza", "DigitalOcean", "Yandex Cloud"];
const CURRENCIES = ["RUB", "EUR", "USD"];
const PERIODS = [
  { v: "month", l: "месяц" },
  { v: "year", l: "год" },
  { v: "hour", l: "час (почасовой)" },
];

const ERRORS: Record<string, string> = {
  bad_name: "имя: латиница, цифры, . _ -, до 63 символов",
  bad_host: "укажите host (ip или домен)",
  bad_port: "порт 1–65535",
  bad_date: "дата оплаты в формате ГГГГ-ММ-ДД",
  bad_cost: "некорректная стоимость",
  bad_url: "ссылка на панель должна начинаться с https://",
  bad_tag: "тег длиннее 40 символов",
  too_many_tags: "не больше 20 тегов",
  bad_user: "укажите пользователя",
  password_required: "введите пароль",
  bad_host_key: "сначала проверьте host key",
  name_taken: "сервер с таким именем уже есть",
  unreachable: "сервер недоступен по ssh",
  busy: "уже идёт несколько установок, подождите",
  no_master_key: "на сервере мониторинга нет мастер-ключа",
  not_found: "сервер не найден",
  onboarding: "для этого сервера уже идёт установка",
};

function errText(e: unknown): string {
  if (e instanceof ApiError) return ERRORS[e.code] ?? `ошибка: ${e.code}`;
  return "нет связи с сервером мониторинга";
}

function Field({ label, htmlFor, children }: { label: string; htmlFor: string; children: React.ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <label className="text-muted-foreground" htmlFor={htmlFor}>
        {label}
      </label>
      {children}
    </div>
  );
}

function Check({
  checked,
  onChange,
  disabled,
  children,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  disabled?: boolean;
  children: React.ReactNode;
}) {
  return (
    <label className={cn("flex cursor-pointer items-start gap-2", disabled && "cursor-default opacity-60")}>
      <input
        type="checkbox"
        className="sr-only"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span className={checked ? "text-primary" : "text-muted-foreground"} aria-hidden>
        {checked ? "[x]" : "[ ]"}
      </span>
      <span>{children}</span>
    </label>
  );
}

function TagInput({ tags, onChange, id }: { tags: string[]; onChange: (t: string[]) => void; id: string }) {
  const [draft, setDraft] = useState("");
  const add = (raw: string) => {
    const t = raw.trim().replace(/^#/, "");
    if (t && !tags.includes(t)) onChange([...tags, t]);
    setDraft("");
  };
  return (
    <div className="flex min-h-9 flex-wrap items-center gap-1.5 border border-input bg-field px-1.5 focus-within:outline-1 focus-within:outline-ring">
      {tags.map((t) => (
        <button
          key={t}
          type="button"
          className="inline-flex h-[22px] items-center border border-border px-1.5 text-xs text-[#9fc3a7] hover:border-crit hover:text-crit"
          onClick={() => onChange(tags.filter((x) => x !== t))}
          aria-label={`убрать тег ${t}`}
        >
          #{t} ×
        </button>
      ))}
      <input
        id={id}
        value={draft}
        placeholder="+ тег"
        className="h-[30px] min-w-[60px] flex-1 bg-transparent text-hi outline-none placeholder:text-muted-foreground"
        onChange={(e) => {
          const v = e.target.value;
          if (v.endsWith(",") || v.endsWith(" ")) add(v.slice(0, -1));
          else setDraft(v);
        }}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            add(draft);
          } else if (e.key === "Backspace" && draft === "" && tags.length) {
            onChange(tags.slice(0, -1));
          }
        }}
        onBlur={() => draft && add(draft)}
      />
    </div>
  );
}

const MARK: Record<OnboardLine["state"], { c: string; m: string }> = {
  ok: { c: "text-primary", m: "✓" },
  run: { c: "text-hi", m: "⟳" },
  warn: { c: "text-warn", m: "!" },
  fail: { c: "text-crit", m: "✗" },
  info: { c: "text-muted-foreground", m: "·" },
};

function emptyCard(): ServerCard {
  return {
    name: "",
    host: "",
    port: 22,
    project: "",
    group: "prod",
    tags: [],
    description: "",
    provider: "",
    plan: "",
    cost: null,
    currency: "RUB",
    period: "month",
    next_payment: "",
    panel_url: "",
  };
}

// "1.2.3.4:2222" or "[::1]:22" -> host, port
function splitHostPort(v: string): { host: string; port: number } {
  const s = v.trim();
  const m6 = s.match(/^\[([^\]]+)\](?::(\d+))?$/);
  if (m6) return { host: m6[1], port: Number(m6[2] ?? 22) };
  const parts = s.split(":");
  if (parts.length === 2 && /^\d+$/.test(parts[1])) return { host: parts[0], port: Number(parts[1]) };
  return { host: s, port: 22 };
}

type Phase = "form" | "running" | "done" | "failed";

export function AddServer({ attachId, onLogout }: { attachId?: number; onLogout: () => void }) {
  const [servers, setServers] = useState<ServerView[]>([]);
  const [card, setCard] = useState<ServerCard>(emptyCard);
  const [hostPort, setHostPort] = useState("");
  const [costText, setCostText] = useState("");
  const [user, setUser] = useState("root");
  const [password, setPassword] = useState("");
  const [sudo, setSudo] = useState(true);
  const [disablePassword, setDisablePassword] = useState(true);
  const [installAgent, setInstallAgent] = useState(true);
  const [probe, setProbe] = useState<ProbeResult | null>(null);
  const [probing, setProbing] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const [error, setError] = useState("");
  const [phase, setPhase] = useState<Phase>("form");
  const [job, setJob] = useState("");
  const [lines, setLines] = useState<OnboardLine[]>([]);
  const [serverId, setServerId] = useState(0);
  const logEnd = useRef<HTMLDivElement>(null);
  const attached = useRef(false);

  useEffect(() => {
    api
      .servers()
      .then(setServers)
      .catch((e) => e?.status === 401 && onLogout());
  }, [onLogout]);

  const existing = attachId ? servers.find((s) => s.id === attachId) : undefined;
  useEffect(() => {
    if (!existing || attached.current) return;
    attached.current = true;
    setCard({
      ...emptyCard(),
      name: existing.name,
      host: existing.host,
      port: existing.port || 22,
      project: existing.project,
      group: existing.group,
      tags: existing.tags,
      description: existing.description,
    });
    if (existing.host) {
      const h = existing.host.includes(":") ? `[${existing.host}]` : existing.host; // IPv6 literal
      setHostPort(existing.port && existing.port !== 22 ? `${h}:${existing.port}` : h);
    }
    setInstallAgent(!existing.last_seen);
  }, [existing]);

  const projects = useMemo(() => [...new Set(servers.map((s) => s.project).filter(Boolean))], [servers]);
  const groups = useMemo(() => [...new Set([...GROUPS, ...servers.map((s) => s.group).filter(Boolean)])], [servers]);
  const providers = useMemo(
    () => [...new Set([...PROVIDERS, ...servers.map((s) => s.provider).filter(Boolean)])],
    [servers],
  );

  const set = <K extends keyof ServerCard>(k: K, v: ServerCard[K]) => setCard((c) => ({ ...c, [k]: v }));
  const locked = phase !== "form";

  // Any change of address invalidates the confirmed host key.
  function changeHostPort(v: string) {
    setHostPort(v);
    setProbe(null);
    setConfirmed(false);
  }

  async function doProbe() {
    setError("");
    const { host, port } = splitHostPort(hostPort);
    if (!host) {
      setError(ERRORS.bad_host);
      return;
    }
    setProbing(true);
    try {
      setProbe(await api.probe(host, port));
      setConfirmed(false);
    } catch (e) {
      setError(errText(e));
    } finally {
      setProbing(false);
    }
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    if (!probe) {
      await doProbe();
      return;
    }
    if (!confirmed) {
      setError("сверьте fingerprint с сервером и подтвердите его");
      return;
    }
    const { host, port } = splitHostPort(hostPort);
    const cost = costText.trim() === "" ? null : Number(costText.replace(",", "."));
    if (cost !== null && !Number.isFinite(cost)) {
      setError(ERRORS.bad_cost);
      return;
    }
    const pw = password;
    setPassword(""); // the password lives only in this request
    try {
      const r = await api.onboard({
        server_id: attachId ?? 0,
        card: { ...card, host, port, cost },
        user,
        password: pw,
        host_key: probe.host_key,
        sudo,
        disable_password: disablePassword,
        install_agent: installAgent,
      });
      setJob(r.job);
      setServerId(r.server_id);
      setLines([]);
      setPhase("running");
    } catch (err) {
      setError(errText(err));
    }
  }

  // Poll the onboarding log.
  useEffect(() => {
    if (phase !== "running" || !job) return;
    let next = 0;
    let alive = true;
    const tick = async () => {
      try {
        const st = await api.onboardStatus(job, next);
        if (!alive) return;
        next = st.next;
        if (st.lines.length) setLines((l) => [...l, ...st.lines]);
        if (st.done) {
          setPhase(st.ok ? "done" : "failed");
          return;
        }
      } catch (e) {
        if ((e as ApiError)?.status === 401) return onLogout();
      }
      if (alive) timer = setTimeout(tick, 1000);
    };
    let timer = setTimeout(tick, 300);
    return () => {
      alive = false;
      clearTimeout(timer);
    };
  }, [phase, job, onLogout]);

  useEffect(() => {
    // Braces matter: newer browsers return a Promise from scrollIntoView.
    logEnd.current?.scrollIntoView({ block: "nearest" });
  }, [lines]);

  const stepOn = (n: number) => {
    if (phase !== "form") return n === 4;
    if (!card.name || !hostPort) return n === 1;
    if (!password) return n === 3 || n === 2;
    return n === 4;
  };

  const steps = ["1 описание", "2 оплата", "3 доступ", "4 ключ и агент"];

  return (
    <div className="flex min-h-screen flex-col">
      <AppHeader active="add" />
      <form onSubmit={submit} className="mx-auto flex w-full max-w-[1320px] flex-1 flex-col gap-7 p-4 sm:p-5">
        <div className="flex flex-wrap items-baseline gap-x-4 gap-y-2">
          <h1 className="text-xl font-bold text-hi">
            {existing ? `+ ssh-доступ: ${existing.name}` : "+ новый сервер"}
          </h1>
          <div className="flex flex-wrap items-baseline gap-1">
            {steps.map((s, i) => (
              <span key={s} className="flex items-baseline gap-1">
                {i > 0 && <span className="text-muted-foreground">──</span>}
                <span className={cn(stepOn(i + 1) ? "bg-[#12361d] px-1.5 text-hi" : "text-primary")}>[{s}]</span>
              </span>
            ))}
          </div>
        </div>

        <section className="grid gap-6 [grid-template-columns:repeat(auto-fit,minmax(300px,1fr))]">
          <Panel title="1 · описание" accent={stepOn(1)}>
            <fieldset disabled={locked} className="flex flex-col gap-2.5">
              <div className="grid grid-cols-2 gap-2.5">
                <Field label="имя" htmlFor="f-name">
                  <input
                    id="f-name"
                    className={fieldCls}
                    value={card.name}
                    required
                    disabled={!!existing}
                    placeholder="shop-db"
                    autoFocus={!attachId}
                    onChange={(e) => set("name", e.target.value)}
                  />
                </Field>
                <Field label="host : port" htmlFor="f-host">
                  <input
                    id="f-host"
                    className={fieldCls}
                    value={hostPort}
                    required
                    placeholder="95.1.2.3:22"
                    onChange={(e) => changeHostPort(e.target.value)}
                  />
                </Field>
              </div>
              <div className="grid grid-cols-2 gap-2.5">
                <Field label="проект" htmlFor="f-project">
                  <input
                    id="f-project"
                    className={fieldCls}
                    list="dl-projects"
                    value={card.project}
                    disabled={!!existing}
                    placeholder="shop"
                    onChange={(e) => set("project", e.target.value)}
                  />
                  <datalist id="dl-projects">
                    {projects.map((p) => (
                      <option key={p} value={p} />
                    ))}
                  </datalist>
                </Field>
                <Field label="группа" htmlFor="f-group">
                  <input
                    id="f-group"
                    className={fieldCls}
                    list="dl-groups"
                    value={card.group}
                    disabled={!!existing}
                    onChange={(e) => set("group", e.target.value)}
                  />
                  <datalist id="dl-groups">
                    {groups.map((g) => (
                      <option key={g} value={g} />
                    ))}
                  </datalist>
                </Field>
              </div>
              {!existing && (
                <>
                  <Field label="теги" htmlFor="f-tags">
                    <TagInput id="f-tags" tags={card.tags} onChange={(t) => set("tags", t)} />
                  </Field>
                  <Field label="описание" htmlFor="f-desc">
                    <textarea
                      id="f-desc"
                      className={cn(fieldCls, "h-16 resize-y py-2")}
                      value={card.description}
                      placeholder="Postgres магазина, реплика на shop-db-2"
                      onChange={(e) => set("description", e.target.value)}
                    />
                  </Field>
                </>
              )}
            </fieldset>
          </Panel>

          {!existing && (
            <Panel title="2 · провайдер и оплата" accent={stepOn(2)}>
              <fieldset disabled={locked} className="flex flex-col gap-2.5">
                <div className="grid grid-cols-2 gap-2.5">
                  <Field label="провайдер" htmlFor="f-prov">
                    <input
                      id="f-prov"
                      className={fieldCls}
                      list="dl-prov"
                      value={card.provider}
                      placeholder="Hetzner"
                      onChange={(e) => set("provider", e.target.value)}
                    />
                    <datalist id="dl-prov">
                      {providers.map((p) => (
                        <option key={p} value={p} />
                      ))}
                    </datalist>
                  </Field>
                  <Field label="тариф" htmlFor="f-plan">
                    <input
                      id="f-plan"
                      className={fieldCls}
                      value={card.plan}
                      placeholder="CPX31"
                      onChange={(e) => set("plan", e.target.value)}
                    />
                  </Field>
                </div>
                <div className="grid grid-cols-2 gap-2.5">
                  <Field label="стоимость" htmlFor="f-cost">
                    <input
                      id="f-cost"
                      className={fieldCls}
                      inputMode="decimal"
                      value={costText}
                      placeholder="15.90"
                      onChange={(e) => setCostText(e.target.value)}
                    />
                  </Field>
                  <Field label="валюта" htmlFor="f-cur">
                    <select
                      id="f-cur"
                      className={fieldCls}
                      value={card.currency}
                      onChange={(e) => set("currency", e.target.value)}
                    >
                      {CURRENCIES.map((c) => (
                        <option key={c}>{c}</option>
                      ))}
                    </select>
                  </Field>
                </div>
                <div className="grid grid-cols-2 gap-2.5">
                  <Field label="период" htmlFor="f-period">
                    <select
                      id="f-period"
                      className={fieldCls}
                      value={card.period}
                      onChange={(e) => set("period", e.target.value)}
                    >
                      {PERIODS.map((p) => (
                        <option key={p.v} value={p.v}>
                          {p.l}
                        </option>
                      ))}
                    </select>
                  </Field>
                  <Field label="след. оплата" htmlFor="f-next">
                    <input
                      id="f-next"
                      type="date"
                      className={fieldCls}
                      value={card.next_payment}
                      onChange={(e) => set("next_payment", e.target.value)}
                    />
                  </Field>
                </div>
                <Field label="ссылка на панель провайдера" htmlFor="f-url">
                  <input
                    id="f-url"
                    type="url"
                    className={fieldCls}
                    value={card.panel_url}
                    placeholder="https://console.hetzner.cloud/…"
                    onChange={(e) => set("panel_url", e.target.value)}
                  />
                </Field>
                <div className="text-xs text-muted-foreground">
                  напоминание в tg за 3 дня до оплаты · шаг можно пропустить
                </div>
              </fieldset>
            </Panel>
          )}

          <Panel title="3 · разовый доступ" accent={stepOn(3)}>
            <fieldset disabled={locked} className="flex flex-col gap-2.5">
              <Field label="пользователь" htmlFor="f-user">
                <input
                  id="f-user"
                  className={fieldCls}
                  value={user}
                  autoComplete="off"
                  onChange={(e) => setUser(e.target.value)}
                />
              </Field>
              <Field label="пароль (один раз)" htmlFor="f-pw">
                <input
                  id="f-pw"
                  type="password"
                  className={fieldCls}
                  value={password}
                  autoComplete="new-password"
                  onChange={(e) => setPassword(e.target.value)}
                />
              </Field>
              <div className="text-xs text-warn">
                пароль нужен только для установки ключа: не пишется в бд и логи, после шага 4 стирается. Подойдёт root
                или пользователь с sudo.
              </div>
            </fieldset>
          </Panel>

          <Panel title="4 · ключ и агент" accent={stepOn(4)}>
            <div className="flex flex-col gap-1.5">
              <div>
                <span className="text-muted-foreground">ключ </span> ed25519, генерируется на сервере мониторинга
              </div>
              <div>
                <span className="text-muted-foreground">юзер </span> srvmon (группа docker)
              </div>
              <div className="break-all">
                <span className="text-muted-foreground">host </span>
                {probe ? (
                  <span className="text-hi">
                    {probe.type} {probe.fingerprint}
                  </span>
                ) : (
                  <span className="text-muted-foreground">не проверен</span>
                )}
              </div>
              {probe && (
                <div className="mb-1">
                  <Check checked={confirmed} onChange={setConfirmed} disabled={locked}>
                    fingerprint совпадает с сервером{" "}
                    <span className="text-xs text-muted-foreground">(ssh-keygen -lf /etc/ssh/ssh_host_*_key.pub)</span>
                  </Check>
                </div>
              )}
              <div className="mt-1.5 flex flex-col gap-1.5">
                <Check checked={installAgent} onChange={setInstallAgent} disabled={locked}>
                  установить srvmon-agent
                </Check>
                <Check checked={sudo} onChange={setSudo} disabled={locked}>
                  sudo без пароля для srvmon <span className="text-xs text-muted-foreground">(root в терминале)</span>
                </Check>
                <Check checked={disablePassword} onChange={setDisablePassword} disabled={locked}>
                  отключить вход по паролю
                </Check>
              </div>
            </div>
          </Panel>
        </section>

        <Panel title="журнал установки" accent={phase !== "form"}>
          <ScrollArea className="max-h-[320px] border border-border bg-field">
            <div className="px-3 py-2.5 leading-normal">
              {lines.length === 0 && phase === "form" && (
                <div className="text-muted-foreground">
                  {probe
                    ? "○ сверьте fingerprint и нажмите «добавить»"
                    : "○ заполните имя и адрес, затем «проверить host key»"}
                </div>
              )}
              {lines.map((l, i) => (
                <div key={i} className="whitespace-pre-wrap">
                  <span className={MARK[l.state].c}>{MARK[l.state].m}</span> {l.text}
                </div>
              ))}
              {phase === "running" && <div className="text-muted-foreground">○ выполняется…</div>}
              <div ref={logEnd} />
            </div>
          </ScrollArea>
          {error && (
            <div className="mt-3 text-crit" role="alert">
              {error}
            </div>
          )}
          <div className="mt-3.5 flex flex-wrap gap-3">
            {phase === "form" && (
              <>
                <Button type="button" variant="outline" onClick={() => navigate("/")}>
                  ← назад
                </Button>
                <Button type="button" variant="outline" onClick={doProbe} disabled={probing || !hostPort}>
                  {probing ? "проверяем…" : probe ? "проверить заново" : "проверить host key"}
                </Button>
                <span className="flex-1" />
                <Button type="submit" disabled={probing || (!!probe && (!confirmed || !password || !card.name))}>
                  {probe ? (existing ? "подключить ⏎" : "добавить сервер ⏎") : "далее ⏎"}
                </Button>
              </>
            )}
            {phase === "running" && (
              <Button type="button" variant="destructive" onClick={() => api.cancelOnboard(job).catch(() => undefined)}>
                отмена (откат)
              </Button>
            )}
            {phase === "failed" && (
              <>
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => {
                    setPhase("form");
                    setJob("");
                  }}
                >
                  ← исправить и повторить
                </Button>
                <span className="flex-1" />
                <Button type="button" variant="outline" onClick={() => navigate("/")}>
                  к обзору
                </Button>
              </>
            )}
            {phase === "done" && (
              <>
                <Button type="button" variant="outline" onClick={() => navigate("/")}>
                  к обзору
                </Button>
                <span className="flex-1" />
                <Button type="button" onClick={() => navigate(`/term/${serverId}`)}>
                  открыть терминал →
                </Button>
              </>
            )}
          </div>
        </Panel>
      </form>

      <footer className="flex flex-wrap gap-x-4 gap-y-1 border-t border-border px-4 py-1.5 sm:px-5">
        <span>
          <span className="bg-primary px-1 text-primary-foreground">tab</span> поле
        </span>
        <span>
          <span className="bg-primary px-1 text-primary-foreground">⏎</span> далее
        </span>
        <span className="ml-auto text-muted-foreground">
          агент получает свой токен для push-метрик · ssh-ключ хранится зашифрованным
        </span>
      </footer>
    </div>
  );
}
