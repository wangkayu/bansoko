import { describe, expect, it } from "vitest";
import {
  currentSeason,
  listSeasonsAround,
  parseSeason,
  seasonFromIndex,
  seasonIndex,
  seasonLabel,
  shiftSeason,
} from "./season";

describe("season helpers", () => {
  it("parses canonical seasons and rejects garbage", () => {
    expect(parseSeason("2026-summer")).toEqual({ year: 2026, term: "summer" });
    expect(parseSeason("2026-autumn")).toBeUndefined();
    expect(parseSeason("summer")).toBeUndefined();
  });

  it("steps across year boundaries", () => {
    expect(shiftSeason("2026-summer", 1)).toBe("2026-fall");
    expect(shiftSeason("2026-fall", 1)).toBe("2027-winter");
    expect(shiftSeason("2026-winter", -1)).toBe("2025-fall");
  });

  it("round-trips index conversion", () => {
    const index = seasonIndex("2025-spring");
    expect(index).toBeDefined();
    expect(seasonFromIndex(index!)).toBe("2025-spring");
  });

  it("labels seasons in Chinese", () => {
    expect(seasonLabel("2026-winter")).toBe("2026 冬");
  });

  it("derives the current season from the month", () => {
    expect(currentSeason(new Date("2026-07-16"))).toBe("2026-summer");
    expect(currentSeason(new Date("2026-01-01"))).toBe("2026-winter");
  });

  it("lists seasons around the center", () => {
    expect(listSeasonsAround("2026-summer", 1, 1)).toEqual(["2026-spring", "2026-summer", "2026-fall"]);
  });
});
