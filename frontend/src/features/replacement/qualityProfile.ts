import type { QualityCompareKey, QualityProfile } from "../../lib/api/client";

// Pure model of the replacement comparison profile (profile-v1), shared by the
// global replacement default and the per-anime business snapshot. Mirrors the
// backend owner (internal/qualityprofile): an ordered list of namespaced
// items; ranked items list value sets best first. Edits that the backend would
// reject are refused here with a problem code, never silently rewritten.

export type CompareNamespace = "subgroup" | "release" | "anibt" | "rt";
export type CompareKind = "score" | "revision" | "single" | "multi" | "flag";
export type CompareItemDef = { key: QualityCompareKey; namespace: CompareNamespace; kind: CompareKind };

/** Registry in catalog order (namespace, then attribute). */
export const COMPARE_ITEMS: readonly CompareItemDef[] = [
  { key: "subgroup.score", namespace: "subgroup", kind: "score" },
  { key: "release.lang", namespace: "release", kind: "single" },
  { key: "release.resolution", namespace: "release", kind: "single" },
  { key: "release.revision", namespace: "release", kind: "revision" },
  { key: "anibt.resolution", namespace: "anibt", kind: "single" },
  { key: "anibt.language", namespace: "anibt", kind: "multi" },
  { key: "anibt.subtitle", namespace: "anibt", kind: "single" },
  { key: "anibt.format", namespace: "anibt", kind: "single" },
  { key: "rt.resolution", namespace: "rt", kind: "single" },
  { key: "rt.sub_lang", namespace: "rt", kind: "multi" },
  { key: "rt.sub_flag", namespace: "rt", kind: "multi" },
  { key: "rt.source", namespace: "rt", kind: "single" },
  { key: "rt.codec", namespace: "rt", kind: "single" },
  { key: "rt.uncensored", namespace: "rt", kind: "flag" },
];

export const COMPARE_NAMESPACES: readonly CompareNamespace[] = ["subgroup", "release", "anibt", "rt"];

export const FLAG_TRUE = "true";
export const FLAG_FALSE = "false";

/** Direction of a flag item: true = the flagged side ranks first. */
export function flagFirst(ranks: readonly string[][]): boolean {
  return ranks[0]?.[0] !== FLAG_FALSE;
}

export function setFlagDirection(profile: QualityProfile, key: QualityCompareKey, first: boolean): QualityProfile {
  return withRanks(profile, key, () => [[first ? FLAG_TRUE : FLAG_FALSE]]);
}

export const MAX_RANKS = 64;
export const MAX_SET_VALUES = 8;
export const MAX_VALUE_BYTES = 128;

export function itemDef(key: QualityCompareKey): CompareItemDef {
  return COMPARE_ITEMS.find((item) => item.key === key) ?? { key, namespace: "release", kind: "single" };
}

export function isRanked(key: QualityCompareKey): boolean {
  const kind = itemDef(key).kind;
  return kind === "single" || kind === "multi";
}

/** Order- and case-insensitive identity of a value set ("CHT+CHS" = "chs+cht"). */
export function setKey(values: readonly string[]): string {
  return [...new Set(values.map((value) => value.trim().toLowerCase()).filter(Boolean))].sort().join("+");
}

/** Items not yet in the profile, in registry order. */
export function unusedItems(profile: QualityProfile): CompareItemDef[] {
  const used = new Set(profile.items.map((item) => item.key));
  return COMPARE_ITEMS.filter((item) => !used.has(item.key));
}

export function addItem(profile: QualityProfile, key: QualityCompareKey): QualityProfile {
  if (profile.items.some((item) => item.key === key)) return profile;
  // A flag item always carries its direction: uncensored first by default.
  const ranks: string[][] = itemDef(key).kind === "flag" ? [[FLAG_TRUE]] : [];
  return { ...profile, items: [...profile.items, { key, ranks }] };
}

export function removeItem(profile: QualityProfile, index: number): QualityProfile {
  return { ...profile, items: profile.items.filter((_, position) => position !== index) };
}

function moved<T>(list: readonly T[], from: number, to: number): T[] {
  if (from === to || from < 0 || to < 0 || from >= list.length || to >= list.length) return [...list];
  const next = [...list];
  const [entry] = next.splice(from, 1);
  next.splice(to, 0, entry);
  return next;
}

