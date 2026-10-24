import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ChevronDown,
  ChevronRight,
  Download,
  FileText,
  Pause,
  PlayCircle,
  RefreshCw,
  Server,
  Trash2,
  Upload,
} from "lucide-react";
import { toast } from "../../ui/toast";

import { cn } from "@/lib/utils";

import { Chip, SegmentedControl, StateBlock, StatePill, type StatePillTone } from "../../ui";
import { SegmentCountLabel } from "../../ui/SegmentCountLabel";
import { Button, Checkbox, Dialog, Progress, Select } from "../../ui/base";
import {
  type QBittorrentTorrent,
  type QBittorrentTorrentFile,
  type QBittorrentServerStatus,
  deleteQBittorrentTorrent,
  getQBittorrentServerStatus,
  getQBittorrentTorrentFiles,
  listConnections,
  listQBittorrentTorrents,
  pauseQBittorrentTorrent,
  resumeQBittorrentTorrent,
  toUserMessage,
} from "../../lib/api/client";
import { configKeys, qbManagementKeys } from "../../lib/queryKeys";
import { t } from "../../lib/i18n";
import { useRememberedSegment } from "../../lib/rememberedSegment";

// --- segments -------------------------------------------------------------------

/** Subscription / one-shot / other. */
export type QBSegment = "subscription" | "one_shot" | "other";
const QB_SEGMENTS = ["subscription", "one_shot", "other"] as const satisfies readonly QBSegment[];
const QB_SEGMENT_STORAGE_KEY = "bansoko.qb.segment";

/**
 * 任务属于哪个分段，只看服务端给出的 origin，不在前端推断。来源不明（unknown：带本应用的
 * 标记但下载记录已不在，或记录互相矛盾）不能证明属于订阅还是一次性，放进「其他」并标注
 * 「来源不明」，不默默归入任何一边。
 */
function segmentOf(torrent: QBittorrentTorrent): QBSegment {
  if (torrent.origin === "subscription") return "subscription";
  if (torrent.origin === "one_shot") return "one_shot";
  return "other";
}

// --- qBittorrent state label mapping -----------------------------------------

type QBStateMeta = { label: string; tone: StatePillTone; title?: string };

// Touch targets: row actions keep a 44px minimum hit area.
const touchTarget = "tw:min-h-11 tw:min-w-11";

// Raw qBittorrent states (4.x and 5.x names) grouped into the labels the UI
// shows. Download speed and remaining time only mean something while a task is
// fetching (see isFetching).
type QBStateGroup = "downloading" | "seeding" | "paused" | "waiting" | "queued" | "checking" | "moving" | "error";

const QB_STATE_GROUP: Record<string, QBStateGroup> = {
  downloading: "downloading",
  metaDL: "downloading",
  forcedMetaDL: "downloading",
  forcedDL: "downloading",
  allocating: "downloading",
  uploading: "seeding",
  stalledUP: "seeding",
  forcedUP: "seeding",
  pausedDL: "paused",
  pausedUP: "paused",
  stoppedDL: "paused",
  stoppedUP: "paused",
  stalledDL: "waiting",
  queuedDL: "queued",
  queuedUP: "queued",
  checkingDL: "checking",
  checkingUP: "checking",
  checkingResumeData: "checking",
  moving: "moving",
  error: "error",
  missingFiles: "error",
};

const QB_GROUP_TONE: Record<QBStateGroup, StatePillTone> = {
  downloading: "online",
  seeding: "info",
  paused: "muted",
  waiting: "warning",
  queued: "muted",
  checking: "muted",
  moving: "muted",
  error: "offline",
};

function qbStateGroup(state: string): QBStateGroup | undefined {
  return Object.hasOwn(QB_STATE_GROUP, state) ? QB_STATE_GROUP[state] : undefined;
}

function qbStateMeta(state: string): QBStateMeta {
  const group = qbStateGroup(state);
  if (!group) return { label: t("qb.state.unknown"), tone: "muted", title: state || undefined };
  return { label: t(`qb.state.${group}`), tone: QB_GROUP_TONE[group] };
}

/** Download speed and ETA only describe a task that is still fetching data. */
function isFetching(state: string): boolean {
  const group = qbStateGroup(state);
  return group === "downloading" || group === "waiting";
}

