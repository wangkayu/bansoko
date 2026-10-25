import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ScanSearch } from "lucide-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "../../ui/toast";
import {
  ApiError,
  alignAnimeFiles,
  listAnimeFiles,
  newUUID,
  recoverAnimeFile,
  refreshAnimeMetadata,
  releasePlacement,
  retryRelease,
  submitReconciliationScan,
  toUserMessage,
  type AnimeFile,
  type AnimeFileAction,
  type AnimeFileCompanion,
  type AnimeFilesView,
} from "../../lib/api/client";
import { SegmentedControl, StatePill, type StatePillTone } from "../../ui";
import { SegmentCountLabel } from "../../ui/SegmentCountLabel";
import { Button, Dialog } from "../../ui/base";
import { fileMaintenanceKeys } from "../../lib/queryKeys";
import { formatBytes, formatDateTime } from "../../lib/labels";
import { t } from "../../lib/i18n";
import { withNote } from "../../lib/i18n/format";
import { reasonLabel } from "../../lib/i18n/reasons";
import { showReleaseRetryResult } from "../anime-tabs/releaseRetryResult";

// The per-anime "files" dialog (opened from a subscribed card's menu, a standalone dialog and not a
//   tab inside the config dialog; a retired subscription opens read-only): one row per imported
//   video, attachments nested beneath it; row states, the new path awaiting alignment, and each
//   row's available actions are all decided by the server — this side only renders. Scanning starts
//   inside the dialog (the RSS tab's scan action opens it and starts there) and polls while in
//   flight.
//   Not a global file manager: no browsing, no renaming, no deletion.
//
// The list: grouped by subgroups (group_name comes from the server; entries without one fall into
//   "other"), with group headers showing the file count and the awaiting-alignment/problem counts;
//   season and episode are not shown, only alignment matters. A row leads with the file name,
//   followed by its directory (a library-relative path); awaiting alignment, the new name and the
//   directory change render as diff lines. Problem rows sort first within a group, awaiting
//   alignment next. The filter (all / awaiting alignment / problems) only changes the display and
//   never the server's verdict.

/** The polling interval while a scan or alignment is in flight. */
const RUNNING_POLL_MS = 2_000;

/** These reasons mean the action does not apply to the row: the button is absent, not disabled. */
const NOT_APPLICABLE = new Set(["nothing_to_align", "file_not_missing"]);

type FileStatus = AnimeFile["status"] | AnimeFileCompanion["status"];

const STATUS_TONE: Record<FileStatus, StatePillTone> = {
  unscanned: "muted",
  aligned: "online",
  misaligned: "info",
  missing: "warning",
  changed: "warning",
  size_mismatch: "warning",
  no_target: "warning",
};

function reasonText(code: string | undefined): string {
  return code ? (reasonLabel(code) ?? "") : "";
}

function statusLabel(status: FileStatus, reason?: string): string {
  const label = t(`tabs.files.status.${status}`);
  const note = reasonText(reason);
  return note ? withNote(label, note) : label;
}

/** An action button: shown when available, absent when not applicable, otherwise disabled with the reason in the tooltip. */
function ActionButton(props: {
  action: AnimeFileAction;
  label: string;
  busy: boolean;
  disabled: boolean;
  variant?: "ghost";
  onClick: () => void;
}) {
  const { action, label, busy, disabled, variant, onClick } = props;
  if (!action.available && NOT_APPLICABLE.has(action.reason_code ?? "")) return null;
  return (
    <Button
      size="sm"
      variant={variant}
      loading={busy}
      disabled={disabled || !action.available}
      title={action.available ? undefined : reasonText(action.reason_code) || undefined}
      onClick={onClick}
    >
      {label}
    </Button>
  );
}

type PathInput = Pick<AnimeFile, "relative_path" | "target_relative_path" | "alignment"> & { status: FileStatus };

/** Splits a relative path into directory and file name (file name only when the directory is empty). */
function splitPath(path: string): { folder: string; name: string } {
  const at = path.lastIndexOf("/");
  return at < 0 ? { folder: "", name: path } : { folder: path.slice(0, at), name: path.slice(at + 1) };
}

