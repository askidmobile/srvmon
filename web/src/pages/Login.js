import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Panel } from "@/components/ui/panel";
import { api, ApiError } from "@/lib/api";
const LOGO = String.raw `  ___ _ ____   ___ __ ___   ___  _ __
 / __| '__\ \ / / '_ ${"`"} _ \ / _ \| '_ \
 \__ \ |   \ V /| | | | | | (_) | | | |
 |___/_|    \_/ |_| |_| |_|\___/|_| |_|`;
function errorText(e) {
    if (!(e instanceof ApiError))
        return "сервер недоступен";
    switch (e.code) {
        case "invalid_password":
            return "неверный пароль";
        case "invalid_code":
            return "неверный код";
        case "password_required":
            return "время шага истекло, введите пароль снова";
        case "too_many_attempts":
            return `слишком много попыток, повторите через ${Math.ceil((e.retryAfter ?? 600) / 60)} мин`;
        case "not_initialized":
            return "панель не настроена: выполните srvmon-server init на сервере";
        default:
            return `ошибка: ${e.code}`;
    }
}
function secondsLeft() {
    return 30 - (Math.floor(Date.now() / 1000) % 30);
}
export function Login({ onSuccess }) {
    const [step, setStep] = useState("password");
    const [password, setPassword] = useState("");
    const [code, setCode] = useState("");
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState("");
    const [left, setLeft] = useState(secondsLeft());
    const codeRef = useRef(null);
    useEffect(() => {
        const t = setInterval(() => setLeft(secondsLeft()), 1000);
        return () => clearInterval(t);
    }, []);
    useEffect(() => {
        if (step === "totp")
            codeRef.current?.focus();
    }, [step]);
    async function submitPassword(e) {
        e.preventDefault();
        setBusy(true);
        setError("");
        try {
            await api.password(password);
            setPassword("");
            setStep("totp");
        }
        catch (err) {
            setError(errorText(err));
        }
        finally {
            setBusy(false);
        }
    }
    async function submitCode(e) {
        e.preventDefault();
        setBusy(true);
        setError("");
        try {
            await api.totp(code.replace(/\s/g, ""));
            onSuccess();
        }
        catch (err) {
            setCode("");
            if (err instanceof ApiError && err.code === "password_required")
                setStep("password");
            setError(errorText(err));
        }
        finally {
            setBusy(false);
        }
    }
    return (_jsx("main", { className: "flex min-h-screen items-center justify-center p-6", children: _jsxs("div", { className: "flex w-full max-w-[480px] flex-col items-center gap-7", children: [_jsx("pre", { "aria-hidden": "true", className: "m-0 text-xs leading-tight text-primary", children: LOGO }), step === "password" ? (_jsx(Panel, { title: "\u0432\u0445\u043E\u0434 \u2500\u2500 \u0448\u0430\u0433 1/2", accent: true, className: "w-full", children: _jsxs("form", { onSubmit: submitPassword, className: "flex flex-col gap-3.5", children: [_jsx("label", { htmlFor: "pw", className: "text-muted-foreground", children: "\u043F\u0430\u0440\u043E\u043B\u044C" }), _jsx(Input, { id: "pw", type: "password", autoComplete: "current-password", autoFocus: true, value: password, onChange: (e) => setPassword(e.target.value) }), _jsx(Button, { type: "submit", size: "lg", disabled: busy || !password, children: "\u043F\u0440\u043E\u0434\u043E\u043B\u0436\u0438\u0442\u044C \u23CE" }), error && _jsx("p", { className: "m-0 text-crit", children: error }), _jsx("p", { className: "m-0 text-xs text-muted-foreground", children: "\u043B\u043E\u0433\u0438\u043D\u0430 \u043D\u0435\u0442 \u00B7 5 \u043D\u0435\u0432\u0435\u0440\u043D\u044B\u0445 \u043F\u043E\u043F\u044B\u0442\u043E\u043A \u2192 \u0431\u0430\u043D ip \u043D\u0430 1\u0447 (fail2ban)" })] }) })) : (_jsx(Panel, { title: "\u0432\u0445\u043E\u0434 \u2500\u2500 \u0448\u0430\u0433 2/2 \u00B7 google authenticator", accent: true, className: "w-full", children: _jsxs("form", { onSubmit: submitCode, className: "flex flex-col gap-3.5", children: [_jsx("label", { htmlFor: "code", className: "text-muted-foreground", children: "6-\u0437\u043D\u0430\u0447\u043D\u044B\u0439 \u043A\u043E\u0434 \u0438\u0437 \u043F\u0440\u0438\u043B\u043E\u0436\u0435\u043D\u0438\u044F \u0438\u043B\u0438 \u0440\u0435\u0437\u0435\u0440\u0432\u043D\u044B\u0439 \u043A\u043E\u0434" }), _jsx(Input, { id: "code", ref: codeRef, inputMode: "numeric", autoComplete: "one-time-code", className: "text-center text-2xl tracking-[12px]", value: code, onChange: (e) => setCode(e.target.value) }), _jsx(Button, { type: "submit", size: "lg", disabled: busy || !code, children: "\u0432\u043E\u0439\u0442\u0438 \u23CE" }), error && _jsx("p", { className: "m-0 text-crit", children: error }), _jsxs("div", { className: "flex flex-wrap justify-between gap-2", children: [_jsx(Button, { type: "button", variant: "ghost", className: "px-0", onClick: () => {
                                            setStep("password");
                                            setError("");
                                        }, children: "\u2190 \u043D\u0430\u0437\u0430\u0434" }), _jsxs("span", { className: "self-center text-xs text-muted-foreground", children: ["\u043A\u043E\u0434 \u043E\u0431\u043D\u043E\u0432\u0438\u0442\u0441\u044F \u0447\u0435\u0440\u0435\u0437 ", left, "\u0441"] })] })] }) })), _jsx("p", { className: "m-0 text-center text-xs text-muted-foreground", children: "tls \u00B7 \u043F\u043E\u043F\u044B\u0442\u043A\u0438 \u0432\u0445\u043E\u0434\u0430 \u043B\u043E\u0433\u0438\u0440\u0443\u044E\u0442\u0441\u044F \u00B7 \u0432\u0445\u043E\u0434 \u0442\u043E\u043B\u044C\u043A\u043E \u0434\u043B\u044F \u0432\u043B\u0430\u0434\u0435\u043B\u044C\u0446\u0430" })] }) }));
}
