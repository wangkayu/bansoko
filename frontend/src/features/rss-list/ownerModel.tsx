import type { ReactNode } from "react";
import { File, FileText, FileVideo, ListTree, Pencil, RotateCcw } from "lucide-react";
import type { AcquisitionTarget, AnimeWorkspace, Release, ReleaseFile, RSSListFile, RSSListGroup, RSSListItem, RSSListView, RSSSource, TargetObservation } from "../../lib/api/client";
import { artifactStateLabel, artifactStateTone, downloadTaskPill, downloadTaskStateLabel, downloadTaskStateTone, fileOperationKindLabel, fileOperationStateLabel, fileOperationStateTone, formatBytes, formatDateTime, formatTimeAgo, evaluationReasonCode, isReasonImpliedByVerdict, releaseFileExcludedLabel, releaseVerdictLabelWithHold, releaseVerdictTone } from "../../lib/labels";
import type { StatePillTone } from "../../ui";
import { StatePill, Button } from "../../ui";
import { ReasonText } from "../../ui/ReasonText";
import { BoundSubtitles, type BoundSubtitleItem } from "../../ui/BoundSubtitles";
import { groupBoundSubtitles } from "../../lib/boundSubtitles";
import { emptyFactsNote, knownReasonLabel, namingFailureDetail, namingPendingLabel, reasonLabel } from "../../lib/i18n/reasons";
import { t } from "../../lib/i18n";
import { labelValue } from "../../lib/i18n/format";
import { groupLabel, disambiguateGroupNames } from "../../lib/groupLabels";
import { groupNameInput } from "./draftModel";
import { TagBadges } from "./TagBadges";
import { tagFilterReasonLabel } from "../../lib/titleTags";
import { manifestSummary, releaseEpisodeLabel, rssFeedState, type RssGroupModel, type RssItemModel, type RssWorkspaceModel } from "./model";

export type RetryTarget = {
  // Stage retries are retired: manual retries of the three acquisition owners (download_task /
// artifact / file_operation) are rejected by the backend with a zero-write 409
// MANUAL_RETRY_STAGE_RETIRED, and the UI no longer produces those targets. RSS source refresh
// (rss_source_runtime) is unaffected and stays available.
  ownerType: "rss_source_runtime";
  ownerId: string;
  failedWorkItemId: string;
};

export type OffsetDrawerSource = RSSSource;

export type OwnerViewHandlers = {
  /** The action key in flight (`ownerType:ownerId`), for button busy states. */
  busyKey?: string;
  /** The effective selection's score (effective_score), keyed `provider:group_ref`; the same value the subgroups page displays. */
  groupScores?: Map<string, number>;
  onRetry?: (target: RetryTarget) => void;
  onOpenOffset?: (source: OffsetDrawerSource) => void;
  /** Retries one seed: runs the normal acceptance flow again under the current rules. */
  onRetryRelease?: (release: Release) => void;
  /** Opens the release's retained torrent payload structure (read-only, never a re-fetch). */
  onOpenStructure?: (release: Release) => void;
  /** Opens this seed's latest acquisition execution record (the processing view). */
  onOpenRecord?: (acquisitionID: string) => void;
};

const previewReason = () => t("rss.previewDisabled");

/** The inline note for retired stage retries. Manual retries of acquisition owners
 *  (download_task / artifact / file_operation) are rejected by the backend with a zero-write 409
 *  MANUAL_RETRY_STAGE_RETIRED, so only the failure state and reason remain here, plus a note that
 *  a replacement entry does not exist yet. */

function describeReason(code?: string | null, detail?: string | null): ReactNode {
  const resolved = evaluationReasonCode(code, detail);
  return resolved ? <ReasonText code={resolved} /> : undefined;
}

// A disabled action explains itself: `note` is the server's reason for refusing it (the retry
// decision's own words), carried beside the action's label so a greyed button never stands mute.
function actionProps(preview: boolean, onClick?: () => void, disabled = false, label = t("rss.action"), note?: string) {
  if (preview) {
    return { disabled: true, title: previewReason(), "aria-label": labelValue(label, previewReason()) };
  }
  if (disabled && note) {
    // Keep the action's own name and carry the reason beside it (the same label/value join the
    // preview state uses), so assistive tech reads "重新执行：文件已解除管理".
    const named = labelValue(label, note);
    return { disabled, title: named, "aria-label": named, onClick };
  }
  return { disabled, title: label, "aria-label": label, onClick };
}

