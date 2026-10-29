import { freezeBusinessDraft, hasCustomSettings, loadBusinessPreset, resetToGlobal } from "./businessDraft";
import { confirmBusinessReplace } from "./confirmBusinessReplace";
import { BusinessSnapshotFields } from "./BusinessSnapshotFields";
import { NewReleasesOnlyField, OwnerNewReleasesOnly } from "./NewReleasesOnlyField";
import { setDraftNewReleasesOnly } from "./newReleasesOnly";
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "../../ui/toast";
import { AlertTriangle, CheckCircle2, Loader2, Play } from "lucide-react";
import {
  ApiError,
  getAnimeWorkspace,
  isVersionConflict,
  patchAnimeSubscription,
  getBusinessPreset,
  newUUID,
  toUserMessage,
  type AnimeConfigCandidate,
  type AnimeDraft,
  type AnimeMetadataDraft,
  type AnimeWorkspace,
  type PreviewFeedSnapshotRef,
  type ProviderGroupDiscoveryGroup,
  type RSSListObservationRef,
} from "../../lib/api/client";
import { draftChangeCount, draftPreviewable, validateDraft, valuesEqual } from "../../lib/draft";
import { CandidateAlreadyActivated, CandidateNotReady, CandidateVersionConflict, CandidateWorkingDuplicate } from "../../lib/candidate/port";
import { getCandidateStore } from "../../lib/candidate/serverCandidateStore";
import type { CatalogProvider } from "../../lib/providers/port";
import type { CatalogEntryView, CatalogMetadataMatch } from "../../lib/providers/viewmodels";
import { SaveStatus } from "../../ui/SaveFooter";
import { AnimeTabs } from "../anime-tabs/AnimeTabs";
import type { DraftTabHost } from "../anime-tabs/host";
import { isWorkspaceOwnerChainStable } from "../workspace/ownerChain";
import { workspaceCapability } from "../workspace/capability";
import { animeKeys, catalogKeys, configKeys } from "../../lib/queryKeys";
import { draftFromEntry } from "../catalog/entryDraft";
import { announceSubscribed } from "../catalog/startSubscription";
import { ExternalValue, MetadataBadges } from "../catalog/card";
import { HeaderPoster } from "./HeaderPoster";
import { formatDateTime, metadataHoldBlocking, subscriptionStateLabel, subscriptionStateTone, type BlockingPhrases } from "../../lib/labels";
import { StateBlock, StatePill, Button, useDirtyConfirm } from "../../ui";
import { RouteDialogLeaveGuard } from "../../shell/RouteDialogLeaveGuard";
import { useRecordLink } from "../../shell/recordLinkContext";
import { Dialog, HelpTip } from "../../ui/base";
import type { MetadataMatchRequest } from "../anime-tabs/MetadataTab";
import { t } from "../../lib/i18n";

// The dialog:
// - Before subscribing and after, the same four-tab implementation in features/anime-tabs/ is
//   shared (the settled ruling: they are essentially one tab, and the mode difference may only
//   be the save path plus mode-specific actions).
// - Before subscribing: the four tabs edit one in-memory AnimeDraft; Subscribe is possible only
//   after the candidate is saved; one-shot downloads are triggered separately from the unified
//   RSS workspace's seed rows.
// - After subscribing: each owner editing surface saves with its own CAS; the global log is
//   reached from the top-right entry.
// The editing area is a refinement area, not a subscription precondition.

// A brand-new draft starts from one complete Global business snapshot with every
// rules section and tag_filters copied from it (every per-anime section is a
// one-time input). That is its initial state, not a
// replacement, so nothing is confirmed. From then on the draft and the
// subscription made from it own their values; a later Global save never reaches
// them. "Use defaults everywhere" copies the current Global again. The server never fills a
// missing business in; a draft without one cannot be previewed or subscribed.
// The title area's metadata facts (the badges and "metadata matched"). A catalog entry is the
// snapshot frozen at open; once the dialog is open the current mode's authoritative source
// wins: draft mode reads the in-memory draft's bindings, subscribed mode reads the workspace
// read model.
type HeaderMetadata = Pick<CatalogEntryView, "metadataMatch" | "metadataHold">;

/** Draft bindings -> the title area's match facts. A complete match requires both Bangumi and
 *  TMDB bound; custom presets follow the same terms as provider entries: no binding means "not
 *  linked", and the title area keeps showing which side is missing — never hidden because it is
 *  a preset. */
function draftHeaderMetadataMatch(metadata: AnimeMetadataDraft): CatalogMetadataMatch {
  const bgmId = metadata.bgm?.work_id || undefined;
  const tmdb = metadata.tmdb ?? undefined;
  const tmdbWorkId = tmdb?.work_id || undefined;
  return {
    matched: !!bgmId && !!tmdbWorkId,
    bgmId,
    tmdbWorkId,
    tmdbMediaKind: tmdb?.media_kind,
    tmdbSeason: tmdb?.season,
    mediaKind: metadata.media_kind ?? undefined,
  };
}

/** workspace read model -> title-area facts: holds and bindings both follow the workspace, never falling back to the frozen entry. */
function workspaceHeaderMetadata(workspace: AnimeWorkspace): HeaderMetadata {
  const tmdb = workspace.metadata.tmdb ?? undefined;
  return {
    metadataMatch: {
      matched: !workspace.metadata_hold.held,
      bgmId: workspace.metadata.bgm?.work_id || undefined,
      tmdbWorkId: tmdb?.work_id || undefined,
      tmdbMediaKind: tmdb?.media_kind,
      tmdbSeason: tmdb?.season,
      mediaKind: workspace.metadata.media_kind ?? undefined,
    },
    metadataHold: workspace.metadata_hold,
  };
}

async function newDraftFromGlobal(
  entry: Parameters<typeof draftFromEntry>[0],
  preset: Promise<Awaited<ReturnType<typeof getBusinessPreset>>> = getBusinessPreset(),
): Promise<AnimeDraft> {
  return loadBusinessPreset(draftFromEntry(entry), await preset);
}

