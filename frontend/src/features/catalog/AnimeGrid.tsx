import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useLocation, useSearchParams } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "../../ui/toast";
import { AlertTriangle, CheckCircle2, CircleHelp, PackageCheck, Plus, RotateCw, type LucideIcon } from "lucide-react";
import { isVersionConflict, newUUID, toUserMessage, type SubscribeResult } from "../../lib/api/client";
import { getCandidateStore } from "../../lib/candidate/serverCandidateStore";
import { blacklistKey, useBlacklist, useBlacklistActions } from "../../lib/blacklist";
import { getCatalogProvider, providerLabel } from "../../lib/providers/registry";
import { deleteCatalogSubscription, cleanupCatalogLocalData } from "../../lib/providers/catalogProvider";
import type { CatalogEntryView, CatalogSnapshotView } from "../../lib/providers/viewmodels";
import { formatTimeAgo } from "../../lib/labels";
import { animeKeys } from "../../lib/queryKeys";
import { SeasonPicker } from "./SeasonPicker";
import { currentSeason, parseSeason } from "./season";
import { BrowseCard, InstanceCard } from "./card";
import { catalogKeys, useCatalog, type GridRow } from "./queries";
import { catalogRowKey } from "./visibleRows";
import { applyStatsFilter, statsCounts, type StatsFilter } from "../../lib/stats";
import { cn } from "@/lib/utils";
import { Button } from "../../ui/base/controls";
import { Segmented } from "../../ui/base";
import { PHONE_MEDIA_QUERY, useMediaQuery } from "../../lib/useMediaQuery";
import { isOneOf, ROUTE_ANIME_TABS, ROUTE_RECORD_LIST, useRouteDialogs, type RouteAnimeTab } from "../../shell/routeDialogs";
import { useCatalogTab, type CatalogTabKey } from "../../shell/catalogTabs";
import { StateBlock } from "../../ui/StateBlock";
import { loadAnimeConfigDialog } from "../anime-config/loadAnimeConfigDialog";
import { loadOneShotDialog } from "../oneshot/loadOneShotDialog";
import { t } from "../../lib/i18n";
import { ConfirmLines } from "./ConfirmLines";
import { wallRowLabel, wallRows } from "./wallRows";
import { WallDayRow } from "./WallDayRow";
import { WALL_ROW_ITEM_CLASS, WALL_ROWS_CLASS } from "./wallRowClasses";
import { FilterInput, FilterToggle, StatsChips, StatusLine, ToolbarRow, type StatusTone } from "./GridToolbar";
import { useCollapseOnScroll } from "./useCollapseOnScroll";

const AnimeConfigDialogEntry = lazy(loadAnimeConfigDialog);
const OneShotDialogEntry = lazy(loadOneShotDialog);

const AddCandidateDialogEntry = lazy(() =>
  import("./AddCandidateDialogEntry").then((module) => ({ default: module.AddCandidateDialogEntry })),
);
const ConfirmDialogEntry = lazy(() =>
  import("../../ui/ConfirmDialogEntry").then((module) => ({ default: module.ConfirmDialogEntry })),
);
const DuplicateSubscriptionDialogEntry = lazy(() =>
  import("./DuplicateSubscriptionDialog").then((module) => ({ default: module.DuplicateSubscriptionDialog })),
);
const AnimeFilesDialogEntry = lazy(() =>
  import("../anime-files/AnimeFilesDialog").then((module) => ({ default: module.AnimeFilesDialog })),
);
const LocalDataCleanupDialogEntry = lazy(() =>
  import("./LocalDataCleanupDialog").then((module) => ({ default: module.LocalDataCleanupDialog })),
);

// AnimeGrid — the product's entry screen. Four columns:
//   Subscribed — the operations surface: wide instance cards (poster left + info right) carrying the
//                complex state, with stop/start, configure and unsubscribe; hiding is not supported
//                (hiding is browse-area curation).
//   AniBT      — browse: a wall of large 2:3 poster cards, one candidate card per entry, marked only
//                "subscribed" when a subscription exists (a boolean, never its state), with
//                subscribe/browse/hide actions.
//   Mikan      — the same as AniBT.
//   Custom     — browse: the user's saved custom RSS presets; adding a preset does not activate
//                anything, and subscribing on the card is what creates the instance.
// Hiding is catalog curation only: it removes an entry from the browse areas (and search results)
//   without ending any subscription, and the subscribed wall keeps showing that anime's
//   subscriptions; viewing and unhiding live only in settings' hidden-anime section. Clicking a
//   card's main area opens the four-tab config dialog directly (a candidate is draft editing, the
//   subscribed wall is owner-edits in the workspace), with no first-level detail dialog in between.

const SEASON_QUERY_KEY = "season";

type TabKey = CatalogTabKey;

type CatalogFilterState = {
  query: string;
  statsFilter: StatsFilter | null;
};

const BROWSABLE_TABS: TabKey[] = ["anibt", "mikan"];

// The phone browse columns' segmented control (subscribed is reached by the bottom tab bar and is not among the options).
const browseSegOptions = (): { value: TabKey; label: string }[] => [
  { value: "anibt", label: "AniBT" },
  { value: "mikan", label: "Mikan" },
  { value: "custom", label: t("grid.tabCustom") },
];

