import { describe, expect, it } from "vitest";
import { t } from "../../lib/i18n";
import { episodesRangeLabel, releaseEpisodeLabel } from "./model";

describe("release episode label", () => {
  it("joins consecutive episodes into ranges", () => {
    expect(episodesRangeLabel([1, 2, 3, 4])).toBe("01-04");
    expect(episodesRangeLabel([7, 1, 3])).toBe("01, 03, 07");
    expect(episodesRangeLabel([1, 2, 3, 7, 8])).toBe("01-03, 07-08");
    expect(episodesRangeLabel([12])).toBe("12");
  });

  it("shows a batch's planned episodes, else the single episode", () => {
    expect(releaseEpisodeLabel({ special: false, coverage: { season: 1, episodes: [1, 2, 3, 4] } })).toBe(t("rss.episodes", { range: "01-04" }));
    expect(releaseEpisodeLabel({ special: false, episode: 5 })).toBe(t("rss.episode", { n: 5 }));
    expect(releaseEpisodeLabel({ special: false })).toBeUndefined();
    expect(releaseEpisodeLabel(undefined)).toBeUndefined();
  });
});
