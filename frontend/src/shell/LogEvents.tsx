import { useEffect, useMemo, useState } from "react";
import { ChevronRight, Clock, Search } from "lucide-react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { cn } from "@/lib/utils";
import { Button, Field, Input, Select } from "../ui/base/controls";
import { StatePill, type StatePillTone } from "../ui/StatePill";
import { Popover } from "../ui/base/overlays";
import { StateBlock } from "../ui/StateBlock";
import {
  listAnime,
  listObservabilityRecords,
  listPluginTargets,
  listReleaseBatchEntries,
  toUserMessage,
  type ObservabilityRecord,
} from "../lib/api/client";
import { Dialog } from "../ui/base/Dialog";
import {
  formatDateTime,
  formatShortDateTime,
  metadataBlocking,
  notificationTriggerKindLabel,
  subscriptionLogKindLabel,
} from "../lib/labels";
import { reasonView } from "../lib/i18n/reasons";
import { animeKeys, configKeys, observabilityKeys } from "../lib/queryKeys";
import { t } from "../lib/i18n";
import { labelValue } from "../lib/i18n/format";
import {
  deliveryView,
  entryGroupName,
  entryView,
  groupEvents,
  joinMeta,
  runView,
  stepText,
  type BusinessEntry,
} from "./logRuns";
import { messageText, type LogState } from "./logState";

// Events tab. Each row has three layers: the title ("anime · episode · group") with the result
// fixed on its right, a second line of facts (files, size, replacement), and a third line with
// the reason only when something failed, is unconfirmed or was deferred. On a phone the result
// and time fold under the title. Expanded, a download shows its steps, files and a technical
// detail box.

const PAGE_LIMIT = 50;
const SEARCH_DEBOUNCE_MS = 300;

export type StateFilter = "" | LogState;
export type TypeFilter = "" | "business" | "plugin";

/** The events read projection: primary hides the repeated release filtering process; all is the full history. */
type EventView = "primary" | "all";

export type EventFilters = {
  state: StateFilter;
  type: TypeFilter;
  animeId: string;
  q: string;
  /** datetime-local values; converted to ISO before entering the query. */
  fromLocal: string;
  toLocal: string;
};

export const EMPTY_FILTERS: EventFilters = { state: "", type: "", animeId: "", q: "", fromLocal: "", toLocal: "" };

const TYPE_OPTIONS = (): { value: TypeFilter; label: string }[] => [
  { value: "", label: t("log.allTypes") },
  { value: "business", label: t("log.typeBusiness") },
  { value: "plugin", label: t("log.typePlugin") },
];

const STATE_OPTIONS = (): { value: StateFilter; label: string }[] => [
  { value: "", label: t("log.allStatus") },
  { value: "in_progress", label: t("log.inProgress") },
  { value: "success", label: t("log.success") },
  { value: "failure", label: t("log.failure") },
  { value: "info", label: t("log.info") },
];

const LIST = "tw:m-0 tw:list-none tw:p-0 tw:rounded-[10px] tw:border tw:border-border tw:bg-card tw:overflow-hidden";
const ITEM = "tw:border-b tw:border-border tw:last:border-b-0";
const HEAD = "tw:grid tw:w-full tw:min-w-0 tw:gap-0.5 tw:px-3 tw:py-2 tw:text-left tw:text-sm";
const TITLE_LINE = "tw:flex tw:min-w-0 tw:items-center tw:gap-2";
const META = "tw:m-0 tw:min-w-0 tw:max-w-full tw:wrap-anywhere tw:text-xs tw:text-muted-foreground";
const EXPANDED = "tw:grid tw:gap-2 tw:border-t tw:border-border tw:bg-muted/30 tw:px-3 tw:py-2.5";
const SECTION_TITLE = "tw:m-0 tw:text-xs tw:font-medium tw:text-muted-foreground";
const SUB_LIST = "tw:m-0 tw:grid tw:list-none tw:gap-1 tw:p-0 tw:text-xs";
const FOCUS = "tw:focus-visible:outline-2 tw:focus-visible:-outline-offset-2 tw:focus-visible:outline-ring";

function stateTone(state: LogState, unconfirmed = false): StatePillTone {
  if (unconfirmed) return "warning";
  switch (state) {
    case "success":
      return "online";
    case "failure":
      return "offline";
    case "in_progress":
      return "info";
    default:
      return "muted";
  }
}

