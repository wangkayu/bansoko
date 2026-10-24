import { describe, expect, it } from "vitest";
import type { AnimeDraft, AnimeListItem, CatalogItem, CatalogSnapshot } from "../api/client";
import { mapAnimeListItem, mapCandidateToEntry, mapCatalogEntries, mapCatalogItem, mapSnapshotView } from "./catalogProvider";
import type { AnimeConfigCandidate } from "../candidate/port";

function makeDraft(overrides: Partial<AnimeDraft> = {}): AnimeDraft {
  return {
    origin: "custom",
    origin_entry_ref: null,
    title: "自定义节目",
    title_override: null,
    instance_label: "客厅版",
    metadata: { bgm: null, tmdb: null },
    subgroups: { max_rss: 3, entries: [] },
    rules: {
      sections: {},
    },
    rss_sources: [
      {
        source_kind: "custom_rss",
        locator: "https://example.test/rss?passkey=secret",
        locator_display: "https://example.test/rss?passkey=secret",
        group_name: "某字幕组",
        title: "自定义节目",
        episode_offset: 0,
      },
    ],
    ...overrides,
  };
}

const candidate: AnimeConfigCandidate = {
  id: "candidate-1",
  origin: "custom",
  origin_entry_ref: null,
  title: "自定义节目",
  title_override: null,
  instance_label: "客厅版",
  draft: makeDraft(),
  version: 1,
  activated_managed_anime_id: null,
  created_at: "2026-07-21T00:00:00Z",
  updated_at: "2026-07-21T00:00:00Z",
  status: "working",
  draft_digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
  current_revision: 1,
  current_revision_digest: "sha256:" + "1".repeat(64),
  last_activity: "2026-07-21T00:00:00Z",
  retention_until: "2026-08-20T00:00:00Z",
  cleanup_eligible_at: null,
  cleanup_reason: null,
};

function listItem(
  origin: AnimeListItem["subscription"]["origin"],
  originEntryRef: string | null,
  state: AnimeListItem["subscription"]["state"] = "active",
  overrides: { managedAnimeID?: string; subscriptionVersion?: number; title?: string } = {},
): AnimeListItem {
  const managedAnimeID = overrides.managedAnimeID ?? "anime-1";
  return {
    managed_anime: { id: managedAnimeID, title: overrides.title ?? "已订阅节目", version: 2 },
    subscription: {
      id: `subscription:${managedAnimeID}`,
      managed_anime_id: managedAnimeID,
      origin,
      origin_entry_ref: originEntryRef,
      instance_label: origin === "custom" ? "客厅版" : null,
      state,
      lifecycle_state: "active",
      lifecycle_epoch: 1,
      version: overrides.subscriptionVersion ?? 3,
      new_releases_only: false,
    },
    metadata: {
      managed_anime_id: managedAnimeID,
      bgm: { work_id: "bgm-1" },
      tmdb: { work_id: "tmdb-1", media_kind: "tv", season: 1 },
      version: 2,
    },
    metadata_hold: { held: false, missing: [] },
    broadcast_day: "none",
  };
}

function providerCatalogItem(overrides: Partial<CatalogItem> = {}): CatalogItem {
  return {
    kind: "mikan_entry",
    provider: "mikan",
    title: "多实例节目",
    provider_entry_ref: "mikan:shared",
    broadcast_day: "none",
    catalog_state: "present",
    preference: "neutral",
    allowed_actions: ["prepare_subscribe"],
    instances: [],
    ...overrides,
  };
}

const INST_A = { managed_anime_ref: "anime-a", subscription_ref: "sub-a", subscription_state: "active", subscription_version: 7, lifecycle_state: "active", lifecycle_epoch: 1 } as const;
const INST_B = { managed_anime_ref: "anime-b", subscription_ref: "sub-b", subscription_state: "stopped", subscription_version: 2, lifecycle_state: "active", lifecycle_epoch: 1 } as const;
const INST_RETIRED = { managed_anime_ref: "anime-r", subscription_ref: "sub-r", subscription_state: "stopped", subscription_version: 9, lifecycle_state: "retired", lifecycle_epoch: 2 } as const;

