import { useEffect, useMemo, useRef, useState } from "react";
import { RefreshCw, ScanSearch } from "lucide-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "../../ui/toast";
import {
  ApiError,
  fetchCustomSnapshot,
  fetchOwnerCustomSnapshot,
  newUUID,
  readAnimeRSSListView,
  setProviderGroupOffset,
  retryRelease,
  retryRSSSourceRuntime,
  toUserMessage,
  type AnimeMetadataPreview,
  type Release,
  type PreviewFeedSnapshotRef,
  type RSSListGroup,
  type RSSListView,
  type RulesSections,
} from "../../lib/api/client";
import { rssListKeys } from "../../lib/queryKeys";
import { Chip, StateBlock, Button, Field, Input } from "../../ui";
import { Checkbox, Dialog, HelpTip } from "../../ui/base";
import { DraftSubgroupRefresh } from "../anime-config/DraftSubgroupRefresh";
import { useRecordLink } from "../../shell/recordLinkContext";
import { refreshEligibleRSSSources, rssActions } from "../workspace/rssActions";
import { offsetConversionLine } from "../rss-list/offsetConversion";
import { ownerViewModel, type OffsetDrawerSource, type RetryTarget } from "../rss-list/ownerModel";
import { RssListViewPanel } from "../rss-list/RssListViewPanel";
import { RssWorkspace } from "../rss-list/RssWorkspace";
import { ReleaseTorrentStructureDialog } from "../rss-list/ReleaseTorrentStructureDialog";
import type { CustomSnapshotSource } from "../rss-list/useRssListWorkspace";
import { FeedSiteNote } from "../catalog/FeedSiteNote";
import type { AnimeTabHost, DraftTabHost, OwnerTabHost } from "./host";
import { t } from "../../lib/i18n";
import { reasonLabel } from "../../lib/i18n/reasons";
import { showReleaseRetryResult } from "./releaseRetryResult";
import { AnimeFilesDialog } from "../anime-files/AnimeFilesDialog";

// The RSS tab's sole implementation: before subscribing it is the draft's preview face
// (side-effect free, writing nothing); after subscribing it is the owner's result/action face
// (refresh/retry/release). Both render the server-composed RSSListView read model: the owner side
// in `owner` mode and the rules preview the same read model's `owner_draft` mode, so there is no
// second rendering surface.
//
// In preview mode (owner_draft) every action is disabled with a reason; leaving the RSS tab or
// saving the rules exits the rules preview. When the metadata tab has unsaved changes, the read
// is likewise owner_draft (carrying the complete unsaved metadata) until saved or discarded; it
// needs no save first and never blocks the preview.

/** The maximum Releases returned per subgroup in owner mode (the server's cap of 500). */
const OWNER_ITEMS_PER_GROUP = 100;

/** The note shown when preview mode disables every action. */
const previewDisabledReason = () => t("rss.previewDisabled");

export function RssTab({
  host,
  rulesPreviewSections,
  metadataPreview,
  onExitRulesPreview,
  onViewRead,
  active,
}: {
  host: AnimeTabHost;
  /** Owner mode: unsaved rules sections; when present the RSS area displays the owner_draft read model. */
  rulesPreviewSections?: RulesSections;
  /** Owner mode: the metadata tab's complete unsaved metadata; when present the display likewise uses the owner_draft read model. */
  metadataPreview?: AnimeMetadataPreview;
  onExitRulesPreview: () => void;
  /** Reported after each successful read of the server read model (the subgroups tab's subscription-facts source). */
  onViewRead?: (view: RSSListView) => void;
  /** Whether this tab is the active one (all tabs stay mounted; mounted is not the same as viewed). */
  active?: boolean;
}) {
  if (host.mode === "draft") {
    return <DraftRssTab host={host} onViewRead={onViewRead} active={active} />;
  }
  const bench = pendingBenchScope(host);
  if (bench) {
    return (
      <PendingRssBench
        host={host}
        provider={bench.provider}
        providerEntryRef={bench.providerEntryRef}
        rulesPreviewSections={rulesPreviewSections}
        metadataPreview={metadataPreview}
        onExitRulesPreview={onExitRulesPreview}
        onViewRead={onViewRead}
        active={active}
      />
    );
  }
  return (
    <OwnerRssTab
      host={host}
      rulesPreviewSections={rulesPreviewSections}
      metadataPreview={metadataPreview}
      onExitRulesPreview={onExitRulesPreview}
      onViewRead={onViewRead}
    />
  );
}

