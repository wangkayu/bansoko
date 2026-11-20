import { listBGMCandidateEpisodes, listTMDBCandidateEpisodes } from "../api/client";
import type { MetadataEpisode, MetadataEpisodeLoader } from "./types";

// Both search candidates and current bindings are browsed by provider work ID.
export const apiMetadataEpisodeLoader: MetadataEpisodeLoader = async (work, season) => {
  if (work.provider === "tmdb") {
    const result = await listTMDBCandidateEpisodes(work.workId, season ?? 1);
    return result.episodes.map(
      (episode): MetadataEpisode => ({
        episode: episode.episode,
        title: episode.title,
        airDate: episode.air_date,
      }),
    );
  }
  const result = await listBGMCandidateEpisodes(work.workId);
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
