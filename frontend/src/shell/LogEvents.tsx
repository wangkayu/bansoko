import { useEffect, useMemo, useState } from "react";
import { ChevronRight, Clock, Search } from "lucide-react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { cn } from "@/lib/utils";
import { Button, Field, Input, Select } from "../ui/base/controls";
import { HelpTip } from "../ui/base/HelpTip";
import { StatePill, type StatePillTone } from "../ui/StatePill";
import { Popover } from "../ui/base/overlays";
import { SegmentedControl } from "../ui/SegmentedControl";
import { StateBlock } from "../ui/StateBlock";
import {
  listAnime,
  listObservabilityRecords,
  toUserMessage,
  type ObservabilityRecord,
} from "../lib/api/client";
import {
  formatDateTime,
  formatShortDateTime,
  metadataBlocking,
  notificationTriggerKindLabel,
  subscriptionLogKindLabel,
} from "../lib/labels";
import { reasonView } from "../lib/i18n/reasons";
import { animeKeys, observabilityKeys } from "../lib/queryKeys";
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
import { isBadgeRecord, messageText, type LogState } from "./logState";
import { isNewer, latestTime, markLogSeen, recordSeenTime } from "./logSeen";

// Events tab. Each row has three layers: the title ("anime · episode · group") with the result
// fixed on its right, a second line of facts (files, size, replacement), and a third line with
// the reason only when something failed, is unconfirmed or was deferred. On a phone the result
// and time fold under the title. Expanded, a download shows its steps, files and a technical
// detail box.

const PAGE_LIMIT = 50;
const SEARCH_DEBOUNCE_MS = 300;

export type StateFilter = "" | LogState;

/** The events read projection: primary hides the repeated release filtering process; all is the full history. */
type EventView = "primary" | "all";

export type EventFilters = {
  state: StateFilter;
  animeId: string;
  q: string;
  /** datetime-local values; converted to ISO before entering the query. */
  fromLocal: string;
  toLocal: string;
};

export const EMPTY_FILTERS: EventFilters = { state: "", animeId: "", q: "", fromLocal: "", toLocal: "" };

