import type { QueryClient } from "@tanstack/react-query";
import { rssListKeys } from "../queryKeys";

// Weak keys do not retain old workspaces after query-cache eviction. Both the
// workspace observer and SSE completion may see the same new snapshot.
const refreshed = new WeakMap<QueryClient, WeakMap<object, Set<string>>>();

export function refreshOwnerRss(client: QueryClient, animeID: string, workspace: unknown, force = false): Promise<void> {
  if (workspace !== null && typeof workspace === "object") {
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
    if (!force && owners.has(animeID)) return Promise.resolve();
    owners.add(animeID);
  }
  return client.invalidateQueries({ queryKey: rssListKeys.owner(animeID) });
}
