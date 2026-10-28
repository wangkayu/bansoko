import type { ObservabilityRecord } from "../lib/api/client";
import { t, tryT } from "../lib/i18n";
import { formatBytes, subscriptionLogKindLabel } from "../lib/labels";
import { joinList } from "../lib/i18n/format";
import { knownReasonLabel, reasonView } from "../lib/i18n/reasons";
import { episodesRangeLabel } from "@/features/rss-list/model";
import {
  bareMessageText,
  deliveryState,
  entryState,
  messageText,
  type BusinessEntry,
  type LogState,
} from "./logState";

// Grouping for the events tab: the events of one acquisition of the same seed merge into one
// row (download → file verification → import → import location → replacement → acquisition
// result); the other adjacent events with the same kind, outcome, anime and message collapse
// into one counted group. Grouping reads only the facts frozen by the server at write time
// (release_id, message_code and details), never parsed from text. Groups are recomputed over
// all loaded pages: an acquisition spanning pages merges in place once the next page loads and
// never appears twice.

export type { BusinessEntry } from "./logState";

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
 * A replacement decision shows one row: of several `replacement_decided` entries with the same
 * subject (a retry after a not-replaced result replaces it), only the newest remains.
 */
export function dedupeReplacements(records: readonly ObservabilityRecord[]): ObservabilityRecord[] {
  const newest = new Map<string, BusinessEntry>();
  for (const record of records) {
    if (record.record_type !== "business") continue;
    const entry = record.entry;
    if (entry.kind !== "replacement_decided" || !entry.subject_id) continue;
    const current = newest.get(entry.subject_id);
    if (!current || compareAscending(current, entry) < 0) newest.set(entry.subject_id, entry);
  }
  return records.filter((record) => {
    if (record.record_type !== "business") return true;
    const entry = record.entry;
    if (entry.kind !== "replacement_decided" || !entry.subject_id) return true;
    return newest.get(entry.subject_id) === entry;
  });
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

function recordId(record: ObservabilityRecord): string {
  if (record.record_type === "business") return record.entry.id;
  if (record.record_type === "plugin") return record.delivery.id;
  if (record.record_type === "routine") return record.routine.id;
  return record.run.id;
}

/** The time a row sorts by: a run's newest record, a delivery's last update. */
export function itemTime(item: EventItem): number {
  if (item.type === "run") return Date.parse(item.steps[item.steps.length - 1]!.occurred_at);
  if (item.type === "group") return Date.parse(item.entries[0]!.occurred_at);
  const record = item.record;
  if (record.record_type === "business") return Date.parse(record.entry.occurred_at);
  if (record.record_type === "plugin") return Date.parse(record.delivery.updated_at ?? record.delivery.created_at);
  return 0;
}

/** Rows of the events list in descending time: one download per row, the other adjacent same-message singles collapse into a group; notification-delivery rows never collapse. */
export function groupEvents(allRecords: readonly ObservabilityRecord[]): EventItem[] {
  const records = dedupeReplacements(allRecords);
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
      if (!emittedRuns.has(steps)) {
        flush();
        emittedRuns.add(steps);
        items.push({ type: "run", key: `run:${steps[0]!.id}`, steps });
      }
      continue;
    }
    const signature = [entry.kind, entry.outcome, entry.managed_anime_id, entry.message_code, JSON.stringify(entry.message_params ?? {})].join("\u0000");
    if (run && run.signature === signature) run.entries.push(entry);
    else {
      flush();
      run = { signature, entries: [entry] };
    }
  }
  flush();
  // Stable: rows with equal time keep the server order.
  return items.map((item, index) => ({ item, index })).sort((a, b) => itemTime(b.item) - itemTime(a.item) || a.index - b.index).map(({ item }) => item);
}

export type RunFile = {
  key: string;
  name: string;
  folder?: string;
  size?: string;
  label: string;
  ok: boolean;
  reason?: string;
};

