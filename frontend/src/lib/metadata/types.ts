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

export interface MetadataSearcher {
  readonly isMock: boolean;
  search(provider: MetadataProvider, query: string): Promise<MetadataCandidate[]>;
}
