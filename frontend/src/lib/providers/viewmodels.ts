import type { MetadataHold } from "../api/client";
import type { BroadcastDay } from "../i18n/broadcastDay";
import type { ReleaseFormat } from "../api/client";

// The catalog view model: one entry shape shared by the four sections
// (Subscribed/Mikan/AniBT/Custom). Unsubscribed entries come from the provider catalog
// fixture or the custom candidate store; subscribed entries come from listAnime. This is a
// pure frontend view model produced by the CatalogProvider adapter (providers/port.ts);
// pages never import the fixture directly.

export type ProviderKey = "anibt" | "mikan" | "custom";

/** Sources of a keyword torrent search. Nyaa and Jackett have no work catalog, so they are search sources only and never ProviderKeys. */
export type SearchProviderKey = "anibt" | "mikan" | "nyaa" | "jackett";

export type SnapshotStatus = "fresh" | "stale" | "error" | "empty";

export interface CatalogSnapshotView {
  status: SnapshotStatus;
  season?: string;
  observedAt?: string;
  /** The time of the last successful refresh (including a 304 recheck); absent when never successful. */
  lastSuccessAt?: string;
  /** The site withdrew this season's original entries at its last successful refresh (an empty catalog came back). */
  withdrawn?: boolean;
  errorMessage?: string;
}

/** The outcome of one manual catalog refresh, used only for a notice; the list follows the subsequent re-read. */
export type CatalogRefreshOutcome = "fresh" | "withdrawn" | "outage" | "zero_result" | "stale";

/** CatalogProviderView — one provider's browse surface. */
export interface CatalogProviderView {
  snapshot: CatalogSnapshotView;
  entries: CatalogEntryView[];
}

/** The metadata match fact: browse cards show ⚠/✓ and subscribing assembles it into the
 *  AnimeDraft. Every entry has a conclusion: subscribed entries follow the server's
 *  metadata_hold; draft/catalog entries follow whether the bindings are complete. */
export interface CatalogMetadataMatch {
  /** Both Bangumi and TMDB are bound; either missing means false (which one is expressed by bgmId / tmdbWorkId themselves). */
  matched: boolean;
  bgmId?: string;
  tmdbWorkId?: string;
  tmdbMediaKind?: "tv" | "movie";
  tmdbSeason?: number;
  /** Subscribed entry: the owner-pinned media kind (metadata.media_kind); never rewrites the TMDB tuple. */
  mediaKind?: "tv" | "movie";
}

/** CatalogEntryView — a single show card, provider-agnostic. */
export interface CatalogEntryView {
  /** The external provider browse attribute; it is not a formal owner identity and does not
   *  become one by being linked to exactly one owner. The formal owner identity comes only
   *  from managedAnimeRef. */
  providerEntryRef: string;
  source: ProviderKey;
  title: string;
  originalTitle?: string;
  posterUrl?: string;
  /** The provider's own cover link: the fallback for the meta poster on browse cards; the
   *  first choice on subscribed list cards (the provider catalog snapshot is cached, so
   *  there is an image even without a BGM binding), falling back to posterUrl when loading
   *  fails. */
  coverUrl?: string;
  season?: string;
  broadcastDay?: BroadcastDay;
  /** The release format the provider catalog states (a read-only fact: Mikan's home
   *  section, AniBT's format); undefined means the provider did not state one. The wall and
   *  the browse pages file movies and OVA/ONA rows by it. */
  releaseFormat?: ReleaseFormat;
  /** The release situation the site itself states (AniBT's hasRelease, Mikan's greyed-out
   *  entries); undefined means unknown and nothing is guessed from the title. A no_releases
   *  browse card gets a mask and stays clickable. */
  releaseState?: "has_releases" | "no_releases";
  /** The seed count AniBT states (a browse fact). */
  releaseCount?: number;
  /** The metadata match fact of an unsubscribed entry. */
  metadataMatch: CatalogMetadataMatch;
  /** The state of one concrete subscription (the subscribed wall, a subscribed custom
   *  preset); always empty on a provider candidate card, where `subscribed` says whether
   *  there is a subscription. */
  subscriptionState?: "pending" | "active" | "stopped";
  managedAnimeRef?: string;
  subscriptionRef?: string;
  /** Provider browse/search candidate card: the external entry has at least one unended
   *  subscription. A candidate card is only marked "subscribed" from this and carries no
   *  subscription's identity, version or state (subscriptionState and friends stay empty). */
  subscribed?: boolean;
  /** Provider browse/search candidate card: the managed anime ids of the external entry's
   *  non-retired instances (lifecycle active), oldest first. Absent or empty means a click opens
   *  the candidate draft; one id opens that subscription; several open the subscription column
   *  filtered to the entry. Identity only: no version or state is carried. */
  liveInstanceRefs?: string[];
  /** The current subscription version patchAnimeSubscription needs. */
  subscriptionVersion?: number;
  /** The owner's irreversible lifecycle boundary. Retired is read-only: the card shows "retired" and no longer offers start/stop/config/unsubscribe. */
  lifecycleState?: "active" | "retired";
  lifecycleEpoch?: number;
  /** A subscribed entry's metadata hold fact (from listAnime's metadata_hold). */
  metadataHold?: MetadataHold;
  /** The source fact for the subscription draft (provided by the fixture / the candidate). */
  rssUrl?: string;
  groupName?: string;
  /** Custom candidate display attribute; neither a managed_anime.id nor an owner identity. It stays on the Custom list after subscribing. */
  instanceLabel?: string;
  /** External link to the source anime page (Mikan/AniBT). */
  sourceUrl?: string;
  /** The premiere/air date (a browse fact, YYYY-MM-DD). */
  airDate?: string;
  /** The catalog's last-updated time (a browse fact; the update text of Mikan's catalog page, shown as written). */
  lastUpdated?: string;
  /** The list of subscribed group names (shown on subscribed entries). */
  subscribedGroups?: string[];
  /** The worst RSS source health (shown on subscribed entries). */
  rssHealth?: "ready" | "degraded" | "failed";
  /** The reason summary of the worst health (for the hover tip; present when degraded/failed and the runtime has a reason). */
  rssHealthReason?: string;
  /** Episode progress (shown on subscribed entries). Used by count only: imported episodes
   *  = the number of deduplicated episodes, compared against total; never aligned by episode
   *  number. `done` is derived by the backend at read time under the same count rule. */
  episodeProgress?: { episodes?: number[]; total?: number; done?: boolean };
  /** The provider-stated catalog score (AniBT's 0-10 rating); absent for providers without one. */
  score?: number;
  /** The custom candidate id; a stable reference to the candidate preset, not a Subscription ID. */
  candidateId?: string;
  /** true when this entry came from a fixture, not a live provider. */
  fixture?: boolean;
}