function initialCatalogFilters(): Record<TabKey, CatalogFilterState> {
  return {
    all: { query: "", statsFilter: null },
    mikan: { query: "", statsFilter: null },
    anibt: { query: "", statsFilter: null },
    custom: { query: "", statsFilter: null },
  };
}

export function AnimeGrid() {
  useEffect(() => {
    void loadAnimeConfigDialog();
  }, []);

  const [searchParams, setSearchParams] = useSearchParams();
  // The column state is shared with shell/catalogTabs: desktop tabs in the header, on phones in the
//   bottom tab bar plus the browse segmented control; the URL query wins and session memory is the
//   fallback without one.
  const { tab, setTab: setCatalogTab } = useCatalogTab();
  const isPhone = useMediaQuery(PHONE_MEDIA_QUERY);
  // The season lives in the base address (`?season=2026-summer`) so an anime-config deep link
//   refreshed returns to the same season; missing or invalid falls back to the current season.
//   Search results are not in the address, so a search deep link stays unrestorable after a
//   refresh.
  const seasonParam = searchParams.get(SEASON_QUERY_KEY);
  // When the catalog is under the settings or another overlay the address carries no season, so the most recent base season continues.
  const onBase = useLocation().pathname === "/";
  const lastBaseSeasonRef = useRef<string | null>(null);
  const resolvedSeason = seasonParam && parseSeason(seasonParam) ? seasonParam : currentSeason();
  if (onBase) lastBaseSeasonRef.current = resolvedSeason;
  const season = onBase ? resolvedSeason : (lastBaseSeasonRef.current ?? resolvedSeason);
  const setSeason = useCallback(
    (next: string) => {
      setSearchParams(
        (prev) => {
          prev.set(SEASON_QUERY_KEY, next);
          return prev;
        },
        { replace: true },
      );
    },
    [setSearchParams],
  );
  // Each catalog column keeps its own local filter; switching providers never drops the user's just-made filter.
  const [filtersByTab, setFiltersByTab] = useState<Record<TabKey, CatalogFilterState>>(initialCatalogFilters);
  const query = filtersByTab[tab].query;
  const statsFilter = filtersByTab[tab].statsFilter;
  const [stateChanging, setStateChanging] = useState<Set<string>>(new Set());
  // The "usable only" toggle: the site catalog hides entries the site marks as having no seeds (unknowns show as usual).
  const [availableOnly, setAvailableOnly] = useState(false);

  // The anime config is route-carried (`/?anime=<catalog row key>&tab=…`, see shell/routeDialogs.ts):
//   the URL decides open/closed; configRow freezes the catalog row at open time (the same as the
//   old entry snapshot at click), resolved on refresh/deep link by row key from the current column,
//   the search results, or the subscribed list. Once the address leaves, the dialog decides when to
//   unmount (confirming unsaved changes first), so configRow clears only after the dialog reports
//   closed.
  const routeDialogs = useRouteDialogs();
  const animeParam = routeDialogs.get("anime");
  const animeTabParam = routeDialogs.get("tab");
  const [configRow, setConfigRow] = useState<{ key: string; row: GridRow; initialTab: RouteAnimeTab } | null>(null);
  const configRouteOpen = configRow !== null && animeParam === configRow.key;
  const lastConfigTabRef = useRef<RouteAnimeTab>("metadata");
  const [addOpen, setAddOpen] = useState(false);
  const [pendingDelete, setPendingDelete] = useState<CatalogEntryView | null>(null);
  const [pendingUnsubscribe, setPendingUnsubscribe] = useState<CatalogEntryView | null>(null);
  const [pendingLocalCleanup, setPendingLocalCleanup] = useState<CatalogEntryView | null>(null);
  const [pendingDuplicate, setPendingDuplicate] = useState<CatalogEntryView | null>(null);
  // The card's files action: the per-anime files dialog (a standalone dialog, not route-carried; a retired subscription opens read-only).
  const [filesEntry, setFilesEntry] = useState<CatalogEntryView | null>(null);

  const queryClient = useQueryClient();
  const catalog = useCatalog(tab, season);
  const rows = useMemo(() => catalog.data?.rows ?? [], [catalog.data]);
  // A deep link to a subscribed instance missing from the current column (e.g. another season): fall back to resolving in the subscribed list.
  const resolvingAnime = animeParam !== null && configRow === null;
  const subscribedCatalog = useCatalog("all", season, { enabled: resolvingAnime && tab !== "all" });
  const blacklist = useBlacklist();
  const { add: addBlacklist } = useBlacklistActions();
  // The key carries the provider: the same bare ref is two entries under two providers.
  const blockedKeys = useMemo(() => new Set(blacklist.map((entry) => entry.key)), [blacklist]);
  const isBlocked = useCallback(
    (entry: CatalogEntryView) => blockedKeys.has(blacklistKey(entry.source, entry.providerEntryRef)),
    [blockedKeys],
  );
  const snapshot = catalog.data?.snapshot;
  const loading = catalog.isLoading;
  const refreshing = catalog.isFetching;
  const error = catalog.error
    ? toUserMessage(
        catalog.error,
        tab === "anibt" ? t("grid.loadFailedSite", { site: "AniBT" }) : tab === "mikan" ? t("grid.loadFailedSite", { site: "Mikan" }) : t("grid.loadFailed"),
      )
    : undefined;

  // Owner mutations here (retire, stop/start, delete, cleanup) change the
  // lifecycle the workspace read model gates on; a cached workspace would reopen
  // a retired owner as editable.
  const invalidateCatalog = useCallback(
    () => Promise.all([
      queryClient.invalidateQueries({ queryKey: catalogKeys.all }),
      queryClient.invalidateQueries({ queryKey: animeKeys.list }),
      queryClient.invalidateQueries({ queryKey: animeKeys.workspacePrefix }),
    ]),
    [queryClient],
  );

  const setTab = useCallback(
    (next: TabKey) => {
      setCatalogTab(next);
    },
    [setCatalogTab],
  );

  // A manual refresh trusts the refresh endpoint's return: toast per that result, then invalidate the catalog query once to re-read the list.
  async function refresh() {
    if (!BROWSABLE_TABS.includes(tab)) return;
    const site = tab === "mikan" ? "Mikan" : "AniBT";
    try {
      const outcome = await getCatalogProvider(tab as "mikan" | "anibt").refreshCatalog(season);
      await invalidateCatalog();
      if (outcome === "outage") toast.error(t("grid.refreshOutage", { site }));
      else if (outcome === "withdrawn") toast.warning(t("grid.refreshWithdrawn", { site }));
      else if (outcome === "zero_result") toast.info(t("grid.refreshZeroResult", { site }));
      else toast.success(t("grid.refreshed"));
    } catch (cause) {
      toast.error(toUserMessage(cause, t("grid.refreshFailed", { site })));
    }
  }

  // The catalog toolbar has no search (from the top-bar review): keyword search goes only through the header's global search.
  const browsable = BROWSABLE_TABS.includes(tab);

  // Invalidates the catalog only: closing is the dialog's own decision — it closes itself after a
//   successful subscription (its internal onOpenChange(false) inside handleSubscribed).
  function onSubscribed() {
    void invalidateCatalog();
  }

  /** Opens the four-tab config dialog directly on a card click (an unsubscribed entry is a draft;
   *  subscribing completes inside the dialog).
   *  Hidden anime never enter the list, so no extra guard is needed here (deep links are handled
   *  separately below).
   *  Retired instances: the backend keeps the complete owner graph
   *  (Release/DownloadTask/Artifact/FileOperation/logs), so opening stays allowed here too — the
   *  dialog enters explicit read-only mode per lifecycle_state (capability turns off every
   *  mutation) rather than using a toast to block history viewing. */
  function openConfig(row: GridRow, tab?: RouteAnimeTab) {
    const key = catalogRowKey(row.entry);
    const initialTab = tab ?? defaultAnimeTab(row.entry);
    lastConfigTabRef.current = initialTab;
    setConfigRow({ key, row, initialTab });
    routeDialogs.open({ anime: key, tab: initialTab });
  }

  const closeConfigRow = useCallback(() => setConfigRow(null), []);
  // "Browse seeds": view the seeds and naming results read-only, and start a one-shot download in
//   the same place. The task flow lives in component state (not on the route); each open remounts by
//   sequence so the previous step's selections are never carried in.
  const [seedTarget, setSeedTarget] = useState<{ key: string; entry: CatalogEntryView; seq: number } | null>(null);
  const seedSeqRef = useRef(0);
  function openBrowseSeeds(entry: CatalogEntryView) {
    seedSeqRef.current += 1;
    setSeedTarget({ key: catalogRowKey(entry), entry, seq: seedSeqRef.current });
  }

  const { open: openRouteDialog } = routeDialogs;
  const restoreConfigKey = configRow?.key;
  const restoreConfigRoute = useCallback(() => {
    if (restoreConfigKey) openRouteDialog({ anime: restoreConfigKey, tab: lastConfigTabRef.current });
  }, [openRouteDialog, restoreConfigKey]);

  // Refresh/deep link: resolve the catalog row from `?anime=`. A hidden and unsubscribed entry does
//   not open — the parameter is dropped after a hint; when every source is exhausted and the row is
//   still not found (a draft from another season) the parameter drops, returning the base address,
//   rather than opening a dialog of unclear identity.
  const { dismiss: dismissRouteDialog } = routeDialogs;
  useEffect(() => {
    if (!resolvingAnime || animeParam === null) return;
    const match = (candidate: GridRow) => catalogRowKey(candidate.entry) === animeParam;
    const found = rows.find(match) ?? subscribedCatalog.data?.rows.find(match);
    if (found) {
      if (found.entry.subscriptionState === undefined && isBlocked(found.entry)) {
        toast.info(t("grid.blockedFirst"));
        dismissRouteDialog();
        return;
      }
      const initialTab = isOneOf(ROUTE_ANIME_TABS, animeTabParam) ? animeTabParam : defaultAnimeTab(found.entry);
      lastConfigTabRef.current = initialTab;
      setConfigRow({ key: animeParam, row: found, initialTab });
      return;
    }
    const settled = (query: { isPending: boolean; isError: boolean }) => !query.isPending || query.isError;
    if (settled(catalog) && (tab === "all" || settled(subscribedCatalog))) dismissRouteDialog();
  }, [animeParam, animeTabParam, catalog, dismissRouteDialog, isBlocked, resolvingAnime, rows, subscribedCatalog, tab]);

  // The hide control exists only in the browse areas (never on the subscribed wall). Hiding only
//   writes the catalog suppression — subscription or not: it ends no subscription and needs no
//   confirmation.
  function hideEntry(entry: CatalogEntryView) {
    void addBlacklist({
      source: entry.source,
      providerEntryRef: entry.providerEntryRef,
      title: entry.title,
    })
      .then(() => toast.success(t("grid.blacklistAdded")))
      .catch((err: unknown) => toast.error(toUserMessage(err, t("grid.blacklistFailed"))));
  }

  /** Deleting an unsubscribed custom candidate needs a second confirmation (irrecoverable; the dialog lists the config summary); a subscribed one may never be deleted. */
  function onDeleteCandidate(entry: CatalogEntryView) {
    if (entry.subscriptionState !== undefined || !entry.candidateId) return;
    setPendingDelete(entry);
  }

  function confirmDeleteCandidate() {
    const entry = pendingDelete;
    if (!entry?.candidateId) return;
    const candidateId = entry.candidateId;
    setPendingDelete(null);
    void getCandidateStore()
      .delete(candidateId)
      .then(() => {
        toast.success(t("grid.presetDeleted", { title: entry.title }));
        void invalidateCatalog();
      })
      .catch((cause) => toast.error(toUserMessage(cause, t("grid.presetDeleteFailed", { title: entry.title }))));
  }

  async function toggleState(row: GridRow) {
    const entry = row.entry;
    const ref = entry.managedAnimeRef;
    if (entry.lifecycleState === "retired") return;
    if (stateChanging.has(ref ?? "")) return;
    // A state toggle is a CAS action against the exact instance: with the instance identity or its
//     version missing it must fail closed (refresh the catalog plus a clear notice), never touching
//     another instance under the same provider ref.
    if (!ref || entry.subscriptionVersion === undefined) {
      await invalidateCatalog();
      toast.error(t("catalog.locateFailed"));
      return;
    }
    const starting = entry.subscriptionState === "pending";
    const nextState = entry.subscriptionState === "active" ? "stopped" : "active";
    setStateChanging((current) => new Set(current).add(ref));
    try {
      await row.provider.setSubscriptionState(ref, {
        expected_version: entry.subscriptionVersion,
        state: nextState,
      });
      await invalidateCatalog();
      toast.success(starting ? t("anime.started") : nextState === "stopped" ? t("grid.paused") : t("grid.resumed"));
    } catch (cause) {
      if (isVersionConflict(cause)) {
        // 409: the version changed — refetch the catalog so the card returns to the latest state/version, then ask the user to retry.
        await invalidateCatalog();
        toast.error(t("catalog.changed"));
      } else {
        toast.error(
          toUserMessage(
            cause,
            starting ? t("anime.startFailed") : nextState === "stopped" ? t("grid.pauseFailed") : t("grid.resumeFailed"),
          ),
        );
      }
    } finally {
      setStateChanging((current) => {
        const next = new Set(current);
        next.delete(ref);
        return next;
      });
    }
  }

  /** Unsubscribes: retires the subscription instance irreversibly (the card's stop/start button
   *  agrees with the stopped state; the copy here says unsubscribe in both cases).
   *  A second confirmation; a success toast plus catalog invalidation to re-read. The confirm state
   *  keeps only the entry, and on execution the version is re-queried from the latest rows by
   *  managedAnimeRef, so a stale version never produces a 409.
   *  A retired instance is retired: this mutation entry no longer exists. */
  function requestUnsubscribe(entry: CatalogEntryView) {
    if (entry.lifecycleState === "retired") return;
    if (entry.subscriptionState === undefined) return;
    setPendingUnsubscribe(entry);
  }

  function confirmUnsubscribe() {
    const pending = pendingUnsubscribe;
    if (!pending) return;
    setPendingUnsubscribe(null);
    // Locating the instance precisely: the confirm dialog holds only the entry, and execution
//     re-queries the version from the latest rows by managedAnimeRef; a missing exact instance fails
//     closed with a refresh, never reusing the first row under the same provider ref.
    const ref = pending.managedAnimeRef;
    if (!ref) {
      void invalidateCatalog();
      toast.error(t("catalog.locateFailed"));
      return;
    }
    void (async () => {
      const latest = rows.find((row) => row.entry.managedAnimeRef === ref)?.entry;
      if (!latest) {
        await invalidateCatalog();
        toast.error(t("catalog.locateFailed"));
        return;
      }
      const version = latest.subscriptionVersion;
      if (version === undefined) {
        await invalidateCatalog();
        toast.error(t("catalog.changed"));
        return;
      }
      try {
        await deleteCatalogSubscription(ref, { expected_version: version });
        await invalidateCatalog();
        toast.success(t("catalog.ended", { title: latest.title }));
      } catch (cause) {
        if (isVersionConflict(cause)) {
          await invalidateCatalog();
          toast.error(t("catalog.changed"));
        } else {
          toast.error(toUserMessage(cause, t("catalog.endFailed", { title: latest.title })));
        }
      }
    })();
  }

  /** Cleans local records: an explicit, separately authorized action targeted only at retired
   *  instances, with confirmation copy that must state "no qB data and no media files are deleted".
   *  On execution the latest lifecycle epoch is re-queried by managedAnimeRef (CAS), so a stale epoch
   *  never produces a 409; both success and conflict refresh the catalog (the local list drops the
   *  entry and the browse areas show it as unsubscribed). */
  function requestLocalCleanup(entry: CatalogEntryView) {
    if (entry.lifecycleState !== "retired") return;
    if (!entry.managedAnimeRef) return;
    setPendingLocalCleanup(entry);
  }

  function confirmLocalCleanup() {
    const pending = pendingLocalCleanup;
    if (!pending) return;
    setPendingLocalCleanup(null);
    const ref = pending.managedAnimeRef;
    if (!ref) {
      void invalidateCatalog();
      toast.error(t("catalog.locateFailed"));
      return;
    }
    void (async () => {
      const latest = rows.find((row) => row.entry.managedAnimeRef === ref)?.entry;
      if (!latest) {
        await invalidateCatalog();
        toast.error(t("catalog.locateFailed"));
        return;
      }
      const epoch = latest.lifecycleEpoch;
      if (epoch === undefined) {
        await invalidateCatalog();
        toast.error(t("catalog.changed"));
        return;
      }
      try {
        await cleanupCatalogLocalData(
          ref,
          { expected_lifecycle_epoch: epoch, confirmation: "delete-local-data" },
          newUUID(),
        );
        await invalidateCatalog();
        toast.success(t("grid.localDeleted", { title: latest.title }));
      } catch (cause) {
        await invalidateCatalog();
        toast.error(toUserMessage(cause, t("grid.localDeleteFailed", { title: latest.title })));
      }
    })();
  }

  /** On a successful duplicate: re-read the catalog and open the new subscription's config dialog by its row key (not yet started, so settings can change before starting). */
  async function openDuplicated(result: SubscribeResult) {
    await invalidateCatalog();
    toast.success(t("grid.duplicated"));
    routeDialogs.open({ anime: result.managed_anime.id });
  }

  function updateQuery(value: string) {
    setFiltersByTab((current) => ({
      ...current,
      [tab]: { ...current[tab], query: value },
    }));
  }

  function toggleStatsFilter(key: StatsFilter) {
    setFiltersByTab((current) => ({
      ...current,
      [tab]: {
        ...current[tab],
        statsFilter: current[tab].statsFilter === key ? null : key,
      },
    }));
  }

  // Hidden anime are removed from the browse columns, with no list toggle to show them; the only
//   place to see them is settings' hidden-anime section. The subscribed wall does not filter by
//   hidden: hiding never affects a subscription.
  const shownRows = useMemo(
    () => (tab === "all" ? rows : rows.filter((r) => !isBlocked(r.entry))),
    [rows, isBlocked, tab],
  );

  const visibleRows = useMemo(() => {
    let list = shownRows;
    if (browsable && availableOnly) list = list.filter((r) => r.entry.releaseState !== "no_releases");
    if (query.trim()) {
      const q = query.trim().toLowerCase();
      list = list.filter(
        (r) =>
          r.entry.title.toLowerCase().includes(q) ||
          r.entry.originalTitle?.toLowerCase().includes(q),
      );
    }
    return applyStatsFilter(list, statsFilter);
  }, [shownRows, query, statsFilter, browsable, availableOnly]);


  // The subscribed wall and site catalogs are one row per day: Monday through Sunday, movie,
//   OVA/ONA, custom, other — empty rows never render and rows scroll horizontally. A filtered row
//   disappears with it. The release format is the fact the site catalog states.
  const wallRowList = useMemo(
    () =>
      tab === "all" || browsable
        ? wallRows(visibleRows, (row) => ({
            broadcastDay: row.entry.broadcastDay,
            mediaKind: row.entry.metadataMatch.mediaKindOverride ?? row.entry.metadataMatch.tmdbMediaKind,
            format: row.entry.releaseFormat,
            custom: row.entry.source === "custom",
          }))
        : null,
    [tab, browsable, visibleRows],
  );
  // The count is based on unfiltered rows (hidden ones never counted), matching the chip's mutually exclusive segments.
  const stats = useMemo(() => statsCounts(shownRows.map((r) => r.entry)), [shownRows]);

  const isCustom = tab === "custom";
  // The site status shows only after the query returns: no placeholder state like "no site data yet" while loading.
  const snapshotStatus = browsable && catalog.isSuccess ? snapshotMeta(snapshot, rows.length) : undefined;
  const settledEmpty = !loading && !error && rows.length === 0;
  // An empty custom column: its empty state carries Add preset itself, so the toolbar does not repeat it.
  const showAddPreset = isCustom && !settledEmpty;
  // The filter box is absent when nothing can be filtered; it stays while a condition exists so it can be cleared.
  const showFilter = rows.length > 0 || query !== "";
  const siteName = tab === "mikan" ? "Mikan" : "AniBT";
  const [phoneFilterOpen, setPhoneFilterOpen] = useState(false);
  const filterFieldOpen = phoneFilterOpen || query !== "";
  const togglePhoneFilter = () => {
    if (filterFieldOpen) {
      setPhoneFilterOpen(false);
      updateQuery("");
    } else {
      setPhoneFilterOpen(true);
    }
  };
  // Phone: the browse column's toolbar collapses on scroll; the subscribed wall's toolbar does not stick and never covers the day separator.
  const collapsible = isPhone && tab !== "all";
  const { ref: toolbarRef, collapsed: toolbarCollapsed } = useCollapseOnScroll<HTMLDivElement>(collapsible);
  const statsChips = [
    { key: "pending" as const, label: t("anime.state.pending"), count: stats.pending },
    { key: "active" as const, label: t("anime.state.active"), count: stats.active },
    { key: "stopped" as const, label: t("anime.state.stopped"), count: stats.stopped },
  ];

  // A single catalog row's card: a browse card in the browse areas, a poster card when subscribed.
  const renderCard = (row: GridRow) =>
      tab === "all" ? (
        <InstanceCard
          entry={row.entry}
          busy={!!row.entry.managedAnimeRef && stateChanging.has(row.entry.managedAnimeRef)}
          onOpenConfig={(_entry, tab) => openConfig(row, tab)}
          onToggleState={() => void toggleState(row)}
          onDuplicate={(entry) => setPendingDuplicate(entry)}
          onUnsubscribe={() => requestUnsubscribe(row.entry)}
          onCleanupLocalData={requestLocalCleanup}
          onOpenFiles={(entry) => setFilesEntry(entry)}
        />
      ) : (
        <BrowseCard
          entry={row.entry}
          onHide={() => hideEntry(row.entry)}
          onOpenConfig={(entry) => openConfig({ ...row, entry })}
          onDeleteCandidate={onDeleteCandidate}
          onBrowseSeeds={openBrowseSeeds}
        />
      );

  // The usable-only toggle is a pressed-state button (aria-pressed, primary-colored outline when
//   pressed), matching the subscribed-count chip's shape; on phones only the icon survives.
  const availableToggle = browsable && rows.length > 0 && (
    <Button
      size={isPhone ? "icon" : "md"}
      aria-pressed={availableOnly}
      aria-label={t("grid.availableOnly")}
      title={t("grid.availableOnly")}
      className="tw:flex-none"
      onClick={() => setAvailableOnly((value) => !value)}
    >
      <PackageCheck aria-hidden className="tw:size-4" />
      {!isPhone && t("grid.availableOnly")}
    </Button>
  );
  // Refresh is a secondary button (the design language's button tier); on phones only the icon survives.
  const refreshButton = browsable && (
    <Button
      size={isPhone ? "icon" : "md"}
      disabled={refreshing}
      aria-label={t("grid.refreshSite", { site: siteName })}
      title={t("grid.refreshSite", { site: siteName })}
      onClick={() => void refresh()}
    >
      <RotateCw aria-hidden className={cn("tw:size-4", refreshing && "tw:motion-safe:animate-spin")} />
      {!isPhone && t("grid.refreshSite", { site: siteName })}
    </Button>
  );
  const addPresetButton = showAddPreset && (
    <Button variant="primary" onClick={() => setAddOpen(true)}>
      <Plus className="tw:size-4" aria-hidden />
      {t("grid.addPreset")}
    </Button>
  );
  const statusLine = snapshotStatus && <StatusLine {...snapshotStatus} />;
  const chips = tab === "all" && rows.length > 0 && (
    <StatsChips chips={statsChips} active={statsFilter} onToggle={toggleStatsFilter} ariaLabel={t("grid.statsAria")} scroll={isPhone} />
  );

  // The toolbar: one line, no card chrome (from the top-bar review). Sticks on desktop; on phones the
//   browse column collapses it while scrolling, and the subscribed wall never sticks.
  const toolbar = isPhone ? (
    <>
      {tab !== "all" && (
        <Segmented
          aria-label={t("nav.mainNavAria")}
          className="catalog-browse-seg"
          value={tab}
          onValueChange={(next) => setTab(next)}
          options={browseSegOptions()}
        />
      )}
      {(browsable || chips || showFilter || addPresetButton) && (
        <div className="tw:flex tw:min-w-0 tw:items-center tw:gap-2">
          {browsable && <SeasonPicker value={season} onChange={setSeason} />}
          {chips}
          {addPresetButton}
          <div className="tw:ml-auto tw:flex tw:flex-none tw:items-center tw:gap-2">
            {showFilter && <FilterToggle open={filterFieldOpen} onToggle={togglePhoneFilter} />}
            {availableToggle}
            {refreshButton}
          </div>
        </div>
      )}
      {showFilter && filterFieldOpen && <FilterInput value={query} onChange={updateQuery} autoFocus={phoneFilterOpen && query === ""} />}
      {statusLine}
    </>
  ) : tab === "all" ? (
    <ToolbarRow end={showFilter && <FilterInput value={query} onChange={updateQuery} className="tw:w-56" />}>{chips}</ToolbarRow>
  ) : (
    <ToolbarRow
      end={
        <>
          {statusLine}
          {refreshButton}
          {addPresetButton}
        </>
      }
    >
      {browsable && <SeasonPicker value={season} onChange={setSeason} />}
      {showFilter && <FilterInput value={query} onChange={updateQuery} className="tw:w-56" />}
      {availableToggle}
    </ToolbarRow>
  );

  return (
    <div className="catalog-page">
      <div
        ref={toolbarRef}
        className={cn(
          "tw:z-30 tw:grid tw:min-w-0 tw:gap-2 tw:bg-background/95 tw:py-2 tw:backdrop-blur-md",
          isPhone && tab === "all" ? "tw:relative" : "tw:sticky tw:top-0",
          collapsible && "tw:motion-safe:transition-transform tw:motion-safe:duration-300 tw:motion-safe:ease-out",
          toolbarCollapsed && "tw:-translate-y-full",
        )}
      >
        {toolbar}
      </div>

      {/* An honest error state: a catalog read failure shows the error plus a refetch retry, never posing as an empty state. */}
      {error && (
        <StateBlock
          kind="error"
          title={error}
          action={
            <Button size="sm" disabled={refreshing || catalog.isFetching} onClick={() => void catalog.refetch()}>
              {t("grid.retry")}
            </Button>
          }
        />
      )}

      {snapshot?.status === "error" && (
        <StateBlock
          kind="error"
          title={snapshot.errorMessage ?? t("grid.siteRefreshFailed")}
          action={
            <Button size="sm" disabled={refreshing} onClick={() => void refresh()}>
              {t("grid.retry")}
            </Button>
          }
        />
      )}
      {snapshot?.status === "stale" && (
        <p role="status" className="catalog-stale">
          {t("grid.stale")}
        </p>
      )}

      <section aria-live="polite">
        {loading && rows.length === 0 ? (
          <StateBlock kind="busy" title={t("grid.loading")} />
        ) : error ? null : settledEmpty ? (
          <EmptyHint
            tab={tab}
            onAddPreset={() => setAddOpen(true)}
            onBrowseProvider={(provider) => setTab(provider)}
          />
        ) : visibleRows.length === 0 ? (
          <StateBlock kind="empty" title={t("search.noMatch")} description={t("grid.empty.filteredDesc")} />
        ) : wallRowList ? (
          <div className={WALL_ROWS_CLASS}>
            {wallRowList.map(({ key, items }) => (
              <WallDayRow key={key} label={wallRowLabel(key)} itemCount={items.length}>
                {items.map((row) => (
                  <li key={catalogRowKey(row.entry)} className={WALL_ROW_ITEM_CLASS}>
                    {renderCard(row)}
                  </li>
                ))}
              </WallDayRow>
            ))}
          </div>
        ) : (
          <ul className="catalog-grid-browse">
            {visibleRows.map((row) => (
              <li key={catalogRowKey(row.entry)}>{renderCard(row)}</li>
            ))}
          </ul>
        )}
      </section>

      {/* The first-level dialog is gone: a card click opens the four-tab config dialog directly. */}
      {configRow && (
        <Suspense fallback={null}>
          <AnimeConfigDialogEntry
            key={configRow.key}
            open
            routeHosted
            routeOpen={configRouteOpen}
            onRouteClosed={closeConfigRow}
            onRouteRestore={restoreConfigRoute}
            onOpenChange={(o) => {
              if (!o) routeDialogs.close();
            }}
            provider={configRow.row.provider}
            entry={configRow.row.entry}
            sourceLabel={providerLabel(configRow.row.entry.source)}
            onSubscribed={onSubscribed}
            onCandidateStateChanged={onSubscribed}
            initialTab={configRow.initialTab}
            onTabChange={(next) => {
              if (!isOneOf(ROUTE_ANIME_TABS, next)) return;
              lastConfigTabRef.current = next;
              if (configRouteOpen) routeDialogs.update({ tab: next });
            }}
          />
        </Suspense>
      )}

      {seedTarget && (
        <Suspense fallback={null}>
          <OneShotDialogEntry
            key={`${seedTarget.key}#${seedTarget.seq}`}
            open
            entry={seedTarget.entry}
            onClose={() => setSeedTarget(null)}
            onOpenRecords={() => {
              setSeedTarget(null);
              routeDialogs.open({ record: ROUTE_RECORD_LIST, record_segment: "one_shot" });
            }}
          />
        </Suspense>
      )}

      {addOpen && (
        <Suspense fallback={null}>
          <AddCandidateDialogEntry
            open
            onOpenChange={setAddOpen}
            onAdded={() => {
              setAddOpen(false);
              toast.success(t("grid.presetSaved"));
              void invalidateCatalog();
            }}
          />
        </Suspense>
      )}

      {pendingDelete && (
        <Suspense fallback={null}>
          <ConfirmDialogEntry
            open
            onOpenChange={(open) => {
              if (!open) setPendingDelete(null);
            }}
            title={t("grid.deletePresetTitle", { title: pendingDelete.title })}
            description={
              <ConfirmLines
                lines={[
                  t("grid.deletePresetLine1", { label: pendingDelete.instanceLabel ?? "-", group: pendingDelete.groupName ?? "-", url: pendingDelete.rssUrl ?? "-" }),
                  t("grid.deletePresetLine2"),
                ]}
              />
            }
            confirmLabel={t("grid.deletePresetConfirm")}
            danger
            onConfirm={confirmDeleteCandidate}
          />
        </Suspense>
      )}

      {pendingUnsubscribe && (
        <Suspense fallback={null}>
          <ConfirmDialogEntry
            open
            onOpenChange={(open) => {
              if (!open) setPendingUnsubscribe(null);
            }}
            title={t("catalog.endTitle", { title: pendingUnsubscribe.title })}
            description={<ConfirmLines lines={[t("grid.confirm.endLine1"), t("grid.confirm.endLine2")]} />}
            confirmLabel={t("catalog.endConfirm")}
            danger
            onConfirm={confirmUnsubscribe}
          />
        </Suspense>
      )}
      {pendingDuplicate && (
        <Suspense fallback={null}>
          <DuplicateSubscriptionDialogEntry
            entry={pendingDuplicate}
            onOpenChange={(open) => {
              if (!open) setPendingDuplicate(null);
            }}
            onDuplicated={openDuplicated}
          />
        </Suspense>
      )}
      {filesEntry?.managedAnimeRef && (
        <Suspense fallback={null}>
          <AnimeFilesDialogEntry
            animeID={filesEntry.managedAnimeRef}
            title={filesEntry.title}
            readOnly={filesEntry.lifecycleState === "retired"}
            onChanged={() => void invalidateCatalog()}
            onClose={() => setFilesEntry(null)}
          />
        </Suspense>
      )}
      {pendingLocalCleanup && (
        <Suspense fallback={null}>
          <LocalDataCleanupDialogEntry
            entry={pendingLocalCleanup}
            onOpenChange={(open) => {
              if (!open) setPendingLocalCleanup(null);
            }}
            onConfirm={confirmLocalCleanup}
          />
        </Suspense>
      )}
    </div>
  );
}

