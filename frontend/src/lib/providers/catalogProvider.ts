import {
  cleanupAnimeLocalData,
  deleteAnimeSubscription,
  listAnime,
  patchAnimeSubscription,
  readCatalogWorkspace,
  refreshCatalogProvider,
  subscribeAnime,
  newUUID,
  type AnimeDraft,
  type AnimeListItem,
  type CatalogItem,
  type CatalogSnapshot,
  type SubscribeResult,
  type Subscription,
  type SubscriptionPatch,
} from "../api/client";
import { getCandidateStore } from "../candidate/serverCandidateStore";
import type { AnimeConfigCandidate } from "../candidate/port";
import type { CatalogProvider } from "./port";
import type {
  CatalogEntryView,
  CatalogProviderView,
  CatalogRefreshOutcome,
  CatalogSnapshotView,
  ProviderKey,
} from "./viewmodels";

// The API adapter: the implementation of the CatalogProvider port. anibt/mikan go through
// the real catalog API (readCatalogWorkspace / refreshCatalogProvider), one candidate card
// per external entry, marked only with whether a subscription exists. The Custom section
// reads local candidate presets and uses activated_managed_anime_id to show their formal
// subscription state. A provider ref/locator is not an owner identity.

export function listCatalogSubscriptions(): Promise<AnimeListItem[]> {
  return listAnime();
}

/** Unsubscribes (irreversibly retires the instance and keeps the local owner evidence). */
export function deleteCatalogSubscription(
  managedAnimeRef: string,
  input: { expected_version: number },
): Promise<void> {
  return deleteAnimeSubscription(managedAnimeRef, input);
}

/** Physically cleans up a retired instance's local records; qB torrents/data and library files are not deleted. */
export function cleanupCatalogLocalData(
  managedAnimeRef: string,
  input: { expected_lifecycle_epoch: number; confirmation: "delete-local-data" },
  idempotencyKey: string,
): Promise<void> {
  return cleanupAnimeLocalData(managedAnimeRef, input, idempotencyKey);
}

function metadataMatchFromItem(item: AnimeListItem): CatalogEntryView["metadataMatch"] {
  const held = item.metadata_hold.held;
  return {
    matched: !held,
    bgmId: item.metadata.bgm?.work_id,
    tmdbWorkId: item.metadata.tmdb?.work_id,
    tmdbMediaKind: item.metadata.tmdb?.media_kind,
    tmdbSeason: item.metadata.tmdb?.season,
    mediaKind: item.metadata.media_kind ?? undefined,
  };
}

/** An unsubscribed custom preset: the bindings come from the preset draft (there is no
 *  metadata_hold to read before subscribing). A preset with no bindings gets `matched: false`
 *  just like a provider catalog entry, and the title area shows "Bangumi and TMDB not linked"
 *  as usual — whether a preset can be subscribed does not depend on the links; a missing link
 *  is only a fact. */
