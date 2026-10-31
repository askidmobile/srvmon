import { clsx } from "clsx";
import { twMerge } from "tailwind-merge";
export function cn(...inputs) {
    return twMerge(clsx(inputs));
}
export function formatBytes(n) {
    if (!Number.isFinite(n) || n <= 0)
        return "0B";
    const units = ["B", "K", "M", "G", "T"];
    let i = 0;
    while (n >= 1024 && i < units.length - 1) {
        n /= 1024;
        i++;
    }
    return `${n >= 10 || i === 0 ? n.toFixed(0) : n.toFixed(1)}${units[i]}`;
}
export function formatUptime(sec) {
    const d = Math.floor(sec / 86400);
    const h = Math.floor((sec % 86400) / 3600);
    if (d > 0)
        return `${d}д ${h}ч`;
    const m = Math.floor((sec % 3600) / 60);
    return `${h}ч ${m}м`;
}
