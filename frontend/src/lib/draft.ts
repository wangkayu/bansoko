import type {
  AnimeDraft,
  AnimeMetadata,
  AnimeMetadataDraft,
  AnimeRules,
  RSSSourceDraft,
  RulesSections,
  SubgroupsOwnerResult,
  SubgroupsSection,
} from "./api/client";
import { t } from "./i18n";

// The in-memory draft model before subscribing, and dirty detection for owner edits after
// subscribing. The draft lives only in component memory: no localStorage and no server draft.

export function createEmptySourceDraft(): RSSSourceDraft {
  return {
    source_kind: "custom_rss",
    locator: "",
    locator_display: null,
    group_name: "",
    title: "",
    episode_offset: 0,
  };
}

export function createEmptyDraft(): AnimeDraft {
  return {
    origin: "custom",
    origin_entry_ref: null,
    title: "",
    title_override: null,
    instance_label: null,
    metadata: { bgm: null, tmdb: null, total_episodes: null },
    subgroups: { max_rss: 3, entries: [] },
    // The rules sections become a full copy when the default settings load (loadBusinessPreset); before that they are empty.
    rules: { sections: {} },
    rss_sources: [createEmptySourceDraft()],
  };
}

// --- Local validation (the server still validates authoritatively; this only decides whether a preview is worth sending and gives direct feedback) ---

export type DraftProblem = { field: string; message: string };

export function validateDraft(draft: AnimeDraft): DraftProblem[] {
  const problems: DraftProblem[] = [];
  if (!draft.title.trim()) {
    problems.push({ field: "title", message: t("config.validation.titleRequired") });
  }
  const tmdb = draft.metadata.tmdb;
  if (tmdb) {
    if (!tmdb.work_id.trim()) {
      problems.push({ field: "metadata.tmdb.work_id", message: t("config.validation.tmdbRequired") });
    }
    if (tmdb.season < 0 || !Number.isInteger(tmdb.season)) {
      problems.push({ field: "metadata.tmdb.season", message: t("config.validation.tmdbSeason") });
    }
  }
  if (draft.origin === "custom" && draft.rss_sources.length === 0) {
    problems.push({ field: "rss_sources", message: t("config.validation.rssRequired") });
  }
  if (draft.origin === "custom" && !draft.instance_label?.trim()) {
    problems.push({ field: "instance_label", message: t("config.validation.labelRequired") });
  }
  draft.rss_sources.forEach((source, index) => {
    const locator = source.locator.trim();
    if (!locator) {
      problems.push({ field: `rss_sources[${index}].locator`, message: t("config.validation.locatorRequired") });
      return;
    }
    if (source.source_kind === "custom_rss" && !/^https?:\/\//i.test(locator)) {
      problems.push({ field: `rss_sources[${index}].locator`, message: t("config.validation.locatorInvalid") });
    }
    if (!source.group_name.trim()) {
      problems.push({ field: `rss_sources[${index}].group_name`, message: t("config.validation.groupRequired") });
    }
    if (!source.title.trim()) {
      problems.push({ field: `rss_sources[${index}].title`, message: t("config.validation.sourceTitleRequired") });
    }
  });
  return problems;
}

/** A preview is sent only on the minimal usable draft, avoiding a 422 on every keystroke. */
export function draftPreviewable(draft: AnimeDraft): boolean {
  if (draft.title.trim() === "") return false;
  // custom requires instance_label + at least one valid RSS source.
  if (draft.origin === "custom") {
    if (!draft.instance_label?.trim()) return false;
    if (draft.rss_sources.length === 0) return false;
  }
  return draft.rss_sources.every(
    (source) =>
      source.locator.trim() !== "" &&
      source.group_name.trim() !== "" &&
      source.title.trim() !== "" &&
      (source.source_kind !== "custom_rss" || /^https?:\/\//i.test(source.locator.trim())),
  );
}

// --- Post-subscription owner → local edit values ---------------------------------------------

export function metadataDraftFromOwner(metadata: AnimeMetadata): AnimeMetadataDraft {
  return {
    bgm: metadata.bgm ? { work_id: metadata.bgm.work_id } : null,
    tmdb: metadata.tmdb
      ? { work_id: metadata.tmdb.work_id, media_kind: metadata.tmdb.media_kind, season: metadata.tmdb.season }
      : null,
    total_episodes: metadata.total_episodes ?? null,
    year: metadata.year ?? null,
    season: metadata.season ?? null,
  };
}

export type SubgroupsEdit = {
  subgroups: SubgroupsSection;
};

export function subgroupsEditFromWorkspace(subgroups: SubgroupsOwnerResult): SubgroupsEdit {
  return {
    subgroups: structuredClone(subgroups.subgroups),
  };
}

export function sectionsFromRules(rules: AnimeRules): RulesSections {
  return structuredClone(rules.sections);
}

// --- Dirty detection (a stable, key-order-independent comparison) ------------------------------

function canonicalize(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map(canonicalize);
  }
  if (value && typeof value === "object") {
    const record = value as Record<string, unknown>;
    return Object.fromEntries(
      Object.keys(record)
        .filter((key) => record[key] !== undefined)
        .sort()
        .map((key) => [key, canonicalize(record[key])]),
    );
  }
  return value;
}

