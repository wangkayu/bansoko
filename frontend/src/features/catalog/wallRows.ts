import { broadcastDayLabel, type BroadcastDay } from "../../lib/i18n/broadcastDay";
import { t } from "../../lib/i18n";
import type { ReleaseFormat } from "../../lib/api/client";

// The rows of the subscribed wall and the provider browse pages (the design language's Subscribed
//   wall ruling): Monday through Sunday, movie, OVA/ONA, custom, other — empty rows never render.
//   The placement rules live in wallRowOf.

type Weekday = Exclude<BroadcastDay, "tbd" | "none">;
export type WallRowKey = Weekday | "movie" | "ova" | "custom" | "other";

const WEEKDAYS: readonly Weekday[] = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"];
export const WALL_ROW_ORDER: readonly WallRowKey[] = [...WEEKDAYS, "movie", "ova", "custom", "other"];

/** The facts row placement needs. */
export interface WallRowFacts {
  broadcastDay?: BroadcastDay;
  /** The effective media kind: the pinned media_kind_override when present, otherwise the TMDB-binding's media_kind. */
  mediaKind?: "tv" | "movie";
  /** The release format the provider catalog states (a read-only fact); null or absent means there is none. */
  format?: ReleaseFormat | null;
  /** The subscription source is a custom RSS (CatalogEntryView.source === "custom"). */
  custom?: boolean;
}

function isWeekday(day: string | undefined): day is Weekday {
  return day !== undefined && (WEEKDAYS as readonly string[]).includes(day);
}

/** Rows: movie (a movie release format; without one, a movie media kind) -> an actual broadcast day
 *  -> OVA/ONA release format -> a custom source -> other. tv / an unstated format get no row of
 *  their own; undated and unscheduled entries land in custom or other. */
export function wallRowOf(facts: WallRowFacts): WallRowKey {
  const format = facts.format ?? undefined;
  if (format === "movie" || (format === undefined && facts.mediaKind === "movie")) return "movie";
  if (isWeekday(facts.broadcastDay)) return facts.broadcastDay;
  if (format === "ova" || format === "ona") return "ova";
  if (facts.custom) return "custom";
  return "other";
}

export interface WallRow<T> {
  key: WallRowKey;
  items: T[];
}

/** Splits by WALL_ROW_ORDER, keeps input order within a row, and skips empty rows. */
export function wallRows<T>(items: readonly T[], factsOf: (item: T) => WallRowFacts): WallRow<T>[] {
  const buckets = new Map<WallRowKey, T[]>();
  for (const item of items) {
    const key = wallRowOf(factsOf(item));
    const bucket = buckets.get(key);
    if (bucket) bucket.push(item);
    else buckets.set(key, [item]);
  }
  return WALL_ROW_ORDER.flatMap((key) => {
    const bucket = buckets.get(key);
    return bucket ? [{ key, items: bucket }] : [];
  });
}

export function wallRowLabel(key: WallRowKey): string {
  return isWeekday(key) ? broadcastDayLabel(key) : t(`grid.wallRow.${key}`);
}
