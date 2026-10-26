import { lazy, Suspense, useCallback, useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { NavLink, useNavigate } from "react-router-dom";
import { cn } from "@/lib/utils";
import {
  CalendarDays,
  ClipboardList,
  Compass,
  History,
  Layers,
  Menu,
  MonitorPlay,
  Search,
  Settings,
} from "lucide-react";
const AccountMenuEntry = lazy(() =>
  import("./AccountMenuEntry").then((module) => ({ default: module.AccountMenuEntry })),
);
const QBManagerDialogEntry = lazy(() =>
  import("../features/qb-manager/QBManagerDialogEntry").then((module) => ({ default: module.QBManagerDialogEntry })),
);
// The file manager is hidden in this version: the automatic mainline does not
// need interactive file management. features/file-manager/* and its component
// tests are kept. To restore it:
// 1. bring back the lazy FileManagerDialogEntry here plus the
//    fileManagerOpen / path-param / mount block below;
// 2. add "files" back to ROUTE_TOOLS in routeDialogs.ts;
// 3. bring back the top-bar button, the MoreSheet file-manager row
//    (onOpenFiles) and the OpenInFileManagerButton in FileEffectPolicySection.
const LogDialogEntry = lazy(() =>
  import("./LogDialogEntry").then((module) => ({ default: module.LogDialogEntry })),
);
const ExecutionRecordsDialogEntry = lazy(() =>
  import("../features/records/ExecutionRecordsDialogEntry").then((module) => ({ default: module.ExecutionRecordsDialogEntry })),
);
const GlobalSearchDialog = lazy(() =>
  import("../features/catalog/GlobalSearchDialog").then((module) => ({ default: module.GlobalSearchDialog })),
);
import {
  listAutomationSchedules,
  listConnections,
  testConnection,
  type AutomationKind,
  type AutomationSchedule,
  type Connection,
  type ConnectionTestResult,
} from "../lib/api/client";
import { formatDateTime } from "../lib/labels";
import { reasonLabel } from "../lib/i18n/reasons";
import { automationKeys, configKeys } from "../lib/queryKeys";
import { PHONE_MEDIA_QUERY, useMediaQuery } from "../lib/useMediaQuery";
import { useLogDialogStore } from "./logDialogStore";
import { isOneOf, ROUTE_DIALOG_STATE, ROUTE_LOG_VIEWS, ROUTE_QB_SEGMENTS, ROUTE_RECORD_LIST, ROUTE_RECORD_SEGMENTS, ROUTE_TOOLS, useRouteDialogs, type RouteLogView } from "./routeDialogs";
import { CATALOG_TABS, readRememberedBrowseTab, useCatalogTab } from "./catalogTabs";
import { MoreSheet } from "./MoreSheet";
import { BrandMark, BrandWordmark } from "./BrandMark";
import { badgeText, useLogFailureBadge } from "./logSeen";
import { loadAnimeConfigDialog } from "../features/anime-config/loadAnimeConfigDialog";
import { Popover } from "../ui/base";
import { t } from "../lib/i18n";

// App shell: one desktop top bar holds the brand, the four catalog section
// tabs (the only page navigation), a standing search entry, tool entries (each
// opens a sheet/dialog, never a page; no ↗ marker on top-bar icons), a status
// dot popover, settings and the account menu (which also holds the catalog
// blocklist). Phones get a thin brand bar plus a floating bottom tab bar
// (Subscribed / Discover / Downloads / More); tools, system status, settings
// and the account actions fold into the More sheet.
//
// No usable downloader: the status dot turns warning with "no downloader
// configured", the downloader entry and the phone Downloads tab stay and open
// an empty state with a go-to-settings action; `?tool=qbittorrent` always
// opens the manager.
//
// Topbar tools and the global log stay route-hosted (`/?tool=…`, `/?log=…`,
// see routeDialogs.ts): the URL is the open state, so they deep-link, survive
// refresh and close on browser Back.

// Copy for the four production automation kinds.
const AUTOMATION_KIND_LABEL = (): Record<AutomationKind, string> => ({
  group_discovery: t("nav.automationKind.group_discovery"),
  rss_refresh: t("nav.automationKind.rss_refresh"),
  metadata_match: t("nav.automationKind.metadata_match"),
  catalog_refresh: t("nav.automationKind.catalog_refresh"),
});

export function TopNav({
  username,
  onLogout,
}: {
  username: string | null;
  onLogout: () => void;
}) {
  const isPhone = useMediaQuery(PHONE_MEDIA_QUERY);
  const [accountMenuRequested, setAccountMenuRequested] = useState(false);
  const [accountMenuOpen, setAccountMenuOpen] = useState(false);
  const [moreSheetOpen, setMoreSheetOpen] = useState(false);

  const connectionsQuery = useQuery({
    queryKey: configKeys.connections,
    queryFn: listConnections,
  });
  const connections = connectionsQuery.data ?? [];
  // A usable downloader is an enabled qBittorrent connection. Without one the
  // status reads "no downloader configured"; the manager entry stays and shows an empty state.
  const hasUsableDownloader = connections.some((c) => c.kind === "qbittorrent" && c.enabled);

  const routeDialogs = useRouteDialogs();
  const toolParam = routeDialogs.get("tool");
  const logParam = routeDialogs.get("log");
  const globalSearchOpen = toolParam === "search";
  const qbManagerOpen = toolParam === "qbittorrent";
  // The file manager is hidden (see the note at the top of this file):
  // `?tool=files` is not in ROUTE_TOOLS and falls back as an unknown tool; its
  // `&path=` / `&file=` / `&op=` params are dropped too. On restore they become
  // valid only under tool=files again.
  const fileRouteParamPresent =
    routeDialogs.get("path") !== null || routeDialogs.get("file") !== null || routeDialogs.get("op") !== null;
  const logDialogOpen = isOneOf(ROUTE_LOG_VIEWS, logParam);
  // Execution records: `?record=list` is the list, any other value is one record's id.
  const recordParam = routeDialogs.get("record");
  const recordAnimeParam = routeDialogs.get("record_anime") ?? undefined;
  // `&record_segment=` / `&qb_segment=`: the two dialogs' current segments, written back to the link on switch.
  const recordSegmentParam = routeDialogs.get("record_segment");
  const qbSegmentParam = routeDialogs.get("qb_segment");
  const recordsOpen = Boolean(recordParam);
  const [lastRecord, setLastRecord] = useState<string>(ROUTE_RECORD_LIST);
  if (recordParam && recordParam !== lastRecord) setLastRecord(recordParam);
  // Keep the last view while the dialog plays its close transition.
  const [lastLogView, setLastLogView] = useState<RouteLogView>("records");
  if (logDialogOpen && logParam !== lastLogView) setLastLogView(logParam);
  const { dismiss: dismissRouteDialog } = routeDialogs;
  const invalidRouteDialog =
    (toolParam !== null && !isOneOf(ROUTE_TOOLS, toolParam)) ||
    (logParam !== null && !logDialogOpen) ||
    (recordParam === "" || (recordAnimeParam !== undefined && !recordParam)) ||
    (recordSegmentParam !== null && (!recordParam || !isOneOf(ROUTE_RECORD_SEGMENTS, recordSegmentParam))) ||
    (qbSegmentParam !== null && (!qbManagerOpen || !isOneOf(ROUTE_QB_SEGMENTS, qbSegmentParam))) ||
    fileRouteParamPresent;
  useEffect(() => {
    if (invalidRouteDialog) dismissRouteDialog();
  }, [dismissRouteDialog, invalidRouteDialog]);

  // Lazy chunks mount on first open and then stay mounted so closing animates.
  const [qbManagerRequested, setQBManagerRequested] = useState(false);
  const [globalSearchRequested, setGlobalSearchRequested] = useState(false);
  const [logDialogRequested, setLogDialogRequested] = useState(false);
  const [recordsRequested, setRecordsRequested] = useState(false);
  if (qbManagerOpen && !qbManagerRequested) setQBManagerRequested(true);
  if (globalSearchOpen && !globalSearchRequested) setGlobalSearchRequested(true);
  if (logDialogOpen && !logDialogRequested) setLogDialogRequested(true);
  if (recordsOpen && !recordsRequested) setRecordsRequested(true);
  const closeRouteDialog = (open: boolean) => {
    if (!open) routeDialogs.close();
  };
  // The search dialog reopens itself: its back affordance (a one-shot download opened from the
  // results returns to the search window) drives onOpenChange(true) back up, so both directions
  // follow the URL route.
  const setSearchDialog = (open: boolean) => {
    if (open) routeDialogs.open({ tool: "search" });
    else routeDialogs.close();
  };

  // The badge = new failures since the log was last seen (events + unrecovered diagnostic errors), kept only in this browser.
  const failedCount = useLogFailureBadge();

  const { tab, setTab } = useCatalogTab();
  const navigate = useNavigate();

  const openSearch = () => routeDialogs.open({ tool: "search" });
  const openQBManager = () => routeDialogs.open({ tool: "qbittorrent" });
  const openLog = () => routeDialogs.open({ log: "records" });
  const openRecords = () => routeDialogs.open({ record: ROUTE_RECORD_LIST });
  const openSettings = () => navigate("/settings", { state: ROUTE_DIALOG_STATE });
  const openDownloaderSettings = () => navigate(DOWNLOADER_SETTINGS, { state: ROUTE_DIALOG_STATE });

  // “/” opens the global search, mirroring the field's kbd hint. Never fires
  // from editable elements.
  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key !== "/" || event.metaKey || event.ctrlKey || event.altKey) return;
      const target = event.target;
      if (target instanceof HTMLElement && target.closest("input, textarea, [contenteditable], [role='dialog']")) return;
      event.preventDefault();
      openSearch();
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);


  const preloadConfig = () => void loadAnimeConfigDialog();

  return (
    <>
      <header className="top-nav">
        <div className="top-nav-inner">
          <NavLink to="/" className="top-nav-brand">
            <BrandMark size={24} />
            <BrandWordmark />
          </NavLink>

          {!isPhone && (
            <nav className="top-nav-tabs" aria-label={t("nav.mainNavAria")}>
              {CATALOG_TABS.map((item) => {
                const label = item.labelKey ? t(item.labelKey) : item.label!;
                return (
                  <button
                    key={item.key}
                    type="button"
                    aria-current={tab === item.key ? "page" : undefined}
                    className={cn("top-nav-tab", tab === item.key && "top-nav-tab-active")}
                    onClick={() => setTab(item.key)}
                  >
                    {label}
                  </button>
                );
              })}
            </nav>
          )}

          {isPhone ? (
            <button
              type="button"
              aria-label={t("nav.search")}
              title={t("nav.search")}
              onClick={openSearch}
              className="top-nav-icon-btn top-nav-phone-search"
            >
              <Search width={18} height={18} />
            </button>
          ) : (
            <button
              type="button"
              aria-label={t("nav.search")}
              onPointerEnter={preloadConfig}
              onFocus={preloadConfig}
              onClick={openSearch}
              className="top-nav-search"
            >
              <Search width={14} height={14} aria-hidden />
              <span className="top-nav-search-placeholder">{t("nav.search")}</span>
              <kbd className="top-nav-kbd" aria-hidden>
                /
              </kbd>
            </button>
          )}

          {!isPhone && (
            <div className="top-nav-actions">
              <StatusPopover connections={connections} loading={!connectionsQuery.isSuccess} />
              <span className="top-nav-divider" aria-hidden />
              <button
                type="button"
                aria-label={t("nav.qbManager")}
                title={t("nav.qbManager")}
                onClick={openQBManager}
                className="top-nav-icon-btn"
              >
                <MonitorPlay width={18} height={18} />
              </button>
              {/* The log and execution records keep their text when the desktop is wide enough, icons only when narrow. */}
              <button
                type="button"
                aria-label={t("nav.log")}
                title={t("nav.log")}
                onClick={openLog}
                className="top-nav-icon-btn top-nav-labeled-btn"
              >
                <ClipboardList width={18} height={18} />
                <span className="top-nav-btn-label" aria-hidden>
                  {t("nav.log")}
                </span>
                {failedCount > 0 && <span className="top-nav-badge">{badgeText(failedCount)}</span>}
              </button>
              {/* The file-manager entry is hidden; restore steps are in the note at the top of this file. */}
              <button
                type="button"
                aria-label={t("nav.records")}
                title={t("nav.records")}
                onClick={openRecords}
                className="top-nav-icon-btn top-nav-labeled-btn"
              >
                <History width={18} height={18} />
                <span className="top-nav-btn-label" aria-hidden>
                  {t("nav.records")}
                </span>
              </button>
              <button
                type="button"
                aria-label={t("nav.settings")}
                title={t("nav.settings")}
                onClick={openSettings}
                className="top-nav-icon-btn"
              >
                <Settings width={18} height={18} />
              </button>
              {accountMenuRequested ? (
                <Suspense
                  fallback={
                    <button type="button" aria-label={t("nav.account")} className="top-nav-icon-btn" title={t("nav.account")}>
                      <span className="account-avatar" aria-hidden>
                        {avatarInitial(username)}
                      </span>
                    </button>
                  }
                >
                  <AccountMenuEntry
                    username={username}
                    onLogout={onLogout}
                    open={accountMenuOpen}
                    onOpenChange={setAccountMenuOpen}
                  />
                </Suspense>
              ) : (
                <button
                  type="button"
                  aria-label={t("nav.account")}
                  onClick={() => {
                    setAccountMenuRequested(true);
                    setAccountMenuOpen(true);
                  }}
                  className="top-nav-icon-btn"
                  title={t("nav.account")}
                >
                  <span className="account-avatar" aria-hidden>
                    {avatarInitial(username)}
                  </span>
                </button>
              )}
            </div>
          )}
        </div>
      </header>

      {isPhone && (
        <PhoneTabBar
          tab={tab}
          onSelectTab={setTab}
          failedCount={failedCount}
          onOpenQBManager={openQBManager}
          onOpenMore={() => setMoreSheetOpen(true)}
        />
      )}

      {isPhone && (
        <MoreSheet
          open={moreSheetOpen}
          onClose={() => setMoreSheetOpen(false)}
          onLogout={onLogout}
          connections={connections}
          failedCount={failedCount}
          onOpenSearch={openSearch}
          onOpenRecords={openRecords}
          onOpenLog={openLog}
          onOpenSettings={openSettings}
          onOpenDownloaderSettings={openDownloaderSettings}
        />
      )}

      {globalSearchRequested && (
        <Suspense fallback={null}>
          <GlobalSearchDialog
            open={globalSearchOpen}
            onOpenChange={setSearchDialog}
            onOpenRecords={() => routeDialogs.open({ record: ROUTE_RECORD_LIST, record_segment: "one_shot" })}
          />
        </Suspense>
      )}

      {qbManagerRequested && (
        <Suspense fallback={null}>
          <QBManagerDialogEntry
            open={qbManagerOpen}
            onOpenChange={closeRouteDialog}
            onOpenSettings={openDownloaderSettings}
            segment={isOneOf(ROUTE_QB_SEGMENTS, qbSegmentParam) ? qbSegmentParam : undefined}
            onSegmentChange={(segment) => routeDialogs.update({ qb_segment: segment })}
          />
        </Suspense>
      )}

      {recordsRequested && (
        <Suspense fallback={<span className="sr-only">{t("nav.recordsLoading")}</span>}>
          <ExecutionRecordsDialogEntry
            open={recordsOpen}
            record={lastRecord}
            animeFilter={recordAnimeParam}
            segment={isOneOf(ROUTE_RECORD_SEGMENTS, recordSegmentParam) ? recordSegmentParam : undefined}
            onSegmentChange={(segment) => routeDialogs.update({ record_segment: segment })}
            onOpenRecord={(id) => routeDialogs.update({ record: id })}
            onShowList={() => routeDialogs.update({ record: ROUTE_RECORD_LIST })}
            onClearAnime={() => routeDialogs.update({ record_anime: undefined })}
            onOpenAnime={(id) => routeDialogs.open({ anime: id, tab: "rss" })}
            onClose={() => routeDialogs.close()}
          />
        </Suspense>
      )}

      {logDialogRequested && (
        <Suspense fallback={<span className="sr-only">{t("nav.logLoading")}</span>}>
          <LogDialogEntry
            open={logDialogOpen}
            view={lastLogView}
            onViewChange={(view) => {
              if (isOneOf(ROUTE_LOG_VIEWS, view)) routeDialogs.update({ log: view });
            }}
            onOpenChange={(open) => {
              if (open) return;
              useLogDialogStore.getState().closeDialog();
              routeDialogs.close();
            }}
            onOpenAnimeMetadata={(id) => {
              useLogDialogStore.getState().closeDialog();
              routeDialogs.open({ anime: id, tab: "metadata" });
            }}
          />
        </Suspense>
      )}
    </>
  );
}

