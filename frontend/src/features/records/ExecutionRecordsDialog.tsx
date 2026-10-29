import { useState, type ReactNode } from "react";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ApiError,
  getAcquisitionRecord,
  listAcquisitionRecordFiles,
  listAcquisitionRecords,
  toUserMessage,
  type AcquisitionConfigHold,
  type AcquisitionDisplayValue,
  type AcquisitionEntryKind,
  type AcquisitionFile,
  type AcquisitionRecordDetail,
  type AcquisitionRecordOrigin,
  type AcquisitionRecordState,
  type AcquisitionRecordSummary,
  type AcquisitionStage,
  type AcquisitionStageName,
  type AcquisitionStageState,
  type AcquisitionTransfer,
} from "../../lib/api/client";
import { formatBytes, formatDateTime } from "../../lib/labels";
import { useRememberedSegment } from "../../lib/rememberedSegment";
import { acquisitionRecordKeys, rssListKeys } from "../../lib/queryKeys";
import { useBoundedRefresh } from "../../lib/events/useBoundedRefresh";
import { t, tryT } from "../../lib/i18n";
import { labelValue } from "../../lib/i18n/format";
import { Alert, Button, Dialog, Field, Select, StatePill, Chip, type SelectOption, StateBlock, type StatePillTone } from "../../ui/base";
import { DescriptionList } from "../../ui/DescriptionList";
import { SegmentedControl } from "../../ui";
import { SegmentCountLabel } from "../../ui/SegmentCountLabel";
import { ReasonText } from "../../ui/ReasonText";
import { episodesRangeLabel } from "../rss-list/model";
import { TagBadges } from "../rss-list/TagBadges";
import { providerLabel } from "../../lib/providers/registry";
import { seedIdentityParts } from "./seedIdentity";
import { ImportRetryConfirmDialog } from "./ImportRetryConfirmDialog";
import { DownloadCancelConfirmDialog } from "./DownloadCancelConfirmDialog";
import { configHoldText } from "./configHold";

// ExecutionRecordsDialog — the read-only list and details of execution records (accepted
//   acquisitions). Route-carried (`?record=list|<id>`, see shell/routeDialogs.ts), full screen on
//   phones. Exactly one repair entry exists: "retry" when the import stage fails and the server
//   reports import_retry_available (the same for subscriptions and one-shot downloads), re-running
//   every failed file; any other repair is a re-enqueue and never happens here. The one other action
//   is "cancel download" on the download stage while the server reports cancel_available. Display text uses
//   only server-provided safe values; hidden or unavailable values show a placeholder and are
//   never reconstructed on the frontend. While open it refreshes with live events
//   (rss/records invalidating ["acquisition-records"]), falling back to the low-frequency
//   useBoundedRefresh polling when disconnected.

const PAGE_LIMIT = 50;
const FILE_PAGE_LIMIT = 100;

const LIST = "tw:m-0 tw:grid tw:list-none tw:gap-2 tw:p-0";
const ITEM = "tw:grid tw:gap-1 tw:rounded-[10px] tw:border tw:border-border tw:bg-card tw:px-3 tw:py-2.5 tw:text-sm";
const ITEM_BUTTON = `${ITEM} tw:w-full tw:cursor-pointer tw:text-left tw:hover:border-primary tw:focus-visible:outline-2 tw:focus-visible:outline-ring`;
const META = "tw:m-0 tw:text-xs tw:text-muted-foreground";
const ITEM_HEAD = "tw:flex tw:flex-wrap tw:items-center tw:gap-x-2 tw:gap-y-1";
const SECTION_TITLE = "tw:m-0 tw:mb-2 tw:text-sm tw:font-semibold";

type Tone = "online" | "warning" | "offline" | "info" | "muted";

const STATE_TONE: Record<AcquisitionRecordState, Tone> = {
  running: "info",
  succeeded: "online",
  failed: "offline",
  cancelled: "muted",
};

const STAGE_TONE: Record<AcquisitionStageState, Tone> = {
  pending: "muted",
  running: "info",
  succeeded: "online",
  failed: "offline",
  blocked: "warning",
  cancelled: "muted",
  waiting_config: "warning",
};

