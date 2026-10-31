import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  deleteAnimeSubscription,
  isVersionConflict,
  listAnime,
  patchAnimeSubscription,
  toUserMessage,
} from "../../lib/api/client";
import { providerLabel } from "../../lib/providers/registry";
import { importedCount, progressDone, progressTotal } from "../../lib/progress";
import { animeKeys } from "../../lib/queryKeys";
import { PHONE_MEDIA_QUERY, useMediaQuery } from "../../lib/useMediaQuery";
import { formatLocaleDate, withNote } from "../../lib/i18n/format";
import { t, type CopyKey } from "../../lib/i18n";
import { toast } from "../../ui/toast";
import { Button, Checkbox, Input, Select, Segmented } from "../../ui/base";
import { Dialog } from "../../ui/base";
import { StateBlock } from "../../ui/StateBlock";
import { StatePill } from "../../ui/StatePill";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { ConfirmLines } from "./ConfirmLines";
import { StatsChips } from "./GridToolbar";
import { seasonLabel } from "./season";
import {
  emptyManageFilters,
  filterManageRows,
  manageCompleted,
  manageOriginOptions,
  manageRowsFrom,
  manageSeasonOptions,
  orderManageRows,
  pausable,
  startable,
  type ManageFilters,
  type ManageOriginFilter,
  type ManageProgressFilter,
  type ManageRow,
  type ManageSeasonFilter,
  type ManageSort,
  type ManageSortKey,
  type ManageState,
} from "./subscribedManage";

/** The sort picker's value: one option per key + direction. */
type ManageSortValue = `${ManageSortKey}:asc` | `${ManageSortKey}:desc`;

function sortValueOf(sort: ManageSort): ManageSortValue {
  return `${sort.key}:${sort.desc ? "desc" : "asc"}`;
}

function sortFromValue(value: ManageSortValue): ManageSort {
  const [key, direction] = value.split(":");
  return { key: key as ManageSortKey, desc: direction === "desc" };
}

// SubscribedManageDialog — the subscribed wall's bulk-management surface (the
// toolbar's 管理 button; lazy-loaded from AnimeGrid). One dense table of every
// subscription instance: 入口 and 季度 are two independent filter dimensions
// (the season is the provider season page the instance was created from, frozen
// at creation; search-created and custom instances have none), the same work's
// instances stay adjacent with an inset line, and the footer's batch actions
// (start / pause / remove) issue one API call per selected instance and then
// refresh. Phones swap the table for two-line rows with a sticky select-all.

const STATE_PILL_TONE: Record<ManageState, "online" | "info" | "muted"> = {
  active: "online",
  pending: "info",
  stopped: "muted",
};
const STATE_LABEL_KEY: Record<ManageState, CopyKey> = {
  active: "anime.state.active",
  pending: "anime.state.pending",
  stopped: "anime.state.stopped",
};
const RSS_PILL: Record<
  NonNullable<ManageRow["rssHealth"]>,
  { tone: "online" | "warning" | "offline"; key: CopyKey }
> = {
  ready: { tone: "online", key: "grid.manage.rssReady" },
  degraded: { tone: "warning", key: "grid.manage.rssDegraded" },
  failed: { tone: "offline", key: "grid.manage.rssFailed" },
};

function originText(origin: ManageRow["origin"]): string {
  return origin === "custom" ? t("grid.manage.originCustom") : providerLabel(origin);
}

/** The dense row's progress: n/total (n/— when the total is unknown), — before
 *  the first import with no total; a 完成 pill when everything announced is in,
 *  else the mini bar (unknown totals show no bar). */
function progressCell(row: ManageRow): { text: string; done: boolean; ratio?: number } {
  const stored = importedCount(row.progress);
  const total = progressTotal(row.progress);
  const done = progressDone(row.progress);
  if (total === undefined) {
    return { text: stored === 0 ? "—" : t("grid.manage.progressNoTotal", { stored }), done };
  }
  return {
    text: t("grid.progress.summaryShort", { stored, total }),
    done,
    ratio: Math.min(100, Math.round((stored / total) * 100)),
  };
}

