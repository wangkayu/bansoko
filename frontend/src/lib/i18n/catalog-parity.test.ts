import { describe, expect, it } from "vitest";
import { zhCN } from "./zh-CN";
import { en } from "./en";
import { localeFromLanguage } from "./index";
import { DIAGNOSTIC_CONDITIONS, MESSAGE_CODES, MESSAGE_SPECS } from "../observabilityCatalog.generated";

function leaves(node: unknown, prefix = ""): Map<string, string> {
  const out = new Map<string, string>();
  if (typeof node === "string") {
    out.set(prefix, node);
    return out;
  }
  for (const [key, child] of Object.entries(node as Record<string, unknown>)) {
    for (const [path, value] of leaves(child, prefix ? `${prefix}.${key}` : key)) out.set(path, value);
  }
  return out;
}

function placeholders(template: string): string[] {
  return [...new Set([...template.matchAll(/\{(\w+)\}/g)].map((m) => m[1]!))].sort();
}

const zh = leaves(zhCN);
const english = leaves(en);

describe("en catalog parity", () => {
  it("has exactly the zh-CN key set", () => {
    expect([...english.keys()].sort()).toEqual([...zh.keys()].sort());
  });

  it("uses the same interpolation variables as zh-CN", () => {
    const mismatched = [...zh].filter(([key, value]) => placeholders(value).join() !== placeholders(english.get(key) ?? "").join());
    expect(mismatched.map(([key]) => key)).toEqual([]);
  });

  it("has no empty values, Chinese characters or full-width punctuation", () => {
    const bad = [...english].filter(([key, value]) => (value.trim() === "" && !key.startsWith("common.format.")) || /[\u3000-\u303f\u4e00-\u9fff\uff00-\uffef]/.test(value));
    expect(bad).toEqual([]);
  });
});

describe("log catalog coverage", () => {
  const specs = MESSAGE_SPECS as Record<string, { params: readonly { name: string; required: boolean }[] }>;

  it("has a log.msg entry per message code, in both locales, with the spec's params", () => {
    const problems: string[] = [];
    for (const code of MESSAGE_CODES) {
      for (const [locale, catalog] of [["zh", zh], ["en", english]] as const) {
        const text = catalog.get(`log.msg.${code}`);
        if (text === undefined) {
          problems.push(`${locale}:${code}:missing`);
          continue;
        }
        const names = specs[code]!.params.map((param) => param.name).sort();
        const found = placeholders(text);
        if (found.some((name) => !names.includes(name))) problems.push(`${locale}:${code}:unknown-param`);
        for (const param of specs[code]!.params.filter((p) => p.required)) {
          if (!found.includes(param.name)) problems.push(`${locale}:${code}:needs-${param.name}`);
        }
      }
    }
    expect(problems).toEqual([]);
  });

  it("has a bare form for every message with optional params", () => {
    const missing: string[] = [];
    for (const code of MESSAGE_CODES) {
      if (!specs[code]!.params.some((param) => !param.required)) continue;
      for (const [locale, catalog] of [["zh", zh], ["en", english]] as const) {
        if (!catalog.has(`log.msgBare.${code}`)) missing.push(`${locale}:${code}`);
      }
    }
    expect(missing).toEqual([]);
  });

  it("has a log.diag sentence per diagnostic condition, in both locales", () => {
    const missing: string[] = [];
    for (const { kind, variant } of DIAGNOSTIC_CONDITIONS) {
      for (const [locale, catalog] of [["zh", zh], ["en", english]] as const) {
        if (!catalog.has(`log.diag.${kind}.${variant}`)) missing.push(`${locale}:${kind}.${variant}`);
      }
    }
    expect(missing).toEqual([]);
  });
});

describe("browser language default", () => {
  it("maps zh* to zh-CN and anything else to en", () => {
    expect(localeFromLanguage("zh-CN")).toBe("zh-CN");
    expect(localeFromLanguage("zh-TW")).toBe("zh-CN");
    expect(localeFromLanguage("en-US")).toBe("en");
    expect(localeFromLanguage("ja")).toBe("en");
    expect(localeFromLanguage(undefined)).toBe("en");
  });
});
