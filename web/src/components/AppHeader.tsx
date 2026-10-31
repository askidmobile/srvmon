import { cn } from "@/lib/utils";

const LINKS = [
  { href: "#/", label: "[1] обзор", key: "overview" },
  { href: "#/add", label: "[+] сервер", key: "add" },
  { href: "#/term", label: "[3] терминал", key: "term" },
] as const;

// TUI header with section links, as in docs/design.
export function AppHeader({ active, right }: { active: (typeof LINKS)[number]["key"]; right?: React.ReactNode }) {
  return (
    <header className="flex flex-wrap items-center gap-x-6 gap-y-3 border-b border-border px-4 py-2.5 sm:px-5">
      <a href="#/" className="font-bold text-primary">
        ▌srvmon
      </a>
      <nav className="flex flex-1 flex-wrap gap-1">
        {LINKS.map((l) => (
          <a
            key={l.key}
            href={l.href}
            className={cn(
              "px-2 py-0.5",
              l.key === active ? "bg-primary text-primary-foreground" : "text-primary hover:text-primary-hover",
            )}
          >
            {l.label}
          </a>
        ))}
      </nav>
      {right}
    </header>
  );
}