describe("catalog mapping", () => {
  it("maps provider-stated release availability and keeps unknown absent", () => {
    const anibt = mapCatalogItem(providerCatalogItem({ kind: "anibt_entry", provider: "anibt", release_state: "no_releases", release_count: 0 }), "anibt");
    expect(anibt.releaseState).toBe("no_releases");
    expect(anibt.releaseCount).toBe(0);
    const mikan = mapCatalogItem(providerCatalogItem({ release_state: "has_releases", update_text: "2026/10/18" }), "mikan");
    expect(mikan.releaseState).toBe("has_releases");
    expect(mikan.lastUpdated).toBe("2026/10/18");
    const unknown = mapCatalogItem(providerCatalogItem(), "mikan");
    expect(unknown.releaseState).toBeUndefined();
    expect(unknown.releaseCount).toBeUndefined();
    expect(unknown.lastUpdated).toBeUndefined();
  });

  it("maps typed provider catalog items", () => {
    const entry = mapCatalogItem(
      {
        kind: "mikan_entry",
        provider: "mikan",
        title: "目录节目",
        provider_entry_ref: "mikan:entry-1",
        bgm_id: "123",
        broadcast_day: "none",
        catalog_state: "present",
        preference: "neutral",
        allowed_actions: ["prepare_subscribe"],
        instances: [],
      },
      "mikan",
    );
    expect(entry.providerEntryRef).toBe("mikan:entry-1");
    expect(entry.source).toBe("mikan");
    expect(entry.metadataMatch.bgmId).toBe("123");
    expect(entry.metadataMatch.matched).toBe(false);
  });

  it("keeps a custom preset after it becomes an active subscription", () => {
    // The link truth between candidate and formal subscription is activated_managed_anime_id
    // (written by the server at activation), not origin_entry_ref (a custom subscription's
    // origin_entry_ref only records where the candidate id came from).
    const activated = { ...candidate, activated_managed_anime_id: "anime-1" };
    const entry = mapCandidateToEntry(activated, listItem("custom", candidate.id));
    expect(entry.providerEntryRef).toBe("custom:candidate-1");
    expect(entry.candidateId).toBe(candidate.id);
    expect(entry.instanceLabel).toBe("客厅版");
    expect(entry.subscriptionState).toBe("active");
    expect(entry.managedAnimeRef).toBe("anime-1");
  });

  it("does not mark an unrelated custom subscription as this preset", () => {
    const entry = mapCandidateToEntry(candidate, listItem("custom", "other-candidate"));
    expect(entry.subscriptionState).toBeUndefined();
    expect(entry.managedAnimeRef).toBeUndefined();
  });

  it("reports an unbound custom preset as unmatched, like the provider catalog", () => {
    // A blank pre-subscription preset has no bindings: matched=false under the same terms as
    // the provider catalog, with bgmId/tmdbWorkId absent expressing which is missing and the
    // title area showing "Bangumi and TMDB not linked" as usual.
    const entry = mapCandidateToEntry(candidate);
    expect(entry.metadataMatch.matched).toBe(false);
    expect(entry.metadataMatch.bgmId).toBeUndefined();
    expect(entry.metadataMatch.tmdbWorkId).toBeUndefined();
  });

  it("shows a saved custom preset's own draft bindings like the provider catalog", () => {
    // When the preset draft binds BGM/TMDB, the card and the config title area get the same
    // facts as the provider catalog: one side alone is an incomplete match, both together are
    // a match.
    const bgmOnly = {
      ...candidate,
      draft: makeDraft({ metadata: { bgm: { work_id: "bgm-2" }, tmdb: null } }),
    };
    expect(mapCandidateToEntry(bgmOnly).metadataMatch.matched).toBe(false);
    expect(mapCandidateToEntry(bgmOnly).metadataMatch.bgmId).toBe("bgm-2");
    expect(mapCandidateToEntry(bgmOnly).metadataMatch.tmdbWorkId).toBeUndefined();
    const both = {
      ...candidate,
      draft: makeDraft({
        metadata: {
          bgm: { work_id: "bgm-2" },
          tmdb: { work_id: "888", media_kind: "tv", season: 3 },
          media_kind_override: "movie",
        },
      }),
    };
    const entry = mapCandidateToEntry(both);
    expect(entry.metadataMatch.matched).toBe(true);
    expect(entry.metadataMatch.bgmId).toBe("bgm-2");
    expect(entry.metadataMatch.tmdbWorkId).toBe("888");
    expect(entry.metadataMatch.tmdbSeason).toBe(3);
    expect(entry.metadataMatch.mediaKindOverride).toBe("movie");
  });

  it("gives a custom preset the poster of its own Bangumi binding", () => {
    // An unsubscribed preset (or one whose subscription was deleted): the poster comes from the preset draft's own binding, independent of any subscription.
    const bound = { ...candidate, poster_url: "/api/v1/images/poster/434144" };
    expect(mapCandidateToEntry(bound).posterUrl).toBe("/api/v1/images/poster/434144");
    expect(mapCandidateToEntry(candidate).posterUrl).toBeUndefined();
    // Once subscribed, the subscription's metadata poster wins.
    const activated = { ...bound, activated_managed_anime_id: "anime-1" };
    const subscribed = { ...listItem("custom", candidate.id), poster_url: "/api/v1/images/poster/999" };
    expect(mapCandidateToEntry(activated, subscribed).posterUrl).toBe("/api/v1/images/poster/999");
  });

  it("keeps the metadata verdict once the preset is subscribed", () => {
    const entry = mapCandidateToEntry(candidate, listItem("custom", candidate.id));
    expect(entry.metadataMatch.matched).toBe(true);
    // Only the list read model carrying the binding facts lets the card show real badges instead of placeholders.
    expect(entry.metadataMatch.bgmId).toBe("bgm-1");
    expect(entry.metadataMatch.tmdbWorkId).toBe("tmdb-1");
    expect(entry.metadataMatch.tmdbSeason).toBe(1);
  });

  it("preserves provider origin in the All management view", () => {
    const entry = mapAnimeListItem(listItem("anibt", "anibt:entry-1", "stopped"));
    expect(entry.source).toBe("anibt");
    expect(entry.providerEntryRef).toBe("anibt:entry-1");
    expect(entry.subscriptionState).toBe("stopped");
  });

  it("names custom All entries with a stable custom key", () => {
    // A custom subscription's stable key is managed_anime.id: origin_entry_ref records where
    // the candidate id came from, while managed_anime.id is the subscription instance's unique
    // identity.
    const entry = mapAnimeListItem(listItem("custom", "candidate-1"));
    expect(entry.source).toBe("custom");
    expect(entry.providerEntryRef).toBe("custom:anime-1");
  });

  it("does not call a BGM-only provider catalog item fully matched", () => {
    const entry = mapCatalogItem(
      {
        kind: "anibt_entry",
        provider: "anibt",
        title: "目录节目",
        provider_entry_ref: "123",
        bgm_id: "123",
        broadcast_day: "none",
        catalog_state: "present",
        preference: "neutral",
        allowed_actions: ["prepare_subscribe"],
        instances: [],
      },
      "anibt",
    );
    expect(entry.metadataMatch.bgmId).toBe("123");
    expect(entry.metadataMatch.matched).toBe(false);
  });

  it("maps the subscription list lifecycle onto the library row", () => {
    const item = listItem("mikan", "mikan:entry-1", "stopped", { managedAnimeID: "anime-lib" });
    item.subscription.lifecycle_state = "retired";
    item.subscription.lifecycle_epoch = 3;
    const entry = mapAnimeListItem(item);
    expect(entry.lifecycleState).toBe("retired");
    expect(entry.lifecycleEpoch).toBe(3);
    expect(entry.subscriptionState).toBe("stopped");
  });

  // A browse/search candidate card cares only whether a subscription exists, not its state:
  // no subscription's id, CAS version, state or display facts are carried.
  const subscriptionFields = [
    "managedAnimeRef",
    "subscriptionRef",
    "subscriptionState",
    "subscriptionVersion",
    "lifecycleState",
    "lifecycleEpoch",
    "metadataHold",
    "subscribedGroups",
    "rssHealth",
    "episodeProgress",
  ] as const;

  it("shows an entry with several subscriptions (a copy) as one candidate marked subscribed, with no subscription fields", () => {
    const entries = mapCatalogEntries([providerCatalogItem({ instances: [INST_A, INST_B] })], "mikan");
    expect(entries).toHaveLength(1);
    expect(entries[0]).toMatchObject({ providerEntryRef: "mikan:shared", title: "多实例节目", subscribed: true });
    for (const key of subscriptionFields) expect(entries[0]?.[key], key).toBeUndefined();
  });

  it("marks a single subscription the same way and ignores the singular compatibility summary", () => {
    const entry = mapCatalogItem(
      providerCatalogItem({
        managed_anime_ref: "anime-a",
        subscription_ref: "sub-a",
        subscription_state: "stopped",
        instances: [INST_A],
      }),
      "mikan",
    );
    expect(entry.subscribed).toBe(true);
    for (const key of subscriptionFields) expect(entry[key], key).toBeUndefined();
  });

  it("marks an entry whose only subscription has ended while its instance remains", () => {
    const [ended] = mapCatalogEntries([providerCatalogItem({ instances: [INST_RETIRED] })], "mikan");
    expect(ended?.subscribed).toBe(true);
    for (const key of subscriptionFields) expect(ended?.[key], key).toBeUndefined();

    const [mixed] = mapCatalogEntries([providerCatalogItem({ instances: [INST_RETIRED, INST_B] })], "mikan");
    expect(mixed?.subscribed).toBe(true);
  });

  it("leaves an entry without subscriptions unmarked", () => {
    const [entry] = mapCatalogEntries([providerCatalogItem()], "mikan");
    expect(entry?.subscribed).toBeUndefined();
    for (const key of subscriptionFields) expect(entry?.[key], key).toBeUndefined();
  });
});

