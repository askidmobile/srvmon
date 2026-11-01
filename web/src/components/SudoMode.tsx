import { useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import { api, errorMessage, type SudoMode as Mode, type SudoState } from "@/lib/api";
import { t } from "@/lib/i18n";

export const SUDO_MODES: { v: Mode; l: string; hint: string }[] = [
  { v: "none", l: "no sudo", hint: "the terminal and the AI agent work without root" },
  { v: "limited", l: "limited sudo", hint: "systemctl status/restart/…, journalctl, service, docker" },
  { v: "full", l: "full sudo", hint: "root in the terminal (sudo -i)" },
];

export function sudoLabel(m: SudoState["mode"]) {
  return m === "custom" ? t("custom rules") : t(SUDO_MODES.find((x) => x.v === m)?.l ?? m);
}

// SudoPicker chooses a sudo mode (add-server wizard, server passport).
export function SudoPicker({
  value,
  onChange,
  disabled,
}: {
  value: Mode | "custom" | null;
  onChange: (m: Mode) => void;
  disabled?: boolean;
}) {
  return (
    <div className="flex flex-wrap gap-1" role="radiogroup" aria-label={t("sudo for srvmon")}>
      {SUDO_MODES.map((m) => (
        <Button
          key={m.v}
          type="button"
          size="sm"
          role="radio"
          aria-checked={value === m.v}
          variant={value === m.v ? "default" : "outline"}
          disabled={disabled}
          title={t(m.hint)}
          onClick={() => onChange(m.v)}
        >
          {t(m.l)}
        </Button>
      ))}
    </div>
  );
}

// SudoMode shows the sudo of the panel's SSH user on a server, read live
// over SSH, and changes it.
export function SudoMode({ id }: { id: number }) {
  const [state, setState] = useState<SudoState | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    let live = true;
    api
      .sudo(id)
      .then((s) => live && setState(s))
      .catch((e) => live && setError(errorMessage(e, t("could not check sudo"))));
    return () => {
      live = false;
    };
  }, [id]);
  const change = (mode: Mode) => {
    if (!state || mode === state.mode || busy) return;
    if (state.mode === "full" && !confirm(t("Narrow sudo? sudo -i in the terminal will stop working; you can switch back here."))) return;
    setBusy(true);
    setError("");
    api
      .setSudo(id, mode)
      .then(setState)
      .catch((e) => setError(errorMessage(e, t("could not change sudo"))))
      .finally(() => setBusy(false));
  };
  if (!state) {
    return <span className={error ? "text-crit" : "text-muted-foreground"}>{error || t("checking…")}</span>;
  }
  return (
    <span className="inline-flex flex-col gap-1">
      <span>
        <span className={state.mode === "full" ? "text-warn" : undefined}>{sudoLabel(state.mode)}</span>
        <span className="text-muted-foreground"> · {state.user}</span>
        {busy && <span className="text-warn"> · {t("applying…")}</span>}
      </span>
      <SudoPicker value={state.mode} onChange={change} disabled={busy} />
      {error && <span className="text-crit">{error}</span>}
    </span>
  );
}
