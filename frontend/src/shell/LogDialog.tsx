import { useEffect, useMemo, useState } from "react";
import { ChevronRight, Clock } from "lucide-react";
import { cn } from "@/lib/utils";
import { Dialog } from "../ui/base/Dialog";
import { Button, Field, Input, Select, Tabs } from "../ui/base/controls";
import { StatePill } from "../ui/StatePill";
import { Popover } from "../ui/base/overlays";
import { StateBlock } from "../ui/StateBlock";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { DeliveryDetailDialog } from "@/features/settings/DeliveryDetailDialog";
import {
  listAnime,
  listObservabilityDiagnostics,
  listObservabilityRecords,
  toUserMessage,
  type DiagnosticEpisode,
  type ObservabilityRecord,
} from "../lib/api/client";
import {
  formatDateTime,
  formatShortDateTime,
  metadataBlocking,
  notificationTriggerKindLabel,
  subscriptionLogKindLabel,
  type BlockingPhrases,
} from "../lib/labels";
import { DIAGNOSTIC_COMPONENTS } from "../lib/observabilityCatalog.generated";
import { useLogDialogStore } from "./logDialogStore";
import {
  entryGroupName,
  groupEvents,
  policyChangeText,
  runView,
  stepText,
  type BusinessEntry,
} from "./logRuns";
import {
  diagnosticSeenTime,
  isFailureRecord,
  isNewer,
  isOpenError,
  latestTime,
  markLogSeen,
  readLogSeen,
  recordSeenTime,
} from "./logSeen";
import { reasonView } from "../lib/i18n/reasons";
import { useBoundedRefresh } from "../lib/events/useBoundedRefresh";
import { ReasonText } from "../ui/ReasonText";
import { animeKeys, observabilityKeys } from "../lib/queryKeys";
import { t, tryT } from "../lib/i18n";
import { labelValue } from "../lib/i18n/format";
import { ErrorDetails } from "../ui/base/ErrorDetails";

// LogDialog — the single global observability entry (top-bar LogDialog with two
// tabs: events and diagnostics). Events carry only the business mainline
// (business history + plugin delivery results); routine/automation execution
// evidence stays internal (no long list). Diagnostics get only the small
// severity/component filters. Filters compress into one row and the time range
// folds into one popover (two datetime-local inputs mapping straight onto the
// contract's from/to). The "status" filter of the two tabs is only a display
// merge: events map to outcome, diagnostics to severity; the query parameter
// semantics do not change.
//
// Events are one compact line each: status, time, kind, title (truncated),
// result. Adjacent events with the same kind, status, anime and result collapse
// into one counted row ("14 seeds"), expandable.
//
// Seen markers (this browser only, see logSeen.ts): when a tab is open and
// unfiltered, rendering the first page marks that tab at the time of the newest
// entry in the list, and the entry badge resets. Failures newer than the mark
// from before this opening carry a "new" tag.

const PAGE_LIMIT = 50;

type OutcomeFilter = "" | "success" | "failure" | "info";

type SeverityFilter = "" | DiagnosticEpisode["severity"];

const OUTCOME_OPTIONS = (): { value: OutcomeFilter; label: string }[] => [
  { value: "", label: t("log.allStatus") },
  { value: "success", label: t("log.success") },
  { value: "failure", label: t("log.failure") },
  { value: "info", label: t("log.info") },
];


const SEVERITY_OPTIONS = (): { value: SeverityFilter; label: string }[] => [
  { value: "", label: t("log.allStatus") },
  { value: "warning", label: t("log.warning") },
  { value: "error", label: t("log.error") },
];

function componentLabel(component: string): string {
  return tryT(`log.component.${component}`) ?? component;
}

function operationLabel(operation: string): string {
  return tryT(`log.operation.${operation}`) ?? operation;
}

const COMPONENT_OPTIONS = (): { value: string; label: string }[] => [
  { value: "", label: t("log.allTypes") },
  ...DIAGNOSTIC_COMPONENTS.map((value) => ({ value, label: componentLabel(value) })),
];

type LogDialogProps = {
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  /** Controlled view (`records` | `diagnostics`); the route-hosted log keeps it in `?log=`. */
  view?: string;
  onViewChange?: (view: string) => void;
  /** Opens an anime's config on the metadata tab (fix action on a blocked-on-metadata event). */
  onOpenAnimeMetadata?: (managedAnimeId: string) => void;
};

