import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import * as ScrollAreaPrimitive from "@radix-ui/react-scroll-area";
import * as React from "react";
import { cn } from "@/lib/utils";
// shadcn/ui ScrollArea with a thin (6px) translucent thumb instead of the
// native browser scrollbar.
export const ScrollArea = React.forwardRef(({ className, children, ...props }, ref) => (_jsxs(ScrollAreaPrimitive.Root, { ref: ref, className: cn("relative overflow-hidden", className), ...props, children: [_jsx(ScrollAreaPrimitive.Viewport, { className: "h-full w-full", children: children }), _jsx(ScrollBar, {}), _jsx(ScrollBar, { orientation: "horizontal" }), _jsx(ScrollAreaPrimitive.Corner, {})] })));
ScrollArea.displayName = ScrollAreaPrimitive.Root.displayName;
export const ScrollBar = React.forwardRef(({ className, orientation = "vertical", ...props }, ref) => (_jsx(ScrollAreaPrimitive.ScrollAreaScrollbar, { ref: ref, orientation: orientation, className: cn("flex touch-none select-none p-px transition-colors", orientation === "vertical" && "h-full w-1.5", orientation === "horizontal" && "h-1.5 flex-col", className), ...props, children: _jsx(ScrollAreaPrimitive.ScrollAreaThumb, { className: "relative flex-1 bg-primary/30 hover:bg-primary/60" }) })));
ScrollBar.displayName = ScrollAreaPrimitive.ScrollAreaScrollbar.displayName;
