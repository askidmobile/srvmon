import { useState } from "react";

import { Button } from "@/components/ui/button";
import { api, type AgentToken as Token, type ServerView } from "@/lib/api";
import { t } from "@/lib/i18n";

// AgentToken reissues the server's agent token. The panel keeps only a hash,
// so the token is shown once, with commands for a manual install. The old
// token keeps working until an agent first pushes with the new one.
export function AgentToken({ s }: { s: ServerView }) {
  const [busy, setBusy] = useState(false);
  const [tok, setTok] = useState<Token | null>(null);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState("");
  const issue = (write: boolean) => {
    const msg = s.has_agent
      ? t("Issue a new agent token? The current agent keeps working until an agent pushes with the new token, then the old one is revoked.")
      : t("Issue an agent token for a manual install?");
    if (!window.confirm(msg)) return;
    setBusy(true);
    setError("");
    api
      .agentToken(s.id, write)
      .then(setTok)
      .catch(() => setError(t("could not issue a token")))
      .finally(() => setBusy(false));
  };
  const copy = (key: string, text: string) => {
    void navigator.clipboard?.writeText(text).then(() => {
      setCopied(key);
      setTimeout(() => setCopied(""), 1500);
    });
  };
  if (!tok) {
    return (
      <span className="inline-flex flex-wrap items-center gap-2">
        <span className="text-muted-foreground">{t("stored as a hash, not shown")}</span>
        {s.has_ssh && s.has_agent && (
          <Button size="sm" variant="outline" disabled={busy} onClick={() => issue(true)}>
            {t("reissue on the server")}
          </Button>
        )}
        <Button size="sm" variant="outline" disabled={busy} onClick={() => issue(false)}>
          {s.has_agent ? t("new token for a manual install") : t("token for a manual install")}
        </Button>
        {error && <span className="text-crit">{error}</span>}
      </span>
    );
  }
  const block = (key: string, title: string, text: string) => (
    <div className="flex flex-col gap-1">
      <div className="flex items-center gap-2">
        <span className="text-muted-foreground">{title}</span>
        <Button size="sm" variant="outline" onClick={() => copy(key, text)}>
          {copied === key ? t("copied") : t("copy")}
        </Button>
      </div>
      <pre className="overflow-x-auto border border-border bg-field px-2 py-1 text-xs whitespace-pre">{text}</pre>
    </div>
  );
  return (
    <div className="flex w-full flex-col gap-2 py-1">
      {tok.written && <span className="text-primary">{t("written to the server: the agent switches to it within a minute")}</span>}
      {tok.write_error && <span className="text-crit">{t("not written to the server: {error}", { error: tok.write_error })}</span>}
      <span className="text-warn">{t("The token is shown once. Without docker (Proxmox VE) run the first command as root; in the panel terminal (user srvmon) put sudo before SRVMON_TOKEN.")}</span>
      {block("token", t("token"), tok.token)}
      {block("systemd", t("systemd service, no docker"), tok.systemd)}
      {block("docker", "docker", tok.docker)}
      <div>
        <Button size="sm" variant="outline" onClick={() => setTok(null)}>
          {t("hide")}
        </Button>
      </div>
    </div>
  );
}
