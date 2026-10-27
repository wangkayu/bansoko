import type {
  Artifact,
  ConnectionTestResult,
  DownloadTask,
  FileOperation,
  MetadataHold,
  NetworkRouteTestResult,
  NotificationTriggerKind,
  ReleaseVerdict,
  RoutineActivityKind,
  RSSSourceRuntime,
  Subscription,
  SubscriptionLogEntry,
} from "./api/client";
import { t, tryT, type CopyKey } from "./i18n";
import { knownReasonLabel } from "./i18n/reasons";
import { formatLocaleDateTime, formatLocaleShortDateTime, joinList } from "./i18n/format";
import { ROUTINE_KINDS } from "./observabilityCatalog.generated";

import type { StatePillTone } from "../ui/StatePill";

// The product-language table: the single copy source for backend owner state/verdict/reason.
// Components must not invent a second set of state words, and internal words like WorkItem,
// lease, fencing and trace never reach the user. All state copy goes through this module.

// --- Subscription -------------------------------------------------------------

export function subscriptionStateLabel(state: Subscription["state"]): string {
  if (state === "active") return t("anime.state.active");
  if (state === "stopped") return t("anime.state.stopped");
  return t("anime.state.pending");
}

export function subscriptionStateTone(state: Subscription["state"]): StatePillTone {
  if (state === "active") return "online";
  return state === "stopped" ? "muted" : "info";
}

// Lifecycle is an irreversible boundary independent of the subscription state; retired instances are read-only.
export function lifecycleStateLabel(state: "active" | "retired"): string {
  return state === "retired" ? t("anime.state.ended") : t("anime.state.ongoing");
}

export function lifecycleStateTone(state: "active" | "retired"): StatePillTone {
  return state === "retired" ? "muted" : "online";
}

// --- Release verdict ------------------------------------------------------------

/**
 * The single source of seed state labels (`verdict.*`). When filtered, pass the reason text
 * to get "filtered (reason)".
 */
export function releaseVerdictLabel(verdict: ReleaseVerdict, filterReason?: string): string {
  if (verdict === "filtered" && filterReason) return t("verdict.filteredWithReason", { reason: filterReason });
  return t(`verdict.${verdict}`);
}

/**
 * Badge words for a file excluded at the filter/identify stage (`rss.fileExcluded.*`): one
 * word each for rule exclusion, an unrecognized episode number and a subtitle bound to no
 * video; an unregistered filter code falls back to "filtered" instead of showing the raw
 * code. The more specific reason comes from the hover's `reason.*`.
 */
export function releaseFileExcludedLabel(code?: string | null): string {
  return t(fileExcludedKey(code));
}

function fileExcludedKey(code?: string | null): CopyKey {
  if (!code) return "rss.fileExcluded.filtered";
  // File rules give file_* codes for video file names (file_not_in_matched etc.).
  if (code.startsWith("file_")) return "rss.fileExcluded.excluded_by_rule";
  switch (code) {
    case "excluded_by_rule":
      return "rss.fileExcluded.excluded_by_rule";
    // No episode number in the video file name: under route it lands on the unknown template, under skip it is excluded as unknown_skipped.
    case "unknown":
    case "unknown_skipped":
    case "episode_unrecognized":
      return "rss.fileExcluded.episode_unrecognized";
    // A subtitle whose prefix matches no video: the only subtitle that gets its own row.
    // (A `subtitle_video_excluded` subtitle always has a bound video and comes out with it, never as its own row.)
    case "subtitle_binding_missing":
    case "subtitle_unbound":
      return "rss.fileExcluded.subtitle_binding_missing";
    default:
      return "rss.fileExcluded.filtered";
  }
}

export function releaseVerdictTone(verdict: ReleaseVerdict): StatePillTone {
  switch (verdict) {
    case "accepted_pending_download":
    case "submitted":
    case "adopted_existing":
      return "online";
    case "acquisition_failed":
      return "offline";
    case "waiting_metadata":
      return "warning";
    case "parse_failed":
      return "offline";
    case "filtered":
    case "subscription_stopped":
    case "source_unavailable":
    case "resume_baseline":
    case "start_baseline":
      return "muted";
    case "discovered":
    case "awaiting_competition":
      return "info";
  }
}

