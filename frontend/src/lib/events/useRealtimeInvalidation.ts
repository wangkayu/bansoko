import { useSyncExternalStore } from "react";
import type { QueryClient, QueryKey } from "@tanstack/react-query";
import { acquisitionRecordKeys, animeKeys, catalogKeys, fileManagerKeys, observabilityKeys } from "../queryKeys";
import { refreshOwnerRss } from "./refreshOwnerRss";
import { createCoalescedInvalidation } from "./coalescedInvalidation";
import { useSessionStore } from "../../shell/sessionStore";
import { createRealtimeEventStream, type RealtimeEvent, type RealtimeStreamHandle } from "./sseClient";

// One SSE connection is shared by the authenticated app. Catalog and subscription
// events refresh both browse projections and the shared subscription list.
// Scoped RSS events read the workspace before coordinating its RSS list refresh
// with RssTab; unchanged workspace snapshots still allow RSS-only updates.
// Unscoped RSS events refresh workspaces, whose observers refresh changed lists.
//
// RSS/record bursts also affect file-manager, execution-record and log queries.
// Coalesce these scopes for one second, collapse overlapping prefixes and queue
// a fresh pass for events received during a read. Never discard loaded pages.
// The shared subscription summary keeps its separate one-second window so a
// long history read cannot hold back card progress and health.
// Manual refresh and visible-page disconnected fallback polling are unchanged.

const ACQUISITION_PROGRESS_KEYS: ReadonlyArray<QueryKey> = [fileManagerKeys.all, acquisitionRecordKeys.all, observabilityKeys.records];

const TARGET_QUERY_KEYS: Record<RealtimeEvent["target"], ReadonlyArray<QueryKey>> = {
  catalog: [catalogKeys.all, animeKeys.list],
  subscribed: [animeKeys.list, catalogKeys.all],
  rss: [animeKeys.workspacePrefix],
  records: [observabilityKeys.all, fileManagerKeys.all, acquisitionRecordKeys.all],
};

const SUBSCRIPTION_LIST_KEYS: ReadonlyArray<QueryKey> = [animeKeys.list];
const SUBSCRIPTION_LIST_COALESCE_MS = 1000;

type Listener = () => void;

let queryClient: QueryClient | undefined;
let stream: RealtimeStreamHandle | undefined;
let running = false;
let connected = false;
let subscriptionListTimer: ReturnType<typeof setTimeout> | undefined;
let progressRefresh: ReturnType<typeof createCoalescedInvalidation> | undefined;
let unsubscribeSession: (() => void) | undefined;
const listeners = new Set<Listener>();

function emitChange() {
  for (const listener of listeners) listener();
}

function scheduleSubscriptionListRefresh() {
  if (subscriptionListTimer) return;
  subscriptionListTimer = setTimeout(() => {
    subscriptionListTimer = undefined;
    if (!queryClient) return;
    for (const queryKey of SUBSCRIPTION_LIST_KEYS) {
      void queryClient.invalidateQueries({ queryKey });
    }
  }, SUBSCRIPTION_LIST_COALESCE_MS);
}

const rssRefreshes = new Map<string, object>();

function handleEvent(event: RealtimeEvent) {
  if (!queryClient) return;
  const client = queryClient;
  if (event.target === "rss" && event.managedAnimeId) {
    const animeID = event.managedAnimeId;
    const key = animeKeys.workspace(animeID);
    const before = client.getQueryData(key);
    const refresh = {};
    rssRefreshes.set(animeID, refresh);
    void client.invalidateQueries({ queryKey: key }).then(() => {
      // A newer event owns the trailing read if this workspace read was cancelled.
      if (rssRefreshes.get(animeID) !== refresh || queryClient !== client) return;
      rssRefreshes.delete(animeID);
      const workspace = client.getQueryData(key);
      // Even unchanged/absent workspace data must not swallow an RSS-only event.
      void refreshOwnerRss(client, animeID, workspace, workspace === before);
    });
  } else if (event.target !== "records") {
    for (const queryKey of TARGET_QUERY_KEYS[event.target]) {
      void client.invalidateQueries({ queryKey });
    }
  }
  if (event.target === "rss" || event.target === "records") {
    progressRefresh ??= createCoalescedInvalidation(client);
    progressRefresh.enqueue(event.target === "rss" ? ACQUISITION_PROGRESS_KEYS : TARGET_QUERY_KEYS.records);
    scheduleSubscriptionListRefresh();
  }
}

function clearPendingRefreshes() {
  progressRefresh?.dispose();
  progressRefresh = undefined;
  clearTimeout(subscriptionListTimer);
  subscriptionListTimer = undefined;
  rssRefreshes.clear();
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