/** The new position while awaiting alignment: a changed name shows the new name; a changed directory shows "old directory → new directory". */
function pendingMove(file: PathInput): { name?: string; folder?: { from: string; to: string } } | undefined {
  if (file.status !== "misaligned" || !file.target_relative_path) return undefined;
  const current = splitPath(file.relative_path);
  const target = splitPath(file.target_relative_path);
  return {
    name: target.name !== current.name ? target.name : undefined,
    folder: target.folder !== current.folder ? { from: current.folder, to: target.folder } : undefined,
  };
}

function AlignmentPill({ file }: { file: PathInput }) {
  if (file.alignment?.state === "in_progress") return <StatePill busy>{t("tabs.files.aligning")}</StatePill>;
  if (file.alignment?.state === "failed") {
    return (
      <StatePill tone="offline">
        {file.alignment.reason_code
          ? t("tabs.files.alignFailedBecause", { reason: reasonText(file.alignment.reason_code) })
          : t("tabs.files.alignFailed")}
      </StatePill>
    );
  }
  return null;
}

/** The file name (primary) plus the new name; the directory (secondary) or the directory-change diff line. */
function FilePath({ file, size, compact = false }: { file: PathInput; size?: number; compact?: boolean }) {
  const { folder, name } = splitPath(file.relative_path);
  const move = pendingMove(file);
  return (
    <div className="tw:grid tw:min-w-0 tw:gap-0.5">
      <span className={`${compact ? "tw:text-sm" : "tw:text-sm tw:font-medium"} tw:wrap-anywhere`} title={file.relative_path}>
        {name}
        {move?.name ? <span className="tw:font-normal tw:text-primary"> → {move.name}</span> : null}
      </span>
      <span className="tw:flex tw:flex-wrap tw:items-baseline tw:gap-x-2 tw:text-xs tw:text-muted-foreground">
        {move?.folder ? (
          <span className="tw:wrap-anywhere" title={file.target_relative_path}>
            {move.folder.from || "/"} → <span className="tw:text-primary">{move.folder.to || "/"}</span>
          </span>
        ) : folder ? (
          <span className="tw:wrap-anywhere">{folder}</span>
        ) : null}
        {size !== undefined ? <span className="tw:tabular-nums">{formatBytes(size)}</span> : null}
      </span>
    </div>
  );
}

const PROBLEM_STATUSES = new Set<FileStatus>(["missing", "changed", "no_target", "size_mismatch"]);

/** Problem: missing, content changed, no new path renderable, an attachment's size changed, or alignment failed. */
function hasProblem(file: AnimeFile): boolean {
  return (
    PROBLEM_STATUSES.has(file.status) ||
    file.alignment?.state === "failed" ||
    file.companions.some((companion) => PROBLEM_STATUSES.has(companion.status) || companion.alignment?.state === "failed")
  );
}

/** Awaiting alignment: the video or any attachment is not at the position the current naming rules render. */
function isPending(file: AnimeFile): boolean {
  return file.status === "misaligned" || file.companions.some((companion) => companion.status === "misaligned");
}

function rowRank(file: AnimeFile): number {
  if (hasProblem(file)) return 0;
  if (isPending(file)) return 1;
  return file.status === "unscanned" ? 2 : 3;
}

type FileFilter = "all" | "pending" | "problem";

type FileGroup = { key: string; name: string; files: AnimeFile[]; pending: number; problems: number };

/** Groups by subgroup; entries without one fall into "other" and sort last. Within a group problems come first, awaiting alignment next. */
function groupFiles(files: AnimeFile[]): FileGroup[] {
  const groups = new Map<string, FileGroup>();
  for (const file of files) {
    const key = file.group_name?.trim() ?? "";
    let group = groups.get(key);
    if (!group) {
      group = { key, name: key, files: [], pending: 0, problems: 0 };
      groups.set(key, group);
    }
    group.files.push(file);
    if (hasProblem(file)) group.problems += 1;
    if (isPending(file)) group.pending += 1;
  }
  for (const group of groups.values()) {
    group.files.sort((a, b) => rowRank(a) - rowRank(b) || a.relative_path.localeCompare(b.relative_path));
  }
  return [...groups.values()].sort((a, b) => {
    if (a.key === "" || b.key === "") return a.key === "" ? 1 : -1;
    return a.name.localeCompare(b.name);
  });
}

