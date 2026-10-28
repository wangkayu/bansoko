import { useEffect, useMemo, useState } from "react";
import { ChevronRight } from "lucide-react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { cn } from "@/lib/utils";
import { Button, Select } from "../ui/base/controls";
import { StatePill } from "../ui/StatePill";
import { StateBlock } from "../ui/StateBlock";
import {
  listObservabilityDiagnostics,
  toUserMessage,
  type DiagnosticEpisode,
  type DiagnosticEpisodePage,
} from "../lib/api/client";
import { observabilityKeys } from "../lib/queryKeys";
import { t } from "../lib/i18n";
import { joinMeta } from "./logRuns";
import {
  DIAG_TYPES,
  diagClassLabel,
  diagRetryText,
  diagSentence,
  diagSpan,
  diagTargetLabel,
  diagTime,
  diagType,
  diagTypeLabel,
  diagViaLabel,
  isOpen,
  settingsPath,
  splitEpisodes,
  type DiagType,
} from "./diagnosticsView";
import { isNewer, markLogSeen } from "./logSeen";

// Diagnostics tab: one row per episode of a condition, ongoing ones first, recovered ones
// (grey) after, and a single internal-errors row last. Rows never show paths or raw error
// text; the closed condition catalog supplies every sentence.

const PAGE_LIMIT = 50;

type StateFilter = "" | "open" | "recovered";
type TypeFilter = "" | DiagType;

const SECTION = "tw:grid tw:gap-3";
const FILTERS = "tw:flex tw:items-center tw:gap-2";
const LIST = "tw:m-0 tw:grid tw:list-none tw:gap-2 tw:p-0";
const ITEM = "tw:grid tw:gap-1 tw:rounded-[10px] tw:border tw:border-border tw:bg-card tw:px-3 tw:py-2.5 tw:text-sm";
const META = "tw:m-0 tw:text-xs tw:text-muted-foreground";
const HEADING = "tw:m-0 tw:text-xs tw:font-medium tw:text-muted-foreground";