export function AnimeConfigDialog({
  open,
  onOpenChange,
  provider,
  entry,
  sourceLabel,
  onSubscribed,
  onCandidateStateChanged = () => {},
  onOpenExisting,
  initialTab,
  onTabChange,
  routeHosted = false,
  routeOpen = true,
  onRouteClosed,
  onRouteRestore,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  provider: CatalogProvider;
  entry: CatalogEntryView;
  sourceLabel: string;
  onSubscribed: () => void;
  /** Refresh catalog after an activation conflict without closing the dialog. */
  onCandidateStateChanged?: () => void;
  /** A subscribe was refused because the provider entry already has one live subscription (stale
   *  browse data): the host opens that subscription's configuration. */
  onOpenExisting?: (managedAnimeID: string) => void;
  /** The tab selected at open (e.g. a card's "subscription policy" landing directly on subgroups). */
  initialTab?: string;
  /** Reports the current tab (written into `?tab=` when the route carries it). */
  onTabChange?: (tab: string) => void;
  /**
   * 由路由承载（目录底座 `/?anime=…`）：浏览器返回/改地址关闭弹窗时，有未保存
   * 修改先走同一个丢弃确认（RouteDialogLeaveGuard，需要 data router）。
   */
  routeHosted?: boolean;
  /**
   * 路由承载时：地址是否仍指向本弹窗。地址在拦截器之外变了（例如手动改 hash，
   * react-router 无法拦下没有历史索引的 POP）时变为 false：无未保存修改则
   * onRouteClosed；有则保持弹窗并弹出同一个丢弃确认——放弃 → onRouteClosed，
   * 取消 → onRouteRestore（调用方把地址推回本弹窗）。
   */
  routeOpen?: boolean;
  onRouteClosed?: () => void;
  onRouteRestore?: () => void;
}) {
  // The mode is decided by the catalog entry alone: the dialog closes after a draft's
  // activation (subscribe) succeeds; one-shot downloads come from the unified RSS workspace's
  // seed rows and no longer switch this dialog's mode in place.
  const subscribed = entry.subscriptionState !== undefined && !!entry.managedAnimeRef;
  const draftDirtyRef = useRef(false);
  const ownerDirtyRef = useRef(false);

  // Title-area metadata overrides are scoped to the entry and mode; switching either invalidates the old override automatically.
  const headerKey = `${entry.source}|${entry.providerEntryRef}|${entry.managedAnimeRef ?? ""}|${subscribed}`;
  const [headerOverride, setHeaderOverride] = useState<{ key: string; value: HeaderMetadata }>();
  const reportHeaderMetadata = useCallback(
    (value: HeaderMetadata) => setHeaderOverride({ key: headerKey, value }),
    [headerKey],
  );
  const headerEntry = useMemo<CatalogEntryView>(
    () => (headerOverride?.key === headerKey ? { ...entry, ...headerOverride.value } : entry),
    [entry, headerKey, headerOverride],
  );

  const handleDraftDirtyChange = useCallback((dirty: boolean) => {
    draftDirtyRef.current = dirty;
  }, []);

  const handleOwnerDirtyChange = useCallback((dirty: boolean) => {
    ownerDirtyRef.current = dirty;
  }, []);

  // Before subscribing, draft dirty is checked; after, owner-edit dirty. One discard confirmation for both modes.
  const isDirty = useCallback(
    () => (subscribed ? ownerDirtyRef.current : draftDirtyRef.current),
    [subscribed],
  );

  // A blocked route departure (browser back etc.): after the discard is confirmed the original navigation proceeds, rather than issuing another close.
  const pendingLeaveRef = useRef<(() => void) | null>(null);
  const closeAfterDiscard = useCallback(() => {
    draftDirtyRef.current = false;
    ownerDirtyRef.current = false;
    const leave = pendingLeaveRef.current;
    pendingLeaveRef.current = null;
    if (leave) leave();
    else onOpenChange(false);
  }, [onOpenChange]);

  const { requestClose, confirmDialog, confirmOpen } = useDirtyConfirm({
    dirty: isDirty,
    onClose: closeAfterDiscard,
    description: subscribed ? t("config.discardSubscribed") : t("config.discardDraft"),
    danger: true,
  });

  const handleSubscribed = useCallback(() => {
    draftDirtyRef.current = false;
    onSubscribed();
    onOpenChange(false);
  }, [onOpenChange, onSubscribed]);

  useEffect(() => {
    draftDirtyRef.current = false;
    ownerDirtyRef.current = false;
  }, [entry.providerEntryRef, subscribed]);

  useEffect(() => {
    if (!open) {
      draftDirtyRef.current = false;
      ownerDirtyRef.current = false;
    }
  }, [open]);

  const requestRouteLeave = useCallback(
    (proceed: () => void) => {
      pendingLeaveRef.current = proceed;
      requestClose();
    },
    [requestClose],
  );
  const cancelRouteLeave = useCallback(() => {
    pendingLeaveRef.current = null;
  }, []);

  // The address has already left this dialog (a navigation the blocker cannot intercept): see
  // routeOpen. On discard closeAfterDiscard calls onRouteClosed and the caller unmounts this
  // component right away; if the confirm closes but the component remains, that was a cancel.
  const routeLeavePromptedRef = useRef(false);
  useEffect(() => {
    if (!routeHosted) return;
    if (routeOpen) {
      routeLeavePromptedRef.current = false;
      return;
    }
    if (routeLeavePromptedRef.current) {
      if (!confirmOpen) {
        routeLeavePromptedRef.current = false;
        pendingLeaveRef.current = null;
        onRouteRestore?.();
      }
      return;
    }
    if (!isDirty()) {
      onRouteClosed?.();
      return;
    }
    routeLeavePromptedRef.current = true;
    pendingLeaveRef.current = () => onRouteClosed?.();
    requestClose();
  }, [confirmOpen, isDirty, onRouteClosed, onRouteRestore, requestClose, routeHosted, routeOpen]);

  const handleOpenChange = useCallback(
    (nextOpen: boolean) => {
      if (nextOpen) onOpenChange(true);
      else requestClose();
    },
    [onOpenChange, requestClose],
  );

  return (
    <>
      <Dialog
        open={open}
        onRequestClose={() => handleOpenChange(false)}
        tier="L"
        focusTitle
        bodyLayout="fill"
        title={
          <span className="tw:block tw:truncate" title={entry.title}>
            {entry.title}
          </span>
        }
        headerStart={<HeaderPoster entry={entry} />}
        description={<ConfigHeaderFacts entry={headerEntry} sourceLabel={sourceLabel} />}
        headerExtra={
          <div className="tw:flex tw:flex-none tw:flex-wrap tw:justify-end tw:gap-1.5">
            {subscribed ? (
              entry.lifecycleState === "retired" ? (
                <StatePill tone="muted">{t("anime.state.ended")}</StatePill>
              ) : (
                <StatePill tone={subscriptionStateTone(entry.subscriptionState ?? "pending")}>
                  {subscriptionStateLabel(entry.subscriptionState ?? "pending")}
                </StatePill>
              )
            ) : headerEntry.metadataMatch.matched ? (
              <StatePill tone="online" icon={CheckCircle2}>{t("config.metadataMatched")}</StatePill>
            ) : null}
          </div>
        }
      >
        <div className="anime-config-dialog-body">
          {subscribed ? (
            <SubscribedBody
              open={open}
              managedAnimeRef={entry.managedAnimeRef ?? ""}
              onClose={requestClose}
              onDirtyChange={handleOwnerDirtyChange}
              onHeaderMetadata={reportHeaderMetadata}
              initialTab={initialTab}
              onTabChange={onTabChange}
            />
          ) : (
            <DraftBody
              provider={provider}
              entry={entry}
              onSubscribed={handleSubscribed}
              onCandidateStateChanged={onCandidateStateChanged}
              onOpenExisting={
                onOpenExisting
                  ? (id) => {
                      draftDirtyRef.current = false;
                      onOpenExisting(id);
                    }
                  : undefined
              }
              onDirtyChange={handleDraftDirtyChange}
              onHeaderMetadata={reportHeaderMetadata}
              onRequestClose={requestClose}
              initialTab={initialTab}
              onTabChange={onTabChange}
            />
          )}
        </div>
      </Dialog>
      {confirmDialog}
      {routeHosted ? (
        <RouteDialogLeaveGuard
          param="anime"
          isDirty={isDirty}
          confirmOpen={confirmOpen}
          onBlocked={requestRouteLeave}
          onCancelled={cancelRouteLeave}
        />
      ) : null}
    </>
  );
}

