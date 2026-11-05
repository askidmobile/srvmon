import { useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import { Check } from "@/components/ui/check";
import { Panel } from "@/components/ui/panel";
import { ApiError } from "@/lib/api";
import { t } from "@/lib/i18n";
import {
  cancelled,
  deviceName,
  hasPlatformAuthenticator,
  isIPHost,
  passkeyApi,
  passkeyProblem,
  type CreationJSON,
  type PasskeyList,
  type PasskeyView,
} from "@/lib/passkey";
import { cn } from "@/lib/utils";

const fieldCls =
  "h-8 w-full min-w-0 border border-input bg-field px-2.5 text-hi placeholder:text-muted-foreground focus-visible:outline-1 focus-visible:outline-ring disabled:opacity-60";

function problemText(code: string): string {
  switch (code) {
    case "passkey_ip":
      return t(
        "The panel is opened by IP address. Passkeys are bound to a domain name and browsers do not allow them on an IP address. Open the panel by its domain over HTTPS to use them; the password and the app code work everywhere.",
      );
    case "passkey_insecure":
      return t(
        "The panel is opened over plain HTTP. Browsers allow passkeys only over HTTPS (or on localhost). Put the panel behind HTTPS on a domain to use them.",
      );
    case "passkey_unsupported":
      return t("This browser does not support passkeys.");
    case "passkey_bad_domain":
      return t("This address cannot be used for passkeys: open the panel by a full domain name.");
    case "passkey_no_origin":
      return t("The browser did not say which address the panel is opened at: passkeys cannot be checked.");
    default:
      return "";
  }
}

function errText(e: unknown): string {
  if (e instanceof ApiError) {
    const p = problemText(e.code);
    if (p) return p;
    switch (e.code) {
      case "invalid_code":
        return t("wrong code");
      case "too_many_attempts":
        return t("too many attempts, try later");
      case "invalid_passkey":
        return t("the passkey was not accepted");
      case "passkey_expired":
        return t("the passkey prompt timed out, try again");
      case "passkey_exists":
        return t("this passkey is already added");
      case "too_many_passkeys":
        return t("too many passkeys: remove one first");
      case "bad_origin":
        return t("the panel address changed during the prompt, try again");
    }
  }
  if (e instanceof DOMException && e.name === "InvalidStateError") return t("this passkey is already added");
  if (e instanceof DOMException) return e.message;
  return t("failed");
}

function when(s: string | null) {
  return s ? new Date(s).toLocaleString() : t("never");
}

// Passkeys: optional sign-in with Face ID, Touch ID, Windows Hello, a phone
// or a security key. Off until the owner checks the address and adds one.
export function PasskeyPanel() {
  const problem = passkeyProblem();
  const [list, setList] = useState<PasskeyList | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [platform, setPlatform] = useState<boolean | null>(null);
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState(deviceName());
  const [code, setCode] = useState("");
  const [agree, setAgree] = useState(false);
  // Prompt options issued after the code passed: the next click opens the
  // browser prompt directly.
  const [options, setOptions] = useState<CreationJSON | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);

  const load = () =>
    passkeyApi
      .list()
      .then((l) => setList({ ...l, passkeys: l.passkeys ?? [] }))
      .catch(() => setLoadErr(t("could not load")));
  useEffect(() => {
    void load();
    void hasPlatformAuthenticator().then(setPlatform);
  }, []);

  const host = location.hostname;
  const keys = list?.passkeys ?? [];
  const here = keys.filter((k) => k.here);

  const reset = () => {
    setAdding(false);
    setCode("");
    setAgree(false);
    setOptions(null);
  };

  // Step 1: the code from the authenticator app confirms it is the owner.
  const confirmCode = async (c: string) => {
    setBusy(true);
    setMsg(null);
    try {
      setOptions(await passkeyApi.addBegin(c.replace(/\s/g, ""), name.trim()));
    } catch (e) {
      setCode("");
      setMsg({ ok: false, text: errText(e) });
    }
    setBusy(false);
  };

  // Step 2: the browser prompt, opened right in the click.
  const add = async () => {
    if (!options) return;
    setBusy(true);
    setMsg(null);
    try {
      await passkeyApi.addFinish(options);
      reset();
      setMsg({ ok: true, text: t("passkey added: the sign-in page now offers it at {host}", { host }) });
      await load();
    } catch (e) {
      setOptions(null);
      setCode("");
      setMsg(
        cancelled(e)
          ? { ok: false, text: t("cancelled; enter a new code from the authenticator app to try again") }
          : { ok: false, text: errText(e) },
      );
    }
    setBusy(false);
  };

  // ✓ met, ✗ blocks passkeys, · only informs.
  const checks: { mark: "✓" | "✗" | "·"; text: string }[] = [
    { mark: isIPHost(host) ? "✗" : "✓", text: t("opened by a domain name, not an IP address ({host})", { host }) },
    { mark: window.isSecureContext ? "✓" : "✗", text: t("HTTPS (or localhost)") },
    { mark: window.PublicKeyCredential ? "✓" : "✗", text: t("the browser supports passkeys") },
    platform
      ? { mark: "✓", text: t("this device has Face ID / Touch ID / Windows Hello") }
      : { mark: "·", text: t("no built-in Face ID / Touch ID / Windows Hello here: a phone or a security key can still be used") },
  ];
  const markCls = { "✓": "text-primary", "✗": "text-crit", "·": "text-muted-foreground" };

  return (
    <Panel title={t("passkeys · face id / touch id")} right={here.length ? t("on at this address") : t("off")}>
      <div className="flex flex-col gap-3 text-sm">
        <div className="text-muted-foreground">
          {t(
            "Optional. Sign in with Face ID, Touch ID, Windows Hello, a phone or a security key instead of the password and the app code. The password and the app code keep working as a fallback.",
          )}
        </div>

        <div className="flex flex-col gap-0.5" aria-label={t("check of this address")}>
          {checks.map((c) => (
            <div key={c.text} className={markCls[c.mark]}>
              {c.mark} {c.text}
            </div>
          ))}
        </div>
        {problem && <div className="text-warn">{problemText(problem)}</div>}

        {loadErr && <div className="text-crit">{loadErr}</div>}
        {keys.length > 0 && (
          <div className="flex flex-col gap-1.5 border-t border-border/60 pt-3">
            {keys.map((k) => (
              <PasskeyRow key={k.id} k={k} onRemoved={() => void load()} />
            ))}
          </div>
        )}

        {!problem &&
          (adding ? (
            <div className="flex flex-col gap-2.5 border-t border-border/60 pt-3">
              <label className="flex flex-col gap-1">
                <span className="text-muted-foreground">{t("passkey name (to tell your devices apart)")}</span>
                <input
                  className={cn(fieldCls, "max-w-72")}
                  maxLength={64}
                  disabled={!!options}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
              </label>
              <Check checked={agree} disabled={!!options} onChange={setAgree}>
                {t(
                  "I understand: the passkey works only at {host}. If the panel moves to another address, I add a new passkey there; the password and the app code stay as a fallback.",
                  { host },
                )}
              </Check>
              {!options ? (
                <div className="flex flex-col gap-1">
                  <span className="text-muted-foreground">
                    {t("1. Confirm it is you: the current 6-digit code from Google Authenticator (the app you sign in with)")}
                  </span>
                  <div className="flex flex-wrap items-center gap-2">
                    <input
                      className={cn(fieldCls, "max-w-48 tracking-[4px]")}
                      placeholder="123456"
                      inputMode="numeric"
                      autoComplete="one-time-code"
                      disabled={!agree || busy}
                      value={code}
                      onChange={(e) => {
                        const v = e.target.value;
                        setCode(v);
                        // Six digits are a whole TOTP code: no extra click.
                        if (/^\d{6}$/.test(v.replace(/\s/g, ""))) void confirmCode(v);
                      }}
                      onKeyDown={(e) => e.key === "Enter" && code.trim() && void confirmCode(code)}
                      aria-label={t("code from Google Authenticator")}
                    />
                    <Button size="sm" variant="outline" disabled={busy || !agree || !code.trim()} onClick={() => void confirmCode(code)}>
                      {t("confirm code")}
                    </Button>
                    <Button size="sm" variant="ghost" disabled={busy} onClick={reset}>
                      {t("cancel")}
                    </Button>
                  </div>
                  {!agree && <span className="text-xs text-muted-foreground">{t("tick the box above first")}</span>}
                </div>
              ) : (
                <div className="flex flex-col gap-1">
                  <span className="text-primary">✓ {t("code confirmed")}</span>
                  <span className="text-muted-foreground">
                    {t("2. Press the button: the browser asks for Touch ID, Face ID or the device PIN")}
                  </span>
                  <div className="flex flex-wrap items-center gap-2">
                    <Button size="sm" disabled={busy} onClick={() => void add()}>
                      {t("add passkey · Touch ID")}
                    </Button>
                    <Button size="sm" variant="ghost" disabled={busy} onClick={reset}>
                      {t("cancel")}
                    </Button>
                  </div>
                </div>
              )}
            </div>
          ) : (
            list &&
            keys.length < list.max && (
              <div>
                <Button size="sm" variant="outline" onClick={() => (setAdding(true), setMsg(null))}>
                  {here.length ? t("add another passkey") : t("turn on: add a passkey")}
                </Button>
              </div>
            )
          ))}
        {msg && <div className={msg.ok ? "text-primary" : "text-crit"}>{msg.text}</div>}
      </div>
    </Panel>
  );
}

