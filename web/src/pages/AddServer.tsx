import { useEffect, useMemo, useRef, useState } from "react";

import { Hotkey, Shell } from "@/components/Shell";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  api,
  ApiError,
  type OnboardLine,
  type ProbeResult,
  type SetupKey,
  type NewServerCard,
  type ServerView,
} from "@/lib/api";
import { t } from "@/lib/i18n";
import { href, navigate, type Route } from "@/lib/router";
import { cn } from "@/lib/utils";

const fieldCls =
  "h-9 w-full min-w-0 border border-input bg-field px-2.5 text-hi placeholder:text-muted-foreground focus-visible:outline-1 focus-visible:outline-ring disabled:opacity-60";

const GROUPS = ["prod", "staging", "infra", "personal"];
const PROVIDERS = ["Hetzner", "Selectel", "Timeweb", "Aeza", "DigitalOcean", "Yandex Cloud"];
const CURRENCIES = ["RUB", "EUR", "USD"];
const PERIODS = [
  { v: "month", l: "month" },
  { v: "year", l: "year" },
  { v: "hour", l: "hour (hourly)" },
];

type LoginMethod = "password" | "key" | "panel_key";

const LOGIN_METHODS: { v: LoginMethod; l: string }[] = [
  { v: "password", l: "password" },
  { v: "key", l: "my key" },
  { v: "panel_key", l: "panel key" },
];

const ERRORS: Record<string, string> = {
  bad_name: "name: latin letters, digits, . _ -, up to 63 characters",
  bad_host: "enter a host (ip or domain)",
  bad_port: "port 1–65535",
  bad_date: "payment date as YYYY-MM-DD",
  bad_cost: "invalid cost",
  bad_url: "the panel link must start with https://",
  bad_tag: "tag: up to 32 characters, no spaces or commas; at most 20 tags",
  bad_description: "description: up to 500 characters",
  field_too_long: "value too long: project and group up to 64 characters, plan up to 128",
  bad_period: "invalid payment period",
  too_many_tags: "at most 20 tags",
  bad_user: "enter a user",
  password_required: "enter the password",
  private_key_required: "paste the private key",
  public_key_given: "this is a public key (.pub): a private one is needed, for example ~/.ssh/id_ed25519",
  bad_private_key: "could not read the private key (OpenSSH or PEM format needed)",
  passphrase_required: "the key is encrypted: enter its passphrase",
  bad_passphrase: "wrong key passphrase",
  setup_key_expired: "the panel key is stale (the server restarted or an hour passed): get a new one",
  bad_method: "unknown sign-in method",
  bad_host_key: "check the host key first",
  name_taken: "a server with this name already exists",
  unreachable: "the server is unreachable over ssh",
  busy: "several setups are already running, please wait",
  no_master_key: "the monitoring server has no master key",
  not_found: "server not found",
  onboarding: "a setup is already running for this server",
};

function errText(e: unknown): string {
  if (e instanceof ApiError) return ERRORS[e.code] ? t(ERRORS[e.code]) : t("error: {code}", { code: e.code });
  return t("no connection to the monitoring server");
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
      <span className={cn("shrink-0 whitespace-pre", checked ? "text-primary" : "text-muted-foreground")} aria-hidden>
        {checked ? "[x]" : "[ ]"}
      </span>
      <span>{children}</span>
    </label>
  );
}