// --- DownloadTask --------------------------------------------------------------

export function downloadTaskStateLabel(state: DownloadTask["state"]): string {
  switch (state) {
    case "queued":
    case "submitting":
      return t("status.download.submitting");
    case "downloading":
      return t("status.download.downloading");
    case "completed":
      return t("status.download.completed");
    case "waiting_retry":
      return t("status.download.waiting_retry");
    case "failed":
      return t("status.download.failed");
  }
}

/** A download task deleted in the downloader: the record ends as cancelled, not failed (no failure tone). */
export function isDownloaderRemoved(task: Pick<DownloadTask, "state" | "failure_code">): boolean {
  return task.state === "failed" && task.failure_code === "DOWNLOADER_TASK_REMOVED";
}

/** A download task's state label and tone; a task deleted in the downloader shows the reason in a neutral tone. */
export function downloadTaskPill(task: Pick<DownloadTask, "state" | "failure_code">): { label: string; tone: StatePillTone } {
  if (isDownloaderRemoved(task)) return { label: t("reason.downloader_task_removed"), tone: "muted" };
  return { label: downloadTaskStateLabel(task.state), tone: downloadTaskStateTone(task.state) };
}

export function downloadTaskStateTone(state: DownloadTask["state"]): StatePillTone {
  switch (state) {
    case "completed":
      return "online";
    case "waiting_retry":
      return "warning";
    case "failed":
      return "offline";
    default:
      return "info";
  }
}

// --- Artifact ------------------------------------------------------------------

export function artifactStateLabel(state: Artifact["state"]): string {
  switch (state) {
    case "pending":
      return t("status.artifact.pending");
    case "verified":
      return t("status.artifact.verified");
    case "waiting_retry":
      return t("status.artifact.waiting_retry");
    case "failed":
      return t("status.artifact.failed");
  }
}

export function artifactStateTone(state: Artifact["state"]): StatePillTone {
  switch (state) {
    case "verified":
      return "online";
    case "waiting_retry":
      return "warning";
    case "failed":
      return "offline";
    default:
      return "muted";
  }
}

// --- FileOperation -------------------------------------------------------------

export function fileOperationStateLabel(state: FileOperation["state"]): string {
  switch (state) {
    case "pending":
    case "running":
      return t("status.fileOperation.running");
    case "completed":
      return t("status.fileOperation.completed");
    case "waiting_retry":
      return t("status.fileOperation.waiting_retry");
    case "failed":
      return t("status.fileOperation.failed");
  }
}

export function fileOperationStateTone(state: FileOperation["state"]): StatePillTone {
  switch (state) {
    case "completed":
      return "online";
    case "waiting_retry":
      return "warning";
    case "failed":
      return "offline";
    default:
      return "info";
  }
}

// --- RSSSourceRuntime health -----------------------------------------------------

export function sourceHealthLabel(health: RSSSourceRuntime["health"]): string {
  switch (health) {
    case "unknown":
      return t("status.health.unknown");
    case "ready":
      return t("status.health.ready");
    case "degraded":
      return t("status.health.degraded");
    case "failed":
      return t("status.health.failed");
  }
}

export function sourceHealthTone(health: RSSSourceRuntime["health"]): StatePillTone {
  switch (health) {
    case "ready":
      return "online";
    case "degraded":
      return "warning";
    case "failed":
      return "offline";
    default:
      return "muted";
  }
}

// --- Connection / route test results (the settings side) ---------------------------------

export function testHealthLabel(health: ConnectionTestResult["health"] | NetworkRouteTestResult["health"]): string {
  return sourceHealthLabel(health);
}

export function testHealthTone(health: ConnectionTestResult["health"] | NetworkRouteTestResult["health"]): StatePillTone {
  return sourceHealthTone(health);
}

