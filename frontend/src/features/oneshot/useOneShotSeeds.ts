import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  ApiError,
  discoverProviderEntryGroups,
  expandSnapshotSeed,
  fetchCustomSnapshot,
  getAnimeConfigCandidate,
  getBusinessPreset,
  previewProviderEntryRSS,
  readAnimeRSSListView,
  readProviderEntryRSS,
  toUserMessage,
  type AnimeConfigCandidate,
  type AnimeDraft,
  type PreviewFeedSnapshotRef,
  type ProviderRSSPreviewResult,
  type RSSListObservationRef,
  type RSSListView,
} from "../../lib/api/client";
import { configKeys } from "../../lib/queryKeys";
import type { CatalogEntryView } from "../../lib/providers/viewmodels";
import type { CustomSnapshotRequest } from "../catalog/CustomSnapshotFetchButton";
import { draftFromEntry } from "../catalog/entryDraft";
import { withGlobalBusiness } from "../anime-config/businessDraft";
import { latestPublishedAt, seedGroupsOf, type OneShotSeedGroup, type OneShotSeedRow } from "./seedRows";
import { reasonLabel } from "../../lib/i18n/reasons";
import { t } from "../../lib/i18n";

// Where "browse seeds" gets its seeds. A one-shot task names its seeds directly and is not limited
//   by subgroup selection: both paths use the one-shot browse read (one_shot).
//   - provider entries (anibt/mikan): discover subgroups first and list only the groups; only an
//     entered group fetches its seed list (a fresh seed list is reused as-is, otherwise fetched
//     once), then the read model is read over the entered groups. No group's seeds are fetched up
//     front.
//   - custom preset: a single seed list with no subgroup layer; an explicit fetch, then read
//     directly. The configuration is this task's own draft (loaded from the preset; edits never
//     write back to it).
//
// No business rule is judged here: downloadability, filter outcomes and target paths all come from the server's read model.

/** An entry in step one's subgroup list: the discovery names it; only an entered group knows its seed count and newest time. */
export type OneShotGroupEntry = {
  key: string;
  name: string;
  /** The seed count of a group that was entered and read; unknown before that. */
  count?: number;
  /** The newest release time among an entered group's seeds; omitted while unknown. */
  latestAt?: string;
};

export type OneShotSeedSource = {
  kind: "provider" | "custom";
  /** This task's configuration: the loaded draft (provider comes from the global config, custom from the preset). */
  draft: AnimeDraft | undefined;
  /** The write entry for this task's draft. Changes only this task, never a subscription or a preset. */
  setDraft: (next: AnimeDraft) => void;
  /** The episode offset at task start (custom takes the preset source's offset, provider is 0). */
  initialOffset: number;
  /** Step one's subgroup list (custom has no such layer; empty). */
  groupEntries: OneShotGroupEntry[];
  /** Groups whose seeds have been read (the entered groups; for custom it is the single seed list). */
  groups: OneShotSeedGroup[];
  /** First-time preparation (load config + discover subgroups) in flight. */
  loading: boolean;
  /** A read-model re-read in flight. */
  pending: boolean;
  error?: string;
  /** The group whose seed list is being fetched. */
  openingGroup?: string;
  /** Entering a group failed: the group and the reason. */
  openError?: { key: string; message: string };
  /** Enters a group: fetch its seed list and re-read if never fetched; an already-fetched one is directly usable. */
  openGroup: (key: string, draft: AnimeDraft | undefined, offset: number) => Promise<void>;
  refreshingGroup?: string;
  refreshError?: string;
  /** Refreshes a single group: re-fetches that group's seed list and returns whether the read back succeeded. */
  refreshGroup: (group: OneShotSeedGroup, draft: AnimeDraft | undefined, offset: number) => Promise<boolean>;
  /**
   * 展开一个尚未展开的种子：只取这颗种子的字节，不重读视图。文件行属于读模型投影，
   * 由调用方随后带着它编辑中的草稿重读。失败返回有界原因文案，成功返回空对象。
   */
  expand: (row: OneShotSeedRow) => Promise<{ error?: string }>;
  /** Re-reads the read model with this task's draft and episode offset. */
  preview: (draft: AnimeDraft | undefined, offset: number) => Promise<void>;
  /** custom: no usable seed-list snapshot yet; the user must fetch explicitly. */
  fetchRequired: boolean;
  fetchBusy: boolean;
  fetchSeeds?: () => void;
  /** Restarts the whole read path after a failure. */
  reload: () => void;
};