function groupIdentity(group: RSSListGroup): string {
  return group.scope.rss_source_id ?? `${group.scope.provider}:${group.scope.provider_entry_ref}:${group.scope.provider_group_ref}`;
}

function releaseForItem(item: RSSListItem, workspace: AnimeWorkspace): Release | undefined {
  const match = /^release:(.+)$/.exec(item.snapshot_id);
  return match ? workspace.releases.find((release) => release.id === match[1]) : undefined;
}

function terminalRows(release: Release | undefined, workspace: AnimeWorkspace): ReactNode[] {
  if (!release) return [];
  const task = workspace.download_tasks.find((entry) => entry.id === release.download_task_id);
  const artifacts = task ? workspace.artifacts.filter((entry) => entry.download_task_id === task.id) : [];
  const operations = artifacts.flatMap((artifact) => workspace.file_operations.filter((operation) => operation.artifact_id === artifact.id));
  const rows: ReactNode[] = [];
  // The three stage owners' retry buttons are retired: only state and reason remain on the row.
  const row = (key: string, tone: StatePillTone, state: string, detail: string | undefined, note?: string) => (
    <div key={key} className="chain-failure-row">
      <StatePill tone={tone}>{state}</StatePill>
      {detail && <span className="text-caption text-muted">{detail}</span>}
      {note && <span className="text-caption text-muted">{note}</span>}
    </div>
  );
  if (task?.state === "failed") {
    const pill = downloadTaskPill(task);
    rows.push(row(`download_task:${task.id}`, pill.tone, pill.label, undefined));
  }
  for (const artifact of artifacts) {
    if (artifact.state !== "failed" && artifact.state !== "waiting_retry") continue;
    rows.push(row(`artifact:${artifact.id}`, artifactStateTone(artifact.state), artifactStateLabel(artifact.state), undefined));
  }
  for (const operation of operations) {
    if (operation.state !== "failed" && operation.state !== "waiting_retry") continue;
    // TARGET_OCCUPIED under the block policy is a deliberate skip, not a fault to diagnose: the
    // row names the policy (never the raw syscall text) so the stage — still retryable — never
    // reads as an unexplained refusal; the episode may well be held by the copy at the target.
    const skippedOccupied = operation.failure_code === "TARGET_OCCUPIED" && operation.collision_policy === "fail";
    rows.push(row(`file_operation:${operation.id}`, fileOperationStateTone(operation.state), fileOperationStateLabel(operation.state),
      `${fileOperationKindLabel(operation.operation)} · ${operation.target_path.replace(/\/+$/, "").split("/").pop() ?? operation.target_path}`,
      skippedOccupied ? t("rss.failureOccupiedSkipped") : undefined));
  }
  return rows;
}

function targetObservationPresentation(observation?: TargetObservation) {
  switch (observation?.state) {
    case "present_matched": return { label: t("rss.target.satisfied"), tone: "online" as const };
    case "present_mismatched": return { label: t("rss.target.mismatch"), tone: "warning" as const };
    case "unreadable": return { label: t("rss.target.unreadable"), tone: "offline" as const };
    // Not in the library yet is a plain state, not a problem: the seed row carries the download
    // stage, and only a real mismatch or a read failure takes an attention tone.
    default: return { label: t("rss.target.pending"), tone: "muted" as const };
  }
}

/** ③'s label for a file whose placement has ended: the row says what became of this one file, so a
 *  superseded file never reads "waiting to import" and never points at the file now holding the
 *  episode (that belongs to the anime's files). */
function placementEndLabel(end: NonNullable<RSSListFile["placement_end"]>) {
  switch (end.state) {
    case "released": return t("rss.placementEnd.released");
    case "displaced": return t(end.cause === "redownload" ? "rss.placementEnd.redownload" : "rss.placementEnd.overwrite");
    default: return t("rss.placementEnd.superseded");
  }
}