/** Processing-chain entry: a release id + the owner id the record declares (for lifecycle lookup). */

export function LogDialog({ open: controlledOpen, onOpenChange, view, onViewChange, onOpenAnimeMetadata }: LogDialogProps = {}) {
  const storeOpen = useLogDialogStore((state) => state.open);
  const open = controlledOpen ?? storeOpen;
  const presetAnimeId = useLogDialogStore((state) => state.managedAnimeId);
  const closeDialog = useLogDialogStore((state) => state.closeDialog);
  const [localTab, setLocalTab] = useState("records");
  const tab = view ?? localTab;
  const setTab = onViewChange ?? setLocalTab;
  const [outcome, setOutcome] = useState<OutcomeFilter>("");
  const [animeId, setAnimeId] = useState("");
  const [severity, setSeverity] = useState<SeverityFilter>("");
  const [component, setComponent] = useState("");
  // The two tabs share one time range; datetime-local local values convert to ISO before entering the query.
  const [fromLocal, setFromLocal] = useState("");
  const [toLocal, setToLocal] = useState("");
  const [deliveryID, setDeliveryID] = useState<string>();
  const from = localToIso(fromLocal);
  const to = localToIso(toLocal);
  // The "seen" marks from before this opening: snapshot them on open to tag
  // entries as "new"; cleared on close.
  const [openedMarks, setOpenedMarks] = useState<{ records: string | null; diagnostics: string | null } | null>(null);
  if (open && openedMarks === null) setOpenedMarks({ records: readLogSeen("records"), diagnostics: readLogSeen("diagnostics") });
  if (!open && openedMarks !== null) setOpenedMarks(null);
  // Only seeing the complete list counts as seen: any filter (including the card
  // entry's preselected anime) leaves the mark alone.
  const unfilteredRange = from === undefined && to === undefined;
  const recordsSeenNow =
    open && tab === "records" && unfilteredRange && outcome === "" && animeId === "" && !presetAnimeId;
  const diagnosticsSeenNow = open && tab === "diagnostics" && unfilteredRange && severity === "" && component === "";
  // Follow realtime events while open (rss/records invalidations refresh the
  // records); low-frequency polling when the stream is down.
  useBoundedRefresh(open ? [observabilityKeys.all] : []);

  // Every opening pours the card entry's preselected anime into the filter; the
  // top-bar entry means all anime.
  useEffect(() => {
    if (open) setAnimeId(presetAnimeId ?? "");
  }, [open, presetAnimeId]);

  const handleOpenChange = (next: boolean) => {
    onOpenChange?.(next);
    if (!next) {
      setDeliveryID(undefined);
      closeDialog();
    }
  };

  const recordsContent = (
    <div className={SECTION}>
      <div className={FILTERS}>
        <div className={FILTER_STATUS}>
          <Select label={t("log.status")} value={outcome} options={OUTCOME_OPTIONS()} onChange={setOutcome} />
        </div>
        <AnimeFilterSelect value={animeId} onChange={setAnimeId} />
        <TimeRangePopover from={fromLocal} to={toLocal} onFromChange={setFromLocal} onToChange={setToLocal} />
      </div>
      <BusinessRecordsList
        outcome={outcome}
        animeId={animeId}
        from={from}
        to={to}
        onOpenDelivery={setDeliveryID}
        onOpenAnimeMetadata={onOpenAnimeMetadata}
        markSeen={recordsSeenNow}
        newSince={openedMarks?.records ?? null}
      />
    </div>
  );

  const diagnosticsContent = (
    <div className={SECTION}>
      <div className={FILTERS}>
        <div className={FILTER_STATUS}>
          <Select<SeverityFilter> label={t("log.status")} value={severity} options={SEVERITY_OPTIONS()} onChange={setSeverity} />
        </div>
        <div className={FILTER_GROW}>
          <Select label={t("log.type")} value={component} options={COMPONENT_OPTIONS()} onChange={setComponent} />
        </div>
        <TimeRangePopover from={fromLocal} to={toLocal} onFromChange={setFromLocal} onToChange={setToLocal} />
      </div>
      <DiagnosticsList
        severity={severity}
        component={component}
        from={from}
        to={to}
        markSeen={diagnosticsSeenNow}
        newSince={openedMarks?.diagnostics ?? null}
      />
    </div>
  );

  return (
    <>
      <Dialog
        open={open}
        onRequestClose={() => handleOpenChange(false)}
        tier="M"
        fixedHeight
        focusTitle
        title={t("log.title")}
        bodyClassName="tw:pt-2"
      >
        <Tabs
          value={tab}
          onChange={setTab}
          items={[
            { key: "records", label: t("log.tabRecords"), children: recordsContent },
            { key: "diagnostics", label: t("log.tabDiagnostics"), children: diagnosticsContent },
          ]}
        />
        {/* Rendered inside the log dialog so they stack as the nested layer and Esc closes only them. */}
        {deliveryID ? <DeliveryDetailDialog deliveryID={deliveryID} onClose={() => setDeliveryID(undefined)} /> : null}
      </Dialog>
    </>
  );
}