/**
 * 一次性下载入口的种子来源。两条路径的 hook 都被调用（React 规则），由 `enabled`
 * 让未选中的那条不做任何事。`entry` 变化时弹窗以 `key` 重挂载。
 */
export function useOneShotSeedSource({ entry, enabled }: { entry: CatalogEntryView; enabled: boolean }): OneShotSeedSource {
  const isProvider = entry.source === "anibt" || entry.source === "mikan";
  const provider = useProviderSeedSource({ entry, enabled: enabled && isProvider });
  const custom = useCustomSeedSource({ entry, enabled: enabled && !isProvider });
  return isProvider ? provider : custom;
}

/** The contract requires every read to carry the group cap; one-shot browsing is not rate-limited per group. */
const ITEMS_PER_GROUP = 100;

/**
 * 展开一个未展开的种子。请求只带快照身份，locator 由服务端从快照读取，因此前端既不
 * 参与取数也不参与判定。返回为空表示已可用。
 */
async function expandSeed(row: OneShotSeedRow, provider: "anibt" | "mikan" | "custom") {
  try {
    const result = await expandSnapshotSeed(row.snapshotID, row.stableSeedKey, {
      provider,
      provider_entry_ref: row.scope.provider_entry_ref,
      provider_group_ref: row.scope.provider_group_ref,
      snapshot_version: row.feed.snapshot_version ?? 0,
      content_hash: row.feed.content_hash ?? "",
    });
    // Failing to fetch/parse is an expected expansion outcome (a state, not an exception): hand the bounded reason to the caller to display.
    if (result.state !== "available") return { error: reasonLabel(result.error_code) ?? t("rss.expandFailed") };
    return {};
  } catch (cause) {
    return { error: toUserMessage(cause, t("rss.expandFailed")) };
  }
}

// --- provider: discover subgroups + per-group seed lists ------------------------------

/** Whether a seed list can serve as the read identity directly: identity complete and still inside the freshness window. A stale one is rejected by the read model. */
function usableSnapshot(result: ProviderRSSPreviewResult): boolean {
  return Boolean(result.snapshot_id && result.snapshot_version && result.content_hash && result.freshness === "fresh");
}

function feedRefOf(result: ProviderRSSPreviewResult): PreviewFeedSnapshotRef {
  if (!result.snapshot_id || !result.snapshot_version || !result.content_hash) throw new Error(t("oneshot.loadSeedsFailed"));
  return {
    provider: result.provider,
    provider_entry_ref: result.provider_entry_ref,
    provider_group_ref: result.group_ref,
    snapshot_id: result.snapshot_id,
    snapshot_version: result.snapshot_version,
    content_hash: result.content_hash,
  };
}

type DiscoveredGroup = { key: string; groupRef: string; name: string };

