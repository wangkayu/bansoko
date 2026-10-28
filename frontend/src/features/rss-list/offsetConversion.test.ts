import { describe, expect, it } from "vitest";
import type { RSSListGroup } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { offsetConversionLine } from "./offsetConversion";

function group(offset: number, episodes: (number | undefined)[]): RSSListGroup {
  return {
    offset: { value: offset, editable: true },
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
});
