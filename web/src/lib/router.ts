import { useEffect, useState } from "react";

// Minimal hash router: #/ overview, #/s/<id> server screen, #/add[/<id>]
// wizard (with an id: give an existing card SSH access), #/term[/<id>] terminal,
// #/agent[/<id>] AI agent (with an id: chat about that server).
// #/alerts alerts, rules and Telegram. #/settings panel settings (AI).
// #/rec[/<id>] terminal recordings (with an id: the player).
export type Route =
  | { name: "overview" }
  | { name: "server"; id: number }
  | { name: "terminal"; id?: number }
  | { name: "add"; id?: number }
  | { name: "agent"; id?: number }
  | { name: "alerts" }
  | { name: "settings" }
  | { name: "recordings"; id?: number };

export function parseRoute(hash: string): Route {
  const parts = hash.replace(/^#\/?/, "").split("/").filter(Boolean);
  const id = Number(parts[1]);
  switch (parts[0]) {
    case "s":
      if (Number.isInteger(id) && id > 0) return { name: "server", id };
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
    case "rec":
      return Number.isInteger(id) && id > 0 ? { name: "recordings", id } : { name: "recordings" };
  }
  return { name: "overview" };
}

export function useRoute(): Route {
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
  server: (id: number) => `#/s/${id}`,
  terminal: (id?: number) => (id ? `#/term/${id}` : "#/term"),
  add: (id?: number) => (id ? `#/add/${id}` : "#/add"),
  agent: (id?: number) => (id ? `#/agent/${id}` : "#/agent"),
  alerts: () => "#/alerts",
  settings: () => "#/settings",
  recordings: (id?: number) => (id ? `#/rec/${id}` : "#/rec"),
};

export function navigate(to: string) {
  window.location.hash = to.replace(/^#/, "");
}

// True when a key event comes from a text field, so page hotkeys stay quiet.
export function typingIn(e: KeyboardEvent): boolean {
  const t = e.target as HTMLElement | null;
  return !!t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable);
}