// A subscribed entry opening the four-tab dialog lands on RSS (the outcome/operations surface) by default; a draft stays on metadata.
function defaultAnimeTab(entry: CatalogEntryView): RouteAnimeTab {
  return entry.subscriptionState !== undefined ? "rss" : "metadata";
}

// The site data status line (from the top-bar review): "101 titles, updated 10 hours ago"; other states name the state itself.
function snapshotMeta(
  snapshot: CatalogSnapshotView | undefined,
  entryCount: number,
): { text: string; tone: StatusTone; Icon: LucideIcon } {
  const join = (...parts: (string | undefined)[]) => parts.filter(Boolean).join(" · ");
  if (!snapshot) {
    return { text: join(t("grid.snapshot.noneLabel"), t("grid.snapshot.noneDetail")), tone: "muted", Icon: CircleHelp };
  }
  // Freshness reads only the most recent successful refresh time (the server's last_success_at), never the most recent attempt.
  const ago = formatTimeAgo(snapshot.lastSuccessAt);
  const updated = ago ? t("grid.snapshot.updatedAgo", { time: ago }) : undefined;
  switch (snapshot.status) {
    case "fresh":
      return { text: join(t("grid.snapshot.freshDetail", { n: entryCount }), updated), tone: "online", Icon: CheckCircle2 };
    case "stale":
      return { text: join(t("grid.snapshot.staleLabel"), updated ?? t("grid.snapshot.staleDetail")), tone: "warning", Icon: AlertTriangle };
    case "error":
      return { text: join(t("grid.snapshot.errorLabel"), t("grid.snapshot.errorDetail"), updated), tone: "offline", Icon: AlertTriangle };
    case "empty":
    default:
      return {
        text: join(t("grid.snapshot.emptyLabel"), snapshot.withdrawn ? t("grid.snapshot.withdrawnDetail") : t("grid.snapshot.emptyDetail"), updated),
        tone: "muted",
        Icon: CircleHelp,
      };
  }
}