// --- Draft: the unified RSS workspace read model (side-effect free) ----------------------

function DraftRssTab({
  host,
  onViewRead,
  active,
}: {
  host: DraftTabHost;
  onViewRead?: (view: RSSListView) => void;
  active?: boolean;
}) {
  // The pre-subscription RSS tab is a test bench: it mirrors the subscription semantics
  // (subgroup selection, filtering, naming output) and has no download entry. One-shot downloads
  // start only from a catalog/search candidate card's "browse seeds". Provider and custom are the
  // same workspace: custom only fetches its source snapshot through the saved preset's source (the
  // same fetch path as a not-yet-started subscription's bench) and has no subgroup discovery.
  const customSnapshot = host.customSnapshot;
  const customSource = useMemo<CustomSnapshotSource | undefined>(() => {
    if (host.draft.origin !== "custom") return undefined;
    if (!customSnapshot) return {};
    return {
      fetch: () => fetchCustomSnapshot(customSnapshot),
      scope: { candidate_id: customSnapshot.candidate_id, revision: customSnapshot.revision },
    };
  }, [host.draft.origin, customSnapshot]);
  const bench = host.subgroupDiscovery
    ? { provider: host.subgroupDiscovery.provider, providerEntryRef: host.subgroupDiscovery.providerEntryRef }
    : customSource
      ? { provider: "custom" as const, providerEntryRef: undefined }
      : undefined;
  return (
    <>
      <div className="rss-draft-toolbar">
        <p className="rss-draft-hint text-caption">
          {t("rss.draftHint")}
        </p>
        {customFeedLocators(host.draft.rss_sources).map((locator, index) => (
          <FeedSiteNote key={index} url={locator} />
        ))}
      </div>
      {/* The pre-subscription's only RSS workspace: subgroups → seeds → torrent file tree.
          The server composes exact snapshot facts with the parser/evaluator/renderer evaluation
          into one read model, and the frontend no longer renders a "seed list" and a "preview
          result" side by side. */}
      {customSource && !customSource.fetch && (
        <p role="status" className="rss-draft-hint text-caption">{t("rss.customBenchSaveFirst")}</p>
      )}
      {host.subgroupDiscovery && (
        <DraftSubgroupRefresh
          provider={host.subgroupDiscovery.provider}
          providerEntryRef={host.subgroupDiscovery.providerEntryRef}
          onDiscovered={host.onDiscovered}
          showHint={false}
        />
      )}
      {bench && (
        <RssWorkspace
          key={`${bench.provider}:${bench.providerEntryRef ?? ""}`}
          provider={bench.provider}
          providerEntryRef={bench.providerEntryRef}
          customSource={customSource}
          draft={host.draft}
          enabled={host.previewable}
          feedSnapshots={host.feedSnapshots}
          onFeedSnapshotsChange={host.onFeedSnapshotsChange}
          observation={host.observation}
          autoLoad={active}
          onDraftChange={host.onDraftChange}
          onViewRead={onViewRead}
        />
      )}
    </>
  );
}

// --- Not-yet-started subscription: the test bench (the same workspace as the draft bench; no downloads, no refresh jobs) ---

/**
 * A not-yet-started subscription uses the test bench, with no provider/custom split of sources: a
 * custom source snapshot is fetched by the subscription's own
 * 来源抓取（服务端从订阅读 URL）。已结束的实例保持 owner 面。
 */
