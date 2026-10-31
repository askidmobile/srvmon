import { useEffect, useState } from "react";

// Minimal hash router: #/ overview, #/s/<id> server screen, #/add[/<id>]
// wizard (with an id: give an existing card SSH access), #/term[/<id>] terminal,
// #/alerts alerts, rules and Telegram.
export type Route =
  | { name: "overview" }
  | { name: "server"; id: number }
  | { name: "terminal"; id?: number }
  | { name: "add"; id?: number }
  | { name: "alerts" };

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
    case "alerts":
      return { name: "alerts" };
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
  alerts: () => "#/alerts",
};

export function navigate(to: string) {
  window.location.hash = to.replace(/^#/, "");
}

// True when a key event comes from a text field, so page hotkeys stay quiet.
export function typingIn(e: KeyboardEvent): boolean {
  const t = e.target as HTMLElement | null;
  return !!t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable);
}
