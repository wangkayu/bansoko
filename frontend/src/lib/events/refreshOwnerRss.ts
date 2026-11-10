import type { QueryClient } from "@tanstack/react-query";
import { rssListKeys } from "../queryKeys";

// Weak keys do not retain old workspaces after query-cache eviction. Both the
// workspace observer and an SSE event may see the same snapshot.
const refreshed = new WeakMap<QueryClient, WeakMap<object, Set<string>>>();
const scopedEvents = new WeakMap<QueryClient, Map<string, number>>();

function snapshotOwners(client: QueryClient, workspace: unknown): Set<string> | undefined {
  if (workspace === null || typeof workspace !== "object") return undefined;
  let snapshots = refreshed.get(client);
  if (!snapshots) {
    snapshots = new WeakMap();
    refreshed.set(client, snapshots);
  }
  let owners = snapshots.get(workspace);
  if (!owners) {
    owners = new Set();
    snapshots.set(workspace, owners);
  }
  return owners;
}

/** Mark one scoped event until its workspace invalidation settles. Its RSS queue
 * already owns the list read, so a workspace observer only records the snapshot. */
export function beginScopedOwnerRssRefresh(client: QueryClient, animeID: string): () => void {
  let events = scopedEvents.get(client);
  if (!events) {
    events = new Map();
    scopedEvents.set(client, events);
  }
  events.set(animeID, (events.get(animeID) ?? 0) + 1);
  let finished = false;
  return () => {
    if (finished) return;
    finished = true;
    const active = scopedEvents.get(client);
    const count = active?.get(animeID) ?? 0;
    if (count <= 1) active?.delete(animeID);
    else active?.set(animeID, count - 1);
  };
}

export function clearScopedOwnerRssRefreshes(client: QueryClient): void {
  scopedEvents.delete(client);
}

/** Record the snapshot whose RSS read was already owned by the scoped queue.
 * React passive effects may run after the workspace invalidation Promise settles. */
export function markScopedOwnerRssSnapshot(client: QueryClient, animeID: string, workspace: unknown): void {
  snapshotOwners(client, workspace)?.add(animeID);
}

export function refreshOwnerRss(client: QueryClient, animeID: string, workspace: unknown): Promise<void> {
  const owners = snapshotOwners(client, workspace);
  if ((scopedEvents.get(client)?.get(animeID) ?? 0) > 0) {
    owners?.add(animeID);
    return Promise.resolve();
  }
  if (owners?.has(animeID)) return Promise.resolve();
  owners?.add(animeID);
  return client.invalidateQueries({ queryKey: rssListKeys.owner(animeID) });
}
