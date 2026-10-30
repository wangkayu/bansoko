import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { ArrowLeft, ChevronRight, RotateCw } from "lucide-react";
import type { RSSListFile } from "../../lib/api/client";
import { formatBytes, formatDateTime } from "../../lib/labels";
import { reasonLabel } from "../../lib/i18n/reasons";
import { getLocale, t } from "../../lib/i18n";
import { cn } from "../../lib/utils";
import { Button, Input, StateBlock } from "../../ui";
import { Checkbox, SpinnerIcon } from "../../ui/base";
import type { OneShotGroupEntry } from "./useOneShotSeeds";
import { needsExpand, selectableRow, type OneShotSeedGroup, type OneShotSeedRow } from "./seedRows";

// The two layers of "browse seeds" step one: subgroups first, and a group's seeds appear only after
//   entering it. A group's seeds render all at once (the seed list is fetched whole, no pagination);
//   the toolbar sticks to the top and stays usable while scrolling. Selection is row checkboxes plus
//   the footer "next" only — no per-row download action.
//
// Motion appears only under motion-safe: entering/leaving a group is a short horizontal slide plus
//   fade-in, expanding the file list is a row-height transition; under prefers-reduced-motion the
//   final state renders directly.

/** Side-by-side meta facts (size, time, count) join with " · " (copy guide §1). */
function metaLine(parts: readonly (string | undefined)[]): string {
  return parts.filter((part): part is string => Boolean(part)).join(" · ");
}

/** A group row's "newest 10/19": month/day only for this year, with the year for earlier ones. */
function shortDate(value: string): string | undefined {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return undefined;
  const sameYear = date.getFullYear() === new Date().getFullYear();
  return date.toLocaleDateString(getLocale(), sameYear ? { month: "numeric", day: "numeric" } : { year: "numeric", month: "numeric", day: "numeric" });
}

/** Why the seed cannot download (an unexpanded seed does not count: it expands automatically once selected). */
function seedBlockedHint(row: OneShotSeedRow): string | undefined {
  if (selectableRow(row)) return undefined;
  return reasonLabel(row.item.download_action.reason_code) ?? t("oneshot.cannotDownload");
}

// --- skeleton ----------------------------------------------------------------------

const skeletonBar = "tw:block tw:rounded tw:bg-muted tw:motion-safe:animate-pulse";

function GroupListSkeleton() {
  return (
    <ul className="tw:m-0 tw:grid tw:list-none tw:gap-2 tw:p-0" aria-hidden>
      {[0, 1, 2, 3, 4].map((index) => (
        <li key={index} className="tw:flex tw:items-center tw:gap-3 tw:rounded-control tw:border tw:border-border tw:px-3 tw:py-3">
          <span className={cn(skeletonBar, "tw:h-4")} style={{ width: `${40 + ((index * 17) % 35)}%` }} />
          <span className="tw:flex-1" />
          <span className={cn(skeletonBar, "tw:h-3 tw:w-12")} />
        </li>
      ))}
    </ul>
  );
}

function SeedListSkeleton() {
  return (
    <ul className="tw:m-0 tw:grid tw:list-none tw:divide-y tw:divide-border tw:p-0" aria-hidden>
      {[0, 1, 2, 3, 4, 5, 6].map((index) => (
        <li key={index} className="tw:flex tw:items-start tw:gap-3 tw:py-3">
          <span className={cn(skeletonBar, "tw:size-4 tw:flex-none")} />
          <span className="tw:grid tw:flex-1 tw:gap-1.5">
            <span className={cn(skeletonBar, "tw:h-3.5")} style={{ width: `${55 + ((index * 23) % 40)}%` }} />
            <span className={cn(skeletonBar, "tw:h-3 tw:w-32")} />
          </span>
        </li>
      ))}
    </ul>
  );
}

// --- layer one: subgroups -----------------------------------------------------------

