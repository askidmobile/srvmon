import { type ReactNode, useCallback, useEffect, useMemo, useState } from "react";

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
} from "@/lib/agent";
import { ApiError } from "@/lib/api";
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
  { id: "off", name: "выкл." },
  { id: "minimal", name: "минимум" },
  { id: "low", name: "низкий" },
  { id: "medium", name: "средний" },
  { id: "high", name: "высокий" },
];

const fieldNames: Record<string, string> = {
  provider: "провайдер",
  base_url: "адрес API",
  api: "протокол",
  model: "модель",
  models: "список моделей",
  thinking: "размышления",
  api_key: "ключ API",
};

function errText(e: unknown, fallback: string) {
  if (e instanceof ApiError) {
    if (fieldNames[e.code]) return `проверьте поле «${fieldNames[e.code]}»`;
    if (e.code === "ai_unavailable") return "srvmon-ai недоступен";
    if (e.code === "agent_disabled") return "агент выключен: нет контейнера srvmon-ai";
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
      .catch((e) => setLoadErr(errText(e, "не удалось загрузить настройки")));
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

  const loadModels = async () => {
    setBusy("models");
    setMsg(null);
    try {
      const r = await agentApi.models({ provider, base_url: baseUrl.trim(), api: isCustom ? api : "", api_key: key.trim() || undefined });
      setList(r.models);
      setListInfo({ live: r.live, error: r.error });
      setOnlyLive(r.live);
    } catch (e) {
      setMsg({ ok: false, text: errText(e, "не удалось получить список моделей") });
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

  const save = async (apiKey?: string, okText = "сохранено") => {
    if (!model) {
      setMsg({ ok: false, text: "выберите модель по умолчанию" });
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
      setMsg({ ok: false, text: errText(e, "не удалось сохранить") });
    }
    setBusy("");
  };

  const runTest = async () => {
    setBusy("test");
    setTest(null);
    try {
      setTest(await agentApi.test());
    } catch (e) {
      setTest({ ok: false, error: errText(e, "нет связи с srvmon-ai") });
    }
    setBusy("");
  };

  const shown = useMemo(() => {
    if (!list) return [];
    const q = filter.trim().toLowerCase();
    return list.filter((m) => (!onlyLive || m.live || picked.includes(m.id)) && (!q || m.id.toLowerCase().includes(q) || (m.name ?? "").toLowerCase().includes(q)));
  }, [list, filter, onlyLive, picked]);

  if (loadErr) return <Panel title="AI-агент">{<div className="text-crit">{loadErr}</div>}</Panel>;
  if (!s) return <Panel title="AI-агент">загрузка…</Panel>;

  const st = s.status;
  const status = !st ? (
    <span className="text-crit">✕ {s.error ?? "srvmon-ai недоступен"}</span>
  ) : st.ready ? (
    <span className="text-primary">
      ● готов · {st.provider} / {st.model}
    </span>
  ) : (
    <span className="text-warn">○ {st.error ?? "не готов"}</span>
  );
  const defaultUrl = isCustom ? "https://api.example.com/v1" : prov?.base_url || "";

  return (
    <Panel title="AI-агент" right={s.saved ? undefined : "сейчас из переменных окружения"}>
      <div className="flex flex-col gap-3">
        <div className="text-sm">
          <span className="text-muted-foreground">статус</span> {status}
        </div>

        <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
          <Field label="провайдер" htmlFor="ai-provider">
            <select id="ai-provider" className={fieldCls} value={provider} onChange={(e) => changeProvider(e.target.value)}>
              {!providers.some((p) => p.id === provider) && provider !== CUSTOM && <option value={provider}>{provider}</option>}
              {providers.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                  {(s.keys ?? []).includes(p.id) ? " · ключ сохранён" : ""}
                </option>
              ))}
              <option value={CUSTOM}>Свой (OpenAI / Anthropic-совместимый){(s.keys ?? []).includes(CUSTOM) ? " · ключ сохранён" : ""}</option>
            </select>
          </Field>
          <Field
            label={isCustom ? "адрес API" : "адрес API (необязательно)"}
            htmlFor="ai-url"
            hint={isCustom ? "например http://ollama:11434/v1 или https://api.example.com/v1" : "для прокси или своего шлюза; пусто — адрес провайдера"}
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
            <Field label="протокол" htmlFor="ai-api">
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
            label="ключ API"
            htmlFor="ai-key"
            hint={
              keyStored ? (
                <>
                  сохранён{sameProvider && s.key_hint ? ` (${s.key_hint})` : ""}, хранится зашифрованным ·{" "}
                  <button
                    type="button"
                    className="text-crit hover:underline"
                    disabled={!!busy}
                    onClick={() => confirm("Удалить ключ API этого провайдера?") && void save("", "ключ удалён")}
                  >
                    удалить
                  </button>
                </>
              ) : (
                "хранится на сервере зашифрованным, в браузер не возвращается"
              )
            }
          >
            <input
              id="ai-key"
              type="password"
              autoComplete="off"
              className={fieldCls}
              value={key}
              placeholder={keyStored ? "оставьте пустым, чтобы не менять" : isCustom ? "если нужен" : "sk-…"}
              onChange={(e) => setKey(e.target.value)}
            />
          </Field>
          <Field label="размышления (для моделей с reasoning)" htmlFor="ai-thinking">
            <select id="ai-thinking" className={fieldCls} value={thinking} onChange={(e) => setThinking(e.target.value)}>
              {thinkingLevels.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name}
                </option>
              ))}
            </select>
          </Field>
        </div>

        <div className="flex flex-col gap-2 border-t border-border/60 pt-3">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm text-muted-foreground">модели</span>
            <span className="text-sm">
              по умолчанию: <span className="text-hi">{model || "не выбрана"}</span>
              {picked.length > 1 && <span className="text-muted-foreground"> · в чате на выбор: {picked.length}</span>}
            </span>
            <span className="flex-1" />
            <Button size="sm" variant="outline" disabled={!!busy || (isCustom && !baseUrl.trim())} onClick={loadModels}>
              {busy === "models" ? "загружаю…" : list ? "обновить список" : "загрузить список у провайдера"}
            </Button>
          </div>

          {picked.length > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {picked.map((m) => (
                <span key={m} className={cn("flex items-center gap-1 border px-1.5 text-xs", m === model ? "border-primary text-primary" : "border-border text-hi")}>
                  {m === model ? "★" : (
                    <button type="button" title="сделать моделью по умолчанию" className="text-muted-foreground hover:text-primary" onClick={() => makeDefault(m)}>
                      ☆
                    </button>
                  )}
                  {m}
                  <button type="button" aria-label={`убрать ${m}`} className="text-muted-foreground hover:text-crit" onClick={() => {
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
                ? `провайдер отдал ${list?.filter((m) => m.live).length ?? 0} моделей`
                : `список у провайдера не получен: ${listInfo.error ?? "ошибка"}${list?.length ? " · показан каталог pi" : ""}`}
            </div>
          )}

          {list && (
            <>
              <div className="flex flex-wrap items-center gap-3">
                <input
                  className={cn(fieldCls, "h-8 max-w-72")}
                  placeholder="фильтр"
                  value={filter}
                  onChange={(e) => setFilter(e.target.value)}
                  aria-label="фильтр моделей"
                />
                {listInfo?.live && list.some((m) => !m.live) && (
                  <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
                    <input type="checkbox" checked={onlyLive} onChange={(e) => setOnlyLive(e.target.checked)} />
                    только доступные по ключу
                  </label>
                )}
              </div>
              <div className="max-h-72 overflow-y-auto border border-border/60 [scrollbar-width:thin]">
                <table className="w-full border-collapse text-left text-sm">
                  <tbody>
                    {shown.slice(0, 300).map((m) => (
                      <tr key={m.id} className="border-t border-border/40 first:border-t-0 hover:bg-white/[0.02]">
                        <td className="w-8 py-1 pl-2">
                          <input type="checkbox" checked={picked.includes(m.id)} onChange={() => togglePick(m.id)} aria-label={`в чате: ${m.id}`} />
                        </td>
                        <td className="py-1 pr-2">
                          <span className={cn(m.id === model ? "text-primary" : "text-hi")}>{m.id}</span>
                          {m.name && m.name !== m.id && <span className="text-muted-foreground"> · {m.name}</span>}
                        </td>
                        <td className="py-1 pr-2 text-xs whitespace-nowrap text-muted-foreground">
                          {m.reasoning ? "reasoning · " : ""}
                          {ctxText(m.context)}
                          {!m.catalog && !isCustom && " · нет в каталоге pi"}
                          {listInfo?.live && !m.live && " · нет у провайдера"}
                        </td>
                        <td className="py-1 pr-2 text-right">
                          {m.id === model ? (
                            <span className="text-xs text-primary">★ по умолч.</span>
                          ) : (
                            <button type="button" className="text-xs text-muted-foreground hover:text-primary" onClick={() => makeDefault(m.id)}>
                              по умолчанию
                            </button>
                          )}
                        </td>
                      </tr>
                    ))}
                    {shown.length === 0 && (
                      <tr>
                        <td className="p-2 text-muted-foreground">ничего не найдено</td>
                      </tr>
                    )}
                  </tbody>
                </table>
              </div>
              {shown.length > 300 && <div className="text-xs text-muted-foreground">показаны первые 300 — уточните фильтр</div>}
            </>
          )}

          <div className="flex flex-wrap items-center gap-2">
            <input
              className={cn(fieldCls, "h-8 max-w-72")}
              placeholder="или id модели вручную"
              value={manual}
              onChange={(e) => setManual(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && (e.preventDefault(), addManual())}
              aria-label="id модели"
              spellCheck={false}
            />
            <Button size="sm" variant="ghost" disabled={!manual.trim()} onClick={addManual}>
              добавить
            </Button>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2 border-t border-border/60 pt-3">
          <Button disabled={!!busy} onClick={() => void save(key.trim() || undefined)}>
            {busy === "save" ? "сохраняю…" : "сохранить и применить"}
          </Button>
          <Button variant="outline" disabled={!!busy || !st?.ready} onClick={runTest}>
            {busy === "test" ? "проверяю…" : "проверить связь"}
          </Button>
          {msg && <span className={cn("text-sm", msg.ok ? "text-primary" : "text-crit")}>{msg.text}</span>}
        </div>
        {test && (
          <div className={cn("text-sm", test.ok ? "text-primary" : "text-crit")}>
            {test.ok ? `✓ ${test.model} ответила за ${((test.ms ?? 0) / 1000).toFixed(1)} с: «${test.text}»` : `✕ ${test.error}`}
          </div>
        )}
        <div className="text-xs text-muted-foreground">
          Применяется сразу, без перезапуска. Открытые чаты продолжат с новой моделью, история сохраняется. Отмеченные
          модели можно выбрать для отдельного чата в панели агента.
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
          <Hotkey k="4">агент</Hotkey>
          <Hotkey k="6">настройки</Hotkey>
        </>
      }
    >
      <div className="max-w-5xl">
        <AIPanel />
      </div>
    </Shell>
  );
}
