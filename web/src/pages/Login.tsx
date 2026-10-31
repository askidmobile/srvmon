import { useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Panel } from "@/components/ui/panel";
import { api, ApiError } from "@/lib/api";
import { t } from "@/lib/i18n";

const LOGO = String.raw`  ___ _ ____   ___ __ ___   ___  _ __
 / __| '__\ \ / / '_ ${"`"} _ \ / _ \| '_ \
 \__ \ |   \ V /| | | | | | (_) | | | |
 |___/_|    \_/ |_| |_| |_|\___/|_| |_|`;

function errorText(e: unknown): string {
  if (!(e instanceof ApiError)) return t("server unavailable");
  switch (e.code) {
    case "invalid_password":
      return t("wrong password");
    case "invalid_code":
      return t("wrong code");
    case "password_required":
      return t("this step timed out, enter the password again");
    case "too_many_attempts":
      return t("too many attempts, try again in {m} min", { m: Math.ceil((e.retryAfter ?? 600) / 60) });
    case "not_initialized":
      return t("the panel is not set up: run srvmon-server init on the server");
    default:
      return t("error: {code}", { code: e.code });
  }
}

function secondsLeft() {
  return 30 - (Math.floor(Date.now() / 1000) % 30);
}

export function Login({ onSuccess }: { onSuccess: () => void }) {
  const [step, setStep] = useState<"password" | "totp">("password");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [left, setLeft] = useState(secondsLeft());
  const codeRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const t = setInterval(() => setLeft(secondsLeft()), 1000);
    return () => clearInterval(t);
  }, []);
  useEffect(() => {
    if (step === "totp") codeRef.current?.focus();
  }, [step]);

  async function submitPassword(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.password(password);
      setPassword("");
      setStep("totp");
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  async function submitCode(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.totp(code.replace(/\s/g, ""));
      onSuccess();
    } catch (err) {
      setCode("");
      if (err instanceof ApiError && err.code === "password_required") setStep("password");
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="flex min-h-screen items-center justify-center p-6">
      <div className="flex w-full max-w-[480px] flex-col items-center gap-7">
        <pre aria-hidden="true" className="m-0 text-xs leading-tight text-primary">
          {LOGO}
        </pre>

        {step === "password" ? (
          <Panel title={t("sign in ── step 1/2")} accent className="w-full">
            <form onSubmit={submitPassword} className="flex flex-col gap-3.5">
              <label htmlFor="pw" className="text-muted-foreground">
                {t("password")}
              </label>
              <Input
                id="pw"
                type="password"
                autoComplete="current-password"
                autoFocus
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
              <Button type="submit" size="lg" disabled={busy || !password}>
                {t("continue")} ⏎
              </Button>
              {error && <p className="m-0 text-crit">{error}</p>}
              <p className="m-0 text-xs text-muted-foreground">
                {t("no username · 5 wrong attempts → ip banned for 1h (fail2ban)")}
              </p>
            </form>
          </Panel>
        ) : (
          <Panel title={t("sign in ── step 2/2 · google authenticator")} accent className="w-full">
            <form onSubmit={submitCode} className="flex flex-col gap-3.5">
              <label htmlFor="code" className="text-muted-foreground">
                {t("6-digit code from the app or a recovery code")}
              </label>
              <Input
                id="code"
                ref={codeRef}
                inputMode="numeric"
                autoComplete="one-time-code"
                className="text-center text-2xl tracking-[12px]"
                value={code}
                onChange={(e) => setCode(e.target.value)}
              />
              <Button type="submit" size="lg" disabled={busy || !code}>
                {t("sign in")} ⏎
              </Button>
              {error && <p className="m-0 text-crit">{error}</p>}
              <div className="flex flex-wrap justify-between gap-2">
                <Button
                  type="button"
                  variant="ghost"
                  className="px-0"
                  onClick={() => {
                    setStep("password");
                    setError("");
                  }}
                >
                  ← {t("back")}
                </Button>
                <span className="self-center text-xs text-muted-foreground">{t("code refreshes in {s}s", { s: left })}</span>
              </div>
            </form>
          </Panel>
        )}

        <p className="m-0 text-center text-xs text-muted-foreground">
          {t("tls · sign-in attempts are logged · owner access only")}
        </p>
      </div>
    </main>
  );
}