// Verdicts of "never eligible" in the plan: excluded at the filter/identify stage, producing no expected target.
const FILTERED_OUTCOMES: ReadonlySet<string> = new Set(["excluded_by_rule", "episode_unrecognized", "subtitle_unbound"]);
// Library-comparison verdicts that already existed at plan time: an expected target exists, but the target is already owned.
const PLAN_DIFF_OUTCOMES: ReadonlySet<string> = new Set(["covered_existing", "covered_in_flight", "target_collision"]);
// Verdicts of the two hard gates (subfolder, non-video/subtitle). Plan and download-directory
// cleanup still book by them, but they were never candidates: the manifest projection does not
// send them down as file rows, and the acquisition-parsed counterpart is not re-added here.
const HARD_GATE_OUTCOMES: ReadonlySet<string> = new Set(["ignored_nested", "ignored_kind"]);

/** The render-domain diagnostic code allowlist: may stand in as ②'s reason when there is no
 *  target. A render failure is not a landing failure — output rendering and execution
 *  failure/unreachable paths are strictly layered, and future probe/execution codes do not belong
 *  here: with no target they degrade into the neutral "target pending", leaving the verdict to ③ /
 *  the seed row. naming_* is the naming-domain prefix (e.g. naming_fact_missing_*); codes like
 *  naming_vars_unavailable equally state directly why there is no output. */
function isRenderDiagnostic(code: string): boolean {
  // The naming engine's codes are uppercase (NAMING_TARGET_NOT_ABSOLUTE) and landing diagnostics
//   keep their case; normalize uniformly before judging so case never decides whether a failure
//   may show as ②.
  const normalized = code.trim().toLowerCase();
  return (
    normalized === "target_render_failed" ||
    normalized === "target_not_absolute" ||
    normalized === "target_path_invalid" ||
    normalized === "target_entry_size_unknown" ||
    normalized.startsWith("naming_")
  );
}

// One file of an owner row. What used to be two segments (the preview layer's "seed files" and
// the acquisition layer's "target paths and file states") already corresponded one to one: the
// read model rebuilds the same file rows from the release's parsed files. After the merge, one
// file spans three rows by "which question the verdict answers", not by which package emitted the
// code:
//
//	① The original file: home of filter (screening/identification) — the file-facts badge, plus a
//	  fate badge when excluded.
//	② The expected output under the current configuration: home of rendering/naming — the current
//	  target path; a current failure reason only when rendering itself produced no target.
//	  Frozen admission targets are a compatibility fallback only when no current projection exists.
//	③ The actual output: home of landing/comparison — the observation badge, or the plan/landing
//	  failure's verdict.
//
// ① always exists; a file excluded by filter has no expectation, hence no ②③. A file whose render
// failed has no target to compare and no ③ either. Render and execution domains are strictly
// layered: only render failures (target_render_failed / naming_* etc., see isRenderDiagnostic) may
// occupy ② when there is no target; landing/probe failures (e.g. the library directory
// unavailable) are not rendering, go only to ③, and never overwrite ② — an absolute-path target
// never needed the library anyway.
//
// Successfully bound subtitles take no row of their own: they are a strip of format badges on
// their video's row (the shared BoundSubtitles), warning-colored with reason tips when
// same-name-not-kept or target-less, expanding to per-item source, size, language and target or
// reason. When the video is excluded the subtitles follow it with neutral badges. Only a subtitle
// bound to no video (subtitle_binding_missing) gets its own row with the filter reason.

/** The source of an owner file row: the preview layer's file row and the acquisition layer's parsed same file, merged by source path. */
type OwnerFileEntry = { path: string; preview?: RSSListFile; parsed?: ReleaseFile };