export function LogDiagnostics({
  active,
  onOpenSettings,
  onOpenRaw,
  newSince,
}: {
  /** The dialog is open on this tab: opening the tab moves the diagnostics seen-mark. */
  active: boolean;
  onOpenSettings?: (path: string) => void;
  onOpenRaw: () => void;
  newSince: string | null;
}) {
  const [state, setState] = useState<StateFilter>("");
  const [type, setType] = useState<TypeFilter>("");
  const query = useInfiniteQuery({
    queryKey: observabilityKeys.diagnosticsFiltered(state),
    queryFn: ({ pageParam }) =>
      listObservabilityDiagnostics({ state: state === "" ? undefined : state, limit: PAGE_LIMIT, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage: DiagnosticEpisodePage) => lastPage.next_cursor ?? undefined,
  });
  const firstPage = query.data?.pages[0];
  // The mark moves to the newest first-failure among the ongoing conditions listed; repeats and recoveries never move the count.
  useEffect(() => {
    if (!active || !firstPage || state === "recovered") return;
    let newest: string | undefined;
    for (const episode of firstPage.episodes) {
      if (isOpen(episode) && isNewer(episode.first_failed_at, newest ?? null)) newest = episode.first_failed_at;
    }
    markLogSeen("diagnostics", newest);
  }, [active, firstPage, state]);

  const episodes = useMemo(() => query.data?.pages.flatMap((page) => page.episodes) ?? [], [query.data]);
  const visible = useMemo(() => episodes.filter((episode) => type === "" || diagType(episode.kind) === type), [episodes, type]);
  const { open, recovered } = useMemo(() => splitEpisodes(visible), [visible]);
  const internal = firstPage?.internal;
  const showInternal = Boolean(internal) && type === "" && state !== "recovered";

  const stateOptions: { value: StateFilter; label: string }[] = [
    { value: "", label: t("log.diag.stateAll") },
    { value: "open", label: t("log.diag.open") },
    { value: "recovered", label: t("log.diag.done") },
  ];
  const typeOptions: { value: TypeFilter; label: string }[] = [
    { value: "", label: t("log.allTypes") },
    ...DIAG_TYPES.map((value) => ({ value, label: diagTypeLabel(value) })),
  ];

  let body: React.ReactNode;
  if (query.error) {
    body = (
      <StateBlock
        kind="error"
        title={toUserMessage(query.error, t("log.loadDiagnosticsFailed"))}
        action={
          <Button size="sm" variant="link" onClick={() => void query.refetch()}>
            {t("log.retry")}
          </Button>
        }
      />
    );
  } else if (!query.data) {
    body = <StateBlock kind="busy" title={t("log.loadingDiagnostics")} />;
  } else if (visible.length === 0 && !showInternal && !query.hasNextPage) {
    body = <StateBlock kind="empty" title={t("log.emptyDiagnostics")} />;
  } else {
    body = (
      <div className="tw:grid tw:gap-3" aria-label={t("log.diagnosticsAria")}>
        {open.length > 0 && (
          <EpisodeGroup heading={t("log.diag.open")} episodes={open} newSince={newSince} onOpenSettings={onOpenSettings} />
        )}
        {recovered.length > 0 && (
          <EpisodeGroup heading={t("log.diag.done")} episodes={recovered} newSince={newSince} onOpenSettings={onOpenSettings} />
        )}
        {showInternal && internal && (
          <ul className={LIST}>
            <li className={cn(ITEM, "tw:text-muted-foreground")}>
              <strong className="tw:font-medium">{t("log.diag.internal", { n: internal.occurrence_count })}</strong>
              <p className={META}>
                {t("log.diag.internalLast", { time: diagTime(internal.last_failed_at) })}
                {internal.last_request_id ? (
                  <>
                    {" · "}
                    {t("log.diag.requestId")} <code className="tw:font-mono">{internal.last_request_id}</code>
                  </>
                ) : null}
                {" · "}
                {t("log.diag.internalLog")}
              </p>
            </li>
          </ul>
        )}
        {query.hasNextPage && (
          <Button size="sm" className="tw:justify-self-center" loading={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>
            {query.isFetchingNextPage ? t("log.loadingMore") : t("log.loadMore")}
          </Button>
        )}
      </div>
    );
  }

  return (
    <div className={SECTION}>
      <div className={FILTERS}>
        <div className="tw:w-32 tw:flex-none">
          <Select<StateFilter> label={t("log.status")} value={state} options={stateOptions} onChange={setState} />
        </div>
        <div className="tw:min-w-0 tw:flex-1">
          <Select<TypeFilter> label={t("log.type")} value={type} options={typeOptions} onChange={setType} />
        </div>
      </div>
      {body}
      <Button size="sm" variant="link" className="tw:justify-self-start" onClick={onOpenRaw}>
        {t("log.rawlog.link")}
      </Button>
    </div>
  );
}

function EpisodeGroup({
  heading,
  episodes,
  newSince,
  onOpenSettings,
}: {
  heading: string;
  episodes: DiagnosticEpisode[];
  newSince: string | null;
  onOpenSettings?: (path: string) => void;
}) {
  return (
    <section className="tw:grid tw:gap-1.5">
      <h3 className={HEADING}>{heading}</h3>
      <ul className={LIST}>
        {episodes.map((episode) => (
          <EpisodeRow key={episode.id} episode={episode} isNew={isOpen(episode) && newSince !== null && isNewer(episode.first_failed_at, newSince)} onOpenSettings={onOpenSettings} />
        ))}
      </ul>
    </section>
  );
}

function EpisodeRow({
  episode,
  isNew,
  onOpenSettings,
}: {
  episode: DiagnosticEpisode;
  isNew: boolean;
  onOpenSettings?: (path: string) => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const open = isOpen(episode);
  const tone = !open ? "muted" : episode.severity === "error" ? "offline" : "warning";
  const stamp = open
    ? t("log.diag.last", { time: diagTime(episode.last_failed_at) })
    : t("log.diag.recovered", { time: diagTime(episode.succeeded_after_at ?? episode.last_failed_at) });
  const classes = [...new Set([diagClassLabel(episode.detail?.error_class, episode.detail?.http_status)].filter((value): value is string => !!value))];
  const secondary = joinMeta(t("log.diag.times", { n: episode.occurrence_count }), diagSpan(episode), ...classes);
  const settings = episode.retry.mode === "settings" && episode.retry.settings_target ? settingsPath(episode.retry.settings_target, episode.target.id) : undefined;
  return (
    <li className={cn(ITEM, !open && "tw:text-muted-foreground")}>
      <button
        type="button"
        aria-expanded={expanded}
        onClick={() => setExpanded((value) => !value)}
        className="tw:grid tw:gap-0.5 tw:rounded-sm tw:text-left tw:focus-visible:outline-2 tw:focus-visible:outline-ring"
      >
        <span className={META}>{diagTargetLabel(episode)}</span>
        <span className="tw:flex tw:items-start tw:gap-1">
          <ChevronRight aria-hidden className={cn("tw:mt-0.5 tw:size-3.5 tw:flex-none tw:transition-transform", expanded && "tw:rotate-90")} />
          <span className={cn("tw:font-medium", open ? "tw:text-foreground" : "tw:text-muted-foreground")}>{diagSentence(episode)}</span>
        </span>
        <span className="tw:flex tw:flex-wrap tw:items-center tw:gap-x-2 tw:text-xs tw:text-muted-foreground">
          <StatePill tone={tone}>{stamp}</StatePill>
          {isNew && <span className="tw:font-medium tw:text-primary">{t("log.new")}</span>}
          <span>{secondary}</span>
        </span>
      </button>
      {open && (
        <p className={cn(META, "tw:flex tw:flex-wrap tw:items-center tw:gap-x-2")}>
          <span>{diagRetryText(episode)}</span>
          {settings && onOpenSettings ? (
            <Button size="sm" variant="link" onClick={() => onOpenSettings(settings)}>
              {t("log.diag.openSettings")}
            </Button>
          ) : null}
        </p>
      )}
      {expanded && (
        <div className="tw:grid tw:gap-1 tw:border-t tw:border-border tw:pt-2">
          <p className={META}>{t("log.diag.first", { time: diagTime(episode.first_failed_at) })}</p>
          {episode.affected ? (
            <p className={META}>{t(episode.affected.kind === "anime" ? "log.diag.affectedAnime" : "log.diag.affectedTask", { n: episode.affected.count })}</p>
          ) : null}
          {episode.recent.length > 0 && (
            <>
              <p className={cn(META, "tw:font-medium")}>{t("log.diag.recent")}</p>
              <ul className="tw:m-0 tw:list-none tw:p-0">
                {episode.recent.slice(0, 5).map((occurrence, index) => (
                  <li key={`${occurrence.at}-${index}`} className={META}>
                    {joinMeta(diagTime(occurrence.at), occurrence.host, diagClassLabel(occurrence.error_class), diagViaLabel(occurrence.via))}
                  </li>
                ))}
              </ul>
            </>
          )}
        </div>
      )}
    </li>
  );
}