function GroupList({
  entries,
  loading,
  openingGroup,
  openError,
  onOpen,
}: {
  entries: OneShotGroupEntry[];
  loading: boolean;
  openingGroup?: string;
  openError?: { key: string; message: string };
  onOpen: (key: string) => void;
}) {
  if (loading) return <GroupListSkeleton />;
  return (
    <ul className="tw:m-0 tw:grid tw:list-none tw:gap-2 tw:p-0">
      {entries.map((entry, index) => {
        const opening = openingGroup === entry.key;
        const error = openError?.key === entry.key ? openError.message : undefined;
        // Only entered groups know their seed count and newest time; nothing is placeholder-written for the unknown.
        const latest = entry.latestAt ? shortDate(entry.latestAt) : undefined;
        const meta = metaLine([
          entry.count !== undefined ? t("oneshot.groupSeedCount", { n: entry.count }) : undefined,
          latest ? t("oneshot.groupLatest", { date: latest }) : undefined,
        ]);
        return (
          <li
            key={entry.key}
            className="tw:motion-safe:animate-reveal"
            // Rows stagger slightly so the list appears to land in sequence; only the first few rows wait, the rest do not.
            style={{ animationDelay: `${Math.min(index, 8) * 20}ms` }}
          >
            <button
              type="button"
              aria-label={t("oneshot.openGroup", { name: entry.name })}
              disabled={openingGroup !== undefined}
              onClick={() => onOpen(entry.key)}
              className="tw:group tw:flex tw:w-full tw:cursor-pointer tw:items-center tw:gap-3 tw:rounded-control tw:border tw:border-border tw:bg-transparent tw:px-3 tw:py-3 tw:text-left tw:text-foreground tw:transition-[background-color,border-color,transform] tw:duration-150 tw:hover:border-ring/40 tw:hover:bg-muted/40 tw:active:scale-[0.99] tw:focus-visible:outline-2 tw:focus-visible:outline-offset-2 tw:focus-visible:outline-ring tw:disabled:cursor-default tw:motion-reduce:transition-none tw:motion-reduce:active:scale-100"
            >
              <span className="tw:grid tw:min-w-0 tw:flex-1 tw:gap-0.5">
                <span className="break-anywhere tw:text-sm tw:font-medium">{entry.name}</span>
                {error && <span className="tw:text-xs tw:text-destructive">{error}</span>}
              </span>
              {meta && <span className="tw:flex-none tw:text-xs tw:text-muted-foreground">{meta}</span>}
              {opening ? (
                <SpinnerIcon className="tw:size-4 tw:flex-none tw:text-muted-foreground" />
              ) : (
                <ChevronRight className="tw:size-4 tw:flex-none tw:text-muted-foreground tw:transition-transform tw:duration-150 tw:group-hover:translate-x-0.5 tw:motion-reduce:transition-none" />
              )}
            </button>
          </li>
        );
      })}
    </ul>
  );
}

// --- layer two: one group's seeds ----------------------------------------------------

export const REVEAL_MS = 200;

/**
 * 展开/收起：行高从 0fr 过渡到 1fr（内容高度未知也能平滑）。收起时内容保留到过渡
 * 结束再卸载；reduced-motion 下没有过渡，直接到终态。
 */
export function Reveal({ open, children }: { open: boolean; children: ReactNode }) {
  const [mounted, setMounted] = useState(open);
  const [shown, setShown] = useState(open);
  useEffect(() => {
    if (open) {
      setMounted(true);
      const frame = window.requestAnimationFrame(() => setShown(true));
      return () => window.cancelAnimationFrame(frame);
    }
    setShown(false);
    const timer = window.setTimeout(() => setMounted(false), REVEAL_MS);
    return () => window.clearTimeout(timer);
  }, [open]);
  if (!mounted) return null;
  return (
    <div
      className={cn(
        "tw:grid tw:transition-[grid-template-rows,opacity] tw:duration-200 tw:ease-out tw:motion-reduce:transition-none",
        shown ? "tw:grid-rows-[1fr] tw:opacity-100" : "tw:grid-rows-[0fr] tw:opacity-0",
      )}
    >
      <div className="tw:min-h-0 tw:overflow-hidden">{children}</div>
    </div>
  );
}

function CatalogFileRow({ file }: { file: RSSListFile }) {
  return (
    <li className="text-caption break-anywhere tw:m-0 tw:text-muted-foreground" title={file.source_path}>
      {file.source_path}
      {file.size_bytes !== undefined && <span> · {formatBytes(file.size_bytes)}</span>}
    </li>
  );
}

export type SeedRowState = {
  selected: boolean;
  expanded: boolean;
  expanding: boolean;
  expandError?: string;
};

