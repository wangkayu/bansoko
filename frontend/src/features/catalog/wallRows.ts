import { broadcastDayLabel, type BroadcastDay } from "../../lib/i18n/broadcastDay";
import { t } from "../../lib/i18n";
import type { ReleaseFormat } from "../../lib/api/client";

// The rows of the subscribed wall and the provider browse pages (the design language's Subscribed
//   wall ruling): weekdays rotated to start from today, then movie, OVA/ONA, custom, other — empty
//   rows never render. The placement rules live in wallRowOf.

export type Weekday = Exclude<BroadcastDay, "tbd" | "none">;
export type WallRowKey = Weekday | "movie" | "ova" | "custom" | "other";

const WEEKDAYS: readonly Weekday[] = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"];
const JS_DAY_TO_WEEKDAY: readonly Weekday[] = ["sun", "mon", "tue", "wed", "thu", "fri", "sat"];
export const WALL_ROW_ORDER: readonly WallRowKey[] = [...WEEKDAYS, "movie", "ova", "custom", "other"];

// Provider broadcast days (Mikan / AniBT) follow the Japanese TV broadcast calendar (JST, UTC+9)
// under the 28-hour late-night convention: a day runs from 04:00 JST to 28:00 JST (04:00 JST next
// calendar morning, i.e. 03:00 UTC+8). Shifting UTC by +5h (UTC+9 minus 4h) maps 04:00 JST to 00:00
// of the shifted UTC date so getUTCDay() is timezone-independent across all clients.
const JST_28H_UTC_OFFSET_MS = (9 - 4) * 60 * 60 * 1000;

/** Returns the active broadcast weekday key ("mon"…"sun") under the fixed JST (UTC+9) 28-hour clock. */
export function currentWeekday(now: Date = new Date()): Weekday {
  const shifted = new Date(now.getTime() + JST_28H_UTC_OFFSET_MS);
  return JS_DAY_TO_WEEKDAY[shifted.getUTCDay()] ?? "mon";
}

/** The facts row placement needs. */
export interface WallRowFacts {
  broadcastDay?: BroadcastDay;
  /** The effective media kind: the pinned media_kind when present, otherwise the TMDB-binding's media_kind. */
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
  isToday?: boolean;
}

/** Splits by weekday order rotated to start from `today` (followed by movie, ova, custom, other),
 *  keeps input order within a row, and skips empty rows. */
export function wallRows<T>(
  items: readonly T[],
  factsOf: (item: T) => WallRowFacts,
  today: Weekday = currentWeekday(),
): WallRow<T>[] {
  const buckets = new Map<WallRowKey, T[]>();
  for (const item of items) {
    const key = wallRowOf(factsOf(item));
    const bucket = buckets.get(key);
    if (bucket) bucket.push(item);
    else buckets.set(key, [item]);
  }
  const startIndex = WEEKDAYS.indexOf(today);
  const rotatedWeekdays =
    startIndex > 0 ? [...WEEKDAYS.slice(startIndex), ...WEEKDAYS.slice(0, startIndex)] : WEEKDAYS;
  const order: readonly WallRowKey[] = [...rotatedWeekdays, "movie", "ova", "custom", "other"];
  return order.flatMap((key) => {
    const bucket = buckets.get(key);
    return bucket ? [{ key, items: bucket, ...(key === today ? { isToday: true } : {}) }] : [];
  });
}

export function wallRowLabel(key: WallRowKey): string {
  return isWeekday(key) ? broadcastDayLabel(key) : t(`grid.wallRow.${key}`);
}
