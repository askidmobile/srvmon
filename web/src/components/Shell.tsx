import * as React from "react";
import { useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { href, type Route } from "@/lib/router";
import { cn } from "@/lib/utils";

const nav: { key: string; label: string; to?: string; match: Route["name"][] }[] = [
  { key: "1", label: "обзор", to: href.overview(), match: ["overview"] },
  { key: "2", label: "серверы", match: ["server", "add"] },
  { key: "3", label: "терминал", to: href.terminal(), match: ["terminal"] },
  { key: "4", label: "агент", match: [] },
  { key: "5", label: "алерты", match: [] },
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