// Title-area facts (moved off the card face in the card redesign): site · season · subgroups ·
// the anime-page link and the original title, plus the Bangumi / TMDB links. Description
// renders as a <p> and only inline elements inside.
function ConfigHeaderFacts({ entry, sourceLabel }: { entry: CatalogEntryView; sourceLabel: string }) {
  return (
    <span className="anime-config-head-facts">
      <span className="anime-config-head-line">
        <span>{sourceLabel}</span>
        {entry.season && <span>· {entry.season}</span>}
        {entry.groupName && <span>· {entry.groupName}</span>}
        {entry.sourceUrl && <ExternalValue href={entry.sourceUrl}>{t("grid.card.sourcePage")}</ExternalValue>}
      </span>
      {entry.originalTitle && (
        <span className="anime-config-head-original" title={entry.originalTitle}>
          {entry.originalTitle}
        </span>
      )}
      <MetadataBadges entry={entry} />
    </span>
  );
}

// --- Subscribed mode: workspace query + polling window + AnimeTabs(owner) -----------------

function SubscribedBody({
  open,
  managedAnimeRef,
  onClose,
  onDirtyChange,
  onHeaderMetadata,
  initialTab,
  onTabChange,
}: {
  open: boolean;
  managedAnimeRef: string;
  onClose: () => void;
  onDirtyChange: (dirty: boolean) => void;
  onHeaderMetadata: (value: HeaderMetadata) => void;
  initialTab?: string;
  onTabChange?: (tab: string) => void;
}) {
  // The 30-second polling window: opens with the dialog; onChanged (a re-read triggered by
  // retry/refresh/save) resets the window, so a retry submitted after the deadline is tracked
  // by polling again.
  const [pollingDeadline, setPollingDeadline] = useState(0);
  const queryClient = useQueryClient();
  // Execution records are a separate routed dialog: opening it replaces this anime's parameter and the back key returns here.
  const openRecord = useRecordLink();
  useEffect(() => {
    if (open && managedAnimeRef !== "") setPollingDeadline(Date.now() + 30_000);
  }, [open, managedAnimeRef]);

  const workspace = useQuery({
    queryKey: animeKeys.workspace(managedAnimeRef),
    queryFn: () => getAnimeWorkspace(managedAnimeRef),
    enabled: open && managedAnimeRef !== "",
    retry: false,
    refetchInterval: (query) => {
      if (!open || managedAnimeRef === "" || query.state.error || !query.state.data) return false;
      if (Date.now() >= pollingDeadline) return false;
      return isWorkspaceOwnerChainStable(query.state.data) ? false : 1_000;
    },
  });

  // After a match is saved the workspace re-reads and the title area updates with it (the catalog snapshot is not waited on).
  useEffect(() => {
    if (workspace.data) onHeaderMetadata(workspaceHeaderMetadata(workspace.data));
  }, [onHeaderMetadata, workspace.data]);

  const [starting, setStarting] = useState(false);
  // The blocking row's fix action: switch to the metadata tab and open that site's match dialog.
  const [matchRequest, setMatchRequest] = useState<MetadataMatchRequest>();

  function handleChanged() {
    setPollingDeadline(Date.now() + 30_000);
    void workspace.refetch();
    // Owner saves/retries change card-visible state (e.g. a metadata hold), so the catalog list is invalidated too.
    void queryClient.invalidateQueries({ queryKey: catalogKeys.all });
  }

  // Lifecycle-driven read-only gating: a retired owner opens the very same workspace read
  // model, with capability switching off every mutation. The judgment happens only here and in
  // the capability module.
  const capability = workspaceCapability(workspace.data?.subscription.lifecycle_state);
  const pending = !capability.readOnly && workspace.data?.subscription.state === "pending";

  // Not started → running: the ordinary first start (no baseline recovery), CAS on the workspace version.
  async function startSubscription() {
    const subscription = workspace.data?.subscription;
    if (!subscription || starting) return;
    setStarting(true);
    try {
      await patchAnimeSubscription(managedAnimeRef, { expected_version: subscription.version, state: "active" });
      toast.success(t("anime.started"));
    } catch (cause) {
      toast.error(isVersionConflict(cause) ? t("catalog.changed") : toUserMessage(cause, t("anime.startFailed")));
    } finally {
      setStarting(false);
      handleChanged();
    }
  }

  const footerStart = (
    <>
      {openRecord && managedAnimeRef !== "" ? (
        <Button variant="ghost" onClick={() => openRecord({ managedAnimeID: managedAnimeRef })}>
          {t("records.title")}
        </Button>
      ) : null}
      <Button onClick={onClose}>{t("act.close")}</Button>
    </>
  );
  const footerEnd = pending ? (
    <Button variant="primary" onClick={() => void startSubscription()} disabled={starting}>
      <Play className="anime-config-subscribe-icon" />
      {starting ? t("anime.starting") : t("anime.start")}
    </Button>
  ) : null;

  return (
    <>
      {workspace.isLoading && (
        <div className="anime-config-dialog-state">
          <StateBlock kind="busy" title={t("config.reading")} />
        </div>
      )}
      {workspace.error && (
        <div className="anime-config-dialog-state">
          <StateBlock
            kind="error"
            title={toUserMessage(workspace.error, t("config.loadFailed"))}
            action={
              <Button size="sm" onClick={() => void workspace.refetch()}>
                {t("act.retry")}
              </Button>
            }
          />
        </div>
      )}
      {workspace.data && (
        <>
          {/* The subscription state appears once, in the title pill; at most one note line here, details into the HelpTip. */}
          {capability.readOnly && <StateLine text={t("anime.ended.banner")} />}
          {pending && (
            <StateLine
              text={t("anime.pending.banner")}
              help={<HelpTip label={t("anime.pending.helpLabel")}>{t("anime.pending.help")}</HelpTip>}
            />
          )}
          {/* When blocked, the info row becomes one warning line with the fix action; imports waiting
              for the configuration add their file count to it (they continue by themselves). */}
          {!capability.readOnly && (workspace.data.metadata_hold.held || workspace.data.config_waiting_files > 0) ? (
            <BlockingRow
              phrases={metadataHoldBlocking(workspace.data.metadata_hold)}
              waitingFiles={workspace.data.config_waiting_files}
              active={workspace.data.subscription.state === "active"}
              onFix={(link) => setMatchRequest((prev) => ({ provider: link, seq: (prev?.seq ?? 0) + 1 }))}
            />
          ) : (
            <WorkspaceSummaryRow
              seedCount={workspace.data.releases.length}
              latestAt={latestReleasePublishedAt(workspace.data)}
            />
          )}
          <AnimeTabs
            host={{
              mode: "owner",
              animeID: managedAnimeRef,
              workspace: workspace.data,
              capability,
              onChanged: handleChanged,
              // Fetch new seeds only: changeable while not started (saved immediately), read-only afterwards.
              rulesExtra: (
                <OwnerNewReleasesOnly animeID={managedAnimeRef} workspace={workspace.data} capability={capability} onChanged={handleChanged} />
              ),
              // The subscribed episode-versions editor (ReplacementPolicySection) lives in the
              // rules tab itself as a save part of the single footer save; the draft edits its
              // own copy through BusinessSnapshotFields.
            }}
            initialTab={initialTab}
            onTabChange={onTabChange}
            onDirtyChange={onDirtyChange}
            matchRequest={matchRequest}
            footerStart={footerStart}
            footerEnd={footerEnd}
            saveIsPrimary={!pending}
          />
        </>
      )}
      {!workspace.data && (
        <div className="anime-config-dialog-footer anime-config-dialog-footer-owner">
          {footerStart}
        </div>
      )}
    </>
  );
}

