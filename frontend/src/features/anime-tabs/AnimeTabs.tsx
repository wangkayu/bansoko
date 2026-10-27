import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { toast } from "../../ui/toast";
import { SavePartsProvider, useSaveParts } from "../../lib/saveParts";
import { draftTabChangeCounts } from "../../lib/draft";
import { SaveButton, SaveStatus, UnsavedDot } from "../../ui/SaveFooter";
import type { AnimeMetadataPreview, RulesSections } from "../../lib/api/client";
import type { AnimeTabHost } from "./host";
import { MetadataTab, type MetadataMatchRequest } from "./MetadataTab";
import { SubgroupsTab } from "./SubgroupsTab";
import { RulesTab } from "./RulesTab";
import { RssTab } from "./RssTab";
import { TabsList, TabsPanel, TabsRoot } from "../../ui/base";
import { t } from "../../lib/i18n";
import { AnimeTabNavigationContext } from "./tabNavigation";

// The sole implementation and shell of an anime's four tabs (metadata/subgroups/rules/RSS):
// shared before subscribing (a draft, in-memory) and after (an owner, CAS saves), with the mode
// differences confined to the host discriminated union. Forking a second tab implementation is
// forbidden (the settled ruling: they are essentially one tab).
//
// keepMounted keeps tabs alive: switching tabs never unloads an editing surface, so an owner's
// unsaved edits survive; a draft's editing state already lives in the shared draft object, so
// keeping mounted has no side effect there.
//
// The save model (styling-contract S4): in owner mode each tab's edits are a draft and the
// footer has a single save submitting the current tab's every change; the multiple endpoints
// behind one tab submit in SAVE_ORDER, stopping at the first failure. A tab with unsaved changes
// is marked beside its name. In draft mode the caller's footer saves the whole draft, and this
// shell only marks tabs against savedDraft.

/** How long unsaved metadata must stay unchanged before the RSS preview re-reads (no per-keystroke re-reads while typing a title). */
const METADATA_PREVIEW_DEBOUNCE_MS = 300;

const SAVE_ORDER = ["metadata", "subgroups", "rules", "parser", "fileEffect", "replacement", "alignment"] as const;
const TAB_PARTS: Record<string, readonly string[]> = {
  metadata: ["metadata"],
  subgroups: ["subgroups"],
  rules: ["rules", "parser", "fileEffect", "replacement", "alignment"],
};

