import { useSyncExternalStore } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  listObservabilityDiagnostics,
  listObservabilityRecords,
  type DiagnosticEpisode,
  type ObservabilityRecord,
} from "../lib/api/client";
import { isBadgeRecord } from "./logState";
import { observabilityKeys } from "../lib/queryKeys";

// The "seen" marks behind the log badge: they exist only in this browser (the contract has
// no database read state and no inbox). Two independent marks: the events mark advances to the
// server time of the newest entry when the unfiltered events list renders, the diagnostics mark
// when the diagnostics tab opens. Badge = failed acquisition results (one per seed) and
// failed / unconfirmed notifications newer than the events mark, plus open diagnostic episodes
// that first failed after the diagnostics mark. Repeats and recoveries never change it; the
// internal-error row never counts. When localStorage is unavailable, reads and writes silently
// skip and everything counts as unseen.

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

/** An open episode is a badge candidate: recovered ones never count. */
export function isOpenEpisode(episode: DiagnosticEpisode): boolean {
  return !episode.succeeded_after_at;
}

/** Open diagnostic episodes that first failed after the mark (repeats keep their first time, so they never re-count). */
export function newOpenEpisodes(episodes: readonly DiagnosticEpisode[], mark: string | null): DiagnosticEpisode[] {
  return episodes.filter((episode) => isOpenEpisode(episode) && isNewer(episode.first_failed_at, mark));
}

/** Records the badge counts: failed acquisition results and failed / unconfirmed notifications newer than the mark. */
export function newBadgeRecords(records: readonly ObservabilityRecord[], mark: string | null): ObservabilityRecord[] {
  return records.filter((record) => isBadgeRecord(record) && isNewer(recordSeenTime(record), mark));
}

export function latestTime(times: Array<string | undefined>): string | undefined {
  let latest: string | undefined;
  for (const time of times) if (time && isNewer(time, latest ?? null)) latest = time;
  return latest;
}

/**
 * The count behind the log entry badge (rule a, see the header). Shared by the top bar, the
 * phone bottom bar and the More sheet.
 */
export function useLogFailureBadge(): number {
  const recordsSeen = useLogSeen("records");
  const diagnosticsSeen = useLogSeen("diagnostics");
  const failures = useQuery({
    queryKey: observabilityKeys.badgeRecords,
    queryFn: () =>
      listObservabilityRecords({ kind: ["business", "plugin"], entry_kind: ["acquisition_settled"], state: "failure", limit: BADGE_LIMIT }),
  });
  const open = useQuery({
    queryKey: observabilityKeys.badgeDiagnostics,
    queryFn: () => listObservabilityDiagnostics({ state: "open", limit: BADGE_LIMIT }),
  });
  return newBadgeRecords(failures.data?.records ?? [], recordsSeen).length + newOpenEpisodes(open.data?.episodes ?? [], diagnosticsSeen).length;
}

export function badgeText(count: number): string {
  return count > 99 ? "99+" : String(count);
}
