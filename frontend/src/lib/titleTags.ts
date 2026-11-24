import type { TitleTagVocabulary } from "./api/client";
import { anibtTagReasonLabel, formatReasonTagList, parseTagReasonDetail, splitTag, tagKey } from "./anibtTags";
import { t, tryT } from "./i18n";
import { joinList, labelValue } from "./i18n/format";

// Display and vocabulary of title-parsed tags ("kind:canonical value"). The server
// identifies them from seed titles with a pinned reltitle version; the vocabulary comes from
// the server (getTitleTagVocabulary) and the frontend keeps no copy of its own. Judging
// happens only on the server.

export function titleDimensionLabel(dimension: string): string {
  return tryT(`titleTags.dimension.${dimension}`) ?? dimension;
}

/** A value's display name: the canonical value shown as-is, never translated. */
export function titleTagValueLabel(_dimension: string, value: string): string {
  return value;
}

/** The meaning note of a canonical value, used only in the rule editor's hover tips; undefined when unregistered. */
export function titleTagValueMeaning(dimension: string, value: string): string | undefined {
  return tryT(`titleTags.value.${dimension}.${value}`);
}

export function titleTagLabel(tag: string): string {
  const parsed = splitTag(tag);
  return parsed ? titleTagValueLabel(parsed.dimension, parsed.value) : tag;
}

/** The full display name with the kind, for hint text ("subtitle language: CHS"). */
export function titleTagFullLabel(tag: string): string {
  const parsed = splitTag(tag);
  return parsed ? labelValue(titleDimensionLabel(parsed.dimension), titleTagValueLabel(parsed.dimension, parsed.value)) : tag;
}

/** Every canonical value of one kind in the vocabulary; empty when the vocabulary has not been read. */
export function titleVocabularyValues(vocabulary: TitleTagVocabulary | undefined, dimension: string): readonly string[] {
  if (!Array.isArray(vocabulary?.dimensions)) return [];
  return vocabulary.dimensions.find((entry) => entry.dimension === dimension)?.values ?? [];
}

/** A tag's canonical spelling (the vocabulary's spelling, case-insensitive); undefined when not in the vocabulary. */
export function canonicalTitleTag(vocabulary: TitleTagVocabulary | undefined, tag: string): string | undefined {
  const parsed = splitTag(tag);
  if (!parsed) return undefined;
  const lowered = parsed.value.toLowerCase();
  const value = titleVocabularyValues(vocabulary, parsed.dimension).find((candidate) => candidate.toLowerCase() === lowered);
  return value === undefined ? undefined : `${parsed.dimension}:${value}`;
}

/** Counts how often each tag occurs across a set of seeds (counted by comparison key). */
export function countTags(tagLists: ReadonlyArray<readonly string[] | undefined>): Map<string, number> {
  const counts = new Map<string, number>();
  for (const tags of tagLists) {
    const seen = new Set<string>();
    for (const tag of tags ?? []) {
      const key = tagKey(tag);
      if (seen.has(key)) continue;
      seen.add(key);
      counts.set(key, (counts.get(key) ?? 0) + 1);
    }
  }
  return counts;
}

/** The display text of a title-parsed tag filter reason; undefined for a non-title-parsing reason. */
export function titleTagReasonLabel(code: string | null | undefined, detail: string | null | undefined): string | undefined {
  const normalized = code?.trim().toLowerCase();
  if (normalized !== "title_tag_missing" && normalized !== "title_tag_whitelist_unmet" && normalized !== "title_tag_blacklisted") {
    return undefined;
  }
  const parsed = parseTagReasonDetail(detail);
  const parts: string[] = [];
  if (parsed.missing.length > 0) {
    const tags = formatReasonTagList(parsed.missing, titleDimensionLabel, titleTagValueLabel);
    parts.push(t(normalized === "title_tag_missing" ? "titleTags.reasonNoTags" : "titleTags.reasonMissing", { tags }));
  }
  if (parsed.blacklisted.length > 0) {
    parts.push(t("titleTags.reasonBlacklisted", { tags: joinList(parsed.blacklisted.map(titleTagLabel)) }));
  }
  if (parts.length > 0) return parts.join(t("anibtTags.summarySeparator"));
  return t(`reason.${normalized}`);
}

/** The reason text of either tag-filter row (the AniBT row or the title-parsing row); undefined when neither. */
export function tagFilterReasonLabel(code: string | null | undefined, detail: string | null | undefined): string | undefined {
  return anibtTagReasonLabel(code, detail) ?? titleTagReasonLabel(code, detail);
}
