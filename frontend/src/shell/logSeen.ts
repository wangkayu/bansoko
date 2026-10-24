import { useSyncExternalStore } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  listObservabilityDiagnostics,
  listObservabilityRecords,
  type DiagnosticEpisode,
  type ObservabilityRecord,
} from "../lib/api/client";
import { observabilityKeys } from "../lib/queryKeys";

// The "seen" marks behind the log badge: they exist only in this browser (the contract has
// no database read state and no inbox). Each tab (events / diagnostics) records one time: when
// that tab is open and unfiltered and the first page has rendered, the mark is set to the
// server time of the newest entry in the list (never the local clock, to avoid clock skew).
// The badge = the failures across both tabs that are newer than their tab's mark. When
// localStorage is unavailable, reads and writes silently skip and everything counts as unseen.

export type LogSeenTab = "records" | "diagnostics";

const STORAGE_KEYS: Record<LogSeenTab, string> = {
  records: "bansoko.log.seen.records",
  diagnostics: "bansoko.log.seen.diagnostics",
};

const BADGE_LIMIT = 50;

const listeners = new Set<() => void>();

function notify() {
  for (const listener of listeners) listener();
}

function onStorage(event: StorageEvent) {
  if (event.key === null || Object.values(STORAGE_KEYS).includes(event.key)) notify();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  if (listeners.size === 1) globalThis.addEventListener?.("storage", onStorage);
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0) globalThis.removeEventListener?.("storage", onStorage);
  };
}

export function readLogSeen(tab: LogSeenTab): string | null {
  try {
    const value = globalThis.localStorage?.getItem(STORAGE_KEYS[tab]) ?? null;
    return value !== null && Number.isFinite(Date.parse(value)) ? value : null;
  } catch {
    return null;
  }
}

/** The mark only advances: a time older than the existing mark is never written. */
export function markLogSeen(tab: LogSeenTab, time: string | undefined) {
  if (!time || !Number.isFinite(Date.parse(time))) return;
  if (!isNewer(time, readLogSeen(tab))) return;
  try {
    globalThis.localStorage?.setItem(STORAGE_KEYS[tab], time);
  } catch {
    return;
  }
  notify();
}

export function useLogSeen(tab: LogSeenTab): string | null {
  return useSyncExternalStore(subscribe, () => readLogSeen(tab), () => null);
}

/** `time` is newer than the mark; without a mark everything counts as new. */
export function isNewer(time: string | undefined, mark: string | null): boolean {
  if (!time) return false;
  if (mark === null) return true;
  return Date.parse(time) > Date.parse(mark);
}

/** The time an event row shows in the log: a business record's occurred time, a notification delivery's last update. */
export function recordSeenTime(record: ObservabilityRecord): string | undefined {
  if (record.record_type === "business") return record.entry?.occurred_at;
  if (record.record_type === "plugin") return record.delivery?.updated_at ?? record.delivery?.created_at;
  return undefined;
}

/** A diagnostic episode's sort time (recovery time first), consistent with the list order. */
export function diagnosticSeenTime(episode: DiagnosticEpisode): string {
  return episode.succeeded_after_at ?? episode.last_failed_at;
}

/** Events shown as failures in the log: business failures, failed notification deliveries or unknown outcomes. */
export function isFailureRecord(record: ObservabilityRecord): boolean {
  if (record.record_type === "business") return record.entry?.outcome === "failure";
  if (record.record_type === "plugin") return record.delivery?.state === "failed" || record.delivery?.state === "unknown";
  return false;
}

/** Error diagnostics not yet recovered. */
export function isOpenError(episode: DiagnosticEpisode): boolean {
  return episode.severity === "error" && !episode.succeeded_after_at;
}

export function latestTime(times: Array<string | undefined>): string | undefined {
  let latest: string | undefined;
  for (const time of times) if (time && isNewer(time, latest ?? null)) latest = time;
  return latest;
}

/**
 * The count behind the log entry badge: only failures visible in the log (business +
 * notification deliveries on the events tab, unrecovered errors on the diagnostics tab), and
 * only those newer than the tab's "seen" mark. Shared by the top bar, the phone bottom bar
 * and the More sheet.
 */
export function useLogFailureBadge(): number {
  const recordsSeen = useLogSeen("records");
  const diagnosticsSeen = useLogSeen("diagnostics");
  const failures = useQuery({
    queryKey: observabilityKeys.badgeRecords("failure"),
    queryFn: () => listObservabilityRecords({ kind: ["business", "plugin"], outcome: "failure", limit: BADGE_LIMIT }),
  });
  // A notification delivery with an unknown result also shows as a failure in the log; the server outcome is unknown.
  const unknownDeliveries = useQuery({
    queryKey: observabilityKeys.badgeRecords("unknown"),
    queryFn: () => listObservabilityRecords({ kind: ["plugin"], outcome: "unknown", limit: BADGE_LIMIT }),
  });
  const errors = useQuery({
    queryKey: observabilityKeys.badgeDiagnostics,
    queryFn: () => listObservabilityDiagnostics({ severity: "error", limit: BADGE_LIMIT }),
  });
  const records = [...(failures.data?.records ?? []), ...(unknownDeliveries.data?.records ?? [])];
  const recordCount = records.filter((record) => isFailureRecord(record) && isNewer(recordSeenTime(record), recordsSeen)).length;
  const errorCount = (errors.data?.episodes ?? []).filter(
    (episode) => isOpenError(episode) && isNewer(episode.last_failed_at, diagnosticsSeen),
  ).length;
  return recordCount + errorCount;
}

export function badgeText(count: number): string {
  return count > 99 ? "99+" : String(count);
}
