import type { RSSListGroup } from "../../lib/api/client";
import { t } from "../../lib/i18n";

/**
 * The offset dialog's live conversion line for one group: a seed's raw episode and the episode
 * the typed offset would give it. The read carries only the episode after the group's current
 * offset, so the raw one is that episode minus the current offset. No line when the input is not
 * a valid offset or no seed of the group has an episode.
 */
export function offsetConversionLine(group: RSSListGroup | undefined, input: string): string | undefined {
  if (!group) return undefined;
  const trimmed = input.trim();
  const offset = trimmed === "" ? Number.NaN : Number(trimmed);
  if (!Number.isInteger(offset) || offset < -1000 || offset > 1000) return undefined;
  const shifted = group.items
    .map((item) => item.evaluation.parsed_facts?.episode)
    .find((episode): episode is number => typeof episode === "number" && Number.isFinite(episode));
  if (shifted === undefined) return undefined;
  const raw = shifted - group.offset.value;
  return t("rss.offsetConversion", { from: raw, to: raw + offset });
}
