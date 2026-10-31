import * as React from "react";

import { cn } from "@/lib/utils";

// btop-style box: a 1px frame with the title sitting on the top border.
export function Panel({
  title,
  right,
  className,
  children,
  accent = false,
}: {
  title?: React.ReactNode;
  right?: React.ReactNode;
  className?: string;
  children: React.ReactNode;
  accent?: boolean;
}) {
  return (
    <section
      className={cn(
        "relative rounded-md border px-4 pt-5 pb-4",
        accent ? "border-primary" : "border-border",
        className,
      )}
    >
      {title && (
        <h2 className="absolute -top-2.5 left-3 bg-background px-1.5 text-sm font-normal leading-5 text-primary">
          {title}
        </h2>
      )}
      {right && (
        <div className="absolute -top-2.5 right-3 bg-background px-1.5 text-xs leading-5 text-muted-foreground">
          {right}
        </div>
      )}
      {children}
    </section>
  );
}