// --- Draft mode: candidate load/save + Subscribe + AnimeTabs(draft) ------------------------

function DraftBody({
  provider,
  entry,
  onSubscribed,
  onCandidateStateChanged,
  onOpenExisting,
  onDirtyChange,
  onHeaderMetadata,
  onRequestClose,
  initialTab,
  onTabChange,
}: {
  provider: CatalogProvider;
  entry: CatalogEntryView;
  onSubscribed: () => void;
  /** Activation conflicts invalidate the catalog without closing this draft dialog. */
  onCandidateStateChanged: () => void;
  onOpenExisting?: (managedAnimeID: string) => void;
  onDirtyChange: (dirty: boolean) => void;
  onHeaderMetadata: (value: HeaderMetadata) => void;
  onRequestClose: () => void;
  initialTab?: string;
  onTabChange?: (tab: string) => void;
}) {
  const candidateStore = getCandidateStore();
  const queryClient = useQueryClient();

  // Candidate loading state
  const [candidateId, setCandidateId] = useState<string | null>(null);
  const [candidateVersion, setCandidateVersion] = useState(0);
  const [candidateRevision, setCandidateRevision] = useState<number | null>(null);
  const [candidateStatus, setCandidateStatus] = useState<AnimeConfigCandidate["status"] | null>(null);
  const [candidateResolutionBlocked, setCandidateResolutionBlocked] = useState(false);
  const [candidateLoaded, setCandidateLoaded] = useState(false);

  // A stable activation attempt: the key is generated once at the first click; retries after a
  // network error or an interrupted response reuse the same key, expected version and initial
  // state. Cleared when candidate/version/state switches or on success.
  const activationAttemptRef = useRef<{
    candidateId: string;
    expectedVersion: number;
    initialState: "active" | "stopped";
    idempotencyKey: string;
  } | null>(null);

  // Draft editing state
  const [draft, setDraft] = useState<AnimeDraft>(() => draftFromEntry(entry));
  // savedDraft must be state, not a ref: the dirty memo depends on it and a successful save
  // must trigger recomputation to clear dirty, otherwise the subscribe button stays disabled
  // forever (observed as a live regression).
  const [savedDraft, setSavedDraft] = useState<AnimeDraft>(() => structuredClone(draft));
  // The title area reads the current in-memory draft's bindings: a completed (unsaved) match updates at once, clearing a binding restores the notice.
  const draftMetadata = draft.metadata;
  useEffect(() => {
    onHeaderMetadata({ metadataMatch: draftHeaderMetadataMatch(draftMetadata) });
  }, [draftMetadata, onHeaderMetadata]);
  // The exact per-group feed snapshot identity backfilled by the unified RSS workspace. It is
  // the draft read model's only input: not a URL, and not something a live fetch may fake.
  // Cleared by child unmount when the entry/candidate switches.
  const [feedSnapshots, setFeedSnapshots] = useState<PreviewFeedSnapshotRef[]>([]);
  const [observation, setObservation] = useState<RSSListObservationRef>();
  const [observedGroups, setObservedGroups] = useState<ProviderGroupDiscoveryGroup[]>([]);
  const [saving, setSaving] = useState(false);
  const [subscribing, setSubscribing] = useState(false);
  const [error, setError] = useState<string>();
  const [saveConflict, setSaveConflict] = useState<{ currentVersion?: number }>();
  const [conflictBusy, setConflictBusy] = useState(false);

  // The current default settings: decides whether "use defaults everywhere" is available. A read failure does not block editing.
  const businessPreset = useQuery({ queryKey: configKeys.businessPreset, queryFn: getBusinessPreset });
  // A new draft shares the same read as the query above (one Global, not one fetch each).
  const cachedPreset = useCallback(
    () => queryClient.fetchQuery({ queryKey: configKeys.businessPreset, queryFn: getBusinessPreset, staleTime: 30_000 }),
    [queryClient],
  );
  const candidateQuery = useMemo(() => {
    const origin = entry.source as "anibt" | "mikan" | "custom";
    return {
      origin,
      origin_entry_ref: entry.source === "anibt" || entry.source === "mikan" ? entry.providerEntryRef : undefined,
    };
  }, [entry.providerEntryRef, entry.source]);

  const candidateMatchesEntry = useCallback(
    (candidate: AnimeConfigCandidate): boolean => {
      if (candidate.origin !== entry.source) return false;
      if (entry.source === "anibt" || entry.source === "mikan") {
        return candidate.origin_entry_ref === entry.providerEntryRef;
      }
      return candidate.origin_entry_ref === null;
    },
    [entry.providerEntryRef, entry.source],
  );

  function applyCandidate(candidate: AnimeConfigCandidate) {
    activationAttemptRef.current = null;
    setCandidateId(candidate.id);
    setCandidateVersion(candidate.version);
    setCandidateRevision(candidate.current_revision);
    setCandidateStatus(candidate.status);
    setCandidateResolutionBlocked(candidate.status !== "working");
    setDraft(structuredClone(candidate.draft));
    setSavedDraft(structuredClone(candidate.draft));
  }

  async function readLatestWorkingCandidate(): Promise<AnimeConfigCandidate | null> {
    const exactCandidateID = entry.candidateId ?? candidateId;
    if (exactCandidateID) {
      const current = await candidateStore.get(exactCandidateID);
      if (current && candidateMatchesEntry(current) && current.status === "working") return current;
      return null;
    }
    if (entry.source === "custom") return null;
    const candidates = await candidateStore.list(candidateQuery);
    return candidates.find((item) => item.status === "working" && candidateMatchesEntry(item)) ?? null;
  }

  async function reloadLatestCandidate() {
    setConflictBusy(true);
    setError(undefined);
    try {
      const latest = await readLatestWorkingCandidate();
      if (latest) {
        applyCandidate(latest);
        setSaveConflict(undefined);
        setError(undefined);
        toast.success(t("config.reloaded"));
      } else if (entry.candidateId || entry.source === "custom") {
        setCandidateId(null);
        setCandidateVersion(0);
        setCandidateRevision(null);
        setCandidateStatus(null);
        setCandidateResolutionBlocked(true);
        setSaveConflict(undefined);
        setError(t("config.draftGone"));
      } else {
        setCandidateId(null);
        setCandidateVersion(0);
        setCandidateRevision(null);
        setCandidateStatus(null);
        setCandidateResolutionBlocked(false);
        const initial = await newDraftFromGlobal(entry);
        setDraft(initial);
        setSavedDraft(initial);
        setSaveConflict(undefined);
        setError(t("config.noDraft"));
      }
    } catch (cause) {
      setError(toUserMessage(cause, t("config.readLatestFailed")));
    } finally {
      setConflictBusy(false);
    }
  }

  async function overwriteLatestCandidate() {
    setConflictBusy(true);
    setError(undefined);
    try {
      let target: AnimeConfigCandidate | null = null;
      let expectedVersion = saveConflict?.currentVersion;
      if (!candidateId || expectedVersion === undefined) {
        target = await readLatestWorkingCandidate();
        if (!target) {
          setError(t("config.noDraftReload"));
          return;
        }
        expectedVersion = target.version;
      }
      const updated = await candidateStore.patch(
        target?.id ?? candidateId!,
        draft,
        expectedVersion,
      );
      applyCandidate(updated);
      setSaveConflict(undefined);
      toast.success(t("config.overwritten"));
    } catch (cause) {
      if (cause instanceof CandidateVersionConflict) {
        setSaveConflict({ currentVersion: cause.currentVersion });
        setError(t("config.conflictAgain"));
      } else {
        setError(toUserMessage(cause, t("config.overwriteFailed")));
      }
    } finally {
      setConflictBusy(false);
    }
  }

  // At open, the exact candidate the entry carries is read first; only a provider entry without
  // a candidateId reads its working candidate by provider identity. dormant/archived keep only
  // history: they cannot be edited or activated and never silently switch to another custom
  // candidate.
  useEffect(() => {
    let cancelled = false;
    setCandidateLoaded(false);
    setError(undefined);
    setSaveConflict(undefined);
    setCandidateResolutionBlocked(false);
    activationAttemptRef.current = null;
    (async () => {
      try {
        if (entry.candidateId) {
          const exact = await candidateStore.get(entry.candidateId);
          if (cancelled) return;
          if (!exact || !candidateMatchesEntry(exact)) {
            setCandidateId(null);
            setCandidateVersion(0);
            setCandidateRevision(null);
            setCandidateStatus(null);
            setCandidateResolutionBlocked(true);
            const initial = draftFromEntry(entry);
            setDraft(initial);
            setSavedDraft(initial);
            setError(t("config.draftMismatch"));
          } else {
            applyCandidate(exact);
            if (exact.status !== "working") {
              setError(t("config.draftNotEditable"));
            }
          }
        } else if (entry.source === "custom") {
          // A newly composed custom draft has no candidate identity yet. It may
          // be explicitly saved, but it must never resolve another custom row.
          const initial = await newDraftFromGlobal(entry, cachedPreset());
          if (cancelled) return;
          setCandidateId(null);
          setCandidateVersion(0);
          setCandidateStatus(null);
          setCandidateResolutionBlocked(false);
          setDraft(initial);
          setSavedDraft(initial);
        } else {
          // The Global preset is read while the candidate list is in flight: the
          // common first open has no working candidate and needs both, so the
          // two round trips overlap instead of queueing. A rejection is only
          // surfaced when the preset is actually consumed below.
          const presetRequest = cachedPreset();
          presetRequest.catch(() => undefined);
          const candidates = await candidateStore.list(candidateQuery);
          if (cancelled) return;
          const latest = candidates.find((item) => item.status === "working" && candidateMatchesEntry(item));
          if (latest) {
            applyCandidate(latest);
          } else {
            // The provider has no working candidate: initialize from the entry; the candidate is created only after an explicit save.
            const initial = await newDraftFromGlobal(entry, presetRequest);
            if (cancelled) return;
            setCandidateId(null);
            setCandidateVersion(0);
            setCandidateRevision(null);
            setCandidateStatus(null);
            setDraft(initial);
            setSavedDraft(initial);
          }
        }
      } catch (cause) {
        // Fail-closed: a read failure never silently initializes the draft from the entry,
        // otherwise the user could unknowingly overwrite a candidate that already exists on the
        // server.
        if (!cancelled) {
          setCandidateResolutionBlocked(true);
          setError(toUserMessage(cause, t("config.readExistingFailed")));
        }
      } finally {
        if (!cancelled) setCandidateLoaded(true);
      }
    })();
    return () => { cancelled = true; };
  }, [cachedPreset, candidateMatchesEntry, candidateQuery, candidateStore, entry, entry.candidateId]);

  const dirty = useMemo(() => !valuesEqual(draft, savedDraft), [draft, savedDraft]);
  const [resettingDefaults, setResettingDefaults] = useState(false);
  // "Use defaults everywhere": the rules sections and tag filter copy the default settings
  // again and the subscription settings (parser, unknown episodes etc.) become the current
  // defaults; subgroups, RSS sources and metadata are untouched. Confirmed first when unsaved
  // changes exist.
  async function resetAllToDefaults() {
    if (dirty && !await confirmBusinessReplace(t("config.useDefaultsBody"), {
      title: t("config.useDefaultsTitle"),
      confirmLabel: t("config.useDefaultsConfirm"),
    })) return;
    setResettingDefaults(true);
    try {
      const preset = await queryClient.fetchQuery({ queryKey: configKeys.businessPreset, queryFn: getBusinessPreset, staleTime: 0 });
      setDraft((current) => resetToGlobal(current, preset));
      toast.success(t("config.useDefaultsDone"));
    } catch (cause) { setError(toUserMessage(cause, t("config.defaultsFailed"))); }
    finally { setResettingDefaults(false); }
  }
  const customized = hasCustomSettings(draft, businessPreset.data);
  const problems = useMemo(() => validateDraft(draft), [draft]);
  // A clean draft without a candidate yet (a new entry opened from a catalog card) can also
  // subscribe directly: subscribing first creates the candidate from the current content and
  // then activates it — with no quick-subscribe button on the card face, subscribing only
  // happens here.
  const unsavedCleanDraft = candidateId === null && !dirty && !candidateResolutionBlocked;
  const savedReady = candidateId !== null && candidateRevision != null && candidateRevision >= 1 && candidateStatus === "working";
  const canSubscribe = !candidateResolutionBlocked && !dirty && (savedReady || unsavedCleanDraft) && problems.length === 0;
  // A custom draft's RSS test bench fetches source snapshots from the saved preset revision
  // (the URL is read by the server). It does not fetch when unsaved changes altered the source:
  // that snapshot would not belong to the current draft's source, and the bench asks for a save
  // first.
  const savedCustomSource = savedDraft?.rss_sources[0];
  const draftCustomSource = draft.rss_sources[0];
  const customSnapshot = useMemo(() => {
    if (entry.source !== "custom" || !savedReady || candidateId === null || candidateRevision == null) return undefined;
    if (!savedCustomSource || !draftCustomSource) return undefined;
    if (savedCustomSource.locator !== draftCustomSource.locator || savedCustomSource.group_name !== draftCustomSource.group_name) return undefined;
    return { candidate_id: candidateId, revision: candidateRevision, expected_version: candidateVersion };
  }, [entry.source, savedReady, candidateId, candidateRevision, candidateVersion, savedCustomSource, draftCustomSource]);

  // The combined read's gating is a pure client-side judgment: the RSS workspace handles its
  // own reading and pending, and the draft no longer fires an extra flat preview request (that
  // would evaluate the same content twice).
  const previewable = useMemo(() => draftPreviewable(draft), [draft]);

  useEffect(() => {
    onDirtyChange(dirty);
    return () => onDirtyChange(false);
  }, [dirty, onDirtyChange]);

  async function saveCandidate() {
    if (candidateResolutionBlocked || (candidateId !== null && candidateStatus !== "working")) {
      setError(t("config.draftNotEditable"));
      return;
    }
    setSaving(true);
    setError(undefined);
    setSaveConflict(undefined);
    try {
      if (candidateId) {
        const updated = await candidateStore.patch(candidateId, await freezeBusinessDraft(draft), candidateVersion);
        applyCandidate(updated);
        toast.success(t("config.saved"));
      } else {
        const created = await candidateStore.save(await freezeBusinessDraft(draft));
        applyCandidate(created);
        toast.success(t("config.saved"));
      }
    } catch (cause) {
      reportSaveError(cause);
    } finally {
      setSaving(false);
    }
  }

  function reportSaveError(cause: unknown) {
    if (cause instanceof CandidateVersionConflict) {
      setSaveConflict({ currentVersion: cause.currentVersion });
      setError(t("config.conflictOther"));
    } else if (cause instanceof CandidateWorkingDuplicate) {
      setSaveConflict({});
      setError(t("anime.draftExists"));
    } else {
      setError(toUserMessage(cause, t("config.saveFailed")));
    }
  }

  async function subscribe() {
    let targetId = candidateId;
    let targetVersion = candidateVersion;
    let targetRevision = candidateRevision;
    if (targetId === null) {
      // A clean unsaved draft: first create the candidate from the current content (the same write as "save configuration"), then activate it.
      if (!unsavedCleanDraft) {
        setError(t("config.noDraft"));
        return;
      }
      setSubscribing(true);
      setError(undefined);
      setSaveConflict(undefined);
      try {
        const created = await candidateStore.save(await freezeBusinessDraft(draft));
        applyCandidate(created);
        targetId = created.id;
        targetVersion = created.version;
        targetRevision = created.current_revision;
      } catch (cause) {
        reportSaveError(cause);
        setSubscribing(false);
        return;
      }
    }
    if (targetRevision == null || targetRevision < 1) {
      setSubscribing(false);
      setError(t("config.noDraft"));
      return;
    }
    setSubscribing(true);
    setError(undefined);
    try {
      // A stable idempotency key: retries after a network error or an interrupted response
      // reuse the same key and expected version, avoiding the duplicate activations per-click
      // UUIDs caused.
      if (!activationAttemptRef.current || activationAttemptRef.current.candidateId !== targetId || activationAttemptRef.current.initialState !== "active") {
        activationAttemptRef.current = {
          candidateId: targetId,
          expectedVersion: targetVersion,
          initialState: "active",
          idempotencyKey: newUUID(),
        };
      }
      const attempt = activationAttemptRef.current;
      const result = await candidateStore.subscribe(attempt.candidateId, attempt.expectedVersion, attempt.idempotencyKey, { revision: targetRevision });
      activationAttemptRef.current = null;
      announceSubscribed(result, entry.title, () => queryClient.invalidateQueries({ queryKey: catalogKeys.all }));
      onSubscribed();
    } catch (cause) {
      if (cause instanceof CandidateAlreadyActivated) {
        onCandidateStateChanged();
        setError(t("config.alreadySubscribed"));
      } else if (cause instanceof CandidateVersionConflict) {
        onCandidateStateChanged();
        setSaveConflict({ currentVersion: cause.currentVersion });
        setError(t("config.conflictSubscribe"));
      } else if (cause instanceof CandidateNotReady) {
        onCandidateStateChanged();
        setCandidateResolutionBlocked(true);
        setError(t("config.draftNotFound"));
      } else if (cause instanceof ApiError && cause.code === "IDEMPOTENCY_KEY_REUSED") {
        setError(t("config.opConflict"));
      } else if (cause instanceof ApiError && cause.code === "PROVIDER_ENTRY_ALREADY_SUBSCRIBED") {
        // Stale browse data: the entry already has a live subscription. Exactly one id opens it;
        // several (older duplicates) show the message.
        onCandidateStateChanged();
        const existing = (cause.details ?? []).flatMap((detail) => detail.managed_anime_ids ?? []);
        if (existing.length === 1 && onOpenExisting) onOpenExisting(existing[0]);
        else setError(toUserMessage(cause, t("config.subscribeFailed")));
      } else {
        setError(toUserMessage(cause, t("config.subscribeFailed")));
      }
    } finally {
      setSubscribing(false);
    }
  }

  if (!candidateLoaded) {
    return <div className="anime-config-dialog-loading"><StateBlock kind="busy" title={t("config.reloadingConfig")} /></div>;
  }

  const draftHost: DraftTabHost = {
    mode: "draft",
    draft,
    feedSnapshots,
    onFeedSnapshotsChange: setFeedSnapshots,
    observation,
    observedGroups,
    onDiscovered: (groups, next) => {
      setObservedGroups(groups);
      setObservation(next);
    },
    // The draft's own copy of the episode-versions policy plus unknown-episode handling (the
    // parser lives in the rules section "parsing", alongside the episode recognition rules).
    rulesExtra: (
      <>
        {draft.business && (
          <section aria-label={t("config.replacementAria")}>
            <BusinessSnapshotFields value={draft.business} onChange={(business) => setDraft({ ...draft, business })} />
          </section>
        )}
        <NewReleasesOnlyField
          checked={draft.new_releases_only === true}
          disabled={candidateResolutionBlocked}
          onChange={(next) => setDraft(setDraftNewReleasesOnly(draft, next))}
        />
      </>
    ),
    // Top right of the rules tab: the whole draft copies the default settings again. Unavailable when already equal to them.
    rulesHeaderAction: (
      <Button
        variant="ghost"
        size="sm"
        loading={resettingDefaults}
        disabled={!customized || saving || subscribing || candidateResolutionBlocked}
        title={customized ? undefined : t("config.useDefaultsNone")}
        onClick={() => void resetAllToDefaults()}
      >
        {t("config.useDefaults")}
      </Button>
    ),
    businessPreset: businessPreset.data,
    savedDraft,
    onDraftChange: candidateResolutionBlocked
      ? () => undefined
      : (next: AnimeDraft) => setDraft(next),
    previewable,
    // Provider entries: the group list can be refreshed proactively even unsubscribed (a
    // read-only discovery, the snapshot stays on the server, no source is created); custom has
    // no provider entry to discover. Both share the same RSS test bench workspace, with the
    // custom source snapshot fetched through customSnapshot.
    subgroupDiscovery:
      entry.source !== "custom"
        ? { provider: entry.source, providerEntryRef: entry.providerEntryRef }
        : undefined,
    customSnapshot,
  };

  return (
    <>
      <AnimeTabs
        host={draftHost}
        initialTab={initialTab}
        onTabChange={onTabChange}
        banner={
          <>
            {/* Keep the complete draft editor in the bounded scroll region.
                It must not consume the fixed tab/footer geometry on small screens. */}
            <WorkspaceSummaryRow state="draft" />
            {error && (
              <StateBlock
                kind="error"
                title={error}
                className="anime-config-draft-error"
                action={saveConflict ? (
                  <span className="anime-config-dialog-actions">
                    <Button disabled={conflictBusy} onClick={() => void reloadLatestCandidate()}>
                      {t("config.reloadLatest")}
                    </Button>
                    <Button
                      variant="primary"
                      disabled={conflictBusy || problems.length > 0}
                      onClick={() => void overwriteLatestCandidate()}
                    >
                      {t("config.overwrite")}
                    </Button>
                  </span>
                ) : candidateResolutionBlocked ? (
                  <Button disabled={conflictBusy} onClick={() => void reloadLatestCandidate()}>
                    {t("config.refreshReselect")}
                  </Button>
                ) : undefined}
              />
            )}
            {problems.length > 0 && (
              <StateBlock
                kind="error"
                title={t("config.problemsTitle")}
                description={
                  <ul className="anime-config-draft-problems">
                    {problems.map((problem) => (
                      <li key={`${problem.field}-${problem.message}`}>
                        {problem.message}
                      </li>
                    ))}
                  </ul>
                }
              />
            )}
          </>
        }
      />

      <div className="anime-config-dialog-footer anime-config-dialog-footer-draft">
        {dirty ? (
          <SaveStatus count={draftChangeCount(savedDraft, draft)} />
        ) : (
          <span className="anime-config-dialog-draft-status">
            {candidateId ? t("config.footerSaved") : t("config.footerUnsaved")}
          </span>
        )}
        <div className="anime-config-dialog-actions">
          <Button onClick={onRequestClose}>{t("act.close")}</Button>
          <Button
            disabled={saving || subscribing || (!dirty && candidateId !== null) || candidateResolutionBlocked || (candidateId !== null && candidateStatus !== "working")}
            title={
              candidateResolutionBlocked || (candidateId !== null && candidateStatus !== "working")
                ? t("config.saveTipBlocked")
                : dirty || candidateId === null
                  ? t("config.saveTipDirty")
                  : t("config.saveTipClean")
            }
            onClick={() => void saveCandidate()}
          >
            {saving ? t("act.saving") : t("config.saveButton")}
          </Button>
          <Button
            variant="primary"
            disabled={saving || subscribing || !canSubscribe}
            title={
              dirty
                ? t("config.subscribeTipSave")
                : problems.length > 0
                    ? t("config.subscribeTipFix")
                    : candidateId === null
                      ? t("config.subscribeTipReadyNew")
                      : t("config.subscribeTipReady")
            }
            onClick={() => void subscribe()}
          >
            <Play className="anime-config-subscribe-icon" />
            {subscribing ? t("anime.state.subscribing") : t("anime.subscribe")}
          </Button>
        </div>
      </div>
    </>
  );
}


