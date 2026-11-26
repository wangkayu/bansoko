import { describe, expect, it } from "vitest";
import { formatEpisodeDuration } from "./duration";

describe("episode duration display", () => {
  it.each([
    ["00:24:00", "24m"], ["24m", "24m"], ["23:40", "24m"], ["01:05:00", "65m"],
    ["", "—"], ["   ", "—"], ["约半小时", "约半小时"], ["24:99", "24:99"],
  ])("formats %j as %j", (duration, expected) => {
    expect(formatEpisodeDuration({ duration })).toBe(expected);
  });

  it("keeps integer TMDB runtimes and distinguishes missing data", () => {
    expect(formatEpisodeDuration({ runtimeMinutes: 24 })).toBe("24m");
    expect(formatEpisodeDuration({ runtimeMinutes: 0 })).toBe("0m");
    expect(formatEpisodeDuration({})).toBe("—");
  });
});
