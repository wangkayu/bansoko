import { afterEach, describe, expect, it, vi } from "vitest";
import { apiMetadataEpisodeLoader } from "./episodeLoader";
import type { MetadataCandidate } from "./types";

function response(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

const tmdbCandidate: MetadataCandidate = {
  provider: "tmdb",
  workId: "265010",
  title: "星际邮差艾拉",
  detailsStatus: "available",
  mediaKind: "tv",
  seasons: [{ seasonNumber: 2, name: "第二季", episodeCount: 2, airDate: "2025-01-03", isSpecial: false }],
};

const bgmCandidate: MetadataCandidate = {
  provider: "bgm",
  workId: "486109",
  title: "沉默魔女的秘密",
  detailsStatus: "not_applicable",
};

describe("API candidate episode loader", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("reads the browsed TMDB season and keeps no section", async () => {
    const fetchMock = vi.fn().mockResolvedValue(response({
      season: 2,
      episodes: [{ episode: 1, title: "第一话", air_date: "2025-01-03" }, { episode: 2 }],
    }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(apiMetadataEpisodeLoader(tmdbCandidate, 2)).resolves.toEqual([
      { episode: 1, title: "第一话", airDate: "2025-01-03" },
      { episode: 2, title: undefined, airDate: undefined },
    ]);
    expect(fetchMock.mock.calls[0]?.[0]).toContain("/metadata/tmdb/works/265010/seasons/2/episodes");
  });

  it("reads a Bangumi subject's whole list with its sections", async () => {
    const fetchMock = vi.fn().mockResolvedValue(response({
      episodes: [
        { episode: 1, title: "第一话", air_date: "2025-01-03", section: "main" },
        { episode: 1, title: "特别篇", air_date: "2025-06-01", section: "special" },
      ],
    }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(apiMetadataEpisodeLoader(bgmCandidate)).resolves.toEqual([
      { episode: 1, title: "第一话", airDate: "2025-01-03", section: "main" },
      { episode: 1, title: "特别篇", airDate: "2025-06-01", section: "special" },
    ]);
    expect(fetchMock.mock.calls[0]?.[0]).toContain("/metadata/bgm/works/486109/episodes");
  });
});
