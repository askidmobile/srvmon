// Thin client for srvmon-server. Cookies are HttpOnly; the browser sends them.
export class ApiError extends Error {
    status;
    code;
    retryAfter;
    constructor(status, code, retryAfter) {
        super(code);
        this.status = status;
        this.code = code;
        this.retryAfter = retryAfter;
    }
}
// Validation errors carry a message for the user.
export function errorMessage(e, fallback) {
    if (e instanceof ApiError && e.message && e.message !== e.code)
        return e.message;
    return fallback;
}
async function request(method, path, body) {
    const res = await fetch(path, {
        method,
        credentials: "same-origin",
        headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
        body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) {
        const retry = Number(res.headers.get("Retry-After") ?? "") || undefined;
        const err = new ApiError(res.status, data.error ?? "error", retry);
        const msg = data.message;
        if (msg)
            err.message = msg;
        throw err;
    }
    return data;
}
export const api = {
    me: () => request("GET", "/api/auth/me"),
    password: (password) => request("POST", "/api/auth/password", { password }),
    totp: (code) => request("POST", "/api/auth/totp", { code }),
    logout: () => request("POST", "/api/auth/logout"),
    servers: () => request("GET", "/api/servers"),
    server: (id) => request("GET", `/api/servers/${id}`),
    history: (id, range) => request("GET", `/api/servers/${id}/history?range=${encodeURIComponent(range)}`),
    updateCard: (id, card) => request("PATCH", `/api/servers/${id}`, card),
    setupKey: () => request("POST", "/api/servers/setup-key", {}),
    probe: (host, port) => request("POST", "/api/servers/probe", { host, port }),
    onboard: (req) => request("POST", "/api/servers/onboard", req),
    onboardStatus: (job, from) => request("GET", `/api/onboard/${job}?from=${from}`),
    cancelOnboard: (job) => request("DELETE", `/api/onboard/${job}`),
    deleteServer: (id) => request("DELETE", `/api/servers/${id}`),
    containerAction: (id, name, action) => request("POST", `/api/servers/${id}/containers/${encodeURIComponent(name)}/${action}`),
    alerts: () => request("GET", "/api/alerts"),
    alertSummary: () => request("GET", "/api/alerts/summary"),
    ackAlert: (id) => request("POST", `/api/alerts/${id}/ack`),
    silenceAlert: (id, minutes) => request("POST", `/api/alerts/${id}/silence`, { minutes }),
    rules: () => request("GET", "/api/alert-rules"),
    createRule: (r) => request("POST", "/api/alert-rules", r),
    updateRule: (r) => request("PUT", `/api/alert-rules/${r.id}`, r),
    deleteRule: (id) => request("DELETE", `/api/alert-rules/${id}`),
    notify: () => request("GET", "/api/notify"),
    saveTelegram: (chat_id, token) => request("PUT", "/api/notify/telegram", token === undefined ? { chat_id } : { chat_id, token }),
    testTelegram: () => request("POST", "/api/notify/telegram/test"),
    saveQuiet: (q) => request("PUT", "/api/notify/quiet", q),
};
export const historyRanges = ["15m", "1h", "6h", "24h", "7d"];
export function terminalURL(serverId, cols, rows, container) {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const q = new URLSearchParams({ cols: String(cols), rows: String(rows) });
    if (container)
        q.set("container", container);
    return `${proto}//${location.host}/api/servers/${serverId}/terminal?${q}`;
}