// --- Metadata hold (the only product hold) and automation blocking reasons ------------------
//
// The single copy source for blocking reasons. What is missing is read only from the
// missing list the server gives (MetadataHold.missing or a log's details.metadata_missing);
// the frontend never infers. When the server did not say (old logs, seeds already fixed but
// not yet re-evaluated) the phrase is "Bangumi or TMDB" without naming one.
//   short: card chip, counts, title areas ("TMDB not linked")
//   long: the config dialog's warning line, log reasons ("automatic download paused: TMDB not linked")
//   seed: seed rows ("waiting for TMDB to be linked")
//   fix: the fix action ("link TMDB"), Bangumi first, then TMDB

export type MetadataLink = "bgm" | "tmdb";

function metadataLinkName(link: MetadataLink): string {
  return link === "bgm" ? "Bangumi" : "TMDB";
}

function metadataLinkNames(missing: readonly string[] | null | undefined): string {
  const links = (missing ?? []).filter((value): value is MetadataLink => value === "bgm" || value === "tmdb");
  // The server gives the order bgm, tmdb; the order is fixed here so one reason never has two wordings.
  const ordered = (["bgm", "tmdb"] as const).filter((link) => links.includes(link));
  return ordered.length > 0 ? joinList(ordered.map(metadataLinkName)) : t("anime.metadata.unknownNames");
}

export type BlockingPhrases = {
  short: string;
  long: string;
  seed: string;
  /** The binding the fix action targets; undefined when the server did not say which is missing (no fix button). */
  fix?: { link: MetadataLink; label: string };
};

/** Every phrasing of one metadata blocking reason; an empty missing means the server did not say which is missing. */
export function metadataBlocking(missing: readonly string[] | null | undefined): BlockingPhrases {
  const names = metadataLinkNames(missing);
  const first = (["bgm", "tmdb"] as const).find((link) => missing?.includes(link));
  return {
    short: t("anime.metadata.missing", { names }),
    long: t("anime.metadata.paused", { names }),
    seed: t("anime.metadata.waiting", { names }),
    fix: first ? { link: first, label: t("anime.metadata.link", { name: metadataLinkName(first) }) } : undefined,
  };
}

/** Returns the phrasings when there is a hold; undefined without one. */
export function metadataHoldBlocking(hold: MetadataHold | null | undefined): BlockingPhrases | undefined {
  return hold?.held ? metadataBlocking(hold.missing) : undefined;
}

export function metadataMissingLabel(missing: readonly string[]): string {
  return metadataBlocking(missing).short;
}

/**
 * Seed state: waiting_metadata writes which binding is being waited on from the server's
 * current hold; every other state goes through releaseVerdictLabel.
 */
export function releaseVerdictLabelWithHold(
  verdict: ReleaseVerdict,
  hold: MetadataHold | null | undefined,
  filterReason?: string,
): string {
  if (verdict === "waiting_metadata" && hold?.held) return metadataBlocking(hold.missing).seed;
  return releaseVerdictLabel(verdict, filterReason);
}

// --- reason code ------------------------------------------------------------------

// Reason-code copy lives in `reason.*` (lib/i18n/reasons.ts). source_disabled /
// source_degraded are reason codes, not verdicts.

/**
 * The server's reason_detail for "filtered" is the more specific filter reason code (e.g.
 * release_must_contain_not_matched); when registered it takes precedence over reason_code
 * itself (filtered_by_rules).
 */
export function evaluationReasonCode(code: string | null | undefined, detail: string | null | undefined): string | undefined {
  if (detail && knownReasonLabel(detail) !== undefined) return detail;
  return code || undefined;
}

/**
 * When the state label already expresses the reason (e.g. "waiting for metadata to be
 * matched" and metadata_incomplete), no separate reason line is shown. Other reasons show as
 * usual, unregistered ones as "unknown reason".
 */
export function isReasonImpliedByVerdict(verdict: string | null | undefined, code: string | null | undefined): boolean {
  if (!code) return false;
  const reason = code.trim().toLowerCase();
  if (verdict === "waiting_metadata") return reason === "metadata_incomplete" || reason === "metadata_hold";
  if (verdict === "subscription_stopped") return reason === "subscription_stopped";
  if (verdict === "start_baseline") return reason === "start_baseline";
  if (verdict === "awaiting_competition") return reason === "awaiting_competition";
  if (verdict === "source_unavailable") return reason === "source_unavailable" || reason === "source_disabled" || reason === "source_degraded";
  return false;
}