function OwnerReleaseFileRow({ preview, parsed, target, observation, subtitles = [] }: {
  preview?: RSSListFile;
  parsed?: ReleaseFile;
  target?: AcquisitionTarget;
  observation?: TargetObservation;
  /** Subtitle rows bound to this video: hung beneath the video (collapsed by default), never their own row. */
  subtitles?: OwnerFileEntry[];
}) {
  const sourcePath = parsed?.path ?? preview?.source_path ?? "";
  const filename = parsed?.filename ?? preview?.filename ?? sourcePath;
  const kind = parsed?.media_kind ?? preview?.media_kind;
  const size = knownSize(parsed?.size_bytes ?? preview?.size_bytes);
  const extension = fileExtension(filename);
  const language = parsed?.language ?? preview?.language;
  // ② shows the server's projection under the current configuration. Frozen admission facts stay
  // available for execution history, but cannot overwrite a current target or hide a current render
  // failure. Only an old row with no current projection and no current render error falls back to
  // its frozen target.
  const frozenTargetPath = parsed?.target_path ?? target?.target_path ?? undefined;
  const targetPath = preview?.rendered_target ?? (preview?.render_error ? undefined : frozenTargetPath);
  const targetNote = preview?.rendered_target ? emptyFactsNote(preview.empty_facts) : undefined;
  const outcome = parsed?.outcome ?? undefined;
  const diagnosticCode = parsed?.target_diagnostic?.code;
  // A duplicate target arrives as target_diagnostic but its semantics are a conflict (③), not a render failure (②).
  const duplicateConflict = diagnosticCode === "target_duplicate" || diagnosticCode === "target_replaced_in_release";
  // The admission layer (the accepted plan) is authoritative; only without a plan does it fall
//   back to the browse layer's per-file verdict, and only when that is absent too does it look for
//   an already-rendered target.
  const planned = outcome === undefined
    ? preview ? preview.included : targetPath !== undefined
    : outcome === "planned" || outcome === "subtitle_follows";
  const filtered = outcome === undefined ? preview !== undefined && !preview.included : FILTERED_OUTCOMES.has(outcome);
  const planDiff = duplicateConflict || (outcome !== undefined && PLAN_DIFF_OUTCOMES.has(outcome));
  // One judgment renders one badge: the admission layer's code wins, falling back to the browse
//   layer only when absent; both the badge word and the hover use this most specific code
//   (file_* under `excluded_by_rule`, the no-video-binding code under `subtitle_unbound`).
  const filterDetailCode = filtered ? (outcome === undefined ? preview?.reason_code : parsed?.outcome_reason ?? outcome) : undefined;
  const planDiffCode = duplicateConflict ? diagnosticCode : parsed?.outcome_reason ?? outcome;
  // ② carries only the expected output: a reason stands in here only when rendering produced no
//   target at all. Placeholder codes are limited to the render domain (isRenderDiagnostic): a
//   render failure is not a landing failure, and non-render diagnostics degrade into the neutral
//   "target pending" when there is no target, leaving the verdict to ③ / the seed row.
  const pendingDiagnostic = preview?.render_error ? preview.reason_code ?? "target_render_failed" : diagnosticCode;
  const renderFailure = planned && targetPath === undefined && pendingDiagnostic !== undefined && isRenderDiagnostic(pendingDiagnostic)
    ? pendingDiagnostic
    : undefined;
  // ③ carries the actual output: when a target exists but never landed (e.g. the library
//   directory unavailable), the verdict lives here and never overwrites ②. When a render failure
//   left no target, ③ stops repeating the error — with no landable target there is no "actual
//   output" to speak of. A duplicate target is likewise the landing side's conflict verdict and
//   goes through planDiff.
  const actualFailure = planned && targetPath !== undefined && !duplicateConflict ? diagnosticCode : undefined;
  const status = targetObservationPresentation(observation);
  // ③'s imported state follows the server's in_library: it judges from placement records, so a file
//   moved by alignment still counts as imported; an observation on the frozen target no longer
//   matches after the move and can no longer stand for "awaiting import".
  const inLibrary = preview?.in_library === true;
  // A file whose placement has ended (superseded, displaced or released): ③ states that one file's
//   own outcome. The row carries no location — this view renders the seed's result and the file's
//   state, and where files sit belongs to the anime's files dialog.
  const placementEnd = preview?.placement_end ?? undefined;
  // On a size mismatch the ③ row says where the difference lies: the actual size in the library
//   versus the file's expected size. When either side is unavailable only the state badge remains,
//   never a half sentence. The target is not repeated here — it is the same string as row ②.
  const observedSize = knownSize(observation?.observed_size_bytes);
  const mismatchSizes = !planDiff && observation?.state === "present_mismatched" && observedSize !== undefined && size !== undefined
    ? t("rss.target.mismatchSizes", { observed: formatBytes(observedSize), expected: formatBytes(size) })
    : undefined;
  return (
    <div className="chain-file">
      {/* ① The original file */}
      <div className="chain-file-row">
        {mediaIcon(kind)}
        <span className="chain-file-name text-caption">{sourcePath}</span>
        <span className="chain-file-badges">
          {parsed?.episode && <span className="chain-file-badge chain-file-badge-strong text-badge">{t("rss.episode", { n: parsed.episode })}</span>}
          {parsed?.resolution && <span className="chain-file-badge text-badge">{parsed.resolution}</span>}
          {parsed?.revision && <span className="chain-file-badge text-badge">{parsed.revision}</span>}
          {language && <span className="chain-file-badge text-badge">{language}</span>}
          {size !== undefined && <span className="chain-file-badge text-badge">{formatBytes(size)}</span>}
          {extension && <span className="chain-file-badge text-badge">{extension}</span>}
          {filterDetailCode !== undefined && (
            <span className="chain-file-badge chain-file-badge-excluded text-badge" title={knownReasonLabel(filterDetailCode) ?? t("common.unspecifiedReason")}>
              {releaseFileExcludedLabel(filterDetailCode)}
            </span>
          )}
        </span>
      </div>
      {/* ② The expected output under the current configuration: the server's current projection,
          plus each following subtitle's own current target. A current render failure must not be
          hidden by a frozen target; only old rows without a current projection fall back to their
          frozen admission target. Landing/probe failures never appear here — ② degrades into the
          neutral "target pending", leaving the verdict to ③. */}
      {planned && (renderFailure !== undefined
        ? <span className="text-caption text-danger" role="alert"><ReasonText code={renderFailure} /></span>
        : targetPath
          ? <>
              <span className="chain-file-path-pending text-caption" title={targetPath}>→ {targetPath}</span>
              {targetNote && <span className="text-caption text-muted">{targetNote}</span>}
            </>
          : <span className="text-caption text-muted">{t("rss.targetPending")}</span>)}
      {/* External subtitles bound to this video: format badges right below the video's target path, details collapsed by default; when the video is excluded the subtitles follow it. */}
      <BoundSubtitles videoName={sourcePath} subtitles={subtitles.map(ownerSubtitleItem)} videoExcluded={filtered} />
      {/* ③ The actual output */}
      {(placementEnd !== undefined || planDiff || (planned && (targetPath !== undefined || inLibrary))) && (
        <div className="chain-file-row">
          {placementEnd !== undefined
            ? <StatePill tone="muted">{placementEndLabel(placementEnd)}</StatePill>
            : planDiff
            ? <span className="text-caption text-muted">{planDiffCode === "target_collision_blocked"
                // A plan-time refusal only happens under the block policy, so the verdict names the
                // policy instead of leaving the occupied target to read like an unexplained skip.
                ? t("rss.planOccupiedSkipped")
                : <ReasonText code={planDiffCode} />}</span>
            : inLibrary
              ? <StatePill tone="online">{t("rss.target.inLibrary")}</StatePill>
            : actualFailure !== undefined
              ? <span className="text-caption text-danger"><ReasonText code={actualFailure} /></span>
              : <StatePill tone={status.tone}>{mismatchSizes ?? status.label}</StatePill>}
        </div>
      )}
    </div>
  );
}