export function SubscribedManageDialog({
  onClose,
  onMutated,
}: {
  onClose: () => void;
  /** The parent's invalidation (catalog + workspace) after any batch mutation. */
  onMutated: () => void;
}) {
  const isPhone = useMediaQuery(PHONE_MEDIA_QUERY);
  const list = useQuery({ queryKey: animeKeys.list, queryFn: listAnime });
  const [filters, setFilters] = useState<ManageFilters>(emptyManageFilters);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [confirmingRemove, setConfirmingRemove] = useState(false);
  const [batching, setBatching] = useState(false);

  const rows = useMemo(() => manageRowsFrom(list.data ?? []), [list.data]);
  const visible = useMemo(() => filterManageRows(rows, filters), [rows, filters]);
  const ordered = useMemo(() => orderManageRows(visible, filters.sort), [visible, filters.sort]);
  const selectedRows = useMemo(() => rows.filter((row) => selected.has(row.key)), [rows, selected]);

  const counts = useMemo(() => {
    const base: Record<ManageState, number> = { active: 0, stopped: 0, pending: 0 };
    for (const row of rows) base[row.state] += 1;
    return base;
  }, [rows]);
  const originOptions = useMemo(() => manageOriginOptions(rows), [rows]);
  const seasonChoices = useMemo(() => manageSeasonOptions(rows), [rows]);
  const doneCount = useMemo(() => rows.filter(manageCompleted).length, [rows]);

  function toggle(key: string) {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }
  const allSelected = ordered.length > 0 && ordered.every(({ row }) => selected.has(row.key));
  const someSelected = ordered.some(({ row }) => selected.has(row.key));
  function toggleAll(next: boolean) {
    setSelected((current) => {
      const merged = new Set(current);
      for (const { row } of ordered) {
        if (next) merged.add(row.key);
        else merged.delete(row.key);
      }
      return merged;
    });
  }

  /** One API call per instance; the summary toast counts, and any version
   *  conflict surfaces as the standard "changed, refreshed" notice. */
  async function runBatch(
    labels: { ok: CopyKey; partFailed: CopyKey },
    targets: ManageRow[],
    call: (row: ManageRow) => Promise<unknown>,
  ) {
    if (targets.length === 0 || batching) return;
    setBatching(true);
    const results = await Promise.allSettled(targets.map(call));
    setBatching(false);
    const ok = results.filter((result) => result.status === "fulfilled").length;
    const failed = targets.length - ok;
    await list.refetch();
    onMutated();
    if (failed === 0) {
      toast.success(t(labels.ok, { n: ok }));
    } else {
      const conflict = results.some(
        (result) => result.status === "rejected" && isVersionConflict(result.reason),
      );
      toast.error(conflict ? t("catalog.changed") : t(labels.partFailed, { ok, failed }));
    }
  }

  const startTargets = selectedRows.filter(startable);
  const pauseTargets = selectedRows.filter(pausable);
  // 未开始 instances cannot pause: the disabled button explains instead of failing.
  const pauseBlocked =
    pauseTargets.length === 0 && selectedRows.some((row) => row.state === "pending" && !row.retired);
  const removeTargets = selectedRows.filter((row) => !row.retired);

  const batchSummary =
    selectedRows.length === 0
      ? t("grid.manage.selNone")
      : (() => {
          const selCounts: Record<ManageState, number> = { active: 0, stopped: 0, pending: 0 };
          for (const row of selectedRows) selCounts[row.state] += 1;
          return t("grid.manage.selInfoStates", {
            n: selectedRows.length,
            active: selCounts.active,
            stopped: selCounts.stopped,
            pending: selCounts.pending,
          });
        })();

  return (
    <Dialog
      open
      tier="L"
      onRequestClose={onClose}
      title={t("grid.manage.title")}
      description={t("grid.manage.subtitle", {
        total: rows.length,
        done: doneCount,
        filtered: ordered.length,
      })}
      bodyLayout="fill"
      footer={
        <div className="tw:flex tw:w-full tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-2">
          <span className="tw:min-w-0 tw:flex-1 tw:truncate tw:text-sm tw:text-muted-foreground tw:phone:basis-full">
            {batchSummary}
          </span>
          {selectedRows.length > 0 && (
            <Button size="sm" onClick={() => setSelected(new Set())}>
              {t("grid.manage.clearSel")}
            </Button>
          )}
          <Button
            size="sm"
            variant="primary"
            loading={batching}
            disabled={startTargets.length === 0}
            onClick={() =>
              runBatch(
                { ok: "grid.manage.startedToast", partFailed: "grid.manage.batchPartFailed" },
                startTargets,
                (row) => patchAnimeSubscription(row.key, { expected_version: row.version, state: "active" }),
              )
            }
          >
            {t("grid.manage.start")}
          </Button>
          <Button
            size="sm"
            loading={batching}
            disabled={pauseTargets.length === 0}
            title={pauseBlocked ? t("grid.manage.pauseBlocked") : undefined}
            onClick={() =>
              runBatch(
                { ok: "grid.manage.pausedToast", partFailed: "grid.manage.batchPartFailed" },
                pauseTargets,
                (row) => patchAnimeSubscription(row.key, { expected_version: row.version, state: "stopped" }),
              )
            }
          >
            {t("grid.manage.pause")}
          </Button>
          <Button size="sm" variant="danger" disabled={removeTargets.length === 0} onClick={() => setConfirmingRemove(true)}>
            {t("grid.manage.remove")}
          </Button>
        </div>
      }
    >
      {/* Filters: state (with counts) · progress · entry × season (two independent
          dimensions, combined by intersection) · created-time sort · search. */}
      <div className="tw:flex tw:min-w-0 tw:flex-wrap tw:items-center tw:gap-2 tw:border-b tw:border-border tw:px-6 tw:py-2.5 tw:phone:px-4">
        <div className={isPhone ? "tw:min-w-0 tw:w-full" : "tw:min-w-0"}>
          <StatsChips<ManageState>
            ariaLabel={t("grid.manage.stateAria")}
            scroll={isPhone}
            buttonSize="compact"
            chips={[
              { key: "active", label: t("anime.state.active"), count: counts.active },
              { key: "stopped", label: t("anime.state.stopped"), count: counts.stopped },
              { key: "pending", label: t("anime.state.pending"), count: counts.pending },
            ]}
            active={filters.state === "all" ? null : filters.state}
            onToggle={(key) => setFilters((current) => ({ ...current, state: current.state === key ? "all" : key }))}
          />
        </div>
        <Segmented<ManageProgressFilter>
          aria-label={t("grid.manage.progressAria")}
          size="toolbar"
          value={filters.progress}
          onValueChange={(progress) => setFilters((current) => ({ ...current, progress }))}
          options={[
            { value: "all", label: t("grid.manage.progressAll") },
            { value: "doing", label: t("grid.manage.progressDoing") },
            { value: "done", label: t("grid.manage.progressDone") },
            { value: "none", label: t("grid.manage.progressNone") },
          ]}
        />
        <div className="tw:w-28 tw:flex-none tw:phone:w-26">
          <Select<ManageOriginFilter>
            label={t("grid.manage.originAria")}
            size="xs"
            value={filters.origin}
            onChange={(origin) => setFilters((current) => ({ ...current, origin }))}
            options={[
              { value: "all", label: t("grid.manage.originAll") },
              ...originOptions.map((origin) => ({
                value: origin,
                label: origin === "custom" ? t("grid.manage.originCustom") : providerLabel(origin),
              })),
            ]}
          />
        </div>
        <div className="tw:w-28 tw:flex-none tw:phone:w-26">
          <Select<ManageSeasonFilter>
            label={t("grid.manage.seasonAria")}
            size="xs"
            value={filters.season}
            onChange={(season) => setFilters((current) => ({ ...current, season }))}
            options={[
              { value: "all", label: t("grid.manage.seasonAll") },
              ...seasonChoices.seasons.map((season) => ({ value: season, label: seasonLabel(season) })),
              ...(seasonChoices.hasNone ? [{ value: "none", label: t("grid.manage.seasonNone") }] : []),
            ]}
          />
        </div>
        <div className="tw:w-28 tw:flex-none tw:phone:w-26">
          <Select<ManageSortValue>
            label={t("grid.manage.sortAria")}
            size="xs"
            value={sortValueOf(filters.sort)}
            onChange={(value) => setFilters((current) => ({ ...current, sort: sortFromValue(value) }))}
            options={[
              { value: "name:asc", label: t("grid.manage.sortNameAsc") },
              { value: "name:desc", label: t("grid.manage.sortNameDesc") },
              { value: "created:desc", label: t("grid.manage.sortDesc") },
              { value: "created:asc", label: t("grid.manage.sortAsc") },
              { value: "season:desc", label: t("grid.manage.sortSeasonDesc") },
              { value: "season:asc", label: t("grid.manage.sortSeasonAsc") },
            ]}
          />
        </div>
        <Input
          type="search"
          aria-label={t("grid.manage.searchAria")}
          placeholder={t("grid.manage.searchPlaceholder")}
          controlSize="xs"
          fullWidth={isPhone}
          className={isPhone ? undefined : "tw:ml-auto tw:w-48"}
          value={filters.query}
          onChange={(event) => setFilters((current) => ({ ...current, query: event.target.value }))}
        />
      </div>

      <div className="tw:min-h-0 tw:flex-1 tw:overflow-auto">
        {list.isPending ? (
          <StateBlock kind="busy" title={t("act.reading")} />
        ) : list.isError ? (
          <StateBlock
            kind="error"
            title={toUserMessage(list.error, t("grid.manage.loadFailed"))}
            action={
              <Button size="sm" onClick={() => void list.refetch()}>
                {t("grid.retry")}
              </Button>
            }
          />
        ) : ordered.length === 0 ? (
          rows.length === 0 ? (
            <StateBlock kind="empty" title={t("catalog.emptySubscribed")} description={t("grid.empty.allDesc")} />
          ) : (
            <StateBlock kind="empty" title={t("search.noMatch")} description={t("grid.empty.filteredDesc")} />
          )
        ) : isPhone ? (
          <ul>
            <li className="tw:sticky tw:top-0 tw:z-[1] tw:flex tw:items-center tw:gap-2 tw:border-b tw:border-border tw:bg-popover tw:px-4 tw:py-2 tw:text-xs tw:text-muted-foreground">
              <Checkbox
                size="sm"
                checked={allSelected}
                indeterminate={someSelected && !allSelected}
                onCheckedChange={toggleAll}
                aria-label={t("grid.manage.selectAll")}
              />
              {t("grid.manage.selectAllList", { n: ordered.length })}
            </li>
            {ordered.map(({ row, grouped }) => (
              <ManageMobileRow
                key={row.key}
                row={row}
                grouped={grouped}
                selected={selected.has(row.key)}
                onToggle={() => toggle(row.key)}
              />
            ))}
          </ul>
        ) : (
          <table className="tw:w-full tw:min-w-[56rem] tw:table-fixed tw:border-collapse tw:text-sm">
            <colgroup>
              <col className="tw:w-9" />
              <col />
              <col className="tw:w-16" />
              <col className="tw:w-[4.5rem]" />
              <col className="tw:w-16" />
              <col className="tw:w-[5.5rem]" />
              <col className="tw:w-[4.5rem]" />
              <col className="tw:w-40" />
              <col className="tw:w-28" />
            </colgroup>
            <thead className="tw:sticky tw:top-0 tw:z-[1] tw:bg-popover">
              <tr className="tw:border-b tw:border-border tw:text-left tw:text-xs tw:font-medium tw:text-muted-foreground">
                <th className="tw:w-9 tw:px-2 tw:py-2">
                  <Checkbox
                    size="sm"
                    checked={allSelected}
                    indeterminate={someSelected && !allSelected}
                    onCheckedChange={toggleAll}
                    aria-label={t("grid.manage.selectAll")}
                  />
                </th>
                <th className="tw:px-2 tw:py-2 tw:font-medium">{t("grid.manage.colTitle")}</th>
                <th className="tw:px-2 tw:py-2 tw:font-medium">{t("grid.manage.colOrigin")}</th>
                <th className="tw:px-2 tw:py-2 tw:text-right tw:font-medium">{t("grid.manage.colSeason")}</th>
                <th className="tw:px-2 tw:py-2 tw:text-right tw:font-medium">{t("grid.manage.colShowSeason")}</th>
                <th className="tw:px-2 tw:py-2 tw:font-medium">{t("grid.manage.colState")}</th>
                <th className="tw:px-2 tw:py-2 tw:font-medium">{t("grid.manage.colRss")}</th>
                <th className="tw:px-2 tw:py-2 tw:text-right tw:font-medium">{t("grid.manage.colProgress")}</th>
                <th className="tw:px-2 tw:py-2 tw:text-right tw:font-medium">{t("grid.manage.colCreatedAt")}</th>
              </tr>
            </thead>
            <tbody>
              {ordered.map(({ row, grouped }) => {
                const progress = progressCell(row);
                return (
                  <tr
                    key={row.key}
                    onClick={() => toggle(row.key)}
                    className={
                      "tw:cursor-pointer tw:border-b tw:border-border/60 tw:hover:bg-muted/50 " +
                      (selected.has(row.key) ? "tw:bg-accent/40 " : "")
                    }
                  >
                    <td
                      className={
                        "tw:h-9 tw:border-l-2 tw:px-2 " +
                        (grouped ? "tw:border-l-primary/45" : "tw:border-l-transparent")
                      }
                      onClick={(event) => event.stopPropagation()}
                    >
                      <Checkbox
                        size="sm"
                        checked={selected.has(row.key)}
                        onCheckedChange={() => toggle(row.key)}
                        aria-label={t("grid.manage.selectRow", { title: row.title })}
                      />
                    </td>
                    <td className="tw:max-w-0 tw:px-2">
                      <span className="tw:flex tw:min-w-0 tw:items-center tw:gap-1.5">
                        <span className="tw:min-w-0 tw:flex-1 tw:truncate" title={row.title}>
                          {row.title}
                        </span>
                        {row.instanceLabel && (
                          <span
                            title={row.instanceLabel}
                            className="tw:min-w-0 tw:max-w-24 tw:truncate tw:rounded tw:border tw:border-border tw:bg-muted tw:px-1.5 tw:text-xs tw:text-muted-foreground"
                          >
                            {row.instanceLabel}
                          </span>
                        )}
                      </span>
                    </td>
                    <td className="tw:px-2 tw:text-muted-foreground">{originText(row.origin)}</td>
                    <td className="tw:px-2 tw:text-right tw:text-muted-foreground tw:tabular-nums">
                      {row.entrySeason ? seasonLabel(row.entrySeason) : "—"}
                    </td>
                    <td className="tw:px-2 tw:text-right tw:text-muted-foreground tw:tabular-nums">
                      {row.showSeason === undefined ? "—" : t("grid.manage.showSeasonLabel", { n: row.showSeason })}
                    </td>
                    <td className="tw:px-2">
                      <StatePill tone={STATE_PILL_TONE[row.state]}>{t(STATE_LABEL_KEY[row.state])}</StatePill>
                    </td>
                    <td className="tw:px-2">
                      {row.rssHealth ? (
                        <StatePill tone={RSS_PILL[row.rssHealth].tone}>{t(RSS_PILL[row.rssHealth].key)}</StatePill>
                      ) : (
                        <span className="tw:text-muted-foreground">—</span>
                      )}
                    </td>
                    <td className="tw:px-2 tw:text-right tw:tabular-nums">
                      <span className="tw:inline-flex tw:items-center tw:justify-end tw:gap-1.5">
                        <span className={progress.done ? "tw:text-online" : ""}>{progress.text}</span>
                        {progress.done ? (
                          <span className="tw:text-xs tw:text-online">{t("grid.manage.done")}</span>
                        ) : progress.ratio !== undefined ? (
                          <span className="tw:inline-block tw:h-1 tw:w-11 tw:overflow-hidden tw:rounded-full tw:bg-muted tw:align-middle">
                            <span className="tw:block tw:h-full tw:rounded-full tw:bg-primary" style={{ width: `${progress.ratio}%` }} />
                          </span>
                        ) : null}
                      </span>
                    </td>
                    <td className="tw:px-2 tw:text-right tw:text-muted-foreground tw:tabular-nums">
                      {formatLocaleDate(new Date(row.createdAt))}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>

      {confirmingRemove && (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) setConfirmingRemove(false);
          }}
          title={t("grid.manage.removeTitle", { n: removeTargets.length })}
          description={
            <ConfirmLines
              lines={[
                t("grid.manage.removeLine1"),
                ...removeTargets
                  .slice(0, 5)
                  .map((row) => (row.instanceLabel ? withNote(row.title, row.instanceLabel) : row.title)),
                ...(removeTargets.length > 5 ? [t("grid.manage.removeMore", { n: removeTargets.length })] : []),
                t("grid.manage.removeLine2"),
              ]}
            />
          }
          confirmLabel={t("grid.manage.removeConfirm")}
          danger
          onConfirm={() => {
            setConfirmingRemove(false);
            setSelected(new Set());
            void runBatch(
              { ok: "grid.manage.removedToast", partFailed: "grid.manage.batchPartFailed" },
              removeTargets,
              (row) => deleteAnimeSubscription(row.key, { expected_version: row.version }),
            );
          }}
        />
      )}
    </Dialog>
  );
}

function ManageMobileRow({
  row,
  grouped,
  selected,
  onToggle,
}: {
  row: ManageRow;
  grouped: boolean;
  selected: boolean;
  onToggle: () => void;
}) {
  const progress = progressCell(row);
  const meta = [
    originText(row.origin),
    row.entrySeason ? seasonLabel(row.entrySeason) : "—",
    row.showSeason === undefined ? "—" : t("grid.manage.showSeasonLabel", { n: row.showSeason }),
    row.rssHealth ? `RSS ${t(RSS_PILL[row.rssHealth].key)}` : "RSS —",
    progress.done ? `${progress.text} ${t("grid.manage.done")}` : progress.text,
    formatLocaleDate(new Date(row.createdAt)),
  ].join(" · ");
  return (
    <li
      onClick={onToggle}
      className={
        "tw:flex tw:cursor-pointer tw:items-center tw:gap-2.5 tw:border-b tw:border-border/60 tw:border-l-2 tw:px-4 tw:py-2 " +
        (grouped ? "tw:border-l-primary/45 " : "tw:border-l-transparent ") +
        (selected ? "tw:bg-accent/40" : "")
      }
    >
      <Checkbox
        size="sm"
        checked={selected}
        onCheckedChange={() => onToggle()}
        aria-label={t("grid.manage.selectRow", { title: row.title })}
        className="tw:flex-none"
      />
      <div className="tw:min-w-0 tw:flex-1">
        <div className="tw:flex tw:items-center tw:gap-1.5">
          <span className="tw:min-w-0 tw:flex-1 tw:truncate tw:text-sm tw:font-medium" title={row.title}>
            {row.title}
          </span>
          {row.instanceLabel && (
            <span
              title={row.instanceLabel}
              className="tw:min-w-0 tw:max-w-24 tw:truncate tw:rounded tw:border tw:border-border tw:bg-muted tw:px-1.5 tw:text-xs tw:text-muted-foreground"
            >
              {row.instanceLabel}
            </span>
          )}
          <StatePill tone={STATE_PILL_TONE[row.state]} className="tw:ml-auto tw:flex-none">
            {t(STATE_LABEL_KEY[row.state])}
          </StatePill>
        </div>
        <div className="tw:mt-0.5 tw:truncate tw:text-xs tw:text-muted-foreground tw:tabular-nums">{meta}</div>
      </div>
    </li>
  );
}
