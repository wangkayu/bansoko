import { useSyncExternalStore } from "react";
import type { QueryClient, QueryKey } from "@tanstack/react-query";
import { acquisitionRecordKeys, animeKeys, catalogKeys, fileManagerKeys, observabilityKeys, rssListKeys } from "../queryKeys";
import { beginScopedOwnerRssRefresh, clearScopedOwnerRssRefreshes, markScopedOwnerRssSnapshot } from "./refreshOwnerRss";
import { createCoalescedInvalidation } from "./coalescedInvalidation";
import { useSessionStore } from "../../shell/sessionStore";
import { createRealtimeEventStream, type RealtimeEvent, type RealtimeStreamHandle } from "./sseClient";

// One SSE connection is shared by the authenticated app. Catalog and subscription
// events refresh both browse projections and the shared subscription list.
// Scoped RSS events start workspace and RSS invalidation together. A scoped
// lifecycle marker prevents RssTab's workspace observer from repeating the RSS
// read; unchanged or absent workspace snapshots never swallow an RSS-only event.
// Unscoped RSS events refresh workspaces, whose observers refresh changed lists.
//
// RSS/record bursts also affect file-manager, execution-record and log queries.
// Each queue starts an idle batch at the microtask checkpoint, collapses
// overlapping prefixes and gives events received during a read one trailing pass.
// Separate queues keep long history reads from holding back cards or the RSS list.
// Manual refresh and visible-page disconnected fallback polling are unchanged.

const ACQUISITION_PROGRESS_KEYS: ReadonlyArray<QueryKey> = [fileManagerKeys.all, acquisitionRecordKeys.all, observabilityKeys.records];

const TARGET_QUERY_KEYS: Record<RealtimeEvent["target"], ReadonlyArray<QueryKey>> = {
  catalog: [catalogKeys.all, animeKeys.list],
  subscribed: [animeKeys.list, catalogKeys.all],
  rss: [animeKeys.workspacePrefix],
  records: [observabilityKeys.all, fileManagerKeys.all, acquisitionRecordKeys.all],
};

const SUBSCRIPTION_LIST_KEYS: ReadonlyArray<QueryKey> = [animeKeys.list];

type Listener = () => void;

let queryClient: QueryClient | undefined;
let stream: RealtimeStreamHandle | undefined;
let running = false;
let connected = false;
let subscriptionListRefresh: ReturnType<typeof createCoalescedInvalidation> | undefined;
let progressRefresh: ReturnType<typeof createCoalescedInvalidation> | undefined;
const scopedRssRefreshes = new Map<string, ReturnType<typeof createCoalescedInvalidation>>();
let unsubscribeSession: (() => void) | undefined;
const listeners = new Set<Listener>();

function emitChange() {
  for (const listener of listeners) listener();
}

function handleEvent(event: RealtimeEvent) {
  if (!queryClient) return;
  const client = queryClient;
  if (event.target === "rss" && event.managedAnimeId) {
    const animeID = event.managedAnimeId;
    const finish = beginScopedOwnerRssRefresh(client, animeID);
    let refresh = scopedRssRefreshes.get(animeID);
    if (!refresh) {
      refresh = createCoalescedInvalidation(client);
      scopedRssRefreshes.set(animeID, refresh);
    }
    refresh.enqueue([rssListKeys.owner(animeID)]);
    const workspaceKey = animeKeys.workspace(animeID);
    const settled = () => {
      markScopedOwnerRssSnapshot(client, animeID, client.getQueryData(workspaceKey));
      finish();
    };
    void client.invalidateQueries({ queryKey: workspaceKey }).then(settled, settled);
  } else if (event.target !== "records") {
    for (const queryKey of TARGET_QUERY_KEYS[event.target]) {
      void client.invalidateQueries({ queryKey });
    }
  }
  if (event.target === "rss" || event.target === "records") {
    progressRefresh ??= createCoalescedInvalidation(client);
    progressRefresh.enqueue(event.target === "rss" ? ACQUISITION_PROGRESS_KEYS : TARGET_QUERY_KEYS.records);
    subscriptionListRefresh ??= createCoalescedInvalidation(client);
    subscriptionListRefresh.enqueue(SUBSCRIPTION_LIST_KEYS);
  }
}

function clearPendingRefreshes() {
  progressRefresh?.dispose();
  progressRefresh = undefined;
  subscriptionListRefresh?.dispose();
  subscriptionListRefresh = undefined;
  scopedRssRefreshes.forEach(refresh => refresh.dispose());
  scopedRssRefreshes.clear();
  if (queryClient) clearScopedOwnerRssRefreshes(queryClient);
}

function syncWithSession() {
  const authenticated = useSessionStore.getState().authenticated;
  if (authenticated && !running) {
    running = true;
    stream?.start();
  } else if (!authenticated && running) {
    running = false;
    stream?.stop();
    clearPendingRefreshes();
    if (connected) {
      connected = false;
      emitChange();
    }
  }
}

/** Binds the realtime runtime to the app's QueryClient. Idempotent. */
export function initRealtimeInvalidation(client: QueryClient): void {
  if (queryClient === client) return;
  clearPendingRefreshes();
  unsubscribeSession?.();
  queryClient = client;
  stream ??= createRealtimeEventStream({
    isAuthenticated: () => useSessionStore.getState().authenticated,
    onEvent: handleEvent,
    onStateChange: (next) => {
      if (connected === next) return;
      connected = next;
      emitChange();
    },
    onUnauthorized: () => useSessionStore.getState().clearSession(),
  });
  syncWithSession();
  unsubscribeSession = useSessionStore.subscribe(syncWithSession);
}

/** Test hook: tear the singleton down between suites. */
export function resetRealtimeInvalidationForTests(): void {
  clearPendingRefreshes();
  unsubscribeSession?.();
  unsubscribeSession = undefined;
  stream?.stop();
  stream = undefined;
  queryClient = undefined;
  running = false;
  connected = false;
}

/** Whether surfaces can rely on push invalidation instead of fallback polling. */
export function useRealtimeConnected(): boolean {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    () => connected,
    () => false,
  );
}