// --- Common -------------------------------------------------------------------------------

const ROUTINE_KIND_LABEL_KEYS = {
  catalog_refresh: "status.routine.catalog_refresh",
  provider_refresh: "status.routine.provider_refresh",
  rss_refresh: "status.routine.rss_refresh",
  metadata_match: "status.routine.metadata_match",
  automation_dispatch: "status.routine.automation_dispatch",
  retention_pass: "status.routine.retention_pass",
  file_manager_operation: "status.routine.file_manager_operation",
} as const satisfies Record<(typeof ROUTINE_KINDS)[number], CopyKey>;

export function routineKindLabel(kind: RoutineActivityKind): string {
	return t(ROUTINE_KIND_LABEL_KEYS[kind]);
}

export function fileOperationKindLabel(operation: FileOperation["operation"]): string {
  switch (operation) {
    case "copy":
      return t("file.operation.copy");
    case "move":
      return t("file.operation.move");
    case "hardlink":
      return t("file.operation.hardlink");
  }
}

/** Human-readable byte counts; a missing value shows the placeholder instead of 0. */
export function formatBytes(bytes?: number | null): string {
  if (bytes === undefined || bytes === null) return "—";
  if (bytes === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  return `${(bytes / Math.pow(1024, index)).toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
}

export function formatDateTime(value: string | undefined): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return formatLocaleDateTime(date);
}

/** Month-day + hour-minute for the log's compact rows; an invalid value returns as written. */
export function formatShortDateTime(value: string | undefined): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return formatLocaleShortDateTime(date);
}

/** Relative time ("3 hours ago"); an invalid or missing time returns undefined and the caller omits it. */
export function formatTimeAgo(value: string | undefined, now: number = Date.now()): string | undefined {
  if (!value) return undefined;
  const timestamp = Date.parse(value);
  if (Number.isNaN(timestamp)) return undefined;
  const minutes = Math.floor(Math.max(0, now - timestamp) / 60_000);
  if (minutes < 1) return t("common.timeAgo.justNow");
  if (minutes < 60) return t("common.timeAgo.minutes", { n: minutes });
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return t("common.timeAgo.hours", { n: hours });
  return t("common.timeAgo.days", { n: Math.floor(hours / 24) });
}

export function subscriptionLogKindLabel(kind: SubscriptionLogEntry["kind"]): string {
  return tryT(`status.logKind.${kind}`) ?? t("status.logKind.other");
}

export function subscriptionLogOutcomeTone(
  outcome: SubscriptionLogEntry["outcome"],
): StatePillTone {
  switch (outcome) {
    case "success":
      return "online";
    case "failure":
      return "offline";
    case "info":
      return "muted";
    default:
      return "muted";
  }
}

// --- Plugin delivery ------------------------------------------------------------

export function notificationTriggerKindLabel(kind: NotificationTriggerKind): string {
  switch (kind) {
    case "download_submitted":
      return t("settings.notifications.trigger.download_submitted");
    case "file_operation_completed":
      return t("settings.notifications.trigger.file_operation_completed");
    case "file_operation_failed":
      return t("settings.notifications.trigger.file_operation_failed");
    case "release_discovered":
      return t("settings.notifications.trigger.release_discovered");
    case "download_failed":
      return t("settings.notifications.trigger.download_failed");
    case "subscription_changed":
      return t("settings.notifications.trigger.subscription_changed");
    case "unknown_files_imported":
      return t("settings.notifications.trigger.unknown_files_imported");
  }
}

/**
 * The subgroup policy vocabulary (for the short labels and slot counts).
 *
 * This is a display-layer vocabulary mapping and does not decide "which groups are in
 * effect": the effective selection is computed by the server with the runtime decision
 * kernel at owner/discovery reads, and the client only reads `effective_selected`.
 */
export type SelectionPolicy = "ranked" | "always" | "never";

export function selectionPolicy(entry: {
  selection_mode: "auto" | "manual";
  manual_selected?: boolean | null;
}): SelectionPolicy {
  if (entry.selection_mode === "auto") return "ranked";
  return entry.manual_selected === true ? "always" : "never";
}
