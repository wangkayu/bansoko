import { useSyncExternalStore } from "react";
import { zhCN } from "./zh-CN";
import { en } from "./en";

// The copy-table read entry. Every user-facing string lives in a copy object, read with
//   `t(key, vars)`; zh-CN is the source of keys, and en is typechecked against the same key set
//   (CopyShape in types.ts). The current language lives in module state and `t()` reads it on every
//   call; the App root remounts the UI per language (useLocale).

type Join<Prefix extends string, Key extends string> = Prefix extends "" ? Key : `${Prefix}.${Key}`;

/** Expands a nested object's leaves into a dotted-path union type. */
type Leaves<T, Prefix extends string = ""> = {
  [K in keyof T & string]: T[K] extends string ? Join<Prefix, K> : Leaves<T[K], Join<Prefix, K>>;
}[keyof T & string];

export type CopyKey = Leaves<typeof zhCN>;

export type CopyVars = Record<string, string | number>;

export const LOCALES = ["zh-CN", "en"] as const;
export type Locale = (typeof LOCALES)[number];

/** Each language option shows its own native name and is never translated with the UI language. */
export const LOCALE_NAMES: Record<Locale, string> = { "zh-CN": "简体中文", en: "English" };

export const catalogs: Record<Locale, unknown> = { "zh-CN": zhCN, en };

export const LOCALE_STORAGE_KEY = "locale";

const isProduction = Boolean(import.meta.env?.PROD);

export function isLocale(value: unknown): value is Locale {
  return typeof value === "string" && (LOCALES as readonly string[]).includes(value);
}

/** A Chinese browser language selects zh-CN; everything else selects en. */
export function localeFromLanguage(language: string | undefined | null): Locale {
  return language && language.toLowerCase().startsWith("zh") ? "zh-CN" : "en";
}

/** The choice saved in this browser wins; absent that, the browser language. */
export function detectLocale(): Locale {
  try {
    const stored = globalThis.localStorage?.getItem(LOCALE_STORAGE_KEY);
    if (isLocale(stored)) return stored;
  } catch {
    // Private mode or storage disabled: follow the browser language.
  }
  return localeFromLanguage(globalThis.navigator?.language);
}

let activeLocale: Locale = detectLocale();
const listeners = new Set<() => void>();

function applyDocumentLang(locale: Locale): void {
  if (typeof document !== "undefined") document.documentElement.lang = locale;
}

applyDocumentLang(activeLocale);

export function getLocale(): Locale {
  return activeLocale;
}

/**
 * Switches the UI language: saves to this browser (not saved when `persist: false`), updates
 *   <html lang>, and notifies subscribers to re-render.
 */
export function setLocale(locale: Locale, options: { persist?: boolean } = {}): void {
  if (options.persist !== false) {
    try {
      globalThis.localStorage?.setItem(LOCALE_STORAGE_KEY, locale);
    } catch {
      // When storage is unavailable, the choice holds only for this session.
    }
  }
  applyDocumentLang(locale);
  if (locale === activeLocale) return;
  activeLocale = locale;
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** The current UI language; a language change triggers a re-render. */
export function useLocale(): Locale {
  return useSyncExternalStore(subscribe, getLocale, getLocale);
}

function lookup(key: string): string | undefined {
  let node: unknown = catalogs[activeLocale];
  for (const part of key.split(".")) {
    if (node === null || typeof node !== "object") return undefined;
    node = (node as Record<string, unknown>)[part];
  }
  return typeof node === "string" ? node : undefined;
}

function interpolate(template: string, vars: CopyVars | undefined, key: string): string {
  return template.replace(/\{(\w+)\}/g, (placeholder, name: string) => {
    const value = vars?.[name];
    if (value === undefined) {
      if (!isProduction) throw new Error(`文案 "${key}" 缺少插值变量 "${name}"`);
      return placeholder;
    }
    return String(value);
  });
}

/**
 * Reads copy and interpolates `{name}`. Key names are checked at the type level; a key missing at
 *   runtime (a dynamically assembled key) throws in dev/test and falls back to showing the key name
 *   in production. For dynamic keys use `tryT`.
 */
export function t(key: CopyKey, vars?: CopyVars): string {
  return tDynamic(key, vars);
}

function tDynamic(key: string, vars?: CopyVars): string {
  const template = lookup(key);
  if (template === undefined) {
    if (!isProduction) throw new Error(`文案键不存在: ${key}`);
    return key;
  }
  return interpolate(template, vars, key);
}

/** The non-throwing read for dynamic keys (e.g. `error.${code}`): returns undefined when absent. */
export function tryT(key: string, vars?: CopyVars): string | undefined {
  const template = lookup(key);
  return template === undefined ? undefined : interpolate(template, vars, key);
}
