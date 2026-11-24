import type { RSSListGroup } from "../../lib/api/client";
import { t } from "../../lib/i18n";

/**
 * The offset dialog's live conversion line for one group: a seed's raw episode and the episode
 * (or episode range) the typed offset and span would give it. The read carries only the episode
 * after the group's current offset, so the raw one is that episode minus the current offset.
 * A span above 1 expands a positive raw episode n into [(n-1)*span+1+offset .. n*span+offset],
 * matching the resolver; episode 0 (a special) stays single. No line when either input is not
 * a valid value or no seed of the group has an episode.
 */
export function offsetConversionLine(group: Pick<RSSListGroup, "items" | "offset"> | undefined, input: string, spanInput = "1"): string | undefined {
  if (!group) return undefined;
  const trimmed = input.trim();
  const offset = trimmed === "" ? Number.NaN : Number(trimmed);
  if (!Number.isInteger(offset) || offset < -1000 || offset > 1000) return undefined;
  const spanTrimmed = spanInput.trim();
  const span = spanTrimmed === "" ? Number.NaN : Number(spanTrimmed);
  if (!Number.isInteger(span) || span < 1 || span > 12) return undefined;
  const shifted = group.items
    .map((item) => item.evaluation.parsed_facts?.episode)
    .find((episode): episode is number => typeof episode === "number" && Number.isFinite(episode));
  if (shifted === undefined) return undefined;
  const currentSpan = group.offset.span > 1 ? group.offset.span : 1;
  const unshifted = shifted - group.offset.value;
  let raw = unshifted;
  if (currentSpan > 1 && unshifted >= 1) {
    if ((unshifted - 1) % currentSpan !== 0) return undefined;
    raw = (unshifted - 1) / currentSpan + 1;
  }
  if (span > 1 && raw >= 1) {
    return t("rss.offsetConversion", { from: raw, to: `${(raw - 1) * span + 1 + offset}-${raw * span + offset}` });
  }
  return t("rss.offsetConversion", { from: raw, to: raw + offset });
}
