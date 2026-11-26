// Progress counts distinct imported items against an optional total. TV episode
// numbers are never mapped to slots. The backend represents a movie as one item.

import { t } from "./i18n";

export interface EpisodeProgressLike {
  episodes?: number[];
  total?: number;
  done?: boolean;
}

/** Number of distinct imported episodes. */
export function importedCount(progress: EpisodeProgressLike | undefined): number {
  return new Set(progress?.episodes ?? []).size;
}

/** Announced total, or undefined when unknown (null/0). */
export function progressTotal(progress: EpisodeProgressLike | undefined): number | undefined {
  const total = progress?.total;
  return total !== undefined && total > 0 ? total : undefined;
}

/** One-line summary under the poster-card title:
 *  - none imported -> 等待首集
 *  - with total -> 已入库 n/total (short: n/total)
 *  - without total -> 已入库 n 集 */
export function progressSummary(progress: EpisodeProgressLike | undefined, short = false, mediaKind?: string | null): string {
  return progressText(importedCount(progress), progressTotal(progress), short, mediaKind);
}

function progressText(count: number, total: number | undefined, short: boolean, mediaKind?: string | null): string {
  if (count === 0) return t(mediaKind === "movie" ? "grid.progress.waitingForImport" : "grid.progress.waiting");
  if (total === undefined) return t("grid.progress.summaryNoTotal", { stored: count });
  return t(short ? "grid.progress.summaryShort" : "grid.progress.summary", { stored: count, total });
}

/** One count for all of a card's progress labels and bar; text stays locale-current. */
export function progressPresentation(progress: EpisodeProgressLike | undefined, mediaKind?: string | null) {
  const count = importedCount(progress);
  const total = progressTotal(progress);
  return {
    count,
    done: progress?.done ?? (total !== undefined && count >= total),
    ratio: total === undefined ? undefined : Math.min(1, count / total),
    summary: progressText(count, total, false, mediaKind),
    shortSummary: progressText(count, total, true, mediaKind),
  };
}

/** All announced episodes are imported (count >= total). Prefers the
 *  server-derived `done`, which uses the same count rule. */
export function progressDone(progress: EpisodeProgressLike | undefined): boolean {
  if (progress?.done !== undefined) return progress.done;
  const total = progressTotal(progress);
  return total !== undefined && importedCount(progress) >= total;
}

/** Fill ratio 0..1 of the progress bar; undefined when the total is unknown. */
export function progressRatio(progress: EpisodeProgressLike | undefined): number | undefined {
  const total = progressTotal(progress);
  if (total === undefined) return undefined;
  return Math.min(1, importedCount(progress) / total);
}
