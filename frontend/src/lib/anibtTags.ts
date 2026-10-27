import type { TagFilterRow, TagFilterRules } from "./api/client";
import { t, tryT } from "./i18n";
import { joinList, labelValue } from "./i18n/format";

// The frontend vocabulary and display of AniBT's structured tags ("dimension:value").
// Judging happens only on the server, comparing the raw values trimmed of surrounding
// whitespace and case-insensitively; the vocabulary here only feeds the picker's candidates
// and display names, so a missing preset or a new value never changes the filter result.

/** The dimension allowlist, in the same order as the server's AniBTTagDimensions. */
export const ANIBT_TAG_DIMENSIONS = [
  "resolution",
  "subtitle",
  "language",
  "format",
  "codec",
  "bitdepth",
  "audio",
  "source",
  "platform",
  "subformat",
  "tag",
] as const;
export type AniBTTagDimension = (typeof ANIBT_TAG_DIMENSIONS)[number];

/**
 * The candidate values shipped with the version. resolution, subtitle, language and format
 * follow the values AniBT's own magnet filter offers; the rest are every value observed across
 * the 2026 spring/summer/autumn seasons. The server's classification table splits customTag
 * into codec through subformat and this matches that table; customTag values outside the table
 * go to tag (other) and have no presets.
 */
export const ANIBT_TAG_PRESETS: Record<AniBTTagDimension, readonly string[]> = {
  resolution: ["4K", "2160p", "1080p", "720p", "480p", "360p"],
  subtitle: ["EXTERNAL", "INTERNAL", "EMBEDDED", "NONE"],
  language: ["CHS", "CHT", "JP", "EN", "KO", "ES", "PT", "FR", "DE", "IT", "RU", "AR", "HI", "ID", "MS", "TH", "VI", "TL", "TR", "PL", "UK"],
  format: ["MKV", "MP4", "AVI", "WEBM"],
  codec: ["HEVC", "AVC", "AV1"],
  bitdepth: ["10bit", "8bit"],
  audio: ["AAC", "FLAC", "OPUS"],
  source: ["WEB-DL", "WEBRip"],
  platform: ["CR", "Baha", "B-Global", "BILIBILI", "ABEMA", "NF", "CTHP"],
  subformat: ["PGS"],
  tag: [],
};

/** The server's per-tag-value limit (UTF-8 bytes) and the per-list (require / exclude) tag count limit. */
export const MAX_TAG_VALUE_BYTES = 128;
export const MAX_TAGS_PER_LIST = 64;

export const DEFAULT_TAG_FILTER_ROW: TagFilterRow = { require: [], exclude: [] };
export const DEFAULT_TAG_FILTER_RULES: TagFilterRules = {
  anibt: DEFAULT_TAG_FILTER_ROW,
  title: DEFAULT_TAG_FILTER_ROW,
};

export type ParsedTag = { dimension: string; value: string };

/** Splits at the first colon; returns undefined when it is not "dimension:value". */
export function splitTag(tag: string): ParsedTag | undefined {
  const index = tag.indexOf(":");
  if (index <= 0 || index === tag.length - 1) return undefined;
  return { dimension: tag.slice(0, index), value: tag.slice(index + 1) };
}

export function joinTag(dimension: string, value: string): string {
  return `${dimension}:${value}`;
}

/** The comparison key: trimmed of surrounding whitespace and case-insensitive, matching the server's NormalizeTagKey. */
export function tagKey(tag: string): string {
  return tag.trim().toLowerCase();
}

export function isAniBTDimension(value: string): value is AniBTTagDimension {
  return (ANIBT_TAG_DIMENSIONS as readonly string[]).includes(value);
}

export function dimensionLabel(dimension: string): string {
  return tryT(`anibtTags.dimension.${dimension}`) ?? dimension;
}

function presetSpelling(dimension: string, value: string): string | undefined {
  if (!isAniBTDimension(dimension)) return undefined;
  const lowered = value.trim().toLowerCase();
  return ANIBT_TAG_PRESETS[dimension].find((preset) => preset.toLowerCase() === lowered);
}

/** A value's display name: the upstream token shown as-is, never translated (translated long names do not display well). */
export function tagValueLabel(_dimension: string, value: string): string {
  return value;
}

/** The meaning note of a preset value, used only in the rule editor's hover tips; undefined when unregistered. */
export function tagValueMeaning(dimension: string, value: string): string | undefined {
  const preset = presetSpelling(dimension, value);
  return preset ? tryT(`anibtTags.value.${dimension}.${preset}`) : undefined;
}

/** A single tag's display name (the value only); returns as-is when it is not "dimension:value". */
export function tagLabel(tag: string): string {
  const parsed = splitTag(tag);
  return parsed ? tagValueLabel(parsed.dimension, parsed.value) : tag;
}

/** The full display name with the dimension, for hint text ("language: CHS"). */
export function tagFullLabel(tag: string): string {
  const parsed = splitTag(tag);
  return parsed ? labelValue(dimensionLabel(parsed.dimension), tagValueLabel(parsed.dimension, parsed.value)) : tag;
}

/** A value the presets do not have (from observed seeds or manual input) is marked "new" in the picker. */
export function isPresetTag(tag: string): boolean {
  const parsed = splitTag(tag);
  return parsed !== undefined && presetSpelling(parsed.dimension, parsed.value) !== undefined;
}

