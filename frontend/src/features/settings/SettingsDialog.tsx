import { useCallback, useEffect, useRef, useState } from "react";
import { NavLink, useBlocker, useLocation, useNavigate, useParams, type BlockerFunction } from "react-router-dom";
import { Bell, Bot, ChevronLeft, Clock, DatabaseBackup, EyeOff, FileQuestion, HardDrive, Plug, Rocket, SlidersHorizontal, UserRound, Users, Wand2 } from "lucide-react";
import { cn } from "@/lib/utils";
import { useDirtyConfirm } from "../../ui";
import { Button, Dialog } from "../../ui/base";
import { SettingsConnections } from "./SettingsConnections";
import { SettingsPolicy } from "./SettingsPolicy";
import { SettingsUnclassified } from "./SettingsRetention";
import { SettingsProviderDetail } from "./SettingsProviderDetail";
import { SavePartsProvider } from "../../lib/saveParts";
import { useSettingsSaveParts } from "./settingsSave";
import { SettingsSaveBar } from "./SettingsSaveBar";
import { SettingsRules } from "./SettingsRules";
import { SettingsDownload } from "./SettingsDownload";
import { SettingsAutomation } from "./SettingsAutomation";
import { SettingsNotifications } from "./SettingsNotifications";
import { SettingsUpdates } from "./SettingsUpdates";
import { SettingsBackup } from "./SettingsBackup";
import { SettingsAILab } from "./SettingsAILab";
import { SettingsAccount } from "./SettingsAccount";
import { HiddenAnimeSettings } from "../blacklist/HiddenAnimeSettings";
import { isRouteDialogEntry, ROUTE_DIALOG_STATE } from "../../shell/routeDialogs";
import { t, type CopyKey } from "../../lib/i18n";

// Global settings: a fullscreen dialog with a left section nav, and the only Settings entrance (a
//   fullscreen dialog/drawer and the single Settings entry, not a single-column long form). Sections
//   still live at /settings/:category, so links deep-link, a refresh stays on the same section, and
//   the browser back button closes; closing returns to the catalog. The F tier (ui/base Dialog): on
//   desktop a left section nav plus the content on the right; at width <=600px it becomes the two
//   level "section list -> section content", with the content page carrying a back control. Each
//   section loads/validates/saves independently, never sending a catch-all Global payload.

// Nav metadata: key/label/icon. The anibt and mikan keys share one parameterized component,
//   SettingsProviderDetail (the render branch passes the provider from active), so only nav
//   information is stored here, never a Component; rendering is decided by renderActive below.
const categories = [
  { key: "connections", labelKey: "settings.nav.connections", icon: Plug },
  { key: "download", labelKey: "settings.nav.download", icon: HardDrive },
  { key: "anibt", labelKey: "settings.nav.anibt", icon: Users },
  { key: "mikan", labelKey: "settings.nav.mikan", icon: Users },
  { key: "policy", labelKey: "settings.nav.policy", icon: SlidersHorizontal },
  // Hidden anime: saves immediately with no draft; no longer opened from the account menu/more/?tool=.
  { key: "hidden", labelKey: "settings.nav.hidden", icon: EyeOff },
  { key: "rules", labelKey: "settings.nav.rules", icon: Wand2 },
  // "Naming variables" (vars) is hidden this version: neither editing nor using custom variables is
//   surfaced in the UI; backend validation and rendering are unchanged and any saved ${vars.*}
//   template still renders. To restore, add back this entry (the Braces icon), its group key, the
//   render branch and the SettingsVars import together; it is an L2 dialog with immediate saving
//   and no dirty callback (the component, copy and component tests remain in the repo).
  // "Retention and cleanup" is hidden for now: the naming variables it depends on are not stable
//   enough to test, and the default is already "keep everything", so behavior is unchanged. To
//   restore, add back this entry, its group key, the render branch and SettingsRetention's dirty
//   callback together (the component and copy remain in the repo).
//   "Files with unrecognized episodes" depends only on the file-effect defaults and is untouched; it
//   keeps its own entry.
  { key: "unclassified", labelKey: "settings.nav.unclassified", icon: FileQuestion },
  { key: "account", labelKey: "settings.nav.account", icon: UserRound },
  { key: "automation", labelKey: "settings.nav.automation", icon: Clock },
  { key: "notifications", labelKey: "settings.nav.notifications", icon: Bell },
  { key: "updates", labelKey: "settings.nav.updates", icon: Rocket },
  { key: "backup", labelKey: "settings.nav.backup", icon: DatabaseBackup },
  { key: "ai", labelKey: "settings.nav.token", icon: Bot },
] as const satisfies ReadonlyArray<{ key: string; labelKey: CopyKey; icon: unknown }>;