type StateFilter = "" | AcquisitionRecordState;
type EntryFilter = "" | AcquisitionEntryKind;

/** Subscription / one-shot: split by entry type on the server (auto-download and download-on-subscribe both belong to the subscription side). */
const RECORD_SEGMENTS = ["subscription", "one_shot"] as const satisfies readonly AcquisitionRecordOrigin[];
const RECORD_SEGMENT_STORAGE_KEY = "bansoko.records.segment";

const stateOptions = (): SelectOption<StateFilter>[] => [
  { value: "", label: t("records.filter.all") },
  { value: "running", label: t("records.state.running") },
  { value: "succeeded", label: t("records.state.succeeded") },
  { value: "failed", label: t("records.state.failed") },
  { value: "cancelled", label: t("records.state.cancelled") },
];

// Within the "subscription" segment there is a further subdivision by entry; the "one-shot" segment has a single entry type and never shows this filter.
const entryOptions = (): SelectOption<EntryFilter>[] => [
  { value: "", label: t("records.filter.all") },
  { value: "automatic", label: t("records.entry.automatic") },
  { value: "subscription_add", label: t("records.entry.subscription_add") },
];

function stateLabel(state: AcquisitionRecordState): string {
  return tryT(`records.state.${state}`) ?? state;
}

function entryLabel(kind: AcquisitionEntryKind): string {
  return tryT(`records.entry.${kind}`) ?? kind;
}

function stageLabel(stage: AcquisitionStageName): string {
  return tryT(`records.stage.${stage}`) ?? stage;
}

function stageStateLabel(state: string): string {
  return tryT(`records.stageState.${state}`) ?? state;
}

/** A server-safe value: shown as-is when visible; a placeholder only when hidden or unavailable. */
function displayText(value: AcquisitionDisplayValue | undefined): string {
  if (value?.state === "visible" && value.value) return value.value;
  return value?.state === "redacted" ? t("records.redacted") : t("records.untitled");
}

/**
 * What this record is about: when the server supplies the frozen plan's episodes, "episode N /
 *   episodes 01-03"; otherwise the torrent title. Episode numbers come only from the server and
 *   are never parsed out of the title.
 */
function recordSubject(item: Pick<AcquisitionRecordSummary, "episodes" | "release">): string | undefined {
  const episodes = item.episodes;
  if (episodes && episodes.length === 1) return t("records.episode", { n: episodes[0] });
  if (episodes && episodes.length > 1) return t("records.episodes", { range: episodesRangeLabel(episodes) });
  if (item.release?.title.state === "visible" && item.release.title.value) return item.release.title.value;
  return undefined;
}

/** Downloading progress and speed, from the download sync's most recent observation. */
function transferText(transfer: AcquisitionTransfer): string {
  const percent = Math.floor((transfer.completed_bytes / transfer.total_bytes) * 100);
  const progress = t("records.transfer.progress", { percent: Math.min(100, Math.max(0, percent)) });
  const speed = typeof transfer.download_speed === "number" ? formatBytes(transfer.download_speed) : undefined;
  return speed === undefined ? progress : `${progress} · ${t("records.transfer.speed", { speed })}`;
}

function RecordStateTag({ state }: { state: AcquisitionRecordState }) {
  return <StatePill tone={STATE_TONE[state] ?? "muted"}>{stateLabel(state)}</StatePill>;
}

/**
 * The location summary for list rows and the detail header: the stage while running (with progress and speed while
 *   downloading, or what the configuration lacks while the import waits for it), or the failed stage and reason at a
 *   terminal state.
 */
function Position({
  stage,
  stageState,
  reasonCode,
  configHold,
  transfer,
}: {
  stage?: AcquisitionStageName;
  stageState?: string;
  reasonCode?: string;
  configHold?: AcquisitionConfigHold;
  transfer?: AcquisitionTransfer;
}) {
  if (!stage || !stageState) return null;
  return (
    <span className={META}>
      {t("records.atStage", { stage: stageLabel(stage), state: stageStateLabel(stageState) })}
      {transfer ? ` · ${transferText(transfer)}` : null}
      {configHold ? ` · ${configHoldText(configHold)}` : null}
      {reasonCode ? (
        <>
          {" · "}
          <ReasonText code={reasonCode} />
        </>
      ) : null}
    </span>
  );
}

