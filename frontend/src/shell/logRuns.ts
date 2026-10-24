import type { ObservabilityRecord } from "../lib/api/client";
import { t, tryT } from "../lib/i18n";
import { formatBytes } from "../lib/labels";
import { joinList } from "../lib/i18n/format";
import { episodesRangeLabel } from "@/features/rss-list/model";

// Grouping for the events tab: the events of one acquisition of the same seed merge into one
// row (download → file verification → import → import location → replacement → acquisition
// result); the other adjacent events with the same kind, outcome, anime and result collapse
// into one counted group. Grouping reads only the facts frozen by the server at write time
// (release_id and details), never parsed from titles or summaries. Groups are recomputed over
// all loaded pages: an acquisition spanning pages merges in place once the next page loads and
// never appears twice.

export type BusinessEntry = Extract<ObservabilityRecord, { record_type: "business" }>["entry"];

export type EventItem =
  | { type: "record"; key: string; record: ObservabilityRecord }
  | { type: "group"; key: string; entries: BusinessEntry[] }
  | { type: "run"; key: string; steps: BusinessEntry[] };

/** Record kinds that belong to one download, in their order within one acquisition (used to order records sharing a timestamp). */
const RUN_KIND_ORDER: Partial<Record<BusinessEntry["kind"], number>> = {
  download_changed: 1,
  artifact_verified: 2,
  file_alignment_completed: 3,
  file_alignment_failed: 3,
  placement_changed: 4,
  replacement_decided: 5,
  acquisition_settled: 6,
};

/** A download needs at least one record of the acquisition itself; a sequence with only an import location or a replacement (file scan, a later re-decision) renders as single rows. */
const CORE_RUN_KINDS = new Set<BusinessEntry["kind"]>([
  "download_changed",
  "artifact_verified",
  "file_alignment_completed",
  "file_alignment_failed",
  "acquisition_settled",
]);

function compareAscending(a: BusinessEntry, b: BusinessEntry): number {
  const time = Date.parse(a.occurred_at) - Date.parse(b.occurred_at);
  if (time !== 0) return time;
  const rank = (RUN_KIND_ORDER[a.kind] ?? 0) - (RUN_KIND_ORDER[b.kind] ?? 0);
  if (rank !== 0) return rank;
  return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
}

/**
 * Records of the same seed split into acquisitions in ascending time order: an acquisition
 * result closes one acquisition and later records start a new one (a re-acquisition after a
 * failure yields two rows). Returns, for each record, the acquisition it belongs to (ascending
 * steps).
 */
function collectRuns(records: readonly ObservabilityRecord[]): Map<string, BusinessEntry[]> {
  const byRelease = new Map<string, BusinessEntry[]>();
  for (const record of records) {
    if (record.record_type !== "business") continue;
    const entry = record.entry;
    if (!entry.release_id || RUN_KIND_ORDER[entry.kind] === undefined) continue;
    const list = byRelease.get(entry.release_id) ?? [];
    list.push(entry);
    byRelease.set(entry.release_id, list);
  }
  const runOf = new Map<string, BusinessEntry[]>();
  for (const entries of byRelease.values()) {
    const sorted = [...entries].sort(compareAscending);
    let current: BusinessEntry[] = [];
    const close = () => {
      if (current.some((entry) => CORE_RUN_KINDS.has(entry.kind))) {
        for (const entry of current) runOf.set(entry.id, current);
      }
      current = [];
    };
    for (const entry of sorted) {
      current.push(entry);
      if (entry.kind === "acquisition_settled") close();
    }
    close();
  }
  return runOf;
}

