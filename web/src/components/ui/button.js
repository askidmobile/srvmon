import { jsx as _jsx } from "react/jsx-runtime";
import { Slot } from "@radix-ui/react-slot";
import { cva } from "class-variance-authority";
import * as React from "react";
import { cn } from "@/lib/utils";
// Heights follow the mockup: 36px default, 24px compact (tables).
const buttonVariants = cva("inline-flex items-center justify-center gap-2 whitespace-nowrap border font-mono transition-colors disabled:pointer-events-none disabled:opacity-50 cursor-pointer", {
    variants: {
        variant: {
            default: "border-primary bg-primary text-primary-foreground font-bold hover:bg-primary-hover",
            outline: "border-border bg-transparent text-foreground hover:border-primary hover:text-primary",
            ghost: "border-transparent bg-transparent text-muted-foreground hover:text-primary",
            destructive: "border-crit bg-transparent text-crit hover:bg-crit hover:text-background",
        },
        size: {
            default: "h-9 px-3",
            sm: "h-6 px-2 text-xs",
            lg: "h-11 px-4",
        },
    },
    defaultVariants: { variant: "default", size: "default" },
});
export const Button = React.forwardRef(({ className, variant, size, asChild = false, ...props }, ref) => {
    const Comp = asChild ? Slot : "button";
    return _jsx(Comp, { ref: ref, className: cn(buttonVariants({ variant, size, className })), ...props });
});
Button.displayName = "Button";