type CategoryKey = (typeof categories)[number]["key"];

// The left nav groups by resource domain into category titles plus child rows: service
//   connections/downloads belong to "services"; default rules/unrecognized episodes/subscription
//   policy are defaults a new subscription copies and belong to "new-subscription defaults";
//   anibt/mikan (site-scoped subgroup rosters and name scoring) and hidden anime belong to "sites";
//   account/automation/notifications/version/backup/tokens belong to "system".
const categoryGroups: { groupKey: CopyKey; keys: CategoryKey[] }[] = [
  { groupKey: "settings.nav.groupService", keys: ["connections", "download"] },
  // On restore, naming variables goes back after "rules"; retention and cleanup rejoins this group as well.
  { groupKey: "settings.nav.groupDefaults", keys: ["rules", "unclassified", "policy"] },
  { groupKey: "settings.nav.groupSite", keys: ["anibt", "mikan", "hidden"] },
  { groupKey: "settings.nav.groupSystem", keys: ["account", "automation", "notifications", "updates", "backup", "ai"] },
];

export function SettingsDialog() {
  const navigate = useNavigate();
  const location = useLocation();
  const { category } = useParams<{ category: string }>();
  const active: CategoryKey = categories.some((c) => c.key === category)
    ? (category as CategoryKey)
    : "connections";
  const activeCategory = categories.find((c) => c.key === active)!;

  // The save model (styling-contract S4): a section either edits in place, registers its endpoints
//   as save parts and is committed by the dialog footer's single save, or lives in a card -> edit
//   dialog where the page itself has no draft and no footer save. Only the current section mounts;
//   the discard confirmation before leaving (switching sections / closing) looks only at this
//   unsaved-parts count.
  const saveParts = useSettingsSaveParts();
  const unsavedRef = useRef(0);
  const unsaved = saveParts.count();
  useEffect(() => {
    unsavedRef.current = unsaved;
  }, [unsaved]);
  const isDirty = useCallback(() => unsavedRef.current > 0, []);
  const inPlace = Object.keys(saveParts.parts).length > 0;

  // Internal section switches / closing already preventDefault first, then let the confirmed navigate
//   through exactly once; otherwise that navigate would re-trigger the blocker and re-open the
//   dialog. Browser back/forward is handled by the blocker directly: on cancel reset the original
//   navigation, on confirm proceed with it.
  const allowNextNavigationRef = useRef(false);
  const blockerPromptedRef = useRef(false);
  const shouldBlock: BlockerFunction = useCallback(
    ({ currentLocation, nextLocation }) => {
      if (allowNextNavigationRef.current) return false;
      const sameLocation =
        currentLocation.pathname === nextLocation.pathname &&
        currentLocation.search === nextLocation.search &&
        currentLocation.hash === nextLocation.hash;
      return !sameLocation && isDirty();
    },
    [isDirty],
  );
  const blocker = useBlocker(shouldBlock);

  useEffect(() => {
    // As soon as an internally allowed navigation has committed, clear the bypass flag so the next external navigation is unaffected.
    allowNextNavigationRef.current = false;
  }, [location]);

  // Dirty departures (switching sections / closing the dialog / ESC) confirm first, uniformly: a ref
//   carries the departure action, executed directly when clean and after confirmation when dirty.
  const pendingActionRef = useRef<(() => void) | null>(null);
  const executePendingAction = useCallback(() => {
    const action = pendingActionRef.current;
    pendingActionRef.current = null;
    action?.();
  }, []);
  const { requestClose, confirmDialog, confirmOpen } = useDirtyConfirm({
    dirty: isDirty,
    onClose: executePendingAction,
    description: t("settings.nav.leaveSectionConfirm"),
    confirmLabel: t("common.discardChanges"),
    danger: true,
  });

  useEffect(() => {
    if (!blocker || blocker.state !== "blocked") {
      blockerPromptedRef.current = false;
      return;
    }

    if (!confirmOpen && blockerPromptedRef.current) {
      // ConfirmDialog's cancel closes confirmOpen; the original history navigation is still blocked and must be reset.
      blocker.reset();
      blockerPromptedRef.current = false;
      pendingActionRef.current = null;
      return;
    }

    if (confirmOpen || blockerPromptedRef.current) return;
    blockerPromptedRef.current = true;
    pendingActionRef.current = () => blocker.proceed();
    requestClose();
  }, [blocker, confirmOpen, requestClose]);

  const requestSwitch = useCallback(
    (key: CategoryKey) => {
      if (key === active) return;
      pendingActionRef.current = () => {
        allowNextNavigationRef.current = true;
        // Switching sections replaces the current entry (keeping the opened-from-base marker) so closing returns to the base in one step.
        navigate(`/settings/${key}`, { replace: true, state: isRouteDialogEntry(location.state) ? ROUTE_DIALOG_STATE : undefined });
      };
      requestClose();
    },
    [active, location.state, navigate, requestClose],
  );

  const handleOpenChange = useCallback(
    (open: boolean) => {
      if (open) return;
      pendingActionRef.current = () => {
        allowNextNavigationRef.current = true;
        // Matching the other top-level dialogs: opened from the base, back returns to the base's history entry; entered by deep link, the entry is replaced with the base address.
        if (isRouteDialogEntry(location.state)) navigate(-1);
        else navigate("/", { replace: true });
      };
      requestClose();
    },
    [location.state, navigate, requestClose],
  );

  // The phone two-level nav's current layer: opening a section directly (deep link / refresh /
//   the catalog's status strip) lands on content, while entering from the header's settings entry
//   (the /settings redirect, state.pane="nav") shows the section list first. Re-decided per
//   navigation from the target address (location.key changes); both columns are visible on desktop,
//   where pane matters only at <=600px.
  const initialPane = (location.state as { pane?: string } | null)?.pane === "nav" ? "nav" : "main";
  const [paneState, setPaneState] = useState<{ key: string; pane: "nav" | "main" }>({ key: location.key, pane: initialPane });
  if (paneState.key !== location.key) setPaneState({ key: location.key, pane: initialPane });
  const pane = paneState.pane;
  const setPane = (next: "nav" | "main") => setPaneState((current) => ({ ...current, pane: next }));

  return (
    <>
      <Dialog
        open
        tier="F"
        focusTitle
        onRequestClose={() => handleOpenChange(false)}
        title={t("settings.nav.title")}
        footer={inPlace ? <SettingsSaveBar parts={saveParts} /> : undefined}
        bodyLayout="fill"
      >
        {/* The nav and content scroll independently: the nav column never scrolls away with the content
            and scrolls on its own once items overflow. body uses the fill layout, each column being
            its own scroll area, with the Dialog body's 24px / phone 16px padding preserved. */}
        <div className="tw:grid tw:min-h-0 tw:min-w-0 tw:flex-1 tw:grid-cols-[11rem_minmax(0,1fr)] tw:gap-6 tw:pl-6 tw:phone:grid-cols-1 tw:phone:gap-0 tw:phone:pl-0">
          <div className={cn("tw:min-h-0 tw:overflow-y-auto tw:py-4 tw:phone:px-4", pane === "main" && "tw:phone:hidden")}>
          <nav
            aria-label={t("settings.nav.label")}
            className="tw:flex tw:flex-col tw:gap-0.5 tw:rounded-xl tw:border tw:border-border tw:bg-card/60 tw:p-1.5"
          >
            {categoryGroups.map((group, groupIndex) => (
              <div
                key={group.groupKey}
                role="group"
                aria-labelledby={`settings-nav-group-${groupIndex}`}
                className={cn(
                  "tw:flex tw:flex-col tw:gap-0.5",
                  groupIndex > 0 && "tw:mt-1 tw:border-t tw:border-border/60 tw:pt-2",
                )}
              >
                <p id={`settings-nav-group-${groupIndex}`} className="tw:m-0 tw:px-3 tw:pt-1 tw:pb-0.5 tw:text-xs tw:font-medium tw:tracking-wide tw:text-muted-foreground tw:uppercase">
                  {t(group.groupKey)}
                </p>
                {group.keys.map((key) => {
                  const item = categories.find((c) => c.key === key)!;
                  return (
                    <NavLink
                      key={item.key}
                      to={`/settings/${item.key}`}
                      onClick={(event) => {
                        // While dirty, intercept the default navigation and navigate via requestSwitch only after confirmation.
                        event.preventDefault();
                        setPane("main");
                        requestSwitch(item.key);
                      }}
                      className={({ isActive }) =>
                        cn(
                          "tw:flex tw:h-9 tw:flex-none tw:items-center tw:phone:h-11 tw:gap-2.5 tw:rounded-lg tw:px-3 tw:text-sm tw:font-medium tw:no-underline tw:transition-colors tw:focus-visible:outline-2 tw:focus-visible:outline-ring",
                          isActive
                            ? "tw:bg-accent tw:text-accent-foreground"
                            : "tw:text-muted-foreground tw:hover:bg-muted tw:hover:text-foreground",
                        )
                      }
                    >
                      <item.icon className="tw:size-4 tw:flex-none" aria-hidden="true" />
                      {t(item.labelKey)}
                    </NavLink>
                  );
                })}
              </div>
            ))}
          </nav>
          </div>

          {/* key=active: switching sections resets scroll and internal state, never carrying the previous section's draft over;
              草稿丢弃前的确认由上面的 requestSwitch 拦截保证，这里不再静默。 */}
          <div
            key={active}
            data-testid="settings-content"
            className={cn("tw:min-h-0 tw:min-w-0 tw:overflow-y-auto tw:py-4 tw:pr-6 tw:phone:px-4", pane === "nav" && "tw:phone:hidden")}
          >
            <div className="tw:mb-2 tw:hidden tw:phone:block">
              <Button variant="ghost" size="sm" className="tw:-ml-2" onClick={() => setPane("nav")}>
                <ChevronLeft className="tw:size-4" aria-hidden="true" />
                {t("settings.nav.backToList")}
              </Button>
            </div>
            {/* The section title exists only for assistive tech: on desktop the left nav's selected state expresses it,
                手机上由返回按钮与分区内容本身表达，再画一遍就是重复。 */}
            <h2 className="sr-only">{t(activeCategory.labelKey)}</h2>
            <SavePartsProvider value={saveParts.registrar}>{renderActive(active)}</SavePartsProvider>
          </div>
        </div>
      </Dialog>
      {confirmDialog}
    </>
  );
}

// Renders the current section. The anibt/mikan keys share SettingsProviderDetail, with the provider
//   passed from active; in-place-edited sections register their save parts via SavePartsProvider.
function renderActive(active: CategoryKey) {
  switch (active) {
    case "policy":
      return <SettingsPolicy />;
    case "anibt":
      return <SettingsProviderDetail key="anibt" provider="anibt" />;
    case "mikan":
      return <SettingsProviderDetail key="mikan" provider="mikan" />;
    case "hidden":
      return <HiddenAnimeSettings />;
    case "connections":
      return <SettingsConnections />;
    case "download":
      return <SettingsDownload />;
    case "rules":
      return <SettingsRules />;
    // While naming variables is hidden there is no "vars" branch: the /settings/vars deep link falls back to service connections.
    case "unclassified":
      return <SettingsUnclassified />;
    case "automation":
      return <SettingsAutomation />;
    case "account":
      return <SettingsAccount />;
    case "notifications":
      return <SettingsNotifications />;
    case "updates":
      return <SettingsUpdates />;
    case "backup":
      return <SettingsBackup />;
    case "ai":
      return <SettingsAILab />;
    default:
      return <SettingsConnections />;
  }
}