export function canonicalJSON(value: unknown): string {
  return JSON.stringify(canonicalize(value));
}

export function valuesEqual(a: unknown, b: unknown): boolean {
  return canonicalJSON(a) === canonicalJSON(b);
}

/** Dirty when any owner edit value differs from the saved value. */
export function metadataDirty(saved: AnimeMetadataDraft, edit: AnimeMetadataDraft): boolean {
  return !valuesEqual(saved, edit);
}

export function subgroupsDirty(saved: SubgroupsEdit, edit: SubgroupsEdit): boolean {
  return !valuesEqual(saved, edit);
}

export function rulesDirty(saved: RulesSections, edit: RulesSections): boolean {
  return !valuesEqual(saved, edit);
}

// --- Unsaved-change counts (the footer's "n unsaved changes") -------------------------------
//
// Counted in units the user can tell apart: objects by top-level field, arrays by entry.
// Display only — dirty is still decided by valuesEqual (the count is 0 if and only if the two
// sides are equal).

function keyedEntries(list: unknown[]): Map<string, unknown> | undefined {
  const out = new Map<string, unknown>();
  for (const item of list) {
    if (!item || typeof item !== "object") return undefined;
    const record = item as Record<string, unknown>;
    const key = typeof record.provider === "string" && typeof record.group_ref === "string"
      ? `${record.provider}:${record.group_ref}`
      : undefined;
    if (key === undefined || out.has(key)) return undefined;
    out.set(key, item);
  }
  return out;
}

export function changeCount(saved: unknown, edit: unknown): number {
  if (valuesEqual(saved, edit)) return 0;
  if (Array.isArray(saved) && Array.isArray(edit)) {
    const a = keyedEntries(saved);
    const b = keyedEntries(edit);
    if (!a || !b) return 1;
    let n = 0;
    for (const key of new Set([...a.keys(), ...b.keys()])) {
      if (!valuesEqual(a.get(key), b.get(key))) n += 1;
    }
    return Math.max(n, 1);
  }
  if (saved && edit && typeof saved === "object" && typeof edit === "object" && !Array.isArray(saved) && !Array.isArray(edit)) {
    const a = saved as Record<string, unknown>;
    const b = edit as Record<string, unknown>;
    let n = 0;
    for (const key of new Set([...Object.keys(a), ...Object.keys(b)])) {
      if (!valuesEqual(a[key], b[key])) n += 1;
    }
    return Math.max(n, 1);
  }
  return 1;
}

/** Subgroups: the cap counts as one change, each subgroup's entries count individually. */
export function subgroupsChangeCount(saved: SubgroupsSection, edit: SubgroupsSection): number {
  if (valuesEqual(saved, edit)) return 0;
  const { entries: savedEntries, ...savedRest } = saved;
  const { entries: editEntries, ...editRest } = edit;
  return changeCount(savedRest, editRest) + changeCount(savedEntries, editEntries);
}

/** Per-tab unsaved-change counts of the pre-subscription draft (shared by tab marks and the footer count). */
export function draftTabChangeCounts(saved: AnimeDraft, edit: AnimeDraft): { metadata: number; subgroups: number; rules: number; rss: number } {
  return {
    metadata: (saved.title === edit.title ? 0 : 1) + changeCount(saved.metadata, edit.metadata),
    subgroups: subgroupsChangeCount(saved.subgroups, edit.subgroups),
    rules: changeCount(saved.rules, edit.rules) + changeCount(saved.business ?? null, edit.business ?? null),
    rss: changeCount(saved.rss_sources, edit.rss_sources),
  };
}

/** The pre-subscription draft's total unsaved changes; consistent with valuesEqual (at least 1 when unequal). */
export function draftChangeCount(saved: AnimeDraft, edit: AnimeDraft): number {
  if (valuesEqual(saved, edit)) return 0;
  const tabs = draftTabChangeCounts(saved, edit);
  return Math.max(tabs.metadata + tabs.subgroups + tabs.rules + tabs.rss, 1);
}
