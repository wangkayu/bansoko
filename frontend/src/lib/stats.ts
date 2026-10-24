import type { CatalogEntryView } from "./providers/viewmodels";

// The subscribed-section count chip: counts subscription states only (not started / active /
// stopped), word-for-word the same as the card pills. A metadata block is not a state: an
// active subscription with missing links still counts as active, and the blocking reason only
// gets its own short chip on the card. Entries matching no segment (e.g. an unsubscribed
// custom preset) are not counted and are unaffected by filtering.

export type StatsFilter = "pending" | "active" | "stopped";

export interface StatsCounts {
  pending: number;
  active: number;
  stopped: number;
}

/** Segment of a single entry; undefined means it belongs to no segment. */
export function statsSegment(entry: CatalogEntryView): StatsFilter | undefined {
  if (entry.subscriptionState === "stopped") return "stopped";
  if (entry.subscriptionState === "pending") return "pending";
  if (entry.subscriptionState === "active") return "active";
  return undefined;
}

/** Count rows per segment over the unfiltered row set. */
export function statsCounts(entries: CatalogEntryView[]): StatsCounts {
  const counts: StatsCounts = { pending: 0, active: 0, stopped: 0 };
  for (const entry of entries) {
    const segment = statsSegment(entry);
    if (segment) counts[segment] += 1;
  }
  return counts;
}

/** Apply the chip filter; null keeps every row (including unsegmented ones). */
export function applyStatsFilter<T extends { entry: CatalogEntryView }>(
  rows: T[],
  filter: StatsFilter | null,
): T[] {
  if (filter === null) return rows;
  return rows.filter((row) => statsSegment(row.entry) === filter);
}