function PasskeyRow({ k, onRemoved }: { k: PasskeyView; onRemoved: () => void }) {
  const [removing, setRemoving] = useState(false);
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const remove = async () => {
    setBusy(true);
    setErr("");
    try {
      await passkeyApi.remove(k.id, code.replace(/\s/g, ""));
      onRemoved();
    } catch (e) {
      setCode("");
      setErr(errText(e));
    }
    setBusy(false);
  };

  return (
    <div className="flex flex-col gap-1">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="text-hi">{k.name}</span>
        <span className={k.here ? "text-muted-foreground" : "text-warn"}>
          {k.rp_id}
          {!k.here && ` · ${t("another address: not offered here")}`}
        </span>
        <span className="text-xs text-muted-foreground">
          {t("added {when}", { when: when(k.created_at) })} · {t("last used {when}", { when: when(k.last_used_at) })}
        </span>
        {!removing && (
          <Button size="sm" variant="ghost" className="h-6 px-1 text-crit" onClick={() => setRemoving(true)}>
            {t("remove")}
          </Button>
        )}
      </div>
      {removing && (
        <div className="flex flex-wrap items-center gap-2">
          <input
            className={cn(fieldCls, "max-w-48")}
            placeholder={t("code from Google Authenticator")}
            inputMode="numeric"
            autoComplete="one-time-code"
            autoFocus
            value={code}
            onChange={(e) => setCode(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && code.trim() && void remove()}
            aria-label={t("code from Google Authenticator")}
          />
          <Button size="sm" variant="outline" className="text-crit" disabled={busy || !code.trim()} onClick={() => void remove()}>
            {t("remove passkey")}
          </Button>
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => (setRemoving(false), setErr(""))}>
            {t("cancel")}
          </Button>
          {err && <span className="text-crit">{err}</span>}
        </div>
      )}
    </div>
  );
}
