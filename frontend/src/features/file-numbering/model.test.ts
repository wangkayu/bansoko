import { describe, expect, it } from "vitest";
import { automaticDraft, draftMap, mergeCheckedOverrides, overrideList, overrideMap, sanitizeOverrides } from "./model";

const baseline = overrideMap([
  { path: "S01/01.mkv", season: 1, episode: 11 },
  { path: "S02/01.mkv", season: 2, episode: 1 },
]);

describe("file numbering overrides", () => {
  it("applies only checked rows and preserves unchecked baseline values", () => {
    const drafts = draftMap(baseline);
    drafts.set("S01/01.mkv", { season: "11", episode: "21" });
    drafts.set("S02/01.mkv", { season: "22", episode: "22" });
    const next = mergeCheckedOverrides(baseline, drafts, new Set(["S02/01.mkv"]));
    expect(next && overrideList(next)).toEqual([
      { path: "S01/01.mkv", season: 1, episode: 11 },
      { path: "S02/01.mkv", season: 22, episode: 22 },
    ]);
  });

  it("restores a checked row by deleting its override", () => {
    const drafts = draftMap(baseline);
    drafts.delete("S01/01.mkv");
    const next = mergeCheckedOverrides(baseline, drafts, new Set(["S01/01.mkv"]));
    expect(next && overrideList(next)).toEqual([{ path: "S02/01.mkv", season: 2, episode: 1 }]);
  });

  it("lets an override inherit the main season setting", () => {
    const drafts = draftMap(baseline);
    drafts.set("S01/01.mkv", { season: "", episode: "12" });
    const next = mergeCheckedOverrides(baseline, drafts, new Set(["S01/01.mkv"]));
    expect(next?.get("S01/01.mkv")).toEqual({ path: "S01/01.mkv", episode: 12 });
  });

  it("rejects an invalid checked row without validating unchecked drafts", () => {
    const drafts = draftMap(baseline);
    drafts.set("S01/01.mkv", { season: "", episode: "bad" });
    expect(mergeCheckedOverrides(baseline, drafts, new Set(["S02/01.mkv"]))).toBeDefined();
    expect(mergeCheckedOverrides(baseline, drafts, new Set(["S01/01.mkv"]))).toBeUndefined();
  });

  it("allows duplicate season and episode values", () => {
    const drafts = draftMap(baseline);
    drafts.set("S02/01.mkv", { season: "1", episode: "11" });
    const next = mergeCheckedOverrides(baseline, drafts, new Set(["S01/01.mkv", "S02/01.mkv"]));
    expect(next && overrideList(next)).toEqual([
      { path: "S01/01.mkv", season: 1, episode: 11 },
      { path: "S02/01.mkv", season: 1, episode: 11 },
    ]);
  });

  it("sanitizes stale paths and returns canonical path order", () => {
    expect(sanitizeOverrides([...baseline.values(), { path: "gone.mkv", season: 1, episode: 1 }], new Set(["S02/01.mkv", "S01/01.mkv"]))).toEqual([
      { path: "S01/01.mkv", season: 1, episode: 11 },
      { path: "S02/01.mkv", season: 2, episode: 1 },
    ]);
  });

  it("uses automatic values only as the initial edit value", () => {
    expect(automaticDraft({ path: "a.mkv", sizeBytes: 1, automaticSeason: 0, automaticEpisode: null, automaticLabel: "auto", subtitleCount: 0 })).toEqual({ season: "0", episode: "" });
  });
});