/** The day separator above each card on the wall: the day's first card spells the broadcast day, later cards of the same day get only the line. */
function EmptyHint({
  tab,
  onAddPreset,
  onBrowseProvider,
}: {
  tab: TabKey;
  onAddPreset: () => void;
  onBrowseProvider: (provider: "anibt" | "mikan") => void;
}) {
  const map: Record<TabKey, { title: string; desc: string }> = {
    all: { title: t("catalog.emptySubscribed"), desc: t("grid.empty.allDesc") },
    anibt: { title: t("search.noMatch"), desc: t("grid.empty.browseDesc") },
    mikan: { title: t("search.noMatch"), desc: t("grid.empty.browseDesc") },
    custom: { title: t("grid.empty.customTitle"), desc: t("grid.empty.customDesc") },
  };
  const { title, desc } = map[tab];
  const action =
    tab === "all" ? (
      <>
        <Button variant="primary" size="sm" onClick={() => onBrowseProvider("anibt")}>
          {t("grid.empty.browseAnibt")}
        </Button>
        <Button size="sm" onClick={() => onBrowseProvider("mikan")}>
          {t("grid.empty.browseMikan")}
        </Button>
      </>
    ) : tab === "custom" ? (
      <Button variant="primary" onClick={onAddPreset}>
        <Plus className="tw:size-4" aria-hidden />
        {t("grid.addPreset")}
      </Button>
    ) : undefined;
  return <StateBlock kind="empty" title={title} description={desc} action={action} />;
}