// A subtitle's output is the target the server renders under the current configuration and the
// placement stage's same rule (the video's directory + the video's base name + the rendered suffix
// + its own extension). A current render error suppresses the frozen target; only a row without a
// current projection falls back to that historical admission fact. Passing and filtering match the
// video row: the admission layer's verdict wins, falling back to the browse layer only when absent.
function ownerSubtitleItem({ path, preview, parsed }: OwnerFileEntry): BoundSubtitleItem {
  const outcome = parsed?.outcome ?? undefined;
  const filtered = outcome === undefined ? preview !== undefined && !preview.included : FILTERED_OUTCOMES.has(outcome);
  // Same-name-not-kept arrives as target_diagnostic (the same source as the video row's duplicateConflict).
  const duplicate = parsed?.target_diagnostic?.code === "target_duplicate";
  const reasonCode = duplicate ? "target_duplicate" : filtered ? (outcome === undefined ? preview?.reason_code : parsed?.outcome_reason ?? outcome) : undefined;
  const pending = preview?.render_error ? namingPendingLabel(preview.reason_code) : undefined;
  const failure = preview?.render_error && !pending ? namingFailureDetail(preview.reason_code, preview.empty_facts) ?? preview.render_error : undefined;
  return {
    key: path,
    path,
    sizeBytes: parsed?.size_bytes ?? preview?.size_bytes,
    language: preview?.sub_lang_tag ?? parsed?.language ?? preview?.language,
    languageSource: preview?.sub_lang_tag ? preview.sub_lang_source : undefined,
    included: !filtered && !duplicate,
    reasonCode,
    target: preview?.rendered_target ?? (preview?.render_error ? undefined : parsed?.target_path ?? undefined),
    targetProblem: pending ?? failure,
  };
}