/** datetime-local local value → contract date-time; empty or invalid values do not filter. */
export function localToIso(value: string): string | undefined {
  if (value === "") return undefined;
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? undefined : parsed.toISOString();
}

function useDebounced<T>(value: T, delay: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = globalThis.setTimeout(() => setDebounced(value), delay);
    return () => globalThis.clearTimeout(timer);
  }, [value, delay]);
  return debounced;
}

type AnimeNames = ReadonlyMap<string, string>;

export function LogEvents({
  filters,
  onFiltersChange,
  onOpenDelivery,
  onOpenAnimeMetadata,
}: {
  filters: EventFilters;
  onFiltersChange: (next: EventFilters) => void;
  onOpenDelivery: (deliveryID: string) => void;
  onOpenAnimeMetadata?: (managedAnimeId: string) => void;
}) {
  const animeList = useQuery({ queryKey: animeKeys.list, queryFn: listAnime });
  const animeNames = useMemo<AnimeNames>(
    () => new Map((animeList.data ?? []).map((item) => [item.managed_anime.id, item.managed_anime.title_override ?? item.managed_anime.title])),
    [animeList.data],
  );
  const q = useDebounced(filters.q.trim(), SEARCH_DEBOUNCE_MS);
  const from = localToIso(filters.fromLocal);
  const to = localToIso(filters.toLocal);
  const patch = (next: Partial<EventFilters>) => onFiltersChange({ ...filters, ...next });
  // The event list keeps the primary projection; the type filter narrows records before grouping.
  const eventView: EventView = "primary";

  return (
    <div className="tw:flex tw:min-h-0 tw:flex-1 tw:flex-col tw:gap-3">
      <div className="tw:grid tw:shrink-0 tw:gap-2">
        <div className="tw:grid tw:grid-cols-[7rem_7rem_minmax(0,1fr)_auto] tw:items-center tw:gap-2 tw:phone:grid-cols-[6.5rem_5rem_minmax(0,1fr)_auto] tw:phone:gap-1">
          <div className="tw:min-w-0">
            <Select<TypeFilter> label={t("log.type")} value={filters.type} options={TYPE_OPTIONS()} onChange={(type) => patch({ type })} />
          </div>
          <div className="tw:min-w-0">
            <Select<StateFilter> label={t("log.status")} value={filters.state} options={STATE_OPTIONS()} onChange={(state) => patch({ state })} />
          </div>
          <div className="tw:min-w-0">
            <Input
              type="search"
              aria-label={t("log.search")}
              placeholder={t("log.searchPlaceholder")}
              title={t("log.searchHint")}
              leadingIcon={<Search className="tw:size-3.5" />}
              value={filters.q}
              maxLength={100}
              onChange={(event) => patch({ q: event.target.value })}
            />
          </div>
          <TimeRangePopover from={filters.fromLocal} to={filters.toLocal} onChange={(fromLocal, toLocal) => patch({ fromLocal, toLocal })} />
        </div>
        {filters.animeId && (
          <small className="tw:text-muted-foreground">
            {t("log.currentSubscription", { title: animeNames.get(filters.animeId) ?? t("log.thisSubscription") })}
          </small>
        )}
      </div>
      <EventsList
        key={filters.type}
        eventView={eventView}
        type={filters.type}
        state={filters.state}
        animeId={filters.animeId}
        q={q}
        from={from}
        to={to}
        animeNames={animeNames}
        onOpenDelivery={onOpenDelivery}
        onOpenAnimeMetadata={onOpenAnimeMetadata}
      />
    </div>
  );
}

