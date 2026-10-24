import type {
  AnimeWorkspace,
  Artifact,
  DownloadTask,
  FileOperation,
  Release,
  RSSSource,
  RSSSourceRuntime,
} from "../../lib/api/client";

// The workspace owner chain: RSSSource -> RSSSourceRuntime -> Release -> DownloadTask ->
//   Artifact -> FileOperation. All data comes from the workspace read model, grouped by owner
//   foreign keys on the frontend. The RSS page already renders only the server read model
//   (RSSListView); the sole consumer remaining here is the polling stability gate
//   `isWorkspaceOwnerChainStable`, with no page-rendering purpose anymore.

export type ReleaseChain = {
  release: Release;
  downloadTask?: DownloadTask;
  artifacts: Artifact[];
  fileOperations: FileOperation[];
};

export type SourceChain = {
  source: RSSSource;
  runtime?: RSSSourceRuntime;
  releases: ReleaseChain[];
  /**
   * Whether the source's provider group is in the subscription's current
   * effective selection (custom RSS sources always count as selected).
   * Selection feeds only the stability gate: unselected sources never produce
   * work and must not keep workspace polling alive.
   */
  selected: boolean;
};

const STABLE_RELEASE_VERDICTS = new Set<Release["verdict"]>([
  "waiting_metadata",
  "subscription_stopped",
  "source_unavailable",
  "parse_failed",
  "filtered",
  "adopted_existing",
  "acquisition_failed",
  "resume_baseline",
  "start_baseline",
]);

function isReleaseChainStable(chain: ReleaseChain): boolean {
  if (chain.artifacts.length > 0) {
    const operationsByArtifact = new Set(chain.fileOperations.map((operation) => operation.artifact_id));
    const artifactsAreStable = chain.artifacts.every(
      (artifact) => artifact.state === "failed"
        || (artifact.state === "verified" && operationsByArtifact.has(artifact.id)),
    );
    if (!artifactsAreStable) return false;

    return chain.fileOperations.every(
      (operation) => operation.state === "completed" || operation.state === "failed",
    );
  }

  if (chain.downloadTask) {
    return chain.downloadTask.state === "failed";
  }

  return STABLE_RELEASE_VERDICTS.has(chain.release.verdict);
}

export function isWorkspaceOwnerChainStable(workspace: AnimeWorkspace): boolean {
  const chains = buildOwnerChain(workspace);
  // Only selected sources participate in the stability gate. Unselected
  // discovered placeholders never produce work and must not block polling.
  const active = chains.filter((chain) => chain.selected);
  if (active.length === 0) return false;

  return active.every((chain) => {
    if (chain.releases.length > 0) {
      return chain.releases.every(isReleaseChainStable);
    }
    return chain.runtime !== undefined && chain.runtime.health !== "unknown";
  });
}

export function buildOwnerChain(workspace: AnimeWorkspace): SourceChain[] {
  const selectedGroups = new Set(
    workspace.subgroups.effective_selection
      .filter((selection) => selection.effective_selected)
      .map((selection) => `${selection.provider}:${selection.group_ref}`),
  );

  const runtimeBySource = new Map<string, RSSSourceRuntime>();
  for (const runtime of workspace.rss_source_runtimes) {
    runtimeBySource.set(runtime.rss_source_id, runtime);
  }
  const taskById = new Map<string, DownloadTask>();
  for (const task of workspace.download_tasks) {
    taskById.set(task.id, task);
  }
  const artifactsByTask = new Map<string, Artifact[]>();
  for (const artifact of workspace.artifacts) {
    const list = artifactsByTask.get(artifact.download_task_id) ?? [];
    list.push(artifact);
    artifactsByTask.set(artifact.download_task_id, list);
  }
  const fileOpsByArtifact = new Map<string, FileOperation[]>();
  for (const operation of workspace.file_operations) {
    const list = fileOpsByArtifact.get(operation.artifact_id) ?? [];
    list.push(operation);
    fileOpsByArtifact.set(operation.artifact_id, list);
  }

  const releasesBySource = new Map<string, Release[]>();
  for (const release of workspace.releases) {
    const list = releasesBySource.get(release.rss_source_id) ?? [];
    list.push(release);
    releasesBySource.set(release.rss_source_id, list);
  }

  return workspace.rss_sources.map((source) => {
    const releases = (releasesBySource.get(source.id) ?? []).map((release) => {
      const downloadTask = release.download_task_id ? taskById.get(release.download_task_id) : undefined;
      const artifacts = downloadTask ? (artifactsByTask.get(downloadTask.id) ?? []) : [];
      const fileOperations = artifacts.flatMap((artifact) => fileOpsByArtifact.get(artifact.id) ?? []);
      return { release, downloadTask, artifacts, fileOperations };
    });
    const provenance = source.provider_provenance;
    const selected =
      source.source_kind === "custom_rss"
      || (provenance !== null && provenance !== undefined
        && selectedGroups.has(`${provenance.provider}:${provenance.provider_group_ref}`));
    return {
      source,
      runtime: runtimeBySource.get(source.id),
      releases,
      selected,
    };
  });
}