function SeedRow({
  row,
  state,
  onToggleSelected,
  onToggleExpanded,
}: {
  row: OneShotSeedRow;
  state: SeedRowState;
  onToggleSelected: (row: OneShotSeedRow) => void;
  onToggleExpanded: (row: OneShotSeedRow) => void;
}) {
  const blocked = seedBlockedHint(row);
  // Step one is browse only: raw title, tracker attribution, size and time, with no parse results shown.
  const sourceSite = row.item.seed.source_site;
  const meta = metaLine([
    sourceSite?.name,
    sourceSite ? t(sourceSite.type === "private" ? "oneshot.sourceSitePrivate" : sourceSite.type === "semi-private" ? "oneshot.sourceSiteSemiPrivate" : "oneshot.sourceSitePublic") : undefined,
    row.sizeBytes != null ? formatBytes(row.sizeBytes) : undefined,
    row.publishedAt ? formatDateTime(row.publishedAt) : undefined,
  ]);
  const { expanded, expanding, expandError } = state;
  return (
    // content-visibility keeps off-screen rows out of layout and paint, so long-list scrolling stays smooth.
    <li className="tw:grid tw:py-2 tw:[contain-intrinsic-size:auto_3.5rem] tw:[content-visibility:auto]">
      <div className="tw:flex tw:items-start tw:gap-2">
        {/* The checkbox is the only selection control: a tap margin around it (larger on phones), and a tap in the margin counts as tapping the checkbox. */}
        <span
          className="tw:-m-1.5 tw:flex tw:flex-none tw:p-1.5 tw:phone:-m-2.5 tw:phone:p-2.5"
          onClick={(event) => {
            if (event.target === event.currentTarget && blocked === undefined) onToggleSelected(row);
          }}
        >
          <Checkbox
            size="sm"
            checked={state.selected}
            disabled={blocked !== undefined}
            title={blocked}
            aria-label={row.title}
            onCheckedChange={() => onToggleSelected(row)}
          />
        </span>
        <div className="tw:grid tw:min-w-0 tw:flex-1 tw:gap-0.5">
          <button
            type="button"
            className="break-anywhere tw:m-0 tw:inline-flex tw:cursor-pointer tw:items-start tw:gap-1 tw:rounded-sm tw:border-0 tw:bg-transparent tw:p-0 tw:text-left tw:text-sm tw:text-foreground tw:hover:text-primary tw:focus-visible:outline-2 tw:focus-visible:outline-offset-2 tw:focus-visible:outline-ring"
            aria-expanded={expanded}
            aria-label={t("oneshot.showFiles", { title: row.title })}
            onClick={() => onToggleExpanded(row)}
          >
            <ChevronRight
              className={cn(
                "icon-sm tw:mt-0.5 tw:flex-none tw:transition-transform tw:duration-200 tw:ease-out tw:motion-reduce:transition-none",
                expanded && "tw:rotate-90",
              )}
            />
            <span>{row.title}</span>
          </button>
          {meta && <p className="text-caption tw:m-0 tw:pl-5">{meta}</p>}
          {blocked && <p className="text-muted text-caption tw:m-0 tw:pl-5">{blocked}</p>}
        </div>
      </div>
      <Reveal open={expanded}>
        <div className="tw:grid tw:gap-1 tw:pt-1.5 tw:pl-11 tw:phone:pl-6">
          {expandError ? (
            <StateBlock
              kind="error"
              title={expandError}
              action={
                <Button size="sm" onClick={() => onToggleExpanded(row)}>
                  {t("act.retry")}
                </Button>
              }
            />
          ) : expanding || needsExpand(row) ? (
            <span className="tw:grid tw:gap-1.5" role="status" aria-label={t("oneshot.expanding")}>
              <span className={cn(skeletonBar, "tw:h-3 tw:w-3/4")} />
              <span className={cn(skeletonBar, "tw:h-3 tw:w-1/2")} />
            </span>
          ) : row.item.files.length === 0 ? (
            <p className="text-muted text-caption tw:m-0">{t("oneshot.noFiles")}</p>
          ) : (
            <ul className="tw:m-0 tw:grid tw:list-none tw:gap-1 tw:p-0">
              {row.item.files.map((file) => (
                <CatalogFileRow key={file.file_ref} file={file} />
              ))}
            </ul>
          )}
        </div>
      </Reveal>
    </li>
  );
}