export type RunView = {
  state: LogState;
  /** The final result (imported / download failed / downloading…). */
  result: string;
  /** The step that decides the result: on failure its reason is used. */
  decisive: BusinessEntry;
  groupName?: string;
  episodes?: string;
  releaseTitle?: string;
  oneShot: boolean;
  /** The newest step: time, anime. */
  latest: BusinessEntry;
  /** Second line: file count · size · replacement terminal phrase. */
  meta: string;
  /** Failure reason, third line. */
  reason?: string;
  /** Redacted technical detail of the failing step. */
  technical?: string;
  files: RunFile[];
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

/** Parallel meta values join with " · "; missing values are simply omitted. */
export function joinMeta(...parts: (string | undefined | null | false)[]): string {
  return parts.filter((part): part is string => typeof part === "string" && part !== "").join(" · ");
}

function sizeText(bytes: number | undefined | null): string | undefined {
  return typeof bytes === "number" && bytes > 0 ? formatBytes(bytes) : undefined;
}

function fileCountText(n: number | undefined | null): string | undefined {
  return typeof n === "number" && n > 0 ? t("log.step.fileCount", { n }) : undefined;
}

/** A download's title parts, final result, summary line and files. Steps are ascending and already deduplicated. */
export function runView(steps: readonly BusinessEntry[]): RunView {
  const latest = steps[steps.length - 1]!;
  const settled = [...steps].reverse().find((step) => step.kind === "acquisition_settled");
  const failure = [...steps].reverse().find((step) => step.outcome === "failure");
  const decisive = settled?.outcome === "failure" && failure ? failure : (settled ?? failure ?? latest);
  const state: LogState = settled ? entryState(settled) : "in_progress";
  const verified = steps.find((step) => step.kind === "artifact_verified");
  const replacement = [...steps].reverse().find((step) => step.kind === "replacement_decided");
  const download = steps.find((step) => step.kind === "download_changed" && step.details?.size_bytes);
  const meta = joinMeta(
    fileCountText(verified?.details?.item_count ?? settled?.details?.item_count),
    sizeText(verified?.details?.size_bytes ?? download?.details?.size_bytes),
    replacement ? messageText(replacement) : undefined,
  );
  const failing = state === "failure";
  return {
    state,
    result: runResult(settled, failure, steps),
    decisive,
    groupName: firstDetail(steps, "group_name") ?? undefined,
    episodes: episodesText(firstDetail(steps, "episodes")),
    releaseTitle: firstDetail(steps, "release_title") ?? undefined,
    oneShot: steps.some((step) => step.source === "one_shot"),
    latest,
    meta,
    reason: failing ? reasonView(decisive.reason_code ?? settled?.reason_code)?.text : undefined,
    technical: failing ? (decisive.details?.reason_detail ?? settled?.details?.reason_detail ?? undefined) : undefined,
    files: runFiles(steps),
  };
}

function runResult(settled: BusinessEntry | undefined, failure: BusinessEntry | undefined, steps: readonly BusinessEntry[]): string {
  if (settled) {
    if (settled.outcome === "success") return t("log.run.result.imported");
    if (settled.outcome === "failure") return failureResult(failure);
    return isRemovedInDownloader(steps) ? t("log.run.result.removed") : t("log.run.result.cancelled");
  }
  const kinds = new Set(steps.map((step) => step.kind));
  if (kinds.has("file_alignment_completed")) return t("log.run.result.importing");
  if (kinds.has("artifact_verified")) return t("log.run.result.importing");
  const download = [...steps].reverse().find((step) => step.kind === "download_changed");
  if (download?.message_code === "download_completed") return t("log.run.result.downloaded");
  return t("log.run.result.downloading");
}

function failureResult(failure: BusinessEntry | undefined): string {
  if (failure?.kind === "download_changed") return t("log.run.result.downloadFailed");
  if (failure?.kind === "file_alignment_failed") return t("log.run.result.importFailed");
  return t("log.run.result.failed");
}

function isRemovedInDownloader(steps: readonly BusinessEntry[]): boolean {
  return steps.some((step) => step.message_code === "download_removed" || step.reason_code?.toLowerCase() === "downloader_task_removed");
}

/** The files of one download, from the import and placement steps the server froze (name, location, size, result). */
function runFiles(steps: readonly BusinessEntry[]): RunFile[] {
  const files: RunFile[] = [];
  const named = new Set<string>();
  for (const step of steps) {
    const name = step.details?.file_name;
    if (!name) continue;
    if (step.kind === "file_alignment_completed" || step.kind === "file_alignment_failed") {
      const failed = step.kind === "file_alignment_failed";
      named.add(name);
      files.push({
        key: step.id,
        name,
        folder: step.details?.library_folder ?? undefined,
        size: sizeText(step.details?.size_bytes),
        label: bareMessageText(step),
        ok: !failed,
        reason: failed ? reasonView(step.reason_code)?.text : undefined,
      });
    }
  }
  for (const step of steps) {
    const name = step.details?.file_name;
    if (step.kind !== "placement_changed" || !name || named.has(name)) continue;
    named.add(name);
    files.push({
      key: step.id,
      name,
      folder: step.details?.library_folder ?? undefined,
      size: sizeText(step.details?.size_bytes),
      label: bareMessageText(step),
      ok: true,
    });
  }
  return files;
}

/**
 * Each step's line when expanded: the message, then its specifics (size, file count,
 * location). A replacement writes the actual outcome: which files were replaced, or why
 * nothing was.
 */
export function stepText(entry: BusinessEntry): string {
  const details = entry.details ?? undefined;
  switch (entry.kind) {
    case "download_changed":
      if (entry.message_code === "download_submitted") return joinMeta(bareMessageText(entry), fileCountText(details?.item_count), sizeText(details?.size_bytes));
      return joinMeta(messageText(entry), entry.message_code === "download_completed" ? sizeText(details?.size_bytes) : undefined);
    case "artifact_verified":
      return joinMeta(messageText(entry), fileCountText(details?.item_count), sizeText(details?.size_bytes));
    case "file_alignment_completed":
    case "file_alignment_failed":
      return joinMeta(messageText(entry), details?.library_folder, sizeText(details?.size_bytes));
    case "placement_changed":
      return joinMeta(messageText(entry), details?.library_folder && details.file_name ? `${details.library_folder}/${details.file_name}` : details?.file_name);
    case "replacement_decided":
      return replacementText(entry);
    case "acquisition_settled": {
      const base = messageText(entry);
      if (entry.outcome === "success" && details?.item_count) return joinMeta(base, t("log.step.importedCount", { n: details.item_count }));
      if (entry.outcome === "failure" && details?.error_count) return joinMeta(base, t("log.step.failedCount", { n: details.error_count }));
      return base;
    }
    default:
      return messageText(entry);
  }
}

function replacementText(entry: BusinessEntry): string {
  const text = messageText(entry);
  const names = entry.details?.replaced_files;
  if (names && names.length > 0) return t("log.step.replacementNames", { text, names: joinList(names) });
  if (entry.details?.kept_file) return t("log.step.replacementNames", { text, names: entry.details.kept_file });
  if (entry.message_code === "replacement_not_replaced") {
    const why = tryT(`log.reason.notReplaced.${entry.details?.reason_code ?? ""}`);
    if (why) return t("log.step.replacementWhy", { text, why });
  }
  return t("log.step.replacement", { text });
}

/** A settings change writes which rule sections changed (rules: title filtering, naming). */
export function policyChangeText(entry: BusinessEntry): string | undefined {
  if (entry.kind !== "policy_changed") return undefined;
  const sections = entry.details?.changed_sections;
  if (!sections || sections.length === 0) return undefined;
  return t("log.rulesChanged", { sections: joinList(sections.map((section) => tryT(`tabs.sectionName.${section}`) ?? section)) });
}

/** The concrete subject of a one-line event (subgroup): settings changes and RSS records write the subgroup name. */
export function entryGroupName(entry: BusinessEntry): string | undefined {
  if (entry.details?.group_name) return entry.details.group_name;
  if (entry.kind === "rss_new_content") return entry.group_ref ?? undefined;
  return undefined;
}

export type EntryView = {
  state: LogState;
  title: string;
  /** The right column: the message. */
  result: string;
  /** Second line (changed sections). */
  meta?: string;
  /** Third line: why a filtered / failed / deferred record ended like this. */
  reason?: string;
  technical?: string;
};

/** A single (non-download) business record. `anime` is omitted when the list is filtered to one anime. */
export function entryView(entry: BusinessEntry, anime: string | undefined): EntryView {
  const result = messageText(entry);
  const label = subscriptionLogKindLabel(entry.kind);
  const title =
    entry.kind === "release_processed"
      ? joinMeta(anime, entry.details?.release_title ?? entryGroupName(entry) ?? label)
      : joinMeta(anime, entryGroupName(entry), label);
  const state = entryState(entry);
  let reason: string | undefined;
  if (state === "failure") reason = reasonView(entry.reason_code)?.text;
  else if (entry.kind === "release_processed" && state === "info") reason = knownReasonLabel(entry.reason_code);
  if (reason === result) reason = undefined;
  return {
    state,
    title,
    result,
    meta: policyChangeText(entry),
    reason,
    technical: state === "failure" ? (entry.details?.reason_detail ?? undefined) : undefined,
  };
}

export type DeliveryView = {
  state: LogState;
  unconfirmed: boolean;
  result: string;
  reason?: string;
};

export function deliveryView(delivery: Extract<ObservabilityRecord, { record_type: "plugin" }>["delivery"]): DeliveryView {
  const state = deliveryState(delivery);
  const result =
    delivery.state === "delivered"
      ? t("log.delivery.delivered")
      : delivery.state === "failed"
        ? t("log.delivery.failed")
        : delivery.state === "unknown"
          ? t("log.delivery.unconfirmed")
          : delivery.state === "waiting_retry"
            ? t("log.delivery.waiting")
            : t("log.delivery.sending");
  return {
    state,
    unconfirmed: delivery.state === "unknown",
    result,
    reason: state === "failure" ? reasonView(delivery.last_error_code)?.text : undefined,
  };
}