/** Accepts only a genuinely usable size: unknown (the field absent) and 0 both mean not known, rendering no badge. */
function knownSize(value: number | null | undefined): number | undefined {
  return typeof value === "number" && value > 0 ? value : undefined;
}

/** A file name's extension; undefined when there is none. */
function fileExtension(name: string): string | undefined {
  const separator = name.lastIndexOf(".");
  return separator > 0 && separator < name.length - 1 ? name.slice(separator + 1) : undefined;
}

function mediaIcon(kind?: string | null) {
  if (kind === "video") return <FileVideo className="icon-sm-fixed" />;
  if (kind === "subtitle") return <FileText className="icon-sm-fixed" />;
  return <File className="icon-sm-fixed" />;
}

function OwnerReleaseDetail({ item, release }: { item: RSSListItem; release?: Release }) {
  const serverFiles = item.files;
  const releaseFiles = release?.release_files ?? [];
  const targets = release?.acquisition_targets ?? [];
  const observations = release?.target_observations ?? [];
  const observationsByID = new Map(observations.map((observation) => [observation.target_id, observation]));
  const parsedByPath = new Map(releaseFiles.map((file) => [file.path, file]));
  const targetsByPath = new Map(targets.map((target) => [target.source_path, target]));
  // One file per row: the preview layer's file rows and the acquisition layer's parsed files already
//   correspond one to one, either side filling in gaps (the true merge comes from the two sides'
//   source paths). Files that failed a hard gate were never candidates — the server no longer sends
//   their rows, and the acquisition-parsed counterpart is not re-added.
  const ownerFiles: OwnerFileEntry[] = serverFiles.map((preview) => ({ path: preview.source_path, preview, parsed: parsedByPath.get(preview.source_path) }));
  for (const parsed of releaseFiles) {
    if (parsed.outcome && HARD_GATE_OUTCOMES.has(parsed.outcome)) continue;
    if (!serverFiles.some((file) => file.source_path === parsed.path)) ownerFiles.push({ path: parsed.path, parsed });
  }
  // Bound subtitles hang beneath their video (the binding comes down from the server), so they never
//   get their own row. The anchor must be a video row in the same list: a subtitle whose video is
//   missing is treated as unbound and stands as its own row.
  const rows = groupBoundSubtitles(ownerFiles, (entry) => entry.path, (entry) => entry.preview?.bound_video_source_path);
  return (
    <div className="chain-files">
      {rows.length > 0 && <>
        <p className="chain-files-label text-badge">{t("rss.seedFiles")}</p>
        {rows.map(({ file: { path, preview, parsed }, subtitles }) => {
          const target = targetsByPath.get(path);
          return <OwnerReleaseFileRow key={path} preview={preview} parsed={parsed} target={target} observation={target ? observationsByID.get(target.id) : undefined} subtitles={subtitles} />;
        })}
      </>}
    </div>
  );
}