function avatarInitial(username: string | null): string {
  const initial = username?.trim().charAt(0);
  return initial ? initial.toUpperCase() : "?";
}

// Phone bottom tab bar: Subscribed / Discover are the page tabs; Downloads
// opens the downloader sheet (always, with an empty state when no downloader
// is set up), More opens the tools/status/account sheet. No ↗ on tabs.
function PhoneTabBar({
  tab,
  onSelectTab,
  failedCount,
  onOpenQBManager,
  onOpenMore,
}: {
  tab: string;
  onSelectTab: (tab: "all" | "mikan" | "anibt" | "custom") => void;
  failedCount: number;
  onOpenQBManager: () => void;
  onOpenMore: () => void;
}) {
  const discoverActive = tab !== "all";
  return (
    <nav className="tabbar" aria-label={t("nav.tabbarAria")}>
      <button
        type="button"
        aria-current={tab === "all" ? "page" : undefined}
        className={cn("tabbar-item", tab === "all" && "tabbar-item-active")}
        onClick={() => onSelectTab("all")}
      >
        <Layers width={20} height={20} aria-hidden />
        <span>{t("grid.tabAll")}</span>
      </button>
      <button
        type="button"
        aria-current={discoverActive ? "page" : undefined}
        className={cn("tabbar-item", discoverActive && "tabbar-item-active")}
        onClick={() => onSelectTab(readRememberedBrowseTab())}
      >
        <Compass width={20} height={20} aria-hidden />
        <span>{t("nav.discover")}</span>
      </button>
      <button type="button" className="tabbar-item" onClick={onOpenQBManager}>
        <MonitorPlay width={20} height={20} aria-hidden />
        <span>{t("nav.download")}</span>
      </button>
      <button type="button" className="tabbar-item" onClick={onOpenMore}>
        <Menu width={20} height={20} aria-hidden />
        <span>{t("nav.more")}</span>
        {failedCount > 0 && <span className="tabbar-badge">{badgeText(failedCount)}</span>}
      </button>
    </nav>
  );
}

