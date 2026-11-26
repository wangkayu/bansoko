import { afterEach, describe, expect, it, vi } from "vitest";
import { apiMetadataEpisodeLoader, resetEpisodeCache } from "./episodeLoader";

const tmdb = { provider: "tmdb", workId: "265010", title: "星际邮差艾拉" } as const;
const bgm = { provider: "bgm", workId: "486109", title: "沉默魔女的秘密" } as const;

describe("API candidate episode loader", () => {
  afterEach(() => {
    resetEpisodeCache();
    vi.unstubAllGlobals();
  });

  it("requests only Chinese by default, and reuses each loaded work, season and language", async () => {
    const still = "/api/v1/images/cover/" + "ab".repeat(32);
    const fetchMock = vi.fn(async (path: string) => {
      const url = new URL(path, "http://localhost");
      const language = url.searchParams.get("language");
      return Response.json({ season: 2, episodes: [{
        episode: 1, title: language, air_date: "2025-01-03", runtime_minutes: 24, still_url: still, overview: `Overview: ${language}`,
      }] });
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(apiMetadataEpisodeLoader(tmdb, 2)).resolves.toEqual([
      { episode: 1, title: "zh-CN", airDate: "2025-01-03", runtimeMinutes: 24, stillUrl: still, overview: "Overview: zh-CN" },
    ]);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0]?.[0]).toContain("/metadata/tmdb/works/265010/seasons/2/episodes?language=zh-CN");
    await expect(apiMetadataEpisodeLoader(tmdb, 2, { language: "ja-JP" })).resolves.toMatchObject([{ overview: "Overview: ja-JP" }]);
    await apiMetadataEpisodeLoader(tmdb, 2);
    await apiMetadataEpisodeLoader(tmdb, 2, { language: "ja-JP" });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    await apiMetadataEpisodeLoader(tmdb, 3);
    await apiMetadataEpisodeLoader({ ...tmdb, workId: "1" }, 2);
    expect(fetchMock).toHaveBeenCalledTimes(4);
  });

  it("forwards cancellation and does not cache a late response from an abandoned browse", async () => {
    const controller = new AbortController();
    const fetchMock = vi.fn(async (_path: string, _init?: RequestInit) => Response.json({ season: 2, episodes: [] }));
    vi.stubGlobal("fetch", fetchMock);
    const first = apiMetadataEpisodeLoader(tmdb, 2, { signal: controller.signal });
    controller.abort();
    await first;
    expect(fetchMock.mock.calls[0]?.[1]?.signal).toBe(controller.signal);
    await apiMetadataEpisodeLoader(tmdb, 2);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("reads a Bangumi subject's whole list with titles, durations and sections", async () => {
    const fetchMock = vi.fn().mockResolvedValue(Response.json({
      episodes: [
        { episode: 1, title: "第一话", original_title: "最初の夜", air_date: "2025-01-03", duration: "23:40", section: "main" },
        { episode: 1, title: "特别篇", section: "special" },
      ],
    }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(apiMetadataEpisodeLoader(bgm)).resolves.toEqual([
      { episode: 1, title: "第一话", originalTitle: "最初の夜", airDate: "2025-01-03", duration: "23:40", section: "main" },
      { episode: 1, title: "特别篇", section: "special" },
    ]);
    expect(fetchMock.mock.calls[0]?.[0]).toContain("/metadata/bgm/works/486109/episodes");
  });
});
