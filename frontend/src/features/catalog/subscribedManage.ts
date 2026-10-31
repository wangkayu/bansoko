import type { AnimeListItem } from "../../lib/api/client";
import { importedCount, progressDone, progressTotal, type EpisodeProgressLike } from "../../lib/progress";
import { seasonIndex } from "./season";

// The subscribed-instance management table's row model (SubscribedManageDialog).
// 入口 and 季度 are two independent dimensions (owner ruling): 入口 = the origin
// (anibt / mikan / 自定义), 季度 = the provider season catalog page the instance
// was created from, frozen at creation. Search-created and custom instances have
// no season; mikan 2026-spring and anibt 2026-spring are different batches, so
// the two filters intersect instead of merging into one column value.

export type ManageOrigin = "anibt" | "mikan" | "custom";
export type ManageState = "pending" | "active" | "stopped";
export type ManageStateFilter = "all" | ManageState;
export type ManageProgressFilter = "all" | "doing" | "done" | "none";
export type ManageOriginFilter = "all" | ManageOrigin;
/** "all" | "none" (no season) | a canonical YYYY-term present in the rows. */
export type ManageSeasonFilter = string;

export interface ManageRow {
  /** The managed anime id: the batch action identity. */
  key: string;
  title: string;
  instanceLabel?: string;
  origin: ManageOrigin;
  /** Same-work grouping key part; custom never groups (each preset is its own work). */
  originEntryRef?: string;
  /** Frozen creation provenance (canonical YYYY-term); absent for search/custom creations. */
  entrySeason?: string;
  /** The owner-edited naming season (anime.season), a display fact of the row. */
  showSeason?: number;
  state: ManageState;
  /** lifecycle_state === "retired": read-only, every batch action skips it. */
  retired: boolean;
  /** The subscription version the CAS patches and the delete carry. */
  version: number;
  rssHealth?: "ready" | "degraded" | "failed";
  progress?: EpisodeProgressLike;
  createdAt: string;
}

function originOf(item: AnimeListItem): ManageOrigin {
  // listAnime never returns oneshot instances (they are excluded server-side);
  // anything unexpected reads as custom, matching the wall's own mapping.
  return item.subscription.origin === "anibt" || item.subscription.origin === "mikan"
    ? item.subscription.origin
    : "custom";
}

export function manageRowsFrom(items: AnimeListItem[]): ManageRow[] {
  return items.map((item) => ({
    key: item.managed_anime.id,
    title: item.managed_anime.title,
    // The note distinguishes subscriptions of one anime. A note that only repeats the
    // title (the old provider-draft default) is no note: show nothing rather than the
    // title twice.
    instanceLabel: instanceNote(item.subscription.instance_label, item.managed_anime.title),
    origin: originOf(item),
    originEntryRef: item.subscription.origin_entry_ref ?? undefined,
    entrySeason: item.subscription.entry_season ?? undefined,
    showSeason: item.metadata.season ?? undefined,
    state: item.subscription.state,
    retired: item.subscription.lifecycle_state === "retired",
    version: item.subscription.version,
    rssHealth: item.rss_health ?? undefined,
    progress: item.episode_progress ?? undefined,
    createdAt: item.subscription.created_at,
  }));
}

function instanceNote(label: string | null | undefined, title: string): string | undefined {
  if (!label || label === title) return undefined;
  return label;
}

/** 已完成 (the filter): the total is known and more than one episode is imported and
 *  everything announced is in — a single-file movie (1/1) shows the 完成 pill but never
 *  lands in this filter. */
export function manageCompleted(row: ManageRow): boolean {
  const total = progressTotal(row.progress);
  return total !== undefined && importedCount(row.progress) > 1 && progressDone(row.progress);
}

function manageAllDone(row: ManageRow): boolean {
  return progressDone(row.progress);
}

export interface ManageFilters {
  state: ManageStateFilter;
  progress: ManageProgressFilter;
  origin: ManageOriginFilter;
  season: ManageSeasonFilter;
  sort: ManageSort;
  query: string;
}

/** The row order: the name (A→Z by default), the creation time, or the entry season. */
export type ManageSortKey = "name" | "created" | "season";

export interface ManageSort {
  key: ManageSortKey;
  /** Descending: Z→A, newest first, latest season first. */
  desc: boolean;
}

export const defaultManageSort: ManageSort = { key: "name", desc: false };

export const emptyManageFilters: ManageFilters = {
  state: "all",
  progress: "all",
  origin: "all",
  season: "all",
  sort: defaultManageSort,
  query: "",
};

