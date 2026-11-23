import type { QualityCompareKey } from "../../lib/api/client";
import { tagValueMeaning } from "../../lib/anibtTags";
import { titleTagValueMeaning } from "../../lib/titleTags";
import { t, type CopyKey } from "../../lib/i18n";
import { itemDef, type CompareNamespace, type EntryProblem } from "./qualityProfile";

// Catalog keys of the comparison items, sources and edit problems. Copy is
// read at call time (the UI language can change at runtime).

export const ITEM_COPY: Record<QualityCompareKey, CopyKey> = {
  "subgroup.score": "qualityCompare.item.subgroup_score",
  "anibt.resolution": "qualityCompare.item.anibt_resolution",
  "anibt.language": "qualityCompare.item.anibt_language",
  "anibt.subtitle": "qualityCompare.item.anibt_subtitle",
  "anibt.format": "qualityCompare.item.anibt_format",
  "rt.resolution": "qualityCompare.item.rt_resolution",
  "rt.sub_lang": "qualityCompare.item.rt_sub_lang",
  "rt.sub_flag": "qualityCompare.item.rt_sub_flag",
  "rt.source": "qualityCompare.item.rt_source",
  "rt.codec": "qualityCompare.item.rt_codec",
  "rt.content_rating": "qualityCompare.item.rt_content_rating",
  "rt.revision": "qualityCompare.item.rt_revision",
  "file.size": "qualityCompare.item.file_size",
};

export const ITEM_DESC_COPY: Record<QualityCompareKey, CopyKey> = {
  "subgroup.score": "qualityCompare.itemDesc.subgroup_score",
  "anibt.resolution": "qualityCompare.itemDesc.anibt_resolution",
  "anibt.language": "qualityCompare.itemDesc.anibt_language",
  "anibt.subtitle": "qualityCompare.itemDesc.anibt_subtitle",
  "anibt.format": "qualityCompare.itemDesc.anibt_format",
  "rt.resolution": "qualityCompare.itemDesc.rt_resolution",
  "rt.sub_lang": "qualityCompare.itemDesc.rt_sub_lang",
  "rt.sub_flag": "qualityCompare.itemDesc.rt_sub_flag",
  "rt.source": "qualityCompare.itemDesc.rt_source",
  "rt.codec": "qualityCompare.itemDesc.rt_codec",
  "rt.content_rating": "qualityCompare.itemDesc.rt_content_rating",
  "rt.revision": "qualityCompare.itemDesc.rt_revision",
  "file.size": "qualityCompare.itemDesc.file_size",
};

export const NAMESPACE_COPY: Record<CompareNamespace, CopyKey> = {
  subgroup: "qualityCompare.namespace.subgroup",
  anibt: "qualityCompare.namespace.anibt",
  rt: "qualityCompare.namespace.rt",
  file: "qualityCompare.namespace.file",
};

export const PROBLEM_COPY: Record<EntryProblem, CopyKey> = {
  invalid: "qualityCompare.problem.invalid",
  combination: "qualityCompare.problem.combination",
  tooWide: "qualityCompare.problem.tooWide",
  duplicate: "qualityCompare.problem.duplicate",
  full: "qualityCompare.problem.full",
};

const RT_VALUE_DIMENSIONS: Partial<Record<QualityCompareKey, string>> = {
  "rt.resolution": "resolution",
  "rt.sub_lang": "sub_lang",
  "rt.sub_flag": "sub_flag",
  "rt.source": "source_type",
  "rt.codec": "video_codec",
  "rt.content_rating": "content_rating",
};

/** Optional localized meaning note for a ranked value (reusing AniBT & title-tag dictionaries). */
export function compareValueMeaning(key: QualityCompareKey, value: string): string | undefined {
  if (key.startsWith("anibt.")) {
    return tagValueMeaning(key.slice("anibt.".length), value);
  }
  const rtDimension = RT_VALUE_DIMENSIONS[key];
  if (rtDimension) {
    return titleTagValueMeaning(rtDimension, value);
  }
  return undefined;
}

/** Accessible full name of an item: "built-in rule / language". */
export function compareItemName(key: QualityCompareKey): string {
  return t("qualityCompare.itemName", { source: t(NAMESPACE_COPY[itemDef(key).namespace]), name: t(ITEM_COPY[key]) });
}