function SeedToolbar({
  title,
  count,
  onBack,
  onRefresh,
  refreshing,
  pending,
  filter,
  onFilterChange,
  selectedCount,
  onSelectAll,
  onClear,
}: {
  title: string;
  count?: number;
  onBack?: () => void;
  onRefresh?: () => void;
  refreshing: boolean;
  pending: boolean;
  filter: string;
  onFilterChange: (value: string) => void;
  selectedCount: number;
  onSelectAll: () => void;
  onClear: () => void;
}) {
  return (
    <div className="tw:sticky tw:top-0 tw:z-10 tw:-mx-6 tw:grid tw:gap-2 tw:border-b tw:border-border tw:bg-popover tw:px-6 tw:pt-3 tw:pb-2.5 tw:phone:-mx-4 tw:phone:px-4">
      <div className="tw:flex tw:min-w-0 tw:items-center tw:gap-2">
        {onBack && (
          <Button size="sm" variant="ghost" aria-label={t("oneshot.backToGroups")} title={t("oneshot.backToGroups")} onClick={onBack} className="tw:-ml-2 tw:flex-none">
            <ArrowLeft className="icon-sm" />
          </Button>
        )}
        <h3 className="text-body tw:m-0 tw:min-w-0 tw:truncate tw:font-medium" title={title}>
          {title}
        </h3>
        {count !== undefined && <span className="tw:flex-none tw:text-xs tw:text-muted-foreground">{t("oneshot.groupSeedCount", { n: count })}</span>}
        <span className="tw:flex-1" />
        {pending && (
          <span role="status" className="tw:flex tw:flex-none tw:items-center tw:gap-1 tw:text-xs tw:text-muted-foreground tw:motion-safe:animate-fade-in">
            <SpinnerIcon className="tw:size-3.5" />
            <span className="tw:phone:sr-only">{t("oneshot.updating")}</span>
          </span>
        )}
        {onRefresh && (
          <Button
            size="sm"
            variant="ghost"
            disabled={refreshing}
            aria-label={t("oneshot.refreshSeedsAction")}
            title={t("oneshot.refreshSeedsAction")}
            onClick={onRefresh}
            className="tw:flex-none"
          >
            <RotateCw className={cn("icon-sm", refreshing && "tw:motion-safe:animate-spin")} />
          </Button>
        )}
      </div>
      <div className="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
        <Input
          aria-label={t("oneshot.seedFilter")}
          placeholder={t("oneshot.seedFilterPlaceholder")}
          value={filter}
          onChange={(event) => onFilterChange(event.target.value)}
          fullWidth={false}
          className="tw:min-w-0 tw:flex-[1_1_14rem] tw:phone:basis-full"
        />
        <span className="tw:ml-auto tw:flex tw:flex-none tw:items-center tw:gap-1">
          <span className="tw:mr-1 tw:text-xs tw:text-muted-foreground" aria-live="polite">
            {t("oneshot.selectedCount", { n: selectedCount })}
          </span>
          <Button size="sm" variant="ghost" onClick={onSelectAll}>
            {t("oneshot.selectAll")}
          </Button>
          <Button size="sm" variant="ghost" onClick={onClear} disabled={selectedCount === 0}>
            {t("oneshot.clearSelection")}
          </Button>
        </span>
      </div>
    </div>
  );
}

