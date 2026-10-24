import type { CatalogEntryView } from "../../../lib/providers/viewmodels";
import { progressDone, progressRatio, progressSummary } from "../../../lib/progress";
import { t } from "../../../lib/i18n";

/** The subscribed poster card's thin progress bar: one continuous bar filled by imported episodes /
 *  total (by count only — no episode-number alignment, no gap marking). Not rendered while the total
 *  is unknown, since the text row already says "n episodes imported". A stopped subscription greys
 *  out, and all-imported turns the completion color. */
export function ProgressTrack({ entry }: { entry: CatalogEntryView }) {
  const progress = entry.episodeProgress;
  const ratio = progressRatio(progress);
  if (ratio === undefined) return null;
  const tone = progressDone(progress)
    ? "catalog-progress-fill-done"
    : entry.subscriptionState === "stopped"
      ? "catalog-progress-fill-stopped"
      : "catalog-progress-fill-active";
  return (
    <div className="catalog-progress-track" role="img" aria-label={t("grid.card.progressAria", { label: progressSummary(progress) })}>
      <span className={`catalog-progress-fill ${tone}`} style={{ width: `${ratio * 100}%` }} />
    </div>
  );
}