function useProviderSeedSource({ entry, enabled }: { entry: CatalogEntryView; enabled: boolean }): OneShotSeedSource {
  const provider: "anibt" | "mikan" = entry.source === "mikan" ? "mikan" : "anibt";
  const providerEntryRef = entry.providerEntryRef;
  const queryClient = useQueryClient();
  // The task draft is taken once: a one-shot task loads the global config directly (it inherits no subscription configuration).
  const seededRef = useRef<AnimeDraft>(draftFromEntry(entry));
  const [draft, setDraft] = useState<AnimeDraft>(seededRef.current);
  /** Whether the draft carried in the Global business snapshot; both reading and creating the task require it non-empty (the server fails closed). */
  const [businessReady, setBusinessReady] = useState(false);
  const [observation, setObservation] = useState<RSSListObservationRef>();
  const [discovered, setDiscovered] = useState<DiscoveredGroup[]>([]);
  const [discovering, setDiscovering] = useState(false);
  const [discoveryError, setDiscoveryError] = useState<string>();
  const [attempt, setAttempt] = useState(0);

  // The Global business snapshot goes through the shared query cache: once read in this session no further request is sent.
  useEffect(() => {
    if (!enabled) return;
    let active = true;
    setBusinessReady(false);
    setDiscoveryError(undefined);
    // The rules section assembled into a full copy of the default settings.
    void withGlobalBusiness(
      seededRef.current,
      queryClient.fetchQuery({ queryKey: configKeys.businessPreset, queryFn: getBusinessPreset }),
    )
      .then((loaded) => {
        if (!active) return;
        setDraft(loaded);
        setBusinessReady(true);
      })
      .catch((cause) => {
        if (active) setDiscoveryError(toUserMessage(cause, t("oneshot.loadSeedsFailed")));
      });
    return () => {
      active = false;
    };
  }, [attempt, enabled, queryClient]);

  // Entering discovers the subgroups at once: step one lists the groups and fetches no group's seeds.
  useEffect(() => {
    if (!enabled) return;
    let active = true;
    setDiscovering(true);
    setDiscoveryError(undefined);
    setObservation(undefined);
    setDiscovered([]);
    void discoverProviderEntryGroups(provider, providerEntryRef)
      .then((result) => {
        if (!active) return;
        setObservation({
          provider: result.provider,
          provider_entry_ref: result.provider_entry_ref,
          snapshot_version: result.snapshot_version,
        });
        setDiscovered(
          result.groups.map((group) => ({
            key: `${provider}:${providerEntryRef}:${group.group_ref}`,
            groupRef: group.group_ref,
            name: group.display_name,
          })),
        );
      })
      .catch((cause) => {
        if (active) setDiscoveryError(toUserMessage(cause, t("oneshot.loadSeedsFailed")));
      })
      .finally(() => {
        if (active) setDiscovering(false);
      });
    return () => {
      active = false;
    };
  }, [attempt, enabled, provider, providerEntryRef]);

  // Seed-list identities of the entered groups: reads carry only these groups, never the others.
  const refsRef = useRef(new Map<string, PreviewFeedSnapshotRef>());
  const [view, setView] = useState<RSSListView>();
  const [pending, setPending] = useState(false);
  const [viewError, setViewError] = useState<string>();
  const [openingGroup, setOpeningGroup] = useState<string>();
  const [openError, setOpenError] = useState<{ key: string; message: string }>();
  const [refreshingGroup, setRefreshingGroup] = useState<string>();
  const [refreshError, setRefreshError] = useState<string>();
  const readSeq = useRef(0);

  // After a re-discovery the old identities no longer belong to the same observation.
  useEffect(() => {
    refsRef.current = new Map();
    setView(undefined);
    setViewError(undefined);
  }, [attempt]);

  /** Reads once over the entered groups; of consecutive re-reads only the last one counts. */
  const read = useCallback(
    async (current: AnimeDraft | undefined, offset: number) => {
      if (!current || !observation || refsRef.current.size === 0) return;
      const seq = ++readSeq.current;
      setPending(true);
      setViewError(undefined);
      const request = () =>
        readAnimeRSSListView({
          input_source: "current_input",
          draft: structuredClone(current),
          feed: [...refsRef.current.values()],
          observation,
          one_shot: true,
          limit_per_group: ITEMS_PER_GROUP,
          ...(offset !== 0 ? { episode_offset: offset } : {}),
        });
      try {
        let result: RSSListView;
        try {
          result = await request();
        } catch (cause) {
          // One of the entered groups' seed lists went stale meanwhile: the read model rejects the
//           whole page. Re-fetch those groups' seed lists (entered groups only), then read again.
          if (!(cause instanceof ApiError) || cause.code !== "FEED_SNAPSHOT_REFRESH_REQUIRED") throw cause;
          for (const ref of [...refsRef.current.values()]) {
            const fresh = await previewProviderEntryRSS(provider, ref.provider_entry_ref, { group_ref: ref.provider_group_ref });
            refsRef.current.set(`${ref.provider}:${ref.provider_entry_ref}:${ref.provider_group_ref}`, feedRefOf(fresh));
          }
          result = await request();
        }
        if (result === null || typeof result !== "object" || !Array.isArray(result.groups)) throw new Error(t("rss.listBadFormat"));
        if (seq === readSeq.current) setView(result);
      } catch (cause) {
        if (seq === readSeq.current) setViewError(toUserMessage(cause, t("oneshot.previewFailed")));
      } finally {
        if (seq === readSeq.current) setPending(false);
      }
    },
    [observation, provider],
  );

  // The read model lists every observed group; only entered groups carry seeds and the others never expand in step one.
  const groups = useMemo(
    () => seedGroupsOf(view).filter((group) => refsRef.current.has(group.key)),
    [view],
  );
  const openGroup = useCallback(
    async (key: string, current: AnimeDraft | undefined, offset: number) => {
      const group = discovered.find((candidate) => candidate.key === key);
      if (!group) return;
      if (refsRef.current.has(key)) {
        // Already fetched: re-read only when the last read did not succeed.
        if (!groups.some((candidate) => candidate.key === key)) await read(current, offset);
        return;
      }
      setOpeningGroup(key);
      setOpenError(undefined);
      try {
        // A fresh seed list is used directly (no network); the group is fetched only when there is none or it is stale.
        const persisted = await readProviderEntryRSS(provider, providerEntryRef, group.groupRef);
        const resolved = usableSnapshot(persisted)
          ? persisted
          : await previewProviderEntryRSS(provider, providerEntryRef, { group_ref: group.groupRef });
        refsRef.current.set(key, feedRefOf(resolved));
      } catch (cause) {
        setOpenError({ key, message: toUserMessage(cause, t("oneshot.loadSeedsFailed")) });
        return;
      } finally {
        setOpeningGroup(undefined);
      }
      await read(current, offset);
    },
    [discovered, groups, provider, providerEntryRef, read],
  );

  const refreshGroup = useCallback(
    async (group: OneShotSeedGroup, current: AnimeDraft | undefined, offset: number) => {
      setRefreshingGroup(group.key);
      setRefreshError(undefined);
      try {
        const refreshed = await previewProviderEntryRSS(provider, group.scope.provider_entry_ref, {
          group_ref: group.scope.provider_group_ref,
        });
        refsRef.current.set(group.key, feedRefOf(refreshed));
      } catch (cause) {
        setRefreshError(toUserMessage(cause, t("rss.refreshSeedsFailed")));
        return false;
      } finally {
        setRefreshingGroup(undefined);
      }
      await read(current, offset);
      return true;
    },
    [provider, read],
  );

  const groupEntries = useMemo<OneShotGroupEntry[]>(
    () =>
      discovered.map((group) => {
        const loaded = groups.find((candidate) => candidate.key === group.key);
        const latestAt = loaded ? latestPublishedAt(loaded.rows) : undefined;
        return {
          key: group.key,
          name: loaded?.name || group.name,
          ...(loaded ? { count: loaded.rows.length } : {}),
          ...(latestAt ? { latestAt } : {}),
        };
      }),
    [discovered, groups],
  );

  const expand = useCallback((row: OneShotSeedRow) => expandSeed(row, provider), [provider]);
  return {
    kind: "provider",
    draft: businessReady ? draft : undefined,
    setDraft,
    initialOffset: 0,
    groupEntries,
    groups,
    // After a failed read it no longer counts as "still reading": otherwise the busy state would cover the error state forever.
    loading: discovering || (!businessReady && discoveryError === undefined),
    pending,
    error: discoveryError ?? viewError,
    openingGroup,
    openError,
    openGroup,
    refreshingGroup,
    refreshError,
    refreshGroup,
    expand,
    preview: read,
    fetchRequired: false,
    fetchBusy: false,
    reload: () => setAttempt((value) => value + 1),
  };
}

