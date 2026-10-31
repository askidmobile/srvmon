// Interface language. Source strings in the code are English and double as
// keys; ../i18n/<lang>.ts maps them to translations. A missing translation
// falls back to English, so a new string never shows up empty.
//
// Adding a language: copy ../i18n/ru.ts to ../i18n/<code>.ts, translate the
// values, list it in LANGS and dicts below and in internal/i18n on the server.
import { useSyncExternalStore } from "react";

import { ru } from "@/i18n/ru";
import { zh } from "@/i18n/zh";

export type Lang = "en" | "ru" | "zh";

export const LANGS: { code: Lang; name: string }[] = [
  { code: "en", name: "English" },
  { code: "ru", name: "Русский" },
  { code: "zh", name: "中文" },
];

export type Dict = Record<string, string>;
const dicts: Record<Lang, Dict> = { en: {}, ru, zh };

const STORAGE = "srvmon.lang";

export function isLang(v: unknown): v is Lang {
  return v === "en" || v === "ru" || v === "zh";
}

function initial(): Lang {
  try {
    const v = localStorage.getItem(STORAGE);
    if (isLang(v)) return v;
  } catch {
    /* storage blocked */
  }
  return "en";
}

let current: Lang = initial();
const listeners = new Set<() => void>();
document.documentElement.lang = current;

export function lang(): Lang {
  return current;
}

/** Switches the language and re-renders subscribers (see useLang). */
export function setLang(l: Lang) {
  try {
    localStorage.setItem(STORAGE, l);
  } catch {
    /* storage blocked */
  }
  if (l === current) return;
  current = l;
  document.documentElement.lang = l;
  listeners.forEach((f) => f());
}

export function useLang(): Lang {
  return useSyncExternalStore(
    (f) => {
      listeners.add(f);
      return () => listeners.delete(f);
    },
    () => current,
  );
}

type Vars = Record<string, string | number>;

function fill(s: string, vars?: Vars): string {
  if (!vars) return s;
  return s.replace(/\{(\w+)\}/g, (m, k: string) => (k in vars ? String(vars[k]) : m));
}

/** Translates an English source string; {name} placeholders take vars. */
export function t(s: string, vars?: Vars): string {
  return fill(dicts[current][s] ?? s, vars);
}

// Plural categories in the order a language lists its forms in a dictionary
// value ("form|form|form"); a category missing here uses the last form.
const forms: Record<Lang, Intl.LDMLPluralRule[]> = {
  en: ["one", "other"],
  ru: ["one", "few", "many"],
  zh: ["other"],
};
const rules = new Map<Lang, Intl.PluralRules>();

/**
 * Plural: tn(n, "{n} alert", "{n} alerts"). The dictionary key is
 * "one|other"; the value lists the language's forms separated by "|".
 * {n} is always available.
 */
export function tn(n: number, one: string, other: string, vars?: Vars): string {
  const v = { n, ...vars };
  const tr = dicts[current][`${one}|${other}`];
  if (tr === undefined) return fill(n === 1 ? one : other, v);
  const list = tr.split("|");
  let pr = rules.get(current);
  if (!pr) {
    pr = new Intl.PluralRules(current);
    rules.set(current, pr);
  }
  const i = forms[current].indexOf(pr.select(n));
  return fill(list[i < 0 ? list.length - 1 : Math.min(i, list.length - 1)], v);
}

/** BCP 47 locale for Intl / toLocaleString. */
export function locale(): string {
  return { en: "en-GB", ru: "ru-RU", zh: "zh-CN" }[current];
}
