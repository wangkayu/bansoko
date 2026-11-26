import type { SubscriptionOrigin, TMDBEpisodeLanguage } from "../api/client";

export type MetadataProvider = "bgm" | "tmdb";
export type MetadataDetailsStatus = "available" | "empty" | "error" | "not_applicable";

export interface MetadataSeason {
  seasonNumber: number;
  name: string;
  episodeCount: number;
  airDate: string;
  isSpecial: boolean;
}

export interface MetadataCandidate {
  provider: MetadataProvider;
  workId: string;
  title: string;
  titleCN?: string;
  originalTitle?: string;
  posterUrl?: string;
  mediaKind?: "tv" | "movie";
  detailsStatus: MetadataDetailsStatus;
  seasons?: MetadataSeason[];
  suggestedSeason?: number;
  year?: number;
  overview?: string;
  score?: number;
}

export interface MetadataAutomaticSearchInput {
  title: string;
  origin: SubscriptionOrigin;
  originEntryRef?: string;
  bgmWorkId?: string;
  tmdbWorkId?: string;
  tmdbMediaKind?: "tv" | "movie";
  tmdbSeason?: number;
}

export interface MetadataSearcher {
  readonly isMock: boolean;
  search(provider: MetadataProvider, query: string): Promise<MetadataCandidate[]>;
  searchAutomatic(provider: MetadataProvider, input: MetadataAutomaticSearchInput): Promise<MetadataCandidate[]>;
}

export interface MetadataEpisode {
  episode: number;
  title?: string;
  /** BGM's original-language title; shown under the localized one when it differs. */
  originalTitle?: string;
  airDate?: string;
  /** BGM's raw duration text (`00:24:00`, `24m`, `23:40`); formatted for display. */
  duration?: string;
  /** TMDB's runtime in whole minutes. */
  runtimeMinutes?: number;
  /** Same-origin proxy path of the episode still; only the preview dialog loads it. */
  stillUrl?: string;
  overview?: string;
  /** BGM's item-level episode sections; TMDB season lists leave this unset. */
  section?: "main" | "special";
}

export type MetadataEpisodeWork = Pick<MetadataCandidate, "provider" | "workId" | "title">;

/** Per-request options. `language` only affects TMDB; `signal` cancels an abandoned browse. */
export type MetadataEpisodeLoadOptions = {
  language?: TMDBEpisodeLanguage;
  signal?: AbortSignal;
};

export type MetadataEpisodeLoader = (
  work: MetadataEpisodeWork,
  season?: number,
  options?: MetadataEpisodeLoadOptions,
) => Promise<MetadataEpisode[]>;
