import { listBGMCandidateEpisodes, listTMDBCandidateEpisodes, type TMDBEpisodeLanguage } from "../api/client";
import type { MetadataEpisode, MetadataEpisodeLoader } from "./types";

export const DEFAULT_EPISODE_LANGUAGE: TMDBEpisodeLanguage = "zh-CN";
const MAX_EPISODE_CACHE_ENTRIES = 32;
const tmdbEpisodeCache = new Map<string, MetadataEpisode[]>();

export function resetEpisodeCache(): void {
  tmdbEpisodeCache.clear();
}

async function tmdbEpisodes(workId: string, season: number, language: TMDBEpisodeLanguage, signal?: AbortSignal) {
  const key = `${workId}:${season}:${language}`;
  const cached = tmdbEpisodeCache.get(key);
  if (cached) return cached;

  const result = await listTMDBCandidateEpisodes(workId, season, language, signal);
  const episodes = result.episodes.map((episode): MetadataEpisode => ({
    episode: episode.episode,
    title: episode.title,
    airDate: episode.air_date,
    runtimeMinutes: episode.runtime_minutes,
    stillUrl: episode.still_url,
    overview: episode.overview,
  }));
  // An abandoned request must not populate or overwrite the session cache.
  if (!signal?.aborted) {
    const oldest = tmdbEpisodeCache.keys().next();
    if (tmdbEpisodeCache.size >= MAX_EPISODE_CACHE_ENTRIES && !oldest.done) tmdbEpisodeCache.delete(oldest.value);
    tmdbEpisodeCache.set(key, episodes);
  }
  return episodes;
}

// Both search candidates and current bindings are browsed by provider work ID.
export const apiMetadataEpisodeLoader: MetadataEpisodeLoader = async (work, season, options) => {
  if (work.provider === "tmdb") {
    return tmdbEpisodes(work.workId, season ?? 1, options?.language ?? DEFAULT_EPISODE_LANGUAGE, options?.signal);
  }
  const result = await listBGMCandidateEpisodes(work.workId, options?.signal);
  return result.episodes.map((episode): MetadataEpisode => ({
    episode: episode.episode,
    title: episode.title,
    originalTitle: episode.original_title,
    airDate: episode.air_date,
    duration: episode.duration,
    section: episode.section,
  }));
};

export const metadataEpisodeLoader: MetadataEpisodeLoader = apiMetadataEpisodeLoader;
