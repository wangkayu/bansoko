import { useSyncExternalStore } from "react";
import type { QueryClient, QueryKey } from "@tanstack/react-query";
import { acquisitionRecordKeys, animeKeys, catalogKeys, fileManagerKeys, observabilityKeys, rssListKeys } from "../queryKeys";
import { useSessionStore } from "../../shell/sessionStore";
import { createRealtimeEventStream, type RealtimeEvent, type RealtimeStreamHandle } from "./sseClient";

// Realtime invalidation runtime (G1): one module-level SSE connection shared
// by the whole app, started once the session is authenticated and stopped on
// logout. Each event target maps to the TanStack Query key prefixes that own
// the corresponding read model:
//
//   catalog    → ["catalog"]            (the four-section list + the auto-refresh preference)
//   subscribed → ["anime", "list"] + ["catalog"]  (card summaries come from the catalog all aggregation)
//   rss        → scoped (event carries managed_anime_id):
//                  ["anime", "workspace", id] + ["rss-list-view", "owner", id]
//                  so only that anime's workspace and RSS list re-read;
//                unscoped (no id): ["anime", "workspace"] — an open RssTab
//                  re-reads its own list when its workspace changes.
//                The 30s polling window stays as the disconnected fallback.
//   records    → ["observability"]      (events + diagnostics)
//   rss and records also re-read ["file-manager"]: file states derive from
//   the owner rows those events report (imports, alignments, cleanups, version replacements).
//   rss and records also re-read ["acquisition-records"] (execution records) and
//   ["observability", "records"] (events): the download, artifact and file
//   operation work items that move an acquisition forward publish rss; the
//   settlement pass publishes rss and records; completion and newly written
//   business log entries publish records.
//
// Manual refresh buttons are untouched and remain the ultimate fallback.

const ACQUISITION_PROGRESS_KEYS: ReadonlyArray<QueryKey> = [fileManagerKeys.all, acquisitionRecordKeys.all, observabilityKeys.records];

const TARGET_QUERY_KEYS: Record<RealtimeEvent["target"], ReadonlyArray<QueryKey>> = {
  catalog: [catalogKeys.all],
  subscribed: [animeKeys.list, catalogKeys.all],
  rss: [animeKeys.workspacePrefix, ...ACQUISITION_PROGRESS_KEYS],
  records: [observabilityKeys.all, fileManagerKeys.all, acquisitionRecordKeys.all],
};

type Listener = () => void;

let queryClient: QueryClient | undefined;
let stream: RealtimeStreamHandle | undefined;
let running = false;
let connected = false;
const listeners = new Set<Listener>();

function emitChange() {
  for (const listener of listeners) listener();
}

function queryKeysFor(event: RealtimeEvent): ReadonlyArray<QueryKey> {
  if (event.target === "rss" && event.managedAnimeId) {
    return [animeKeys.workspace(event.managedAnimeId), rssListKeys.owner(event.managedAnimeId), ...ACQUISITION_PROGRESS_KEYS];
  }
  return TARGET_QUERY_KEYS[event.target];
}

function handleEvent(event: RealtimeEvent) {
  if (!queryClient) return;
  for (const queryKey of queryKeysFor(event)) {
    void queryClient.invalidateQueries({ queryKey });
  }
}

function syncWithSession() {
  const authenticated = useSessionStore.getState().authenticated;
  if (authenticated && !running) {
    running = true;
    stream?.start();
  } else if (!authenticated && running) {
    running = false;
    stream?.stop();
    if (connected) {
      connected = false;
      emitChange();
    }
  }
}

/** Binds the realtime runtime to the app's QueryClient. Idempotent. */
export function initRealtimeInvalidation(client: QueryClient): void {
  if (queryClient === client) return;
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
  useSessionStore.subscribe(syncWithSession);
}

/** Test hook: tear the singleton down between suites. */
export function resetRealtimeInvalidationForTests(): void {
  stream?.stop();
  stream = undefined;
  queryClient = undefined;
  running = false;
  connected = false;
}

/**
 * Whether the realtime stream is currently connected. Data surfaces use this
 * to decide if they can rely on push invalidation or must keep polling.
 */
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