// Shared layout for the two tab bodies: one filter row above the list.
const SECTION = "tw:grid tw:gap-3";
const FILTERS = "tw:flex tw:items-center tw:gap-2";
const FILTER_STATUS = "tw:w-28 tw:flex-none";
const FILTER_GROW = "tw:min-w-0 tw:flex-1";
const LIST = "tw:m-0 tw:grid tw:list-none tw:gap-2 tw:p-0";
// Events: one compact line per event (phone: meta + result on line one, title on line two).
const EVENT_LIST = "tw:m-0 tw:list-none tw:p-0 tw:rounded-[10px] tw:border tw:border-border tw:bg-card tw:overflow-hidden";
const EVENT_ROW =
  "tw:flex tw:w-full tw:min-w-0 tw:items-center tw:gap-x-2.5 tw:gap-y-0.5 tw:px-3 tw:py-1.5 tw:text-sm tw:text-left tw:phone:flex-wrap";
const EVENT_ITEM = "tw:border-b tw:border-border tw:last:border-b-0";
const EVENT_TIME = "tw:flex-none tw:tabular-nums tw:text-xs tw:text-muted-foreground";
const EVENT_KIND = "tw:flex-none tw:text-xs tw:text-muted-foreground";
const EVENT_TITLE = "tw:min-w-0 tw:flex-1 tw:truncate tw:font-medium tw:phone:order-last tw:phone:basis-full";
const EVENT_RESULT = "tw:min-w-0 tw:max-w-[45%] tw:truncate tw:text-xs tw:text-muted-foreground tw:phone:max-w-none tw:phone:flex-1";
const ITEM = "tw:grid tw:gap-1 tw:rounded-[10px] tw:border tw:border-border tw:bg-card tw:px-3 tw:py-2.5 tw:text-sm";
const META = "tw:m-0 tw:text-xs tw:text-muted-foreground";
const ITEM_HEAD = "tw:flex tw:flex-wrap tw:items-center tw:gap-x-2 tw:gap-y-1";

/** Anime filter: options come from the existing listAnime query, values are the contract managed_anime_id. */
function AnimeFilterSelect({ value, onChange }: { value: string; onChange: (next: string) => void }) {
  const anime = useQuery({ queryKey: animeKeys.list, queryFn: listAnime });
  return (
    <div className={FILTER_GROW}>
      <Select
        label={t("log.anime")}
        value={value}
        options={[
          { value: "", label: t("log.allAnime") },
          ...(anime.data ?? []).map((item) => ({
            value: item.managed_anime.id,
            label: item.managed_anime.title_override ?? item.managed_anime.title,
          })),
        ]}
        onChange={onChange}
      />
    </div>
  );
}

/** datetime-local local value → contract date-time; empty or invalid values do not filter. */
function localToIso(value: string): string | undefined {
  if (value === "") return undefined;
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? undefined : parsed.toISOString();
}

