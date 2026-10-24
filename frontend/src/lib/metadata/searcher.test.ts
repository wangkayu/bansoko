import { describe, expect, it, vi } from "vitest";
import { apiMetadataSearcher } from "./searcher";

function response(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

describe("API metadata searcher", () => {
  it("maps candidates returned by the typed metadata endpoint", async () => {
    const fetchMock = vi.fn().mockResolvedValue(response({
      provider: "bgm",
      query: "进击的巨人",
      candidates: [{
        provider: "bgm",
        provider_work_id: "486109",
        title: "进击的巨人",
        original_title: "進撃の巨人",
        overview: "简介",
        poster_url: "/api/v1/assets/poster",
        media_kind: "tv",
        details_status: "not_applicable",
        suggested_season: 1,
        year: 2026,
        release_format: "movie",
        score: 0.96,
      }],
    }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(apiMetadataSearcher.search("bgm", "进击的巨人")).resolves.toEqual([{
      provider: "bgm",
      workId: "486109",
      title: "进击的巨人",
      originalTitle: "進撃の巨人",
      overview: "简介",
      posterUrl: "/api/v1/assets/poster",
      mediaKind: "tv",
      detailsStatus: "not_applicable",
      seasons: undefined,
      suggestedSeason: 1,
      year: 2026,
      score: 0.96,
    }]);
    expect(fetchMock).toHaveBeenCalledOnce();
    vi.unstubAllGlobals();
  });

  it("maps real TV seasons and details status", async () => {
    const fetchMock = vi.fn().mockResolvedValue(response({
      provider: "tmdb",
      query: "show",
      candidates: [{
        provider: "tmdb",
        provider_work_id: "42",
        title: "Show",
        media_kind: "tv",
        details_status: "available",
        seasons: [{ season_number: 12, name: "Season Twelve", episode_count: 10, air_date: "2025-01-01", is_special: false }],
        suggested_season: 12,
        score: 1,
      }],
    }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(apiMetadataSearcher.search("tmdb", "show")).resolves.toEqual([{
      provider: "tmdb",
      workId: "42",
      title: "Show",
      originalTitle: undefined,
      overview: undefined,
      posterUrl: undefined,
      mediaKind: "tv",
      detailsStatus: "available",
      seasons: [{ seasonNumber: 12, name: "Season Twelve", episodeCount: 10, airDate: "2025-01-01", isSpecial: false }],
      suggestedSeason: 12,
      year: undefined,
      score: 1,
    }]);
    vi.unstubAllGlobals();
  });

  it("preserves an empty server result", async () => {
    const fetchMock = vi.fn().mockResolvedValue(response({ provider: "tmdb", query: "unknown", candidates: [] }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(apiMetadataSearcher.search("tmdb", "unknown")).resolves.toEqual([]);
    vi.unstubAllGlobals();
  });
});
