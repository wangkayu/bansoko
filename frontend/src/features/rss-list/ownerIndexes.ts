import type { AnimeWorkspace } from "../../lib/api/client";

function indexed<T, K>(key: (item: T) => K) {
  const cache = new WeakMap<T[], ReadonlyMap<K, T>>();
  return (items: T[]) => {
    let result = cache.get(items);
    if (!result) {
      const map = new Map<K, T>();
      for (const item of items) {
        const id = key(item);
        if (!map.has(id)) map.set(id, item);
      }
      result = map;
      cache.set(items, result);
    }
    return result;
  };
}

function grouped<T, K>(key: (item: T) => K) {
  const cache = new WeakMap<T[], ReadonlyMap<K, readonly T[]>>();
  return (items: T[]) => {
    let result = cache.get(items);
    if (!result) {
      const map = new Map<K, T[]>();
      for (const item of items) {
        const id = key(item);
        const group = map.get(id);
        if (group) group.push(item);
        else map.set(id, [item]);
      }
      result = map;
      cache.set(items, result);
    }
    return result;
  };
}

// Query data is immutable. Weak array keys reuse unchanged sections across
// workspace updates and release them when those query snapshots are collected.
const releases = indexed((item: AnimeWorkspace["releases"][number]) => item.id);
const tasks = indexed((item: AnimeWorkspace["download_tasks"][number]) => item.id);
const artifacts = grouped((item: AnimeWorkspace["artifacts"][number]) => item.download_task_id);
const operations = grouped((item: AnimeWorkspace["file_operations"][number]) => item.artifact_id);
const sources = indexed((item: AnimeWorkspace["rss_sources"][number]) => item.id);
const runtimes = indexed((item: AnimeWorkspace["rss_source_runtimes"][number]) => item.rss_source_id);

export function ownerIndexes(workspace: AnimeWorkspace) {
  return {
    releases: releases(workspace.releases),
    tasks: tasks(workspace.download_tasks),
    artifacts: artifacts(workspace.artifacts),
    operations: operations(workspace.file_operations),
    sources: sources(workspace.rss_sources),
    runtimes: runtimes(workspace.rss_source_runtimes),
  };
}

export type OwnerIndexes = ReturnType<typeof ownerIndexes>;
