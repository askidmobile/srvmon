import { useCallback, useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { ApiError, readBody } from "@/lib/api";
import { t } from "@/lib/i18n";
import { cn } from "@/lib/utils";

// MCP access tokens: Claude Desktop, Cursor and Claude Code reach the agent
// tools at /mcp with one of these. The secret is shown once, on creation.

type Scope = "read" | "exec" | "tasks";

interface McpToken {
  id: number;
  name: string;
  prefix: string;
  scopes: Scope[];
  created_at: string;
  expires_at?: string;
  last_used_at?: string;
  last_ip?: string;
}

interface McpTokens {
  endpoint: string;
  scopes: Scope[];
  tokens: McpToken[];
}

interface Created {
  token: McpToken;
  secret: string;
  endpoint: string;
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const data = await readBody(res);
  if (!res.ok) {
    const d = data as { error?: string; field?: string };
    throw new ApiError(res.status, d.field ?? d.error ?? "error");
  }
  return data as T;
}

const scopeText: Record<Scope, string> = {
  read: "servers, metrics, alerts and tasks",
  exec: "commands: reads run at once, changes wait for your confirmation",
  tasks: "create and delete scheduled agent tasks",
};

const periods = [
  { days: 0, name: "no expiry" },
  { days: 30, name: "30 days" },
  { days: 90, name: "90 days" },
  { days: 365, name: "1 year" },
];

const fieldCls =
  "h-9 w-full min-w-0 border border-input bg-field px-2.5 text-hi placeholder:text-muted-foreground focus-visible:outline-1 focus-visible:outline-ring";

function fmt(d?: string) {
  return d ? new Date(d).toLocaleString() : "";
}

function Snippet({ title, text }: { title: string; text: string }) {
  const [copied, setCopied] = useState(false);
  const copy = () => {
    void navigator.clipboard?.writeText(text).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        {title}
        <span className="flex-1" />
        <button type="button" className="hover:text-primary" onClick={copy}>
          {copied ? t("copied") : t("copy")}
        </button>
      </div>
      <pre className="overflow-x-auto border border-border/60 bg-field px-2 py-1 text-xs text-hi [scrollbar-width:thin]">{text}</pre>
    </div>
  );
}

function configs(endpoint: string, secret: string) {
  const auth = `Authorization: Bearer ${secret}`;
  const cursor = JSON.stringify({ mcpServers: { srvmon: { url: endpoint, headers: { Authorization: `Bearer ${secret}` } } } }, null, 2);
  const desktop = JSON.stringify(
    {
      mcpServers: {
        srvmon: {
          command: "npx",
          args: ["-y", "mcp-remote@latest", endpoint, "--header", "Authorization:${SRVMON_MCP_AUTH}"],
          env: { SRVMON_MCP_AUTH: `Bearer ${secret}` },
        },
      },
    },
    null,
    2,
  );
  return [
    { title: "Claude Code", text: `claude mcp add --transport http srvmon ${endpoint} --header "${auth}"` },
    { title: "Cursor · ~/.cursor/mcp.json", text: cursor },
    { title: "Claude Desktop · claude_desktop_config.json", text: desktop },
  ];
}

export function McpPanel() {
  const [data, setData] = useState<McpTokens | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<Scope[]>(["read"]);
  const [days, setDays] = useState(90);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [created, setCreated] = useState<Created | null>(null);

  const load = useCallback(() => {
    req<McpTokens>("GET", "/api/mcp/tokens")
      .then((v) => {
        v.tokens ??= [];
        setData(v);
      })
      .catch(() => setLoadErr(t("could not load the tokens")));
  }, []);
  useEffect(load, [load]);

  const toggle = (s: Scope) => setScopes((p) => (p.includes(s) ? p.filter((x) => x !== s) : [...p, s]));

  const create = async () => {
    setErr("");
    if (!name.trim()) return setErr(t("give the token a name"));
    if (!scopes.length) return setErr(t("pick at least one permission"));
    setBusy(true);
    try {
      setCreated(await req<Created>("POST", "/api/mcp/tokens", { name: name.trim(), scopes, days }));
      setName("");
      load();
    } catch (e) {
      setErr(e instanceof ApiError && e.code === "too_many_tokens" ? t("too many tokens, revoke unused ones") : t("could not create the token"));
    }
    setBusy(false);
  };

  const revoke = async (tok: McpToken) => {
    if (!confirm(t("Revoke the token “{name}”? Clients that use it lose access at once.", { name: tok.name }))) return;
    try {
      await req("DELETE", `/api/mcp/tokens/${tok.id}`);
      if (created?.token.id === tok.id) setCreated(null);
    } catch {
      setErr(t("could not revoke the token"));
    }
    load();
  };

  if (loadErr) return <Panel title="MCP">{<div className="text-crit">{loadErr}</div>}</Panel>;
  if (!data) return <Panel title="MCP">{t("loading…")}</Panel>;

  return (
    <Panel title={t("MCP access")} right={data.endpoint}>
      <div className="flex flex-col gap-3">
        <div className="text-xs text-muted-foreground">
          {t(
            "Connect Claude Desktop, Cursor or Claude Code to srvmon: the same tools and the same confirmation rules as the built-in agent. Each client gets its own token that you can revoke here.",
          )}
        </div>

        {created && (
          <div className="flex flex-col gap-2 border border-primary/70 bg-primary/5 p-2">
            <div className="text-sm text-primary">
              ✓ {t("Token “{name}” created. Copy it now: it is not shown again.", { name: created.token.name })}
            </div>
            <Snippet title={t("token")} text={created.secret} />
            {configs(created.endpoint, created.secret).map((c) => (
              <Snippet key={c.title} title={c.title} text={c.text} />
            ))}
            <div>
              <Button size="sm" variant="ghost" onClick={() => setCreated(null)}>
                {t("hide")}
              </Button>
            </div>
          </div>
        )}

        <div className="grid grid-cols-1 gap-3 md:grid-cols-[1fr_auto]">
          <input
            className={fieldCls}
            placeholder={t("token name, e.g. laptop / cursor")}
            value={name}
            maxLength={64}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && (e.preventDefault(), void create())}
            aria-label={t("token name")}
          />
          <select className={cn(fieldCls, "md:w-40")} value={days} onChange={(e) => setDays(Number(e.target.value))} aria-label={t("expires")}>
            {periods.map((p) => (
              <option key={p.days} value={p.days}>
                {t(p.name)}
              </option>
            ))}
          </select>
        </div>
        <div className="flex flex-col gap-1">
          {data.scopes.map((s) => (
            <label key={s} className="flex items-start gap-2 text-sm">
              <input type="checkbox" className="mt-1" checked={scopes.includes(s)} onChange={() => toggle(s)} />
              <span>
                <span className="text-hi">{s}</span> <span className="text-muted-foreground">· {t(scopeText[s])}</span>
              </span>
            </label>
          ))}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button disabled={busy} onClick={() => void create()}>
            {busy ? t("creating…") : t("create token")}
          </Button>
          {err && <span className="text-sm text-crit">{err}</span>}
        </div>

        {data.tokens.length > 0 && (
          <div className="overflow-x-auto border-t border-border/60 pt-2 [scrollbar-width:thin]">
            <table className="w-full border-collapse text-left text-sm">
              <thead className="text-xs text-muted-foreground">
                <tr>
                  <th className="py-1 pr-3 font-normal">{t("name")}</th>
                  <th className="py-1 pr-3 font-normal">{t("permissions")}</th>
                  <th className="py-1 pr-3 font-normal">{t("expires")}</th>
                  <th className="py-1 pr-3 font-normal">{t("last used")}</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {data.tokens.map((tok) => {
                  const expired = !!tok.expires_at && new Date(tok.expires_at) < new Date();
                  return (
                    <tr key={tok.id} className={cn("border-t border-border/40", expired && "opacity-60")}>
                      <td className="py-1 pr-3">
                        <span className="text-hi">{tok.name}</span> <span className="text-xs text-muted-foreground">{tok.prefix}…</span>
                      </td>
                      <td className="py-1 pr-3 text-xs">{tok.scopes.join(", ")}</td>
                      <td className={cn("py-1 pr-3 text-xs whitespace-nowrap", expired ? "text-crit" : "text-muted-foreground")}>
                        {tok.expires_at ? (expired ? t("expired") : new Date(tok.expires_at).toLocaleDateString()) : t("never")}
                      </td>
                      <td className="py-1 pr-3 text-xs whitespace-nowrap text-muted-foreground">
                        {tok.last_used_at ? `${fmt(tok.last_used_at)}${tok.last_ip ? ` · ${tok.last_ip}` : ""}` : t("not used yet")}
                      </td>
                      <td className="py-1 text-right">
                        <button type="button" className="text-xs text-crit hover:underline" onClick={() => void revoke(tok)}>
                          {t("revoke")}
                        </button>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </Panel>
  );
}
