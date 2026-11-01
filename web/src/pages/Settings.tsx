import { type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";

import { McpPanel } from "@/components/McpPanel";
import { Hotkey, Shell } from "@/components/Shell";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import {
  agentApi,
  type AIModel,
  type AIProvider,
  type AISettings,
  type AISettingsInput,
  type AITest,
  type DiagnoseSettings,
} from "@/lib/agent";
import { api as panelApi, ApiError } from "@/lib/api";
import { LANGS, type Lang, setLang, t, tn, useLang } from "@/lib/i18n";
import { type Route } from "@/lib/router";
import { cn } from "@/lib/utils";

const fieldCls =
  "h-9 w-full min-w-0 border border-input bg-field px-2.5 text-hi placeholder:text-muted-foreground focus-visible:outline-1 focus-visible:outline-ring disabled:opacity-60";

const CUSTOM = "custom";
const customAPIs: { id: string; name: string }[] = [
  { id: "openai-completions", name: "OpenAI Chat Completions (/chat/completions)" },
  { id: "openai-responses", name: "OpenAI Responses (/responses)" },
  { id: "anthropic-messages", name: "Anthropic Messages (/v1/messages)" },
];
const thinkingLevels = [
  { id: "off", name: "off" },
  { id: "minimal", name: "minimal" },
  { id: "low", name: "low" },
  { id: "medium", name: "medium" },
  { id: "high", name: "high" },
];

const fieldNames: Record<string, string> = {
  provider: "provider",
  base_url: "API address",
  api: "protocol",
  model: "model",
  models: "model list",
  thinking: "thinking",
  api_key: "API key",
};

function errText(e: unknown, fallback: string) {
  if (e instanceof ApiError) {
    if (fieldNames[e.code]) return t("check the field “{field}”", { field: t(fieldNames[e.code]) });
    if (e.code === "ai_unavailable") return t("srvmon-ai is unavailable");
    if (e.code === "agent_disabled") return t("the agent is off: no srvmon-ai container");
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

function ctxText(n?: number) {
  if (!n) return "";
  return n >= 1_000_000 ? `${Math.round(n / 100_000) / 10}M` : `${Math.round(n / 1000)}k`;
}

// Interface language. Stored on the server: Telegram messages and the AI
// agent use it too.
function LanguagePanel() {
  const cur = useLang();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const change = async (l: Lang) => {
    setBusy(true);
    setErr("");
    try {
      await panelApi.setLanguage(l);
      setLang(l);
    } catch (e) {
      setErr(errText(e, t("could not save")));
    }
    setBusy(false);
  };
  return (
    <Panel title={t("language")}>
      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap gap-2" role="radiogroup" aria-label={t("language")}>
          {LANGS.map((l) => (
            <Button
              key={l.code}
              size="sm"
              variant={l.code === cur ? "default" : "outline"}
              role="radio"
              aria-checked={l.code === cur}
              disabled={busy}
              onClick={() => l.code !== cur && void change(l.code)}
            >
              {l.name}
            </Button>
          ))}
        </div>
        {err && <div className="text-sm text-crit">{err}</div>}
        <div className="text-xs text-muted-foreground">
          {t("The language of the panel, Telegram bot messages and AI agent replies.")}
        </div>
      </div>
    </Panel>
  );
}

function AIPanel() {
  const [s, setS] = useState<AISettings | null>(null);
  const [providers, setProviders] = useState<AIProvider[]>([]);
  const [loadErr, setLoadErr] = useState("");
  // form
  const [provider, setProvider] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [api, setApi] = useState("openai-completions");
  const [key, setKey] = useState("");
  const [model, setModel] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const [thinking, setThinking] = useState("low");
  // model list
  const [list, setList] = useState<AIModel[] | null>(null);
  const [listInfo, setListInfo] = useState<{ live: boolean; error?: string } | null>(null);
  const [filter, setFilter] = useState("");
  const [onlyLive, setOnlyLive] = useState(true);
  const [manual, setManual] = useState("");
  // actions
  const [busy, setBusy] = useState("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [test, setTest] = useState<AITest | null>(null);

  const fill = useCallback((v: AISettings) => {
    setS(v);
    setProvider(v.provider || "deepseek");
    setBaseUrl(v.base_url ?? "");
    setApi(v.api || "openai-completions");
    setModel(v.model);
    setPicked(v.models.length ? v.models : v.model ? [v.model] : []);
    setThinking(v.thinking || "low");
    setKey("");
  }, []);

  useEffect(() => {
    agentApi
      .settings()
      .then(fill)
      .catch((e) => setLoadErr(errText(e, t("could not load the settings"))));
    agentApi
      .providers()
      .then(setProviders)
      .catch(() => undefined);
  }, [fill]);

  const prov = providers.find((p) => p.id === provider);
  const isCustom = provider === CUSTOM;
  const keyStored = !!s && (s.keys ?? []).includes(provider);
  const sameProvider = !!s && s.provider === provider;

  const changeProvider = (p: string) => {
    setProvider(p);
    setBaseUrl("");
    setKey("");
    setList(null);
    setListInfo(null);
    setTest(null);
    if (s && p === s.provider) {
      setModel(s.model);
      setPicked(s.models);
      setBaseUrl(s.base_url ?? "");
    } else {
      setModel("");
      setPicked([]);
    }
  };

  // The endpoint the form points at now: a model list that comes back after
  // the provider or address changed belongs to another endpoint.
  const endpoint = `${provider}|${baseUrl.trim()}|${isCustom ? api : ""}`;
  const endpointRef = useRef(endpoint);
  endpointRef.current = endpoint;
  // A list loaded for another address or protocol says nothing about this one.
  useEffect(() => {
    setList(null);
    setListInfo(null);
  }, [endpoint]);

  const loadModels = async () => {
    const asked = endpoint;
    setBusy("models");
    setMsg(null);
    try {
      const r = await agentApi.models({ provider, base_url: baseUrl.trim(), api: isCustom ? api : "", api_key: key.trim() || undefined });
      if (endpointRef.current === asked) {
        setList(r.models);
        setListInfo({ live: r.live, error: r.error });
        setOnlyLive(r.live);
      }
    } catch (e) {
      if (endpointRef.current === asked) setMsg({ ok: false, text: errText(e, t("could not get the model list")) });
    }
    setBusy("");
  };

  const togglePick = (id: string) => {
    setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]));
    if (!model) setModel(id);
  };
  const makeDefault = (id: string) => {
    setModel(id);
    setPicked((p) => (p.includes(id) ? p : [id, ...p]));
  };
  const addManual = () => {
    const id = manual.trim();
    if (!id) return;
    if (!model) setModel(id);
    setPicked((p) => (p.includes(id) ? p : [...p, id]));
    setManual("");
  };

  const input = (apiKey?: string): AISettingsInput => ({
    provider,
    base_url: baseUrl.trim(),
    api: isCustom ? api : "",
    model,
    thinking,
    models: picked.filter((m) => m !== model),
    ...(apiKey !== undefined ? { api_key: apiKey } : {}),
  });

  const save = async (apiKey?: string, okText = t("saved")) => {
    if (!model) {
      setMsg({ ok: false, text: t("pick a default model") });
      return;
    }
    setBusy("save");
    setMsg(null);
    setTest(null);
    try {
      const v = await agentApi.saveSettings(input(apiKey));
      fill(v);
      setMsg(v.error ? { ok: false, text: v.error } : { ok: true, text: okText });
    } catch (e) {
      setMsg({ ok: false, text: errText(e, t("could not save")) });
    }
    setBusy("");
  };

  const runTest = async () => {
    setBusy("test");
    setTest(null);
    try {
      setTest(await agentApi.test());
    } catch (e) {
      setTest({ ok: false, error: errText(e, t("no connection to srvmon-ai")) });
    }
    setBusy("");
  };

  const shown = useMemo(() => {
    if (!list) return [];
    const q = filter.trim().toLowerCase();
    return list.filter((m) => (!onlyLive || m.live || picked.includes(m.id)) && (!q || m.id.toLowerCase().includes(q) || (m.name ?? "").toLowerCase().includes(q)));
  }, [list, filter, onlyLive, picked]);

  if (loadErr) return <Panel title={t("AI agent")}>{<div className="text-crit">{loadErr}</div>}</Panel>;
  if (!s) return <Panel title={t("AI agent")}>{t("loading…")}</Panel>;

  const st = s.status;
  const status = !st ? (
    <span className="text-crit">✕ {s.error ?? t("srvmon-ai is unavailable")}</span>
  ) : st.ready ? (
    <span className="text-primary">
      ● {t("ready")} · {st.provider} / {st.model}
    </span>
  ) : (
    <span className="text-warn">○ {st.error ?? t("not ready")}</span>
  );
  const defaultUrl = isCustom ? "https://api.example.com/v1" : prov?.base_url || "";

  return (
    <Panel title={t("AI agent")} right={s.saved ? undefined : t("currently from environment variables")}>
      <div className="flex flex-col gap-3">
        <div className="text-sm">
          <span className="text-muted-foreground">{t("status")}</span> {status}
        </div>

        <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
          <Field label={t("provider")} htmlFor="ai-provider">
            <select id="ai-provider" className={fieldCls} value={provider} onChange={(e) => changeProvider(e.target.value)}>
              {!providers.some((p) => p.id === provider) && provider !== CUSTOM && <option value={provider}>{provider}</option>}
              {providers.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                  {(s.keys ?? []).includes(p.id) ? ` · ${t("key saved")}` : ""}
                </option>
              ))}
              <option value={CUSTOM}>
                {t("Custom (OpenAI / Anthropic compatible)")}
                {(s.keys ?? []).includes(CUSTOM) ? ` · ${t("key saved")}` : ""}
              </option>
            </select>
          </Field>
          <Field
            label={isCustom ? t("API address") : t("API address (optional)")}
            htmlFor="ai-url"
            hint={
              isCustom
                ? t("for example http://ollama:11434/v1 or https://api.example.com/v1")
                : t("for a proxy or your own gateway; empty: the provider's address")
            }
          >
            <input
              id="ai-url"
              className={fieldCls}
              value={baseUrl}
              placeholder={defaultUrl}
              onChange={(e) => setBaseUrl(e.target.value)}
              autoComplete="off"
              spellCheck={false}
            />
          </Field>
          {isCustom && (
            <Field label={t("protocol")} htmlFor="ai-api">
              <select id="ai-api" className={fieldCls} value={api} onChange={(e) => setApi(e.target.value)}>
                {customAPIs.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name}
                  </option>
                ))}
              </select>
            </Field>
          )}
          <Field
            label={t("API key")}
            htmlFor="ai-key"
            hint={
              keyStored ? (
                <>
                  {t("saved")}
                  {sameProvider && s.key_hint ? ` (${s.key_hint})` : ""}, {t("stored encrypted")} ·{" "}
                  <button
                    type="button"
                    className="text-crit hover:underline"
                    disabled={!!busy}
                    onClick={() => confirm(t("Delete the API key of this provider?")) && void save("", t("key deleted"))}
                  >
                    {t("delete")}
                  </button>
                </>
              ) : (
                t("stored encrypted on the server, never sent back to the browser")
              )
            }
          >
            <input
              id="ai-key"
              type="password"
              autoComplete="off"
              className={fieldCls}
              value={key}
              placeholder={keyStored ? t("leave empty to keep it") : isCustom ? t("if needed") : "sk-…"}
              onChange={(e) => setKey(e.target.value)}
            />
          </Field>
          <Field label={t("thinking (for reasoning models)")} htmlFor="ai-thinking">
            <select id="ai-thinking" className={fieldCls} value={thinking} onChange={(e) => setThinking(e.target.value)}>
              {thinkingLevels.map((lv) => (
                <option key={lv.id} value={lv.id}>
                  {t(lv.name)}
                </option>
              ))}
            </select>
          </Field>
        </div>

        <div className="flex flex-col gap-2 border-t border-border/60 pt-3">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm text-muted-foreground">{t("models")}</span>
            <span className="text-sm">
              {t("default:")} <span className="text-hi">{model || t("not selected")}</span>
              {picked.length > 1 && <span className="text-muted-foreground"> · {t("selectable in chat: {n}", { n: picked.length })}</span>}
            </span>
            <span className="flex-1" />
            <Button size="sm" variant="outline" disabled={!!busy || (isCustom && !baseUrl.trim())} onClick={loadModels}>
              {busy === "models" ? t("loading…") : list ? t("refresh the list") : t("load the list from the provider")}
            </Button>
          </div>

          {picked.length > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {picked.map((m) => (
                <span key={m} className={cn("flex items-center gap-1 border px-1.5 text-xs", m === model ? "border-primary text-primary" : "border-border text-hi")}>
                  {m === model ? "★" : (
                    <button type="button" title={t("make it the default model")} className="text-muted-foreground hover:text-primary" onClick={() => makeDefault(m)}>
                      ☆
                    </button>
                  )}
                  {m}
                  <button type="button" aria-label={t("remove {m}", { m })} className="text-muted-foreground hover:text-crit" onClick={() => {
                    setPicked((p) => p.filter((x) => x !== m));
                    if (m === model) setModel(picked.find((x) => x !== m) ?? "");
                  }}>
                    ×
                  </button>
                </span>
              ))}
            </div>
          )}

          {listInfo && (
            <div className={cn("text-xs", listInfo.live ? "text-muted-foreground" : "text-warn")}>
              {listInfo.live
                ? tn(list?.filter((m) => m.live).length ?? 0, "the provider returned {n} model", "the provider returned {n} models")
                : `${t("could not get the list from the provider: {err}", { err: listInfo.error ?? t("error") })}${list?.length ? ` · ${t("showing the pi catalog")}` : ""}`}
            </div>
          )}

          {list && (
            <>
              <div className="flex flex-wrap items-center gap-3">
                <input
                  className={cn(fieldCls, "h-8 max-w-72")}
                  placeholder={t("filter")}
                  value={filter}
                  onChange={(e) => setFilter(e.target.value)}
                  aria-label={t("model filter")}
                />
                {listInfo?.live && list.some((m) => !m.live) && (
                  <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
                    <input type="checkbox" checked={onlyLive} onChange={(e) => setOnlyLive(e.target.checked)} />
                    {t("only available with the key")}
                  </label>
                )}
              </div>
              <div className="max-h-72 overflow-y-auto border border-border/60 [scrollbar-width:thin]">
                <table className="w-full border-collapse text-left text-sm">
                  <tbody>
                    {shown.slice(0, 300).map((m) => (
                      <tr key={m.id} className="border-t border-border/40 first:border-t-0 hover:bg-white/[0.02]">
                        <td className="w-8 py-1 pl-2">
                          <input type="checkbox" checked={picked.includes(m.id)} onChange={() => togglePick(m.id)} aria-label={t("in chat: {id}", { id: m.id })} />
                        </td>
                        <td className="py-1 pr-2">
                          <span className={cn(m.id === model ? "text-primary" : "text-hi")}>{m.id}</span>
                          {m.name && m.name !== m.id && <span className="text-muted-foreground"> · {m.name}</span>}
                        </td>
                        <td className="py-1 pr-2 text-xs whitespace-nowrap text-muted-foreground">
                          {m.reasoning ? "reasoning · " : ""}
                          {ctxText(m.context)}
                          {!m.catalog && !isCustom && ` · ${t("not in the pi catalog")}`}
                          {listInfo?.live && !m.live && ` · ${t("not at the provider")}`}
                        </td>
                        <td className="py-1 pr-2 text-right">
                          {m.id === model ? (
                            <span className="text-xs text-primary">★ {t("default")}</span>
                          ) : (
                            <button type="button" className="text-xs text-muted-foreground hover:text-primary" onClick={() => makeDefault(m.id)}>
                              {t("make default")}
                            </button>
                          )}
                        </td>
                      </tr>
                    ))}
                    {shown.length === 0 && (
                      <tr>
                        <td className="p-2 text-muted-foreground">{t("nothing found")}</td>
                      </tr>
                    )}
                  </tbody>
                </table>
              </div>
              {shown.length > 300 && <div className="text-xs text-muted-foreground">{t("showing the first 300, narrow the filter")}</div>}
            </>
          )}

          <div className="flex flex-wrap items-center gap-2">
            <input
              className={cn(fieldCls, "h-8 max-w-72")}
              placeholder={t("or a model id by hand")}
              value={manual}
              onChange={(e) => setManual(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && (e.preventDefault(), addManual())}
              aria-label={t("model id")}
              spellCheck={false}
            />
            <Button size="sm" variant="ghost" disabled={!manual.trim()} onClick={addManual}>
              {t("add")}
            </Button>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2 border-t border-border/60 pt-3">
          <Button disabled={!!busy} onClick={() => void save(key.trim() || undefined)}>
            {busy === "save" ? t("saving…") : t("save and apply")}
          </Button>
          <Button variant="outline" disabled={!!busy || !st?.ready} onClick={runTest}>
            {busy === "test" ? t("checking…") : t("test connection")}
          </Button>
          {msg && <span className={cn("text-sm", msg.ok ? "text-primary" : "text-crit")}>{msg.text}</span>}
        </div>
        {test && (
          <div className={cn("text-sm", test.ok ? "text-primary" : "text-crit")}>
            {test.ok
              ? `✓ ${t("{model} replied in {s} s: “{text}”", { model: test.model ?? "", s: ((test.ms ?? 0) / 1000).toFixed(1), text: test.text ?? "" })}`
              : `✕ ${test.error}`}
          </div>
        )}
        <div className="text-xs text-muted-foreground">
          {t(
            "Applies at once, no restart. Open chats continue with the new model and keep their history. Checked models can be picked for a single chat in the agent panel.",
          )}
        </div>
      </div>
    </Panel>
  );
}

