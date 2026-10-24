// Episode progress helpers. The backend reports the imported (offset-applied,
// SP-excluded) episode numbers plus an optional announced total. Progress is
// count-based: the number of imported episodes is
// compared with the total; episode numbers are never mapped to slots, so the
// UI shows no gaps or missing episodes.

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
export function progressSummary(progress: EpisodeProgressLike | undefined, short = false): string {
  const count = importedCount(progress);
  if (count === 0) return t("grid.progress.waiting");
  const total = progressTotal(progress);
  if (total === undefined) return t("grid.progress.summaryNoTotal", { stored: count });
  return t(short ? "grid.progress.summaryShort" : "grid.progress.summary", { stored: count, total });
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
