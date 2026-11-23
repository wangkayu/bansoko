import type { QualityCompareKey, QualityProfile } from "../../lib/api/client";
import { ANIBT_TAG_PRESETS, isAniBTDimension } from "../../lib/anibtTags";

// Pure model of the replacement comparison profile (profile-v2), shared by the
// global replacement default and the per-anime business snapshot. Mirrors the
// backend owner (internal/qualityprofile): an ordered list of namespaced
// items; ranked items list value sets best first. Edits that the backend would
// reject are refused here with a problem code, never silently rewritten.

export type CompareNamespace = "subgroup" | "anibt" | "rt" | "file";
export type CompareKind = "score" | "revision" | "size" | "single" | "multi";
export type SizeDirection = "larger" | "smaller";
export type CompareItemDef = { key: QualityCompareKey; namespace: CompareNamespace; kind: CompareKind };

/** Registry in catalog order (namespace, then attribute). */
export const COMPARE_ITEMS: readonly CompareItemDef[] = [
  { key: "subgroup.score", namespace: "subgroup", kind: "score" },
  { key: "anibt.resolution", namespace: "anibt", kind: "single" },
  { key: "anibt.language", namespace: "anibt", kind: "multi" },
  { key: "anibt.subtitle", namespace: "anibt", kind: "single" },
  { key: "anibt.format", namespace: "anibt", kind: "single" },
  { key: "rt.resolution", namespace: "rt", kind: "single" },
  { key: "rt.sub_lang", namespace: "rt", kind: "multi" },
  { key: "rt.sub_flag", namespace: "rt", kind: "multi" },
  { key: "rt.source", namespace: "rt", kind: "single" },
  { key: "rt.codec", namespace: "rt", kind: "single" },
  { key: "rt.content_rating", namespace: "rt", kind: "single" },
  { key: "rt.revision", namespace: "rt", kind: "revision" },
  { key: "file.size", namespace: "file", kind: "size" },
];

export const COMPARE_NAMESPACES: readonly CompareNamespace[] = ["subgroup", "anibt", "rt", "file"];

export const MAX_RANKS = 64;
export const MAX_SET_VALUES = 8;
export const MAX_VALUE_BYTES = 128;

export function itemDef(key: QualityCompareKey): CompareItemDef {
  return COMPARE_ITEMS.find((item) => item.key === key) ?? { key, namespace: "rt", kind: "single" };
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
  const ranks = itemDef(key).kind === "size" ? [["larger"]] : [];
  return { ...profile, items: [...profile.items, { key, ranks }] };
}

export function sizeDirection(ranks: readonly string[][]): SizeDirection {
  return ranks[0]?.[0] === "smaller" ? "smaller" : "larger";
}

export function setSizeDirection(profile: QualityProfile, direction: SizeDirection): QualityProfile {
  return withRanks(profile, "file.size", () => [[direction]]);
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
export type InsertPosition = "auto" | "end";
export type AddEntryResult =
  | { profile: QualityProfile; insertedAt: number; problem?: undefined }
  | { profile?: undefined; insertedAt?: undefined; problem: EntryProblem };

const encoder = new TextEncoder();

/**
 * Adds one entry to a ranked item. In "auto" mode (default), a combination goes
 * before the first narrower entry, so wider sets rank first by default (the
 * best-ranked hit wins, so a narrower entry listed earlier would shadow it).
 * In "end" mode, the entry is appended to the tail.
 */
export function addEntry(
  profile: QualityProfile,
  key: QualityCompareKey,
  values: readonly string[],
  position: InsertPosition = "auto",
): AddEntryResult {
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
  const at = position === "auto" ? item.ranks.findIndex((existing) => existing.length < entry.length) : -1;
  const insertedAt = at < 0 ? item.ranks.length : at;
  return {
    insertedAt,
    profile: withRanks(profile, key, (ranks) => (at < 0 ? [...ranks, entry] : [...ranks.slice(0, at), entry, ...ranks.slice(at)])),
  };
}

const COMMON_CANDIDATES_BY_KEY: Partial<Record<QualityCompareKey, readonly string[]>> = {
  "anibt.language": ["CHS", "CHT", "JP", "EN", "KO"],
  "rt.sub_lang": ["JPSC", "JPTC", "SC", "TC", "ZH", "JPN", "ENG", "MULTI", "RAW"],
};
const DEFAULT_COMMON_LIMIT = 8;

/**
 * Splits a ranked item's candidate pool into high-frequency common choices shown
 * immediately and overflow choices tucked behind "+ more" to prevent long walls
 * of chips (e.g. 21 languages).
 */
export function splitCandidates(
  key: QualityCompareKey,
  candidates: readonly string[] | undefined,
): { common: string[]; overflow: string[] } {
  const list = candidates ? [...candidates] : [];
  if (list.length <= DEFAULT_COMMON_LIMIT) return { common: list, overflow: [] };
  const preferred = COMMON_CANDIDATES_BY_KEY[key];
  if (!preferred) {
    return { common: list.slice(0, DEFAULT_COMMON_LIMIT), overflow: list.slice(DEFAULT_COMMON_LIMIT) };
  }
  const preferredSet = new Set(preferred.map((value) => value.toLowerCase()));
  const common: string[] = [];
  const overflow: string[] = [];
  for (const value of list) {
    if (preferredSet.has(value.toLowerCase())) common.push(value);
    else overflow.push(value);
  }
  if (common.length === 0) {
    return { common: list.slice(0, DEFAULT_COMMON_LIMIT), overflow: list.slice(DEFAULT_COMMON_LIMIT) };
  }
  return { common, overflow };
}

/**
 * Candidate values for a ranked item: for anibt.* the same presets the tag filter uses (one
 * source), then the server's already-seen values; duplicates fold case-insensitively and the
 * first spelling wins. Other namespaces offer the server vocabulary as-is.
 */
export function candidateValues(key: string, seen: readonly string[] | undefined): string[] | undefined {
  if (key === "rt.sub_flag") {
    const out = seen ? [...seen] : [];
    if (!out.some((value) => value.trim().toLowerCase() === "unknown")) {
      out.push("unknown");
    }
    return out;
  }
  const dimension = key.startsWith("anibt.") ? key.slice("anibt.".length) : "";
  if (!isAniBTDimension(dimension)) return seen ? [...seen] : undefined;
  const out: string[] = [];
  const folded = new Set<string>();
  for (const value of [...ANIBT_TAG_PRESETS[dimension], ...(seen ?? [])]) {
    const fold = value.trim().toLowerCase();
    if (!fold || folded.has(fold)) continue;
    folded.add(fold);
    out.push(value.trim());
  }
  return out;
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