describe("mapSnapshotView", () => {
  const snapshot = (status: CatalogSnapshot["status"]): CatalogSnapshot => ({
    id: "s1", season: "2026-autumn", status, provider: "mikan", observed_at: "2026-10-02T01:00:00Z",
    source_version: "2026-autumn", contract_version: "mikan-season-html-v1", health_state: "ready",
    health_checked_at: "2026-10-02T01:00:00Z", version: 1, stale: false,
  });

  it("shows a valid season the source has not populated as empty, never as an outage", () => {
    expect(mapSnapshotView(snapshot("zero_result")).status).toBe("empty");
  });

  it("keeps a real outage as an error and shows a withdrawn season as empty", () => {
    expect(mapSnapshotView(snapshot("outage")).status).toBe("error");
    expect(mapSnapshotView(snapshot("withdrawn"))).toMatchObject({ status: "empty", withdrawn: true });
  });

  it("marks a snapshot without a success in 24 hours as stale, but keeps an outage an outage", () => {
    const old = { stale: true, last_success_at: "2026-09-30T00:00:00Z" };
    expect(mapSnapshotView({ ...snapshot("fresh"), ...old })).toMatchObject({ status: "stale", lastSuccessAt: "2026-09-30T00:00:00Z" });
    expect(mapSnapshotView({ ...snapshot("outage"), ...old }).status).toBe("error");
  });
});
