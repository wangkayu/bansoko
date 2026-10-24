import { useEffect, useRef, useState } from "react";
import { toast } from "../../ui/toast";
import {
  ApiError,
  expandSnapshotSeed,
  toUserMessage,
  type AnimeDraft,
  type PreviewFeedSnapshotRef,
  type RSSListGroup,
  type RSSListItem,
  type RSSListObservationRef,
  type RSSListView,
} from "../../lib/api/client";
import { StateBlock, Button, Field, Input } from "../../ui";
import { Dialog, HelpTip } from "../../ui/base";
import { RssListViewPanel } from "./RssListViewPanel";
import { draftWorkspaceModel, matchDraftSourceIndex, type DraftExpandState } from "./draftModel";
import { reasonLabel } from "../../lib/i18n/reasons";
import { useRssListWorkspace, type CustomSnapshotSource, type OwnerBenchInput } from "./useRssListWorkspace";
import { t } from "../../lib/i18n";

export function RssWorkspace({
  provider,
  providerEntryRef,
  draft,
  ownerBench,
  customSource,
  enabled,
  feedSnapshots,
  onFeedSnapshotsChange,
  observation,
  autoLoad,
  onViewRead,
  onSnapshotsChanged,
  onDraftChange,
  help,
}: {
  /** A custom source (a draft or a not-yet-started subscription) has no provider entry; the snapshot is fetched via customSource. */
  provider: "anibt" | "mikan" | "custom";
  providerEntryRef?: string;
  customSource?: CustomSnapshotSource;
  /** The draft test bench's input; a not-yet-started subscription's test bench uses ownerBench instead (renders no offset entry). */
  draft?: AnimeDraft;
  ownerBench?: OwnerBenchInput;
  enabled: boolean;
  feedSnapshots: PreviewFeedSnapshotRef[];
  onFeedSnapshotsChange: (refs: PreviewFeedSnapshotRef[]) => void;
  observation?: RSSListObservationRef;
  /**
   * Auto reads when the hosting tab activates: a selected group's seeds are fetched and parsed by
   * default, rather than making the user click preview just because nothing is subscribed yet.
   * Each observation (first activation, then every new discovery) auto-reads once; a config change
   * only marks stale and re-previewing is explicit by the user — no live recomputation.
   */
  autoLoad?: boolean;
  onViewRead?: (view: RSSListView) => void;
  onSnapshotsChanged?: () => void;
  /** The in-memory draft write entry: draft-side offset edits update draft.rss_sources through it; when absent no offset entry renders. */
  onDraftChange?: (next: AnimeDraft) => void;
  /** The toolbar HelpTip's content; defaults to the draft's release note. */
  help?: string;
}) {
  const workspace = useRssListWorkspace({
    provider,
    providerEntryRef,
    draft,
    ownerBench,
    customSource,
    enabled,
    feedSnapshots,
    onFeedSnapshotsChange,
    observation,
    onViewRead,
  });
  const [showFiltered, setShowFiltered] = useState(false);
  // Auto reads happen only on two input facts: the tab's first activation, and a discovery that
//   brings a new observation. "Which observation was auto-read" is recorded, so one observation is
//   never read twice and a failure does not loop into retries; a config change only marks stale —
//   re-previewing is explicit by the user.
  const autoLoadedFor = useRef<string | null>(null);
  const { pending, preview } = workspace;
  const observationKey = observation ? String(observation.snapshot_version) : "none";
  useEffect(() => {
    if (!autoLoad || !enabled || pending || autoLoadedFor.current === observationKey) return;
    autoLoadedFor.current = observationKey;
    void preview();
  }, [autoLoad, enabled, pending, preview, observationKey]);
  // Expansion state is per seed: loading shows the work in flight, failed keeps the error so the
//   user can retry (a failure never writes the server cache; a retry is simply another expansion
//   call).
  const [expandStates, setExpandStates] = useState<Map<string, DraftExpandState>>(() => new Map());
  // After expansion the panel is asked to open this row: clicking expand or download both means wanting the file structure first.
  const [openedSeedKey, setOpenedSeedKey] = useState<string>();
  const stale = workspace.stale;
  // Draft-side offset editing, in dialog form (the owner side is a separate implementation, neither
//   affects the other). The target records the draft source's index, not the source object — on
//   save the source is taken from the latest draft prop, so an edit never writes back a stale copy
//   after the draft was updated elsewhere while editing.
  const [offsetTarget, setOffsetTarget] = useState<{ name: string; sourceIndex: number }>();
  const [offsetDraft, setOffsetDraft] = useState("");
  const [offsetError, setOffsetError] = useState<string>();

  function openOffsetDialog(group: RSSListGroup) {
    if (!draft) return;
    const index = matchDraftSourceIndex(draft, group);
    if (index === undefined) return;
    setOffsetTarget({ name: group.display_name, sourceIndex: index });
    setOffsetDraft(String(draft.rss_sources[index]?.episode_offset ?? 0));
    setOffsetError(undefined);
  }

  function closeOffsetDialog() {
    setOffsetTarget(undefined);
    setOffsetDraft("");
    setOffsetError(undefined);
  }

  // Saving writes only the in-memory draft (the episode_offset of the matching source in
//   draft.rss_sources), then re-reads the preview immediately with the new draft (the existing
//   draft change -> view re-read channel; the preview's draftOverride parameter exists exactly for
//   the gap where the parent has not yet passed back the new draft prop).
  async function saveOffset() {
    if (!offsetTarget) return;
    const trimmed = offsetDraft.trim();
    const offset = trimmed === "" ? Number.NaN : Number(trimmed);
    if (!Number.isInteger(offset) || offset < -1000 || offset > 1000) {
      setOffsetError(t("rss.offsetInvalid"));
      return;
    }
    const source = draft?.rss_sources[offsetTarget.sourceIndex];
    if (!draft || !source || !onDraftChange) {
      closeOffsetDialog();
      return;
    }
    const nextSources = draft.rss_sources.slice();
    nextSources[offsetTarget.sourceIndex] = { ...source, episode_offset: offset };
    const next = { ...draft, rss_sources: nextSources };
    onDraftChange(next);
    closeOffsetDialog();
    await workspace.preview(undefined, next);
  }
  function setExpandState(seedKey: string, state?: DraftExpandState) {
    setExpandStates((prev) => {
      const next = new Map(prev);
      if (state) next.set(seedKey, state);
      else next.delete(seedKey);
      return next;
    });
  }

  /**
   * 快照身份漂移（过期/已刷新）的既有处置：给出显式刷新动作，刷新该组后带新身份重读预览。
   */
  function offerSnapshotRefresh(group: RSSListGroup, cause: unknown) {
    toast.error(toUserMessage(cause, t("rss.expandFailed")), {
      action: {
        label: t("rss.refreshSeedsAction"),
        onClick: () => {
          void (async () => {
            // refreshGroup returns the refreshed identity list in full: re-read with the new identity
//             directly, never depending on the next render after the host's prop is passed back.
            const refreshed = await workspace.refreshGroup(group);
            if (!refreshed) return;
            onSnapshotsChanged?.();
            await workspace.preview(refreshed);
            toast.info(t("rss.seedsRefreshedRetry"));
          })();
        },
      },
    });
  }

  /**
   * 展开一份种子：服务端按快照发布的 locator 抓取一次并长期缓存，成功后重读视图——展开
   * 响应只有文件摘要，逐文件行与目标路径由读模型的投影给出。返回该种子是否已展开。
   */
  async function expandSeed(group: RSSListGroup, item: RSSListItem): Promise<boolean> {
    const seedKey = `${item.snapshot_id}:${item.stable_seed_key}`;
    // An already-expanded row needs no fetch: treat it as success and let the caller focus on opening the row.
    if (item.torrent.state !== "pending") return true;
    if (expandStates.get(seedKey)?.state === "loading") return false;
    setExpandState(seedKey, { state: "loading" });
    // A failure still opens the row: the error and retry entry live in it, and a collapsed row is equivalent to no feedback at all.
    const fail = (message: string) => {
      setExpandState(seedKey, { state: "failed", error: message });
      setOpenedSeedKey(seedKey);
    };
    try {
      const result = await expandSnapshotSeed(item.snapshot_id, item.stable_seed_key, {
        provider,
        provider_entry_ref: group.scope.provider_entry_ref,
        provider_group_ref: group.scope.provider_group_ref,
        snapshot_version: group.feed.snapshot_version ?? 0,
        content_hash: group.feed.content_hash ?? "",
      });
      if (result.state !== "available") {
        // A fetch/parse failure is a state, not an exception: the row still counts as unexpanded and the error stays shown in it.
        fail(reasonLabel(result.error_code) ?? t("rss.expandFailed"));
        return false;
      }
      setExpandState(seedKey);
      await workspace.preview();
      setOpenedSeedKey(seedKey);
      return true;
    } catch (cause) {
      fail(toUserMessage(cause, t("rss.expandFailed")));
      if (cause instanceof ApiError && cause.code === "FEED_SNAPSHOT_REFRESH_REQUIRED") offerSnapshotRefresh(group, cause);
      return false;
    }
  }

  return (
    <section aria-label={t("rss.listAria")} className="seed-preview">
      <div className="rss-draft-toolbar">
        <HelpTip label={t("rss.workspaceHelpLabel")}>{help ?? t("rss.workspaceHint")}</HelpTip>
        <div className="rss-draft-actions">
          <Button size="sm" disabled={!enabled || workspace.pending} onClick={() => void workspace.preview()}>
            {workspace.pending ? t("rss.previewing") : t("rss.preview")}
          </Button>
          <Button size="sm" variant="secondary" onClick={() => setShowFiltered((prev) => !prev)}>
            {showFiltered ? t("rss.hideFiltered") : t("rss.showFiltered")}
          </Button>
        </div>
      </div>
      {stale && workspace.view && (
        <p role="status" className="rss-draft-hint text-caption" data-config-identity={workspace.view.config_identity}>
          {t("rss.configChanged")}
        </p>
      )}
      {enabled && workspace.view?.observation?.state === "not_retained" && (
        <p role="status" className="rss-draft-hint text-caption">
          {t("rss.groupsGone")}
        </p>
      )}
      {!enabled && <StateBlock kind="empty" title={t("rss.needTitle")} description={t("rss.needTitleHint")} />}
      {enabled && workspace.refreshError && <StateBlock kind="error" title={workspace.refreshError} />}
      {enabled && workspace.error && <StateBlock kind="error" title={workspace.error} />}
      {enabled && workspace.pending && !workspace.view && <StateBlock kind="busy" title={t("rss.firstLoad")} />}
      {enabled && workspace.view && (
        <RssListViewPanel
          model={draftWorkspaceModel(workspace.view, {
            stale,
            expandStates,
            openedSeedKey,
            onExpand: (item) => {
              const group = workspace.view?.groups.find((candidate) =>
                candidate.items.some((row) => row.snapshot_id === item.snapshot_id && row.stable_seed_key === item.stable_seed_key),
              );
              if (group) void expandSeed(group, item);
            },
            onRefreshGroup: (group) => {
              // A refresh wins only that group's new seed-list identity; re-reading without it leaves the
//               view at the pre-refresh result, plus one "config changed" line. On success re-read
//               with the new identity at once; the failure surfaces through refreshError.
              void (async () => {
                const refreshed = await workspace.refreshGroup(group);
                if (!refreshed) return;
                onSnapshotsChanged?.();
                await workspace.preview(refreshed);
              })();
            },
            refreshingGroup: workspace.refreshingGroup,
            draftOffsetFor: onDraftChange && draft
              ? (group) => {
                  const index = matchDraftSourceIndex(draft, group);
                  return index === undefined ? undefined : (draft.rss_sources[index]?.episode_offset ?? 0);
                }
              : undefined,
            onEditOffset: onDraftChange && draft ? openOffsetDialog : undefined,
          })}
          showFiltered={showFiltered}
        />
      )}
      <Dialog
        open={offsetTarget !== undefined}
        onRequestClose={closeOffsetDialog}
        tier="S"
        title={offsetTarget ? t("rss.offsetTitle", { name: offsetTarget.name }) : t("rss.offsetTitleGeneric")}
        footer={
          <>
            <Button onClick={closeOffsetDialog}>{t("act.cancel")}</Button>
            <Button variant="primary" onClick={() => void saveOffset()}>
              {t("rss.saveOffset")}
            </Button>
          </>
        }
      >
        {offsetTarget && (
          <div className="offset-modal-stack">
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
            {offsetError && (
              <StateBlock kind="error" title={offsetError} />
            )}
          </div>
        )}
      </Dialog>
    </section>
  );
}
