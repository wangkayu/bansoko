import type { ReactNode } from "react";
import type { CatalogEntryView } from "../../../lib/providers/viewmodels";
import { broadcastDayLabel, type BroadcastDay } from "../../../lib/i18n/broadcastDay";

// Only an actual broadcast day gets a corner badge; undetermined / unscheduled shows none.
const WEEKDAYS: ReadonlySet<BroadcastDay> = new Set(["mon", "tue", "wed", "thu", "fri", "sat", "sun"]);

/** The browse card's top-left badges: broadcast day + score + the caller's state pill. Pure display, never intercepting clicks. */
export function CornerBadges({ entry, children }: { entry: CatalogEntryView; children?: ReactNode }) {
  const day = entry.broadcastDay && WEEKDAYS.has(entry.broadcastDay) ? entry.broadcastDay : undefined;
  if (!day && entry.score === undefined && !children) return null;
  return (
    <div className="catalog-corner-badges">
      {day && (
        <span className="catalog-corner-badge text-badge">
          {broadcastDayLabel(day)}
        </span>
      )}
      {entry.score !== undefined && (
        <span className="catalog-corner-badge-score text-badge">
          {entry.score.toFixed(1)}
        </span>
      )}
      {children}
    </div>
  );
}