function TagInput({ tags, onChange, id }: { tags: string[]; onChange: (t: string[]) => void; id: string }) {
  const [draft, setDraft] = useState("");
  const add = (raw: string) => {
    const tag = raw.trim().replace(/^#/, "");
    if (tag && !tags.includes(tag)) onChange([...tags, tag]);
    setDraft("");
  };
  return (
    <div className="flex min-h-9 flex-wrap items-center gap-1.5 border border-input bg-field px-1.5 focus-within:outline-1 focus-within:outline-ring">
      {tags.map((tag) => (
        <button
          key={tag}
          type="button"
          className="inline-flex h-[22px] items-center border border-border px-1.5 text-xs text-[#9fc3a7] hover:border-crit hover:text-crit"
          onClick={() => onChange(tags.filter((x) => x !== tag))}
          aria-label={t("remove tag {tag}", { tag })}
        >
          #{tag} ×
        </button>
      ))}
      <input
        id={id}
        value={draft}
        placeholder={`+ ${t("tag")}`}
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

function emptyCard(): NewServerCard {
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

export function AddServer({
  attachId,
  route,
  version,
  onLogout,
}: {
  attachId?: number;
  route: Route;
  version: string;
  onLogout: () => void;
}) {
  const [servers, setServers] = useState<ServerView[]>([]);
  const [card, setCard] = useState<NewServerCard>(emptyCard);
  const [hostPort, setHostPort] = useState("");
  const [costText, setCostText] = useState("");
  const [user, setUser] = useState("root");
  const [password, setPassword] = useState("");
  const [method, setMethod] = useState<LoginMethod>("password");
  const [privateKey, setPrivateKey] = useState("");
  const [passphrase, setPassphrase] = useState("");
  const [setupKey, setSetupKey] = useState<SetupKey | null>(null);
  const [copied, setCopied] = useState(false);
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
  const [committed, setCommitted] = useState(false);
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

  const set = <K extends keyof NewServerCard>(k: K, v: NewServerCard[K]) => setCard((c) => ({ ...c, [k]: v }));
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
      setError(t("check the fingerprint against the server and confirm it"));
      return;
    }
    const { host, port } = splitHostPort(hostPort);
    const cost = costText.trim() === "" ? null : Number(costText.replace(",", "."));
    if (cost !== null && !Number.isFinite(cost)) {
      setError(ERRORS.bad_cost);
      return;
    }
    // Secrets live only in this request.
    const pw = password;
    const pk = privateKey;
    const pp = passphrase;
    setPassword("");
    setPrivateKey("");
    setPassphrase("");
    try {
      const r = await api.onboard({
        server_id: attachId ?? 0,
        card: { ...card, host, port, cost },
        user,
        method,
        password: method === "password" || user !== "root" ? pw : "",
        private_key: method === "key" ? pk : "",
        passphrase: method === "key" ? pp : "",
        setup_key: method === "panel_key" ? (setupKey?.id ?? "") : "",
        host_key: probe.host_key,
        sudo,
        disable_password: disablePassword,
        install_agent: installAgent,
      });
      setJob(r.job);
      setServerId(r.server_id);
      setLines([]);
      setCommitted(false);
      setPhase("running");
    } catch (err) {
      setError(errText(err));
      // The panel key is gone (expired or the server restarted): offer a new one.
      if (err instanceof ApiError && err.code === "setup_key_expired") setSetupKey(null);
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
        setCommitted(st.committed);
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

  const loginReady = method === "password" ? !!password : method === "key" ? !!privateKey.trim() : !!setupKey;

  async function getSetupKey() {
    setError("");
    try {
      setSetupKey(await api.setupKey());
      setCopied(false);
    } catch (e) {
      setError(errText(e));
    }
  }

  const sudoField = (
    <Field
      label={user === "root" ? t("sudo password (not needed for root)") : t("sudo password, if sudo asks for one")}
      htmlFor="f-sudo"
    >
      <input
        id="f-sudo"
        type="password"
        className={fieldCls}
        value={password}
        autoComplete="new-password"
        disabled={user === "root"}
        onChange={(e) => setPassword(e.target.value)}
      />
    </Field>
  );

  const stepOn = (n: number) => {
    if (phase !== "form") return n === 4;
    if (!card.name || !hostPort) return n === 1;
    if (!loginReady) return n === 3 || n === 2;
    return n === 4;
  };

  const steps = [`1 ${t("description")}`, `2 ${t("payment")}`, `3 ${t("access")}`, `4 ${t("key and agent")}`];

  return (
    <Shell
      route={route}
      version={version}
      onLogout={onLogout}
      footer={
        <>
          <Hotkey k="tab">{t("field")}</Hotkey>
          <Hotkey k="⏎">{t("next")}</Hotkey>
        </>
      }
      footerNote={t("the agent gets its own token to push metrics · the ssh key is stored encrypted")}
    >
      <form onSubmit={submit} className="mx-auto flex w-full max-w-[1320px] flex-1 flex-col gap-7">
        <div className="flex flex-wrap items-baseline gap-x-4 gap-y-2">
          <h1 className="text-xl font-bold text-hi">
            {existing ? `+ ${t("ssh access:")} ${existing.name}` : `+ ${t("new server")}`}
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
          <Panel title={`1 · ${t("description")}`} accent={stepOn(1)}>
            <fieldset disabled={locked} className="flex min-w-0 flex-col gap-2.5">
              <div className="grid grid-cols-2 gap-2.5">
                <Field label={t("name")} htmlFor="f-name">
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
                <Field label={t("project")} htmlFor="f-project">
                  <input
                    id="f-project"
                    maxLength={64}
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
                <Field label={t("group")} htmlFor="f-group">
                  <input
                    id="f-group"
                    maxLength={64}
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
                  <Field label={t("tags")} htmlFor="f-tags">
                    <TagInput id="f-tags" tags={card.tags} onChange={(t) => set("tags", t)} />
                  </Field>
                  <Field label={t("description")} htmlFor="f-desc">
                    <textarea
                      id="f-desc"
                      maxLength={500}
                      className={cn(fieldCls, "h-16 resize-y py-2")}
                      value={card.description}
                      placeholder={t("Shop Postgres, replica on shop-db-2")}
                      onChange={(e) => set("description", e.target.value)}
                    />
                  </Field>
                </>
              )}
            </fieldset>
          </Panel>

          {!existing && (
            <Panel title={`2 · ${t("provider and payment")}`} accent={stepOn(2)}>
              <fieldset disabled={locked} className="flex min-w-0 flex-col gap-2.5">
                <div className="grid grid-cols-2 gap-2.5">
                  <Field label={t("provider")} htmlFor="f-prov">
                    <input
                      id="f-prov"
                      maxLength={64}
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
                  <Field label={t("plan")} htmlFor="f-plan">
                    <input
                      id="f-plan"
                      maxLength={128}
                      className={fieldCls}
                      value={card.plan}
                      placeholder="CPX31"
                      onChange={(e) => set("plan", e.target.value)}
                    />
                  </Field>
                </div>
                <div className="grid grid-cols-2 gap-2.5">
                  <Field label={t("cost")} htmlFor="f-cost">
                    <input
                      id="f-cost"
                      className={fieldCls}
                      inputMode="decimal"
                      value={costText}
                      placeholder="15.90"
                      onChange={(e) => setCostText(e.target.value)}
                    />
                  </Field>
                  <Field label={t("currency")} htmlFor="f-cur">
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
                  <Field label={t("period")} htmlFor="f-period">
                    <select
                      id="f-period"
                      className={fieldCls}
                      value={card.period}
                      onChange={(e) => set("period", e.target.value)}
                    >
                      {PERIODS.map((p) => (
                        <option key={p.v} value={p.v}>
                          {t(p.l)}
                        </option>
                      ))}
                    </select>
                  </Field>
                  <Field label={t("next payment")} htmlFor="f-next">
                    <input
                      id="f-next"
                      type="date"
                      className={fieldCls}
                      value={card.next_payment}
                      onChange={(e) => set("next_payment", e.target.value)}
                    />
                  </Field>
                </div>
                <Field label={t("provider panel link")} htmlFor="f-url">
                  <input
                    id="f-url"
                    maxLength={200}
                    type="url"
                    className={fieldCls}
                    value={card.panel_url}
                    placeholder="https://console.hetzner.cloud/…"
                    onChange={(e) => set("panel_url", e.target.value)}
                  />
                </Field>
                <div className="text-xs text-muted-foreground">
                  {t("tg reminder 3 days before the payment · this step can be skipped")}
                </div>
              </fieldset>
            </Panel>
          )}

          <Panel title={`3 · ${t("one-time access")}`} accent={stepOn(3)}>
            <fieldset disabled={locked} className="flex min-w-0 flex-col gap-2.5">
              <Field label={t("user")} htmlFor="f-user">
                <input
                  id="f-user"
                  className={fieldCls}
                  value={user}
                  autoComplete="off"
                  onChange={(e) => setUser(e.target.value)}
                />
              </Field>
              <div className="flex flex-col gap-1">
                <span className="text-muted-foreground">{t("sign-in")}</span>
                <div className="flex flex-wrap gap-1" role="radiogroup" aria-label={t("sign-in method")}>
                  {LOGIN_METHODS.map((m) => (
                    <Button
                      key={m.v}
                      type="button"
                      size="sm"
                      role="radio"
                      aria-checked={method === m.v}
                      variant={method === m.v ? "default" : "outline"}
                      onClick={() => {
                        setMethod(m.v);
                        setError("");
                      }}
                    >
                      {t(m.l)}
                    </Button>
                  ))}
                </div>
              </div>

              {method === "password" && (
                <>
                  <Field label={t("password (once)")} htmlFor="f-pw">
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
                    {t(
                      "The password is only used to install the key: it is not written to the db or logs and is wiped after step 4. Use root or a user with sudo. Password sign-in is off on the server? Pick “my key” or “panel key”.",
                    )}
                  </div>
                </>
              )}

              {method === "key" && (
                <>
                  <Field label={t("private key (once)")} htmlFor="f-pk">
                    <textarea
                      id="f-pk"
                      className={cn(fieldCls, "h-24 resize-y py-1.5 font-mono text-xs")}
                      value={privateKey}
                      spellCheck={false}
                      autoComplete="off"
                      placeholder={"-----BEGIN OPENSSH PRIVATE KEY-----\n…"}
                      onChange={(e) => setPrivateKey(e.target.value)}
                    />
                  </Field>
                  <Field label={t("key passphrase, if any")} htmlFor="f-pp">
                    <input
                      id="f-pp"
                      type="password"
                      className={fieldCls}
                      value={passphrase}
                      autoComplete="new-password"
                      onChange={(e) => setPassphrase(e.target.value)}
                    />
                  </Field>
                  {sudoField}
                  <div className="text-xs text-warn">
                    {t(
                      "The private key you sign in to the server with (for example ~/.ssh/id_ed25519, not .pub). It is used once to install the srvmon key: not written to the db or logs, wiped after step 4.",
                    )}
                  </div>
                </>
              )}

              {method === "panel_key" && (
                <>
                  {!setupKey ? (
                    <Button type="button" variant="outline" onClick={getSetupKey}>
                      {t("show the panel key")}
                    </Button>
                  ) : (
                    <div className="flex flex-col gap-1.5">
                      <span className="text-muted-foreground">
                        {t("run on the server as user")} <span className="text-hi">{user || "root"}</span>:
                      </span>
                      <pre className="max-h-28 overflow-auto border border-border bg-field p-2 text-xs break-all whitespace-pre-wrap text-hi">
                        {setupKey.command}
                      </pre>
                      <div className="flex flex-wrap items-center gap-2">
                        <Button
                          type="button"
                          size="sm"
                          variant="outline"
                          onClick={() =>
                            navigator.clipboard
                              ?.writeText(setupKey.command)
                              .then(() => setCopied(true))
                              .catch(() => undefined)
                          }
                        >
                          {copied ? `${t("copied")} ✓` : t("copy")}
                        </Button>
                        <Button type="button" size="sm" variant="ghost" onClick={getSetupKey}>
                          {t("new key")}
                        </Button>
                        <span className="min-w-0 text-xs break-all text-muted-foreground">{setupKey.fingerprint}</span>
                      </div>
                    </div>
                  )}
                  {sudoField}
                  <div className="text-xs text-warn">
                    {t(
                      "The panel public key goes to authorized_keys; after setup only the srvmon user keeps it and it is removed from {user}. The private part never leaves the monitoring server.",
                      { user: user || "root" },
                    )}
                  </div>
                </>
              )}
            </fieldset>
          </Panel>

          <Panel title={`4 · ${t("key and agent")}`} accent={stepOn(4)}>
            <div className="flex flex-col gap-1.5">
              <div>
                <span className="text-muted-foreground">{t("key")} </span> {t("ed25519, generated on the monitoring server")}
              </div>
              <div>
                <span className="text-muted-foreground">{t("user")} </span> srvmon ({t("docker group")})
              </div>
              <div className="break-all">
                <span className="text-muted-foreground">host </span>
                {probe ? (
                  <span className="text-hi">
                    {probe.type} {probe.fingerprint}
                  </span>
                ) : (
                  <span className="text-muted-foreground">{t("not checked")}</span>
                )}
              </div>
              {probe && (
                <div className="mb-1">
                  <Check checked={confirmed} onChange={setConfirmed} disabled={locked}>
                    {t("the fingerprint matches the server")}{" "}
                    <span className="text-xs text-muted-foreground">(ssh-keygen -lf /etc/ssh/ssh_host_*_key.pub)</span>
                  </Check>
                </div>
              )}
              <div className="mt-1.5 flex flex-col gap-1.5">
                <Check checked={installAgent} onChange={setInstallAgent} disabled={locked}>
                  {t("install srvmon-agent")}
                </Check>
                <Check checked={sudo} onChange={setSudo} disabled={locked}>
                  {t("passwordless sudo for srvmon")} <span className="text-xs text-muted-foreground">({t("root in the terminal")})</span>
                </Check>
                <Check checked={disablePassword} onChange={setDisablePassword} disabled={locked}>
                  {t("turn off password sign-in")}
                </Check>
              </div>
            </div>
          </Panel>
        </section>

        <Panel title={t("setup log")} accent={phase !== "form"}>
          <ScrollArea className="max-h-[320px] border border-border bg-field">
            <div className="px-3 py-2.5 leading-normal">
              {lines.length === 0 && phase === "form" && (
                <div className="text-muted-foreground">
                  {probe
                    ? `○ ${t("check the fingerprint and press “add”")}`
                    : `○ ${t("fill in the name and address, then “check host key”")}`}
                </div>
              )}
              {lines.map((l, i) => (
                <div key={i} className="whitespace-pre-wrap">
                  <span className={MARK[l.state].c}>{MARK[l.state].m}</span> {l.text}
                </div>
              ))}
              {phase === "running" && <div className="text-muted-foreground">○ {t("running…")}</div>}
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
                <Button type="button" variant="outline" onClick={() => navigate(href.overview())}>
                  ← {t("back")}
                </Button>
                <Button type="button" variant="outline" onClick={doProbe} disabled={probing || !hostPort}>
                  {probing ? t("checking…") : probe ? t("check again") : t("check host key")}
                </Button>
                <span className="flex-1" />
                <Button type="submit" disabled={probing || (!!probe && (!confirmed || !loginReady || !card.name))}>
                  {probe ? (existing ? t("connect") : t("add server")) : t("next")} ⏎
                </Button>
              </>
            )}
            {phase === "running" && !committed && (
              <Button type="button" variant="destructive" onClick={() => api.cancelOnboard(job).catch(() => undefined)}>
                {t("cancel (roll back)")}
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
                  ← {t("fix and retry")}
                </Button>
                <span className="flex-1" />
                <Button type="button" variant="outline" onClick={() => navigate(href.overview())}>
                  {t("to the overview")}
                </Button>
              </>
            )}
            {phase === "done" && (
              <>
                <Button type="button" variant="outline" onClick={() => navigate(href.overview())}>
                  {t("to the overview")}
                </Button>
                <span className="flex-1" />
                <Button type="button" onClick={() => navigate(href.terminal(serverId))}>
                  {t("open the terminal")} →
                </Button>
              </>
            )}
          </div>
        </Panel>
      </form>
    </Shell>
  );
}
