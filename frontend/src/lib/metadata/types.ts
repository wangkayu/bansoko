import type { SubscriptionOrigin } from "../api/client";

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
  airDate?: string;
  /** BGM's item-level episode sections; TMDB season lists leave this unset. */
  section?: "main" | "special";
}

export type MetadataEpisodeWork = Pick<MetadataCandidate, "provider" | "workId" | "title">;

export type MetadataEpisodeLoader = (work: MetadataEpisodeWork, season?: number) => Promise<MetadataEpisode[]>;
