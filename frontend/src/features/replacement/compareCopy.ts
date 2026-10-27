import type { QualityCompareKey } from "../../lib/api/client";
import { t, type CopyKey } from "../../lib/i18n";
import { itemDef, type CompareNamespace, type EntryProblem } from "./qualityProfile";

// Catalog keys of the comparison items, sources and edit problems. Copy is
// read at call time (the UI language can change at runtime).

export const ITEM_COPY: Record<QualityCompareKey, CopyKey> = {
  "subgroup.score": "qualityCompare.item.subgroup_score",
  "release.lang": "qualityCompare.item.release_lang",
  "release.resolution": "qualityCompare.item.release_resolution",
  "release.revision": "qualityCompare.item.release_revision",
  "anibt.resolution": "qualityCompare.item.anibt_resolution",
  "anibt.language": "qualityCompare.item.anibt_language",
  "anibt.subtitle": "qualityCompare.item.anibt_subtitle",
  "anibt.format": "qualityCompare.item.anibt_format",
  "rt.resolution": "qualityCompare.item.rt_resolution",
  "rt.sub_lang": "qualityCompare.item.rt_sub_lang",
  "rt.sub_flag": "qualityCompare.item.rt_sub_flag",
  "rt.source": "qualityCompare.item.rt_source",
  "rt.codec": "qualityCompare.item.rt_codec",
  "rt.uncensored": "qualityCompare.item.rt_uncensored",
};

export const NAMESPACE_COPY: Record<CompareNamespace, CopyKey> = {
  subgroup: "qualityCompare.namespace.subgroup",
  release: "qualityCompare.namespace.release",
  anibt: "qualityCompare.namespace.anibt",
  rt: "qualityCompare.namespace.rt",
};

export const PROBLEM_COPY: Record<EntryProblem, CopyKey> = {
  invalid: "qualityCompare.problem.invalid",
  combination: "qualityCompare.problem.combination",
  tooWide: "qualityCompare.problem.tooWide",
  duplicate: "qualityCompare.problem.duplicate",
  full: "qualityCompare.problem.full",
};

/** Accessible full name of an item: "built-in rule / language". */
export function compareItemName(key: QualityCompareKey): string {
  return t("qualityCompare.itemName", { source: t(NAMESPACE_COPY[itemDef(key).namespace]), name: t(ITEM_COPY[key]) });
}
