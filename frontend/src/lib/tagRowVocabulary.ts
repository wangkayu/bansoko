import type { TitleTagVocabulary } from "./api/client";
import { ANIBT_TAG_DIMENSIONS, ANIBT_TAG_PRESETS, dimensionLabel, isAniBTDimension, splitTag, tagFullLabel, tagValueLabel, tagValueMeaning } from "./anibtTags";
import { t } from "./i18n";
import { withNote } from "./i18n/format";
import { canonicalTitleTag, titleDimensionLabel, titleTagFullLabel, titleTagValueLabel, titleTagValueMeaning, titleVocabularyValues } from "./titleTags";

// The per-row vocabulary description of the tag rule editor
// (features/anime-forms/TagRowEditor):
// - AniBT row: candidates = preset values + selected / observed values the presets lack
//   (marked "new"), with manual input allowed;
// - title-parsing row: candidates = selected + values observed for this anime; adding picks
//   from the reltitle vocabulary, values outside it cannot be entered.

export type TagRowVocabulary = {
  dimensions: readonly string[];
  /** Candidate values always shown for the kind. */
  presets: (dimension: string) => readonly string[];
  dimensionLabel: (dimension: string) => string;
  valueLabel: (dimension: string, value: string) => string;
  fullLabel: (tag: string) => string;
  /** Hover tip: kind + raw value, plus the meaning when registered (the value itself is never translated). */
  hoverLabel: (tag: string) => string;
  /** Whether a value outside the presets is marked "new". */
  markFresh: boolean;
  /** The values selectable when adding; undefined means manual input. */
  addValues?: (dimension: string) => readonly string[];
  /** Normalizes an observed tag; undefined means it is not offered as a candidate. */
  normalizeObserved: (tag: string) => string | undefined;
  defaultAddDimension: string;
  groupAria: (dimensionLabel: string) => string;
};

function explained(full: (tag: string) => string, meaning: (dimension: string, value: string) => string | undefined) {
  return (tag: string): string => {
    const parsed = splitTag(tag);
    const note = parsed && meaning(parsed.dimension, parsed.value);
    return note && note !== parsed.value ? withNote(full(tag), note) : full(tag);
  };
}

export const ANIBT_ROW_VOCABULARY: TagRowVocabulary = {
  dimensions: ANIBT_TAG_DIMENSIONS,
  presets: (dimension) => (isAniBTDimension(dimension) ? ANIBT_TAG_PRESETS[dimension] : []),
  dimensionLabel,
  valueLabel: tagValueLabel,
  fullLabel: tagFullLabel,
  hoverLabel: explained(tagFullLabel, tagValueMeaning),
  markFresh: true,
  normalizeObserved: (tag) => {
    const parsed = splitTag(tag.trim());
    return parsed && isAniBTDimension(parsed.dimension) ? tag.trim() : undefined;
  },
  defaultAddDimension: "tag",
  groupAria: (dimension) => t("anibtTags.groupAria", { dimension }),
};

/** The title-parsing row's vocabulary: both the kinds and the selectable values come from the server's reltitle vocabulary. */
export function titleRowVocabulary(vocabulary: TitleTagVocabulary | undefined): TagRowVocabulary {
  const dimensions = Array.isArray(vocabulary?.dimensions) ? vocabulary.dimensions : [];
  return {
    dimensions: dimensions.map((entry) => entry.dimension),
    presets: () => [],
    dimensionLabel: titleDimensionLabel,
    valueLabel: titleTagValueLabel,
    fullLabel: titleTagFullLabel,
    hoverLabel: explained(titleTagFullLabel, titleTagValueMeaning),
    markFresh: false,
    addValues: (dimension) => titleVocabularyValues(vocabulary, dimension),
    normalizeObserved: (tag) => canonicalTitleTag(vocabulary, tag),
    defaultAddDimension: dimensions[0]?.dimension ?? "resolution",
    groupAria: (dimension) => t("titleTags.groupAria", { dimension }),
  };
}