/** Rows of the events list: one download per row, the other adjacent same-result singles collapse into a group; notification-delivery rows never collapse. */
export function groupEvents(records: readonly ObservabilityRecord[], resultText: (entry: BusinessEntry) => string): EventItem[] {
  const runOf = collectRuns(records);
  const emittedRuns = new Set<BusinessEntry[]>();
  const items: EventItem[] = [];
  let run: { signature: string; entries: BusinessEntry[] } | null = null;
  const flush = () => {
    if (!run) return;
    const [first] = run.entries;
    items.push(
      run.entries.length > 1
        ? { type: "group", key: `group:${first!.id}`, entries: run.entries }
        : { type: "record", key: `business:${first!.id}`, record: { record_type: "business", entry: first! } },
    );
    run = null;
  };
  for (const record of records) {
    if (record.record_type !== "business") {
      flush();
      items.push({ type: "record", key: `${record.record_type}:${recordId(record)}`, record });
      continue;
    }
    const entry = record.entry;
    const steps = runOf.get(entry.id);
    if (steps) {
      // The list runs in descending time order: a download appears at the position of its newest step.
      if (!emittedRuns.has(steps)) {
        flush();
        emittedRuns.add(steps);
        items.push({ type: "run", key: `run:${steps[0]!.id}`, steps });
      }
      continue;
    }
    const signature = [entry.kind, entry.outcome, entry.managed_anime_id, resultText(entry)].join("\u0000");
    if (run && run.signature === signature) run.entries.push(entry);
    else {
      flush();
      run = { signature, entries: [entry] };
    }
  }
  flush();
  return items;
}

function recordId(record: ObservabilityRecord): string {
  if (record.record_type === "business") return record.entry.id;
  if (record.record_type === "plugin") return record.delivery.id;
  if (record.record_type === "routine") return record.routine.id;
  return record.run.id;
}

export type RunView = {
  outcome: BusinessEntry["outcome"];
  /** The final result (imported / acquisition failed / downloading…). */
  result: string;
  /** The step that decides the result: on failure its reason is used. */
  decisive: BusinessEntry;
  groupName?: string;
  episodes?: string;
  releaseTitle?: string;
  /** The newest step: time, anime. */
  latest: BusinessEntry;
};

function firstDetail<K extends keyof NonNullable<BusinessEntry["details"]>>(steps: readonly BusinessEntry[], key: K) {
  for (const step of steps) {
    const value = step.details?.[key];
    if (value !== undefined && value !== null) return value as NonNullable<NonNullable<BusinessEntry["details"]>[K]>;
  }
  return undefined;
}

/** Episode label: a single episode "episode N", several "episodes 01-03"; only from the episode numbers the server froze. */
export function episodesText(episodes: readonly number[] | undefined): string | undefined {
  if (!episodes || episodes.length === 0) return undefined;
  if (episodes.length === 1) return t("records.episode", { n: episodes[0]! });
  return t("records.episodes", { range: episodesRangeLabel(episodes) });
}

/** A download's title and final result. Steps are ascending. */
export function runView(steps: readonly BusinessEntry[]): RunView {
  const latest = steps[steps.length - 1]!;
  const settled = [...steps].reverse().find((step) => step.kind === "acquisition_settled");
  const failure = [...steps].reverse().find((step) => step.outcome === "failure");
  const decisive = settled ?? failure ?? latest;
  return {
    outcome: settled ? settled.outcome : failure ? "failure" : latest.outcome,
    result: runResult(settled, failure, steps),
    decisive,
    groupName: firstDetail(steps, "group_name") ?? undefined,
    episodes: episodesText(firstDetail(steps, "episodes")),
    releaseTitle: firstDetail(steps, "release_title") ?? undefined,
    latest,
  };
}

function runResult(settled: BusinessEntry | undefined, failure: BusinessEntry | undefined, steps: readonly BusinessEntry[]): string {
  if (settled) {
    if (settled.outcome === "success") return t("log.run.result.imported");
    if (settled.outcome === "failure") return t("log.run.result.failed");
    return isRemovedInDownloader(steps) ? t("log.run.result.removed") : t("log.run.result.cancelled");
  }
  if (failure) {
    if (failure.kind === "download_changed") return t("log.run.result.downloadFailed");
    if (failure.kind === "file_alignment_failed") return t("log.run.result.importFailed");
    return t("log.run.result.failed");
  }
  const kinds = new Set(steps.map((step) => step.kind));
  if (kinds.has("file_alignment_completed")) return t("log.run.result.imported");
  if (kinds.has("artifact_verified")) return t("log.run.result.importing");
  const download = [...steps].reverse().find((step) => step.kind === "download_changed");
  if (download?.reason_code === "submitted") return t("log.run.result.downloading");
  if (download && download.outcome === "info") return t("log.run.result.removed");
  if (download) return t("log.run.result.downloaded");
  return t("log.run.result.downloading");
}

function isRemovedInDownloader(steps: readonly BusinessEntry[]): boolean {
  return steps.some((step) => step.reason_code?.toLowerCase() === "downloader_task_removed");
}

