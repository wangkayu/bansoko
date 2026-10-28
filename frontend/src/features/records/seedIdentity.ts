import type { AcquisitionSeedIdentity } from "../../lib/api/client";
import { formatBytes } from "../../lib/labels";
import { titleTagLabel } from "../../lib/titleTags";

// Key title tags on a list row: the kinds that tell two seeds of one episode apart. Order is display order.
const KEY_TAG_DIMENSIONS = ["resolution", "sub_lang"] as const;

/** The parts of a list row's identity line, in order: group, resolution, subtitle language, version, size. Absent values are skipped. */
export function seedIdentityParts(seed: AcquisitionSeedIdentity | undefined): string[] {
  if (!seed) return [];
  const parts: string[] = [];
  if (seed.group_name) parts.push(seed.group_name);
  const tags = seed.title_tags ?? [];
  for (const dimension of KEY_TAG_DIMENSIONS) {
    for (const tag of tags) {
      if (tag.startsWith(`${dimension}:`)) parts.push(titleTagLabel(tag));
    }
  }
  if (seed.release_version) parts.push(seed.release_version);
  if (typeof seed.size_bytes === "number") parts.push(formatBytes(seed.size_bytes));
  return parts;
}
