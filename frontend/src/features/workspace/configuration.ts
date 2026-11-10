import type { AnimeWorkspace, AnimeWorkspaceConfiguration } from "../../lib/api/client";
import { isWorkspaceOwnerChainStable } from "./ownerChain";

/** Both reads carry the same configuration owners; only the RSS read carries history. */
export type WorkspaceConfiguration = AnimeWorkspace | AnimeWorkspaceConfiguration;

export function workspaceStable(workspace: WorkspaceConfiguration): boolean {
  return "release_summary" in workspace ? workspace.release_summary.owner_chain_stable : isWorkspaceOwnerChainStable(workspace);
}

export function workspaceOverview(workspace: WorkspaceConfiguration): Pick<AnimeWorkspaceConfiguration["release_summary"], "count" | "latest_published_at"> {
  if ("release_summary" in workspace) return workspace.release_summary;
  return {
    count: workspace.releases.length,
    latest_published_at: workspace.releases
      .map(release => release.raw_facts.published_at)
      .filter((value): value is string => value !== undefined)
      .sort().at(-1),
  };
}

export function workspaceReleaseSummary(workspace: WorkspaceConfiguration): AnimeWorkspaceConfiguration["release_summary"] {
  if ("release_summary" in workspace) return workspace.release_summary;
  return {
    ...workspaceOverview(workspace),
    owner_chain_stable: workspaceStable(workspace),
    tag_observations: workspace.releases.map(release => ({
      anibt_tags: release.source_facts.anibt?.tags ?? [],
      title_tags: release.source_facts.title_tags?.tags ?? [],
    })).filter(tags => tags.anibt_tags.length > 0 || tags.title_tags.length > 0),
  };
}

/** Used by fixtures too: never fill omitted execution history with empty arrays. */
export function workspaceConfiguration(workspace: AnimeWorkspace): AnimeWorkspaceConfiguration {
  return {
    managed_anime: workspace.managed_anime,
    subscription: workspace.subscription,
    metadata: workspace.metadata,
    metadata_hold: workspace.metadata_hold,
    subgroups: workspace.subgroups,
    rules: workspace.rules,
    rss_sources: workspace.rss_sources,
    rss_source_runtimes: workspace.rss_source_runtimes,
    episode_progress: workspace.episode_progress,
    config_waiting_files: workspace.config_waiting_files,
    release_summary: workspaceReleaseSummary(workspace),
  };
}