/** Parallel meta values join with " · "; missing values are simply omitted. */
function meta(...parts: (string | undefined | null | false)[]): string {
  return parts.filter((part): part is string => typeof part === "string" && part !== "").join(" · ");
}

function sizeText(bytes: number | undefined | null): string | undefined {
  return typeof bytes === "number" && bytes > 0 ? formatBytes(bytes) : undefined;
}

function fileCountText(n: number | undefined | null): string | undefined {
  return typeof n === "number" && n > 0 ? t("log.step.fileCount", { n }) : undefined;
}

/**
 * Each step's specifics when expanded: file name, import location, size; a replacement writes
 * the actual outcome. Older records without these facts fall back to the summary the server
 * wrote.
 */
export function stepText(entry: BusinessEntry): string {
  const details = entry.details ?? undefined;
  const reason = entry.reason_code?.toLowerCase();
  const name = details?.file_name;
  switch (entry.kind) {
    case "download_changed": {
      if (!details) return entry.summary;
      if (reason === "submitted") return meta(t("log.step.submitted"), fileCountText(details.item_count), sizeText(details.size_bytes));
      if (reason === "downloader_task_removed") return t("log.step.removed");
      if (entry.outcome === "failure") return t("log.step.downloadFailed");
      return meta(t("log.step.downloaded"), sizeText(details.size_bytes));
    }
    case "artifact_verified":
      if (!details) return entry.summary;
      return meta(t("log.step.verified"), fileCountText(details.item_count), sizeText(details.size_bytes));
    case "file_alignment_completed": {
      if (!name) return entry.summary;
      const key = reason === "file_adopted" ? "log.step.adopted" : reason === "file_retained" ? "log.step.retained" : "log.step.imported";
      return meta(t(key, { name }), details?.library_folder, sizeText(details?.size_bytes));
    }
    case "file_alignment_failed":
      return name ? meta(t("log.step.importFailed", { name }), sizeText(details?.size_bytes)) : entry.summary;
    case "placement_changed": {
      if (!details) return entry.summary;
      const label = reason === "unknown_file_released" ? t("log.step.unknownReleased") : t("log.step.placement");
      return meta(label, details.library_folder && name ? `${details.library_folder}/${name}` : name);
    }
    case "replacement_decided":
      return replacementText(entry);
    case "acquisition_settled": {
      if (entry.outcome === "success")
        return meta(t("log.step.settled"), details?.item_count ? t("log.step.importedCount", { n: details.item_count }) : undefined);
      if (entry.outcome === "failure")
        return meta(t("log.step.settledFailed"), details?.error_count ? t("log.step.failedCount", { n: details.error_count }) : undefined);
      return t("log.step.settledCancelled");
    }
    default:
      return entry.summary;
  }
}

/** A replacement writes the actual outcome: which file was replaced / which existing version was kept / how many versions the episode now has. */
function replacementText(entry: BusinessEntry): string {
  const details = entry.details;
  if (details?.replaced_files && details.replaced_files.length > 0)
    return t("log.step.replaced", { names: joinList(details.replaced_files) });
  if (details?.kept_file) return t("log.step.kept", { name: details.kept_file });
  if (typeof details?.version_count === "number" && details.version_count > 0)
    return t("log.step.versions", { n: details.version_count });
  return entry.summary;
}

/** A settings change writes which rule sections changed (rules: title filtering, naming); older records fall back to the summary. */
export function policyChangeText(entry: BusinessEntry): string | undefined {
  if (entry.kind !== "policy_changed") return undefined;
  const sections = entry.details?.changed_sections;
  if (sections && sections.length > 0) {
    return t("log.rulesChanged", { sections: joinList(sections.map((section) => tryT(`tabs.sectionName.${section}`) ?? section)) });
  }
  if (entry.reason_code === "rss_source_offset_changed" && entry.details?.group_name) return t("log.offsetChanged");
  return undefined;
}

/** The concrete subject of a one-line event (subgroup): settings changes and RSS records write the subgroup name. */
export function entryGroupName(entry: BusinessEntry): string | undefined {
  if (entry.details?.group_name) return entry.details.group_name;
  if (entry.kind === "rss_new_content" || entry.kind === "rss_refresh_failed") return entry.group_ref ?? undefined;
  return undefined;
}