const CONNECTION_LABEL = (): Record<Connection["kind"], string> => ({
  qbittorrent: t("nav.connection.qbittorrent"),
  rss_http: t("nav.connection.rss_http"),
  tmdb: t("nav.connection.tmdb"),
  anibt: t("nav.connection.anibt"),
  mikan: t("nav.connection.mikan"),
  nyaa: t("nav.connection.nyaa"),
  bgm: t("nav.connection.bgm"),
});

// The status-dot popover (replacing the former status bar): every service's
// configuration state (not configured / disabled / enabled) and reachability.
// Reachability is never faked: opening the popover runs one real connection
// test per enabled connection (POST /config/connections/{id}/test), and a
// "test again" stays. A row click goes to the matching settings section. RSS
// access is only the custom RSS network setting, not a service, and does not
// enter the status.
const STATUS_KINDS: Connection["kind"][] = ["qbittorrent", "tmdb", "anibt", "mikan", "bgm"];

/** The downloader (the qBittorrent connection) is configured in the download section. */
const DOWNLOADER_SETTINGS = "/settings/download";

type ReachState = "testing" | "failed" | ConnectionTestResult;

/** Settings-section target of each status-popover row: AniBT/Mikan have their
 *  own site sections; qB is configured under the download section; the other
 *  connections land on the connections section — with a configured row it
 *  carries a ?connection= deep link that opens that connection's edit dialog
 *  directly (consumed in SettingsConnections), unconfigured lands on the
 *  section only. */
