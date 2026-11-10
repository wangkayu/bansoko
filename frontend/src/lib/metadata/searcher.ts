import { searchMetadata as apiSearchMetadata, searchMetadataAutomatically } from "../api/client";
import type { MetadataCandidate, MetadataSearcher, MetadataSeason } from "./types";

function toCandidate(c: import("../api/client").MetadataSearchCandidate): MetadataCandidate {
  return {
    provider: c.provider,
    workId: c.provider_work_id,
    title: c.title,
    originalTitle: c.original_title,
    overview: c.overview,
    posterUrl: c.poster_url,
    mediaKind: c.media_kind,
    detailsStatus: c.details_status,
    seasons: c.seasons?.map((season): MetadataSeason => ({
      seasonNumber: season.season_number,
      name: season.name,
      episodeCount: season.episode_count,
      airDate: season.air_date,
      isSpecial: season.is_special,
    })),
    suggestedSeason: c.suggested_season ?? undefined,
    year: c.year ?? undefined,
    score: c.score,
  };
}

export const apiMetadataSearcher: MetadataSearcher = {
  isMock: false,
  async search(provider, query) {
    const result = await apiSearchMetadata(provider, query);
    return result.candidates.map(toCandidate);
  },
  async searchAutomatic(provider, input) {
    const result = await searchMetadataAutomatically({
      provider,
      title: input.title,
      origin: input.origin,
      origin_entry_ref: input.originEntryRef,
      bgm_work_id: input.bgmWorkId,
      tmdb_work_id: input.tmdbWorkId,
      tmdb_media_kind: input.tmdbMediaKind,
      tmdb_season: input.tmdbSeason,
    });
    return result.candidates.map(toCandidate);
  },
};

export const metadataSearcher: MetadataSearcher = apiMetadataSearcher;