// --- formatting helpers -------------------------------------------------------

/** Byte count, or undefined when qBittorrent did not report one (-1 = unknown). */
function formatBytes(bytes?: number | null): string | undefined {
  if (bytes === undefined || bytes === null || bytes < 0) return undefined;
  if (bytes === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  return `${(bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

/** Normalizes the app version so a leading "v" is never duplicated. */
function formatVersion(version?: string | null): string | undefined {
  if (!version) return undefined;
  return version.startsWith("v") ? version : `v${version}`;
}

function formatSpeed(bytesPerSec?: number | null): string | undefined {
  const size = formatBytes(bytesPerSec);
  return size === undefined ? undefined : `${size}/s`;
}

// qBittorrent reports 8640000 (100 days) when the ETA is unknown.
const QB_ETA_UNKNOWN = 8640000;

function formatEta(seconds?: number | null): string | undefined {
  if (seconds === undefined || seconds === null || seconds <= 0 || seconds >= QB_ETA_UNKNOWN) return undefined;
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const secs = Math.floor(seconds % 60);
  if (days > 0) return t("qb.eta.days", { d: days, h: hours });
  if (hours > 0) return t("qb.eta.hours", { h: hours, m: minutes });
  if (minutes > 0) return t("qb.eta.minutes", { m: minutes, s: secs });
  return t("qb.eta.seconds", { s: secs });
}

/** Whole percent, rounded down so 99.6% never reads as finished. */
function progressPercent(progress?: number | null): number {
  return Math.floor(Math.min(Math.max(progress ?? 0, 0), 1) * 100);
}

/** Newest first; qBittorrent returns tasks in no stable order. */
function byNewest(a: QBittorrentTorrent, b: QBittorrentTorrent): number {
  return (b.added_on ?? 0) - (a.added_on ?? 0) || a.name.localeCompare(b.name);
}

// --- main dialog --------------------------------------------------------------

export function QBManagerDialog({
  open,
  onOpenChange,
  onOpenSettings,
  segment: routeSegment,
  onSegmentChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Opens the settings section that configures downloaders (empty state action). */
  onOpenSettings?: () => void;
  /** The link's segment; when absent, the browser's last choice, defaulting to subscription. */
  segment?: QBSegment;
  onSegmentChange?: (segment: QBSegment) => void;
}) {
  const queryClient = useQueryClient();
  const [segment, setSegment] = useRememberedSegment({
    storageKey: QB_SEGMENT_STORAGE_KEY,
    values: QB_SEGMENTS,
    routeValue: routeSegment,
    onRouteChange: onSegmentChange,
  });

  // Resolve configured qBittorrent connections.
  const connectionsQuery = useQuery({
    queryKey: configKeys.connections,
    queryFn: listConnections,
    enabled: open,
  });

  const qbConnections = useMemo(() => {
    const all = connectionsQuery.data ?? [];
    return all.filter((item) => {
      // Discriminated union: check the `kind` field on the resolved variant.
      const kind = (item as { kind?: string }).kind;
      return kind === "qbittorrent";
    });
  }, [connectionsQuery.data]);

  const [selectedConnectionId, setSelectedConnectionId] = useState<string | null>(null);
  const connectionId = selectedConnectionId ?? qbConnections[0]?.id ?? null;

  const [expandedHash, setExpandedHash] = useState<string | null>(null);
  const [pendingDelete, setPendingDelete] = useState<{ hash: string; name: string } | null>(null);
  const [deleteFiles, setDeleteFiles] = useState(false);

  useEffect(() => {
    if (!open) {
      setPendingDelete(null);
      setDeleteFiles(false);
    }
  }, [open]);

  const torrentsQuery = useQuery({
    queryKey: qbManagementKeys.torrents(connectionId),
    queryFn: () => listQBittorrentTorrents(connectionId!),
    enabled: open && connectionId !== null,
    refetchInterval: open && connectionId ? 3000 : false,
  });

  const statusQuery = useQuery({
    queryKey: qbManagementKeys.status(connectionId),
    queryFn: () => getQBittorrentServerStatus(connectionId!),
    enabled: open && connectionId !== null,
    refetchInterval: open && connectionId ? 3000 : false,
  });

  const filesQuery = useQuery({
    queryKey: qbManagementKeys.files(connectionId, expandedHash),
    queryFn: () => getQBittorrentTorrentFiles(connectionId!, expandedHash!),
    enabled: open && connectionId !== null && expandedHash !== null,
  });

  const invalidateAll = () => {
    void queryClient.invalidateQueries({ queryKey: qbManagementKeys.all });
  };

  const pauseMutation = useMutation({
    mutationFn: ({ hash }: { hash: string }) => pauseQBittorrentTorrent(connectionId!, hash),
    onSuccess: invalidateAll,
    onError: (cause) => toast.error(toUserMessage(cause, t("qb.pauseFailed"))),
  });

  const resumeMutation = useMutation({
    mutationFn: ({ hash }: { hash: string }) => resumeQBittorrentTorrent(connectionId!, hash),
    onSuccess: invalidateAll,
    onError: (cause) => toast.error(toUserMessage(cause, t("qb.resumeFailed"))),
  });

  const deleteMutation = useMutation({
    mutationFn: ({ hash, deleteFiles: del }: { hash: string; deleteFiles: boolean }) =>
      deleteQBittorrentTorrent(connectionId!, hash, del),
    onSuccess: () => {
      toast.success(t("qb.deleted"));
      setPendingDelete(null);
      setDeleteFiles(false);
      invalidateAll();
    },
    onError: (cause) => {
      toast.error(toUserMessage(cause, t("qb.deleteFailed")));
      // A 409 QB_TORRENT_IN_USE means an acquisition claimed the torrent after
      // the list was read; refetch so the owner tag and disabled state show.
      invalidateAll();
    },
  });

  const torrents = useMemo(() => [...(torrentsQuery.data ?? [])].sort(byNewest), [torrentsQuery.data]);
  // The downloader returns the full list (unpaginated); segments are split and counted by the server-provided origin, updating with the 3s polling.
  const segmentCounts = useMemo(() => {
    const counts: Record<QBSegment, number> = { subscription: 0, one_shot: 0, other: 0 };
    for (const torrent of torrents) counts[segmentOf(torrent)] += 1;
    return counts;
  }, [torrents]);
  const visibleTorrents = useMemo(() => torrents.filter((torrent) => segmentOf(torrent) === segment), [torrents, segment]);
  const segmentOptions = QB_SEGMENTS.map((value) => ({
    value,
    label: <SegmentCountLabel label={t(`qb.segment.${value}`)} count={torrentsQuery.data ? segmentCounts[value] : undefined} />,
  }));
  const changeSegment = (value: QBSegment) => {
    setSegment(value);
    setExpandedHash(null);
  };
  const status = statusQuery.data;
  const busy = pauseMutation.isPending || resumeMutation.isPending || deleteMutation.isPending;
  // The list polls, so the confirmation follows ownership changes while it is open.
  const pendingDeleteBlocked =
    pendingDelete !== null && torrents.some((item) => item.hash === pendingDelete.hash && item.acquisition_owner);
  // When qB is unreachable both queries fail with the same message; the task
  // list notice reports it once and its retry refetches both.
  const statusErrorInBar = statusQuery.error && !torrentsQuery.error;
  const showStatusBar = qbConnections.length > 1 || statusErrorInBar || (!statusQuery.error && status);
  const retryConnection = () => {
    void statusQuery.refetch();
    void torrentsQuery.refetch();
  };

  return (
    <Dialog
      open={open}
      onRequestClose={() => onOpenChange(false)}
      tier="R"
      focusTitle
      title={
        <span className="tw:inline-flex tw:items-center tw:gap-2">
          <PlayCircle size={14} />
          <span>{t("qb.title")}</span>
        </span>
      }
      description={t("qb.description")}
    >
      <div className="tw:flex tw:min-h-0 tw:flex-col">
        {/* Connection selector + server status bar */}
        {connectionsQuery.isLoading ? (
          <StateBlock kind="busy" title={t("qb.loadingConnections")} />
        ) : connectionsQuery.error ? (
          <StateBlock kind="error"
            title={toUserMessage(connectionsQuery.error, t("qb.loadConnectionsFailed"))}
            action={
              <Button onClick={() => connectionsQuery.refetch()}>
                <RefreshCw size={14} />
                {t("qb.retry")}
              </Button>
            }
          />
        ) : qbConnections.length === 0 ? (
          // No downloader yet (UI review G1/Q4): the entry stays and says so, with the way to fix it.
          <StateBlock
            kind="empty"
            title={t("qb.notConfigured")}
            description={t("qb.notConfiguredHint")}
            action={
              onOpenSettings ? (
                <Button variant="primary" onClick={onOpenSettings}>
                  {t("qb.goSettings")}
                </Button>
              ) : undefined
            }
          />
        ) : !showStatusBar ? null : (
          <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-3 tw:rounded-lg tw:border tw:border-border tw:bg-muted/30 tw:px-3 tw:py-2">
            {qbConnections.length > 1 && (
              <div className="tw:max-w-full tw:min-w-48">
                <Select
                  label={t("qb.selectLabel")}
                  value={connectionId ?? ""}
                  onChange={(value) => {
                    setSelectedConnectionId(value || null);
                    setExpandedHash(null);
                  }}
                  options={qbConnections.map((conn) => {
                    const named = conn as { name?: string; base_url?: string };
                    return { value: conn.id, label: named.name || named.base_url || conn.id };
                  })}
                />
              </div>
            )}

            {statusErrorInBar ? (
              <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-2" role="alert">
                <span className="tw:text-sm tw:text-destructive">
                  {toUserMessage(statusQuery.error, t("qb.loadStatusFailed"))}
                </span>
                <Button size="sm" onClick={() => statusQuery.refetch()}>
                  <RefreshCw size={14} />
                  {t("qb.retry")}
                </Button>
              </div>
            ) : !statusQuery.error && status ? (
              <ServerStatusBar status={status} />
            ) : null}
          </div>
        )}

        {/* Torrent list */}
        {qbConnections.length > 0 && (
          <div className="tw:min-h-0">
            {torrentsQuery.isLoading ? (
              <StateBlock kind="busy" title={t("qb.loadingTasks")} />
            ) : torrentsQuery.error ? (
              <StateBlock kind="error"
                title={toUserMessage(torrentsQuery.error, t("qb.loadTasksFailed"))}
                action={
                  <Button onClick={retryConnection}>
                    <RefreshCw size={14} />
                    {t("qb.retry")}
                  </Button>
                }
              />
            ) : torrents.length === 0 ? (
              <StateBlock kind="empty" title={t("qb.noTasks")} />
            ) : (
              <>
              <SegmentedControl<QBSegment>
                ariaLabel={t("qb.segment.aria")}
                className="tw:mt-3 tw:justify-self-start"
                value={segment}
                onChange={changeSegment}
                options={segmentOptions}
              />
              {visibleTorrents.length === 0 ? (
                <StateBlock
                  kind="empty"
                  className="tw:mt-3"
                  title={t(`qb.segmentEmpty.${segment}`)}
                  description={t(`qb.segmentEmpty.${segment}Hint`)}
                />
              ) : (
              <ul className="tw:m-0 tw:block tw:list-none tw:p-0" aria-label={t("qb.taskList")}>
                {visibleTorrents.map((torrent) => (
                  <TorrentRow
                    key={torrent.hash}
                    torrent={torrent}
                    expanded={expandedHash === torrent.hash}
                    onToggleExpand={() =>
                      setExpandedHash((prev) => (prev === torrent.hash ? null : torrent.hash))
                    }
                    files={filesQuery.data}
                    filesLoading={expandedHash === torrent.hash && filesQuery.isLoading}
                    filesError={expandedHash === torrent.hash ? filesQuery.error : undefined}
                    onPause={() => pauseMutation.mutate({ hash: torrent.hash })}
                    onResume={() => resumeMutation.mutate({ hash: torrent.hash })}
                    onDelete={() => setPendingDelete({ hash: torrent.hash, name: torrent.name })}
                    busy={busy}
                  />
                ))}
              </ul>
              )}
              </>
            )}
          </div>
        )}
      </div>

      <Dialog
        open={open && pendingDelete !== null}
        onRequestClose={() => {
          setPendingDelete(null);
          setDeleteFiles(false);
        }}
        tier="S"
        title={pendingDelete ? t("qb.deleteTitle", { name: pendingDelete.name }) : ""}
        footer={
          <>
            <Button
              onClick={() => {
                setPendingDelete(null);
                setDeleteFiles(false);
              }}
            >
              {t("qb.cancel")}
            </Button>
            <Button
              variant="danger"
              loading={deleteMutation.isPending}
              disabled={pendingDelete === null || pendingDeleteBlocked}
              onClick={() => {
                if (!pendingDelete) return;
                deleteMutation.mutate({ hash: pendingDelete.hash, deleteFiles });
              }}
            >
              {t("qb.delete")}
            </Button>
          </>
        }
      >
        <div className="tw:grid tw:w-full tw:gap-3 tw:text-sm">
          <span>{t("qb.removeFromClient")}</span>
          <Checkbox checked={deleteFiles} onCheckedChange={setDeleteFiles} disabled={pendingDeleteBlocked}>
            {t("qb.deleteFiles")}
          </Checkbox>
          {pendingDeleteBlocked && <span className="tw:text-muted-foreground">{t("qb.deleteBlocked")}</span>}
        </div>
      </Dialog>
    </Dialog>
  );
}

// --- server status bar --------------------------------------------------------

function ServerStatusBar({ status }: { status: QBittorrentServerStatus }) {
  // qBittorrent connection_status values: connected | firewalled | disconnected.
  const state = status.connection_status === "connected" || status.connection_status === "firewalled" ? "connected" : status.connection_status;
  const version = formatVersion(status.version);
  const down = formatSpeed(status.dl_speed);
  const up = formatSpeed(status.up_speed);
  const free = formatBytes(status.free_space);
  return (
    <div className="tw:text-xs" role="status">
      <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-x-4 tw:gap-y-1">
        {version && (
          <span className="tw:inline-flex tw:items-center tw:gap-1.5">
            <Server size={14} />
            <span>{version}</span>
          </span>
        )}
        <StatePill tone={state === "connected" ? "online" : "offline"}>
          {state === "connected"
            ? status.connection_status === "firewalled"
              ? t("qb.connectedFirewalled")
              : t("qb.connected")
            : t("qb.disconnected")}
        </StatePill>
        {down && (
          <span className="tw:inline-flex tw:items-center tw:gap-1.5">
            <Download size={14} />
            <span>↓{down}</span>
          </span>
        )}
        {up && (
          <span className="tw:inline-flex tw:items-center tw:gap-1.5">
            <Upload size={14} />
            <span>↑{up}</span>
          </span>
        )}
        {free && <span>{t("qb.available", { size: free })}</span>}
      </div>
    </div>
  );
}

// --- torrent row --------------------------------------------------------------

function TorrentRow({
  torrent,
  expanded,
  onToggleExpand,
  files,
  filesLoading,
  filesError,
  onPause,
  onResume,
  onDelete,
  busy,
}: {
  torrent: QBittorrentTorrent;
  expanded: boolean;
  onToggleExpand: () => void;
  files?: QBittorrentTorrentFile[];
  filesLoading: boolean;
  filesError?: unknown;
  onPause: () => void;
  onResume: () => void;
  onDelete: () => void;
  busy: boolean;
}) {
  const meta = qbStateMeta(torrent.state);
  const progressPct = progressPercent(torrent.progress);
  const isPaused = qbStateGroup(torrent.state) === "paused";
  const fetching = isFetching(torrent.state);
  const size = formatBytes(torrent.size);
  const down = fetching ? formatSpeed(torrent.dlspeed) : undefined;
  const eta = fetching ? formatEta(torrent.eta) : undefined;
  const up = torrent.upspeed && torrent.upspeed > 0 ? formatSpeed(torrent.upspeed) : undefined;
  // Present only while a running subscription or one-shot download still needs the torrent.
  const owner = torrent.acquisition_owner;
  // The segment already says who created the task; the owner tag shows only when it says something else.
  const ownerTag = owner && owner !== torrent.origin ? owner : undefined;
  const blockedReasonId = `qb-delete-blocked-${torrent.hash}`;

  return (
    <li className="tw:border-b tw:border-border tw:py-3 tw:last:border-b-0">
      <div className="tw:flex tw:min-w-0 tw:items-start tw:gap-2">
        <Button
          variant="ghost"
          size="sm"
          className={cn(touchTarget, "tw:flex-none")}
          onClick={onToggleExpand}
          aria-label={expanded ? t("qb.collapseFiles") : t("qb.expandFiles")}
          aria-expanded={expanded}
        >
          {expanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
        </Button>

        <div className="tw:min-w-0 tw:flex-1">
          <div className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-x-2 tw:gap-y-1">
            {/* The name keeps at least 12rem: on a phone the tags wrap below it instead of squeezing it to a few
                letters. min-w-0 carries the truncation; the full name is in the title attribute. */}
            <span className="tw:min-w-0 tw:flex-[1_1_12rem] tw:truncate" title={torrent.name}>
              {torrent.name}
            </span>
            <StatePill tone={meta.tone} title={meta.title}>{meta.label}</StatePill>
            {ownerTag && <Chip>{t(`qb.owner.${ownerTag}`)}</Chip>}
            {torrent.origin === "unknown" && (
              <Chip title={t("qb.originUnknownHint")}>
                {t("qb.originUnknown")}
              </Chip>
            )}
          </div>
          <div className="tw:mt-1 tw:flex tw:flex-wrap tw:gap-x-3 tw:gap-y-0.5 tw:text-xs tw:text-muted-foreground">
            {size && <span>{t("qb.size", { size })}</span>}
            {down && <span>↓{down}</span>}
            {up && <span>↑{up}</span>}
            {eta && <span>{t("qb.remaining", { eta })}</span>}
            {owner && <span id={blockedReasonId}>{t("qb.deleteBlocked")}</span>}
          </div>
        </div>

        <span className="tw:flex tw:flex-none tw:gap-0.5">
          {isPaused ? (
            <Button variant="ghost" size="sm" className={touchTarget} onClick={onResume} disabled={busy} aria-label={t("qb.resume")}>
              <PlayCircle size={14} />
            </Button>
          ) : (
            <Button variant="ghost" size="sm" className={touchTarget} onClick={onPause} disabled={busy} aria-label={t("qb.pause")}>
              <Pause size={14} />
            </Button>
          )}
          <Button
            variant="ghost"
            size="sm"
            className={cn(touchTarget, "tw:text-destructive")}
            onClick={onDelete}
            disabled={busy || owner !== undefined}
            title={owner ? t("qb.deleteBlocked") : undefined}
            aria-describedby={owner ? blockedReasonId : undefined}
            aria-label={t("qb.deleteAria", { name: torrent.name })}
          >
            <Trash2 size={14} />
          </Button>
        </span>
      </div>

      <div className="tw:mt-2 tw:pl-8">
        <Progress value={progressPct} label={t("qb.progressAria", { name: torrent.name })} tone={progressPct >= 100 ? "success" : "default"} />
      </div>

      {/* Expanded file list */}
      {expanded && (
        <div className="tw:mt-2 tw:pl-8">
          {filesLoading ? (
            <StateBlock kind="busy" title={t("qb.loadingFiles")} />
          ) : filesError ? (
            <span role="alert" className="tw:text-sm tw:text-destructive">
              {toUserMessage(filesError, t("qb.loadFilesFailed"))}
            </span>
          ) : files && files.length > 0 ? (
            <ul
              className="tw:m-0 tw:list-none tw:rounded-md tw:border tw:border-border/60 tw:p-0"
              aria-label={t("qb.filesAria")}
            >
              {files.map((file) => (
                <li key={file.name} className="tw:border-t tw:border-border/60 tw:px-2 tw:py-1.5 tw:first:border-t-0">
                  <div className="tw:flex tw:w-full tw:min-w-0 tw:items-center tw:gap-2 tw:text-xs">
                    <FileText size={14} className="tw:flex-none" />
                    <span className="tw:min-w-0 tw:flex-1 tw:truncate" title={file.name}>
                      {file.name}
                    </span>
                    <span className="tw:flex-none tw:tabular-nums">{formatBytes(file.size)}</span>
                    <span className="tw:w-10 tw:flex-none tw:text-right tw:tabular-nums">
                      {progressPercent(file.progress)}%
                    </span>
                  </div>
                </li>
              ))}
            </ul>
          ) : (
            <span className="tw:text-sm tw:text-muted-foreground">{t("qb.noFileInfo")}</span>
          )}
        </div>
      )}
    </li>
  );
}
