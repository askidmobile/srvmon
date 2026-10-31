import { jsx as _jsx, jsxs as _jsxs, Fragment as _Fragment } from "react/jsx-runtime";
import { useEffect, useMemo, useRef, useState } from "react";
import { Hotkey, Shell } from "@/components/Shell";
import { Button } from "@/components/ui/button";
import { Panel } from "@/components/ui/panel";
import { ScrollArea } from "@/components/ui/scroll-area";
import { api, ApiError, } from "@/lib/api";
import { href, navigate } from "@/lib/router";
import { cn } from "@/lib/utils";
const fieldCls = "h-9 w-full min-w-0 border border-input bg-field px-2.5 text-hi placeholder:text-muted-foreground focus-visible:outline-1 focus-visible:outline-ring disabled:opacity-60";
const GROUPS = ["prod", "staging", "infra", "личные"];
const PROVIDERS = ["Hetzner", "Selectel", "Timeweb", "Aeza", "DigitalOcean", "Yandex Cloud"];
const CURRENCIES = ["RUB", "EUR", "USD"];
const PERIODS = [
    { v: "month", l: "месяц" },
    { v: "year", l: "год" },
    { v: "hour", l: "час (почасовой)" },
];
const LOGIN_METHODS = [
    { v: "password", l: "пароль" },
    { v: "key", l: "мой ключ" },
    { v: "panel_key", l: "ключ панели" },
];
const ERRORS = {
    bad_name: "имя: латиница, цифры, . _ -, до 63 символов",
    bad_host: "укажите host (ip или домен)",
    bad_port: "порт 1–65535",
    bad_date: "дата оплаты в формате ГГГГ-ММ-ДД",
    bad_cost: "некорректная стоимость",
    bad_url: "ссылка на панель должна начинаться с https://",
    bad_tag: "тег: до 32 символов, без пробелов и запятых; не больше 20 тегов",
    bad_description: "описание: до 500 символов",
    field_too_long: "слишком длинное значение: проект и группа до 64 символов, тариф до 128",
    bad_period: "некорректный период оплаты",
    too_many_tags: "не больше 20 тегов",
    bad_user: "укажите пользователя",
    password_required: "введите пароль",
    private_key_required: "вставьте приватный ключ",
    public_key_given: "это публичный ключ (.pub): нужен приватный, например ~/.ssh/id_ed25519",
    bad_private_key: "не удалось прочитать приватный ключ (нужен формат OpenSSH или PEM)",
    passphrase_required: "ключ зашифрован: введите его пароль (passphrase)",
    bad_passphrase: "неверный пароль ключа (passphrase)",
    setup_key_expired: "ключ панели устарел (сервер перезапускался или прошёл час): получите новый",
    bad_method: "неизвестный способ входа",
    bad_host_key: "сначала проверьте host key",
    name_taken: "сервер с таким именем уже есть",
    unreachable: "сервер недоступен по ssh",
    busy: "уже идёт несколько установок, подождите",
    no_master_key: "на сервере мониторинга нет мастер-ключа",
    not_found: "сервер не найден",
    onboarding: "для этого сервера уже идёт установка",
};
function errText(e) {
    if (e instanceof ApiError)
        return ERRORS[e.code] ?? `ошибка: ${e.code}`;
    return "нет связи с сервером мониторинга";
}
function Field({ label, htmlFor, children }) {
    return (_jsxs("div", { className: "flex min-w-0 flex-col gap-1", children: [_jsx("label", { className: "text-muted-foreground", htmlFor: htmlFor, children: label }), children] }));
}
function Check({ checked, onChange, disabled, children, }) {
    return (_jsxs("label", { className: cn("flex cursor-pointer items-start gap-2", disabled && "cursor-default opacity-60"), children: [_jsx("input", { type: "checkbox", className: "sr-only", checked: checked, disabled: disabled, onChange: (e) => onChange(e.target.checked) }), _jsx("span", { className: checked ? "text-primary" : "text-muted-foreground", "aria-hidden": true, children: checked ? "[x]" : "[ ]" }), _jsx("span", { children: children })] }));
}
function TagInput({ tags, onChange, id }) {
    const [draft, setDraft] = useState("");
    const add = (raw) => {
        const t = raw.trim().replace(/^#/, "");
        if (t && !tags.includes(t))
            onChange([...tags, t]);
        setDraft("");
    };
    return (_jsxs("div", { className: "flex min-h-9 flex-wrap items-center gap-1.5 border border-input bg-field px-1.5 focus-within:outline-1 focus-within:outline-ring", children: [tags.map((t) => (_jsxs("button", { type: "button", className: "inline-flex h-[22px] items-center border border-border px-1.5 text-xs text-[#9fc3a7] hover:border-crit hover:text-crit", onClick: () => onChange(tags.filter((x) => x !== t)), "aria-label": `убрать тег ${t}`, children: ["#", t, " \u00D7"] }, t))), _jsx("input", { id: id, value: draft, placeholder: "+ \u0442\u0435\u0433", className: "h-[30px] min-w-[60px] flex-1 bg-transparent text-hi outline-none placeholder:text-muted-foreground", onChange: (e) => {
                    const v = e.target.value;
                    if (v.endsWith(",") || v.endsWith(" "))
                        add(v.slice(0, -1));
                    else
                        setDraft(v);
                }, onKeyDown: (e) => {
                    if (e.key === "Enter") {
                        e.preventDefault();
                        add(draft);
                    }
                    else if (e.key === "Backspace" && draft === "" && tags.length) {
                        onChange(tags.slice(0, -1));
                    }
                }, onBlur: () => draft && add(draft) })] }));
}
const MARK = {
    ok: { c: "text-primary", m: "✓" },
    run: { c: "text-hi", m: "⟳" },
    warn: { c: "text-warn", m: "!" },
    fail: { c: "text-crit", m: "✗" },
    info: { c: "text-muted-foreground", m: "·" },
};
function emptyCard() {
    return {
        name: "",
        host: "",
        port: 22,
        project: "",
        group: "prod",
        tags: [],
        description: "",
        provider: "",
        plan: "",
        cost: null,
        currency: "RUB",
        period: "month",
        next_payment: "",
        panel_url: "",
    };
}
// "1.2.3.4:2222" or "[::1]:22" -> host, port
function splitHostPort(v) {
    const s = v.trim();
    const m6 = s.match(/^\[([^\]]+)\](?::(\d+))?$/);
    if (m6)
        return { host: m6[1], port: Number(m6[2] ?? 22) };
    const parts = s.split(":");
    if (parts.length === 2 && /^\d+$/.test(parts[1]))
        return { host: parts[0], port: Number(parts[1]) };
    return { host: s, port: 22 };
}
export function AddServer({ attachId, route, version, onLogout, }) {
    const [servers, setServers] = useState([]);
    const [card, setCard] = useState(emptyCard);
    const [hostPort, setHostPort] = useState("");
    const [costText, setCostText] = useState("");
    const [user, setUser] = useState("root");
    const [password, setPassword] = useState("");
    const [method, setMethod] = useState("password");
    const [privateKey, setPrivateKey] = useState("");
    const [passphrase, setPassphrase] = useState("");
    const [setupKey, setSetupKey] = useState(null);
    const [copied, setCopied] = useState(false);
    const [sudo, setSudo] = useState(true);
    const [disablePassword, setDisablePassword] = useState(true);
    const [installAgent, setInstallAgent] = useState(true);
    const [probe, setProbe] = useState(null);
    const [probing, setProbing] = useState(false);
    const [confirmed, setConfirmed] = useState(false);
    const [error, setError] = useState("");
    const [phase, setPhase] = useState("form");
    const [job, setJob] = useState("");
    const [lines, setLines] = useState([]);
    const [serverId, setServerId] = useState(0);
    const [committed, setCommitted] = useState(false);
    const logEnd = useRef(null);
    const attached = useRef(false);
    useEffect(() => {
        api
            .servers()
            .then(setServers)
            .catch((e) => e?.status === 401 && onLogout());
    }, [onLogout]);
    const existing = attachId ? servers.find((s) => s.id === attachId) : undefined;
    useEffect(() => {
        if (!existing || attached.current)
            return;
        attached.current = true;
        setCard({
            ...emptyCard(),
            name: existing.name,
            host: existing.host,
            port: existing.port || 22,
            project: existing.project,
            group: existing.group,
            tags: existing.tags,
            description: existing.description,
        });
        if (existing.host) {
            const h = existing.host.includes(":") ? `[${existing.host}]` : existing.host; // IPv6 literal
            setHostPort(existing.port && existing.port !== 22 ? `${h}:${existing.port}` : h);
        }
        setInstallAgent(!existing.last_seen);
    }, [existing]);
    const projects = useMemo(() => [...new Set(servers.map((s) => s.project).filter(Boolean))], [servers]);
    const groups = useMemo(() => [...new Set([...GROUPS, ...servers.map((s) => s.group).filter(Boolean)])], [servers]);
    const providers = useMemo(() => [...new Set([...PROVIDERS, ...servers.map((s) => s.provider).filter(Boolean)])], [servers]);
    const set = (k, v) => setCard((c) => ({ ...c, [k]: v }));
    const locked = phase !== "form";
    // Any change of address invalidates the confirmed host key.
    function changeHostPort(v) {
        setHostPort(v);
        setProbe(null);
        setConfirmed(false);
    }
    async function doProbe() {
        setError("");
        const { host, port } = splitHostPort(hostPort);
        if (!host) {
            setError(ERRORS.bad_host);
            return;
        }
        setProbing(true);
        try {
            setProbe(await api.probe(host, port));
            setConfirmed(false);
        }
        catch (e) {
            setError(errText(e));
        }
        finally {
            setProbing(false);
        }
    }
    async function submit(e) {
        e.preventDefault();
        setError("");
        if (!probe) {
            await doProbe();
            return;
        }
        if (!confirmed) {
            setError("сверьте fingerprint с сервером и подтвердите его");
            return;
        }
        const { host, port } = splitHostPort(hostPort);
        const cost = costText.trim() === "" ? null : Number(costText.replace(",", "."));
        if (cost !== null && !Number.isFinite(cost)) {
            setError(ERRORS.bad_cost);
            return;
        }
        // Secrets live only in this request.
        const pw = password;
        const pk = privateKey;
        const pp = passphrase;
        setPassword("");
        setPrivateKey("");
        setPassphrase("");
        try {
            const r = await api.onboard({
                server_id: attachId ?? 0,
                card: { ...card, host, port, cost },
                user,
                method,
                password: method === "password" || user !== "root" ? pw : "",
                private_key: method === "key" ? pk : "",
                passphrase: method === "key" ? pp : "",
                setup_key: method === "panel_key" ? (setupKey?.id ?? "") : "",
                host_key: probe.host_key,
                sudo,
                disable_password: disablePassword,
                install_agent: installAgent,
            });
            setJob(r.job);
            setServerId(r.server_id);
            setLines([]);
            setCommitted(false);
            setPhase("running");
        }
        catch (err) {
            setError(errText(err));
            // The panel key is gone (expired or the server restarted): offer a new one.
            if (err instanceof ApiError && err.code === "setup_key_expired")
                setSetupKey(null);
        }
    }
    // Poll the onboarding log.
    useEffect(() => {
        if (phase !== "running" || !job)
            return;
        let next = 0;
        let alive = true;
        const tick = async () => {
            try {
                const st = await api.onboardStatus(job, next);
                if (!alive)
                    return;
                next = st.next;
                if (st.lines.length)
                    setLines((l) => [...l, ...st.lines]);
                setCommitted(st.committed);
                if (st.done) {
                    setPhase(st.ok ? "done" : "failed");
                    return;
                }
            }
            catch (e) {
                if (e?.status === 401)
                    return onLogout();
            }
            if (alive)
                timer = setTimeout(tick, 1000);
        };
        let timer = setTimeout(tick, 300);
        return () => {
            alive = false;
            clearTimeout(timer);
        };
    }, [phase, job, onLogout]);
    useEffect(() => {
        // Braces matter: newer browsers return a Promise from scrollIntoView.
        logEnd.current?.scrollIntoView({ block: "nearest" });
    }, [lines]);
    const loginReady = method === "password" ? !!password : method === "key" ? !!privateKey.trim() : !!setupKey;
    async function getSetupKey() {
        setError("");
        try {
            setSetupKey(await api.setupKey());
            setCopied(false);
        }
        catch (e) {
            setError(errText(e));
        }
    }
    const sudoField = (_jsx(Field, { label: user === "root" ? "пароль sudo (не нужен для root)" : "пароль sudo, если sudo его спрашивает", htmlFor: "f-sudo", children: _jsx("input", { id: "f-sudo", type: "password", className: fieldCls, value: password, autoComplete: "new-password", disabled: user === "root", onChange: (e) => setPassword(e.target.value) }) }));
    const stepOn = (n) => {
        if (phase !== "form")
            return n === 4;
        if (!card.name || !hostPort)
            return n === 1;
        if (!loginReady)
            return n === 3 || n === 2;
        return n === 4;
    };
    const steps = ["1 описание", "2 оплата", "3 доступ", "4 ключ и агент"];
    return (_jsx(Shell, { route: route, version: version, onLogout: onLogout, footer: _jsxs(_Fragment, { children: [_jsx(Hotkey, { k: "tab", children: "\u043F\u043E\u043B\u0435" }), _jsx(Hotkey, { k: "\u23CE", children: "\u0434\u0430\u043B\u0435\u0435" })] }), footerNote: "\u0430\u0433\u0435\u043D\u0442 \u043F\u043E\u043B\u0443\u0447\u0430\u0435\u0442 \u0441\u0432\u043E\u0439 \u0442\u043E\u043A\u0435\u043D \u0434\u043B\u044F push-\u043C\u0435\u0442\u0440\u0438\u043A \u00B7 ssh-\u043A\u043B\u044E\u0447 \u0445\u0440\u0430\u043D\u0438\u0442\u0441\u044F \u0437\u0430\u0448\u0438\u0444\u0440\u043E\u0432\u0430\u043D\u043D\u044B\u043C", children: _jsxs("form", { onSubmit: submit, className: "mx-auto flex w-full max-w-[1320px] flex-1 flex-col gap-7", children: [_jsxs("div", { className: "flex flex-wrap items-baseline gap-x-4 gap-y-2", children: [_jsx("h1", { className: "text-xl font-bold text-hi", children: existing ? `+ ssh-доступ: ${existing.name}` : "+ новый сервер" }), _jsx("div", { className: "flex flex-wrap items-baseline gap-1", children: steps.map((s, i) => (_jsxs("span", { className: "flex items-baseline gap-1", children: [i > 0 && _jsx("span", { className: "text-muted-foreground", children: "\u2500\u2500" }), _jsxs("span", { className: cn(stepOn(i + 1) ? "bg-[#12361d] px-1.5 text-hi" : "text-primary"), children: ["[", s, "]"] })] }, s))) })] }), _jsxs("section", { className: "grid gap-6 [grid-template-columns:repeat(auto-fit,minmax(300px,1fr))]", children: [_jsx(Panel, { title: "1 \u00B7 \u043E\u043F\u0438\u0441\u0430\u043D\u0438\u0435", accent: stepOn(1), children: _jsxs("fieldset", { disabled: locked, className: "flex min-w-0 flex-col gap-2.5", children: [_jsxs("div", { className: "grid grid-cols-2 gap-2.5", children: [_jsx(Field, { label: "\u0438\u043C\u044F", htmlFor: "f-name", children: _jsx("input", { id: "f-name", className: fieldCls, value: card.name, required: true, disabled: !!existing, placeholder: "shop-db", autoFocus: !attachId, onChange: (e) => set("name", e.target.value) }) }), _jsx(Field, { label: "host : port", htmlFor: "f-host", children: _jsx("input", { id: "f-host", className: fieldCls, value: hostPort, required: true, placeholder: "95.1.2.3:22", onChange: (e) => changeHostPort(e.target.value) }) })] }), _jsxs("div", { className: "grid grid-cols-2 gap-2.5", children: [_jsxs(Field, { label: "\u043F\u0440\u043E\u0435\u043A\u0442", htmlFor: "f-project", children: [_jsx("input", { id: "f-project", maxLength: 64, className: fieldCls, list: "dl-projects", value: card.project, disabled: !!existing, placeholder: "shop", onChange: (e) => set("project", e.target.value) }), _jsx("datalist", { id: "dl-projects", children: projects.map((p) => (_jsx("option", { value: p }, p))) })] }), _jsxs(Field, { label: "\u0433\u0440\u0443\u043F\u043F\u0430", htmlFor: "f-group", children: [_jsx("input", { id: "f-group", maxLength: 64, className: fieldCls, list: "dl-groups", value: card.group, disabled: !!existing, onChange: (e) => set("group", e.target.value) }), _jsx("datalist", { id: "dl-groups", children: groups.map((g) => (_jsx("option", { value: g }, g))) })] })] }), !existing && (_jsxs(_Fragment, { children: [_jsx(Field, { label: "\u0442\u0435\u0433\u0438", htmlFor: "f-tags", children: _jsx(TagInput, { id: "f-tags", tags: card.tags, onChange: (t) => set("tags", t) }) }), _jsx(Field, { label: "\u043E\u043F\u0438\u0441\u0430\u043D\u0438\u0435", htmlFor: "f-desc", children: _jsx("textarea", { id: "f-desc", maxLength: 500, className: cn(fieldCls, "h-16 resize-y py-2"), value: card.description, placeholder: "Postgres \u043C\u0430\u0433\u0430\u0437\u0438\u043D\u0430, \u0440\u0435\u043F\u043B\u0438\u043A\u0430 \u043D\u0430 shop-db-2", onChange: (e) => set("description", e.target.value) }) })] }))] }) }), !existing && (_jsx(Panel, { title: "2 \u00B7 \u043F\u0440\u043E\u0432\u0430\u0439\u0434\u0435\u0440 \u0438 \u043E\u043F\u043B\u0430\u0442\u0430", accent: stepOn(2), children: _jsxs("fieldset", { disabled: locked, className: "flex min-w-0 flex-col gap-2.5", children: [_jsxs("div", { className: "grid grid-cols-2 gap-2.5", children: [_jsxs(Field, { label: "\u043F\u0440\u043E\u0432\u0430\u0439\u0434\u0435\u0440", htmlFor: "f-prov", children: [_jsx("input", { id: "f-prov", maxLength: 64, className: fieldCls, list: "dl-prov", value: card.provider, placeholder: "Hetzner", onChange: (e) => set("provider", e.target.value) }), _jsx("datalist", { id: "dl-prov", children: providers.map((p) => (_jsx("option", { value: p }, p))) })] }), _jsx(Field, { label: "\u0442\u0430\u0440\u0438\u0444", htmlFor: "f-plan", children: _jsx("input", { id: "f-plan", maxLength: 128, className: fieldCls, value: card.plan, placeholder: "CPX31", onChange: (e) => set("plan", e.target.value) }) })] }), _jsxs("div", { className: "grid grid-cols-2 gap-2.5", children: [_jsx(Field, { label: "\u0441\u0442\u043E\u0438\u043C\u043E\u0441\u0442\u044C", htmlFor: "f-cost", children: _jsx("input", { id: "f-cost", className: fieldCls, inputMode: "decimal", value: costText, placeholder: "15.90", onChange: (e) => setCostText(e.target.value) }) }), _jsx(Field, { label: "\u0432\u0430\u043B\u044E\u0442\u0430", htmlFor: "f-cur", children: _jsx("select", { id: "f-cur", className: fieldCls, value: card.currency, onChange: (e) => set("currency", e.target.value), children: CURRENCIES.map((c) => (_jsx("option", { children: c }, c))) }) })] }), _jsxs("div", { className: "grid grid-cols-2 gap-2.5", children: [_jsx(Field, { label: "\u043F\u0435\u0440\u0438\u043E\u0434", htmlFor: "f-period", children: _jsx("select", { id: "f-period", className: fieldCls, value: card.period, onChange: (e) => set("period", e.target.value), children: PERIODS.map((p) => (_jsx("option", { value: p.v, children: p.l }, p.v))) }) }), _jsx(Field, { label: "\u0441\u043B\u0435\u0434. \u043E\u043F\u043B\u0430\u0442\u0430", htmlFor: "f-next", children: _jsx("input", { id: "f-next", type: "date", className: fieldCls, value: card.next_payment, onChange: (e) => set("next_payment", e.target.value) }) })] }), _jsx(Field, { label: "\u0441\u0441\u044B\u043B\u043A\u0430 \u043D\u0430 \u043F\u0430\u043D\u0435\u043B\u044C \u043F\u0440\u043E\u0432\u0430\u0439\u0434\u0435\u0440\u0430", htmlFor: "f-url", children: _jsx("input", { id: "f-url", maxLength: 200, type: "url", className: fieldCls, value: card.panel_url, placeholder: "https://console.hetzner.cloud/\u2026", onChange: (e) => set("panel_url", e.target.value) }) }), _jsx("div", { className: "text-xs text-muted-foreground", children: "\u043D\u0430\u043F\u043E\u043C\u0438\u043D\u0430\u043D\u0438\u0435 \u0432 tg \u0437\u0430 3 \u0434\u043D\u044F \u0434\u043E \u043E\u043F\u043B\u0430\u0442\u044B \u00B7 \u0448\u0430\u0433 \u043C\u043E\u0436\u043D\u043E \u043F\u0440\u043E\u043F\u0443\u0441\u0442\u0438\u0442\u044C" })] }) })), _jsx(Panel, { title: "3 \u00B7 \u0440\u0430\u0437\u043E\u0432\u044B\u0439 \u0434\u043E\u0441\u0442\u0443\u043F", accent: stepOn(3), children: _jsxs("fieldset", { disabled: locked, className: "flex min-w-0 flex-col gap-2.5", children: [_jsx(Field, { label: "\u043F\u043E\u043B\u044C\u0437\u043E\u0432\u0430\u0442\u0435\u043B\u044C", htmlFor: "f-user", children: _jsx("input", { id: "f-user", className: fieldCls, value: user, autoComplete: "off", onChange: (e) => setUser(e.target.value) }) }), _jsxs("div", { className: "flex flex-col gap-1", children: [_jsx("span", { className: "text-muted-foreground", children: "\u0432\u0445\u043E\u0434" }), _jsx("div", { className: "flex flex-wrap gap-1", role: "radiogroup", "aria-label": "\u0441\u043F\u043E\u0441\u043E\u0431 \u0432\u0445\u043E\u0434\u0430", children: LOGIN_METHODS.map((m) => (_jsx(Button, { type: "button", size: "sm", role: "radio", "aria-checked": method === m.v, variant: method === m.v ? "default" : "outline", onClick: () => {
                                                        setMethod(m.v);
                                                        setError("");
                                                    }, children: m.l }, m.v))) })] }), method === "password" && (_jsxs(_Fragment, { children: [_jsx(Field, { label: "\u043F\u0430\u0440\u043E\u043B\u044C (\u043E\u0434\u0438\u043D \u0440\u0430\u0437)", htmlFor: "f-pw", children: _jsx("input", { id: "f-pw", type: "password", className: fieldCls, value: password, autoComplete: "new-password", onChange: (e) => setPassword(e.target.value) }) }), _jsx("div", { className: "text-xs text-warn", children: "\u043F\u0430\u0440\u043E\u043B\u044C \u043D\u0443\u0436\u0435\u043D \u0442\u043E\u043B\u044C\u043A\u043E \u0434\u043B\u044F \u0443\u0441\u0442\u0430\u043D\u043E\u0432\u043A\u0438 \u043A\u043B\u044E\u0447\u0430: \u043D\u0435 \u043F\u0438\u0448\u0435\u0442\u0441\u044F \u0432 \u0431\u0434 \u0438 \u043B\u043E\u0433\u0438, \u043F\u043E\u0441\u043B\u0435 \u0448\u0430\u0433\u0430 4 \u0441\u0442\u0438\u0440\u0430\u0435\u0442\u0441\u044F. \u041F\u043E\u0434\u043E\u0439\u0434\u0451\u0442 root \u0438\u043B\u0438 \u043F\u043E\u043B\u044C\u0437\u043E\u0432\u0430\u0442\u0435\u043B\u044C \u0441 sudo. \u0412\u0445\u043E\u0434 \u043F\u043E \u043F\u0430\u0440\u043E\u043B\u044E \u043D\u0430 \u0441\u0435\u0440\u0432\u0435\u0440\u0435 \u0432\u044B\u043A\u043B\u044E\u0447\u0435\u043D? \u0412\u044B\u0431\u0435\u0440\u0438\u0442\u0435 \u00AB\u043C\u043E\u0439 \u043A\u043B\u044E\u0447\u00BB \u0438\u043B\u0438 \u00AB\u043A\u043B\u044E\u0447 \u043F\u0430\u043D\u0435\u043B\u0438\u00BB." })] })), method === "key" && (_jsxs(_Fragment, { children: [_jsx(Field, { label: "\u043F\u0440\u0438\u0432\u0430\u0442\u043D\u044B\u0439 \u043A\u043B\u044E\u0447 (\u043E\u0434\u0438\u043D \u0440\u0430\u0437)", htmlFor: "f-pk", children: _jsx("textarea", { id: "f-pk", className: cn(fieldCls, "h-24 resize-y py-1.5 font-mono text-xs"), value: privateKey, spellCheck: false, autoComplete: "off", placeholder: "-----BEGIN OPENSSH PRIVATE KEY-----\n…", onChange: (e) => setPrivateKey(e.target.value) }) }), _jsx(Field, { label: "\u043F\u0430\u0440\u043E\u043B\u044C \u043A\u043B\u044E\u0447\u0430, \u0435\u0441\u043B\u0438 \u0435\u0441\u0442\u044C", htmlFor: "f-pp", children: _jsx("input", { id: "f-pp", type: "password", className: fieldCls, value: passphrase, autoComplete: "new-password", onChange: (e) => setPassphrase(e.target.value) }) }), sudoField, _jsx("div", { className: "text-xs text-warn", children: "\u043D\u0443\u0436\u0435\u043D \u043F\u0440\u0438\u0432\u0430\u0442\u043D\u044B\u0439 \u043A\u043B\u044E\u0447, \u043A\u043E\u0442\u043E\u0440\u044B\u043C \u0432\u044B \u0441\u0430\u043C\u0438 \u0437\u0430\u0445\u043E\u0434\u0438\u0442\u0435 \u043D\u0430 \u0441\u0435\u0440\u0432\u0435\u0440 (\u043D\u0430\u043F\u0440\u0438\u043C\u0435\u0440 ~/.ssh/id_ed25519, \u043D\u0435 .pub). \u041E\u043D \u0438\u0441\u043F\u043E\u043B\u044C\u0437\u0443\u0435\u0442\u0441\u044F \u043E\u0434\u0438\u043D \u0440\u0430\u0437 \u0434\u043B\u044F \u0443\u0441\u0442\u0430\u043D\u043E\u0432\u043A\u0438 \u043A\u043B\u044E\u0447\u0430 srvmon: \u043D\u0435 \u043F\u0438\u0448\u0435\u0442\u0441\u044F \u0432 \u0431\u0434 \u0438 \u043B\u043E\u0433\u0438, \u043F\u043E\u0441\u043B\u0435 \u0448\u0430\u0433\u0430 4 \u0441\u0442\u0438\u0440\u0430\u0435\u0442\u0441\u044F." })] })), method === "panel_key" && (_jsxs(_Fragment, { children: [!setupKey ? (_jsx(Button, { type: "button", variant: "outline", onClick: getSetupKey, children: "\u043F\u043E\u043A\u0430\u0437\u0430\u0442\u044C \u043A\u043B\u044E\u0447 \u043F\u0430\u043D\u0435\u043B\u0438" })) : (_jsxs("div", { className: "flex flex-col gap-1.5", children: [_jsxs("span", { className: "text-muted-foreground", children: ["\u0432\u044B\u043F\u043E\u043B\u043D\u0438\u0442\u0435 \u043D\u0430 \u0441\u0435\u0440\u0432\u0435\u0440\u0435 \u043F\u043E\u0434 \u043F\u043E\u043B\u044C\u0437\u043E\u0432\u0430\u0442\u0435\u043B\u0435\u043C ", _jsx("span", { className: "text-hi", children: user || "root" }), ":"] }), _jsx("pre", { className: "max-h-28 overflow-auto border border-border bg-field p-2 text-xs break-all whitespace-pre-wrap text-hi", children: setupKey.command }), _jsxs("div", { className: "flex flex-wrap items-center gap-2", children: [_jsx(Button, { type: "button", size: "sm", variant: "outline", onClick: () => navigator.clipboard
                                                                    ?.writeText(setupKey.command)
                                                                    .then(() => setCopied(true))
                                                                    .catch(() => undefined), children: copied ? "скопировано ✓" : "копировать" }), _jsx(Button, { type: "button", size: "sm", variant: "ghost", onClick: getSetupKey, children: "\u043D\u043E\u0432\u044B\u0439 \u043A\u043B\u044E\u0447" }), _jsx("span", { className: "min-w-0 text-xs break-all text-muted-foreground", children: setupKey.fingerprint })] })] })), sudoField, _jsxs("div", { className: "text-xs text-warn", children: ["\u043F\u0443\u0431\u043B\u0438\u0447\u043D\u044B\u0439 \u043A\u043B\u044E\u0447 \u043F\u0430\u043D\u0435\u043B\u0438 \u0434\u043E\u0431\u0430\u0432\u043B\u044F\u0435\u0442\u0441\u044F \u0432 authorized_keys, \u043F\u043E\u0441\u043B\u0435 \u0443\u0441\u0442\u0430\u043D\u043E\u0432\u043A\u0438 \u043E\u043D \u043E\u0441\u0442\u0430\u0451\u0442\u0441\u044F \u0442\u043E\u043B\u044C\u043A\u043E \u0443 \u043F\u043E\u043B\u044C\u0437\u043E\u0432\u0430\u0442\u0435\u043B\u044F srvmon, \u0430 \u0443 ", user || "root", " \u0443\u0431\u0438\u0440\u0430\u0435\u0442\u0441\u044F. \u041F\u0440\u0438\u0432\u0430\u0442\u043D\u0430\u044F \u0447\u0430\u0441\u0442\u044C \u043D\u0435 \u043F\u043E\u043A\u0438\u0434\u0430\u0435\u0442 \u0441\u0435\u0440\u0432\u0435\u0440 \u043C\u043E\u043D\u0438\u0442\u043E\u0440\u0438\u043D\u0433\u0430."] })] }))] }) }), _jsx(Panel, { title: "4 \u00B7 \u043A\u043B\u044E\u0447 \u0438 \u0430\u0433\u0435\u043D\u0442", accent: stepOn(4), children: _jsxs("div", { className: "flex flex-col gap-1.5", children: [_jsxs("div", { children: [_jsx("span", { className: "text-muted-foreground", children: "\u043A\u043B\u044E\u0447 " }), " ed25519, \u0433\u0435\u043D\u0435\u0440\u0438\u0440\u0443\u0435\u0442\u0441\u044F \u043D\u0430 \u0441\u0435\u0440\u0432\u0435\u0440\u0435 \u043C\u043E\u043D\u0438\u0442\u043E\u0440\u0438\u043D\u0433\u0430"] }), _jsxs("div", { children: [_jsx("span", { className: "text-muted-foreground", children: "\u044E\u0437\u0435\u0440 " }), " srvmon (\u0433\u0440\u0443\u043F\u043F\u0430 docker)"] }), _jsxs("div", { className: "break-all", children: [_jsx("span", { className: "text-muted-foreground", children: "host " }), probe ? (_jsxs("span", { className: "text-hi", children: [probe.type, " ", probe.fingerprint] })) : (_jsx("span", { className: "text-muted-foreground", children: "\u043D\u0435 \u043F\u0440\u043E\u0432\u0435\u0440\u0435\u043D" }))] }), probe && (_jsx("div", { className: "mb-1", children: _jsxs(Check, { checked: confirmed, onChange: setConfirmed, disabled: locked, children: ["fingerprint \u0441\u043E\u0432\u043F\u0430\u0434\u0430\u0435\u0442 \u0441 \u0441\u0435\u0440\u0432\u0435\u0440\u043E\u043C", " ", _jsx("span", { className: "text-xs text-muted-foreground", children: "(ssh-keygen -lf /etc/ssh/ssh_host_*_key.pub)" })] }) })), _jsxs("div", { className: "mt-1.5 flex flex-col gap-1.5", children: [_jsx(Check, { checked: installAgent, onChange: setInstallAgent, disabled: locked, children: "\u0443\u0441\u0442\u0430\u043D\u043E\u0432\u0438\u0442\u044C srvmon-agent" }), _jsxs(Check, { checked: sudo, onChange: setSudo, disabled: locked, children: ["sudo \u0431\u0435\u0437 \u043F\u0430\u0440\u043E\u043B\u044F \u0434\u043B\u044F srvmon ", _jsx("span", { className: "text-xs text-muted-foreground", children: "(root \u0432 \u0442\u0435\u0440\u043C\u0438\u043D\u0430\u043B\u0435)" })] }), _jsx(Check, { checked: disablePassword, onChange: setDisablePassword, disabled: locked, children: "\u043E\u0442\u043A\u043B\u044E\u0447\u0438\u0442\u044C \u0432\u0445\u043E\u0434 \u043F\u043E \u043F\u0430\u0440\u043E\u043B\u044E" })] })] }) })] }), _jsxs(Panel, { title: "\u0436\u0443\u0440\u043D\u0430\u043B \u0443\u0441\u0442\u0430\u043D\u043E\u0432\u043A\u0438", accent: phase !== "form", children: [_jsx(ScrollArea, { className: "max-h-[320px] border border-border bg-field", children: _jsxs("div", { className: "px-3 py-2.5 leading-normal", children: [lines.length === 0 && phase === "form" && (_jsx("div", { className: "text-muted-foreground", children: probe
                                            ? "○ сверьте fingerprint и нажмите «добавить»"
                                            : "○ заполните имя и адрес, затем «проверить host key»" })), lines.map((l, i) => (_jsxs("div", { className: "whitespace-pre-wrap", children: [_jsx("span", { className: MARK[l.state].c, children: MARK[l.state].m }), " ", l.text] }, i))), phase === "running" && _jsx("div", { className: "text-muted-foreground", children: "\u25CB \u0432\u044B\u043F\u043E\u043B\u043D\u044F\u0435\u0442\u0441\u044F\u2026" }), _jsx("div", { ref: logEnd })] }) }), error && (_jsx("div", { className: "mt-3 text-crit", role: "alert", children: error })), _jsxs("div", { className: "mt-3.5 flex flex-wrap gap-3", children: [phase === "form" && (_jsxs(_Fragment, { children: [_jsx(Button, { type: "button", variant: "outline", onClick: () => navigate(href.overview()), children: "\u2190 \u043D\u0430\u0437\u0430\u0434" }), _jsx(Button, { type: "button", variant: "outline", onClick: doProbe, disabled: probing || !hostPort, children: probing ? "проверяем…" : probe ? "проверить заново" : "проверить host key" }), _jsx("span", { className: "flex-1" }), _jsx(Button, { type: "submit", disabled: probing || (!!probe && (!confirmed || !loginReady || !card.name)), children: probe ? (existing ? "подключить ⏎" : "добавить сервер ⏎") : "далее ⏎" })] })), phase === "running" && !committed && (_jsx(Button, { type: "button", variant: "destructive", onClick: () => api.cancelOnboard(job).catch(() => undefined), children: "\u043E\u0442\u043C\u0435\u043D\u0430 (\u043E\u0442\u043A\u0430\u0442)" })), phase === "failed" && (_jsxs(_Fragment, { children: [_jsx(Button, { type: "button", variant: "outline", onClick: () => {
                                                setPhase("form");
                                                setJob("");
                                            }, children: "\u2190 \u0438\u0441\u043F\u0440\u0430\u0432\u0438\u0442\u044C \u0438 \u043F\u043E\u0432\u0442\u043E\u0440\u0438\u0442\u044C" }), _jsx("span", { className: "flex-1" }), _jsx(Button, { type: "button", variant: "outline", onClick: () => navigate(href.overview()), children: "\u043A \u043E\u0431\u0437\u043E\u0440\u0443" })] })), phase === "done" && (_jsxs(_Fragment, { children: [_jsx(Button, { type: "button", variant: "outline", onClick: () => navigate(href.overview()), children: "\u043A \u043E\u0431\u0437\u043E\u0440\u0443" }), _jsx("span", { className: "flex-1" }), _jsx(Button, { type: "button", onClick: () => navigate(href.terminal(serverId)), children: "\u043E\u0442\u043A\u0440\u044B\u0442\u044C \u0442\u0435\u0440\u043C\u0438\u043D\u0430\u043B \u2192" })] }))] })] })] }) }));
}