function pendingBenchScope(host: OwnerTabHost): { provider: "anibt" | "mikan" | "custom"; providerEntryRef?: string } | undefined {
  const subscription = host.workspace.subscription;
  if (subscription.state !== "pending" || host.capability.readOnly) return undefined;
  if (subscription.origin === "custom") return { provider: "custom" };
  if (subscription.origin !== "anibt" && subscription.origin !== "mikan") return undefined;
  if (!subscription.origin_entry_ref) return undefined;
  return { provider: subscription.origin, providerEntryRef: subscription.origin_entry_ref };
}

function PendingRssBench({
  host,
  provider,
  providerEntryRef,
  rulesPreviewSections,
  metadataPreview,
  onExitRulesPreview,
  onViewRead,
  active,
}: {
  host: OwnerTabHost;
  provider: "anibt" | "mikan" | "custom";
  providerEntryRef?: string;
  rulesPreviewSections?: RulesSections;
  metadataPreview?: AnimeMetadataPreview;
  onExitRulesPreview: () => void;
  onViewRead?: (view: RSSListView) => void;
  active?: boolean;
}) {
  const { animeID, workspace } = host;
  // The server composes the bench read model from the instance's current configuration; unsaved
  // rule and metadata changes in the dialog are submitted with the read — the same semantics as
  // the draft bench previewing an unsaved draft. Configuration identity = saved versions + unsaved
  // changes.
  const identity = JSON.stringify([
    workspace.subscription.version,
    workspace.subgroups.version,
    workspace.rules.version,
    workspace.metadata?.version ?? null,
    rulesPreviewSections ?? null,
    metadataPreview ?? null,
  ]);
  const ownerBench = useMemo(
    () => ({ managedAnimeID: animeID, rulesPreview: rulesPreviewSections, metadataPreview, identity }),
    [animeID, identity, rulesPreviewSections, metadataPreview],
  );
  const [feedSnapshots, setFeedSnapshots] = useState<PreviewFeedSnapshotRef[]>([]);
  // A custom source: the same fetch descriptor as the draft bench, with the subscription's own saved source as the fetch subject.
  const customSource = useMemo<CustomSnapshotSource | undefined>(
    () => (provider === "custom" ? { fetch: () => fetchOwnerCustomSnapshot(animeID) } : undefined),
    [provider, animeID],
  );
  // The link is found by provenance; a group without one is saved at version 0, which creates it.
  // The workspace is re-read after the save so the next save carries the new version.
  async function saveGroupOffset(group: RSSListGroup, offset: number) {
    if (provider === "custom") return;
    const ref = group.scope.provider_group_ref;
    const link = workspace.rss_sources.find(
      (source) => source.provider_provenance?.provider === provider && source.provider_provenance.provider_group_ref === ref,
    );
    await setProviderGroupOffset(animeID, provider, ref, { expected_version: link?.version ?? 0, episode_offset: offset });
    host.onChanged();
  }
  return (
    <div className="rss-tab-stack">
      {provider === "custom" && customFeedLocators(workspace.rss_sources).map((locator, index) => (
        <FeedSiteNote key={index} url={locator} />
      ))}
      {/* The not-started note is one line at the dialog's top; only the unsaved-changes preview notice stays here. */}
      {(rulesPreviewSections || metadataPreview) && (
        <div className="rss-notice">
          <p role="status" className="rss-notice-text text-caption">
            {metadataPreview ? t("rss.pendingBenchPreviewEdits") : t("rss.pendingBenchPreview")}
          </p>
          {rulesPreviewSections && (
            <Button size="sm" variant="ghost" onClick={onExitRulesPreview}>
              {t("rss.exitPreview")}
            </Button>
          )}
        </div>
      )}
      <RssWorkspace
        provider={provider}
        providerEntryRef={providerEntryRef}
        ownerBench={ownerBench}
        customSource={customSource}
        enabled
        feedSnapshots={feedSnapshots}
        onFeedSnapshotsChange={setFeedSnapshots}
        autoLoad={active ?? true}
        onViewRead={onViewRead}
        onSaveGroupOffset={provider === "custom" ? undefined : saveGroupOffset}
        help={t("rss.pendingBenchHelp")}
      />
    </div>
  );
}