// --- Header summary row (the overview face's content folded into the four-tab dialog, no separate panel) ---

// "Last updated" takes the newest seed's publish time from the read model; with no time fact at
// all it returns undefined and the cell shows the "—" placeholder. The contract has logs, but
// their semantics are the subscription operation ledger, not a source for "last updated".
function latestReleasePublishedAt(workspace: AnimeWorkspace): string | undefined {
  return workspace.releases
    .map((release) => release.raw_facts.published_at)
    .filter((value): value is string => value !== undefined)
    .sort()
    .at(-1);
}

// The compact info row: seeds collected · last updated. The subscription state lives only in
// the title pill, metadata links in the title area, and when blocked the whole row becomes a
// BlockingRow — so there is no state cell and no "metadata match" cell here anymore. A draft has
// no title pill, and its "not subscribed" state still sits on this row.
function WorkspaceSummaryRow({
  state,
  seedCount,
  latestAt,
}: {
  /** Only a draft passes "draft". */
  state?: "draft";
  seedCount?: number;
  latestAt?: string;
}) {
  return (
    <div
      role="status"
      aria-label={t("config.summary.aria")}
      className="anime-config-summary text-caption"
    >
      {state === "draft" && <SummaryCell label={t("config.summary.state")} value={t("anime.state.draft")} emphasize />}
      <SummaryCell
        label={t("config.summary.seeds")}
        value={seedCount === undefined ? "—" : String(seedCount)}
        title={seedCount === undefined ? t("config.summary.seedsTip") : undefined}
      />
      <SummaryCell
        label={t("config.summary.latest")}
        value={latestAt === undefined ? "—" : formatDateTime(latestAt)}
        title={latestAt === undefined ? t("config.summary.latestTip") : undefined}
      />
    </div>
  );
}