function groupSummary(group: FileGroup): string {
  const parts = [t("tabs.files.groupFiles", { n: group.files.length })];
  if (group.pending > 0) parts.push(t("tabs.files.groupPending", { n: group.pending }));
  if (group.problems > 0) parts.push(t("tabs.files.groupProblems", { n: group.problems }));
  return parts.join(" · ");
}

/** An unavailable 409 reason code surfaces only through the copy catalog. */
function deniedReason(cause: unknown, code: string): string | undefined {
  return cause instanceof ApiError && cause.code === code ? cause.details?.[0]?.code : undefined;
}

function FileRow(props: {
  file: AnimeFile;
  readOnly: boolean;
  onChanged: () => void;
  align: (placementIDs?: string[]) => Promise<void>;
  aligning: boolean;
  recover: (placementID: string, generation: number) => Promise<void>;
}) {
  const { file, readOnly, onChanged, align, aligning, recover } = props;
  const [busy, setBusy] = useState<"align" | "rerun" | "release" | "recover">();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [recoverOpen, setRecoverOpen] = useState(false);

  async function runAlign() {
    if (busy) return;
    setBusy("align");
    try {
      await align([file.placement_id]);
    } finally {
      setBusy(undefined);
    }
  }
  async function rerun() {
    if (busy || !file.release_id) return;
    setBusy("rerun");
    try {
      showReleaseRetryResult(await retryRelease(file.release_id, newUUID()));
      onChanged();
    } catch (cause) {
      const reason = deniedReason(cause, "RELEASE_NOT_RETRYABLE");
      toast.error(reason ? t("rss.releaseRetry.blocked", { reason: reasonText(reason) }) : toUserMessage(cause, t("rss.releaseRetry.failed")));
    } finally {
      setBusy(undefined);
    }
  }
  async function release() {
    if (busy) return;
    setBusy("release");
    try {
      await releasePlacement(file.placement_id, newUUID());
      toast.success(t("policy.fileEffect.released"));
      setConfirmOpen(false);
      onChanged();
    } catch (cause) {
      const reason = deniedReason(cause, "PLACEMENT_EXIT_UNAVAILABLE");
      toast.error(reason ? t("policy.fileEffect.exitBlocked", { reason: reasonText(reason) }) : toUserMessage(cause, t("policy.fileEffect.exitFailed")));
    } finally {
      setBusy(undefined);
    }
  }

  async function runRecover() {
    if (busy || !file.recovery) return;
    setBusy("recover");
    try {
      await recover(file.placement_id, file.recovery.generation);
      setRecoverOpen(false);
      onChanged();
    } finally {
      setBusy(undefined);
    }
  }

  const locked = busy !== undefined || aligning;
  const problem = hasProblem(file);
  const pending = !problem && isPending(file);
  const accent = problem ? "tw:border-l-warning" : pending ? "tw:border-l-primary" : "tw:border-l-transparent";
  return (
    <li
      className={`tw:grid tw:gap-1.5 tw:border-b tw:border-l-2 tw:border-border tw:py-2 tw:pl-2 ${accent}`}
      data-placement-id={file.placement_id}
      data-state={problem ? "problem" : pending ? "pending" : file.status}
    >
      <div className="tw:flex tw:flex-wrap tw:items-start tw:gap-2">
        <div className="tw:min-w-0 tw:flex-1">
          <FilePath file={file} size={file.size_bytes} />
        </div>
        <span className="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
          <StatePill tone={STATUS_TONE[file.status]}>{statusLabel(file.status, file.status_reason)}</StatePill>
          <AlignmentPill file={file} />
        </span>
        {readOnly ? null : (
          <span className="tw:flex tw:flex-wrap tw:gap-2">
            <ActionButton action={file.actions.align} label={t("tabs.files.align")} busy={busy === "align"} disabled={locked} onClick={() => void runAlign()} />
            <ActionButton action={file.actions.rerun} label={t("tabs.files.rerun")} busy={busy === "rerun"} disabled={locked} onClick={() => void rerun()} />
            <ActionButton
              action={file.actions.release}
              label={t("policy.fileEffect.releasePlacement")}
              busy={false}
              disabled={locked}
              variant="ghost"
              onClick={() => setConfirmOpen(true)}
            />
            <ActionButton
              action={file.actions.recover}
              label={t("policy.fileEffect.recover")}
              busy={busy === "recover"}
              disabled={locked}
              onClick={() => setRecoverOpen(true)}
            />
          </span>
        )}
      </div>
      {file.companions.length > 0 ? (
        <ul className="tw:m-0 tw:grid tw:list-none tw:gap-1 tw:pl-4">
          {file.companions.map((companion) => (
            <li key={companion.id} className="tw:flex tw:flex-wrap tw:items-start tw:gap-2">
              <span className="tw:text-xs tw:text-muted-foreground tw:pt-0.5">
                {companion.media_kind === "subtitle" ? t("policy.fileEffect.companionSubtitle") : t("policy.fileEffect.companionOther")}
              </span>
              <div className="tw:min-w-0 tw:flex-1">
                <FilePath file={companion} size={companion.size_bytes} compact />
              </div>
              {companion.status !== "aligned" ? <StatePill tone={STATUS_TONE[companion.status]}>{statusLabel(companion.status)}</StatePill> : null}
              <AlignmentPill file={companion} />
            </li>
          ))}
        </ul>
      ) : null}
      <Dialog
        open={confirmOpen}
        tier="S"
        title={t("policy.fileEffect.releaseConfirmTitle")}
        onRequestClose={() => setConfirmOpen(false)}
        footer={
          <>
            <Button onClick={() => setConfirmOpen(false)}>{t("act.cancel")}</Button>
            <Button variant="primary" loading={busy === "release"} onClick={() => void release()}>
              {t("policy.fileEffect.releaseConfirm")}
            </Button>
          </>
        }
      >
        <p className="tw:m-0 tw:text-sm">{t("policy.fileEffect.releaseConfirmBody")}</p>
        <p className="tw:m-0 tw:text-sm tw:wrap-anywhere">{file.path}</p>
      </Dialog>
      {file.recovery ? (
        <Dialog
          open={recoverOpen}
          tier="S"
          title={t("policy.fileEffect.recoverConfirmTitle")}
          onRequestClose={() => setRecoverOpen(false)}
          footer={
            <>
              <Button onClick={() => setRecoverOpen(false)}>{t("act.cancel")}</Button>
              <Button variant="primary" loading={busy === "recover"} onClick={() => void runRecover()}>
                {t("policy.fileEffect.recover")}
              </Button>
            </>
          }
        >
          <p className="tw:m-0 tw:text-sm">{t("policy.fileEffect.recoverConfirmBody")}</p>
          <p className="tw:m-0 tw:text-sm tw:wrap-anywhere">{t("policy.fileEffect.recoverTarget", { path: file.recovery.target_path })}</p>
          {file.recovery.companions > 0 && !file.recovery.companions_complete ? (
            <p className="tw:m-0 tw:text-sm tw:text-muted-foreground">
              {t("policy.fileEffect.recoverCompanionsGap", { n: file.recovery.companions - file.recovery.companions_present })}
            </p>
          ) : file.recovery.companions > 0 ? (
            <p className="tw:m-0 tw:text-sm tw:text-muted-foreground">
              {t("policy.fileEffect.recoverCompanions", { present: file.recovery.companions_present, n: file.recovery.companions })}
            </p>
          ) : null}
        </Dialog>
      ) : null}
    </li>
  );
}