/** The time range folds into one popover: the trigger button shows the current range, two datetime-local inputs plus a clear. */
function TimeRangePopover({ from, to, onChange }: { from: string; to: string; onChange: (from: string, to: string) => void }) {
  const [open, setOpen] = useState(false);
  const fromText = from ? formatShortDateTime(localToIso(from)) : "";
  const toText = to ? formatShortDateTime(localToIso(to)) : "";
  const summary =
    fromText && toText
      ? t("log.timeBetween", { from: fromText, to: toText })
      : fromText
        ? t("log.timeFrom", { time: fromText })
        : toText
          ? t("log.timeUntil", { time: toText })
          : t("log.timeAny");
  return (
    <Popover
      open={open}
      onOpenChange={setOpen}
      align="end"
      title={t("log.timeRange")}
      trigger={
        <Button
          aria-label={labelValue(t("log.timeRange"), summary)}
          title={labelValue(t("log.timeRange"), summary)}
          data-range-active={Boolean(from || to)}
          className={cn(
            "tw:relative tw:size-9 tw:min-w-0 tw:shrink-0 tw:justify-self-end tw:p-0 tw:phone:size-11",
            (from || to) && "tw:border-primary tw:text-primary",
          )}
        >
          <Clock className="tw:size-3.5 tw:flex-none" aria-hidden />
          {(from || to) && <span aria-hidden className="tw:absolute tw:right-1.5 tw:top-1.5 tw:size-1.5 tw:rounded-full tw:bg-primary" />}
        </Button>
      }
    >
      <div className="tw:grid tw:w-64 tw:max-w-full tw:gap-2">
        <Field label={t("log.startTime")}>
          <Input aria-label={t("log.startTime")} type="datetime-local" value={from} onChange={(event) => onChange(event.target.value, to)} />
        </Field>
        <Field label={t("log.endTime")}>
          <Input aria-label={t("log.endTime")} type="datetime-local" value={to} onChange={(event) => onChange(from, event.target.value)} />
        </Field>
        <Button size="sm" variant="link" className="tw:justify-self-end" disabled={!from && !to} onClick={() => onChange("", "")}>
          {t("log.clearTime")}
        </Button>
      </div>
    </Popover>
  );
}