export function ExecutionRecordsDialog({
  open,
  record,
  animeFilter,
  segment,
  onSegmentChange,
  onOpenRecord,
  onShowList,
  onClearAnime,
  onOpenAnime,
  onClose,
}: {
  open: boolean;
  /** `list` or the id of one execution record. */
  record: string;
  /** The list filtered by anime (managed anime id). */
  animeFilter?: string;
  /** The link's segment (subscription / one-shot); when absent, the browser's last choice, defaulting to subscription. */
  segment?: AcquisitionRecordOrigin;
  onSegmentChange?: (segment: AcquisitionRecordOrigin) => void;
  onOpenRecord: (acquisitionID: string) => void;
  onShowList: () => void;
  onClearAnime: () => void;
  onOpenAnime: (managedAnimeID: string) => void;
  onClose: () => void;
}) {
  const showList = record === "list";
  useBoundedRefresh(open ? [acquisitionRecordKeys.all] : []);
  return (
    <Dialog open={open} onRequestClose={onClose} tier="R" focusTitle title={t("records.title")}>
      {showList ? (
        <RecordList
          animeFilter={animeFilter}
          segment={segment}
          onSegmentChange={onSegmentChange}
          onOpenRecord={onOpenRecord}
          onClearAnime={onClearAnime}
        />
      ) : (
        <RecordDetail acquisitionID={record} onShowList={onShowList} onOpenAnime={onOpenAnime} />
      )}
    </Dialog>
  );
}