export function AnimeFilesDialog({
  animeID,
  title,
  readOnly,
  scanOnOpen = false,
  onChanged,
  onClose,
}: {
  animeID: string;
  /** The anime title (the dialog title). */
  title: string;
  /** A retired subscription: read-only, with no scan and no actions shown. */
  readOnly: boolean;
  /** Starts one scan on open (the RSS tab's scan action). */
  scanOnOpen?: boolean;
  /** The caller's re-read after an action completes, or a scan/alignment finishes (workspace / catalog). */
  onChanged: () => void;
  onClose: () => void;
}) {
  return (
    <Dialog open tier="M" title={t("tabs.files.dialogTitle", { title })} onRequestClose={onClose}>
      <AnimeFilesPanel animeID={animeID} readOnly={readOnly} scanOnOpen={scanOnOpen} onChanged={onChanged} />
    </Dialog>
  );
}

function AnimeFilesPanel({
  animeID,
  readOnly,
  scanOnOpen,
  onChanged,
}: {
  animeID: string;
  readOnly: boolean;
  scanOnOpen: boolean;
  onChanged: () => void;
}) {
  const queryClient = useQueryClient();
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string>();
  const [alignBusy, setAlignBusy] = useState(false);
  const files = useQuery({
    queryKey: fileMaintenanceKeys.files(animeID),
    queryFn: () => listAnimeFiles(animeID),
    refetchInterval: (query) => {
      const data = query.state.data;
      return data && (data.scan.state === "running" || data.aligning) ? RUNNING_POLL_MS : false;
    },
  });
  const data: AnimeFilesView | undefined = files.data;
  const [filter, setFilter] = useState<FileFilter>("all");
  const allFiles = data?.files;
  const { groups, pendingCount, problemCount } = useMemo(() => {
    const list = allFiles ?? [];
    const visible = list.filter((file) => (filter === "pending" ? isPending(file) : filter === "problem" ? hasProblem(file) : true));
    return { groups: groupFiles(visible), pendingCount: list.filter(isPending).length, problemCount: list.filter(hasProblem).length };
  }, [allFiles, filter]);
  const scanRunning = submitting || data?.scan.state === "running";
  const working = scanRunning || data?.aligning === true;

  // When a scan or alignment moves from in-flight to finished, the caller re-reads the workspace: the
//   RSS row's file state is refreshed by that same scan. Not finished until the result has been
//   read.
  const hasData = data !== undefined;
  const wasWorking = useRef(false);
  useEffect(() => {
    if (!working && !hasData) return;
    if (wasWorking.current && !working) onChanged();
    wasWorking.current = working;
  }, [working, hasData, onChanged]);

  const refresh = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: fileMaintenanceKeys.files(animeID) });
  }, [queryClient, animeID]);

  const rowChanged = useCallback(() => {
    refresh();
    onChanged();
  }, [refresh, onChanged]);

  const canScan = !readOnly;
  const scan = useCallback(async () => {
    if (submitting || !canScan) return;
    setSubmitting(true);
    setSubmitError(undefined);
    try {
      await submitReconciliationScan(animeID);
    } catch (cause) {
      // A scan is already in flight: follow that one's result and do not count it as a failure.
      if (!(cause instanceof ApiError && cause.code === "SCAN_IN_PROGRESS")) {
        setSubmitError(toUserMessage(cause, t("rss.scanFailed")));
      }
    } finally {
      setSubmitting(false);
      refresh();
    }
  }, [submitting, canScan, animeID, refresh]);

  // One more scan after an alignment finishes: the previous scan recorded
  // pre-move paths, so without rescanning the rows keep describing where the
  // files used to be. The trigger is the server's own rescan_required fact,
  // not the in-flight flag: an alignment can start and settle between two
  // polls, and a client watching for the falling edge then never rescans.
  const aligning = data?.aligning === true;
  const rescanRequired = data?.scan.rescan_required === true && !aligning && !scanRunning;
  const rescanRequested = useRef<string>("");
  useEffect(() => {
    if (!hasData || !rescanRequired) return;
    // The generation is what the flag is about: a rescan of the same
    // generation is requested once, so the poll that follows the new scan (a
    // newer generation, before its alignment settles) never loops.
    const generation = String(data?.scan.generation ?? "");
    if (rescanRequested.current === generation) return;
    rescanRequested.current = generation;
    void scan();
  }, [rescanRequired, hasData, data?.scan.generation, scan]);
  // The RSS tab's scan action: start one scan on open (a StrictMode replay never double-submits).
  const scannedOnOpen = useRef(false);
  useEffect(() => {
    if (!scanOnOpen || scannedOnOpen.current) return;
    scannedOnOpen.current = true;
    void scan();
  }, [scanOnOpen, scan]);

  const align = useCallback(
    async (placementIDs?: string[]) => {
      if (!data) return;
      try {
        const result = await alignAnimeFiles(animeID, data.scan.generation, placementIDs);
        if (result.admitted > 0) toast.success(t("tabs.files.alignAdmitted", { n: result.admitted }));
        else toast.info(t("tabs.files.alignNothing"));
      } catch (cause) {
        const reason = deniedReason(cause, "ANIME_FILES_ALIGN_UNAVAILABLE");
        toast.error(reason ? t("tabs.files.alignBlocked", { reason: reasonText(reason) }) : toUserMessage(cause, t("tabs.files.alignFailed")));
      } finally {
        refresh();
      }
    },
    [data, animeID, refresh],
  );

  // The explicit recovery confirm: the server re-verifies every frozen fact and
  // re-attaches the placement; a refusal is a typed 409 whose reason code the
  // copy catalog renders. The success toast confirms the adoption; the re-read
  // then shows the row aligned.
  const recover = useCallback(
    async (placementID: string, generation: number) => {
      try {
        await recoverAnimeFile(animeID, placementID, generation);
        toast.success(t("policy.fileEffect.recovered"));
      } catch (cause) {
        const reason = deniedReason(cause, "ANIME_FILES_RECOVER_REFUSED");
        toast.error(reason ? t("policy.fileEffect.recoverBlocked", { reason: reasonText(reason) }) : toUserMessage(cause, t("policy.fileEffect.recoverFailed")));
      } finally {
        refresh();
      }
    },
    [animeID, refresh],
  );
  // The 对齐（刷新元数据）variant: re-pull the bound works bypassing the positive
  // caches first, so the stale episode titles the alignment preview renders are
  // gone before the owner aligns; then re-compute the rows in place from the
  // fresh cache and only then admit the alignment. A failed re-pull is a typed
  // offline reason and the alignment is never admitted on top of stale values.
  const alignWithRefresh = useCallback(
    async (placementIDs?: string[]) => {
      if (!data) return;
      try {
        await refreshAnimeMetadata(animeID);
      } catch (cause) {
        toast.error(toUserMessage(cause, t("rss.metadataRefreshFailed")));
        refresh();
        return;
      }
      await align(placementIDs);
    },
    [data, animeID, align, refresh],
  );

  async function alignAllWithRefresh() {
    if (alignBusy) return;
    setAlignBusy(true);
    try {
      await alignWithRefresh();
    } finally {
      setAlignBusy(false);
    }
  }

  async function alignAll() {
    if (alignBusy) return;
    setAlignBusy(true);
    try {
      await align();
    } finally {
      setAlignBusy(false);
    }
  }

  const failure =
    !scanRunning && data?.scan.state === "failed"
      ? data.scan.failure_code
        ? withNote(t("rss.scanFailed"), reasonText(data.scan.failure_code))
        : t("rss.scanFailed")
      : undefined;
  const scanLine = data?.scan.scanned_at ? t("tabs.files.lastScan", { time: formatDateTime(data.scan.scanned_at) }) : t("tabs.files.unscanned");
  const alignAllAction = data?.align_all_action;
  const showAlignAll = alignAllAction && !(alignAllAction.available === false && NOT_APPLICABLE.has(alignAllAction.reason_code ?? ""));

  return (
    <div className="tw:grid tw:gap-3">
      <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
        {scanRunning ? <StatePill busy>{t("act.scanning")}</StatePill> : <span className="tw:text-sm tw:text-muted-foreground">{scanLine}</span>}
        {data?.aligning && !scanRunning ? <StatePill busy>{t("tabs.files.aligning")}</StatePill> : null}
        {canScan || (!readOnly && showAlignAll) ? (
          <span className="tw:ml-auto tw:flex tw:flex-wrap tw:gap-2">
            {!readOnly && showAlignAll && alignAllAction ? (
              <>
                <Button
                  size="sm"
                  loading={alignBusy}
                  disabled={!alignAllAction.available || alignBusy || working}
                  title={alignAllAction.available ? undefined : reasonText(alignAllAction.reason_code) || undefined}
                  onClick={() => void alignAllWithRefresh()}
                >
                  {t("tabs.files.alignRefresh")}
                </Button>
                <Button
                  size="sm"
                  loading={alignBusy}
                  disabled={!alignAllAction.available || alignBusy || working}
                  title={alignAllAction.available ? undefined : reasonText(alignAllAction.reason_code) || undefined}
                  onClick={() => void alignAll()}
                >
                  {t("tabs.files.alignAll")}
                </Button>
              </>
            ) : null}
            {canScan ? (
              <Button
                size="sm"
                disabled={scanRunning || (data !== undefined && !data.scan_action.available)}
                title={data && !data.scan_action.available ? reasonText(data.scan_action.reason_code) || undefined : undefined}
                onClick={() => void scan()}
              >
                <ScanSearch className={scanRunning ? "chain-refresh-icon chain-refresh-icon-spin" : "chain-refresh-icon"} />
                {scanRunning ? t("act.scanning") : t("tabs.files.scan")}
              </Button>
            ) : null}
          </span>
        ) : null}
      </div>
      {submitError ? <p role="alert" className="policy-card-error">{submitError}</p> : null}
      {failure ? <p role="alert" className="policy-card-error">{failure}</p> : null}
      {data?.root ? <p className="tw:m-0 tw:text-xs tw:text-muted-foreground tw:wrap-anywhere">{t("tabs.files.root", { path: data.root })}</p> : null}
      {files.isLoading ? <StatePill busy>{t("tabs.files.reading")}</StatePill> : null}
      {files.error ? <StatePill tone="offline">{toUserMessage(files.error, t("tabs.files.readFailed"))}</StatePill> : null}
      {data && data.files.length === 0 ? <p className="tw:m-0 tw:text-sm tw:text-muted-foreground">{t("tabs.files.empty")}</p> : null}
      {data && data.files.length > 0 ? (
        <>
          <SegmentedControl
            compact
            ariaLabel={t("tabs.files.filterAria")}
            value={filter}
            onChange={setFilter}
            options={[
              { value: "all", label: <SegmentCountLabel label={t("tabs.files.filter.all")} count={data.files.length} /> },
              { value: "pending", label: <SegmentCountLabel label={t("tabs.files.filter.pending")} count={pendingCount} /> },
              { value: "problem", label: <SegmentCountLabel label={t("tabs.files.filter.problem")} count={problemCount} /> },
            ]}
          />
          {groups.length === 0 ? <p className="tw:m-0 tw:text-sm tw:text-muted-foreground">{t("tabs.files.filterEmpty")}</p> : null}
          <ul className="tw:m-0 tw:grid tw:list-none tw:gap-3 tw:p-0" aria-label={t("tabs.files.title")}>
            {groups.map((group) => {
              const name = group.key === "" ? t("tabs.files.otherGroup") : group.name;
              return (
                <li key={group.key || "__other"} className="tw:grid tw:gap-1">
                  <div className="tw:flex tw:flex-wrap tw:items-baseline tw:gap-2 tw:border-b tw:border-border tw:pb-1">
                    <span className="tw:text-sm tw:font-semibold">{name}</span>
                    <span className="tw:text-xs tw:text-muted-foreground">{groupSummary(group)}</span>
                  </div>
                  <ul className="tw:m-0 tw:list-none tw:p-0" aria-label={name}>
                    {group.files.map((file) => (
                      <FileRow
                        key={file.placement_id}
                        file={file}
                        readOnly={readOnly}
                        onChanged={rowChanged}
                        align={align}
                        aligning={alignBusy}
                        recover={recover}
                      />
                    ))}
                  </ul>
                </li>
              );
            })}
          </ul>
        </>
      ) : null}
    </div>
  );
}