function ownerItem(item: RSSListItem, release: Release | undefined, workspace: AnimeWorkspace, handlers: OwnerViewHandlers, preview: boolean, hold: RSSListView["metadata_hold"]): RssItemModel {
  const evaluation = item.evaluation;
  const verdict = evaluation.verdict;
  const reasonCode = evaluationReasonCode(evaluation.reason_code, evaluation.reason_detail);
  const filteredWithReason = evaluation.state === "evaluated" && verdict === "filtered" && !!reasonCode;
  const status = evaluation.state === "reevaluating"
    ? { label: t("verdict.reevaluating"), tone: "warning" as StatePillTone }
    : evaluation.state === "evaluated" && verdict
      ? { label: releaseVerdictLabelWithHold(verdict, hold, filteredWithReason ? tagFilterReasonLabel(evaluation.reason_code, evaluation.reason_detail) ?? reasonLabel(reasonCode) : undefined), tone: releaseVerdictTone(verdict) }
      : { label: evaluation.state === "not_selected" ? t("verdict.notSelected") : t("verdict.unparsed"), tone: "muted" as StatePillTone };
  const task = release?.download_task_id
    ? workspace.download_tasks.find((entry) => entry.id === release.download_task_id)
    : undefined;
  const retries = terminalRows(release, workspace);
  // Why the seed refuses to run again (the retry decision's stable reason), shown on the disabled
  // button: a release whose files were released from management reads 文件已解除管理 instead of the
  // bare label.
  const retryDenied = item.download_action.allowed
    ? undefined
    : reasonLabel(item.download_action.reason_code) ?? undefined;
  const actions = (
    <>
      {((handlers.onOpenRecord && release?.acquisition_id) || preview) && <Button size="sm" variant="ghost" {...actionProps(preview, () => release?.acquisition_id && handlers.onOpenRecord?.(release.acquisition_id), false, t("records.openFromRelease"))}><FileText className="icon-sm-fixed" /></Button>}
      {((handlers.onOpenStructure && release) || preview) && <Button size="sm" variant="ghost" {...actionProps(preview, () => release && handlers.onOpenStructure?.(release), false, t("rss.structure.open"))}><ListTree className="icon-sm-fixed" /></Button>}
      {(item.download_action.kind === "retry" || preview) && <Button size="sm" variant="secondary" {...actionProps(preview, () => release && handlers.onRetryRelease?.(release), !item.download_action.allowed || !handlers.onRetryRelease || (!!release && handlers.busyKey === `release:${release.id}`), t("rss.retryRelease"), retryDenied)}><RotateCcw className="icon-sm-fixed" aria-hidden />{t("rss.retryRelease")}</Button>}
    </>
  );
  return {
    key: `${item.snapshot_id}:${item.stable_seed_key}`,
    title: item.seed.title,
    // With tags, show the merged single tag row (the row keeps only the filter-participating ones, the rest go into the expansion); with none, fall back to the parsed quality/language.
    leadingMeta: item.seed.anibt_tags?.length || item.seed.title_tags?.length ? <TagBadges anibt={item.seed.anibt_tags} title={item.seed.title_tags} rules={workspace.rules?.effective_tag_filters} /> : (
      <>
        {evaluation.parsed_facts?.quality && <span className="text-caption text-muted">{evaluation.parsed_facts.quality}</span>}
        {evaluation.parsed_facts?.languages?.length ? <span className="text-caption text-muted">{evaluation.parsed_facts.languages.join("/")}</span> : null}
      </>
    ),
    publishedAt: item.seed.published_at ?? undefined,
    sizeBytes: item.seed.size_bytes,
    episodeLabel: releaseEpisodeLabel(evaluation.parsed_facts),
    status,
    filtered: evaluation.state === "evaluated" && verdict === "filtered",
    statusDetail: filteredWithReason || isReasonImpliedByVerdict(verdict, reasonCode) ? undefined : reasonCode ? <ReasonText code={reasonCode} /> : undefined,
    files: item.files.map((file) => ({
      key: file.file_ref,
      path: file.source_path,
      sizeBytes: file.size_bytes,
      mediaKind: file.media_kind,
      language: file.sub_lang_tag ?? file.language,
      languageSource: file.sub_lang_tag ? file.sub_lang_source : undefined,
      boundVideoPath: file.bound_video_source_path,
      included: file.included,
      reasonCode: file.reason_code,
      reason: describeReason(file.reason_code),
      target: file.rendered_target ?? undefined,
      targetNote: file.rendered_target ? emptyFactsNote(file.empty_facts) : undefined,
      targetError: file.render_error && !namingPendingLabel(file.reason_code) ? namingFailureDetail(file.reason_code, file.empty_facts) ?? file.render_error : undefined,
      targetPending: file.render_error ? namingPendingLabel(file.reason_code) : undefined,
    })),
    extras: task || retries.length > 0 ? <>
      {task && task.state !== "failed" && <StatePill tone={downloadTaskStateTone(task.state)}>{downloadTaskStateLabel(task.state)}</StatePill>}
      {retries}
    </> : undefined,
    actions,
    detail: <OwnerReleaseDetail item={item} release={release} />,
    manifestSummary: item.torrent.manifest ? manifestSummary(item) : undefined,
    manifestUnavailable: knownReasonLabel(item.torrent.error_code) ?? (item.torrent.state === "not_applicable" ? t("rss.manifestNotApplicable") : undefined),
  };
}