// --- custom: preset draft + explicitly fetched seed-list snapshot ----------------------

type CustomSeedSnapshot = {
  snapshot_id: string;
  snapshot_version: number;
  content_hash: string;
  provider_entry_ref: string;
  group_ref: string;
};

/** A fetched snapshot must carry complete identity, otherwise the read surface has no reference to fill back verbatim. */
async function fetchExactCustomSnapshot(request: CustomSnapshotRequest): Promise<CustomSeedSnapshot> {
  const result = await fetchCustomSnapshot(request);
  if (
    result.provider !== "custom" ||
    !result.snapshot_id ||
    !result.snapshot_version ||
    !result.content_hash ||
    !result.provider_entry_ref ||
    !result.group_ref
  ) {
    throw new Error(t("addPreset.incomplete"));
  }
  return {
    snapshot_id: result.snapshot_id,
    snapshot_version: result.snapshot_version,
    content_hash: result.content_hash,
    provider_entry_ref: result.provider_entry_ref,
    group_ref: result.group_ref,
  };
}

function useCustomSeedSource({ entry, enabled }: { entry: CatalogEntryView; enabled: boolean }): OneShotSeedSource {
  const candidateId = entry.candidateId;
  const queryClient = useQueryClient();
  const [candidate, setCandidate] = useState<AnimeConfigCandidate>();
  const [draft, setDraft] = useState<AnimeDraft>();
  const [candidateError, setCandidateError] = useState<string>();
  const [attempt, setAttempt] = useState(0);

  // The preset draft is the task's starting point; when it carries no business snapshot the global
//   config is loaded (the server fails closed on drafts without one). Later edits belong to this
//   task only and never write back to the preset.
  useEffect(() => {
    if (!enabled || !candidateId) return;
    let active = true;
    setCandidate(undefined);
    setDraft(undefined);
    setCandidateError(undefined);
    void getAnimeConfigCandidate(candidateId)
      .then(async (value) => {
        const loaded = value.draft.business
          ? value.draft
          : await withGlobalBusiness(
              value.draft,
              queryClient.fetchQuery({ queryKey: configKeys.businessPreset, queryFn: getBusinessPreset }),
            );
        if (!active) return;
        setCandidate(value);
        setDraft(loaded);
      })
      .catch((cause) => {
        if (active) setCandidateError(toUserMessage(cause, t("addPreset.loadFailed")));
      });
    return () => {
      active = false;
    };
  }, [attempt, candidateId, enabled, queryClient]);

  const request = useMemo<CustomSnapshotRequest | undefined>(
    () =>
      candidate && candidate.current_revision != null
        ? { candidate_id: candidate.id, revision: candidate.current_revision, expected_version: candidate.version }
        : undefined,
    [candidate],
  );

  // Fetching the seed list is explicit and does not happen on open: without a snapshot a clear fetch entry is offered.
  const [snapshot, setSnapshot] = useState<CustomSeedSnapshot>();
  const [fetchBusy, setFetchBusy] = useState(false);
  const [fetchError, setFetchError] = useState<string>();
  const fetchSeeds = useCallback(async () => {
    if (!request) return;
    setFetchBusy(true);
    setFetchError(undefined);
    try {
      setSnapshot(await fetchExactCustomSnapshot(request));
    } catch (cause) {
      setFetchError(toUserMessage(cause, t("oneshot.loadSeedsFailed")));
    } finally {
      setFetchBusy(false);
    }
  }, [request]);

  const [view, setView] = useState<RSSListView>();
  const [pending, setPending] = useState(false);
  const [viewError, setViewError] = useState<string>();
  // Reads land by sequence number: consecutive adjustments re-read concurrently and only the last result counts.
  const readSeq = useRef(0);

  const feedRef = useMemo<PreviewFeedSnapshotRef | undefined>(
    () =>
      snapshot
        ? {
            provider: "custom",
            provider_entry_ref: snapshot.provider_entry_ref,
            provider_group_ref: snapshot.group_ref,
            snapshot_id: snapshot.snapshot_id,
            snapshot_version: snapshot.snapshot_version,
            content_hash: snapshot.content_hash,
          }
        : undefined,
    [snapshot],
  );

  // One-shot browse: reads this task's draft, with the server binding seeds to the named snapshots; the read itself touches no network.
  const readView = useCallback(
    async (current: AnimeDraft | undefined, offset: number) => {
      if (!current || !feedRef) return;
      const seq = ++readSeq.current;
      setPending(true);
      setViewError(undefined);
      try {
        const result = await readAnimeRSSListView({
          input_source: "current_input",
          draft: structuredClone(current),
          feed: [feedRef],
          one_shot: true,
          limit_per_group: ITEMS_PER_GROUP,
          ...(offset !== 0 ? { episode_offset: offset } : {}),
        });
        if (seq === readSeq.current) setView(result);
      } catch (cause) {
        if (seq === readSeq.current) setViewError(toUserMessage(cause, t("oneshot.previewFailed")));
      } finally {
        if (seq === readSeq.current) setPending(false);
      }
    },
    [feedRef],
  );

  const initialOffset = candidate?.draft.rss_sources[0]?.episode_offset ?? 0;
  // With a snapshot in hand, read once with the task-start draft and offset; after that the dialog re-reads explicitly.
  const draftRef = useRef(draft);
  draftRef.current = draft;
  useEffect(() => {
    if (!enabled || !feedRef) return;
    void readView(draftRef.current, initialOffset);
  }, [enabled, feedRef, initialOffset, readView]);

  const expand = useCallback((row: OneShotSeedRow) => expandSeed(row, "custom"), []);
  const groups = useMemo(() => seedGroupsOf(view), [view]);
  return {
    kind: "custom",
    draft,
    setDraft,
    initialOffset,
    groupEntries: [],
    groups,
    loading:
      enabled &&
      candidateError === undefined &&
      fetchError === undefined &&
      (draft === undefined || (view === undefined && pending)),
    pending,
    error: candidateError ?? fetchError ?? viewError,
    openGroup: async () => {},
    refreshGroup: async () => false,
    expand,
    preview: readView,
    // No fetch is required before a candidate has been read; a ready candidate with no snapshot means an explicit fetch is needed.
    fetchRequired: Boolean(request) && draft !== undefined && !snapshot && !fetchBusy,
    fetchBusy,
    fetchSeeds: () => void fetchSeeds(),
    reload: () => setAttempt((value) => value + 1),
  };
}
