import * as React from "react";

import { cn } from "@/lib/utils";

// TUI-style checkbox: "[x]" / "[ ]". The real <input> stays in the DOM
// (visually hidden) so keyboard focus, the label click and screen readers
// keep working; the visible marker follows its focus ring.
export function Check({
  checked,
  onChange,
  disabled,
  className,
  children,
  "aria-label": ariaLabel,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  disabled?: boolean;
  className?: string;
  children?: React.ReactNode;
  "aria-label"?: string;
}) {
  return (
    <label className={cn("group flex cursor-pointer items-start gap-2", disabled && "cursor-default opacity-60", className)}>
      <input
        type="checkbox"
        className="peer sr-only"
        checked={checked}
        disabled={disabled}
        aria-label={ariaLabel}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span
        className={cn(
          "shrink-0 whitespace-pre peer-focus-visible:outline-1 peer-focus-visible:outline-ring",
          checked ? "text-primary" : "text-muted-foreground group-hover:text-hi",
        )}
        aria-hidden
      >
        {checked ? "[x]" : "[ ]"}
      </span>
      {children !== undefined && <span>{children}</span>}
    </label>
  );
}
