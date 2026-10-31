import { jsx as _jsx } from "react/jsx-runtime";
import * as React from "react";
import { cn } from "@/lib/utils";
export const Input = React.forwardRef(({ className, ...props }, ref) => (_jsx("input", { ref: ref, className: cn("h-11 w-full border border-input bg-field px-3 text-hi placeholder:text-muted-foreground focus-visible:outline-1 focus-visible:outline-ring disabled:opacity-50", className), ...props })));
Input.displayName = "Input";
