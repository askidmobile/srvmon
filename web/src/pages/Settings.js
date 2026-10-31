import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Hotkey, Shell } from "@/components/Shell";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { agentApi, } from "@/lib/agent";
import { ApiError } from "@/lib/api";
import { cn } from "@/lib/utils";
const fieldCls = "h-9 w-full min-w-0 border border-input bg-field px-2.5 text-hi placeholder:text-muted-foreground focus-visible:outline-1 focus-visible:outline-ring disabled:opacity-60";
const CUSTOM = "custom";
const customAPIs = [
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
const fieldNames = {
    provider: "провайдер",
    base_url: "адрес API",
    api: "протокол",
    model: "модель",
    models: "список моделей",
    thinking: "размышления",
    api_key: "ключ API",
};
function errText(e, fallback) {
    if (e instanceof ApiError) {
        if (fieldNames[e.code])
            return `проверьте поле «${fieldNames[e.code]}»`;
        if (e.code === "ai_unavailable")
            return "srvmon-ai недоступен";
        if (e.code === "agent_disabled")
            return "агент выключен: нет контейнера srvmon-ai";
    }
    return fallback;
}
function Field({ label, htmlFor, hint, children }) {
    return (_jsxs("div", { className: "flex flex-col gap-1", children: [_jsx("label", { className: "text-sm text-muted-foreground", htmlFor: htmlFor, children: label }), children, hint && _jsx("div", { className: "text-xs text-muted-foreground", children: hint })] }));
}
function ctxText(n) {
    if (!n)
        return "";
    return n >= 1_000_000 ? `${Math.round(n / 100_000) / 10}M` : `${Math.round(n / 1000)}k`;
}
function AIPanel() {
    const [s, setS] = useState(null);
    const [providers, setProviders] = useState([]);
    const [loadErr, setLoadErr] = useState("");
    // form
    const [provider, setProvider] = useState("");
    const [baseUrl, setBaseUrl] = useState("");
    const [api, setApi] = useState("openai-completions");
    const [key, setKey] = useState("");
    const [model, setModel] = useState("");
    const [picked, setPicked] = useState([]);
    const [thinking, setThinking] = useState("low");
    // model list
    const [list, setList] = useState(null);
    const [listInfo, setListInfo] = useState(null);
    const [filter, setFilter] = useState("");
    const [onlyLive, setOnlyLive] = useState(true);
    const [manual, setManual] = useState("");
    // actions
    const [busy, setBusy] = useState("");
    const [msg, setMsg] = useState(null);
    const [test, setTest] = useState(null);
    const fill = useCallback((v) => {
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
    const changeProvider = (p) => {
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
        }
        else {
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
        }
        catch (e) {
            if (endpointRef.current === asked)
                setMsg({ ok: false, text: errText(e, "не удалось получить список моделей") });
        }
        setBusy("");
    };
    const togglePick = (id) => {
        setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]));
        if (!model)
            setModel(id);
    };
    const makeDefault = (id) => {
        setModel(id);
        setPicked((p) => (p.includes(id) ? p : [id, ...p]));
    };
    const addManual = () => {
        const id = manual.trim();
        if (!id)
            return;
        if (!model)
            setModel(id);
        setPicked((p) => (p.includes(id) ? p : [...p, id]));
        setManual("");
    };
    const input = (apiKey) => ({
        provider,
        base_url: baseUrl.trim(),
        api: isCustom ? api : "",
        model,
        thinking,
        models: picked.filter((m) => m !== model),
        ...(apiKey !== undefined ? { api_key: apiKey } : {}),
    });
    const save = async (apiKey, okText = "сохранено") => {
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
        }
        catch (e) {
            setMsg({ ok: false, text: errText(e, "не удалось сохранить") });
        }
        setBusy("");
    };
    const runTest = async () => {
        setBusy("test");
        setTest(null);
        try {
            setTest(await agentApi.test());
        }
        catch (e) {
            setTest({ ok: false, error: errText(e, "нет связи с srvmon-ai") });
        }
        setBusy("");
    };
    const shown = useMemo(() => {
        if (!list)
            return [];
        const q = filter.trim().toLowerCase();
        return list.filter((m) => (!onlyLive || m.live || picked.includes(m.id)) && (!q || m.id.toLowerCase().includes(q) || (m.name ?? "").toLowerCase().includes(q)));
    }, [list, filter, onlyLive, picked]);
    if (loadErr)
        return _jsx(Panel, { title: "AI-\u0430\u0433\u0435\u043D\u0442", children: _jsx("div", { className: "text-crit", children: loadErr }) });
    if (!s)
        return _jsx(Panel, { title: "AI-\u0430\u0433\u0435\u043D\u0442", children: "\u0437\u0430\u0433\u0440\u0443\u0437\u043A\u0430\u2026" });
    const st = s.status;
    const status = !st ? (_jsxs("span", { className: "text-crit", children: ["\u2715 ", s.error ?? "srvmon-ai недоступен"] })) : st.ready ? (_jsxs("span", { className: "text-primary", children: ["\u25CF \u0433\u043E\u0442\u043E\u0432 \u00B7 ", st.provider, " / ", st.model] })) : (_jsxs("span", { className: "text-warn", children: ["\u25CB ", st.error ?? "не готов"] }));
    const defaultUrl = isCustom ? "https://api.example.com/v1" : prov?.base_url || "";
    return (_jsx(Panel, { title: "AI-\u0430\u0433\u0435\u043D\u0442", right: s.saved ? undefined : "сейчас из переменных окружения", children: _jsxs("div", { className: "flex flex-col gap-3", children: [_jsxs("div", { className: "text-sm", children: [_jsx("span", { className: "text-muted-foreground", children: "\u0441\u0442\u0430\u0442\u0443\u0441" }), " ", status] }), _jsxs("div", { className: "grid grid-cols-1 gap-3 md:grid-cols-2", children: [_jsx(Field, { label: "\u043F\u0440\u043E\u0432\u0430\u0439\u0434\u0435\u0440", htmlFor: "ai-provider", children: _jsxs("select", { id: "ai-provider", className: fieldCls, value: provider, onChange: (e) => changeProvider(e.target.value), children: [!providers.some((p) => p.id === provider) && provider !== CUSTOM && _jsx("option", { value: provider, children: provider }), providers.map((p) => (_jsxs("option", { value: p.id, children: [p.name, (s.keys ?? []).includes(p.id) ? " · ключ сохранён" : ""] }, p.id))), _jsxs("option", { value: CUSTOM, children: ["\u0421\u0432\u043E\u0439 (OpenAI / Anthropic-\u0441\u043E\u0432\u043C\u0435\u0441\u0442\u0438\u043C\u044B\u0439)", (s.keys ?? []).includes(CUSTOM) ? " · ключ сохранён" : ""] })] }) }), _jsx(Field, { label: isCustom ? "адрес API" : "адрес API (необязательно)", htmlFor: "ai-url", hint: isCustom ? "например http://ollama:11434/v1 или https://api.example.com/v1" : "для прокси или своего шлюза; пусто — адрес провайдера", children: _jsx("input", { id: "ai-url", className: fieldCls, value: baseUrl, placeholder: defaultUrl, onChange: (e) => setBaseUrl(e.target.value), autoComplete: "off", spellCheck: false }) }), isCustom && (_jsx(Field, { label: "\u043F\u0440\u043E\u0442\u043E\u043A\u043E\u043B", htmlFor: "ai-api", children: _jsx("select", { id: "ai-api", className: fieldCls, value: api, onChange: (e) => setApi(e.target.value), children: customAPIs.map((a) => (_jsx("option", { value: a.id, children: a.name }, a.id))) }) })), _jsx(Field, { label: "\u043A\u043B\u044E\u0447 API", htmlFor: "ai-key", hint: keyStored ? (_jsxs(_Fragment, { children: ["\u0441\u043E\u0445\u0440\u0430\u043D\u0451\u043D", sameProvider && s.key_hint ? ` (${s.key_hint})` : "", ", \u0445\u0440\u0430\u043D\u0438\u0442\u0441\u044F \u0437\u0430\u0448\u0438\u0444\u0440\u043E\u0432\u0430\u043D\u043D\u044B\u043C \u00B7", " ", _jsx("button", { type: "button", className: "text-crit hover:underline", disabled: !!busy, onClick: () => confirm("Удалить ключ API этого провайдера?") && void save("", "ключ удалён"), children: "\u0443\u0434\u0430\u043B\u0438\u0442\u044C" })] })) : ("хранится на сервере зашифрованным, в браузер не возвращается"), children: _jsx("input", { id: "ai-key", type: "password", autoComplete: "off", className: fieldCls, value: key, placeholder: keyStored ? "оставьте пустым, чтобы не менять" : isCustom ? "если нужен" : "sk-…", onChange: (e) => setKey(e.target.value) }) }), _jsx(Field, { label: "\u0440\u0430\u0437\u043C\u044B\u0448\u043B\u0435\u043D\u0438\u044F (\u0434\u043B\u044F \u043C\u043E\u0434\u0435\u043B\u0435\u0439 \u0441 reasoning)", htmlFor: "ai-thinking", children: _jsx("select", { id: "ai-thinking", className: fieldCls, value: thinking, onChange: (e) => setThinking(e.target.value), children: thinkingLevels.map((t) => (_jsx("option", { value: t.id, children: t.name }, t.id))) }) })] }), _jsxs("div", { className: "flex flex-col gap-2 border-t border-border/60 pt-3", children: [_jsxs("div", { className: "flex flex-wrap items-center gap-2", children: [_jsx("span", { className: "text-sm text-muted-foreground", children: "\u043C\u043E\u0434\u0435\u043B\u0438" }), _jsxs("span", { className: "text-sm", children: ["\u043F\u043E \u0443\u043C\u043E\u043B\u0447\u0430\u043D\u0438\u044E: ", _jsx("span", { className: "text-hi", children: model || "не выбрана" }), picked.length > 1 && _jsxs("span", { className: "text-muted-foreground", children: [" \u00B7 \u0432 \u0447\u0430\u0442\u0435 \u043D\u0430 \u0432\u044B\u0431\u043E\u0440: ", picked.length] })] }), _jsx("span", { className: "flex-1" }), _jsx(Button, { size: "sm", variant: "outline", disabled: !!busy || (isCustom && !baseUrl.trim()), onClick: loadModels, children: busy === "models" ? "загружаю…" : list ? "обновить список" : "загрузить список у провайдера" })] }), picked.length > 0 && (_jsx("div", { className: "flex flex-wrap gap-1.5", children: picked.map((m) => (_jsxs("span", { className: cn("flex items-center gap-1 border px-1.5 text-xs", m === model ? "border-primary text-primary" : "border-border text-hi"), children: [m === model ? "★" : (_jsx("button", { type: "button", title: "\u0441\u0434\u0435\u043B\u0430\u0442\u044C \u043C\u043E\u0434\u0435\u043B\u044C\u044E \u043F\u043E \u0443\u043C\u043E\u043B\u0447\u0430\u043D\u0438\u044E", className: "text-muted-foreground hover:text-primary", onClick: () => makeDefault(m), children: "\u2606" })), m, _jsx("button", { type: "button", "aria-label": `убрать ${m}`, className: "text-muted-foreground hover:text-crit", onClick: () => {
                                            setPicked((p) => p.filter((x) => x !== m));
                                            if (m === model)
                                                setModel(picked.find((x) => x !== m) ?? "");
                                        }, children: "\u00D7" })] }, m))) })), listInfo && (_jsx("div", { className: cn("text-xs", listInfo.live ? "text-muted-foreground" : "text-warn"), children: listInfo.live
                                ? `провайдер отдал ${list?.filter((m) => m.live).length ?? 0} моделей`
                                : `список у провайдера не получен: ${listInfo.error ?? "ошибка"}${list?.length ? " · показан каталог pi" : ""}` })), list && (_jsxs(_Fragment, { children: [_jsxs("div", { className: "flex flex-wrap items-center gap-3", children: [_jsx("input", { className: cn(fieldCls, "h-8 max-w-72"), placeholder: "\u0444\u0438\u043B\u044C\u0442\u0440", value: filter, onChange: (e) => setFilter(e.target.value), "aria-label": "\u0444\u0438\u043B\u044C\u0442\u0440 \u043C\u043E\u0434\u0435\u043B\u0435\u0439" }), listInfo?.live && list.some((m) => !m.live) && (_jsxs("label", { className: "flex items-center gap-1.5 text-xs text-muted-foreground", children: [_jsx("input", { type: "checkbox", checked: onlyLive, onChange: (e) => setOnlyLive(e.target.checked) }), "\u0442\u043E\u043B\u044C\u043A\u043E \u0434\u043E\u0441\u0442\u0443\u043F\u043D\u044B\u0435 \u043F\u043E \u043A\u043B\u044E\u0447\u0443"] }))] }), _jsx("div", { className: "max-h-72 overflow-y-auto border border-border/60 [scrollbar-width:thin]", children: _jsx("table", { className: "w-full border-collapse text-left text-sm", children: _jsxs("tbody", { children: [shown.slice(0, 300).map((m) => (_jsxs("tr", { className: "border-t border-border/40 first:border-t-0 hover:bg-white/[0.02]", children: [_jsx("td", { className: "w-8 py-1 pl-2", children: _jsx("input", { type: "checkbox", checked: picked.includes(m.id), onChange: () => togglePick(m.id), "aria-label": `в чате: ${m.id}` }) }), _jsxs("td", { className: "py-1 pr-2", children: [_jsx("span", { className: cn(m.id === model ? "text-primary" : "text-hi"), children: m.id }), m.name && m.name !== m.id && _jsxs("span", { className: "text-muted-foreground", children: [" \u00B7 ", m.name] })] }), _jsxs("td", { className: "py-1 pr-2 text-xs whitespace-nowrap text-muted-foreground", children: [m.reasoning ? "reasoning · " : "", ctxText(m.context), !m.catalog && !isCustom && " · нет в каталоге pi", listInfo?.live && !m.live && " · нет у провайдера"] }), _jsx("td", { className: "py-1 pr-2 text-right", children: m.id === model ? (_jsx("span", { className: "text-xs text-primary", children: "\u2605 \u043F\u043E \u0443\u043C\u043E\u043B\u0447." })) : (_jsx("button", { type: "button", className: "text-xs text-muted-foreground hover:text-primary", onClick: () => makeDefault(m.id), children: "\u043F\u043E \u0443\u043C\u043E\u043B\u0447\u0430\u043D\u0438\u044E" })) })] }, m.id))), shown.length === 0 && (_jsx("tr", { children: _jsx("td", { className: "p-2 text-muted-foreground", children: "\u043D\u0438\u0447\u0435\u0433\u043E \u043D\u0435 \u043D\u0430\u0439\u0434\u0435\u043D\u043E" }) }))] }) }) }), shown.length > 300 && _jsx("div", { className: "text-xs text-muted-foreground", children: "\u043F\u043E\u043A\u0430\u0437\u0430\u043D\u044B \u043F\u0435\u0440\u0432\u044B\u0435 300 \u2014 \u0443\u0442\u043E\u0447\u043D\u0438\u0442\u0435 \u0444\u0438\u043B\u044C\u0442\u0440" })] })), _jsxs("div", { className: "flex flex-wrap items-center gap-2", children: [_jsx("input", { className: cn(fieldCls, "h-8 max-w-72"), placeholder: "\u0438\u043B\u0438 id \u043C\u043E\u0434\u0435\u043B\u0438 \u0432\u0440\u0443\u0447\u043D\u0443\u044E", value: manual, onChange: (e) => setManual(e.target.value), onKeyDown: (e) => e.key === "Enter" && (e.preventDefault(), addManual()), "aria-label": "id \u043C\u043E\u0434\u0435\u043B\u0438", spellCheck: false }), _jsx(Button, { size: "sm", variant: "ghost", disabled: !manual.trim(), onClick: addManual, children: "\u0434\u043E\u0431\u0430\u0432\u0438\u0442\u044C" })] })] }), _jsxs("div", { className: "flex flex-wrap items-center gap-2 border-t border-border/60 pt-3", children: [_jsx(Button, { disabled: !!busy, onClick: () => void save(key.trim() || undefined), children: busy === "save" ? "сохраняю…" : "сохранить и применить" }), _jsx(Button, { variant: "outline", disabled: !!busy || !st?.ready, onClick: runTest, children: busy === "test" ? "проверяю…" : "проверить связь" }), msg && _jsx("span", { className: cn("text-sm", msg.ok ? "text-primary" : "text-crit"), children: msg.text })] }), test && (_jsx("div", { className: cn("text-sm", test.ok ? "text-primary" : "text-crit"), children: test.ok ? `✓ ${test.model} ответила за ${((test.ms ?? 0) / 1000).toFixed(1)} с: «${test.text}»` : `✕ ${test.error}` })), _jsx("div", { className: "text-xs text-muted-foreground", children: "\u041F\u0440\u0438\u043C\u0435\u043D\u044F\u0435\u0442\u0441\u044F \u0441\u0440\u0430\u0437\u0443, \u0431\u0435\u0437 \u043F\u0435\u0440\u0435\u0437\u0430\u043F\u0443\u0441\u043A\u0430. \u041E\u0442\u043A\u0440\u044B\u0442\u044B\u0435 \u0447\u0430\u0442\u044B \u043F\u0440\u043E\u0434\u043E\u043B\u0436\u0430\u0442 \u0441 \u043D\u043E\u0432\u043E\u0439 \u043C\u043E\u0434\u0435\u043B\u044C\u044E, \u0438\u0441\u0442\u043E\u0440\u0438\u044F \u0441\u043E\u0445\u0440\u0430\u043D\u044F\u0435\u0442\u0441\u044F. \u041E\u0442\u043C\u0435\u0447\u0435\u043D\u043D\u044B\u0435 \u043C\u043E\u0434\u0435\u043B\u0438 \u043C\u043E\u0436\u043D\u043E \u0432\u044B\u0431\u0440\u0430\u0442\u044C \u0434\u043B\u044F \u043E\u0442\u0434\u0435\u043B\u044C\u043D\u043E\u0433\u043E \u0447\u0430\u0442\u0430 \u0432 \u043F\u0430\u043D\u0435\u043B\u0438 \u0430\u0433\u0435\u043D\u0442\u0430." })] }) }));
}
export function SettingsPage({ route, version, onLogout }) {
    useEffect(() => {
        // A dead session shows up as 401 on the first request.
        agentApi.status().catch((e) => e instanceof ApiError && e.status === 401 && onLogout());
    }, [onLogout]);
    return (_jsx(Shell, { route: route, version: version, onLogout: onLogout, footer: _jsxs(_Fragment, { children: [_jsx(Hotkey, { k: "4", children: "\u0430\u0433\u0435\u043D\u0442" }), _jsx(Hotkey, { k: "6", children: "\u043D\u0430\u0441\u0442\u0440\u043E\u0439\u043A\u0438" })] }), children: _jsx("div", { className: "max-w-5xl", children: _jsx(AIPanel, {}) }) }));
}