export function moveItem(profile: QualityProfile, from: number, to: number): QualityProfile {
  return { ...profile, items: moved(profile.items, from, to) };
}

function withRanks(profile: QualityProfile, key: QualityCompareKey, change: (ranks: string[][]) => string[][]): QualityProfile {
  return { ...profile, items: profile.items.map((item) => (item.key === key ? { ...item, ranks: change(item.ranks) } : item)) };
}

export function moveEntry(profile: QualityProfile, key: QualityCompareKey, from: number, to: number): QualityProfile {
  return withRanks(profile, key, (ranks) => moved(ranks, from, to));
}

export function removeEntry(profile: QualityProfile, key: QualityCompareKey, index: number): QualityProfile {
  return withRanks(profile, key, (ranks) => ranks.filter((_, position) => position !== index));
}

/** Splits typed text into entry values: multi items read "A+B" as a combination. */
export function parseEntry(text: string, key: QualityCompareKey): string[] {
  const trimmed = text.trim();
  if (itemDef(key).kind !== "multi") return trimmed ? [trimmed] : [];
  return trimmed.split("+").map((value) => value.trim());
}

export type EntryProblem = "invalid" | "combination" | "tooWide" | "duplicate" | "full";
export type AddEntryResult = { profile: QualityProfile; problem?: undefined } | { profile?: undefined; problem: EntryProblem };

const encoder = new TextEncoder();

/**
 * Adds one entry to a ranked item. A combination goes before the first
 * narrower entry, so wider sets rank first by default (the best-ranked hit
 * wins, so a narrower entry listed earlier would shadow it).
 */
export function addEntry(profile: QualityProfile, key: QualityCompareKey, values: readonly string[]): AddEntryResult {
  const item = profile.items.find((entry) => entry.key === key);
  if (!item || !isRanked(key)) return { problem: "invalid" };
  const entry = values.map((value) => value.trim());
  if (entry.length === 0 || entry.some((value) => value === "" || value.includes("+") || encoder.encode(value).length > MAX_VALUE_BYTES)) {
    return { problem: itemDef(key).kind === "single" && values.some((value) => value.includes("+")) ? "combination" : "invalid" };
  }
  if (itemDef(key).kind === "single" && entry.length !== 1) return { problem: "combination" };
  if (setKey(entry).split("+").length !== entry.length) return { problem: "invalid" };
  if (entry.length > MAX_SET_VALUES) return { problem: "tooWide" };
  const identity = setKey(entry);
  if (item.ranks.some((existing) => setKey(existing) === identity)) return { problem: "duplicate" };
  if (item.ranks.length >= MAX_RANKS) return { problem: "full" };
  // A combination is written in case-insensitive alphabetical order: the
  // server validates without rewriting, and the client digests what it sends.
  if (entry.length > 1) entry.sort((a, b) => a.toLowerCase().localeCompare(b.toLowerCase()));
  const at = item.ranks.findIndex((existing) => existing.length < entry.length);
  return {
    profile: withRanks(profile, key, (ranks) => (at < 0 ? [...ranks, entry] : [...ranks.slice(0, at), entry, ...ranks.slice(at)])),
  };
}

/** Values offered for a ranked item: suggestions not already listed alone. */
export function suggestions(ranks: readonly string[][], vocabulary: readonly string[] | undefined): string[] {
  const listed = new Set(ranks.filter((entry) => entry.length === 1).map((entry) => setKey(entry)));
  return (vocabulary ?? []).filter((value) => !listed.has(setKey([value])));
}

/** Values a combination can be built from: the suggestions plus every listed value. */
export function comboValues(ranks: readonly string[][], vocabulary: readonly string[] | undefined): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const value of [...(vocabulary ?? []), ...ranks.flat()]) {
    const folded = value.trim().toLowerCase();
    if (!folded || seen.has(folded)) continue;
    seen.add(folded);
    out.push(value.trim());
  }
  return out;
}

export function entryLabel(entry: readonly string[]): string {
  return entry.join("+");
}

export function profilesEqual(a: QualityProfile, b: QualityProfile): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}