/** One line about the subscription state (not started / ended), optionally with a HelpTip; the state itself lives only in the title pill. */
function StateLine({ text, help }: { text: string; help?: ReactNode }) {
  return (
    <div role="status" className="anime-config-summary text-caption">
      <span className="tw:inline-flex tw:items-center tw:gap-1 tw:text-muted-foreground">
        {text}
        {help}
      </span>
    </div>
  );
}

/**
 * 自动化阻塞警告行：订阅中写长形（自动下载已暂停：未关联 TMDB），未开始/已停止本来就
 * 不下载，写短形（未关联 TMDB）；后面跟修复动作（关联 TMDB，打开元数据匹配弹窗）。
 * 等待配置的入库文件数跟在后面（· 分隔）；只有它时没有修复动作。
 */
function BlockingRow({
  phrases,
  waitingFiles,
  active,
  onFix,
}: {
  phrases: BlockingPhrases | undefined;
  waitingFiles: number;
  active: boolean;
  onFix: (link: "bgm" | "tmdb") => void;
}) {
  const parts = [
    ...(phrases ? [active ? phrases.long : phrases.short] : []),
    ...(waitingFiles > 0 ? [t("anime.configWaiting", { count: waitingFiles })] : []),
  ];
  const fix = phrases?.fix;
  return (
    <div role="status" className="anime-config-summary anime-config-blocking text-caption">
      <span className="tw:inline-flex tw:min-w-0 tw:items-center tw:gap-1.5 tw:text-warning tw:font-medium">
        <AlertTriangle aria-hidden className="tw:size-3.5 tw:shrink-0" />
        {parts.join(" · ")}
      </span>
      {fix && (
        <Button size="sm" variant="secondary" onClick={() => onFix(fix.link)}>
          {fix.label}
        </Button>
      )}
    </div>
  );
}

function SummaryCell({
  label,
  value,
  title,
  emphasize,
}: {
  label: string;
  value: string;
  title?: string;
  /** A subscription-state cell: the value is emphasized in the foreground color while other cells keep the default muted value. */
  emphasize?: boolean;
}) {
  return (
    <span className="anime-config-summary-cell" title={title}>
      <span className="anime-config-summary-label">{label}</span>
      <span className={emphasize ? "anime-config-summary-value-emphasized" : undefined}>{value}</span>
    </span>
  );
}
