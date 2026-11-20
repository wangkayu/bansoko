import { describe, expect, it } from "vitest";
import type { CatalogEntryView } from "../../lib/providers/viewmodels";
import { catalogListMatches } from "./catalogListFilter";

const entry: CatalogEntryView = {
  source: "anibt", providerEntryRef: "anibt:fixture", title: "Sky Notes",
  originalTitle: "Forest Post", instanceLabel: "Local Feed", metadataMatch: { matched: false },
};

describe("catalogListMatches", () => {
  it.each([
    ["", true],
    ['sky "forest post"', true],
    ['sky -"forest post"', false],
    ['sky -"forest notes"', true],
    ["-NOTES", false],
    ["sky absent", false],
    ["local", false],
  ])("matches %s against the title and original title: %s", (query, expected) => {
    expect(catalogListMatches(query)(entry)).toBe(expected);
  });

  it("includes the subscription note only on custom presets", () => {
    const custom = { ...entry, source: "custom" as const };
    expect(catalogListMatches('sky "local feed"')(custom)).toBe(true);
    expect(catalogListMatches('sky -"local feed"')(custom)).toBe(false);
  });
});