/** The time range folds into one popover: the trigger button shows the current range, two datetime-local inputs plus a clear. */
function TimeRangePopover({
  from,
  to,
  onFromChange,
  onToChange,
}: {
  from: string;
  to: string;
  onFromChange: (next: string) => void;
  onToChange: (next: string) => void;
}) {
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
        <Button aria-label={labelValue(t("log.timeRange"), summary)} className="tw:min-w-0 tw:max-w-[45%] tw:flex-none">
          <Clock className="tw:size-3.5 tw:flex-none" aria-hidden />
          <span className="tw:min-w-0 tw:truncate">{summary}</span>
        </Button>
      }
    >
      <div className="tw:grid tw:w-64 tw:max-w-full tw:gap-2">
        <Field label={t("log.startTime")}>
          <Input
            aria-label={t("log.startTime")}
            type="datetime-local"
            value={from}
            onChange={(event) => onFromChange(event.target.value)}
          />
        </Field>
        <Field label={t("log.endTime")}>
          <Input
            aria-label={t("log.endTime")}
            type="datetime-local"
            value={to}
            onChange={(event) => onToChange(event.target.value)}
          />
        </Field>
        <Button
          size="sm"
          variant="link"
          className="tw:justify-self-end"
          disabled={!from && !to}
          onClick={() => {
            onFromChange("");
            onToChange("");
          }}
        >
          {t("log.clearTime")}
        </Button>
      </div>
    </Popover>
  );
}

function BusinessRecordsList({
  outcome,
  animeId,
  from,
  to,
  onOpenDelivery,
  onOpenAnimeMetadata,
  markSeen,
  newSince,
}: {
  outcome: OutcomeFilter;
  animeId: string;
  from?: string;
  to?: string;
  onOpenDelivery: (deliveryID: string) => void;
  onOpenAnimeMetadata?: (managedAnimeId: string) => void;
  markSeen: boolean;
  newSince: string | null;
}) {
  const query = useInfiniteQuery({
    queryKey: observabilityKeys.businessLine(outcome, animeId, from, to),
    queryFn: ({ pageParam }) =>
      listObservabilityRecords({
        kind: ["business", "plugin"],
        outcome: outcome === "" ? undefined : outcome,
        managed_anime_id: animeId === "" ? undefined : animeId,
        from,
        to,
        limit: PAGE_LIMIT,
        cursor: pageParam,
      }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor ?? undefined,
  });
  const items = useMemo(
    () => groupEvents(query.data?.pages.flatMap((page) => page.records) ?? [], businessResultText),
    [query.data],
  );
  // Anime names come from the anime list (the same query as the anime filter); when filtered to one anime the rows do not repeat it.
  const animeList = useQuery({ queryKey: animeKeys.list, queryFn: listAnime });
  const animeNames = useMemo<AnimeNames>(
    () => new Map((animeList.data ?? []).map((item) => [item.managed_anime.id, item.managed_anime.title_override ?? item.managed_anime.title])),
    [animeList.data],
  );
  const hideAnime = animeId !== "";
  const firstPage = query.data?.pages[0];
  useEffect(() => {
    if (markSeen && firstPage) markLogSeen("records", latestTime(firstPage.records.map(recordSeenTime)));
  }, [markSeen, firstPage]);
  const isNew = (record: ObservabilityRecord) =>
    newSince !== null && isFailureRecord(record) && isNewer(recordSeenTime(record), newSince);
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set());
  const toggle = (key: string) =>
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });

  if (query.error) {
    return (
      <StateBlock kind="error"
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
    return <StateBlock kind="empty" title={t("log.emptyRecords")} />;
  }

  return (
    <div className="tw:grid tw:gap-2">
      <ul aria-label={t("log.recordsAria")} className={EVENT_LIST}>
        {items.map((item) =>
          item.type === "run" ? (
            <RunItem
              key={item.key}
              steps={item.steps}
              expanded={expanded.has(item.key)}
              onToggle={() => toggle(item.key)}
              isNew={item.steps.some((entry) => isNew({ record_type: "business", entry }))}
              anime={hideAnime ? undefined : animeName(item.steps[item.steps.length - 1]!, animeNames)}
            />
          ) : item.type === "group" ? (
            <EventGroup
              key={item.key}
              entries={item.entries}
              expanded={expanded.has(item.key)}
              onToggle={() => toggle(item.key)}
              isNew={isNew({ record_type: "business", entry: item.entries[0]! })}
              anime={hideAnime ? undefined : animeName(item.entries[0]!, animeNames)}
              onOpenAnimeMetadata={onOpenAnimeMetadata}
            />
          ) : (
            <BusinessRecordItem
              key={item.key}
              record={item.record}
              isNew={isNew(item.record)}
              anime={
                item.record.record_type === "business" && !hideAnime ? animeName(item.record.entry, animeNames) : undefined
              }
              onOpenDelivery={onOpenDelivery}
              onOpenAnimeMetadata={onOpenAnimeMetadata}
            />
          ),
        )}
      </ul>
      {query.hasNextPage && (
        <Button
          size="sm"
          className="tw:justify-self-center"
          loading={query.isFetchingNextPage}
          onClick={() => void query.fetchNextPage()}
        >
          {query.isFetchingNextPage ? t("log.loadingMore") : t("log.loadMore")}
        </Button>
      )}
    </div>
  );
}