// --- Owner: the result/action face -------------------------------------------------------------

function OwnerRssTab({
  host,
  rulesPreviewSections,
  metadataPreview,
  onExitRulesPreview,
  onViewRead,
}: {
  host: OwnerTabHost;
  rulesPreviewSections?: RulesSections;
  metadataPreview?: AnimeMetadataPreview;
  onExitRulesPreview: () => void;
  onViewRead?: (view: RSSListView) => void;
}) {
  const { animeID, workspace, capability, onChanged } = host;
  const queryClient = useQueryClient();
  const rulesPreviewMode = rulesPreviewSections !== undefined;
  const previewMode = rulesPreviewMode || metadataPreview !== undefined;
  const [busyKey, setBusyKey] = useState<string>();
  const [refreshBusy, setRefreshBusy] = useState(false);
  const [offsetDrawerSource, setOffsetDrawerSource] = useState<OffsetDrawerSource>();
  const [offsetDraft, setOffsetDraft] = useState("");
  const [offsetSaving, setOffsetSaving] = useState(false);
  const [offsetError, setOffsetError] = useState<string>();
  // Scanning the library: opens the anime's "files" dialog where the scan starts and its results show (one scan also refreshes the RSS rows' file states).
  const [filesOpen, setFilesOpen] = useState(false);
  // One release's retained torrent payload structure: read-only, opened from that release row.
  const [structureRelease, setStructureRelease] = useState<Release>();
  // A seed's processing is the execution-record detail (read-only, ended subscriptions included): route-carried, the back key returns to this anime.
  const openRecord = useRecordLink();
  const [showFiltered, setShowFiltered] = useState(false);

  const groupScores = useMemo(() => {
    const map = new Map<string, number>();
    for (const entry of workspace.subgroups.effective_selection) {
      map.set(`${entry.provider}:${entry.group_ref}`, entry.effective_score);
    }
    return map;
  }, [workspace.subgroups]);

  // The owner read model's identity is decided by the server's configuration identity:
  // subscription version + rules version. Actions (refresh/retry/release/download) re-read by
  // invalidating this read model, never by the frontend mutating the tree itself.
  const viewQuery = useQuery({
    queryKey: rssListKeys.ownerView(
      animeID,
      workspace.subscription.version,
      workspace.rules.version,
      previewMode ? JSON.stringify([rulesPreviewSections ?? null, metadataPreview ?? null]) : "",
    ),
    retry: false,
    queryFn: async (): Promise<RSSListView> => {
      const view = await readAnimeRSSListView(
        previewMode
          ? {
              input_source: "owner_draft",
              managed_anime_id: animeID,
              ...(rulesPreviewSections ? { rules_preview: rulesPreviewSections } : {}),
              ...(metadataPreview ? { metadata_preview: metadataPreview } : {}),
              limit_per_group: OWNER_ITEMS_PER_GROUP,
            }
          : { input_source: "owner", managed_anime_id: animeID, limit_per_group: OWNER_ITEMS_PER_GROUP },
      );
      // The runtime payload is an external boundary: a response missing groups must fail closed
      // into an error state, never let the whole panel throw while rendering.
      if (view === null || typeof view !== "object" || !Array.isArray(view.groups)) {
        throw new Error(t("rss.listBadFormat"));
      }
      return view;
    },
  });
  const view = viewQuery.data;

  // The latest read model doubles as the subgroups tab's subscription-facts source (the server's effective_selected).
  useEffect(() => {
    if (view) onViewRead?.(view);
  }, [view, onViewRead]);

  // The owner read model is the server's projection of workspace facts: after every workspace
  // re-read (polling advancing downloads/verification/imports, or a new Release; an SSE `rss`
  // event without an anime id also reaches here through the workspace re-read, while an event with
  // an id invalidates this anime's list key directly), the anime's RSS list re-reads exactly once,
  // otherwise the panel would stay on a stale result. react-query reuses the old object for
  // deeply-equal data, so the re-read triggers only on a real change.
  const lastWorkspace = useRef(workspace);
  useEffect(() => {
    if (lastWorkspace.current === workspace) return;
    lastWorkspace.current = workspace;
    void queryClient.invalidateQueries({ queryKey: rssListKeys.owner(animeID) });
  }, [workspace, queryClient, animeID]);

  /** The re-read after an action: read-model invalidation + the outer workspace re-read (keeping the existing polling-window semantics). */
  const handleChanged = () => {
    void queryClient.invalidateQueries({ queryKey: rssListKeys.owner(animeID) });
    onChanged();
  };

  // Re-runs one seed: the whole chain re-enqueues — the server re-evaluates under the current
  // rules (it may be filtered), and on acceptance downloads and imports again, with no confirmation
  // dialog. Available after a download failure, an import failure, or a lost library file. The
  // outcome (started / filtered / held / present / could not start) gets a one-line note; reason
  // codes surface only through the copy catalog.
  async function handleRetryRelease(release: Release) {
    setBusyKey(`release:${release.id}`);
    try {
      const result = await retryRelease(release.id, newUUID());
      showReleaseRetryResult(result);
      handleChanged();
    } catch (cause) {
      const reason = cause instanceof ApiError && cause.code === "RELEASE_NOT_RETRYABLE" ? cause.details?.[0]?.code : undefined;
      toast.error(reason ? t("rss.releaseRetry.blocked", { reason: reasonLabel(reason) ?? "" }) : toUserMessage(cause, t("rss.releaseRetry.failed")));
    } finally {
      setBusyKey(undefined);
    }
  }

  async function handleRetry(target: RetryTarget) {
    setBusyKey(`${target.ownerType}:${target.ownerId}`);
    // Stage retries are retired: the download_task / artifact UI entries are gone and only the
    // RSS source refresh remains here. Repairing file imports lives in the execution record's
    // import stage (retry import), not here.
    const call = {
      rss_source_runtime: retryRSSSourceRuntime,
    }[target.ownerType];
    try {
      await call(
        target.ownerId,
        { failed_work_item_id: target.failedWorkItemId, reason: t("rss.retryReason") },
        newUUID(),
      );
      toast.success(t("rss.retried"));
      handleChanged();
    } catch (cause) {
      toast.error(toUserMessage(cause, t("rss.retryFailed")));
    } finally {
      setBusyKey(undefined);
    }
  }

  async function handleRefresh() {
    if (refreshBusy) return;
    setRefreshBusy(true);
    try {
      const summary = await refreshEligibleRSSSources(workspace);
      if (summary.eligible === 0) {
        toast.info(t("rss.nothingToRefresh"));
      } else if (summary.failed === 0) {
        toast.success(t("rss.refreshSubmitted", { n: summary.succeeded }));
      } else if (summary.succeeded === 0) {
        toast.error(t("rss.refreshSubmitFailed", { n: summary.failed }));
      } else {
        toast.warning(t("rss.refreshPartial", { ok: summary.succeeded, failed: summary.failed }));
      }
      // A re-read is triggered whether or not any refreshable source exists: this is both the
      // manual re-read entry and, through the outer onChanged, a reopening of the 30-second
      // polling window.
      handleChanged();
    } catch (cause) {
      toast.error(toUserMessage(cause, t("rss.refreshFailed")));
    } finally {
      setRefreshBusy(false);
    }
  }

  const ownerConversion = offsetDrawerSource
    ? offsetConversionLine(view?.groups.find((group) => group.scope.rss_source_id === offsetDrawerSource.id), offsetDraft)
    : undefined;

  function openOffsetDrawer(source: OffsetDrawerSource) {
    setOffsetDrawerSource(source);
    setOffsetDraft(String(source.episode_offset));
    setOffsetError(undefined);
  }

  function closeOffsetDrawer(force = false) {
    if (offsetSaving && !force) return;
    setOffsetDrawerSource(undefined);
    setOffsetDraft("");
    setOffsetError(undefined);
  }

  async function handleSaveOffset() {
    if (!offsetDrawerSource) return;
    const trimmed = offsetDraft.trim();
    const offset = trimmed === "" ? Number.NaN : Number(trimmed);
    if (!Number.isInteger(offset) || offset < -1000 || offset > 1000) {
      setOffsetError(t("rss.offsetInvalid"));
      return;
    }
    setOffsetSaving(true);
    setOffsetError(undefined);
    try {
      await rssActions.saveRSSSourceOffset({
        managed_anime_id: animeID,
        rss_source_id: offsetDrawerSource.id,
        expected_version: offsetDrawerSource.version,
        episode_offset: offset,
      });
      toast.success(t("rss.offsetSaved"));
      closeOffsetDrawer(true);
      handleChanged();
    } catch (cause) {
      setOffsetError(toUserMessage(cause, t("rss.offsetSaveFailed")));
    } finally {
      setOffsetSaving(false);
    }
  }

  // In preview mode every entry is present but uniformly disabled with one shared reason; outside
  // preview the existing capability gates (capability.can(...)) and the server's action judgments
  // apply.
  const previewDisabled = previewMode;
  const disabledReason = previewDisabled ? previewDisabledReason() : undefined;
  // A not-yet-started subscription does no work at all (fail closed): refresh and scan are unavailable before the subscription starts.
  const notStarted = workspace.subscription.state === "pending";
  const workDisabledReason = disabledReason ?? (notStarted ? t("rss.pendingDisabled") : undefined);

  const model = useMemo(
    () =>
      view
        ? ownerViewModel(view, workspace, {
            busyKey,
            groupScores,
            onRetry: capability.can("owner.retry") ? (target) => void handleRetry(target) : undefined,
            onOpenOffset: capability.can("rss.offset") ? openOffsetDrawer : undefined,
            onRetryRelease: capability.can("release.retry")
              ? (release) => void handleRetryRelease(release)
              : undefined,
            onOpenStructure: (release) => setStructureRelease(release),
            onOpenRecord: openRecord ? (acquisitionID) => openRecord({ acquisitionID }) : undefined,
          })
        : undefined,
    // handleRetry/openOffsetDrawer are fresh function references every render: they are referenced
    // only by node events and take no part in the model identity, so they are deliberately left out
    // of the dependencies to keep the whole tree from rebuilding each render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [view, workspace, busyKey, groupScores, capability, openRecord],
  );

  const toolbar = (
    <>
      <Checkbox
        size="sm"
        className="tw:text-muted-foreground"
        checked={showFiltered}
        disabled={previewDisabled}
        title={disabledReason}
        onCheckedChange={setShowFiltered}
      >
        {t("rss.showFiltered")}
      </Checkbox>
      {capability.can("rss.refresh") && (
        <Button
          size="sm"
          disabled={refreshBusy || previewDisabled || notStarted}
          title={workDisabledReason}
          onClick={() => void handleRefresh()}
        >
          <RefreshCw className={refreshBusy ? "chain-refresh-icon chain-refresh-icon-spin" : "chain-refresh-icon"} />
          {refreshBusy ? t("act.refreshing") : t("rss.refresh")}
        </Button>
      )}
      {capability.can("targets.scan") && (
        <Button size="sm" disabled={previewDisabled || notStarted} title={workDisabledReason} onClick={() => setFilesOpen(true)}>
          <ScanSearch className="chain-refresh-icon" />
          {t("rss.scan")}
        </Button>
      )}
    </>
  );

  return (
    <div className="rss-tab-stack">
      {previewMode && (
        <div className="rss-notice">
          <p className="rss-notice-text text-caption">
            {metadataPreview
              ? rulesPreviewMode
                ? t("rss.previewNoticeRulesMetadata")
                : t("rss.previewNoticeMetadata")
              : t("rss.previewNotice")}
            {rulesPreviewMode && (
              <HelpTip label={t("rss.help.previewLabel")} className="tw:ml-1 tw:align-middle">{t("rss.help.preview")}</HelpTip>
            )}
          </p>
          <p role="status" className="rss-notice-text text-caption">
            {previewDisabledReason()}
          </p>
          {/* Exiting only drops the rules preview; unsaved metadata stops being previewed only after being saved or discarded. */}
          {rulesPreviewMode && (
            <Button size="sm" variant="ghost" onClick={onExitRulesPreview}>
              {t("rss.exitPreview")}
            </Button>
          )}
        </div>
      )}
      {customFeedLocators(workspace.rss_sources).map((locator, index) => (
        <FeedSiteNote key={index} url={locator} />
      ))}
      {!previewMode && !capability.readOnly && notStarted && (
        <div className="rss-notice">
          <p role="status" className="rss-notice-text text-caption">
            {t("rss.pendingDisabled")}
          </p>
        </div>
      )}
      {!previewMode && capability.readOnly && (
        <div className="rss-notice">
          <p className="rss-notice-text text-caption">
            {t("rss.endedNotice")}
            <HelpTip label={t("rss.help.endedLabel")} className="tw:ml-1 tw:align-middle">{t("rss.help.ended")}</HelpTip>
          </p>
        </div>
      )}
      {viewQuery.error && !view && (
        <StateBlock kind="error" title={toUserMessage(viewQuery.error, t("rss.loadFailed"))} />
      )}
      {!view && !viewQuery.error && <StateBlock kind="busy" title={t("rss.loading")} />}
      {view && model && (
        <RssListViewPanel
          model={model}
          showFiltered={showFiltered}
          badge={previewMode ? <Chip>{t("rss.previewBadge")}</Chip> : undefined}
          toolbar={toolbar}
          notice={
            view.truncated ? (
              <p className="seed-preview-hint text-caption">
                {t("rss.truncated", { n: view.limit_per_group })}
              </p>
            ) : undefined
          }
        />
      )}
      {filesOpen ? (
        <AnimeFilesDialog
          animeID={animeID}
          title={workspace.managed_anime.title}
          readOnly={capability.readOnly}
          scanOnOpen
          onChanged={handleChanged}
          onClose={() => {
            setFilesOpen(false);
            handleChanged();
          }}
        />
      ) : null}
      <ReleaseTorrentStructureDialog
        releaseID={structureRelease?.id}
        onClose={() => setStructureRelease(undefined)}
      />
      <Dialog
        open={offsetDrawerSource !== undefined}
        onRequestClose={() => closeOffsetDrawer()}
        tier="S"
        title={offsetDrawerSource ? t("rss.offsetTitle", { name: offsetDrawerSource.group_name }) : t("rss.offsetTitleGeneric")}
        footer={
          <>
            <Button disabled={offsetSaving} onClick={() => closeOffsetDrawer()}>{t("act.cancel")}</Button>
            <Button variant="primary" disabled={offsetSaving} onClick={() => void handleSaveOffset()}>
              {offsetSaving ? t("act.saving") : t("rss.saveOffset")}
            </Button>
          </>
        }
      >
        {offsetDrawerSource && (
          <div className="offset-modal-stack">
            <p className="offset-modal-source text-caption" title={offsetDrawerSource.title}>
              {offsetDrawerSource.title}
            </p>
            <Field label={t("anime.offset.label")} hint={t("rss.offsetFieldHint")}>
              <Input
                aria-label={t("anime.offset.label")}
                inputMode="numeric"
                type="number"
                min={-1000}
                max={1000}
                step="1"
                value={offsetDraft}
                onChange={(event) => {
                  setOffsetDraft(event.target.value);
                  setOffsetError(undefined);
                }}
              />
            </Field>
            {ownerConversion && <p className="text-caption text-muted">{ownerConversion}</p>}
            {offsetError && (
              <StateBlock kind="error" title={offsetError} />
            )}
          </div>
        )}
      </Dialog>
    </div>
  );
}

/** Custom feed URLs of a draft or subscription; each is labelled with its site support. */
function customFeedLocators(sources: readonly { source_kind: string; locator?: string | null; locator_display?: string | null }[]): string[] {
  return sources
    .filter((source) => source.source_kind === "custom_rss")
    .map((source) => (source.locator ?? source.locator_display ?? "").trim())
    .filter((locator) => locator !== "");
}