export function filterManageRows(rows: ManageRow[], filters: ManageFilters): ManageRow[] {
  const query = filters.query.trim().toLowerCase();
  return rows.filter((row) => {
    if (filters.state !== "all" && row.state !== filters.state) return false;
    switch (filters.progress) {
      case "done":
        if (!manageCompleted(row)) return false;
        break;
      case "doing":
        if (!(importedCount(row.progress) > 0 && !manageAllDone(row))) return false;
        break;
      case "none":
        if (importedCount(row.progress) > 0) return false;
        break;
    }
    if (filters.origin !== "all" && row.origin !== filters.origin) return false;
    if (filters.season !== "all" && (row.entrySeason ?? "none") !== filters.season) return false;
    if (query) {
      const haystack = `${row.title} ${row.instanceLabel ?? ""} ${row.origin}`.toLowerCase();
      if (!haystack.includes(query)) return false;
    }
    return true;
  });
}

// Name order follows the Chinese (pinyin) collation the rest of the product reads titles in;
// numeric runs compare as numbers so 「第2季」 sits before 「第10季」.
const nameCollator = new Intl.Collator("zh-Hans", { numeric: true, sensitivity: "base" });

/** Same-work instances (origin + origin_entry_ref) stay adjacent. The sort orders the groups
 *  between themselves by their edge value (name, the newest/oldest creation, the latest/earliest
 *  entry season). Members inside a group always follow creation ascending — the sort key is about
 *  telling works apart, and inside one work the original must stay ahead of its duplicates even
 *  when an owner-edited title or a different entry season would sort otherwise. `grouped` marks
 *  the rows of a multi-instance work. */
export function orderManageRows(
  rows: ManageRow[],
  sort: ManageSort,
): Array<{ row: ManageRow; grouped: boolean }> {
  const groups = new Map<string, ManageRow[]>();
  for (const row of rows) {
    const key = row.origin === "custom" || !row.originEntryRef ? `#${row.key}` : `${row.origin}:${row.originEntryRef}`;
    const bucket = groups.get(key);
    if (bucket) bucket.push(row);
    else groups.set(key, [row]);
  }
  const ordered = [...groups.values()].map((members) => [...members].sort(compareMembers));
  ordered.sort((a, b) => compareGroups(a, b, sort) || a[0].key.localeCompare(b[0].key));
  return ordered.flatMap((members) =>
    members.map((row) => ({ row, grouped: members.length > 1 })),
  );
}

function compareMembers(a: ManageRow, b: ManageRow): number {
  return a.createdAt.localeCompare(b.createdAt) || a.key.localeCompare(b.key);
}

function compareGroups(a: ManageRow[], b: ManageRow[], sort: ManageSort): number {
  if (sort.key === "season") {
    return compareSeasons(groupSeason(a, sort.desc), groupSeason(b, sort.desc), sort.desc);
  }
  if (sort.key === "name") {
    const byName = nameCollator.compare(groupTitle(a), groupTitle(b));
    return sort.desc ? -byName : byName;
  }
  const byCreated = groupCreated(a, sort.desc).localeCompare(groupCreated(b, sort.desc));
  return sort.desc ? -byCreated : byCreated;
}

function groupTitle(members: ManageRow[]): string {
  return members[0].title;
}

function groupCreated(members: ManageRow[], desc: boolean): string {
  const times = members.map((member) => member.createdAt).sort();
  return desc ? times[times.length - 1] : times[0];
}

function groupSeason(members: ManageRow[], desc: boolean): string | undefined {
  const seasons = members
    .map((member) => member.entrySeason)
    .filter((season): season is string => Boolean(season))
    .sort((a, b) => (seasonIndex(a) ?? 0) - (seasonIndex(b) ?? 0));
  if (seasons.length === 0) return undefined;
  return desc ? seasons[seasons.length - 1] : seasons[0];
}

/** A missing season always sorts last, never flipped by the direction. */
function compareSeasons(a: string | undefined, b: string | undefined, desc: boolean): number {
  const ai = a ? seasonIndex(a) : undefined;
  const bi = b ? seasonIndex(b) : undefined;
  if (ai === undefined && bi === undefined) return 0;
  if (ai === undefined) return 1;
  if (bi === undefined) return -1;
  return desc ? bi - ai : ai - bi;
}

/** The 入口 options actually present in the data, in the fixed order. */
export function manageOriginOptions(rows: ManageRow[]): ManageOrigin[] {
  const present = new Set(rows.map((row) => row.origin));
  return (["anibt", "mikan", "custom"] as const).filter((origin) => present.has(origin));
}

/** The distinct seasons present, newest first; `true` in the second slot when any row has none. */
export function manageSeasonOptions(rows: ManageRow[]): { seasons: string[]; hasNone: boolean } {
  const present = new Set<string>();
  let hasNone = false;
  for (const row of rows) {
    if (row.entrySeason) present.add(row.entrySeason);
    else hasNone = true;
  }
  const seasons = [...present].sort((a, b) => (seasonIndex(b) ?? 0) - (seasonIndex(a) ?? 0));
  return { seasons, hasNone };
}

/** Batch eligibility: retired rows never act, pending cannot pause, nothing but a stop re-starts. */
export function startable(row: ManageRow): boolean {
  return !row.retired && row.state !== "active";
}

export function pausable(row: ManageRow): boolean {
  return !row.retired && row.state === "active";
}