/** A group of identical-result events: the title slot is an expandable count, expanded to list each entry. */
function EventGroup({
  entries,
  expanded,
  onToggle,
  isNew,
  anime,
  onOpenAnimeMetadata,
}: {
  entries: BusinessEntry[];
  expanded: boolean;
  onToggle: () => void;
  isNew: boolean;
  /** Anime name; not passed when filtered to one anime. */
  anime?: string;
  onOpenAnimeMetadata?: (managedAnimeId: string) => void;
}) {
  const first = entries[0]!;
  const count = first.kind === "release_processed" ? t("log.groupReleases", { n: entries.length }) : t("log.groupEvents", { n: entries.length });
  const title = joinMeta(anime, count);
  return (
    <li className={EVENT_ITEM}>
      <div className={EVENT_ROW}>
        <OutcomeTag outcome={first.outcome}>{businessOutcomeLabel(first.outcome)}</OutcomeTag>
        {isNew && <NewMark />}
        <time className={EVENT_TIME} dateTime={first.occurred_at} title={formatDateTime(first.occurred_at)}>
          {formatShortDateTime(first.occurred_at)}
        </time>
        <span className={EVENT_KIND}>{subscriptionLogKindLabel(first.kind)}</span>
        <button
          type="button"
          aria-expanded={expanded}
          onClick={onToggle}
          className={cn(
            EVENT_TITLE,
            "tw:inline-flex tw:items-center tw:gap-1 tw:rounded-sm tw:text-left tw:hover:text-primary tw:focus-visible:outline-2 tw:focus-visible:outline-ring",
          )}
        >
          <ChevronRight aria-hidden className={cn("tw:size-3.5 tw:flex-none tw:transition-transform", expanded && "tw:rotate-90")} />
          <span className="tw:truncate">{title}</span>
        </button>
        <BusinessResult entry={first} onOpenAnimeMetadata={onOpenAnimeMetadata} />
      </div>
      {expanded && (
        <ul className="tw:m-0 tw:list-none tw:border-t tw:border-border tw:bg-muted/30 tw:p-0 tw:pl-6">
          {entries.map((entry) => {
            const subject = entrySubject(entry, undefined) || businessResultText(entry);
            return (
              <li key={entry.id} className={EVENT_ITEM}>
                <div className={EVENT_ROW}>
                  <time className={EVENT_TIME} dateTime={entry.occurred_at} title={formatDateTime(entry.occurred_at)}>
                    {formatShortDateTime(entry.occurred_at)}
                  </time>
                  <span className={EVENT_TITLE} title={subject}>
                    {subject}
                  </span>
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </li>
  );
}

/** Renders only the two business-mainline kinds; the query kind keeps routine/automation out. */
function BusinessRecordItem({
  record,
  isNew,
  anime,
  onOpenDelivery,
  onOpenAnimeMetadata,
}: {
  record: ObservabilityRecord;
  isNew: boolean;
  /** Anime name; not passed when filtered to one anime. */
  anime?: string;
  onOpenDelivery: (deliveryID: string) => void;
  onOpenAnimeMetadata?: (managedAnimeId: string) => void;
}) {
  if (record.record_type === "business") {
    const entry = record.entry;
    const subject = entrySubject(entry, anime);
    return (
      <li className={EVENT_ITEM}>
        <div className={EVENT_ROW}>
          <OutcomeTag outcome={entry.outcome}>{businessOutcomeLabel(entry.outcome)}</OutcomeTag>
          {isNew && <NewMark />}
          <time className={EVENT_TIME} dateTime={entry.occurred_at} title={formatDateTime(entry.occurred_at)}>
            {formatShortDateTime(entry.occurred_at)}
          </time>
          <span className={EVENT_KIND}>{subscriptionLogKindLabel(entry.kind)}</span>
          {subject ? (
            <span className={EVENT_TITLE} title={subject}>
              {subject}
            </span>
          ) : null}
          <BusinessResult entry={entry} asTitle={!subject} onOpenAnimeMetadata={onOpenAnimeMetadata} />
        </div>
      </li>
    );
  }
  if (record.record_type === "plugin") {
    const delivery = record.delivery;
    const outcome =
      delivery.state === "delivered" ? "success" : delivery.state === "failed" || delivery.state === "unknown" ? "failure" : "info";
    const trigger = notificationTriggerKindLabel(delivery.trigger_kind);
    return (
      <li className={EVENT_ITEM}>
        <button
          type="button"
          className={cn(EVENT_ROW, "tw:cursor-pointer tw:hover:bg-muted/50 tw:focus-visible:outline-2 tw:focus-visible:-outline-offset-2 tw:focus-visible:outline-ring")}
          onClick={() => onOpenDelivery(delivery.id)}
          aria-label={t("log.openDelivery", { kind: trigger })}
        >
          <OutcomeTag outcome={outcome}>{businessOutcomeLabel(outcome)}</OutcomeTag>
          {isNew && <NewMark />}
          <time className={EVENT_TIME} dateTime={delivery.updated_at} title={formatDateTime(delivery.updated_at)}>
            {formatShortDateTime(delivery.updated_at)}
          </time>
          <span className={EVENT_KIND}>{t("log.tagDelivery")}</span>
          <span className={EVENT_TITLE}>{trigger}</span>
          <span className={EVENT_RESULT} title={delivery.target_display}>
            {delivery.target_display}
          </span>
        </button>
      </li>
    );
  }
  return null;
}

/**
 * One download per row: the title is "[anime ·] group · episode N", the result is the final
 * result (failures carry a reason) and the tone follows the final result. Expanded, the seed
 * title comes first, then each step's specifics in time order.
 */
function RunItem({
  steps,
  expanded,
  onToggle,
  isNew,
  anime,
}: {
  steps: BusinessEntry[];
  expanded: boolean;
  onToggle: () => void;
  isNew: boolean;
  /** Anime name; not passed when filtered to one anime. */
  anime?: string;
}) {
  const view = runView(steps);
  const named = Boolean(view.groupName || view.episodes);
  const subject = named ? joinMeta(anime, view.groupName, view.episodes) : (view.releaseTitle ?? anime ?? t("log.run.kind"));
  const latest = view.latest;
  return (
    <li className={EVENT_ITEM}>
      <div className={EVENT_ROW}>
        <OutcomeTag outcome={view.outcome}>{businessOutcomeLabel(view.outcome)}</OutcomeTag>
        {isNew && <NewMark />}
        <time className={EVENT_TIME} dateTime={latest.occurred_at} title={formatDateTime(latest.occurred_at)}>
          {formatShortDateTime(latest.occurred_at)}
        </time>
        <span className={EVENT_KIND}>{t("log.run.kind")}</span>
        <button
          type="button"
          aria-expanded={expanded}
          onClick={onToggle}
          title={view.releaseTitle}
          className={cn(
            EVENT_TITLE,
            "tw:inline-flex tw:items-center tw:gap-1 tw:rounded-sm tw:text-left tw:hover:text-primary tw:focus-visible:outline-2 tw:focus-visible:outline-ring",
          )}
        >
          <ChevronRight aria-hidden className={cn("tw:size-3.5 tw:flex-none tw:transition-transform", expanded && "tw:rotate-90")} />
          <span className="tw:truncate">{subject}</span>
        </button>
        <span className={EVENT_RESULT}>
          {view.result}
          {view.outcome === "failure" ? <FailureReason entry={view.decisive} /> : null}
        </span>
      </div>
      {expanded && (
        <ul aria-label={t("log.run.stepsAria")} className="tw:m-0 tw:list-none tw:border-t tw:border-border tw:bg-muted/30 tw:p-0 tw:pl-6">
          {view.releaseTitle && named ? (
            <li className={EVENT_ITEM}>
              <div className={EVENT_ROW}>
                <span className={cn(EVENT_TITLE, "tw:font-normal tw:text-muted-foreground")} title={view.releaseTitle}>
                  {view.releaseTitle}
                </span>
              </div>
            </li>
          ) : null}
          {steps.map((step) => {
            const text = stepText(step);
            return (
              <li key={step.id} className={EVENT_ITEM}>
                <div className={EVENT_ROW}>
                  <OutcomeTag outcome={step.outcome}>{businessOutcomeLabel(step.outcome)}</OutcomeTag>
                  <time className={EVENT_TIME} dateTime={step.occurred_at} title={formatDateTime(step.occurred_at)}>
                    {formatShortDateTime(step.occurred_at)}
                  </time>
                  <span className={EVENT_KIND}>{subscriptionLogKindLabel(step.kind)}</span>
                  <span className={cn(EVENT_TITLE, "tw:font-normal")} title={text}>
                    {text}
                    <FailureReason entry={step} />
                  </span>
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </li>
  );
}

/** Result column: a block writes the long-form reason with a fix action, otherwise a summary plus the failure reason. */
function BusinessResult({
  entry,
  asTitle = false,
  onOpenAnimeMetadata,
}: {
  entry: BusinessEntry;
  /** With no concrete subject to write (a settings change filtered to one anime etc.), the result takes the title slot. */
  asTitle?: boolean;
  onOpenAnimeMetadata?: (managedAnimeId: string) => void;
}) {
  const hold = holdPhrases(entry);
  const text = businessResultText(entry);
  return (
    <>
      <span className={asTitle ? EVENT_TITLE : EVENT_RESULT} title={text}>
        {hold ? (
          hold.long
        ) : (
          <>
            {policyChangeText(entry) ?? entry.summary}
            <FailureReason entry={entry} />
          </>
        )}
      </span>
      {hold?.fix && onOpenAnimeMetadata ? (
        <Button size="sm" variant="link" className="tw:flex-none" onClick={() => onOpenAnimeMetadata(entry.managed_anime_id)}>
          {hold.fix.label}
        </Button>
      ) : null}
    </>
  );
}

function OutcomeTag({ outcome, children }: { outcome: "success" | "failure" | "info"; children: React.ReactNode }) {
  return (
    <span className="tw:flex-none">
      <StatePill tone={outcome === "success" ? "online" : outcome === "failure" ? "offline" : "muted"}>{children}</StatePill>
    </span>
  );
}

/** A failure not seen before this opening: a small mark valid only while this opening lasts. */
function NewMark() {
  return <span className="tw:flex-none tw:text-xs tw:font-medium tw:text-primary">{t("log.new")}</span>;
}

function DiagnosticsList({
  severity,
  component,
  from,
  to,
  markSeen,
  newSince,
}: {
  severity: SeverityFilter;
  component: string;
  from?: string;
  to?: string;
  markSeen: boolean;
  newSince: string | null;
}) {
  const query = useInfiniteQuery({
    queryKey: observabilityKeys.diagnosticsFiltered(severity, component, from, to),
    queryFn: ({ pageParam }) =>
      listObservabilityDiagnostics({
        severity: severity === "" ? undefined : severity,
        component: component === "" ? undefined : component,
        from,
        to,
        limit: PAGE_LIMIT,
        cursor: pageParam,
      }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor ?? undefined,
  });
  const firstPage = query.data?.pages[0];
  useEffect(() => {
    if (markSeen && firstPage) markLogSeen("diagnostics", latestTime(firstPage.episodes.map(diagnosticSeenTime)));
  }, [markSeen, firstPage]);

  if (query.error) {
    return (
      <StateBlock kind="error"
        title={toUserMessage(query.error, t("log.loadDiagnosticsFailed"))}
        action={
          <Button size="sm" variant="link" onClick={() => void query.refetch()}>
            {t("log.retry")}
          </Button>
        }
      />
    );
  }
  if (!query.data) return <StateBlock kind="busy" title={t("log.loadingDiagnostics")} />;
  const episodes = query.data.pages.flatMap((page) => page.episodes);
  if (episodes.length === 0 && !query.hasNextPage) {
    return <StateBlock kind="empty" title={t("log.emptyDiagnostics")} />;
  }

  return (
    <div className="tw:grid tw:gap-2">
      <ul aria-label={t("log.diagnosticsAria")} className={LIST}>
        {episodes.map((episode) => (
          <li key={episode.id} className={ITEM}>
            <div className={ITEM_HEAD}>
              <StatePill tone={episode.severity === "error" ? "offline" : "warning"}>
                {episode.severity === "error" ? t("log.error") : t("log.warning")}
              </StatePill>
              {newSince !== null && isOpenError(episode) && isNewer(episode.last_failed_at, newSince) && <NewMark />}
              <strong className="tw:font-semibold">
                {componentLabel(episode.component)} · {operationLabel(episode.operation)}
              </strong>
              <span className={META}>{formatDateTime(episode.last_failed_at)}</span>
            </div>
            <p className={META}>
              <ReasonText code={episode.error_code} /> · {t("log.firstSeen", { time: formatDateTime(episode.first_failed_at) })} ·{" "}
              {t("log.repeated", { n: episode.occurrence_count })}
            </p>
            {episode.last_error ? <ErrorDetails details={{ message: episode.last_error, fields: [] }} /> : null}
            {episode.details_status === "invalid" ? <p className={META}>{t("log.detailsInvalid")}</p> : null}
            {episode.succeeded_after_at && (
              <p className="tw:m-0 tw:text-xs tw:text-online">{t("log.succeededAfter", { time: formatDateTime(episode.succeeded_after_at) })}</p>
            )}
          </li>
        ))}
      </ul>
      {query.hasNextPage && (
        <Button
          size="sm"
          className="tw:justify-self-center"
          loading={query.isFetchingNextPage}
          onClick={() => void query.fetchNextPage()}
        >
          {query.isFetchingNextPage ? t("log.loadingMore") : t("log.loadMore")}
        </Button>
      )}
    </div>
  );
}


function businessOutcomeLabel(outcome: "success" | "failure" | "info"): string {
  return t(`log.${outcome}`);
}

/**
 * A metadata-blocked seed evaluation: the whole row carries only the long-form blocking
 * reason (automatic download paused: no TMDB linked); what is missing is read from
 * details.metadata_missing the server wrote into the log. Older records without it are not
 * named (Bangumi or TMDB) and get no fix action.
 */
function holdPhrases(entry: BusinessEntry): BlockingPhrases | undefined {
  const code = entry.reason_code?.trim().toLowerCase();
  if (code !== "metadata_incomplete" && code !== "metadata_hold") return undefined;
  return metadataBlocking(entry.details?.metadata_missing);
}

/** Plain text of the result column: the collapsed comparison key and the hover-full text. */
function businessResultText(entry: BusinessEntry): string {
  const hold = holdPhrases(entry);
  if (hold) return hold.long;
  const summary = policyChangeText(entry) ?? entry.summary;
  const reason = failureReasonView(entry);
  return reason ? `${summary}${t("log.reason", { reason: reason.text })}` : summary;
}

type AnimeNames = ReadonlyMap<string, string>;

/**
 * Name of the record's anime: the current name from the anime list; when not in the list, a
 * non-seed record's title is the anime name at write time (a seed record's title is the seed
 * title).
 */
function animeName(entry: BusinessEntry, names: AnimeNames): string | undefined {
  return names.get(entry.managed_anime_id) ?? (entry.kind === "release_processed" ? undefined : entry.title);
}

/**
 * The concrete subject of a one-line event: seed records write the seed title; others write
 * "[anime ·] group". Filtered to one anime with no group it is empty and the result takes the
 * title slot.
 */
function entrySubject(entry: BusinessEntry, anime: string | undefined): string {
  if (entry.kind === "release_processed") return entry.title;
  return joinMeta(anime, entryGroupName(entry));
}

/** Parallel title elements join with " · "; missing ones are simply omitted. */
function joinMeta(...parts: (string | undefined)[]): string {
  return parts.filter((part): part is string => !!part).join(" · ");
}

function failureReasonView(entry: BusinessEntry) {
  if (entry.outcome !== "failure") return undefined;
  const view = reasonView(entry.reason_code);
  if (!view || entry.summary.includes(view.text)) return undefined;
  return view;
}

/**
 * The user sees only business reasons, and only on failure: for success and info the reason
 * code is the summary itself. A summary that already states the reason is not repeated; the
 * body and related identifiers stay in the API for internal diagnosis.
 */
function FailureReason({ entry }: { entry: BusinessEntry }) {
  const view = failureReasonView(entry);
  if (!view) return null;
  return <span title={view.title}>{t("log.reason", { reason: view.text })}</span>;
}