const EVENT_VIEW_OPTIONS = (): { value: EventView; label: string }[] => [
  { value: "primary", label: t("log.view.primary") },
  { value: "all", label: t("log.view.all") },
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
const HEAD = "tw:grid tw:w-full tw:min-w-0 tw:gap-0.5 tw:px-3 tw:pt-2 tw:text-left tw:text-sm";
const TITLE_LINE = "tw:flex tw:min-w-0 tw:flex-wrap tw:items-baseline tw:gap-x-2.5 tw:gap-y-0.5";
const TITLE = "tw:min-w-0 tw:flex-1 tw:font-medium tw:phone:basis-full";
const RESULT = "tw:flex-none tw:whitespace-nowrap tw:text-xs";
const TIME = "tw:flex-none tw:tabular-nums tw:text-xs tw:text-muted-foreground";
const META = "tw:m-0 tw:text-xs tw:text-muted-foreground";
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
  presetAnime,
  active,
  newSince,
  onOpenDelivery,
  onOpenAnimeMetadata,
}: {
  filters: EventFilters;
  onFiltersChange: (next: EventFilters) => void;
  /** The card entry preselected an anime: the mark stays put, like with any filter. */
  presetAnime: boolean;
  /** The dialog is open on this tab: an unfiltered list moves the events seen-mark. */
  active: boolean;
  newSince: string | null;
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
  const unfiltered = filters.state === "" && filters.animeId === "" && q === "" && from === undefined && to === undefined && !presetAnime;
  const patch = (next: Partial<EventFilters>) => onFiltersChange({ ...filters, ...next });
  // Every opening starts on the primary view (LogEvents remounts with the dialog); switching views keeps the filters.
  const [eventView, setEventView] = useState<EventView>("primary");

  return (
    <div className="tw:grid tw:gap-3">
      <div className="tw:grid tw:gap-2">
        <div className="tw:flex tw:items-center tw:gap-1.5">
          <SegmentedControl<EventView> ariaLabel={t("log.view.aria")} value={eventView} options={EVENT_VIEW_OPTIONS()} onChange={setEventView} />
          <HelpTip label={t("log.view.helpLabel")}>{t("log.view.help")}</HelpTip>
        </div>
        <div className="tw:flex tw:items-center tw:gap-2">
          <div className="tw:w-28 tw:flex-none">
            <Select<StateFilter> label={t("log.status")} value={filters.state} options={STATE_OPTIONS()} onChange={(state) => patch({ state })} />
          </div>
          <div className="tw:min-w-0 tw:flex-1">
            <Select
              label={t("log.anime")}
              value={filters.animeId}
              options={[
                { value: "", label: t("log.allAnime") },
                ...(animeList.data ?? []).map((item) => ({
                  value: item.managed_anime.id,
                  label: item.managed_anime.title_override ?? item.managed_anime.title,
                })),
              ]}
              onChange={(animeId) => patch({ animeId })}
            />
          </div>
          <TimeRangePopover from={filters.fromLocal} to={filters.toLocal} onChange={(fromLocal, toLocal) => patch({ fromLocal, toLocal })} />
        </div>
        <Input
          aria-label={t("log.search")}
          placeholder={t("log.searchPlaceholder")}
          leadingIcon={<Search className="tw:size-3.5" />}
          value={filters.q}
          maxLength={100}
          onChange={(event) => patch({ q: event.target.value })}
        />
      </div>
      <EventsList
        key={eventView}
        eventView={eventView}
        onShowAll={() => setEventView("all")}
        state={filters.state}
        animeId={filters.animeId}
        q={q}
        from={from}
        to={to}
        animeNames={animeNames}
        markSeen={active && unfiltered}
        newSince={newSince}
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
        <Button aria-label={labelValue(t("log.timeRange"), summary)} className="tw:min-w-0 tw:max-w-[40%] tw:flex-none">
          <Clock className="tw:size-3.5 tw:flex-none" aria-hidden />
          <span className="tw:min-w-0 tw:truncate">{summary}</span>
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
  onShowAll,
  state,
  animeId,
  q,
  from,
  to,
  animeNames,
  markSeen,
  newSince,
  onOpenDelivery,
  onOpenAnimeMetadata,
}: {
  eventView: EventView;
  onShowAll: () => void;
  state: StateFilter;
  animeId: string;
  q: string;
  from?: string;
  to?: string;
  animeNames: AnimeNames;
  markSeen: boolean;
  newSince: string | null;
  onOpenDelivery: (deliveryID: string) => void;
  onOpenAnimeMetadata?: (managedAnimeId: string) => void;
}) {
  const query = useInfiniteQuery({
    queryKey: observabilityKeys.businessLine(eventView, state, animeId, q, from, to),
    queryFn: ({ pageParam }) =>
      listObservabilityRecords({
        kind: ["business", "plugin"],
        event_view: eventView,
        state: state === "" ? undefined : state,
        q: q === "" ? undefined : q,
        managed_anime_id: animeId === "" ? undefined : animeId,
        from,
        to,
        limit: PAGE_LIMIT,
        cursor: pageParam,
      }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor ?? undefined,
  });
  const items = useMemo(() => groupEvents(query.data?.pages.flatMap((page) => page.records) ?? []), [query.data]);
  const hideAnime = animeId !== "";
  const firstPage = query.data?.pages[0];
  useEffect(() => {
    if (markSeen && firstPage) markLogSeen("records", latestTime(firstPage.records.map(recordSeenTime)));
  }, [markSeen, firstPage]);
  const isNew = (record: ObservabilityRecord) => newSince !== null && isBadgeRecord(record) && isNewer(recordSeenTime(record), newSince);
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
  if (items.length === 0) {
    // An empty primary view says no main event matches the filters and offers the
    // full history; it never claims the instance has no logs at all.
    if (eventView === "primary") {
      return (
        <StateBlock
          kind="empty"
          title={t("log.emptyPrimary")}
          action={
            <Button size="sm" variant="link" onClick={onShowAll}>
              {t("log.showAll")}
            </Button>
          }
        />
      );
    }
    return <StateBlock kind="empty" title={t("log.emptyRecords")} />;
  }

  return (
    <div className="tw:grid tw:gap-2">
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
                isNew={item.steps.some((entry) => isNew({ record_type: "business", entry }))}
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
          const record = item.record;
          if (record.record_type === "business") {
            return (
              <EntryItem
                key={item.key}
                entry={record.entry}
                anime={animeOf(record.entry)}
                isNew={isNew(record)}
                onOpenAnimeMetadata={onOpenAnimeMetadata}
              />
            );
          }
          if (record.record_type === "plugin") {
            return (
              <DeliveryItem
                key={item.key}
                delivery={record.delivery}
                anime={hideAnime || !record.delivery.managed_anime_id ? undefined : animeNames.get(record.delivery.managed_anime_id)}
                isNew={isNew(record)}
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
  const head = (
    <>
      <span className={TITLE_LINE}>
        <span className={TITLE} title={titleHint ?? title}>
          {onToggle ? (
            <ChevronRight aria-hidden className={cn("tw:mr-1 tw:inline tw:size-3.5 tw:align-[-2px] tw:transition-transform", expanded && "tw:rotate-90")} />
          ) : null}
          {title}
          {tags}
        </span>
        <span className={RESULT}>
          <StatePill tone={tone} busy={busy}>
            {result}
          </StatePill>
        </span>
        <time className={TIME} dateTime={time} title={formatDateTime(time)}>
          {formatShortDateTime(time)}
        </time>
      </span>
      {meta ? <span className={cn(META, onToggle && "tw:pl-[18px]")}>{meta}</span> : null}
    </>
  );
  const pad = "tw:pb-2";
  return (
    <li className={ITEM}>
      {onToggle || onActivate ? (
        <button
          type="button"
          aria-expanded={onToggle ? expanded : undefined}
          aria-label={activateLabel}
          onClick={onToggle ?? onActivate}
          className={cn(HEAD, "tw:cursor-pointer tw:hover:bg-muted/50", reason ? "tw:pb-0.5" : pad, FOCUS)}
        >
          {head}
        </button>
      ) : (
        <div className={cn(HEAD, reason ? "tw:pb-0.5" : pad)}>{head}</div>
      )}
      {reason ? (
        <div className={cn("tw:flex tw:flex-wrap tw:items-center tw:gap-x-2 tw:px-3 tw:pb-2", onToggle && "tw:pl-[30px]")}>
          <p className={cn(META, "tw:min-w-0 tw:break-words")}>{t("log.reason.line", { reason })}</p>
          {reasonAction}
        </div>
      ) : null}
      {expanded ? children : null}
    </li>
  );
}

function NewMark() {
  return <span className="tw:ml-1.5 tw:text-xs tw:font-medium tw:text-primary">{t("log.new")}</span>;
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
  isNew,
}: {
  steps: BusinessEntry[];
  anime?: string;
  expanded: boolean;
  onToggle: () => void;
  isNew: boolean;
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
          {isNew && <NewMark />}
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
  const count = first.kind === "release_processed" ? t("log.groupReleases", { n: entries.length }) : t("log.groupEvents", { n: entries.length });
  return (
    <Row
      title={joinMeta(anime, count)}
      result={view.result}
      tone={stateTone(view.state)}
      time={first.occurred_at}
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

function EntryItem({
  entry,
  anime,
  isNew,
  onOpenAnimeMetadata,
}: {
  entry: BusinessEntry;
  anime?: string;
  isNew: boolean;
  onOpenAnimeMetadata?: (managedAnimeId: string) => void;
}) {
  const view = entryView(entry, anime);
  const code = entry.reason_code?.trim().toLowerCase();
  const hold = code === "metadata_incomplete" || code === "metadata_hold" ? metadataBlocking(entry.details?.metadata_missing) : undefined;
  const reason = hold ? hold.long : view.reason;
  return (
    <Row
      title={view.title}
      tags={isNew ? <NewMark /> : undefined}
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

function DeliveryItem({
  delivery,
  anime,
  isNew,
  onOpen,
}: {
  delivery: Extract<ObservabilityRecord, { record_type: "plugin" }>["delivery"];
  anime?: string;
  isNew: boolean;
  onOpen: () => void;
}) {
  const view = deliveryView(delivery);
  const trigger = notificationTriggerKindLabel(delivery.trigger_kind);
  return (
    <Row
      title={joinMeta(delivery.target_display, trigger, anime)}
      tags={isNew ? <NewMark /> : undefined}
      result={view.result}
      tone={stateTone(view.state, view.unconfirmed)}
      busy={view.state === "in_progress"}
      time={delivery.updated_at ?? delivery.created_at}
      reason={view.reason}
      onActivate={onOpen}
      activateLabel={t("log.openDelivery", { kind: trigger })}
    />
  );
}
