import type { CatalogEntryView } from "../../../lib/providers/viewmodels";
import { progressPresentation } from "../../../lib/progress";
import { t } from "../../../lib/i18n";

/** The subscribed poster card's thin progress bar: one continuous bar filled by imported episodes /
 *  total (by count only — no episode-number alignment, no gap marking). Not rendered while the total
 *  is unknown, since the text row already says "n episodes imported". A stopped subscription greys
 *  out, and all-imported turns the completion color. */
export function ProgressTrack({ entry, progress = progressPresentation(entry.episodeProgress) }: {
  entry: CatalogEntryView;
  progress?: ReturnType<typeof progressPresentation>;
}) {
  const { ratio } = progress;
  if (ratio === undefined) return null;
  const tone = progress.done
    ? "catalog-progress-fill-done"
    : entry.subscriptionState === "stopped"
      ? "catalog-progress-fill-stopped"
      : "catalog-progress-fill-active";
  return (
    <div className="catalog-progress-track" role="img" aria-label={t("grid.card.progressAria", { label: progress.summary })}>
      <span className={`catalog-progress-fill ${tone}`} style={{ width: `${ratio * 100}%` }} />
    </div>
  );
}
