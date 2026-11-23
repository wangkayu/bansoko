import { describe, expect, it } from "vitest";
import type { RSSListGroup } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { offsetConversionLine } from "./offsetConversion";

function group(offset: number, episodes: (number | undefined)[]): RSSListGroup {
  return {
    offset: { value: offset, span: 1, editable: true },
    items: episodes.map((episode) => ({ evaluation: { parsed_facts: episode === undefined ? {} : { episode } } })),
  } as unknown as RSSListGroup;
}

describe("offsetConversionLine", () => {
  it("raw episode = shown episode minus the current offset, then the typed offset applies", () => {
    expect(offsetConversionLine(group(-48, [5]), "-52")).toBe(t("rss.offsetConversion", { from: 53, to: 1 }));
    expect(offsetConversionLine(group(0, [53]), "-48")).toBe(t("rss.offsetConversion", { from: 53, to: 5 }));
  });

  it("uses the first seed that has an episode", () => {
    expect(offsetConversionLine(group(0, [undefined, 7]), "1")).toBe(t("rss.offsetConversion", { from: 7, to: 8 }));
  });

  it("is hidden without an episode or with an invalid input", () => {
    expect(offsetConversionLine(group(0, [undefined]), "1")).toBeUndefined();
    expect(offsetConversionLine(undefined, "1")).toBeUndefined();
    expect(offsetConversionLine(group(0, [3]), "")).toBeUndefined();
    expect(offsetConversionLine(group(0, [3]), "1001")).toBeUndefined();
  });

  it("expands a positive raw episode into the typed span's range", () => {
    expect(offsetConversionLine(group(0, [1]), "0", "2")).toBe(t("rss.offsetConversion", { from: 1, to: "1-2" }));
    expect(offsetConversionLine(group(0, [2]), "0", "2")).toBe(t("rss.offsetConversion", { from: 2, to: "3-4" }));
    expect(offsetConversionLine(group(0, [3]), "1", "2")).toBe(t("rss.offsetConversion", { from: 3, to: "6-7" }));
    expect(offsetConversionLine(group(0, [2]), "0", "3")).toBe(t("rss.offsetConversion", { from: 2, to: "4-6" }));
  });

  it("keeps episode 0 single and span 1 unchanged, and rejects invalid spans", () => {
    expect(offsetConversionLine(group(0, [0]), "0", "2")).toBe(t("rss.offsetConversion", { from: 0, to: 0 }));
    expect(offsetConversionLine(group(0, [2]), "0", "1")).toBe(t("rss.offsetConversion", { from: 2, to: 2 }));
    expect(offsetConversionLine(group(0, [2]), "0")).toBe(t("rss.offsetConversion", { from: 2, to: 2 }));
    expect(offsetConversionLine(group(0, [2]), "0", "0")).toBeUndefined();
    expect(offsetConversionLine(group(0, [2]), "0", "13")).toBeUndefined();
    expect(offsetConversionLine(group(0, [2]), "0", "")).toBeUndefined();
    expect(offsetConversionLine(group(0, [2]), "0", "1.5")).toBeUndefined();
  });
});
