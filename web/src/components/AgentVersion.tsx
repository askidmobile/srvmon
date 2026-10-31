import { useState } from "react";

import { Button } from "@/components/ui/button";
import { api, type ServerView } from "@/lib/api";
import { t } from "@/lib/i18n";

// Release builds report the full commit sha; show it like the image tag.
export function shortVersion(v: string | undefined) {
  if (!v) return "?";
  return /^[0-9a-f]{40}$/.test(v) ? v.slice(0, 7) : v;
}

// needsAgentUpdate: the panel can replace this server's agent with its own.
export function needsAgentUpdate(s: ServerView) {
  return !!s.agent_outdated && s.has_ssh && s.agent_update?.state !== "running";
}

// AgentVersion is the agent's version with its update state and, when the
// agent is outdated and the panel has SSH access, an update button.
export function AgentVersion({ s, onStarted }: { s: ServerView; onStarted?: () => void }) {
  const [error, setError] = useState("");
  const r = s.snapshot?.report;
  const u = s.agent_update;
  const start = () => {
    setError("");
    api
      .updateAgent(s.id)
      .then(() => onStarted?.())
      .catch((e) => setError(e?.code === "update_running" ? t("already updating") : t("could not start the update")));
  };
  return (
    <span className="inline-flex flex-wrap items-center gap-x-2">
      <span>{r ? shortVersion(r.agent_version) : t("not connected")}</span>
      {s.agent_outdated && u?.state !== "running" && (
        <span className="text-warn" title={t("the panel runs {v}", { v: shortVersion(s.agent_latest) })}>
          {t("outdated")}
        </span>
      )}
      {u?.state === "running" && <span className="text-warn">{t("updating…")}</span>}
      {u?.state === "ok" && !s.agent_outdated && <span className="text-primary">{t("updated")}</span>}
      {u?.state === "failed" && <span className="text-crit">{t("update failed: {error}", { error: u.error ?? "" })}</span>}
      {needsAgentUpdate(s) && (
        <Button size="sm" variant="outline" onClick={start}>
          {t("update agent")}
        </Button>
      )}
      {s.agent_outdated && !s.has_ssh && <span className="text-muted-foreground">{t("no ssh access: update it on the server")}</span>}
      {error && <span className="text-crit">{error}</span>}
    </span>
  );
}
