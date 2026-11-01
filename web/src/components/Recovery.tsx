import { type ReactNode, useCallback, useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { ApiError } from "@/lib/api";
import { type BackupConfig, type BackupObject, type BackupView, type EmergencyView, backupApi } from "@/lib/backup";
import { t } from "@/lib/i18n";
import { cn } from "@/lib/utils";

const fieldCls =
  "h-9 w-full min-w-0 border border-input bg-field px-2.5 text-hi placeholder:text-muted-foreground focus-visible:outline-1 focus-visible:outline-ring disabled:opacity-60";

const fieldNames: Record<string, string> = {
  endpoint: "endpoint",
  bucket: "bucket",
  prefix: "prefix",
  region: "region",
  access_key: "access key ID",
  secret_key: "secret key",
  interval_hours: "every, hours",
  keep: "keep copies",
};

function errText(e: unknown, fallback: string) {
  if (e instanceof ApiError) {
    if (fieldNames[e.code]) return t("check the field “{field}”", { field: t(fieldNames[e.code]) });
    if (e.code === "invalid_code") return t("wrong code");
    if (e.code === "too_many_attempts") return t("too many attempts, try later");
    if (e.code === "busy") return t("a backup is already running");
    if (e.code === "no_master_key") return t("the master key is not loaded");
    if (e.message && e.message !== e.code) return e.message;
  }
  return fallback;
}

function Field({ label, htmlFor, hint, children }: { label: string; htmlFor?: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <label className="text-sm text-muted-foreground" htmlFor={htmlFor}>
        {label}
      </label>
      {children}
      {hint && <div className="text-xs text-muted-foreground">{hint}</div>}
    </div>
  );
}

function when(s?: string) {
  return s ? new Date(s).toLocaleString() : "—";
}

function size(n?: number) {
  if (!n) return "";
  if (n < 1024 * 1024) return `${Math.max(1, Math.round(n / 1024))} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

const emptyConfig: BackupConfig = {
  enabled: true,
  s3: { endpoint: "", region: "", bucket: "", prefix: "srvmon/", access_key: "", path_style: false },
  interval_hours: 24,
  keep: 14,
};

// Backups of the panel database to S3-compatible storage.
export function BackupPanel() {
  const [v, setV] = useState<BackupView | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [c, setC] = useState<BackupConfig>(emptyConfig);
  const [secret, setSecret] = useState("");
  const [busy, setBusy] = useState("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [list, setList] = useState<BackupObject[] | null>(null);
  const [listErr, setListErr] = useState("");

  const fill = useCallback((x: BackupView) => {
    setV(x);
    setC(x.saved ? x.config : { ...emptyConfig, interval_hours: x.config.interval_hours || 24, keep: x.config.keep || 14 });
  }, []);
  useEffect(() => {
    backupApi.get().then(fill, (e) => setLoadErr(errText(e, t("could not load"))));
  }, [fill]);

  const set3 = (k: keyof BackupConfig["s3"], val: string | boolean) => setC((x) => ({ ...x, s3: { ...x.s3, [k]: val } }));

  const save = async () => {
    setBusy("save");
    setMsg(null);
    try {
      fill(await backupApi.save(c, secret.trim() || undefined));
      setSecret("");
      setMsg({ ok: true, text: t("saved") });
    } catch (e) {
      setMsg({ ok: false, text: errText(e, t("could not save")) });
    }
    setBusy("");
  };
  const test = async () => {
    setBusy("test");
    setMsg(null);
    try {
      const r = await backupApi.test(c, secret.trim() || undefined);
      setMsg(r.ok ? { ok: true, text: t("storage works: write, read and delete succeeded") } : { ok: false, text: r.error ?? t("failed") });
    } catch (e) {
      setMsg({ ok: false, text: errText(e, t("failed")) });
    }
    setBusy("");
  };
  const run = async () => {
    setBusy("run");
    setMsg(null);
    try {
      const st = await backupApi.run();
      setV((x) => (x ? { ...x, status: st } : x));
      setMsg(st.error ? { ok: false, text: st.error } : { ok: true, text: t("backup uploaded: {name}", { name: st.object ?? "" }) });
      if (list) void loadList();
    } catch (e) {
      setMsg({ ok: false, text: errText(e, t("failed")) });
    }
    setBusy("");
  };
  const loadList = async () => {
    setListErr("");
    try {
      const r = await backupApi.list();
      setList(r.backups ?? []);
      if (r.error) setListErr(r.error);
    } catch (e) {
      setListErr(errText(e, t("could not load")));
    }
  };

  if (loadErr)
    return (
      <Panel title={t("backup")}>
        <div className="text-crit">{loadErr}</div>
      </Panel>
    );
  if (!v) return <Panel title={t("backup")}>{t("loading…")}</Panel>;
  const st = v.status;
  const right = st.running
    ? t("running…")
    : st.error
      ? `✕ ${t("last attempt failed")}`
      : st.ok_at
        ? `✓ ${when(st.ok_at)}`
        : t("no backups yet");

  return (
    <Panel title={t("backup")} right={right}>
      <div className="flex flex-col gap-4">
        <div className="text-xs text-muted-foreground">
          {t(
            "The panel database (servers, SSH keys, rules, settings, agent chats) is copied on schedule, encrypted with a key derived from the master key and uploaded to S3-compatible storage. The master key is never in a backup: export it below and keep it offline. Metrics history is not backed up.",
          )}
        </div>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={c.enabled} onChange={(e) => setC({ ...c, enabled: e.target.checked })} />
          {t("back up on schedule")}
        </label>
        <div className="grid gap-3 sm:grid-cols-2">
          <Field
            label={t("endpoint")}
            htmlFor="bk-endpoint"
            hint={t("for example storage.yandexcloud.net, s3.eu-central-1.amazonaws.com or https://<account>.r2.cloudflarestorage.com")}
          >
            <input
              id="bk-endpoint"
              className={fieldCls}
              value={c.s3.endpoint}
              onChange={(e) => set3("endpoint", e.target.value)}
              spellCheck={false}
            />
          </Field>
          <Field label={t("region")} htmlFor="bk-region" hint={t("optional; e.g. ru-central1, auto for R2")}>
            <input
              id="bk-region"
              className={fieldCls}
              value={c.s3.region}
              onChange={(e) => set3("region", e.target.value)}
              spellCheck={false}
            />
          </Field>
          <Field label={t("bucket")} htmlFor="bk-bucket">
            <input
              id="bk-bucket"
              className={fieldCls}
              value={c.s3.bucket}
              onChange={(e) => set3("bucket", e.target.value)}
              spellCheck={false}
            />
          </Field>
          <Field label={t("prefix")} htmlFor="bk-prefix">
            <input
              id="bk-prefix"
              className={fieldCls}
              value={c.s3.prefix}
              onChange={(e) => set3("prefix", e.target.value)}
              spellCheck={false}
            />
          </Field>
          <Field label={t("access key ID")} htmlFor="bk-ak">
            <input
              id="bk-ak"
              className={fieldCls}
              value={c.s3.access_key}
              onChange={(e) => set3("access_key", e.target.value)}
              spellCheck={false}
              autoComplete="off"
            />
          </Field>
          <Field label={t("secret key")} htmlFor="bk-sk" hint={v.has_secret ? t("saved encrypted; leave empty to keep it") : undefined}>
            <input
              id="bk-sk"
              type="password"
              className={fieldCls}
              value={secret}
              placeholder={v.has_secret ? "••••••••" : ""}
              onChange={(e) => setSecret(e.target.value)}
              autoComplete="new-password"
            />
          </Field>
          <Field label={t("every, hours")} htmlFor="bk-every">
            <input
              id="bk-every"
              type="number"
              min={1}
              max={168}
              className={fieldCls}
              value={c.interval_hours}
              onChange={(e) => setC({ ...c, interval_hours: Number(e.target.value) })}
            />
          </Field>
          <Field label={t("keep copies")} htmlFor="bk-keep">
            <input
              id="bk-keep"
              type="number"
              min={1}
              max={365}
              className={fieldCls}
              value={c.keep}
              onChange={(e) => setC({ ...c, keep: Number(e.target.value) })}
            />
          </Field>
        </div>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={c.s3.path_style} onChange={(e) => set3("path_style", e.target.checked)} />
          {t("path-style addressing (MinIO and some providers)")}
        </label>

        <div className="flex flex-wrap items-center gap-2 border-t border-border/60 pt-3">
          <Button disabled={!!busy} onClick={() => void save()}>
            {busy === "save" ? t("saving…") : t("save")}
          </Button>
          <Button variant="outline" disabled={!!busy} onClick={() => void test()}>
            {busy === "test" ? t("checking…") : t("test storage")}
          </Button>
          <Button variant="outline" disabled={!!busy || !v.saved || st.running} onClick={() => void run()}>
            {busy === "run" ? t("backing up…") : t("back up now")}
          </Button>
          <Button variant="ghost" disabled={!v.saved} onClick={() => void loadList()}>
            {t("list backups")}
          </Button>
          {msg && <span className={cn("text-sm break-all", msg.ok ? "text-primary" : "text-crit")}>{msg.text}</span>}
        </div>

        <div className="grid gap-1 text-sm sm:grid-cols-[12rem_1fr]">
          <span className="text-muted-foreground">{t("last success")}</span>
          <span className="text-hi">
            {when(st.ok_at)}{" "}
            {st.object && (
              <span className="text-muted-foreground">
                · {st.object} {size(st.size)}
              </span>
            )}
          </span>
          {st.error && (
            <>
              <span className="text-muted-foreground">{t("last attempt")}</span>
              <span className="break-all text-crit">
                {when(st.attempt_at)} · {st.error}
              </span>
            </>
          )}
        </div>
        {list && (
          <div className="flex flex-col gap-1 text-sm">
            {listErr && <div className="text-crit">{listErr}</div>}
            {list.length === 0 && !listErr && <div className="text-muted-foreground">{t("no backups in the bucket")}</div>}
            {list.map((o) => (
              <div key={o.key} className="flex gap-3">
                <span className="text-hi">{o.key}</span>
                <span className="text-muted-foreground">{size(o.size)}</span>
              </div>
            ))}
          </div>
        )}
        <MasterKey exportedAt={v.key_exported_at} onExported={(at) => setV((x) => (x ? { ...x, key_exported_at: at } : x))} />
      </div>
    </Panel>
  );
}

// Export of the master key: needs a fresh code from the authenticator.
function MasterKey({ exportedAt, onExported }: { exportedAt?: string; onExported: (at: string) => void }) {
  const [code, setCode] = useState("");
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [copied, setCopied] = useState(false);

  const exportKey = async () => {
    setBusy(true);
    setErr("");
    try {
      const r = await backupApi.exportKey(code.trim());
      setKey(r.key);
      setCode("");
      onExported(new Date().toISOString());
    } catch (e) {
      setErr(errText(e, t("failed")));
    }
    setBusy(false);
  };
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(key);
      setCopied(true);
    } catch {
      /* clipboard blocked: the key is selectable */
    }
  };

  return (
    <div className={cn("flex flex-col gap-2 border-t border-border/60 pt-3", !exportedAt && "text-warn")}>
      <div className="text-sm">
        {exportedAt
          ? t("master key exported {when}. Keep it offline: without it the backups cannot be decrypted.", { when: when(exportedAt) })
          : `⚠ ${t("The master key has not been exported yet. Save it in a password manager: without it no backup can be restored.")}`}
      </div>
      {key ? (
        <div className="flex flex-col gap-2">
          <code className="border border-border bg-field p-2 text-sm break-all text-hi select-all">{key}</code>
          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" variant="outline" onClick={() => void copy()}>
              {copied ? t("copied") : t("copy")}
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setKey("")}>
              {t("hide")}
            </Button>
          </div>
          <div className="text-xs text-muted-foreground">
            {t("To restore on a new server: srvmon-server master-key -import, then srvmon-server restore. See docs/backup.md.")}
          </div>
        </div>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          <input
            className={cn(fieldCls, "h-8 max-w-48")}
            placeholder={t("code from the app")}
            inputMode="numeric"
            autoComplete="one-time-code"
            value={code}
            onChange={(e) => setCode(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && code.trim() && void exportKey()}
            aria-label={t("code from the app")}
          />
          <Button size="sm" variant="outline" disabled={busy || !code.trim()} onClick={() => void exportKey()}>
            {t("show master key")}
          </Button>
          {err && <span className="text-sm text-crit">{err}</span>}
        </div>
      )}
    </div>
  );
}

const stateCls: Record<string, string> = {
  ok: "text-primary",
  failed: "text-crit",
  pending: "text-warn",
  running: "text-muted-foreground",
  no_ssh: "text-muted-foreground",
};

function stateText(s: string) {
  switch (s) {
    case "ok":
      return t("in place");
    case "failed":
      return t("failed");
    case "running":
      return t("applying…");
    case "no_ssh":
      return t("no SSH key in the panel");
    default:
      return t("not applied");
  }
}

// The owner's own SSH keys, kept by the panel in authorized_keys of its
// account on every server: losing the panel does not lock the servers.
export function EmergencyPanel() {
  const [v, setV] = useState<EmergencyView | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);

  const fill = useCallback((x: EmergencyView) => {
    x.keys ??= [];
    x.servers ??= [];
    setV(x);
    return x;
  }, []);
  useEffect(() => {
    backupApi.emergency().then(
      (x) =>
        setText(
          fill(x)
            .keys.map((k) => k.line)
            .join("\n"),
        ),
      (e) => setLoadErr(errText(e, t("could not load"))),
    );
  }, [fill]);
  // Follow servers that are being updated.
  const pending = v?.servers.some((s) => s.state === "running") ?? false;
  useEffect(() => {
    if (!pending) return;
    const id = setTimeout(() => void backupApi.emergency().then(fill, () => {}), 1500);
    return () => clearTimeout(id);
  }, [pending, v, fill]);

  const save = async () => {
    setBusy(true);
    setMsg(null);
    const keys = text
      .split("\n")
      .map((l) => l.trim())
      .filter((l) => l && !l.startsWith("#"));
    try {
      const x = fill(await backupApi.saveEmergency(keys));
      setText(x.keys.map((k) => k.line).join("\n"));
      setMsg({ ok: true, text: t("saved, applying to servers") });
      setTimeout(() => void backupApi.emergency().then(fill, () => {}), 800);
    } catch (e) {
      setMsg({ ok: false, text: errText(e, t("could not save")) });
    }
    setBusy(false);
  };
  const apply = async () => {
    setBusy(true);
    setMsg(null);
    try {
      fill(await backupApi.applyEmergency());
      setTimeout(() => void backupApi.emergency().then(fill, () => {}), 800);
    } catch (e) {
      setMsg({ ok: false, text: errText(e, t("failed")) });
    }
    setBusy(false);
  };

  if (loadErr)
    return (
      <Panel title={t("emergency access")}>
        <div className="text-crit">{loadErr}</div>
      </Panel>
    );
  if (!v) return <Panel title={t("emergency access")}>{t("loading…")}</Panel>;
  const sshServers = v.servers.filter((s) => s.state !== "no_ssh");
  const okCount = sshServers.filter((s) => s.state === "ok").length;

  return (
    <Panel
      title={t("emergency access")}
      right={v.keys.length ? t("{ok} of {n} servers", { ok: okCount, n: sshServers.length }) : t("no keys")}
    >
      <div className="flex flex-col gap-3">
        <div className="text-xs text-muted-foreground">
          {t(
            "Your own public SSH keys (a laptop, a YubiKey). The panel adds them to authorized_keys of its SSH user on every server, next to the panel key, so you can still get in if the panel or its master key is lost. Only public keys (.pub) — never paste a private key.",
          )}
        </div>
        <textarea
          className={cn(fieldCls, "h-28 py-2 font-mono text-xs")}
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder="ssh-ed25519 AAAAC3Nz... me@laptop"
          spellCheck={false}
          aria-label={t("public keys, one per line")}
        />
        {v.keys.length > 0 && (
          <div className="flex flex-col gap-0.5 text-xs text-muted-foreground">
            {v.keys.map((k) => (
              <div key={k.fingerprint}>
                {k.type} {k.fingerprint} {k.comment && `· ${k.comment}`}
              </div>
            ))}
          </div>
        )}
        <div className="flex flex-wrap items-center gap-2">
          <Button disabled={busy} onClick={() => void save()}>
            {t("save and apply")}
          </Button>
          <Button variant="outline" disabled={busy || sshServers.length === 0} onClick={() => void apply()}>
            {t("apply to all servers again")}
          </Button>
          {msg && <span className={cn("text-sm", msg.ok ? "text-primary" : "text-crit")}>{msg.text}</span>}
        </div>
        {v.servers.length > 0 && (
          <div className="grid gap-x-4 gap-y-0.5 text-sm sm:grid-cols-[minmax(8rem,auto)_1fr]">
            {v.servers.map((s) => (
              <div key={s.id} className="contents">
                <span className="text-hi">{s.name}</span>
                <span className={cn("break-all", stateCls[s.state])}>
                  {stateText(s.state)}
                  {s.error && ` · ${s.error}`}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    </Panel>
  );
}
