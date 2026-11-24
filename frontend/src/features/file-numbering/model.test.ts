import { describe, expect, it } from "vitest";
import { automaticDraft, draftMap, episodeInputLabel, overrideList, overrideMap, overridesFromDrafts, parseEpisodeInput, sanitizeOverrides, sequenceEpisodeInput } from "./model";

const baseline = overrideMap([
  { path: "S01/01.mkv", season: 1, episode: 11 },
  { path: "S02/01.mkv", season: 2, episode: 1 },
]);

describe("file numbering overrides", () => {
  it("applies every current draft without a separate submission selection", () => {
    const drafts = draftMap(baseline);
    drafts.set("S01/01.mkv", { season: "11", episode: "21" });
    drafts.set("S02/01.mkv", { season: "22", episode: "22" });
    const next = overridesFromDrafts(drafts);
    expect(next && overrideList(next)).toEqual([
      { path: "S01/01.mkv", season: 11, episode: 21 },
      { path: "S02/01.mkv", season: 22, episode: 22 },
    ]);
  });

  it("restores any row by deleting its draft, including a baseline override", () => {
    const drafts = draftMap(baseline);
    drafts.delete("S01/01.mkv");
    const next = overridesFromDrafts(drafts);
    expect(next && overrideList(next)).toEqual([{ path: "S02/01.mkv", season: 2, episode: 1 }]);
  });

  it("lets an override inherit the main season setting", () => {
    const drafts = draftMap(baseline);
    drafts.set("S01/01.mkv", { season: "", episode: "12" });
    const next = overridesFromDrafts(drafts);
    expect(next?.get("S01/01.mkv")).toEqual({ path: "S01/01.mkv", episode: 12 });
  });

  it("rejects the whole draft map when any override is invalid", () => {
    const drafts = draftMap(baseline);
    drafts.set("S01/01.mkv", { season: "", episode: "bad" });
    expect(overridesFromDrafts(drafts)).toBeUndefined();
    drafts.set("S01/01.mkv", { season: "-1", episode: "1" });
    expect(overridesFromDrafts(drafts)).toBeUndefined();
  });

  it("allows duplicate season and episode values", () => {
    const drafts = draftMap(baseline);
    drafts.set("S02/01.mkv", { season: "1", episode: "11" });
    const next = overridesFromDrafts(drafts);
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

describe("episode range input", () => {
  it("parses single integers and consecutive ranges only", () => {
    expect(parseEpisodeInput("1")).toEqual({ episode: 1 });
    expect(parseEpisodeInput(" 08 ")).toEqual({ episode: 8 });
    expect(parseEpisodeInput("1-2")).toEqual({ episode: 1, episode_end: 2 });
    expect(parseEpisodeInput("01-12")).toEqual({ episode: 1, episode_end: 12 });
    expect(parseEpisodeInput("3-3")).toEqual({ episode: 3 });
    for (const invalid of ["", "0", "2-1", "1-13", "ep1-2", "E01-E02", "1,2", "1-", "-", "1.5", "1 - 2"]) {
      expect(parseEpisodeInput(invalid), invalid).toBeUndefined();
    }
  });

  it("labels ranges with padded episode numbers", () => {
    expect(episodeInputLabel("1")).toBe("E01");
    expect(episodeInputLabel("1-2")).toBe("E01-E02");
    expect(episodeInputLabel("08-12")).toBe("E08-E12");
    expect(episodeInputLabel("2-1")).toBeUndefined();
  });

  it("fills a sequential batch with a per-file span", () => {
    expect(sequenceEpisodeInput(1, 0, 1)).toBe("1");
    expect(sequenceEpisodeInput(1, 2, 1)).toBe("3");
    expect(sequenceEpisodeInput(1, 0, 2)).toBe("1-2");
    expect(sequenceEpisodeInput(1, 1, 2)).toBe("3-4");
    expect(sequenceEpisodeInput(1, 2, 2)).toBe("5-6");
    expect(sequenceEpisodeInput(5, 1, 12)).toBe("17-28");
  });

  it("round-trips range overrides through drafts", () => {
    const ranged = overrideMap([{ path: "S01/01.mkv", episode: 1, episode_end: 2 }]);
    const drafts = draftMap(ranged);
    expect(drafts.get("S01/01.mkv")?.episode).toBe("1-2");
    const next = overridesFromDrafts(drafts);
    expect(next && overrideList(next)).toEqual([{ path: "S01/01.mkv", episode: 1, episode_end: 2 }]);
    drafts.set("S01/01.mkv", { season: "", episode: "3-3" });
    const collapsed = overridesFromDrafts(drafts);
    expect(collapsed && overrideList(collapsed)).toEqual([{ path: "S01/01.mkv", episode: 3 }]);
  });

  it("sanitizes stale or invalid range ends out of the selection", () => {
    const selected = new Set(["a.mkv", "b.mkv", "c.mkv", "d.mkv"]);
    const cleaned = sanitizeOverrides([
      { path: "a.mkv", episode: 1, episode_end: 2 },
      { path: "b.mkv", episode: 3, episode_end: 3 },
      { path: "c.mkv", episode: 1, episode_end: 13 },
      { path: "d.mkv", episode: 2, episode_end: 1 },
    ], selected);
    expect(cleaned).toEqual([
      { path: "a.mkv", episode: 1, episode_end: 2 },
      { path: "b.mkv", episode: 3 },
      { path: "c.mkv", episode: 1 },
      { path: "d.mkv", episode: 2 },
    ]);
  });
});