export function SeedBrowser({
  level,
  direction,
  groupEntries,
  loadingGroups,
  openingGroup,
  openError,
  onOpenGroup,
  activeGroup,
  activeTitle,
  activeLoading,
  onBack,
  onRefresh,
  refreshing,
  refreshError,
  pending,
  filter,
  onFilterChange,
  selectedCount,
  onSelectAll,
  onClear,
  rowState,
  onToggleSelected,
  onToggleExpanded,
  header,
  emptyState,
}: {
  level: "groups" | "seeds";
  /** The most recent navigation direction: decides which side the enter animation slides in from. */
  direction: "forward" | "back";
  groupEntries: OneShotGroupEntry[];
  loadingGroups: boolean;
  openingGroup?: string;
  openError?: { key: string; message: string };
  onOpenGroup: (key: string) => void;
  activeGroup?: OneShotSeedGroup;
  activeTitle: string;
  /** The entered group is still fetching its seed list. */
  activeLoading: boolean;
  onBack?: () => void;
  onRefresh?: () => void;
  refreshing: boolean;
  refreshError?: string;
  pending: boolean;
  filter: string;
  onFilterChange: (value: string) => void;
  selectedCount: number;
  onSelectAll: () => void;
  onClear: () => void;
  rowState: (row: OneShotSeedRow) => SeedRowState;
  onToggleSelected: (row: OneShotSeedRow) => void;
  onToggleExpanded: (row: OneShotSeedRow) => void;
  /** The step caption above the scroll area, shown in every layer. */
  header: ReactNode;
  /** Failure/empty states that replace the whole browse area. */
  emptyState?: ReactNode;
}) {
  const scrollRef = useRef<HTMLDivElement>(null);
  // Returning to the subgroup list restores the leaving position; entering a group starts at the top.
  const listScroll = useRef(0);
  const paneKey = level === "seeds" ? `seeds:${activeGroup?.key ?? activeTitle}` : "groups";
  useLayoutEffect(() => {
    const node = scrollRef.current;
    if (!node) return;
    node.scrollTop = level === "groups" ? listScroll.current : 0;
  }, [paneKey, level]);

  const needle = filter.trim().toLowerCase();
  const rows = activeGroup ? (needle ? activeGroup.rows.filter((row) => row.title.toLowerCase().includes(needle)) : activeGroup.rows) : [];
  const enter = direction === "forward" ? "tw:motion-safe:animate-pane-forward" : "tw:motion-safe:animate-pane-back";

  return (
    // The step position renders once outside the scroll area: every layer (subgroups, seeds, empty) sees it, and switching layers never remounts it.
    <div className="tw:flex tw:min-h-0 tw:flex-1 tw:flex-col">
      <div className="tw:flex-none tw:px-6 tw:pt-3 tw:phone:px-4">{header}</div>
      <div
        ref={scrollRef}
        onScroll={(event) => {
          if (level === "groups") listScroll.current = event.currentTarget.scrollTop;
        }}
        className="tw:min-h-0 tw:flex-1 tw:overflow-y-auto tw:overscroll-contain tw:px-6 tw:pb-4 tw:phone:px-4"
      >
        {emptyState ? (
          <div className="tw:grid tw:gap-3 tw:py-4">
            {emptyState}
          </div>
        ) : level === "groups" ? (
          <section key={paneKey} className={cn("tw:grid tw:gap-3 tw:py-4", enter)} aria-label={t("oneshot.groupsTitle")}>
            {!loadingGroups && <p className="tw:m-0 tw:text-xs tw:text-muted-foreground">{t("oneshot.groupsCount", { n: groupEntries.length })}</p>}
            <GroupList entries={groupEntries} loading={loadingGroups} openingGroup={openingGroup} openError={openError} onOpen={onOpenGroup} />
          </section>
        ) : (
          <section key={paneKey} className={cn("tw:grid tw:content-start", enter)} aria-label={activeTitle}>
            <SeedToolbar
              title={activeTitle}
              count={activeGroup?.rows.length}
              onBack={onBack}
              onRefresh={onRefresh}
              refreshing={refreshing}
              pending={pending && !activeLoading}
              filter={filter}
              onFilterChange={onFilterChange}
              selectedCount={selectedCount}
              onSelectAll={onSelectAll}
              onClear={onClear}
            />
            <div className="tw:grid tw:gap-2 tw:pt-2">
              {refreshError && <StateBlock kind="error" title={refreshError} />}
              {activeLoading ? (
                <SeedListSkeleton />
              ) : !activeGroup ? null : activeGroup.rows.length === 0 ? (
                <StateBlock kind="empty" title={t("oneshot.groupEmpty")} />
              ) : rows.length === 0 ? (
                <StateBlock kind="empty" title={t("oneshot.noMatch")} />
              ) : (
                <ul className="tw:m-0 tw:grid tw:list-none tw:divide-y tw:divide-border tw:p-0 tw:motion-safe:animate-fade-in">
                  {rows.map((row) => (
                    <SeedRow
                      key={row.key}
                      row={row}
                      state={rowState(row)}
                      onToggleSelected={onToggleSelected}
                      onToggleExpanded={onToggleExpanded}
                    />
                  ))}
                </ul>
              )}
            </div>
          </section>
        )}
      </div>
    </div>
  );
}
