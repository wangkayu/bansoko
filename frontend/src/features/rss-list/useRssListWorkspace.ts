import { useCallback, useMemo, useRef, useState } from "react";
import {
  ApiError,
  previewProviderEntryRSS,
  readAnimeRSSListView,
  readProviderEntryRSS,
  toUserMessage,
  type AnimeDraft,
  type AnimeMetadataPreview,
  type PreviewFeedSnapshotRef,
  type ProviderRSSPreviewResult,
  type RSSListGroup,
  type RSSListObservationRef,
  type RSSListView,
  type RSSListViewInput,
  type RulesSections
} from "../../lib/api/client";
import { t } from "../../lib/i18n";

/** The contract requires every read to carry the subgroup cap; owner mode is the only consumer, current_input passes only the value. */
const OWNER_ITEMS_PER_GROUP = 100;

export type RssListWorkspace = {
  view?: RSSListView;
  viewDraftIdentity?: string;
  pending: boolean;
  error?: string;
  refreshAllBusy: boolean;
  refreshingGroup?: string;
  refreshError?: string;
  /** The inputs (draft/observation/snapshot) the read view was built on no longer match the current ones. */
  stale: boolean;
  /** draftOverride: when the caller just wrote the in-memory draft and the parent has not yet passed the new draft prop back,
      直接带新草稿重读（草稿面偏移编辑走这条通道）。
      offsetOverride：预览专用的集数偏移（一次性下载的偏移在草稿源之外，只作用于
      这次读取，因此单独传）。 */
  preview: (feedOverride?: PreviewFeedSnapshotRef[], draftOverride?: AnimeDraft, offsetOverride?: number) => Promise<void>;
  /** On success returns the refreshed complete snapshot identity list for an immediate re-read with the new identities; null on failure. */
  refreshGroup: (group: RSSListGroup) => Promise<PreviewFeedSnapshotRef[] | null>;
};

/**
 * 一次读取的输入身份：草稿 + 它所用的观察 + 各组的 feed 快照身份 + 预览偏移。
 * 任何一项变化都使已读取的视图过时：重新发现、刷新某组种子、改一次性的集数
 * 偏移都改变视图的输入，所以过时判断不能只比较草稿。快照身份按组排序，顺序
 * 不影响结果。
 */
export function rssViewIdentity(
  draft: unknown,
  observation: RSSListObservationRef | undefined,
  feed: PreviewFeedSnapshotRef[],
  episodeOffset = 0,
): string {
  const snapshots = feed
    .map((ref) => `${ref.provider}:${ref.provider_entry_ref}:${ref.provider_group_ref}:${ref.snapshot_id}:${ref.snapshot_version}`)
    .sort();
  return JSON.stringify([draft, observation ?? null, snapshots, episodeOffset]);
}

function groupKeyOf(group: RSSListGroup): string {
  return `${group.scope.provider}:${group.scope.provider_entry_ref}:${group.scope.provider_group_ref}`;
}

/** Whether a snapshot identity is complete enough to read the read model. */
function hasSnapshotIdentity(result: ProviderRSSPreviewResult | null | undefined): boolean {
  return Boolean(result && result.snapshot_id && result.snapshot_version && result.content_hash);
}

/**
 * 持久化快照能否直接当这次读取的身份：身份完整且仍在新鲜窗口内。
 * 过期快照按契约要触发一次实时抓取；把它当身份交给读模型只会 fail closed
 * （FEED_SNAPSHOT_REFRESH_REQUIRED），看不到任何种子。
 */
function snapshotUsable(result: ProviderRSSPreviewResult | null | undefined): boolean {
  return hasSnapshotIdentity(result) && result!.freshness === "fresh";
}

/**
 * 未开始订阅的测试台输入：服务端按实例当前配置（加上弹窗里未保存的规则与元数据修改）组合
 * 与草稿测试台同一个读模型。identity 是这份配置的身份（版本 + 未保存修改），它
 * 变化时已读视图过时。
 */
