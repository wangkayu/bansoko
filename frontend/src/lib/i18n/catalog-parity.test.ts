import { describe, expect, it } from "vitest";
import { zhCN } from "./zh-CN";
import { en } from "./en";
import { localeFromLanguage } from "./index";

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

describe("browser language default", () => {
  it("maps zh* to zh-CN and anything else to en", () => {
    expect(localeFromLanguage("zh-CN")).toBe("zh-CN");
    expect(localeFromLanguage("zh-TW")).toBe("zh-CN");
    expect(localeFromLanguage("en-US")).toBe("en");
    expect(localeFromLanguage("ja")).toBe("en");
    expect(localeFromLanguage(undefined)).toBe("en");
  });
});
