import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { cn } from "@/lib/utils";
// btop-style box: a 1px frame with the title sitting on the top border.
export function Panel({ title, right, className, children, accent = false, }) {
    return (_jsxs("section", { className: cn("relative rounded-md border px-4 pt-5 pb-4", accent ? "border-primary" : "border-border", className), children: [title && (_jsx("h2", { className: "absolute -top-2.5 left-3 bg-background px-1.5 text-sm font-normal leading-5 text-primary", children: title })), right && (_jsx("div", { className: "absolute -top-2.5 right-3 max-w-[calc(100%-8rem)] truncate bg-background px-1.5 text-xs leading-5 text-muted-foreground", title: typeof right === "string" ? right : undefined, children: right })), children] }));
}
