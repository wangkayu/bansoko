import type { RSSListGroup, RSSListItem, RSSListView } from "../../lib/api/client";
import { releaseEpisodeLabel } from "../rss-list/model";

// The step-one selection model of "browse seeds": projecting the server's RSS read model into
//   multi-selectable rows.
//
//   Nothing is re-judged here: a seed's eligibility comes from the server's `download_action`,
//   grouping and order follow the server's response, and the filter is a single local title
//   substring match.

export type OneShotSeedRow = {
  /** Selection identity = snapshot + stable seed key (matching the one-shot request's path parameters). */
  key: string;
  snapshotID: string;
  stableSeedKey: string;
  groupKey: string;
  groupName: string;
  title: string;
  publishedAt?: string;
  sizeBytes?: number | null;
  /** The owning group's scope and seed-list identity; fed back verbatim into the request on submit. */
  scope: RSSListGroup["scope"];
  feed: RSSListGroup["feed"];
  item: RSSListItem;
};

export type OneShotSeedGroup = {
  key: string;
  name: string;
  /** The group's full identity; both refreshing that group's seeds and filling back the request rely on it. */
  scope: RSSListGroup["scope"];
  /** The server's seed-list state for the group; when stale the group needs a refresh first. */
  feedState: RSSListGroup["feed"]["state"];
  rows: OneShotSeedRow[];
};

export function seedKeyOf(item: Pick<RSSListItem, "snapshot_id" | "stable_seed_key">): string {
  return `${item.snapshot_id}:${item.stable_seed_key}`;
}

export function groupKeyOf(group: RSSListGroup): string {
  return `${group.scope.provider}:${group.scope.provider_entry_ref}:${group.scope.provider_group_ref}`;
}

/** Read model -> selection rows. Returns an empty array on a malformed response (the caller renders an empty state, never throws). */
export function seedGroupsOf(view: RSSListView | undefined): OneShotSeedGroup[] {
  if (!view || typeof view !== "object" || !Array.isArray(view.groups)) return [];
  return view.groups.map((group) => {
    const key = groupKeyOf(group);
    return {
      key,
      name: group.display_name,
      scope: group.scope,
      feedState: group.feed.state,
      rows: group.items.map((item) => ({
        key: seedKeyOf(item),
        snapshotID: item.snapshot_id,
        stableSeedKey: item.stable_seed_key,
        groupKey: key,
        groupName: group.display_name,
        title: item.seed.title,
        publishedAt: item.seed.published_at ?? undefined,
        sizeBytes: item.seed.size_bytes,
        scope: group.scope,
        feed: group.feed,
        item,
      })),
    };
  });
}

/** The title-keyword filter; a group with no hits does not render, while an unfiltered empty group stays (its own empty state has its own note). */
export function filterSeedGroups(groups: OneShotSeedGroup[], query: string): OneShotSeedGroup[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return groups;
  return groups
    .map((group) => ({ ...group, rows: group.rows.filter((row) => row.title.toLowerCase().includes(needle)) }))
    .filter((group) => group.rows.length > 0);
}

export function seedRowCount(groups: OneShotSeedGroup[]): number {
  return groups.reduce((total, group) => total + group.rows.length, 0);
}

export function selectableRows(groups: OneShotSeedGroup[]): OneShotSeedRow[] {
  return groups.flatMap((group) => group.rows).filter((row) => row.item.download_action.allowed);
}

/**
 * 组是否需要先刷新种子：服务端说这组的快照不是新鲜的。
 * `snapshot_required` 表示连身份都还没有，同样走刷新路径。
 */
export function groupNeedsRefresh(group: OneShotSeedGroup): boolean {
  return group.feedState !== "fresh";
}

export function toggleKey(keys: ReadonlySet<string>, key: string): Set<string> {
  const next = new Set(keys);
  if (next.has(key)) next.delete(key);
  else next.add(key);
  return next;
}

/** The seed is not yet expanded (the server gave only RSS facts). */
export function needsExpand(row: OneShotSeedRow): boolean {
  return row.item.torrent.state === "pending" && row.item.download_action.reason_code === "seed_not_expanded";
}

export function selectableRow(row: OneShotSeedRow): boolean {
  return row.item.download_action.allowed || needsExpand(row);
}

/** A row's episode numbers: parsed by the server with this task's parser; hidden when parsing produced nothing. */
export function seedEpisodeLabel(row: OneShotSeedRow): string | undefined {
  return releaseEpisodeLabel(row.item.evaluation.parsed_facts);
}

/** The newest release time in a group's seeds (an RSS fact); undefined when no seed has a time. */
export function latestPublishedAt(rows: readonly Pick<OneShotSeedRow, "publishedAt">[]): string | undefined {
  let latest: { at: string; time: number } | undefined;
  for (const row of rows) {
    if (!row.publishedAt) continue;
    const time = Date.parse(row.publishedAt);
    if (Number.isNaN(time)) continue;
    if (!latest || time > latest.time) latest = { at: row.publishedAt, time };
  }
  return latest?.at;
}

/** A shorthand of the target path: only the last segment survives ("…/show E03.mkv"); with no directory it returns as-is. */
export function shortTarget(target: string): string {
  const trimmed = target.replace(/\/+$/, "");
  const cut = trimmed.lastIndexOf("/");
  return cut > 0 ? `…/${trimmed.slice(cut + 1)}` : trimmed;
}

/** Inputs step two can jump to: empties the naming relies on map by source to the matching form field. */
export type FactField = "title" | "year" | "season" | "bgm" | "tmdb" | "kind" | "naming";

/**
 * 空值对应的表单项。番剧自己的值（标题、年份、季度、类型）跳到那一项；Bangumi、TMDB
 * 的值跳到各自的关联；其余（集数、文件、种子等）由模板引用，跳到命名。
 */
export function factField(path: string): FactField {
  if (path === "anime.year") return "year";
  if (path === "anime.title" || path === "anime.title_override") return "title";
  if (path === "anime.kind") return "kind";
  if (path === "anime.season" || path.startsWith("anime.season_")) return "season";
  if (path.startsWith("bgm.")) return "bgm";
  if (path.startsWith("tmdb.")) return "tmdb";
  return "naming";
}

