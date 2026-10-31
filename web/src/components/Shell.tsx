import * as React from "react";
import { useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import { api, type AlertSummary } from "@/lib/api";
import { href, type Route } from "@/lib/router";
import { cn } from "@/lib/utils";

const nav: { key: string; label: string; to?: string; match: Route["name"][] }[] = [
  { key: "1", label: "обзор", to: href.overview(), match: ["overview"] },
  { key: "2", label: "серверы", match: ["server", "add"] },
  { key: "3", label: "терминал", to: href.terminal(), match: ["terminal"] },
  { key: "4", label: "агент", to: href.agent(), match: ["agent"] },
  { key: "5", label: "алерты", to: href.alerts(), match: ["alerts"] },
  { key: "6", label: "настройки", match: [] },
];

function Clock() {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const t = setInterval(() => setNow(new Date()), 1000);
    return () => clearInterval(t);
  }, []);
  return <span className="text-muted-foreground">{now.toISOString().slice(11, 19)} UTC</span>;
}

// "▲ 2 алерта · tg: ok" in the header of every page.
function AlertBadge() {
  const [sum, setSum] = useState<AlertSummary | null>(null);
  useEffect(() => {
    let alive = true;
    const load = () =>
      api
        .alertSummary()
        .then((s) => alive && setSum(s))
        .catch(() => undefined);
    load();
    const t = setInterval(load, 15000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, []);
  if (!sum) return null;
  const tg = { ok: "text-muted-foreground", off: "text-muted-foreground/60", error: "text-crit" }[sum.telegram];
  return (
    <a href={href.alerts()} className="flex gap-3 whitespace-nowrap hover:opacity-80">
      {sum.open > 0 ? (
        <span className={sum.crit > 0 ? "text-crit" : "text-warn"}>
          ▲ {sum.open} {plural(sum.open, "алерт", "алерта", "алертов")}
        </span>
      ) : (
        <span className="text-muted-foreground">алертов нет</span>
      )}
      <span className={tg}>tg: {sum.telegram === "ok" ? "ok" : sum.telegram === "off" ? "выкл" : "ошибка"}</span>
    </a>
  );
}

export function plural(n: number, one: string, few: string, many: string): string {
  const m10 = n % 10;
  const m100 = n % 100;
  if (m10 === 1 && m100 !== 11) return one;
  if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14)) return few;
  return many;
}

export function Hotkey({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <span className="whitespace-nowrap">
      <span className="bg-primary px-1 text-primary-foreground">{k}</span> {children}
    </span>
  );
}

// Page frame from the mockup: header with numbered navigation, content and
// a footer with hotkeys.
export function Shell({
  route,
  version,
  onLogout,
  status,
  footer,
  footerNote,
  children,
}: {
  route: Route;
  version: string;
  onLogout: () => void;
  status?: React.ReactNode;
  footer?: React.ReactNode;
  footerNote?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div className="flex min-h-screen flex-col">
      <header className="flex flex-wrap items-center gap-x-6 gap-y-2 border-b border-border px-4 py-2.5 sm:px-5">
        <a href={href.overview()} className="font-bold text-primary">
          ▌srvmon
        </a>
        <nav className="flex flex-1 flex-wrap gap-1" aria-label="разделы">
          {nav.map((n) => {
            const on = n.match.includes(route.name);
            const cls = cn("px-2 py-0.5", on ? "bg-primary text-primary-foreground" : "text-primary hover:text-primary-hover");
            return n.to ? (
              <a key={n.key} href={n.to} className={cls} aria-current={on ? "page" : undefined}>
                [{n.key}] {n.label}
              </a>
            ) : (
              <span
                key={n.key}
                className={on ? cls : "px-2 py-0.5 text-muted-foreground/60"}
                title={on ? undefined : "скоро"}
              >
                [{n.key}] {n.label}
              </span>
            );
          })}
        </nav>
        {status}
        <AlertBadge />
        <Clock />
        <span className="text-xs text-muted-foreground">v{version.slice(0, 12)}</span>
        <Button
          variant="outline"
          size="sm"
          onClick={async () => {
            await api.logout().catch(() => undefined);
            onLogout();
          }}
        >
          выйти
        </Button>
      </header>
      <main className="flex flex-1 flex-col gap-5 px-4 py-4 sm:px-5">{children}</main>
      {(footer || footerNote) && (
        <footer className="flex flex-wrap gap-x-4 gap-y-1 border-t border-border px-4 py-1.5 text-xs sm:px-5">
          {footer}
          {footerNote && <span className="ml-auto text-muted-foreground">{footerNote}</span>}
        </footer>
      )}
    </div>
  );
}