export function AnimeTabs({
  host,
  initialTab = "metadata",
  onTabChange,
  onDirtyChange,
  banner,
  matchRequest,
  footerStart,
  footerEnd,
  saveIsPrimary = true,
}: {
  host: AnimeTabHost;
  /** The tab selected at open (e.g. a card's "subscription policy" landing on subgroups, subscribed landing on rss). */
  initialTab?: string;
  /** Reports tab changes (the route-carried config dialog writes it into `?tab=`). */
  onTabChange?: (tab: string) => void;
  /** Owner mode: raised whenever any owner tab has unsaved edits (for the discard confirmation before closing). */
  onDirtyChange?: (dirty: boolean) => void;
  /** A banner rendered at the top of the scroll area (the draft's error/draft-problem blocks). */
  banner?: ReactNode;
  /**
   * 打开元数据匹配（配置弹窗阻塞警告行的修复动作）：每次 seq 变化切到元数据 tab 并打开
   * 该站点的匹配弹窗，走与元数据 tab 按钮完全相同的流程。
   */
  matchRequest?: MetadataMatchRequest;
  /** Owner footer: actions to the save button's left (execution records, close). */
  footerStart?: ReactNode;
  /** Owner footer: actions to the save button's right (start the subscription). */
  footerEnd?: ReactNode;
  /** When the footer already has another primary action (start the subscription), save demotes to secondary. */
  saveIsPrimary?: boolean;
}) {
  const [tab, setTab] = useState(initialTab);
  useEffect(() => {
    if (matchRequest) setTab("metadata");
  }, [matchRequest]);
  // Rules preview (owner): triggered by the rules tab, the RSS area switches to preview mode.
  // The preview's input is the **unsaved rules sections**; the actual owner_draft read model is
  // one side-effect-free read by the RSS tab (`readAnimeRSSListView` owner_draft), with no second
  // rendering surface.
  const [rulesPreviewSections, setRulesPreviewSections] = useState<RulesSections>();
  const saveParts = useSaveParts(SAVE_ORDER);

  const exitRulesPreview = useCallback(() => setRulesPreviewSections(undefined), []);
  // Owner mode's unsaved metadata: the metadata tab reports the complete value and the RSS tab
  // previews against it (owner_draft) until saved or discarded. The save model is unchanged; this
  // only hands the value over after debouncing.
  const [metadataPreviewLive, setMetadataPreviewLive] = useState<AnimeMetadataPreview>();
  const [metadataPreview, setMetadataPreview] = useState<AnimeMetadataPreview>();
  useEffect(() => {
    // Clearing (saved or discarded) takes effect immediately; with a value, it is handed over once typing pauses.
    if (!metadataPreviewLive) {
      setMetadataPreview(undefined);
      return;
    }
    const timer = setTimeout(() => setMetadataPreview(metadataPreviewLive), METADATA_PREVIEW_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [metadataPreviewLive]);

  // Each tab's unsaved-change count: an owner's comes from the registered save parts, a draft's from the diff against savedDraft.
  const draftCounts = useMemo(
    () => (host.mode === "draft" && host.savedDraft ? draftTabChangeCounts(host.savedDraft, host.draft) : undefined),
    [host],
  );
  const tabCount = (key: string): number => {
    if (host.mode === "draft") return draftCounts?.[key as keyof typeof draftCounts] ?? 0;
    const keys = TAB_PARTS[key];
    return keys ? saveParts.count(keys) : 0;
  };
  const handleRulesContentChange = useCallback(() => {
    if (host.mode === "owner") setRulesPreviewSections(undefined);
  }, [host.mode]);
  useEffect(() => {
    onTabChange?.(tab);
  }, [tab, onTabChange]);
  const anyDirty = host.mode === "owner" && saveParts.count() > 0;
  useEffect(() => {
    onDirtyChange?.(anyDirty);
  }, [anyDirty, onDirtyChange]);

  // Leaving the RSS tab exits preview mode (a preview holds only while viewed).
  const handleTabChange = useCallback((next: string) => {
    if (next !== "rss") setRulesPreviewSections(undefined);
    setTab(next);
  }, []);

  function handleRulesPreview(sections: RulesSections) {
    if (host.mode !== "owner") return;
    setRulesPreviewSections(sections);
    setTab("rss");
  }

  const tabLabel = (key: string, label: string) =>
    tabCount(key) > 0 ? (
      <span className="tw:inline-flex tw:items-center tw:gap-1.5">
        {label}
        <UnsavedDot />
      </span>
    ) : (
      label
    );
  const tabItems = [
    { key: "metadata", label: tabLabel("metadata", t("tabs.names.metadata")) },
    { key: "subgroups", label: tabLabel("subgroups", t("tabs.names.subgroups")) },
    { key: "rules", label: tabLabel("rules", t("tabs.names.rules")) },
    { key: "rss", label: tabLabel("rss", host.mode === "draft" ? t("tabs.names.rssPreview") : t("tabs.names.rss")) },
  ];
  // The single decision point of read-only gating: in owner mode the capability decides whether
  // this workspace is read-only (the retired lifecycle). A draft has no lifecycle owner and is
  // always editable. Every tab below consumes only this one boolean and no longer derives
  // lifecycleState on its own.
  const readOnly = host.mode === "owner" && host.capability.readOnly;

  // The footer's single save: submits only the current tab's changes (the RSS tab has nothing saveable).
  const activeParts = TAB_PARTS[tab];
  const activeCount = activeParts ? saveParts.count(activeParts) : 0;
  async function saveActiveTab() {
    if (!activeParts) return;
    const run = await saveParts.save(activeParts);
    if (run.ok) toast.success(run.messages[0] ?? t("act.saved"));
  }
  const showSave = host.mode === "owner" && !readOnly && activeParts !== undefined;
  const footer =
    host.mode === "owner" && (footerStart || footerEnd || showSave) ? (
      <div className="anime-config-dialog-footer anime-config-dialog-footer-owner">
        {showSave ? (
          <SaveStatus count={activeCount} failed={saveParts.failed} className="tw:mr-auto" />
        ) : null}
        {footerStart}
        {showSave ? (
          <SaveButton count={activeCount} busy={saveParts.busy} onSave={() => void saveActiveTab()} primary={saveIsPrimary} />
        ) : null}
        {footerEnd}
      </div>
    ) : null;

  return (
    <AnimeTabNavigationContext.Provider value={handleTabChange}>
     <SavePartsProvider value={saveParts.registrar}>
      <TabsRoot value={tab} onChange={handleTabChange} className="tw:flex-1">
        <TabsList id="anime-config-tabs" items={tabItems} className="tw:px-5" />
        <div className="anime-tabs-body">
          {banner}
          <TabsPanel value="metadata" keepMounted data-state={tab === "metadata" ? "active" : "inactive"}>
            <MetadataTab
              host={host}
              readOnly={readOnly}
              matchRequest={matchRequest}
              onPreviewChange={host.mode === "owner" ? setMetadataPreviewLive : undefined}
            />
          </TabsPanel>
          <TabsPanel value="subgroups" keepMounted data-state={tab === "subgroups" ? "active" : "inactive"}>
            <SubgroupsTab host={host} readOnly={readOnly} />
          </TabsPanel>
          <TabsPanel value="rules" keepMounted data-state={tab === "rules" ? "active" : "inactive"}>
            <RulesTab
              host={host}
              readOnly={readOnly}
              onContentChange={handleRulesContentChange}
              onSaved={exitRulesPreview}
              active={tab === "rules"}
              onPreview={host.mode === "owner" && host.capability.can("rules.preview") ? handleRulesPreview : undefined}
            />
          </TabsPanel>
          <TabsPanel value="rss" keepMounted data-state={tab === "rss" ? "active" : "inactive"}>
            <RssTab
              host={host}
              rulesPreviewSections={rulesPreviewSections}
              metadataPreview={host.mode === "owner" ? metadataPreview : undefined}
              onExitRulesPreview={exitRulesPreview}
              active={tab === "rss"}
            />
          </TabsPanel>
        </div>
      </TabsRoot>
      {footer}
     </SavePartsProvider>
    </AnimeTabNavigationContext.Provider>
  );
}