function EventsList({
  eventView,
  type,
  state,
  animeId,
  q,
  from,
  to,
  animeNames,
  onOpenDelivery,
  onOpenAnimeMetadata,
}: {
  eventView: EventView;
  type: TypeFilter;
  state: StateFilter;
  animeId: string;
  q: string;
  from?: string;
  to?: string;
  animeNames: AnimeNames;
  onOpenDelivery: (deliveryID: string) => void;
  onOpenAnimeMetadata?: (managedAnimeId: string) => void;
}) {
  const targets = useQuery({
    queryKey: configKeys.pluginTargets,
    queryFn: listPluginTargets,
    enabled: type === "" || type === "plugin",
    staleTime: 60_000,
  });
  const query = useInfiniteQuery({
    queryKey: [...observabilityKeys.businessLine(eventView, state, animeId, q, from, to), type],
    queryFn: ({ pageParam, signal }) =>
      listObservabilityRecords({
        kind: type ? [type] : ["business", "plugin"],
        event_view: eventView,
        state: state === "" ? undefined : state,
        q: q === "" ? undefined : q,
        managed_anime_id: animeId === "" ? undefined : animeId,
        from,
        to,
        limit: PAGE_LIMIT,
        cursor: pageParam,
      }, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor ?? undefined,
  });
  const items = useMemo(() => groupEvents(query.data?.pages.flatMap((page) => page.records) ?? []), [query.data]);
  const hideAnime = animeId !== "";
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set());
  const toggle = (key: string) =>
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  const animeOf = (entry: BusinessEntry) => (hideAnime ? undefined : entry.anime_title || animeNames.get(entry.managed_anime_id));

  if (query.error) {
    return (
      <StateBlock
        kind="error"
        title={toUserMessage(query.error, t("log.loadRecordsFailed"))}
        action={
          <Button size="sm" variant="link" onClick={() => void query.refetch()}>
            {t("log.retry")}
          </Button>
        }
      />
    );
  }
  if (!query.data) return <StateBlock kind="busy" title={t("log.loadingRecords")} />;
  if (items.length === 0) return <StateBlock kind="empty" title={t("log.emptyRecords")} />;

  return (
    <div
      role="region"
      aria-label={t("log.recordsAria")}
      tabIndex={0}
      className="tw:grid tw:min-h-0 tw:auto-rows-max tw:gap-2 tw:overflow-y-auto tw:overscroll-contain tw:focus-visible:outline-2 tw:focus-visible:-outline-offset-2 tw:focus-visible:outline-ring"
    >
      <ul aria-label={t("log.recordsAria")} className={LIST}>
        {items.map((item) => {
          if (item.type === "run") {
            return (
              <RunItem
                key={item.key}
                steps={item.steps}
                anime={animeOf(item.steps[item.steps.length - 1]!)}
                expanded={expanded.has(item.key)}
                onToggle={() => toggle(item.key)}
              />
            );
          }
          if (item.type === "group") {
            return (
              <GroupItem
                key={item.key}
                entries={item.entries}
                anime={animeOf(item.entries[0]!)}
                expanded={expanded.has(item.key)}
                onToggle={() => toggle(item.key)}
              />
            );
          }
          if (item.type === "batch") {
            return (
              <BatchItem
                key={item.key}
                entry={item.entry}
                anime={animeOf(item.entry)}
                expanded={expanded.has(item.key)}
                onToggle={() => toggle(item.key)}
              />
            );
          }
          const record = item.record;
          if (record.record_type === "business") {
            return (
              <EntryItem
                key={item.key}
                entry={record.entry}
                anime={animeOf(record.entry)}
                onOpenAnimeMetadata={onOpenAnimeMetadata}
              />
            );
          }
          if (record.record_type === "plugin") {
            return (
              <DeliveryItem
                key={item.key}
                delivery={record.delivery}
                targetName={targets.data?.targets.find((target) => target.id === record.delivery.plugin_target_config_id)?.name}
                anime={hideAnime || !record.delivery.managed_anime_id ? undefined : animeNames.get(record.delivery.managed_anime_id)}
                onOpen={() => onOpenDelivery(record.delivery.id)}
              />
            );
          }
          return null;
        })}
      </ul>
      {query.hasNextPage && (
        <Button size="sm" className="tw:justify-self-center" loading={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>
          {query.isFetchingNextPage ? t("log.loadingMore") : t("log.loadMore")}
        </Button>
      )}
    </div>
  );
}

/** The shared three-layer row. `onToggle` makes the head a disclosure button; `onActivate` makes it a plain action. */
function Row({
  title,
  titleHint,
  tags,
  result,
  tone,
  busy,
  time,
  meta,
  reason,
  reasonAction,
  expanded,
  onToggle,
  onActivate,
  activateLabel,
  children,
}: {
  title: string;
  titleHint?: string;
  tags?: React.ReactNode;
  result: string;
  tone: StatePillTone;
  busy?: boolean;
  time: string;
  meta?: string;
  reason?: string;
  reasonAction?: React.ReactNode;
  expanded?: boolean;
  onToggle?: () => void;
  onActivate?: () => void;
  activateLabel?: string;
  children?: React.ReactNode;
}) {
  const summary = joinMeta(reason, meta);
  const head = (
    <>
      <span data-event-line="title" className={TITLE_LINE}>
        {onToggle ? (
          <ChevronRight
            aria-hidden
            className={cn("tw:size-3.5 tw:shrink-0 tw:transition-transform", expanded && "tw:rotate-90")}
          />
        ) : null}
        <span className="tw:min-w-0 tw:flex-1 tw:truncate tw:font-medium" title={titleHint ?? title}>
          {title}
          {tags}
        </span>
        <span className="tw:min-w-0 tw:max-w-[48%] tw:truncate tw:text-xs" title={result}>
          <StatePill tone={tone} busy={busy}>
            {result}
          </StatePill>
        </span>
      </span>
      <span data-event-line="context" className="tw:flex tw:min-w-0 tw:items-center tw:gap-2 tw:text-xs tw:text-muted-foreground">
        <span className="tw:min-w-0 tw:flex-1 tw:truncate" title={summary || undefined}>
          {summary}
        </span>
        {reasonAction}
        <time className="tw:shrink-0 tw:whitespace-nowrap tw:tabular-nums" dateTime={time} title={formatDateTime(time)}>
          {formatShortDateTime(time)}
        </time>
      </span>
    </>
  );
  return (
    <li className={ITEM} data-event-row="">
      {onToggle || onActivate ? (
        <button
          type="button"
          aria-expanded={onToggle ? expanded : undefined}
          aria-label={activateLabel}
          onClick={onToggle ?? onActivate}
          className={cn(HEAD, "tw:cursor-pointer tw:hover:bg-muted/50", FOCUS)}
        >
          {head}
        </button>
      ) : (
        <div className={HEAD}>{head}</div>
      )}
      {expanded ? children : null}
    </li>
  );
}

function OneShotTag() {
  return (
    <span className="tw:ml-1.5 tw:inline-block tw:rounded-sm tw:border tw:border-border tw:px-1 tw:text-xs tw:font-normal tw:text-muted-foreground">
      {t("log.oneShot")}
    </span>
  );
}

/** One download per row; see the file header for the layers. */
function RunItem({
  steps,
  anime,
  expanded,
  onToggle,
}: {
  steps: BusinessEntry[];
  anime?: string;
  expanded: boolean;
  onToggle: () => void;
}) {
  const view = runView(steps);
  const named = Boolean(view.groupName || view.episodes);
  const title = named ? joinMeta(anime, view.episodes, view.groupName) : (view.releaseTitle ?? anime ?? t("log.run.kind"));
  return (
    <Row
      title={title}
      titleHint={view.releaseTitle}
      tags={
        <>
          {view.oneShot && <OneShotTag />}
        </>
      }
      result={view.result}
      tone={stateTone(view.state)}
      busy={view.state === "in_progress"}
      time={view.latest.occurred_at}
      meta={view.meta}
      reason={view.reason}
      expanded={expanded}
      onToggle={onToggle}
    >
      <div className={EXPANDED}>
        {view.releaseTitle && named ? <p className={cn(META, "tw:break-all")}>{view.releaseTitle}</p> : null}
        <h4 className={SECTION_TITLE}>{t("log.section.steps")}</h4>
        <ul aria-label={t("log.run.stepsAria")} className={SUB_LIST}>
          {steps.map((step) => {
            const reason = step.outcome === "failure" ? reasonView(step.reason_code)?.text : undefined;
            return (
              <li key={step.id} className="tw:flex tw:flex-wrap tw:gap-x-2">
                <time className="tw:tabular-nums tw:text-muted-foreground" dateTime={step.occurred_at} title={formatDateTime(step.occurred_at)}>
                  {formatShortDateTime(step.occurred_at)}
                </time>
                <span className="tw:min-w-0 tw:break-words">
                  {stepText(step)}
                  {reason ? <span className="tw:text-muted-foreground"> · {t("log.reason.line", { reason })}</span> : null}
                </span>
              </li>
            );
          })}
        </ul>
        {view.files.length > 0 && (
          <>
            <h4 className={SECTION_TITLE}>{t("log.section.files")}</h4>
            <ul className={SUB_LIST}>
              {view.files.map((file) => (
                <li key={file.key} className="tw:grid tw:gap-0.5">
                  <span className="tw:break-all">{file.name}</span>
                  <span className={cn("tw:text-muted-foreground", !file.ok && "tw:text-destructive")}>
                    {joinMeta(file.label, file.folder, file.size, file.reason)}
                  </span>
                </li>
              ))}
            </ul>
          </>
        )}
        {view.technical ? (
          <>
            <h4 className={SECTION_TITLE}>{t("log.section.tech")}</h4>
            <pre className="tw:m-0 tw:overflow-auto tw:whitespace-pre-wrap tw:break-all tw:rounded-md tw:border tw:border-border tw:bg-card tw:p-2 tw:font-mono tw:text-xs">
              {view.technical}
            </pre>
          </>
        ) : null}
      </div>
    </Row>
  );
}

/** A group of identical-message events: a counted row, expanded to list each entry. */
function GroupItem({
  entries,
  anime,
  expanded,
  onToggle,
}: {
  entries: BusinessEntry[];
  anime?: string;
  expanded: boolean;
  onToggle: () => void;
}) {
  const first = entries[0]!;
  const view = entryView(first, anime);
  // Release groups put the count in the right column beside the message
  // ("已被过滤 8 个种子"): a count in the title reads like a comparison with
  // the discovery row beside it.
  const releaseGroup = first.kind === "release_processed";
  const title = releaseGroup
    ? joinMeta(anime, entryGroupName(first) ?? subscriptionLogKindLabel(first.kind))
    : joinMeta(anime, t("log.groupEvents", { n: entries.length }));
  const result = releaseGroup ? `${view.result} ${t("log.groupReleases", { n: entries.length })}` : view.result;
  return (
    <Row
      title={title}
      result={result}
      tone={stateTone(view.state)}
      time={first.occurred_at}
      meta={first.message_code === "offset_aligned" ? view.meta : undefined}
      expanded={expanded}
      onToggle={onToggle}
    >
      <ul className={cn(EXPANDED, SUB_LIST)}>
        {entries.map((entry) => (
          <li key={entry.id} className="tw:flex tw:flex-wrap tw:gap-x-2">
            <time className="tw:tabular-nums tw:text-muted-foreground" dateTime={entry.occurred_at} title={formatDateTime(entry.occurred_at)}>
              {formatShortDateTime(entry.occurred_at)}
            </time>
            <span className="tw:min-w-0 tw:break-words">
              {joinMeta(entry.details?.release_title ?? entryGroupName(entry), messageText(entry)) || subscriptionLogKindLabel(entry.kind)}
            </span>
          </li>
        ))}
      </ul>
    </Row>
  );
}

/** One release evaluation batch: the head row of many detail entries the
 * primary view hides. Expanding fetches the first page of details; the rest
 * live behind the "view all" dialog. */
function BatchItem({
  entry,
  anime,
  expanded,
  onToggle,
}: {
  entry: BusinessEntry;
  anime?: string;
  expanded: boolean;
  onToggle: () => void;
}) {
  const view = entryView(entry, anime);
  const batchKey = entry.subject_id ?? "";
  const total = entry.batch_counts?.total ?? 0;
  const details = useQuery({
    queryKey: observabilityKeys.releaseBatch(`${batchKey}:preview`),
    // The inline preview stays short so the batch head, its preview and the
    // "view all" link fit one dialog screen; everything past it lives in the
    // nested details dialog.
    queryFn: ({ signal }) => listReleaseBatchEntries(batchKey, { limit: 6 }, signal),
    enabled: expanded && batchKey !== "",
    staleTime: 30_000,
  });
  const [showAll, setShowAll] = useState(false);
  const entries = (details.data?.records ?? []).flatMap((record) => (record.record_type === "business" ? [record.entry] : []));
  const shown = entries.length;
  return (
    <Row
      title={view.title}
      result={view.result}
      tone={stateTone(view.state)}
      time={entry.occurred_at}
      meta={view.meta}
      expanded={expanded}
      onToggle={onToggle}
    >
      {details.isLoading ? (
        <p className={cn(META, "tw:pl-[30px]")}>{t("log.loadingRecords")}</p>
      ) : details.isError ? (
        <p className={cn(META, "tw:pl-[30px]")}>{toUserMessage(details.error, t("log.loadRecordsFailed"))}</p>
      ) : (
        <>
          <ul className={cn(EXPANDED, SUB_LIST)}>
            {entries.map((detail) => (
              <li key={detail.id} className="tw:flex tw:flex-wrap tw:gap-x-2">
                <time className="tw:tabular-nums tw:text-muted-foreground" dateTime={detail.occurred_at} title={formatDateTime(detail.occurred_at)}>
                  {formatShortDateTime(detail.occurred_at)}
                </time>
                <span className="tw:min-w-0 tw:break-words">
                  {joinMeta(detail.details?.release_title ?? entryGroupName(detail), messageText(detail)) || subscriptionLogKindLabel(detail.kind)}
                </span>
              </li>
            ))}
          </ul>
          {total > shown || details.data?.next_cursor ? (
            <div className="tw:pl-[30px]">
              <Button size="sm" variant="link" onClick={() => setShowAll(true)}>
                {t("log.batch.viewAll", { n: Math.max(total, shown) })}
              </Button>
            </div>
          ) : null}
        </>
      )}
      {showAll ? <BatchDialog batchKey={batchKey} anime={anime} onRequestClose={() => setShowAll(false)} /> : null}
    </Row>
  );
}

/** The full paginated detail list of one batch, newest first. */
function BatchDialog({
  batchKey,
  anime,
  onRequestClose,
}: {
  batchKey: string;
  anime?: string;
  onRequestClose: () => void;
}) {
  const query = useInfiniteQuery({
    queryKey: observabilityKeys.releaseBatch(`${batchKey}:all`),
    queryFn: ({ pageParam, signal }) => listReleaseBatchEntries(batchKey, { limit: 50, cursor: pageParam }, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor ?? undefined,
  });
  const entries = (query.data?.pages ?? []).flatMap((page) => page.records).flatMap((record) => (record.record_type === "business" ? [record.entry] : []));
  return (
    <Dialog open tier="M" fixedHeight onRequestClose={onRequestClose} title={joinMeta(anime, t("log.batch.detailsTitle")) ?? t("log.batch.detailsTitle")}>
      <ul className={SUB_LIST}>
        {entries.map((detail) => (
          <li key={detail.id} className="tw:flex tw:flex-wrap tw:gap-x-2">
            <time className="tw:tabular-nums tw:text-muted-foreground" dateTime={detail.occurred_at} title={formatDateTime(detail.occurred_at)}>
              {formatShortDateTime(detail.occurred_at)}
            </time>
            <span className="tw:min-w-0 tw:break-words">
              {joinMeta(detail.details?.release_title ?? entryGroupName(detail), messageText(detail)) || subscriptionLogKindLabel(detail.kind)}
            </span>
          </li>
        ))}
      </ul>
      {query.hasNextPage ? (
        <Button size="sm" className="tw:mt-2" loading={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>
          {query.isFetchingNextPage ? t("log.loadingMore") : t("log.loadMore")}
        </Button>
      ) : null}
      {entries.length === 0 && !query.isLoading ? <p className={META}>{t("log.batch.empty")}</p> : null}
    </Dialog>
  );
}

function EntryItem({
  entry,
  anime,
  onOpenAnimeMetadata,
}: {
  entry: BusinessEntry;
  anime?: string;
  onOpenAnimeMetadata?: (managedAnimeId: string) => void;
}) {
  const view = entryView(entry, anime);
  const code = entry.reason_code?.trim().toLowerCase();
  const hold = code === "metadata_incomplete" || code === "metadata_hold" ? metadataBlocking(entry.details?.metadata_missing) : undefined;
  const reason = hold ? hold.long : view.reason;
  return (
    <Row
      title={view.title}
      result={view.result}
      tone={stateTone(view.state)}
      time={entry.occurred_at}
      meta={view.meta}
      reason={reason}
      reasonAction={
        hold?.fix && onOpenAnimeMetadata ? (
          <Button size="sm" variant="link" className="tw:flex-none" onClick={() => onOpenAnimeMetadata(entry.managed_anime_id)}>
            {hold.fix.label}
          </Button>
        ) : undefined
      }
    />
  );
}

function adapterLabel(kind: Extract<ObservabilityRecord, { record_type: "plugin" }>["delivery"]["adapter_kind"]): string {
  switch (kind) {
    case "telegram":
      return t("settings.notifications.adapter.telegram");
    case "feishu":
      return t("settings.notifications.adapter.feishu");
    default:
      return t("settings.notifications.adapter.webhook");
  }
}

function DeliveryItem({
  delivery,
  targetName,
  anime,
  onOpen,
}: {
  delivery: Extract<ObservabilityRecord, { record_type: "plugin" }>["delivery"];
  targetName?: string;
  anime?: string;
  onOpen: () => void;
}) {
  const view = deliveryView(delivery);
  const trigger = notificationTriggerKindLabel(delivery.trigger_kind);
  const facts = delivery.payload_status === "available" ? delivery.payload?.facts : undefined;
  const factTitle = facts?.["anime.title"];
  const subject = typeof factTitle === "string" && factTitle.trim() !== "" ? factTitle : anime;
  const rawEpisode = facts?.["ep.number"];
  const episode = typeof rawEpisode === "number" ? rawEpisode : typeof rawEpisode === "string" && /^\d+$/.test(rawEpisode) ? Number(rawEpisode) : undefined;
  const episodeText = typeof episode === "number" && episode > 0 ? t("records.episode", { n: episode }) : undefined;
  const adapter = adapterLabel(delivery.adapter_kind);
  const target = targetName ? t("log.delivery.targetWithAdapter", { name: targetName, adapter }) : adapter;
  return (
    <Row
      title={delivery.delivery_kind === "test" ? t("log.delivery.test") : joinMeta(subject, episodeText, trigger)}
      result={view.result}
      tone={stateTone(view.state, view.unconfirmed)}
      busy={view.state === "in_progress"}
      time={delivery.updated_at ?? delivery.created_at}
      meta={t("log.delivery.toTarget", { target })}
      reason={view.reason}
      onActivate={onOpen}
      activateLabel={t("log.openDelivery", { kind: trigger })}
    />
  );
}