function metadataMatchFromCandidateDraft(candidate: AnimeConfigCandidate): CatalogEntryView["metadataMatch"] {
  const metadata = candidate.draft.metadata;
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

export function mapCandidateToEntry(
  candidate: AnimeConfigCandidate,
  subscription?: AnimeListItem,
): CatalogEntryView {
  const subscriptionManagedAnimeID = subscription?.managed_anime.id;
  const draft = candidate.draft;
  const firstSource = draft.rss_sources.length > 0 ? draft.rss_sources[0] : undefined;
  return {
    providerEntryRef: `custom:${candidate.id}`,
    source: "custom",
    title: candidate.title,
    // The binding facts come from the preset draft (once subscribed, the subscription read
    // model is authoritative): bound BGM / TMDB show the same facts on the card and in the
    // config title area as a provider catalog entry; only a draft with nothing bound has no
    // conclusion.
    metadataMatch: subscription ? metadataMatchFromItem(subscription) : metadataMatchFromCandidateDraft(candidate),
    rssUrl: firstSource?.locator ?? "",
    groupName: firstSource?.group_name ?? "",
    // The preset's own note, never the title: a custom draft requires the owner to write one,
    // and a note that only repeats the title is no note.
    instanceLabel: candidate.instance_label ?? undefined,
    candidateId: candidate.id,
    // The preset's own BGM binding supplies the poster (a server proxy path); once
    // subscribed, the subscription's metadata poster below wins. An unsubscribed preset or
    // one whose subscription was deleted keeps its cover.
    posterUrl: candidate.poster_url ?? undefined,
    ...(subscription && subscriptionManagedAnimeID === candidate.activated_managed_anime_id
      ? {
          subscriptionState: subscription.subscription.state,
          lifecycleState: subscription.subscription.lifecycle_state,
          lifecycleEpoch: subscription.subscription.lifecycle_epoch,
          managedAnimeRef: subscription.managed_anime.id,
          subscriptionRef: subscription.subscription.id,
          subscriptionVersion: subscription.subscription.version,
          metadataHold: subscription.metadata_hold,
          subscribedGroups: subscription.subscribed_groups ?? undefined,
          rssHealth: subscription.rss_health ?? undefined,
          rssHealthReason: subscription.rss_health_reason ?? undefined,
          posterUrl: subscription.poster_url ?? candidate.poster_url ?? undefined,
          broadcastDay: subscription.broadcast_day ?? undefined,
          releaseFormat: subscription.release_format ?? undefined,
          episodeProgress: mapEpisodeProgress(subscription.episode_progress),
        }
      : {}),
  };
}

type ApiEpisodeProgress = AnimeListItem["episode_progress"];

/** Maps imported progress: the generated contract's episodes (ascending, SP excluded) pass through, and done is derived at read time and passed through. */
function mapEpisodeProgress(progress: ApiEpisodeProgress): CatalogEntryView["episodeProgress"] {
  if (!progress) return undefined;
  return {
    episodes: progress.episodes,
    total: progress.total ?? undefined,
    done: progress.done,
  };
}

export function mapAnimeListItem(item: AnimeListItem): CatalogEntryView {
  const origin = item.subscription.origin;
  const source: ProviderKey =
    origin === "anibt" || origin === "mikan" ? origin : "custom";
  return {
    providerEntryRef:
      source === "custom"
        ? `custom:${item.managed_anime.id}`
        : item.subscription.origin_entry_ref ?? item.managed_anime.id,
    source,
    title: item.managed_anime.title,
    instanceLabel: item.subscription.instance_label ?? undefined,
    // The frozen creation provenance (pre-108 instances and search/custom
    // creations carry none): a display fact of the card, never re-derived.
    season: item.subscription.entry_season ?? undefined,
    posterUrl: item.poster_url ?? undefined,
    coverUrl: item.cover_url ?? undefined,
    broadcastDay: item.broadcast_day ?? undefined,
    releaseFormat: item.release_format ?? undefined,
    episodeProgress: mapEpisodeProgress(item.episode_progress),
    metadataMatch: metadataMatchFromItem(item),
    subscriptionState: item.subscription.state,
    lifecycleState: item.subscription.lifecycle_state,
    lifecycleEpoch: item.subscription.lifecycle_epoch,
    managedAnimeRef: item.managed_anime.id,
    subscriptionRef: item.subscription.id,
    subscriptionVersion: item.subscription.version,
    metadataHold: item.metadata_hold,
    subscribedGroups: item.subscribed_groups ?? undefined,
    rssHealth: item.rss_health ?? undefined,
    rssHealthReason: item.rss_health_reason ?? undefined,
  };
}

// --- catalog API -> viewmodel mappers ---

function mapSnapshotStatus(status: CatalogSnapshot["status"]): CatalogSnapshotView["status"] {
  switch (status) {
    case "fresh":
      return "fresh";
    case "stale":
      return "stale";
    case "outage":
      return "error";
    case "zero_result":
      // The season is valid but the source has published nothing yet (Mikan
      // answers a just-started season with an empty template). Not an outage.
      return "empty";
    case "withdrawn":
      // The site returned an empty catalog this time and withdrew the season's original entries: not an outage, and no usable entries.
      return "empty";
    default:
      return "stale";
  }
}

export function mapSnapshotView(snap: CatalogSnapshot): CatalogSnapshotView {
  const status = mapSnapshotStatus(snap.status);
  return {
    // A refresh not successful for over 24 hours (the server's `stale`) displays as stale; a site outage still displays as an error.
    status: snap.stale && status !== "error" ? "stale" : status,
    season: snap.season,
    observedAt: snap.observed_at,
    lastSuccessAt: snap.last_success_at ?? undefined,
    withdrawn: snap.status === "withdrawn" || undefined,
    errorMessage: snap.error_message ?? undefined,
  };
}

export function mapCatalogItem(item: CatalogItem, provider: ProviderKey, season?: string): CatalogEntryView {
  // A browse/search card is a candidate, not a subscription: only whether this external
  // entry has a subscription matters, and no subscription's id/version/state is carried. An
  // ended subscription still counts as subscribed while the instance remains on the
  // subscribed wall; only when local-record cleanup removes the instance is it unsubscribed again.
  const subscribed = (item.instances ?? []).length > 0;
  // A click opens an existing subscription instead of a second draft while the entry has a
  // non-retired instance; a retired-only entry may be subscribed again.
  const liveInstanceRefs = (item.instances ?? [])
    .filter((instance) => instance.lifecycle_state !== "retired")
    .map((instance) => instance.managed_anime_ref);
  return {
    providerEntryRef: item.provider_entry_ref,
    source: provider,
    title: item.title,
    // The season page being browsed: display fact of the card, and the frozen
    // provenance draftFromEntry carries onto the subscription at creation.
    // Search-created entries pass no season.
    ...(season ? { season } : {}),
    originalTitle: item.original_title ?? undefined,
    // The server's poster proxy path (present only with a BGM binding); the BGM image host
    // is unreachable from browsers, so it must go through the own proxy and the card falls
    // back to a placeholder on load failure. Without a binding, the provider's own cover is
    // the fallback.
    posterUrl: item.poster_url ?? undefined,
    coverUrl: item.cover_url ?? undefined,
    broadcastDay: item.broadcast_day ?? undefined,
    releaseFormat: item.release_format ?? undefined,
    // The release situation the site itself states; absent means unknown and stays unfilled.
    releaseState: item.release_state ?? undefined,
    releaseCount: item.release_count ?? undefined,
    score: item.score ?? undefined,
    lastUpdated: item.update_text ?? undefined,
    // CatalogItem only carries the optional BGM fact. A complete match also
    // requires the atomic TMDB binding, which is not part of this read model.
    metadataMatch: {
      matched: false,
      bgmId: item.bgm_id ?? undefined,
    },
    ...(subscribed ? { subscribed: true } : {}),
    ...(liveInstanceRefs.length > 0 ? { liveInstanceRefs } : {}),
  };
}

/**
 * Provider 浏览列表投影：每个外部条目一张候选卡（键是 provider ref）。条目有订阅时
 * 卡片只标「已订阅」；订阅自身（含复制出的订阅）的状态与操作只在已订阅墙上。
 */
export function mapCatalogEntries(items: CatalogItem[], provider: ProviderKey, season?: string): CatalogEntryView[] {
  return items.map((item) => mapCatalogItem(item, provider, season));
}

class ApiCatalogProvider implements CatalogProvider {
  readonly isMock = false;

  constructor(
    readonly key: ProviderKey,
    readonly label: string,
  ) {}

  async getCatalog(season: string): Promise<CatalogProviderView> {
    // custom is not a browsable catalog provider: it reads the local candidate
    // store.
    if (this.key === "custom") {
      const candidateStore = getCandidateStore();
      const [candidates, subscriptions] = await Promise.all([
        candidateStore.list({ origin: "custom" }),
        listCatalogSubscriptions(),
      ]);
      return {
        entries: candidates.map((candidate) =>
          mapCandidateToEntry(
            candidate,
            subscriptions.find(
              (item) =>
                item.subscription.origin === "custom" &&
                item.managed_anime.id === candidate.activated_managed_anime_id,
            ),
          ),
        ),
        snapshot: { status: "fresh", observedAt: new Date().toISOString() },
      };
    }
    const workspace = await readCatalogWorkspace(this.key, season);
    return {
      entries: mapCatalogEntries(workspace.items, this.key, season),
      snapshot: mapSnapshotView(workspace.snapshot),
    };
  }

  async refreshCatalog(season: string): Promise<CatalogRefreshOutcome> {
    if (this.key === "custom") return "fresh";
    const snapshot = await refreshCatalogProvider(this.key, season);
    return snapshot.status;
  }

  subscribe(draft: AnimeDraft): Promise<SubscribeResult> {
    return subscribeAnime(draft, newUUID());
  }

  async setSubscriptionState(managedAnimeRef: string, patch: SubscriptionPatch): Promise<Subscription> {
    return patchAnimeSubscription(managedAnimeRef, patch);
  }
}

export function createCatalogProvider(key: ProviderKey, label: string): CatalogProvider {
  return new ApiCatalogProvider(key, label);
}