function ownerGroup(group: RSSListGroup, workspace: AnimeWorkspace, handlers: OwnerViewHandlers, preview: boolean, names: ReadonlyMap<string, string>, hold: RSSListView["metadata_hold"]): RssGroupModel {
  const displayName = groupLabel(names, group.scope.provider, group.scope.provider_group_ref, group.display_name);
  const sourceID = group.scope.rss_source_id;
  const source = sourceID ? workspace.rss_sources.find((candidate) => candidate.id === sourceID) : undefined;
  const runtime = sourceID ? workspace.rss_source_runtimes.find((candidate) => candidate.rss_source_id === sourceID) : undefined;
  // The score shares its source with the subgroups page: the effective selection's effective_score.
//   The owner read model's group.score is not a computed value (the owner path leaves it empty,
//   always 0) and is not read; groups without an effective selection (custom) show no score.
  const score = handlers.groupScores?.get(`${group.scope.provider}:${group.scope.provider_group_ref}`);
  const actions = (
    <span className="chain-source-actions">
      {(handlers.onOpenOffset && source || preview) && <Button size="sm" variant="ghost" {...actionProps(preview, () => source && handlers.onOpenOffset?.(source), !group.offset.editable || !handlers.onOpenOffset, t("rss.adjustOffset", { name: displayName }))}><Pencil className="icon-sm" /> {t("rss.offset", { n: group.offset.value })}</Button>}
      {((runtime && runtime.health !== "ready" && runtime.failed_work_item_id && handlers.onRetry) || preview) && <Button size="sm" variant="ghost" {...actionProps(preview, () => runtime?.failed_work_item_id && handlers.onRetry?.({ ownerType: "rss_source_runtime", ownerId: runtime.rss_source_id, failedWorkItemId: runtime.failed_work_item_id }), handlers.busyKey === `rss_source_runtime:${runtime?.rss_source_id}`, t("rss.retryRefresh"))}>{t("rss.retryRefresh")}</Button>}
    </span>
  );
  // Source runtime: the last successful refresh time is always shown; on a failed refresh the
//   failure reason is appended (the seed list itself keeps the previous content).
  const lastSuccess = formatTimeAgo(runtime?.last_success_at);
  const runtimeFailed = runtime !== undefined && (runtime.health === "degraded" || runtime.health === "failed");
  return {
    key: groupIdentity(group),
    displayName,
    meta: (
      <>
        {score !== undefined && <span className="text-caption text-muted">{t("rss.score", { n: score })}</span>}
        {lastSuccess && <span className="text-caption text-muted" title={formatDateTime(runtime?.last_success_at)}>{t("rss.lastSuccess", { time: lastSuccess })}</span>}
      </>
    ),
    feed: rssFeedState(group.feed.state),
    feedDetail: describeReason(group.feed.error_code)
      ?? (runtimeFailed ? describeReason(runtime.reason_code, runtime.reason_detail) ?? t("rss.refreshFailedNoReason") : undefined),
    actions,
    items: group.items.map((item) => ownerItem(item, releaseForItem(item, workspace), workspace, handlers, preview, hold)),
    emptyText: t("rss.emptyOwnerGroup"),
  };
}

/** Projects the authoritative owner RSS read model without rebuilding server decisions. */
export function ownerViewModel(view: RSSListView, workspace: AnimeWorkspace, handlers: OwnerViewHandlers): RssWorkspaceModel {
  const preview = view.mode === "owner_draft";
  const names = disambiguateGroupNames(view.groups.map(groupNameInput));
  return {
    groups: view.groups.map((group) => ownerGroup(group, workspace, handlers, preview, names, view.metadata_hold)),
    emptyText: view.groups.length === 0
      ? t("rss.emptyNoSources")
      : undefined,
  };
}