function RecordList({
  animeFilter,
  segment: routeSegment,
  onSegmentChange,
  onOpenRecord,
  onClearAnime,
}: {
  animeFilter?: string;
  segment?: AcquisitionRecordOrigin;
  onSegmentChange?: (segment: AcquisitionRecordOrigin) => void;
  onOpenRecord: (acquisitionID: string) => void;
  onClearAnime: () => void;
}) {
  const [segment, setSegment] = useRememberedSegment({
    storageKey: RECORD_SEGMENT_STORAGE_KEY,
    values: RECORD_SEGMENTS,
    routeValue: routeSegment,
    onRouteChange: onSegmentChange,
  });
  const [state, setState] = useState<StateFilter>("");
  const [entry, setEntry] = useState<EntryFilter>("");
  // The entry subdivision belongs only to the "subscription" segment.
  const entryKind = segment === "subscription" ? entry : "";
  const query = useInfiniteQuery({
    queryKey: acquisitionRecordKeys.list({ managedAnimeID: animeFilter, state, entryKind, origin: segment }),
    queryFn: ({ pageParam }) =>
      listAcquisitionRecords({
        origin: segment,
        managed_anime_id: animeFilter || undefined,
        state: state || undefined,
        entry_kind: entryKind || undefined,
        limit: PAGE_LIMIT,
        cursor: pageParam,
      }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor ?? undefined,
    // Keeps the previous counts while switching segments so the numbers on segment labels do not flicker.
    placeholderData: (previous) => previous,
  });
  const fresh = query.isPlaceholderData ? undefined : query.data;
  const items = fresh?.pages.flatMap((page) => page.items) ?? [];
  // Both segments' counts arrive in the same response (independent of segment and entry subdivision); take the newest page's.
  const counts = query.data?.pages.at(-1)?.origin_counts;
  const animeTitle = items.find((item) => item.anime?.managed_anime_id === animeFilter)?.anime?.title;
  const filtered = Boolean(animeFilter || state || entryKind);
  const segmentOptions = RECORD_SEGMENTS.map((value) => ({
    value,
    label: <SegmentCountLabel label={t(`records.segment.${value}`)} count={counts?.[value]} />,
  }));

  return (
    <div className="tw:grid tw:gap-3">
      <SegmentedControl<AcquisitionRecordOrigin>
        ariaLabel={t("records.segment.aria")}
        className="tw:justify-self-start"
        value={segment}
        onChange={setSegment}
        options={segmentOptions}
      />
      <div className="tw:grid tw:grid-cols-2 tw:gap-3">
        <Field label={t("records.filter.state")}>
          <Select<StateFilter> label={t("records.filter.state")} value={state} options={stateOptions()} onChange={setState} />
        </Field>
        {segment === "subscription" ? (
          <Field label={t("records.filter.entry")}>
            <Select<EntryFilter> label={t("records.filter.entry")} value={entry} options={entryOptions()} onChange={setEntry} />
          </Field>
        ) : null}
      </div>
      {animeFilter ? (
        <div className={ITEM_HEAD}>
          <Chip>{animeTitle ? t("records.filter.anime", { title: animeTitle }) : t("records.filter.animeFallback")}</Chip>
          <Button size="sm" variant="link" onClick={onClearAnime}>
            {t("records.filter.clearAnime")}
          </Button>
        </div>
      ) : null}
      {query.error ? (
        <StateBlock kind="error"
          title={toUserMessage(query.error, t("records.loadFailed"))}
          action={
            <Button size="sm" variant="link" onClick={() => void query.refetch()}>
              {t("log.retry")}
            </Button>
          }
        />
      ) : !fresh ? (
        <StateBlock kind="busy" title={t("records.loading")} />
      ) : items.length === 0 ? (
        filtered ? (
          <StateBlock kind="empty" title={t("records.emptyFiltered")} />
        ) : (
          <StateBlock
            kind="empty"
            title={t(`records.segmentEmpty.${segment}`)}
            description={t(`records.segmentEmpty.${segment}Hint`)}
          />
        )
      ) : (
        <>
          <ul aria-label={t("records.listAria")} className={LIST}>
            {items.map((item) => (
              <RecordRow
                key={item.acquisition_id}
                item={item}
                showEntry={segment === "subscription"}
                onOpen={() => onOpenRecord(item.acquisition_id)}
              />
            ))}
          </ul>
          {query.hasNextPage ? (
            <Button
              size="sm"
              className="tw:justify-self-center"
              loading={query.isFetchingNextPage}
              onClick={() => void query.fetchNextPage()}
            >
              {t("records.loadMore")}
            </Button>
          ) : null}
        </>
      )}
    </div>
  );
}

function RecordRow({ item, showEntry, onOpen }: { item: AcquisitionRecordSummary; showEntry: boolean; onOpen: () => void }) {
  const title = displayText(item.source_title);
  // A subscription download's name is the anime title; when they match, no second line.
  const animeTitle = item.anime && item.anime.title !== title ? item.anime.title : undefined;
  const subject = recordSubject(item);
  const identity = seedIdentityParts(item.seed).join(" · ");
  const torrentTitle = item.release?.title.state === "visible" && item.release.title.value ? item.release.title.value : undefined;
  return (
    <li>
      <button type="button" className={ITEM_BUTTON} onClick={onOpen}>
        <div className={ITEM_HEAD}>
          <RecordStateTag state={item.state} />
          {showEntry ? <Chip>{entryLabel(item.entry_kind)}</Chip> : null}
          {item.replaces_existing ? <Chip>{t("records.replacement")}</Chip> : null}
          <span className={META}>{formatDateTime(item.accepted_at)}</span>
        </div>
        <strong className="tw:font-semibold tw:break-words">{title}</strong>
        {subject && subject !== title ? (
          <span className="tw:min-w-0 tw:truncate tw:text-sm" title={subject}>
            {subject}
          </span>
        ) : null}
        {identity ? <span className="tw:text-sm">{identity}</span> : null}
        {torrentTitle && torrentTitle !== title && torrentTitle !== subject ? (
          <span className={`${META} tw:min-w-0 tw:truncate`} title={torrentTitle}>
            {torrentTitle}
          </span>
        ) : null}
        {animeTitle ? <span className={META}>{animeTitle}</span> : null}
        <Position
          stage={item.stage}
          stageState={item.stage_state}
          reasonCode={item.reason_code}
          configHold={item.config_hold}
          transfer={item.transfer}
        />
      </button>
    </li>
  );
}

function RecordDetail({
  acquisitionID,
  onShowList,
  onOpenAnime,
}: {
  acquisitionID: string;
  onShowList: () => void;
  onOpenAnime: (managedAnimeID: string) => void;
}) {
  const query = useQuery({
    queryKey: acquisitionRecordKeys.detail(acquisitionID),
    queryFn: () => getAcquisitionRecord(acquisitionID),
  });
  const back = (
    <Button size="sm" variant="link" className="tw:justify-self-start" onClick={onShowList}>
      {t("records.allRecords")}
    </Button>
  );

  if (query.error) {
    const notFound = query.error instanceof ApiError && query.error.status === 404;
    return (
      <div className="tw:grid tw:gap-3">
        {back}
        <StateBlock kind="error"
          title={notFound ? t("records.detail.notFound") : toUserMessage(query.error, t("records.detail.loadFailed"))}
          action={
            notFound ? undefined : (
              <Button size="sm" variant="link" onClick={() => void query.refetch()}>
                {t("log.retry")}
              </Button>
            )
          }
        />
      </div>
    );
  }
  if (!query.data) return <StateBlock kind="busy" title={t("records.detail.loading")} />;
  const detail = query.data;
  const compact = detail.record_detail === "compact";

  return (
    <div className="tw:grid tw:gap-4">
      {back}
      <DetailHeader detail={detail} onOpenAnime={onOpenAnime} />
      {detail.entry_kind === "one_shot" && (detail.state === "failed" || detail.state === "cancelled") ? (
        <p className="tw:m-0 tw:text-sm tw:text-muted-foreground">
          {t(detail.state === "failed" ? "records.detail.oneShotFailed" : "records.detail.oneShotCancelled")}
        </p>
      ) : null}
      {compact ? (
        <Alert
          tone="info"
          title={detail.compact_reason === "owner_cleanup" ? t("records.detail.compactCleanup") : t("records.detail.compact")}
        />
      ) : null}
      {!compact && detail.stages.length > 0 ? (
        <StageList detail={detail} />
      ) : null}
      {detail.outcome ? <OutcomeSection outcome={detail.outcome} /> : null}
      {detail.files_available ? <FileSection acquisitionID={detail.acquisition_id} /> : null}
    </div>
  );
}

function DetailHeader({ detail, onOpenAnime }: { detail: AcquisitionRecordDetail; onOpenAnime: (managedAnimeID: string) => void }) {
  const items: { label: ReactNode; value: ReactNode }[] = [
    { label: t("records.detail.name"), value: <span className="tw:break-words">{displayText(detail.source_title)}</span> },
    {
      label: t("records.detail.state"),
      value: (
        <span className={ITEM_HEAD}>
          <RecordStateTag state={detail.state} />
          <Position stage={detail.stage} stageState={detail.stage_state} reasonCode={detail.reason_code} configHold={detail.config_hold} />
        </span>
      ),
    },
    { label: t("records.detail.entry"), value: entryLabel(detail.entry_kind) },
  ];
  if (detail.episodes?.length) {
    items.push({ label: t("records.detail.episodes"), value: recordSubject({ episodes: detail.episodes }) });
  }
  if (detail.anime) {
    const anime = detail.anime;
    items.push({
      label: t("records.detail.anime"),
      value: (
        <Button size="sm" variant="link" title={t("records.detail.openAnime")} onClick={() => onOpenAnime(anime.managed_anime_id)}>
          {anime.title}
        </Button>
      ),
    });
  }
  if (detail.release) {
    items.push({ label: t("records.detail.release"), value: <span className="tw:break-words">{displayText(detail.release.title)}</span> });
  }
  const seed = detail.seed;
  if (seed?.group_name) {
    const site = seed.provider ? providerLabel(seed.provider) : undefined;
    items.push({
      label: t("records.detail.group"),
      value: site ? t("records.detail.groupWithSite", { group: seed.group_name, site }) : seed.group_name,
    });
  } else if (seed?.provider) {
    items.push({ label: t("records.detail.site"), value: providerLabel(seed.provider) });
  }
  if (seed?.published_at) items.push({ label: t("records.detail.published"), value: formatDateTime(seed.published_at) });
  if (typeof seed?.size_bytes === "number") items.push({ label: t("records.detail.size"), value: formatBytes(seed.size_bytes) });
  if (seed?.info_hash) items.push({ label: t("records.detail.infoHash"), value: <InfoHash hash={seed.info_hash} /> });
  if (seed?.title_tags?.length || seed?.anibt_tags?.length) {
    items.push({ label: t("records.detail.tags"), value: <TagBadges anibt={seed.anibt_tags} title={seed.title_tags} /> });
  }
  items.push({ label: t("records.detail.accepted"), value: formatDateTime(detail.accepted_at) });
  if (detail.ended_at) items.push({ label: t("records.detail.ended"), value: formatDateTime(detail.ended_at) });
  return <DescriptionList items={items} />;
}

function InfoHash({ hash }: { hash: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(hash);
      setCopied(true);
      globalThis.setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  };
  return (
    <span className={ITEM_HEAD}>
      <code className="tw:break-all tw:text-xs">{hash}</code>
      <Button size="sm" variant="link" onClick={() => void copy()}>
        {copied ? t("records.detail.hashCopied") : t("records.detail.copyHash")}
      </Button>
    </span>
  );
}

function StageList({ detail }: { detail: AcquisitionRecordDetail }) {
  const { stages, transfer } = detail;
  return (
    <section>
      <h3 className={SECTION_TITLE}>{t("records.detail.stagesTitle")}</h3>
      <ol aria-label={t("records.detail.stagesAria")} className={LIST}>
        {stages.map((stage) => (
          <li key={stage.stage} className={ITEM}>
            <div className={ITEM_HEAD}>
              <strong className="tw:font-semibold">{stageLabel(stage.stage)}</strong>
              {stage.state ? (
                <StatePill tone={STAGE_TONE[stage.state] ?? "muted"}>{stageStateLabel(stage.state)}</StatePill>
              ) : (
                <StatePill>{stageStateLabel(stage.availability)}</StatePill>
              )}
              {stage.updated_at ? <span className={META}>{formatDateTime(stage.updated_at)}</span> : null}
            </div>
            <StageMeta stage={stage} transfer={stage.stage === "download_wait" && stage.state === "running" ? transfer : undefined} />
            {/* Waiting for configuration is not a failure: what is missing and that it continues by itself; no retry. */}
            {stage.state === "waiting_config" && detail.config_hold ? (
              <>
                <p className={META}>{configHoldText(detail.config_hold)}</p>
                <p className={META}>{t("records.configHold.note")}</p>
              </>
            ) : null}
            {stage.stage === "download_wait" && detail.cancel_available ? (
              <DownloadCancel acquisitionID={detail.acquisition_id} version={detail.version} oneShot={detail.entry_kind === "one_shot"} />
            ) : null}
            {stage.stage === "file_execution" && stage.state === "failed" && detail.import_retry_available ? (
              <ImportRetry acquisitionID={detail.acquisition_id} version={detail.version} />
            ) : null}
          </li>
        ))}
      </ol>
    </section>
  );
}

// The only repair for an import-stage failure: after confirming, re-run every failed file of this record. Records refresh on success.
function ImportRetry({ acquisitionID, version }: { acquisitionID: string; version: number }) {
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  return (
    <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
      <Button size="sm" variant="secondary" onClick={() => setConfirming(true)}>
        {t("records.importRetry.action")}
      </Button>
      {confirming ? (
        <ImportRetryConfirmDialog
          acquisitionID={acquisitionID}
          version={version}
          onClose={() => setConfirming(false)}
          onAccepted={() => void queryClient.invalidateQueries({ queryKey: acquisitionRecordKeys.all })}
        />
      ) : null}
    </div>
  );
}

// Cancel a download that has not reached import (the server reports cancel_available): after confirming, the
//   qBittorrent task is deleted with its data. Records and RSS lists refresh on success.
function DownloadCancel({ acquisitionID, version, oneShot }: { acquisitionID: string; version: number; oneShot: boolean }) {
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  return (
    <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
      <Button size="sm" variant="secondary" onClick={() => setConfirming(true)}>
        {t("records.downloadCancel.action")}
      </Button>
      {confirming ? (
        <DownloadCancelConfirmDialog
          acquisitionID={acquisitionID}
          version={version}
          oneShot={oneShot}
          onClose={() => setConfirming(false)}
          onAccepted={() => {
            void queryClient.invalidateQueries({ queryKey: acquisitionRecordKeys.all });
            void queryClient.invalidateQueries({ queryKey: rssListKeys.all });
          }}
        />
      ) : null}
    </div>
  );
}

function StageMeta({ stage, transfer }: { stage: AcquisitionStage; transfer?: AcquisitionTransfer }) {
  const parts: ReactNode[] = [];
  if (transfer) parts.push(transferText(transfer));
  if (typeof stage.done === "number" && typeof stage.total === "number") {
    parts.push(t("records.detail.progress", { done: stage.done, total: stage.total }));
  }
  if (typeof stage.attempts_started === "number" && stage.attempts_started > 1) {
    parts.push(t("records.detail.attempts", { n: stage.attempts_started }));
  }
  if (stage.reason_code) parts.push(<ReasonText key="reason" code={stage.reason_code} />);
  if (parts.length === 0) return null;
  return (
    <p className={META}>
      {parts.map((part, index) => (
        <span key={index}>
          {index > 0 ? " · " : null}
          {part}
        </span>
      ))}
    </p>
  );
}

function OutcomeSection({ outcome }: { outcome: NonNullable<AcquisitionRecordDetail["outcome"]> }) {
  const counts = outcome.counts;
  return (
    <section>
      <h3 className={SECTION_TITLE}>{t("records.detail.outcomeTitle")}</h3>
      <div className={ITEM}>
        <div className={ITEM_HEAD}>
          <RecordStateTag state={outcome.outcome} />
          <span className={META}>{formatDateTime(outcome.settled_at)}</span>
        </div>
        {counts ? (
          <p className={META}>
            {t("records.detail.counts", { total: counts.total, required: counts.required, succeeded: counts.succeeded, failed: counts.failed })}
          </p>
        ) : null}
        {outcome.primary_failure ? (
          <p className={META}>
            {stageLabel(outcome.primary_failure.stage)}
            {" · "}
            <ReasonText code={outcome.primary_failure.reason_code} />
          </p>
        ) : null}
        {outcome.has_unknown_effect ? <p className={META}>{t("records.detail.unknownEffect")}</p> : null}
      </div>
    </section>
  );
}

function FileSection({ acquisitionID }: { acquisitionID: string }) {
  const query = useInfiniteQuery({
    queryKey: acquisitionRecordKeys.files(acquisitionID),
    queryFn: ({ pageParam }) => listAcquisitionRecordFiles(acquisitionID, { limit: FILE_PAGE_LIMIT, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor ?? undefined,
    retry: (failureCount, error) => !(error instanceof ApiError && [404, 409, 410].includes(error.status)) && failureCount < 2,
  });
  const files = query.data?.pages.flatMap((page) => page.items) ?? [];

  let body: ReactNode;
  if (query.error) {
    const status = query.error instanceof ApiError ? query.error.status : 0;
    body =
      status === 409 ? (
        <span className={META}>{t("records.file.notReady")}</span>
      ) : status === 410 ? (
        <span className={META}>{t("records.file.unavailable")}</span>
      ) : (
        <StateBlock kind="error" title={toUserMessage(query.error, t("records.file.loadFailed"))} />
      );
  } else if (!query.data) {
    body = <StateBlock kind="busy" title={t("records.file.loading")} />;
  } else if (files.length === 0) {
    body = <span className={META}>{t("records.file.empty")}</span>;
  } else {
    body = (
      <>
        <ul aria-label={t("records.file.aria")} className={LIST}>
          {files.map((file) => (
            <FileRow key={file.file_ref} file={file} />
          ))}
        </ul>
        {query.hasNextPage ? (
          <Button size="sm" className="tw:justify-self-center" loading={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>
            {t("records.loadMore")}
          </Button>
        ) : null}
      </>
    );
  }
  return (
    <section className="tw:grid tw:gap-2">
      <h3 className={SECTION_TITLE}>{t("records.detail.filesTitle")}</h3>
      {body}
    </section>
  );
}

/** The actual outcome of same-name file handling (the server judges by the facts recorded at execution time; not sent when it cannot be determined). */
const COLLISION_TONE: Record<NonNullable<AcquisitionFile["collision_outcome"]>, StatePillTone> = {
  none: "muted",
  skipped: "warning",
  versioned: "info",
  overwritten: "warning",
};

function FileRow({ file }: { file: AcquisitionFile }) {
  const status = fileStatus(file);
  // Files not imported explain why they were not selected (unknown file, excluded by filter rules); the reason for selection is not shown.
  const reason =
    file.delivery?.reason_code ??
    file.operation?.reason_code ??
    file.handoff?.reason_code ??
    (file.requirement === "excluded" ? file.selection_reason_code : undefined);
  return (
    <li className={ITEM}>
      <span className="tw:break-words">{displayText(file.relative_path)}</span>
      {file.placed_path ? (
        <span className={`${META} tw:break-all`} title={displayText(file.placed_path)}>
          {t("records.file.placedAt", { path: displayText(file.placed_path) })}
        </span>
      ) : null}
      <div className={ITEM_HEAD}>
        <span className={META}>{tryT(`records.file.media.${file.media_kind}`) ?? file.media_kind}</span>
        <span className={META}>{formatBytes(file.size_bytes)}</span>
        {status ? <StatePill tone={status.tone}>{status.label}</StatePill> : null}
        {file.handoff ? <StatePill>{tryT(`records.file.handoff.${file.handoff.state}`) ?? file.handoff.state}</StatePill> : null}
        {file.collision_outcome ? (
          <StatePill tone={COLLISION_TONE[file.collision_outcome]}>
            {labelValue(t("records.file.collision.label"), t(`records.file.collision.${file.collision_outcome}`))}
          </StatePill>
        ) : null}
        {file.placement_end ? <StatePill tone="muted">{placementEndLabel(file.placement_end)}</StatePill> : null}
        {reason ? (
          <span className={META}>
            <ReasonText code={reason} />
          </span>
        ) : null}
        {file.placement_failure ? <StatePill tone="offline">{t("records.file.handoff.failed")}</StatePill> : null}
        {file.placement_failure?.reason_code ? (
          <span className={META}>
            <ReasonText code={file.placement_failure.reason_code} />
          </span>
        ) : null}
      </div>
    </li>
  );
}

/** Post-import outcomes: replaced by a re-fetch (washing), overwritten by another file (noting the anime that overwrote it), or superseded by a new file after a re-download. */
function placementEndLabel(end: NonNullable<AcquisitionFile["placement_end"]>): string {
  if (end.state === "superseded") return t("records.file.end.superseded");
  if (end.cause === "redownload") return t("records.file.end.redownloaded");
  return end.successor ? t("records.file.end.displacedBy", { title: end.successor.title }) : t("records.file.end.displaced");
}

/** File status: the delivery outcome first, then the import operation's current state; a file not imported is merely marked as such. */
function fileStatus(file: AcquisitionFile): { label: string; tone: Tone } | undefined {
  if (file.requirement === "excluded") return { label: t("records.file.excluded"), tone: "muted" };
  if (file.delivery) {
    return file.delivery.outcome === "succeeded"
      ? { label: t("records.file.delivery.succeeded"), tone: "online" }
      : { label: t("records.file.delivery.failed"), tone: "offline" };
  }
  if (file.operation) {
    const tone: Tone = file.operation.state === "failed" ? "offline" : file.operation.state === "completed" ? "online" : "info";
    return { label: tryT(`records.file.operation.${file.operation.state}`) ?? file.operation.state, tone };
  }
  return undefined;
}