export type OwnerBenchInput = {
  managedAnimeID: string;
  rulesPreview?: RulesSections;
  /** The metadata tab's complete unsaved metadata (a read-only owner_draft overlay, never written). */
  metadataPreview?: AnimeMetadataPreview;
  identity: string;
};

/**
 * custom 来源测试台的源快照：来源 URL 只在服务端读（预设或订阅保存的来源），前端只给
 * 抓取入口与作用域。草稿测试台与未开始订阅的测试台共用这一份描述，区别只在抓取的
 * 主体（预设修订 / 订阅）。
 */
export type CustomSnapshotSource = {
  /** Fetches the saved source's snapshot; omitted means no saved source is fetchable yet (e.g. the draft is unsaved). */
  fetch?: () => Promise<ProviderRSSPreviewResult>;
  /** The draft read model's snapshot scope: the saved preset's revision (an owner bench's is decided by the server per subscription; omit there). */
  scope?: { candidate_id: string; revision: number };
};

export function useRssListWorkspace({
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
  episodeOffset = 0,
  oneShot = false,
}: {
  /** This workspace's source. Custom has no provider entry; its snapshot is fetched through customSource. */
  provider: "anibt" | "mikan" | "custom";
  /** The provider source's entry; omitted for custom. */
  providerEntryRef?: string;
  /** The draft bench's input; unused while ownerBench exists. */
  draft?: AnimeDraft;
  /** A not-yet-started subscription's bench: reads per instance; omitted for drafts. */
  ownerBench?: OwnerBenchInput;
  /** A custom source's (draft or not-yet-started subscription) source-snapshot fetch and scope. */
  customSource?: CustomSnapshotSource;
  enabled: boolean;
  feedSnapshots: PreviewFeedSnapshotRef[];
  onFeedSnapshotsChange: (refs: PreviewFeedSnapshotRef[]) => void;
  /** The latest subgroup discovery's observation identity; when present the server lists the observed groups alongside. */
  observation?: RSSListObservationRef;
  onViewRead?: (view: RSSListView) => void;
  /** A preview-only episode offset (a one-shot download's offset belongs to no draft source). */
  episodeOffset?: number;
  /**
   * 一次性浏览：服务端不按字幕组选择限制列出的组，每个组都按 episode_offset 预览。
   * 只有一次性下载弹窗使用；订阅前的测试台保持订阅语义。
   */
  oneShot?: boolean;
}): RssListWorkspace {
  const [view, setView] = useState<RSSListView>();
  const [viewDraftIdentity, setViewDraftIdentity] = useState<string>();
  const [pending, setPending] = useState(false);
  const readSeq = useRef(0);
  const [error, setError] = useState<string>();
  const [refreshError, setRefreshError] = useState<string>();
  const [refreshingGroup, setRefreshingGroup] = useState<string>();
  // This workspace's snapshot identities: provider keys by entry; the custom bench has only its own single source.
  const ownsRef = useCallback(
    (ref: PreviewFeedSnapshotRef) => ref.provider === provider && (provider === "custom" || ref.provider_entry_ref === providerEntryRef),
    [provider, providerEntryRef],
  );
  const identities = useMemo(() => feedSnapshots.filter(ownsRef), [feedSnapshots, ownsRef]);

  /** Fetches one group's snapshot: custom fetches the saved source (URL read by the server), provider takes the live preview. */
  const fetchGroupSnapshot = useCallback((group: RSSListGroup): Promise<ProviderRSSPreviewResult> | null => {
    if (group.scope.provider === "custom") return customSource?.fetch?.() ?? null;
    if (provider === "custom") return null;
    return previewProviderEntryRSS(provider, group.scope.provider_entry_ref, { group_ref: group.scope.provider_group_ref });
  }, [customSource, provider]);
  const customScope = customSource?.scope;
  /** The read's input identity: the draft (or the owner's configuration identity) plus the custom snapshot scope. */
  const inputKeyOf = useCallback(
    (inputDraft: unknown) => (customScope ? [inputDraft, customScope] : inputDraft),
    [customScope],
  );

  const publishIdentity = useCallback((result: ProviderRSSPreviewResult, base: PreviewFeedSnapshotRef[]) => {
    if (!result.snapshot_id || !result.snapshot_version || !result.content_hash) return base;
    const ref: PreviewFeedSnapshotRef = {
      provider: result.provider,
      provider_entry_ref: result.provider_entry_ref,
      provider_group_ref: result.group_ref,
      snapshot_id: result.snapshot_id,
      snapshot_version: result.snapshot_version,
      content_hash: result.content_hash,
    };
    const next = base.filter((existing) =>
      existing.provider !== ref.provider || existing.provider_entry_ref !== ref.provider_entry_ref || existing.provider_group_ref !== ref.provider_group_ref,
    );
    next.push(ref);
    onFeedSnapshotsChange(next);
    return next;
  }, [onFeedSnapshotsChange]);

  const preview = useCallback(async (feedOverride?: PreviewFeedSnapshotRef[], draftOverride?: AnimeDraft, offsetOverride?: number) => {
    if (!enabled) return;
    // Reads land by sequence: continuous config adjustments cause concurrent re-reads and only
//   the last result counts. New input must never be dropped because a previous read is still in
//   flight — that would leave the UI stuck on the old configuration's rendering.
    const seq = ++readSeq.current;
    setPending(true);
    setError(undefined);
    const previewOffset = offsetOverride ?? episodeOffset;
    try {
      const inputDraft = ownerBench ? undefined : structuredClone(draftOverride ?? draft);
      const inputKey = inputKeyOf(ownerBench ? ownerBench.identity : inputDraft);
      const ownerRequest = (feed: PreviewFeedSnapshotRef[]): RSSListViewInput => ({
        input_source: ownerBench?.rulesPreview || ownerBench?.metadataPreview ? "owner_draft" : "owner",
        managed_anime_id: ownerBench!.managedAnimeID,
        ...(ownerBench?.rulesPreview ? { rules_preview: ownerBench.rulesPreview } : {}),
        ...(ownerBench?.metadataPreview ? { metadata_preview: ownerBench.metadataPreview } : {}),
        feed,
        limit_per_group: OWNER_ITEMS_PER_GROUP,
      });
      // A one-shot download's offset belongs to no draft source, and the read model adopts it only
//   when the group has none of its own; it is preview input, not a write-back, so the draft is
//   untouched.
      const readWith = (feed: PreviewFeedSnapshotRef[]) =>
        ownerBench ? readAnimeRSSListView(ownerRequest(feed)) : readAnimeRSSListView({
          input_source: "current_input",
          draft: inputDraft,
          ...(customScope ? customScope : {}),
          feed,
          ...(observation ? { observation } : {}),
          ...(previewOffset !== 0 ? { episode_offset: previewOffset } : {}),
          ...(oneShot ? { one_shot: true } : {}),
          // The owner mode's subgroup cap; current_input does not use it, but the contract requires it.
          limit_per_group: OWNER_ITEMS_PER_GROUP,
        });
      let finalFeed = feedOverride ?? identities;
      let result: RSSListView;
      try {
        result = await readWith(finalFeed);
      } catch (cause) {
        // Snapshot identities kept locally may have gone stale. The read model fails closed
//   page-wide on a stale identity with only a "refresh the seed list first" note, and the
//   workspace has no entry that could refresh it anyway. Dropping this workspace's identities and
//   re-reading once makes the read model report the selected groups as snapshot_required, and only
//   then does the backfill logic below take over (a fresh snapshot reads purely; a stale or
//   missing one actually fetches the provider).
        // A custom source's snapshot scope follows its saved source and revision: when old
//   identities no longer belong to it, drop and re-read the same way, then fetch on demand.
        const stale = cause instanceof ApiError && (
          cause.code === "FEED_SNAPSHOT_REFRESH_REQUIRED" ||
          (provider === "custom" && cause.code === "FEED_SNAPSHOT_IDENTITY_INVALID")
        );
        if (!stale) throw cause;
        finalFeed = finalFeed.filter((ref) => !ownsRef(ref));
        result = await readWith(finalFeed);
      }
      if (result === null || typeof result !== "object" || !Array.isArray(result.groups)) {
        throw new Error(t("rss.listBadFormat"));
      }
      const required = result.groups.filter((group) => group.feed.state === "snapshot_required");
      if (required.length > 0) {
        let updated = finalFeed;
        for (const group of required) {
          // Only this workspace's source groups are backfilled: a provider workspace backfills
//   provider groups, a custom bench its own custom group (staying snapshot-required when there is
//   no fetchable saved source).
          if (group.scope.provider !== provider) continue;
          let resolved: ProviderRSSPreviewResult | null;
          if (provider === "custom") {
            resolved = await fetchGroupSnapshot(group);
          } else {
            // A persisted snapshot identity wins: fresh means a pure read (no network); only a
//   missing or expired one fetches the provider. Reopening the workspace relies entirely on this
//   path for identity backfill.
            const persisted = await readProviderEntryRSS(provider, group.scope.provider_entry_ref, group.scope.provider_group_ref);
            resolved = snapshotUsable(persisted) ? persisted : await fetchGroupSnapshot(group);
          }
          if (resolved) updated = publishIdentity(resolved, updated);
        }
        // With no identities backfilled (e.g. a custom draft with no fetchable saved source yet) no re-read is needed.
        if (updated !== finalFeed) {
          finalFeed = updated;
          result = await readWith(finalFeed);
          if (result === null || typeof result !== "object" || !Array.isArray(result.groups)) {
            throw new Error(t("rss.listBadFormat"));
          }
        }
      }
      if (seq !== readSeq.current) return;
      setView(result);
      // The snapshot identities are the set this read actually ended up using (possibly backfilled mid-read).
      setViewDraftIdentity(rssViewIdentity(inputKey, observation, finalFeed, previewOffset));
      onViewRead?.(result);
    } catch (cause) {
      if (seq === readSeq.current) setError(toUserMessage(cause, t("rss.listFailed")));
    } finally {
      if (seq === readSeq.current) setPending(false);
    }
  }, [draft, ownerBench, enabled, episodeOffset, identities, observation, oneShot, onViewRead, provider, ownsRef, fetchGroupSnapshot, publishIdentity, customScope, inputKeyOf]);

  const refreshGroup = useCallback(async (group: RSSListGroup): Promise<PreviewFeedSnapshotRef[] | null> => {
    const key = groupKeyOf(group);
    // A single-group refresh only makes sense for this workspace's source group.
    if (group.scope.provider !== provider) return null;
    const fetching = fetchGroupSnapshot(group);
    if (!fetching) return null;
    setRefreshingGroup(key);
    setRefreshError(undefined);
    try {
      return publishIdentity(await fetching, feedSnapshots);
    } catch (cause) {
      setRefreshError(toUserMessage(cause, t("rss.refreshSeedsFailed")));
      return null;
    } finally {
      setRefreshingGroup(undefined);
    }
  }, [feedSnapshots, provider, fetchGroupSnapshot, publishIdentity]);
  const stale = viewDraftIdentity !== undefined && viewDraftIdentity !== rssViewIdentity(inputKeyOf(ownerBench ? ownerBench.identity : draft), observation, identities, episodeOffset);
  return { view, viewDraftIdentity, stale, pending, error, refreshAllBusy: false, refreshingGroup, refreshError, preview, refreshGroup };
}
