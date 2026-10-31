import { useEffect, useState } from "react";
export function parseRoute(hash) {
    const parts = hash.replace(/^#\/?/, "").split("/").filter(Boolean);
    const id = Number(parts[1]);
    switch (parts[0]) {
        case "s":
            if (Number.isInteger(id) && id > 0)
                return { name: "server", id };
            break;
        case "term":
            return Number.isInteger(id) && id > 0 ? { name: "terminal", id } : { name: "terminal" };
        case "add":
            return Number.isInteger(id) && id > 0 ? { name: "add", id } : { name: "add" };
        case "agent":
            return Number.isInteger(id) && id > 0 ? { name: "agent", id } : { name: "agent" };
        case "alerts":
            return { name: "alerts" };
        case "settings":
            return { name: "settings" };
    }
    return { name: "overview" };
}
export function useRoute() {
    const [route, setRoute] = useState(() => parseRoute(window.location.hash));
    useEffect(() => {
        const on = () => {
            setRoute(parseRoute(window.location.hash));
            window.scrollTo(0, 0);
        };
        window.addEventListener("hashchange", on);
        return () => window.removeEventListener("hashchange", on);
    }, []);
    return route;
}
export const href = {
    overview: () => "#/",
    server: (id) => `#/s/${id}`,
    terminal: (id) => (id ? `#/term/${id}` : "#/term"),
    add: (id) => (id ? `#/add/${id}` : "#/add"),
    agent: (id) => (id ? `#/agent/${id}` : "#/agent"),
    alerts: () => "#/alerts",
    settings: () => "#/settings",
};
export function navigate(to) {
    window.location.hash = to.replace(/^#/, "");
}
// True when a key event comes from a text field, so page hotkeys stay quiet.
export function typingIn(e) {
    const t = e.target;
    return !!t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable);
}
