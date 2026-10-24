import { searchMetadata as apiSearchMetadata } from "../api/client";
import type { MetadataCandidate, MetadataSearcher, MetadataSeason } from "./types";

export const apiMetadataSearcher: MetadataSearcher = {
  isMock: false,
  async search(provider, query) {
    const result = await apiSearchMetadata(provider, query);
    return result.candidates.map((c): MetadataCandidate => ({
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
    }));
  },
};

export const metadataSearcher: MetadataSearcher = apiMetadataSearcher;
