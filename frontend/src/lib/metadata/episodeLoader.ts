import { listBGMCandidateEpisodes, listTMDBCandidateEpisodes } from "../api/client";
import type { MetadataEpisode, MetadataEpisodeLoader } from "./types";

// Candidate EP reference reads the same providers the match dialog searches.
// It is read-only and independent of any existing anime binding; the server
// keeps it out of the naming episode-title cache.
export const apiMetadataEpisodeLoader: MetadataEpisodeLoader = async (candidate, season) => {
  if (candidate.provider === "tmdb") {
    const result = await listTMDBCandidateEpisodes(candidate.workId, season ?? 1);
    return result.episodes.map(
      (episode): MetadataEpisode => ({
        episode: episode.episode,
        title: episode.title,
        airDate: episode.air_date,
      }),
    );
  }
  const result = await listBGMCandidateEpisodes(candidate.workId);
  return result.episodes.map(
    (episode): MetadataEpisode => ({
      episode: episode.episode,
      title: episode.title,
      airDate: episode.air_date,
      section: episode.section,
    }),
  );
};

export const metadataEpisodeLoader: MetadataEpisodeLoader = apiMetadataEpisodeLoader;