export function hasTag(tags: readonly string[], tag: string): boolean {
  const key = tagKey(tag);
  return tags.some((candidate) => tagKey(candidate) === key);
}

export type TagRole = "require" | "exclude";

/** All tags of a row (require first, exclude after), each with its role. */
export function rowEntries(row: TagFilterRow): { tag: string; role: TagRole }[] {
  return [
    ...row.require.map((tag) => ({ tag, role: "require" as const })),
    ...row.exclude.map((tag) => ({ tag, role: "exclude" as const })),
  ];
}

export function rowTags(row: TagFilterRow): string[] {
  return rowEntries(row).map((entry) => entry.tag);
}

/** Whether this list of this row is full (the server allows at most MAX_TAGS_PER_LIST per list). */
export function rowRoleFull(row: TagFilterRow, role: TagRole): boolean {
  return row[role].length >= MAX_TAGS_PER_LIST;
}

/** Adds a tag (require by default); returns the row unchanged when the tag is already in either list (case-insensitively) or the list is full. */
export function addRowTag(row: TagFilterRow, tag: string, role: TagRole = "require"): TagFilterRow {
  if (hasTag(rowTags(row), tag) || rowRoleFull(row, role)) return row;
  return { ...row, [role]: [...row[role], tag] };
}

export function removeRowTag(row: TagFilterRow, tag: string): TagFilterRow {
  const key = tagKey(tag);
  const keep = (tags: string[]) => tags.filter((candidate) => tagKey(candidate) !== key);
  return { require: keep(row.require), exclude: keep(row.exclude) };
}

/** Moves an existing tag to require or exclude, appended at the new list's end; unchanged when it already has that role or the new list is full. */
export function setRowTagRole(row: TagFilterRow, tag: string, role: TagRole): TagFilterRow {
  if (hasTag(row[role], tag) || !hasTag(rowTags(row), tag) || rowRoleFull(row, role)) return row;
  const kept = removeRowTag(row, tag);
  return { ...kept, [role]: [...kept[role], tag] };
}

/** Validation for a manually added tag (added to the role list); undefined when it passes. */
export function validateTagValue(row: TagFilterRow, role: TagRole, dimension: string, rawValue: string): string | undefined {
  const value = rawValue.trim();
  if (value === "") return t("anibtTags.valueEmpty");
  if (/[\r\n\t]/.test(value)) return t("anibtTags.valueInvalid");
  if (new TextEncoder().encode(value).length > MAX_TAG_VALUE_BYTES) return t("anibtTags.valueTooLong");
  if (hasTag(rowTags(row), joinTag(dimension, value))) return t("anibtTags.duplicate");
  if (rowRoleFull(row, role)) return t("anibtTags.tooMany", { n: MAX_TAGS_PER_LIST });
  return undefined;
}

export function tagRoleLabel(role: TagRole): string {
  return t(`anibtTags.role.${role}`);
}

/** A one-sentence summary of a rule row: "no tag filtering" or "require: CHS, 1080p; exclude: HEVC". */
export function tagRowSummary(row: TagFilterRow, label: (tag: string) => string = tagLabel): string {
  const parts = (["require", "exclude"] as const)
    .filter((role) => row[role].length > 0)
    .map((role) => labelValue(tagRoleLabel(role), joinList(row[role].map(label))));
  if (parts.length === 0) return t("anibtTags.none");
  return parts.join(t("anibtTags.summarySeparator"));
}

/**
 * The display text of an AniBT tag filter reason. The server's reason_detail looks like
 * "missing: language:CHS, resolution:1080p" or "blacklisted: codec:HEVC"; when it parses,
 * the concrete tags are written out, otherwise the registered copy of the reason code is
 * used. Returns undefined for a non-AniBT-tag reason.
 */
export function anibtTagReasonLabel(code: string | null | undefined, detail: string | null | undefined): string | undefined {
  const normalized = code?.trim().toLowerCase();
  if (normalized !== "anibt_tag_missing" && normalized !== "anibt_tag_whitelist_unmet" && normalized !== "anibt_tag_blacklisted") {
    return undefined;
  }
  const match = detail ? /^(missing|blacklisted):\s*(.+)$/.exec(detail.trim()) : null;
  if (match && normalized !== "anibt_tag_missing") {
    const tags = joinList(match[2]!.split(/,\s*/).filter(Boolean).map(tagLabel));
    return t(match[1] === "missing" ? "anibtTags.reasonMissing" : "anibtTags.reasonBlacklisted", { tags });
  }
  return t(`reason.${normalized}`);
}

/** Collects values beyond the presets from frozen seed tags, as the picker's "new value" candidates. */
export function observedExtraTags(tagLists: ReadonlyArray<readonly string[] | undefined>): string[] {
  const seen = new Set<string>();
  const result: string[] = [];
  for (const tags of tagLists) {
    for (const tag of tags ?? []) {
      const parsed = splitTag(tag.trim());
      if (!parsed || !isAniBTDimension(parsed.dimension) || isPresetTag(tag)) continue;
      const key = tagKey(tag);
      if (seen.has(key)) continue;
      seen.add(key);
      result.push(tag.trim());
    }
  }
  return result;
}
