import { ApiError } from "@/lib/api";
async function req(method, path, body) {
    const res = await fetch(path, {
        method,
        credentials: "same-origin",
        headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
        body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok)
        throw new ApiError(res.status, data.field ?? data.error ?? "error");
    return data;
}
export const agentApi = {
    status: () => req("GET", "/api/agent/status"),
    chats: () => req("GET", "/api/agent/chats"),
    newChat: (serverId) => req("POST", "/api/agent/chats", serverId ? { server_id: serverId } : {}),
    chat: (id) => req("GET", `/api/agent/chats/${id}`),
    deleteChat: (id) => req("DELETE", `/api/agent/chats/${id}`),
    abort: (id) => req("POST", `/api/agent/chats/${id}/abort`),
    actions: (state) => req("GET", `/api/agent/actions${state ? `?state=${state}` : ""}`),
    decide: (id, decision) => req("POST", `/api/agent/actions/${id}/${decision}`),
    tasks: () => req("GET", "/api/agent/tasks"),
    newTask: (t) => req("POST", "/api/agent/tasks", t),
    updateTask: (id, t) => req("PATCH", `/api/agent/tasks/${id}`, t),
    deleteTask: (id) => req("DELETE", `/api/agent/tasks/${id}`),
    runTask: (id) => req("POST", `/api/agent/tasks/${id}/run`),
    setChatModel: (id, model) => req("PUT", `/api/agent/chats/${id}/model`, { model }),
    settings: () => req("GET", "/api/agent/settings"),
    saveSettings: (s) => req("PUT", "/api/agent/settings", s),
    providers: () => req("GET", "/api/agent/providers"),
    models: (s) => req("POST", "/api/agent/models", s),
    test: (model) => req("POST", "/api/agent/test", { model: model ?? "" }),
};
/** Sends a message and calls onEvent for each streamed event. */
export async function runChat(chatId, text, onEvent) {
    const res = await fetch(`/api/agent/chats/${chatId}/run`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text }),
    });
    if (!res.ok || !res.body) {
        const data = await res.json().catch(() => ({}));
        throw new ApiError(res.status, data.error ?? "error");
    }
    const reader = res.body.getReader();
    const dec = new TextDecoder();
    let buf = "";
    for (;;) {
        const { done, value } = await reader.read();
        if (done)
            break;
        buf += dec.decode(value, { stream: true });
        let nl;
        while ((nl = buf.indexOf("\n")) >= 0) {
            const line = buf.slice(0, nl).trim();
            buf = buf.slice(nl + 1);
            if (!line)
                continue;
            try {
                onEvent(JSON.parse(line));
            }
            catch {
                // skip a malformed line
            }
        }
    }
}
/** One-line summary of a tool call for the transcript. */
export function toolLine(t) {
    const a = t.args ?? {};
    const s = (k) => (typeof a[k] === "string" ? a[k] : "");
    switch (t.name) {
        case "srvmon_exec":
            return `${s("server")} $ ${s("command")}`;
        case "srvmon_servers":
            return "серверы";
        case "srvmon_server":
            return `сервер ${s("server")}`;
        case "srvmon_tasks":
            return "задачи";
        case "srvmon_task_create":
            return `новая задача ${s("name")} · ${s("schedule")}`;
        case "srvmon_task_delete":
            return `удалить задачу #${String(a.id ?? "")}`;
    }
    return t.name;
}