function settingsTargetFor(kind: Connection["kind"], connection: Connection | undefined): string {
  if (kind === "anibt" || kind === "mikan") return `/settings/${kind}`;
  if (kind === "qbittorrent") return DOWNLOADER_SETTINGS;
  if (connection) return `/settings/connections?connection=${encodeURIComponent(connection.id)}`;
  return "/settings/connections";
}

function StatusPopover({ connections: allConnections, loading }: { connections: Connection[]; loading: boolean }) {
  const connections = useMemo(() => allConnections.filter((c) => STATUS_KINDS.includes(c.kind)), [allConnections]);
  const [open, setOpen] = useState(false);
  const navigate = useNavigate();
  const schedules = useQuery({ queryKey: automationKeys.schedules, queryFn: listAutomationSchedules });
  const [tests, setTests] = useState<Record<string, ReachState>>({});

  const nextRun = (schedules.data ?? [])
    .filter((s): s is AutomationSchedule & { next_run_at: string } => s.enabled && Boolean(s.next_run_at))
    .sort((a, b) => a.next_run_at.localeCompare(b.next_run_at))[0];

  const runTests = useCallback(() => {
    const targets = connections.filter((connection) => connection.enabled);
    setTests(Object.fromEntries(targets.map((connection) => [connection.id, "testing" as const])));
    for (const connection of targets) {
      testConnection(connection.id)
        .then((result) => setTests((prev) => (connection.id in prev ? { ...prev, [connection.id]: result } : prev)))
        .catch(() => setTests((prev) => (connection.id in prev ? { ...prev, [connection.id]: "failed" as const } : prev)));
    }
  }, [connections]);

  useEffect(() => {
    if (open) runTests();
  }, [open, runTests]);

  const allEnabled = connections.length > 0 && connections.every((c) => c.enabled);
  const anyUnreachable = Object.values(tests).some((state) => state === "failed" || (typeof state === "object" && state.health !== "ready"));
  // Without a usable downloader the system can download nothing: the status says as much.
  const noDownloader = !loading && !connections.some((c) => c.kind === "qbittorrent" && c.enabled);
  const healthy = allEnabled && !anyUnreachable && !noDownloader;
  const go = (to: string) => {
    setOpen(false);
    navigate(to, { state: ROUTE_DIALOG_STATE });
  };

  return (
    <Popover
      open={open}
      onOpenChange={setOpen}
      align="end"
      trigger={
        <button type="button" aria-label={t("nav.statusAria")} title={t("nav.statusAria")} className="top-nav-status">
          <span aria-hidden className={cn("system-status-dot", healthy ? "system-status-dot-enabled" : "system-status-dot-warning")} />
          {loading ? t("nav.statusLoading") : noDownloader ? t("nav.statusNoDownloader") : healthy ? t("nav.statusOk") : t("nav.statusPartial")}
        </button>
      }
    >
      <div className="system-status-pop">
        <div className="system-status-section system-status-section-row">
          <span>{t("nav.statusConnections")}</span>
          <button type="button" className="system-status-retest" onClick={runTests}>
            {t("nav.statusRetest")}
          </button>
        </div>
        {STATUS_KINDS.map((kind) => {
          const connection = connections.find((candidate) => candidate.kind === kind);
          const credentialMissing = Boolean(connection && "credential_configured" in connection && connection.credential_configured === false);
          const configText = !connection
            ? t("nav.statusNotConfigured")
            : !connection.enabled
              ? t("common.disabled")
              : credentialMissing
                ? t("common.credentialUnset")
                : t("common.enabled");
          const reach = connection?.enabled ? tests[connection.id] : undefined;
          const reachReason = typeof reach === "object" && reach.health !== "ready" ? reasonLabel(reach.reason_code) : undefined;
          const reachText = !connection || !connection.enabled
            ? undefined
            : reach === undefined || reach === "testing"
              ? t("nav.statusTesting")
              : reach === "failed"
                ? t("nav.statusUnreachable")
                : reach.health === "ready"
                  ? t("nav.statusReachable")
                  : reachReason
                    ? t("nav.statusUnreachableReason", { reason: reachReason })
                    : t("nav.statusUnreachable");
          const downloaderMissing = kind === "qbittorrent" && noDownloader;
          const dotTone = downloaderMissing
            ? "system-status-dot-warning"
            : !connection || !connection.enabled
            ? "system-status-dot-disabled"
            : reach === undefined || reach === "testing"
              ? "system-status-dot-disabled"
              : reach !== "failed" && typeof reach === "object" && reach.health === "ready"
                ? "system-status-dot-enabled"
                : "system-status-dot-warning";
          const target = settingsTargetFor(kind, connection);

          return (
            <button key={kind} type="button" className="system-status-row" onClick={() => go(target)}>
              <span aria-hidden className={cn("system-status-dot", dotTone)} />
              <span className="system-status-row-label">{CONNECTION_LABEL()[kind]}</span>
              <span className="system-status-row-state">{configText}</span>
              {reachText && <span className="system-status-row-state">{reachText}</span>}
              {downloaderMissing && <span className="system-status-row-action">{t("nav.goSettings")}</span>}
            </button>
          );
        })}
        <div className="system-status-divider" role="separator" />
        <p className="system-status-section">{t("nav.statusNextRun")}</p>
        <button type="button" className="system-status-row" onClick={() => go("/settings/automation")}>
          <CalendarDays className="system-status-row-icon" aria-hidden />
          {nextRun ? (
            <span className="system-status-row-label">
              {AUTOMATION_KIND_LABEL()[nextRun.kind] ?? nextRun.kind} · {formatDateTime(nextRun.next_run_at)}
            </span>
          ) : (
            <span className="system-status-row-label">{t("nav.automationEmpty")}</span>
          )}
        </button>
      </div>
    </Popover>
  );
}