// Automatic AI diagnosis of alerts: limits keep the model bill bounded.
function DiagnosePanel() {
  const [d, setD] = useState<DiagnoseSettings | null>(null);
  const [models, setModels] = useState<string[]>([]);
  const [hour, setHour] = useState("");
  const [day, setDay] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  useEffect(() => {
    agentApi
      .diagnoseSettings()
      .then((v) => {
        setD(v);
        setHour(String(v.per_hour));
        setDay(String(v.per_day));
      })
      .catch((e) => setMsg({ ok: false, text: errText(e, t("could not load")) }));
    agentApi
      .status()
      .then((st) => setModels(st.models ?? []))
      .catch(() => undefined);
  }, []);
  if (!d) return msg ? <Panel title={t("AI diagnosis of alerts")}><div className="text-crit">{msg.text}</div></Panel> : null;
  const save = async () => {
    setBusy(true);
    setMsg(null);
    try {
      const v = await agentApi.saveDiagnoseSettings({ ...d, per_hour: Number(hour), per_day: Number(day) });
      setD(v);
      setMsg({ ok: true, text: t("saved") });
    } catch (e) {
      setMsg({ ok: false, text: e instanceof ApiError && e.message !== e.code ? e.message : errText(e, t("could not save")) });
    }
    setBusy(false);
  };
  const check = (on: boolean, label: string, set: (v: boolean) => void) => (
    <button type="button" className="flex items-center gap-2 text-left" onClick={() => set(!on)} aria-pressed={on}>
      <span className={cn("whitespace-pre", on ? "text-primary" : "text-muted-foreground")}>{on ? "[x]" : "[ ]"}</span>
      <span>{label}</span>
    </button>
  );
  return (
    <Panel title={t("AI diagnosis of alerts")}>
      <div className="flex flex-col gap-3">
        <div className="text-xs text-muted-foreground">
          {t(
            "When an alert fires, the agent collects logs with read-only commands and sends the likely cause as a reply to the Telegram alert. Commands that change a server are refused. The same alert on the same server is diagnosed at most once in 6 hours.",
          )}
        </div>
        {check(d.enabled, t("diagnose critical alerts automatically"), (v) => setD({ ...d, enabled: v }))}
        {check(d.warn, t("warnings too"), (v) => setD({ ...d, warn: v }))}
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <Field label={t("per hour, at most")} htmlFor="diag-hour">
            <input id="diag-hour" className={fieldCls} inputMode="numeric" value={hour} onChange={(e) => setHour(e.target.value)} />
          </Field>
          <Field label={t("per day, at most")} htmlFor="diag-day">
            <input id="diag-day" className={fieldCls} inputMode="numeric" value={day} onChange={(e) => setDay(e.target.value)} />
          </Field>
          <Field label={t("model")} htmlFor="diag-model" hint={t("a cheaper model is enough")}>
            <select id="diag-model" className={fieldCls} value={d.model} onChange={(e) => setD({ ...d, model: e.target.value })}>
              <option value="">{t("default")}</option>
              {[...new Set([...models, ...(d.model ? [d.model] : [])])].map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
            </select>
          </Field>
        </div>
        <div className="flex items-center gap-3">
          <Button disabled={busy} onClick={() => void save()}>
            {t("save")}
          </Button>
          {msg && <span className={cn("text-sm", msg.ok ? "text-primary" : "text-crit")}>{msg.text}</span>}
        </div>
      </div>
    </Panel>
  );
}

export function SettingsPage({ route, version, onLogout }: { route: Route; version: string; onLogout: () => void }) {
  useEffect(() => {
    // A dead session shows up as 401 on the first request.
    agentApi.status().catch((e) => e instanceof ApiError && e.status === 401 && onLogout());
  }, [onLogout]);
  return (
    <Shell
      route={route}
      version={version}
      onLogout={onLogout}
      footer={
        <>
          <Hotkey k="4">{t("agent")}</Hotkey>
          <Hotkey k="6">{t("settings")}</Hotkey>
        </>
      }
    >
      <div className="flex max-w-5xl flex-col gap-5">
        <LanguagePanel />
        <AIPanel />
        <DiagnosePanel />
        <McpPanel />
      </div>
    </Shell>
  );
}
