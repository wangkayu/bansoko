import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { preset as businessPreset } from "../../testing/businessPreset";
import { t } from "../../lib/i18n";

// AnimeGrid integration surface: the two-area grid breakpoints (browse-area poster wall vs
//   action-area wide cards), hidden entries never entering the lists, the hide toast wording; the
//   hide-entry scope (browse area only; hiding a subscribed entry = unsubscribing the instance and
//   hiding); cards going straight to the config dialog (no first-level detail dialog);
//   pause/start and unsubscribe always present on the instance card; custom-preset save-failure
//   feedback and the delete-preset double confirm.
// HTTP-boundary integration fixture: AnimeGrid imports the same production API
// adapters used by the shipped bundle; Vitest supplies their responses through
// fetch instead of enabling a product-side fixture branch.

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

const FIXED_NOW = new Date("2026-07-15T12:00:00Z");

type FetchStub = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;
type UnknownRecord = Record<string, unknown>;

function asRecord(value: unknown): UnknownRecord {
  return typeof value === "object" && value !== null ? value as UnknownRecord : {};
}

let fetchMock: ReturnType<typeof vi.fn<FetchStub>>;
let suppressions: Array<Record<string, unknown>> = [];
let candidates: Array<Record<string, unknown>> = [];
// Entries beyond the fixture ones in the site catalog.
let extraCatalogItems: Array<Record<string, unknown>> = [];
let deletedManagedAnimeIDs = new Set<string>();
let candidateSaveError: { status: number; body: unknown } | undefined;
// Injected failure of the subscription list (GET /anime) — non-candidate GET requests return 500.
let animeListError: { status: number; body: unknown } | undefined;
// Provider search: the injected result of /catalog/providers/*/search (422 = search not supported).
let searchResult: { status?: number; body: unknown } | { status: number; body: unknown } = {
  status: 422,
  body: { code: "SEARCH_NOT_SUPPORTED", message: "先搜索" },
};
let searchCalled = false;

// The working custom candidate: the target of the delete confirm dialog and store.delete tests.
//   A custom candidate's origin_entry_ref is always null; the candidate row's id is the server
//   identity.
const customCandidateFixture = {
  id: "cand-1",
  origin: "custom",
  origin_entry_ref: null,
  title: "测试番剧",
  title_override: null,
  instance_label: "客厅版",
  draft: {
    origin: "custom",
    origin_entry_ref: null,
    title: "测试番剧",
    title_override: null,
    instance_label: "客厅版",
    metadata: { bgm: null, tmdb: null },
    subgroups: { max_rss: 3, entries: [] },
    rules: {
      sections: {
        parser: { mode: "inherit" },
        release_filter: { mode: "inherit" },
        file_filter: { mode: "inherit" },
        naming: { mode: "inherit" },
        file_effect: { mode: "inherit" },
      },
    },
    rss_sources: [
      {
        source_kind: "custom_rss",
        locator: "https://example.com/feed.xml",
        locator_display: "https://example.com/feed.xml",
        group_name: "某字幕组",
        title: "测试番剧",
        episode_offset: 0,
      },
    ],
  },
  version: 1,
  activated_managed_anime_id: null,
  created_at: "2026-07-01T00:00:00Z",
  updated_at: "2026-07-01T00:00:00Z",
  status: "working",
  draft_digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
  current_revision: 1,
  current_revision_digest: "sha256:" + "1".repeat(64),
  last_activity: "2026-07-01T00:00:00Z",
  retention_until: "2026-07-31T00:00:00Z",
  cleanup_eligible_at: null,
  cleanup_reason: null,
};

function customCandidate(id: string, title: string, overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    ...structuredClone(customCandidateFixture),
    activated_managed_anime_id: null,
    status: "working",
    ...overrides,
    id,
    title,
    draft: {
      ...structuredClone(customCandidateFixture.draft),
      ...(asRecord(overrides.draft)),
      title,
    },
  };
}

const catalogFixtures = {
  mikan: {
    title: "进击的巨人",
    original_title: "進撃の巨人",
    provider_entry_ref: "mikan:sample-giant",
    bgm_id: "486109",
  },
  anibt: {
    title: "星际邮差艾拉",
    original_title: "星間配達人エラ",
    provider_entry_ref: "anibt:stellar-mail-ella",
    bgm_id: "518900",
  },
} as const;

function animeListItemFromCandidate(candidate: Record<string, unknown>): Record<string, unknown> | null {
  const managedAnimeID = candidate.activated_managed_anime_id;
  if (typeof managedAnimeID !== "string" || deletedManagedAnimeIDs.has(managedAnimeID)) return null;
  const draft = asRecord(candidate.draft);
  const metadata = asRecord(draft.metadata);
  const subscriptionID = `subscription:${candidate.id as string}`;
  const state = (candidate.subscription_state as "pending" | "active" | "stopped" | undefined) ?? "active";
  return {
    managed_anime: {
      id: managedAnimeID,
      title: candidate.title,
      title_override: candidate.title_override ?? null,
      version: candidate.version,
    },
    subscription: {
      id: subscriptionID,
      managed_anime_id: managedAnimeID,
      origin: candidate.origin,
      origin_entry_ref: candidate.origin_entry_ref ?? null,
      instance_label: candidate.instance_label ?? null,
      state,
      lifecycle_state: (candidate.lifecycle_state as string | undefined) ?? "active",
      lifecycle_epoch: (candidate.lifecycle_epoch as number | undefined) ?? 1,
      version: candidate.subscription_version ?? 1,
    },
    metadata: {
      managed_anime_id: managedAnimeID,
      bgm: metadata.bgm ?? null,
      tmdb: metadata.tmdb ?? null,
      version: candidate.metadata_version ?? 1,
    },
    metadata_hold: {
      held: metadata.bgm == null || metadata.tmdb == null,
      missing: [
        ...(metadata.bgm == null ? ["bgm"] : []),
        ...(metadata.tmdb == null ? ["tmdb"] : []),
      ],
    },
    subscribed_groups: candidate.subscribed_groups ?? [],
    rss_health: candidate.rss_health ?? null,
    rss_health_reason: candidate.rss_health_reason ?? null,
    poster_url: null,
    cover_url: null,
    broadcast_day: (candidate as { broadcast_day?: string }).broadcast_day ?? "sat",
    ...((candidate as { release_format?: string }).release_format ? { release_format: (candidate as { release_format?: string }).release_format } : {}),
    episode_progress: candidate.episode_progress ?? null,
  };
}

function currentAnimeList(): Array<Record<string, unknown>> {
  return candidates
    .map(animeListItemFromCandidate)
    .filter((item): item is Record<string, unknown> => item !== null);
}

function catalogWorkspace(provider: "mikan" | "anibt") {
  const item = catalogFixtures[provider];
  const subscriptions = currentAnimeList();
  // Mirrors the server projection: the instances array is authoritative; with a single instance a
  //   compatibility singular summary is filled in, with several the singular fields stay empty (the
  //   frontend must not guess the first one by provider ref).
  const instances = subscriptions
    .filter((candidate) => {
      const candidateSubscription = candidate.subscription as Record<string, unknown>;
      return candidateSubscription.origin === provider && candidateSubscription.origin_entry_ref === item.provider_entry_ref;
    })
    .map((candidate) => {
      const subscription = candidate.subscription as Record<string, unknown>;
      return {
        managed_anime_ref: (candidate.managed_anime as Record<string, unknown>).id,
        subscription_ref: subscription.id,
        subscription_state: subscription.state,
        subscription_version: subscription.version,
      };
    });
  const singular = instances.length === 1
    ? {
        managed_anime_ref: instances[0]!.managed_anime_ref,
        subscription_ref: instances[0]!.subscription_ref,
        subscription_state: instances[0]!.subscription_state,
      }
    : {};
  return {
    season: "2026-summer",
    snapshot: {
      id: `${provider}-snapshot`,
      season: "2026-summer",
      status: "fresh",
      provider,
      observed_at: "2026-07-15T12:00:00Z",
      source_version: "fixture",
      contract_version: "v2",
      health_state: "ready",
      health_checked_at: "2026-07-15T12:00:00Z",
      version: 1,
    },
    items: [{
      kind: provider === "mikan" ? "mikan_entry" : "anibt_entry",
      provider,
      title: item.title,
      original_title: item.original_title,
      provider_entry_ref: item.provider_entry_ref,
      bgm_id: item.bgm_id,
      poster_url: null,
      cover_url: null,
      broadcast_day: "sat",
      catalog_state: "present",
      preference: "neutral",
      allowed_actions: ["prepare_subscribe"],
      instances,
      ...singular,
    }, ...extraCatalogItems],
  };
}

// The subscribed instances' workspace read model: the same workspace contract, lifecycle_state
//   taken from the candidate. A retired instance opening it must enter read-only mode (capability
//   shutting off every mutation), not be blocked by a toast.
function animeWorkspaceFor(managedAnimeID: string): Record<string, unknown> {
  const candidate = candidates.find((item) => item.activated_managed_anime_id === managedAnimeID);
  return {
    managed_anime: { id: managedAnimeID, title: candidate?.title ?? "进击的巨人", title_override: null, version: 1 },
    placement_handoffs: [],
    subscription: {
      id: `subscription:${candidate?.id as string}`,
      managed_anime_id: managedAnimeID,
      origin: candidate?.origin ?? "mikan",
      origin_entry_ref: candidate?.origin_entry_ref ?? null,
      instance_label: candidate?.instance_label ?? null,
      state: (candidate?.subscription_state as string | undefined) ?? "active",
      lifecycle_state: (candidate?.lifecycle_state as string | undefined) ?? "active",
      lifecycle_epoch: (candidate?.lifecycle_epoch as number | undefined) ?? 1,
      version: Number(candidate?.subscription_version ?? 1),
    },
    metadata: { managed_anime_id: managedAnimeID, bgm: null, tmdb: null, version: 1 },
    metadata_hold: { held: true, missing: ["bgm", "tmdb"] },
    subgroups: {
      managed_anime_id: managedAnimeID,
      subgroups: { max_rss: 1, entries: [] },
      effective_selection: [],
      rss_sources: [],
      version: 1,
    },
    rules: {
      managed_anime_id: managedAnimeID,
      sections: {
        parser: { mode: "inherit" },
        release_filter: { mode: "inherit" },
        file_filter: { mode: "inherit" },
        naming: { mode: "inherit" },
        file_effect: { mode: "inherit" },
      },
      effective: {
        parser: { episode_patterns: [] },
        release_filter: { must_contain: "", not_in: "" },
        file_filter: { must_contain: "", not_in: "" },
        naming: {
          language: "js-naming-1",
          unknown: { directory_template: "", video_filename_template: "" },
          tv: { directory_template: "{title}", video_filename_template: "{title}" },
          movie: { directory_template: "{title}", video_filename_template: "{title}" },
          subtitle_suffix_template: "",
        },
        file_effect: { operation: "hardlink", collision_policy: "versioned" },
      },
      version: 1,
    },
    rss_sources: [],
    rss_source_runtimes: [],
    releases: [],
    download_tasks: [],
    artifacts: [],
    file_operations: [],
    logs: [],
    episode_progress: null,
  };
}

function installCatalogFetchRoutes() {
  const originalFetch = fetchMock;
  fetchMock = vi.fn<FetchStub>(async (input, init) => {
    const url = String(input);
    const catalogMatch = url.match(/\/catalog\/providers\/(mikan|anibt)\/seasons\/[^/?]+$/);
    if (catalogMatch && init?.method === "POST") {
      return new Response(JSON.stringify(catalogWorkspace(catalogMatch[1] as "mikan" | "anibt").snapshot), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (catalogMatch && (!init?.method || init.method === "GET")) {
      return new Response(JSON.stringify(catalogWorkspace(catalogMatch[1] as "mikan" | "anibt")), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    return originalFetch(input, init);
  });
  vi.stubGlobal("fetch", fetchMock);
}

function stubFetch() {
  fetchMock = vi.fn<FetchStub>(async (input, init) => {
    const url = String(input);
    const searchURL = new URL(url, "http://localhost");
    if (searchURL.pathname.endsWith("/config/business-preset")) {
      return new Response(JSON.stringify(businessPreset), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    // The typed client uses the OpenAPI `q` query parameter (not `query`).
    if (searchURL.pathname.match(/\/catalog\/providers\/[^/]+\/search$/)) {
      expect(searchURL.searchParams.get("q")).toBeTruthy();
      expect(searchURL.searchParams.get("query")).toBeNull();
      searchCalled = true;
      return new Response(JSON.stringify(searchResult.body), {
        status: searchResult.status ?? 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    // Each rule segment is the draft's own value (editable); the naming preview requests the server; the preview result is not of interest here.
    if (url.includes("/template-previews/") || url.includes("/template-catalogs/")) {
      return new Response(JSON.stringify({ code: "IGNORED", message: "ignored" }), { status: 400, headers: { "Content-Type": "application/json" } });
    }
    if (url.includes("/catalog/suppressions")) {
      // Mirrors the server: PUT/DELETE really write the in-memory table, visible to subsequent GETs
      //   and to the catalog merge. The ref segment is encoded with encodePathSegment (e.g.
      //   mikan%3Asample-giant) and decoded before being stored.
      const match = url.match(/\/catalog\/suppressions\/([^/]+)\/([^/?]+)/);
      const provider = match ? decodeURIComponent(match[1]!) : undefined;
      const ref = match ? decodeURIComponent(match[2]!) : undefined;
      if (init?.method === "PUT" && provider && ref) {
        const body = init.body ? (JSON.parse(String(init.body)) as { title_snapshot?: string }) : {};
        suppressions = [
          ...suppressions.filter((s) => !(s.provider === provider && s.provider_entry_ref === ref)),
          {
            id: `suppression:${provider}:${ref}`,
            provider,
            provider_entry_ref: ref,
            title_snapshot: body.title_snapshot ?? "",
            suppressed_at: "2026-07-15T12:00:00Z",
            version: 1,
          },
        ];
        return new Response(JSON.stringify(suppressions.at(-1)), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (init?.method === "DELETE" && provider && ref) {
        suppressions = suppressions.filter((s) => !(s.provider === provider && s.provider_entry_ref === ref));
        return new Response(null, { status: 204 });
      }
      return new Response(JSON.stringify(suppressions), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (url.includes("/anime-config-candidates")) {
      // Activating a candidate: mirrors the server, writing activated_managed_anime_id back to the candidate row.
      if (init?.method === "POST" && url.includes("/subscribe")) {
        const id = url.split("/anime-config-candidates/")[1]?.split("/")[0];
        const target = candidates.find((item) => item.id === id);
        if (target) {
          target.activated_managed_anime_id = `anime:${id}`;
          target.status = "dormant";
          // Mirrors the server: creates as not-yet-started (pending) by default; start: true runs it right away.
          const input = JSON.parse(String(init.body || "{}")) as { initial_state?: string; start?: boolean };
          target.subscription_state = input.initial_state ?? (input.start ? "active" : "pending");
          target.subscription_version = 1;
        }
        const targetDraft = asRecord(target?.draft);
        const targetMetadata = asRecord(targetDraft.metadata);
        return new Response(JSON.stringify({
          managed_anime: { id: `anime:${id}`, title: target?.title ?? "测试番剧", title_override: null, version: 1 },
          subscription: {
            id: `subscription:${id}`,
            managed_anime_id: `anime:${id}`,
            origin: target?.origin ?? "custom",
            origin_entry_ref: target?.origin_entry_ref ?? null,
            instance_label: target?.instance_label ?? null,
            state: target?.subscription_state ?? "active",
            version: target?.subscription_version ?? 1,
          },
          metadata: { managed_anime_id: `anime:${id}`, bgm: targetMetadata.bgm ?? null, tmdb: targetMetadata.tmdb ?? null, version: 1 },
          rss_sources: [],
          effective_config_versions: {
            subgroups: 1,
            parser: 1,
            release_filter: 1,
            file_filter: 1,
            subtitle: 1,
            naming: 1,
            file_effect: 1,
          },
        }), {
          status: 201,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (init?.method === "POST" && candidateSaveError) {
        return new Response(JSON.stringify(candidateSaveError.body), {
          status: candidateSaveError.status,
          headers: { "Content-Type": "application/json" },
        });
      }
      // Saving a new candidate: mirrors the server; custom always creates (no origin_entry_ref).
      if (init?.method === "POST") {
        const body = JSON.parse(String(init.body)) as { draft: UnknownRecord };
        const created: UnknownRecord = {
          id: "cand-new",
          origin: body.draft.origin,
          origin_entry_ref: body.draft.origin_entry_ref ?? null,
          title: body.draft.title,
          title_override: body.draft.title_override ?? null,
          instance_label: body.draft.instance_label ?? null,
          draft: body.draft,
          version: 1,
          activated_managed_anime_id: null,
          created_at: "2026-07-01T00:00:00Z",
          updated_at: "2026-07-01T00:00:00Z",
          status: "working",
          draft_digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
          last_activity: "2026-07-01T00:00:00Z",
          retention_until: "2026-07-31T00:00:00Z",
        };
        candidates.push(created);
        return new Response(JSON.stringify(created), {
          status: 201,
          headers: { "Content-Type": "application/json" },
        });
      }
      // CAS write-back: mirrors PatchAnimeConfigCandidate, updating origin_entry_ref along with the draft.
      if (init?.method === "PATCH") {
        const id = url.split("/anime-config-candidates/")[1]?.split("/")[0]?.split("?")[0];
        const target = candidates.find((item) => item.id === id);
        if (!target) {
          return new Response(JSON.stringify({ code: "NOT_FOUND", message: "候选不存在" }), {
            status: 404,
            headers: { "Content-Type": "application/json" },
          });
        }
        const body = JSON.parse(String(init.body)) as { draft: Record<string, unknown> };
        Object.assign(target, {
          origin: body.draft.origin,
          origin_entry_ref: body.draft.origin_entry_ref ?? null,
          title: body.draft.title,
          draft: body.draft,
          version: (target.version as number) + 1,
          status: "working",
          last_activity: "2026-07-01T00:00:00Z",
        });
        return new Response(JSON.stringify(target), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (init?.method === "DELETE") {
        const id = url.split("/anime-config-candidates/")[1]?.split("/")[0];
        candidates = candidates.filter((candidate) => candidate.id !== id);
        return new Response(null, { status: 204 });
      }
      // The candidate list (including ?origin=custom filtering) returns the shared contract wrapper; single reads return one candidate.
      const candidateID = url.split("/anime-config-candidates/")[1]?.split("/")[0];
      if (candidateID) {
        const candidate = candidates.find((item) => item.id === candidateID);
        return new Response(JSON.stringify(candidate ?? { code: "NOT_FOUND", message: "候选不存在" }), {
          status: candidate ? 200 : 404,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response(JSON.stringify({ candidates }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    // When animeListError is injected, listAnime fails.
    if (animeListError && (!init?.method || init.method === "GET") && url.endsWith("/anime")) {
      return new Response(JSON.stringify(animeListError.body), {
        status: animeListError.status,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (url.endsWith("/anime") && (!init?.method || init.method === "GET")) {
      return new Response(JSON.stringify(currentAnimeList()), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    // The workspace read model read when a subscribed card opens the workspace dialog (retired reads this same one).
    const workspaceMatch = url.match(/\/anime\/([^/?]+)\/workspace$/);
    if (workspaceMatch && (!init?.method || init.method === "GET")) {
      return new Response(JSON.stringify(animeWorkspaceFor(decodeURIComponent(workspaceMatch[1]!))), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    // Duplicating a subscription: mirrors the server, creating a separate not-yet-started subscription (same catalog entry, its own note).
    const duplicateMatch = url.match(/\/anime\/([^/]+)\/duplicate$/);
    if (duplicateMatch && init?.method === "POST") {
      const sourceID = decodeURIComponent(duplicateMatch[1]!);
      const source = candidates.find((item) => item.activated_managed_anime_id === sourceID);
      const body = JSON.parse(String(init.body || "{}")) as { instance_label: string };
      const copy = { ...structuredClone(source ?? {}), id: "cand-copy", activated_managed_anime_id: "anime:copy", instance_label: body.instance_label, subscription_state: "pending", subscription_version: 1 };
      candidates.push(copy);
      return new Response(JSON.stringify({
        managed_anime: { id: "anime:copy", title: copy.title, title_override: null, version: 1 },
        subscription: { id: "subscription:cand-copy", managed_anime_id: "anime:copy", origin: copy.origin, origin_entry_ref: copy.origin_entry_ref ?? null, instance_label: body.instance_label, state: "pending", lifecycle_state: "active", lifecycle_epoch: 1, version: 1 },
        metadata: { managed_anime_id: "anime:copy", bgm: null, tmdb: null, version: 1 },
        rss_sources: [],
        effective_config_versions: { subgroups: 1, parser: 1, release_filter: 1, file_filter: 1, naming: 1, file_effect: 1 },
      }), { status: 201, headers: { "Content-Type": "application/json" } });
    }
    if (/\/anime\/([^/]+)\/subscription$/.test(url) && init?.method === "DELETE") {
      const managedAnimeID = decodeURIComponent(url.match(/\/anime\/([^/]+)\/subscription$/)![1]!);
      const target = candidates.find((item) => item.activated_managed_anime_id === managedAnimeID);
      const body = JSON.parse(String(init.body || "{}")) as { expected_version?: number };
      if (!target || body.expected_version !== Number(target.subscription_version ?? 1)) {
        return new Response(JSON.stringify({ code: "CONFLICT", message: "订阅版本已变化" }), {
          status: 409,
          headers: { "Content-Type": "application/json" },
        });
      }
      deletedManagedAnimeIDs.add(managedAnimeID);
      return new Response(null, { status: 204 });
    }
    if (/\/anime\/([^/]+)\/subscription$/.test(url) && init?.method === "PATCH") {
      const managedAnimeID = decodeURIComponent(url.match(/\/anime\/([^/]+)\/subscription$/)![1]!);
      const target = candidates.find((item) => item.activated_managed_anime_id === managedAnimeID);
      const body = JSON.parse(String(init.body || "{}")) as { expected_version?: number; state: "active" | "stopped" };
      if (!target || body.expected_version !== Number(target.subscription_version ?? 1)) {
        return new Response(JSON.stringify({ code: "CONFLICT", message: "订阅版本已变化" }), {
          status: 409,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (target) {
        const body = JSON.parse(String(init.body)) as { state: "active" | "stopped" };
        target.subscription_state = body.state;
        target.subscription_version = (target.subscription_version as number) + 1;
        return new Response(JSON.stringify({
          id: `subscription:${target.id}`,
          managed_anime_id: managedAnimeID,
          origin: target.origin,
          origin_entry_ref: target.origin_entry_ref,
          instance_label: target.instance_label,
          state: target.subscription_state,
          version: target.subscription_version,
        }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response(JSON.stringify({ code: "NOT_FOUND", message: "订阅不存在" }), { status: 404, headers: { "Content-Type": "application/json" } });
    }
    return new Response(JSON.stringify([]), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
}


// The fixture getCatalog sleeps 120ms internally, and the hide path triggers another round of catalog invalidation and re-read.
async function flush() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(150);
    for (let index = 0; index < 10; index += 1) {
      await Promise.resolve();
    }
  });
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

// Opens a card's menu. Every case in this file runs under fake timers, so waitFor's polling
//   timers never advance and findBy* would hang until the case times out; the menu itself is
//   mounted onto the Portal synchronously after the click, so it suffices to flush the
//   microtasks after clicking and then take the items with a synchronous getByRole.
async function openMenu(trigger: HTMLElement) {
  // Base UI's menu trigger opens on mousedown, while the actual expansion happens in an animation
  //   frame it schedules (useClick -> AnimationFrame). This file runs under fake timers, where
  //   waitFor/findBy* polling timers never advance, so we manually advance one frame here and then
  //   take the menu items with synchronous queries.
  fireEvent.mouseDown(trigger);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(50);
    await Promise.resolve();
    await Promise.resolve();
  });
}

async function flushLazyEntry(kind: "config" | "add" | "confirm") {
  await act(async () => {
    if (kind === "config") await import("../anime-config/AnimeConfigDialogEntry");
    if (kind === "add") await import("./AddCandidateDialogEntry");
    if (kind === "confirm") await import("../../ui/ConfirmDialogEntry");
    await Promise.resolve();
    await Promise.resolve();
  });
}

async function renderGrid(initialEntry = "/?source=mikan") {
  const { AnimeGrid } = await import("./AnimeGrid");
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  // Data router: the route-hosted anime config uses useBlocker (RouteDialogLeaveGuard).
  const router = createMemoryRouter([{ path: "*", element: <AnimeGrid /> }], { initialEntries: [initialEntry] });
  const rendered = render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { ...rendered, queryClient: client, router };
}

describe("AnimeGrid（fixture 目录）", () => {
  beforeEach(() => {
    vi.resetModules();
    vi.useFakeTimers();
    vi.setSystemTime(FIXED_NOW);
    vi.clearAllMocks();
    // Clears the catalog tab memory so cases do not leak a tab across boundaries (read when ?source= is absent).
    window.sessionStorage.clear();
    suppressions = [];
    candidates = [];
    extraCatalogItems = [];
    deletedManagedAnimeIDs = new Set();
    candidateSaveError = undefined;
    searchResult = { status: 422, body: { code: "SEARCH_NOT_SUPPORTED", message: "先搜索" } };
    searchCalled = false;
    stubFetch();
    installCatalogFetchRoutes();
  });

  afterEach(async () => {
    // Base UI's animation-frame scheduler is a process-level singleton: an rAF that did not finish
    //   before the fake timers are torn down leaves it permanently believing a frame is scheduled,
    //   and later cases' menus/dialogs never mount. Tear down by advancing that frame first.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(50);
    });
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("切换分栏时分别保留标题筛选，不静默清空", async () => {
    // Since the split tabs moved to the top bar (the shell/catalogTabs shared URL state), the page
    //   no longer renders tab buttons; we switch by address here, covering the same ?source= state
    //   path.
    const { router } = await renderGrid();
    await flush();

    const filter = screen.getByPlaceholderText(t("grid.filterPlaceholder"));
    fireEvent.change(filter, { target: { value: "Mikan 条件" } });
    expect(filter).toHaveValue("Mikan 条件");

    await act(async () => {
      await router.navigate("/?source=anibt", { replace: true });
    });
    await flush();
    const anibtFilter = screen.getByPlaceholderText(t("grid.filterPlaceholder"));
    expect(anibtFilter).toHaveValue("");
    fireEvent.change(anibtFilter, { target: { value: "AniBT 条件" } });

    await act(async () => {
      await router.navigate("/?source=mikan", { replace: true });
    });
    await flush();
    expect(screen.getByPlaceholderText(t("grid.filterPlaceholder"))).toHaveValue("Mikan 条件");

    await act(async () => {
      await router.navigate("/?source=anibt", { replace: true });
    });
    await flush();
    expect(screen.getByPlaceholderText(t("grid.filterPlaceholder"))).toHaveValue("AniBT 条件");
  }, 15000);

  it("站点目录按播出形式分行：剧场版、OVA 不进星期行（Mikan 分区 7/8）", async () => {
    extraCatalogItems = [
      {
        kind: "mikan_entry", provider: "mikan", title: "剧场版条目", provider_entry_ref: "mikan:movie", bgm_id: null,
        poster_url: null, cover_url: null, broadcast_day: "none", release_format: "movie",
        catalog_state: "present", preference: "neutral", allowed_actions: ["prepare_subscribe"], instances: [],
      },
      {
        kind: "mikan_entry", provider: "mikan", title: "OVA 条目", provider_entry_ref: "mikan:ova", bgm_id: null,
        poster_url: null, cover_url: null, broadcast_day: "none", release_format: "ova",
        catalog_state: "present", preference: "neutral", allowed_actions: ["prepare_subscribe"], instances: [],
      },
    ];
    const { container } = await renderGrid();
    await flush();
    const rows = container.querySelectorAll("section[data-wall-row]");
    expect([...rows].map((row) => row.querySelector("h3")?.textContent)).toEqual([
      t("grid.weekday.sat"),
      t("grid.wallRow.movie"),
      t("grid.wallRow.ova"),
    ]);
    expect(within(rows[1] as HTMLElement).getByText("剧场版条目")).toBeInTheDocument();
  });

  it("「只看可用」隐藏站点标明没有种子的条目，未知的照常显示", async () => {
    extraCatalogItems = [
      {
        kind: "mikan_entry", provider: "mikan", title: "暂无种子条目", provider_entry_ref: "mikan:empty", bgm_id: null,
        poster_url: null, cover_url: null, broadcast_day: "sat", release_state: "no_releases",
        catalog_state: "present", preference: "neutral", allowed_actions: ["prepare_subscribe"], instances: [],
      },
    ];
    await renderGrid();
    await flush();
    expect(screen.getByText("暂无种子条目")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("grid.availableOnly"), pressed: false }));
    await flush();
    expect(screen.queryByText("暂无种子条目")).not.toBeInTheDocument();
    expect(screen.getByText(catalogFixtures.mikan.title)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("grid.availableOnly") })).toHaveAttribute("aria-pressed", "true");
  });

  it("两区布局：站点目录与已订阅都按天分成横向行", async () => {
    const { container, unmount } = await renderGrid();
    await flush();

    // The browse area (Mikan): rows split by day like the subscribed wall, no longer a multi-column grid
    expect(container.querySelector("ul.catalog-grid-browse")).toBeNull();
    const browseRows = container.querySelectorAll("section[data-wall-row]");
    expect([...browseRows].map((row) => row.querySelector("h3")?.textContent)).toEqual([t("grid.weekday.sat")]);
    unmount();

    // The subscribed (action) area: one horizontally scrolling track per row; cards stay out of the browse area's multi-column grid
    candidates = [mikanActivatedCandidate];
    const wide = await renderGrid("/?source=all");
    await flush();
    expect(wide.container.querySelector("ul.catalog-grid-browse")).toBeNull();
    expect(wide.container.querySelectorAll("section[data-wall-row] ul").length).toBeGreaterThan(0);
  });

  it("目录只展示手动刷新，不展示自动刷新开关", async () => {
    await renderGrid();
    await flush();

    expect(screen.getByRole("button", { name: t("grid.refreshSite", { site: "Mikan" }) })).toBeInTheDocument();
    expect(screen.queryByText(/自动刷新/)).not.toBeInTheDocument();
  });

  it("工具条：刷新是次按钮，没有目录内搜索，筛选框只有图标和占位文字（UI 评审 C2/C3/B2）", async () => {
    await renderGrid();
    await flush();

    const refresh = screen.getByRole("button", { name: t("grid.refreshSite", { site: "Mikan" }) });
    expect(refresh).toHaveClass("tw:bg-card");
    expect(refresh.className).not.toMatch(/(^| )tw:bg-(accent|primary)/);
    expect(screen.queryByRole("button", { name: t("grid.search") })).not.toBeInTheDocument();
    const filter = screen.getByRole("searchbox", { name: t("grid.filter") });
    expect(filter).toHaveAttribute("placeholder", t("grid.filterPlaceholder"));
    expect(screen.queryByText(t("grid.filter"))).not.toBeInTheDocument();
  });

  it("站点状态写成「n 部 · 多久前更新」，读取完成前不显示", async () => {
    const base = fetchMock;
    let release!: () => void;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    fetchMock = vi.fn<FetchStub>(async (input, init) => {
      const url = String(input);
      if (/\/catalog\/providers\/mikan\/seasons\/[^/?]+$/.test(url) && (!init?.method || init.method === "GET")) {
        await gate;
        const workspace = catalogWorkspace("mikan");
        return Response.json({ ...workspace, snapshot: { ...workspace.snapshot, last_success_at: "2026-07-15T09:00:00Z", stale: false } });
      }
      return base(input, init);
    });
    vi.stubGlobal("fetch", fetchMock);
    await renderGrid();
    await flush();
    expect(screen.queryByText(new RegExp(t("grid.snapshot.noneLabel")))).not.toBeInTheDocument();
    expect(screen.getByText(t("grid.loading"))).toBeInTheDocument();

    release();
    await flush();
    const ago = t("grid.snapshot.updatedAgo", { time: t("common.timeAgo.hours", { n: 3 }) });
    expect(screen.getByText(`${t("grid.snapshot.freshDetail", { n: 1 })} · ${ago}`)).toBeInTheDocument();
  });

  it("快照显示最近一次成功刷新的相对时间", async () => {
    const base = fetchMock;
    fetchMock = vi.fn<FetchStub>(async (input, init) => {
      const url = String(input);
      if (/\/catalog\/providers\/mikan\/seasons\/[^/?]+$/.test(url) && (!init?.method || init.method === "GET")) {
        const workspace = catalogWorkspace("mikan");
        return Response.json({ ...workspace, snapshot: { ...workspace.snapshot, last_success_at: "2026-07-15T09:00:00Z", stale: false } });
      }
      return base(input, init);
    });
    vi.stubGlobal("fetch", fetchMock);
    await renderGrid();
    await flush();

    expect(screen.getByText(new RegExp(t("grid.snapshot.updatedAgo", { time: t("common.timeAgo.hours", { n: 3 }) })))).toBeInTheDocument();
  });

  it.each([
    ["outage", "error", "grid.refreshOutage"],
    ["withdrawn", "warning", "grid.refreshWithdrawn"],
    ["zero_result", "info", "grid.refreshZeroResult"],
    ["fresh", "success", "grid.refreshed"],
  ] as const)("手动刷新按返回状态 %s 提示，并只重读一次目录", async (status, level, key) => {
    const base = fetchMock;
    fetchMock = vi.fn<FetchStub>(async (input, init) => {
      const url = String(input);
      if (url.endsWith("/catalog/providers/mikan/seasons/2026-summer/refresh") && init?.method === "POST") {
        return Response.json({ ...catalogWorkspace("mikan").snapshot, status, stale: false });
      }
      return base(input, init);
    });
    vi.stubGlobal("fetch", fetchMock);
    await renderGrid();
    await flush();
    const catalogReads = () => fetchMock.mock.calls.filter(
      ([input, init]) => /\/catalog\/providers\/mikan\/seasons\/[^/?]+$/.test(String(input)) && (!init?.method || init.method === "GET"),
    ).length;
    const before = catalogReads();

    fireEvent.click(screen.getByRole("button", { name: t("grid.refreshSite", { site: "Mikan" }) }));
    await flush();

    const { toast } = await import("sonner");
    const message = key === "grid.refreshed" ? t(key) : t(key, { site: "Mikan" });
    expect(toast[level]).toHaveBeenCalledWith(message);
    expect(catalogReads() - before).toBe(1);
  });

  it("已隐藏的番剧不出现在列表里，也没有显示它们的开关", async () => {
    suppressions = [
      {
        provider: "mikan",
        provider_entry_ref: "mikan:sample-giant",
        title_snapshot: "进击的巨人",
        suppressed_at: "2026-07-10T00:00:00Z",
      },
    ];
    await renderGrid();
    await flush();

    expect(screen.queryByRole("button", { name: t("grid.card.openConfig", { title: "进击的巨人" }) })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /隐藏/ })).not.toBeInTheDocument();
  });

  it("未隐藏条目点卡片直接打开四 Tab 配置弹窗（无一级详情弹窗）", async () => {
    await renderGrid();
    await flush();

    fireEvent.click(screen.getByRole("button", { name: t("grid.card.openConfig", { title: "进击的巨人" }) }));
    await flushLazyEntry("config");
    await flush();

    // DraftBody opens directly via tab, without a first-level "open details" dialog
    expect(screen.getByRole("tab", { name: t("tabs.names.metadata") })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: t("field.subgroup") })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: t("tabs.names.rules") })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: t("tabs.names.rssPreview") })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "高级配置" })).not.toBeInTheDocument();
  }, 15000);

  it("番剧配置由路由承载：打开写入 ?anime=&tab=，切页签替换 tab，关闭回到底座并保留 ?source=", async () => {
    const { router } = await renderGrid();
    await flush();
    const search = () => new URLSearchParams(router.state.location.search);

    fireEvent.click(screen.getByRole("button", { name: t("grid.card.openConfig", { title: "进击的巨人" }) }));
    await flushLazyEntry("config");
    await flush();
    expect(screen.getByRole("dialog", { name: "进击的巨人" })).toBeInTheDocument();
    expect(search().get("anime")).toBe("mikan:sample-giant");
    expect(search().get("tab")).toBe("metadata");
    expect(search().get("source")).toBe("mikan");
    expect(router.state.historyAction).toBe("PUSH");

    fireEvent.click(screen.getByRole("tab", { name: t("tabs.names.rules") }));
    await flush();
    expect(search().get("tab")).toBe("rules");
    expect(router.state.historyAction).toBe("REPLACE");

    // A browser back (with no unsaved changes) closes the dialog directly, restoring the base address from before it opened.
    await act(() => router.navigate(-1));
    await flush();
    expect(screen.queryByRole("dialog", { name: "进击的巨人" })).not.toBeInTheDocument();
    expect(router.state.location.search).toBe("?source=mikan");
  }, 15000);

  it("深链 ?anime=&tab= 刷新后恢复弹窗与页签；关闭替换为底座地址", async () => {
    const { router } = await renderGrid("/?source=mikan&anime=mikan%3Asample-giant&tab=rules");
    await flush();
    await flushLazyEntry("config");
    await flush();

    expect(screen.getByRole("dialog", { name: "进击的巨人" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: t("tabs.names.rules") })).toHaveAttribute("aria-selected", "true");

    const dialog = screen.getByRole("dialog", { name: "进击的巨人" });
    fireEvent.click(within(dialog).getAllByRole("button", { name: t("act.close") })[0]!);
    await flush();
    expect(screen.queryByRole("dialog", { name: "进击的巨人" })).not.toBeInTheDocument();
    expect(router.state.location.search).toBe("?source=mikan");
    expect(router.state.historyAction).toBe("REPLACE");
  }, 15000);

  it("深链到无法解析的目录行：去掉参数，不打开弹窗", async () => {
    const { router } = await renderGrid("/?source=mikan&anime=mikan%3Ano-such-entry&tab=rss");
    await flush();
    await flush();
    expect(router.state.location.search).toBe("?source=mikan");
    expect(screen.queryByRole("tab", { name: t("tabs.names.metadata") })).not.toBeInTheDocument();
  }, 15000);

  it("深链：已隐藏且未订阅的条目不打开配置，提示并去掉参数", async () => {
    suppressions = [
      {
        provider: "mikan",
        provider_entry_ref: "mikan:sample-giant",
        title_snapshot: "进击的巨人",
        suppressed_at: "2026-07-10T00:00:00Z",
      },
    ];
    const { router } = await renderGrid("/?source=mikan&anime=mikan%3Asample-giant");
    await flush();
    await flush();
    const { toast } = await import("sonner");
    expect(toast.info).toHaveBeenCalledWith(t("grid.blockedFirst"));
    expect(router.state.location.search).toBe("?source=mikan");
    expect(screen.queryByRole("tab", { name: t("tabs.names.metadata") })).not.toBeInTheDocument();
  }, 15000);

  it("有未保存修改时浏览器返回先走放弃确认：取消保持打开且地址恢复，放弃才关闭", async () => {
    const { router } = await renderGrid();
    await flush();
    fireEvent.click(screen.getByRole("button", { name: t("grid.card.openConfig", { title: "进击的巨人" }) }));
    await flushLazyEntry("config");
    await flush();
    const openedSearch = router.state.location.search;

    const titleRegion = screen.getByRole("region", { name: t("tabs.metadata.title") });
    fireEvent.click(within(titleRegion).getByRole("button", { name: t("tabs.metadata.editTitleAria") }));
    fireEvent.change(within(titleRegion).getByLabelText(t("tabs.metadata.title")), { target: { value: "改名番" } });
    fireEvent.click(within(titleRegion).getByRole("button", { name: t("tabs.metadata.saveTitle") }));
    await flush();
    expect(screen.getByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument();

    await act(() => router.navigate(-1));
    await flush();
    const confirm = screen.getByRole("alertdialog");
    expect(within(confirm).getByText(t("common.discardEditsTitle"))).toBeInTheDocument();

    fireEvent.click(within(confirm).getByRole("button", { name: t("act.cancel") }));
    await flush();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "进击的巨人" })).toBeInTheDocument();
    expect(router.state.location.search).toBe(openedSearch);
    expect(screen.getByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument();

    await act(() => router.navigate(-1));
    await flush();
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: t("common.discardChanges") }));
    await flush();
    expect(screen.queryByRole("dialog", { name: "进击的巨人" })).not.toBeInTheDocument();
    expect(router.state.location.search).toBe("?source=mikan");
  }, 15000);

  it("retired 实例点卡片打开同一个只读 workspace，不再被 toast 挡住", async () => {
    candidates = [{ ...mikanActivatedCandidate, lifecycle_state: "retired", lifecycle_epoch: 2 }];
    const { router } = await renderGrid("/?source=all");
    await flush();

    const { toast } = await import("sonner");
    expect(screen.getByText(t("anime.state.ended"))).toBeInTheDocument();
    // The menu does not repeat the read-only viewing entry: the poster click IS it.
    await openMenu(screen.getByRole("button", { name: t("grid.card.moreFor", { title: "进击的巨人" }) }));
    expect(screen.getByRole("menuitem", { name: t("catalog.deleteLocal") })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /查看|History|View/ })).not.toBeInTheDocument();
    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
    await flush();

    fireEvent.click(screen.getByRole("button", { name: t("grid.card.viewFor", { title: "进击的巨人" }) }));
    await flushLazyEntry("config");
    await flush();
    expect(router.state.location.search).toContain("anime=");
    expect(screen.getByRole("dialog", { name: "进击的巨人" })).toBeInTheDocument();

    // What opens is the read-only workspace: the retired banner is visible and every mutation entry is omitted.
    expect(toast.info).not.toHaveBeenCalledWith("该实例已退役，仅保留历史记录，不能再修改配置");
    expect(screen.getByText(t("anime.ended.banner"))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: new RegExp(t("rss.refresh")) })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^保存/ })).not.toBeInTheDocument();
  });

  it("隐藏成功 toast 指向已隐藏的番剧", async () => {
    await renderGrid();
    await flush();

    const { toast } = await import("sonner");
    await openMenu(screen.getByRole("button", { name: t("grid.card.moreFor", { title: "进击的巨人" }) }));
    fireEvent.click(screen.getByRole("menuitem", { name: t("grid.card.hide") }));
    await flush();
    expect(toast.success).toHaveBeenCalledWith(t("grid.blacklistAdded"));
  });

  // The subscription action area (the subscribed tab) has no hide entry. Hiding in the browse area
  //   is catalog curation only: a subscribed entry also just gets a suppression record, without
  //   unsubscribing, and the subscribed wall keeps showing it.
  const mikanActivatedCandidate = {
    ...customCandidateFixture,
    id: "cand-m1",
    origin: "mikan",
    origin_entry_ref: "mikan:sample-giant",
    title: "进击的巨人",
    instance_label: null,
    activated_managed_anime_id: "anime:cand-m1",
    status: "dormant",
    draft: {
      ...customCandidateFixture.draft,
      origin: "mikan",
      origin_entry_ref: "mikan:sample-giant",
      title: "进击的巨人",
      instance_label: null,
    },
  };

  // A second existing owner tied to the same external provider entry (mikan:sample-giant), e.g. a
  //   duplicated subscription: its managed id and subscription version both differ from the first
  //   one's, proving that actions on the subscribed wall target by owner precisely while the browse
  //   area shows only one candidate card.
  const mikanSecondOwner = {
    ...mikanActivatedCandidate,
    id: "cand-m2",
    activated_managed_anime_id: "anime:cand-m2",
    subscription_version: 5,
  };

  it("订阅列表（已订阅 tab）不提供隐藏入口", async () => {
    candidates = [mikanActivatedCandidate];
    await renderGrid("/?source=all");
    await flush();

    expect(screen.getByText("进击的巨人")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("grid.card.hide") })).not.toBeInTheDocument();
  });

  // Whether a subscription exists at all is what matters, not its state: an entry with any
  //   subscription (including the duplicated one) yields exactly one candidate card, marked
  //   "subscribed", with no subscription state.
  it("浏览区：有订阅的条目只出一张卡，只标已订阅，没有订阅状态", async () => {
    candidates = [{ ...mikanActivatedCandidate }, { ...mikanSecondOwner, subscription_state: "stopped" }];
    await renderGrid("/?source=mikan");
    await flush();

    expect(screen.getAllByRole("button", { name: t("grid.card.openConfig", { title: "进击的巨人" }) })).toHaveLength(1);
    expect(screen.getByText(t("grid.card.subscribed"))).toBeInTheDocument();
    for (const state of ["pending", "active", "stopped", "ended"] as const) {
      expect(screen.queryByText(t(`anime.state.${state}`))).not.toBeInTheDocument();
    }
  });

  it("浏览区隐藏有订阅的条目：只写隐藏记录，不确认、不结束订阅", async () => {
    candidates = [{ ...mikanActivatedCandidate }, { ...mikanSecondOwner }];
    await renderGrid("/?source=mikan");
    await flush();

    await openMenu(screen.getByRole("button", { name: t("grid.card.moreFor", { title: "进击的巨人" }) }));
    fireEvent.click(screen.getByRole("menuitem", { name: t("grid.card.hide") }));
    await flush();
    await flush();

    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    const puts = fetchMock.mock.calls.filter(
      ([input, init]) => String(input).includes("/catalog/suppressions/mikan/") && init?.method === "PUT",
    );
    expect(puts).toHaveLength(1);
    const deletes = fetchMock.mock.calls.filter(
      ([input, init]) => init?.method === "DELETE" && /\/anime\/[^/]+\/subscription$/.test(String(input)),
    );
    expect(deletes).toHaveLength(0);
    expect(deletedManagedAnimeIDs.size).toBe(0);
    const { toast } = await import("sonner");
    expect(toast.success).toHaveBeenCalledWith(t("grid.blacklistAdded"));
    // After hiding, gone from the browse area.
    expect(screen.queryByRole("button", { name: t("grid.card.openConfig", { title: "进击的巨人" }) })).not.toBeInTheDocument();
  });

  it("已隐藏的番剧仍在已订阅墙上：每个订阅各一张卡，状态与操作照旧", async () => {
    suppressions = [
      {
        provider: "mikan",
        provider_entry_ref: "mikan:sample-giant",
        title_snapshot: "进击的巨人",
        suppressed_at: "2026-07-10T00:00:00Z",
      },
    ];
    candidates = [{ ...mikanActivatedCandidate }, { ...mikanSecondOwner }];
    await renderGrid("/?source=all");
    await flush();

    expect(screen.getAllByRole("button", { name: t("grid.card.moreFor", { title: "进击的巨人" }) })).toHaveLength(2);
  });

  it("结束订阅需确认，确认后实例退役并刷新列表", async () => {
    candidates = [{ ...mikanActivatedCandidate }];
    const { queryClient } = await renderGrid("/?source=all");
    await flush();
    // A workspace read before retirement must not reopen the retired owner as editable.
    queryClient.setQueryData(["anime", "workspace", "cached-owner"], { subscription: { lifecycle_state: "active" } });

    await openMenu(screen.getByRole("button", { name: t("grid.card.moreFor", { title: "进击的巨人" }) }));
    fireEvent.click(screen.getByRole("menuitem", { name: t("catalog.endConfirm") }));
    await flushLazyEntry("confirm");
    await flush();

    // The confirm dialog appears; cancelling does not delete
    expect(screen.getByText(t("catalog.endTitle", { title: "进击的巨人" }))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("act.cancel") }));
    await flush();
    expect(screen.getByText("进击的巨人")).toBeInTheDocument();

    // Re-open and confirm: the instance disappears from the subscription list
    await openMenu(screen.getByRole("button", { name: t("grid.card.moreFor", { title: "进击的巨人" }) }));
    fireEvent.click(screen.getByRole("menuitem", { name: t("catalog.endConfirm") }));
    fireEvent.click(screen.getByRole("button", { name: t("catalog.endConfirm"), exact: true }));
    await flush();
    await flush();

    const { toast } = await import("sonner");
    expect(toast.success).toHaveBeenCalledWith(t("catalog.ended", { title: "进击的巨人" }));
    expect(screen.queryByText("进击的巨人")).not.toBeInTheDocument();
    expect(queryClient.getQueryState(["anime", "workspace", "cached-owner"])?.isInvalidated).toBe(true);
  });

  it("卡片停止按钮直接停止订阅实例（不经详情弹窗）", async () => {
    candidates = [{ ...mikanActivatedCandidate }];
    await renderGrid("/?source=all");
    await flush();

    await openMenu(screen.getByRole("button", { name: t("grid.card.moreFor", { title: "进击的巨人" }) }));
    fireEvent.click(screen.getByRole("menuitem", { name: t("grid.card.pause") }));
    await flush();
    await flush();

    const { toast } = await import("sonner");
    expect(toast.success).toHaveBeenCalledWith(t("grid.paused"));
    // Re-opens the menu to verify the switch to a start item after stopping (the menu must be opened again after it closed).
    await openMenu(screen.getByRole("button", { name: t("grid.card.moreFor", { title: "进击的巨人" }) }));
    expect(screen.getByRole("menuitem", { name: t("grid.card.start") })).toBeInTheDocument();
  });

  it("未开始的实例显示「未开始」，卡片菜单「开始订阅」把它开始", async () => {
    candidates = [{ ...mikanActivatedCandidate, subscription_state: "pending" }];
    await renderGrid("/?source=all");
    await flush();

    expect(screen.getAllByText(t("anime.state.pending")).length).toBeGreaterThan(0);
    await openMenu(screen.getByRole("button", { name: t("grid.card.moreFor", { title: "进击的巨人" }) }));
    fireEvent.click(screen.getByRole("menuitem", { name: t("grid.card.begin") }));
    await flush();
    await flush();

    const patches = fetchMock.mock.calls.filter(
      ([input, init]) => init?.method === "PATCH" && /\/anime\/[^/]+\/subscription$/.test(String(input)),
    );
    expect(patches).toHaveLength(1);
    expect(JSON.parse(String(patches[0]![1]?.body))).toMatchObject({ state: "active" });
    const { toast } = await import("sonner");
    expect(toast.success).toHaveBeenCalledWith(t("anime.started"));
    await openMenu(screen.getByRole("button", { name: t("grid.card.moreFor", { title: "进击的巨人" }) }));
    expect(screen.getByRole("menuitem", { name: t("grid.card.pause") })).toBeInTheDocument();
  });

  it("卡片菜单「复制」：填写订阅备注后新建未开始的订阅，并打开它的配置弹窗", async () => {
    candidates = [{ ...mikanActivatedCandidate }];
    const { router } = await renderGrid("/?source=all");
    await flush();

    await openMenu(screen.getByRole("button", { name: t("grid.card.moreFor", { title: "进击的巨人" }) }));
    fireEvent.click(screen.getByRole("menuitem", { name: t("grid.card.duplicate") }));
    await act(async () => {
      await import("./DuplicateSubscriptionDialog");
      await Promise.resolve();
    });
    await flush();
    const dialog = screen.getByRole("dialog", { name: t("grid.duplicateTitle", { title: "进击的巨人" }) });
    const confirm = within(dialog).getByRole("button", { name: t("grid.duplicateConfirm") });
    expect(confirm).toBeDisabled();
    fireEvent.change(within(dialog).getByRole("textbox"), { target: { value: "只订阅一个字幕组" } });
    fireEvent.click(confirm);
    await flush();
    await flushLazyEntry("config");
    await flush();

    const posts = fetchMock.mock.calls.filter(([input, init]) => init?.method === "POST" && String(input).endsWith("/anime/anime%3Acand-m1/duplicate"));
    expect(posts).toHaveLength(1);
    expect(JSON.parse(String(posts[0]![1]?.body))).toEqual({ instance_label: "只订阅一个字幕组" });
    expect(new Headers(posts[0]![1]?.headers).get("Idempotency-Key")).toBeTruthy();
    const { toast } = await import("sonner");
    expect(toast.success).toHaveBeenCalledWith(t("grid.duplicated"));
    expect(new URLSearchParams(router.state.location.search).get("anime")).toBe("anime:copy");
    expect(screen.getByRole("dialog", { name: "进击的巨人" })).toBeInTheDocument();
    expect(screen.getAllByText("只订阅一个字幕组").length).toBeGreaterThan(0);
  }, 15000);

  // Clicking a subscribed candidate card behaves exactly like an unsubscribed one: it opens the
  //   candidate draft (the same provider ref row key), never any subscription's workspace.
  it("浏览区点有订阅的卡片：打开候选草稿，不读任何订阅的工作区", async () => {
    candidates = [{ ...mikanActivatedCandidate }, { ...mikanSecondOwner }];
    const { router } = await renderGrid("/?source=mikan");
    await flush();

    fireEvent.click(screen.getByRole("button", { name: t("grid.card.openConfig", { title: "进击的巨人" }) }));
    await flushLazyEntry("config");
    await flush();

    expect(screen.getByRole("dialog", { name: "进击的巨人" })).toBeInTheDocument();
    const search = new URLSearchParams(router.state.location.search);
    expect(search.get("anime")).toBe("mikan:sample-giant");
    expect(search.get("tab")).toBe("metadata");
    expect(screen.getByRole("tab", { name: t("tabs.names.rssPreview") })).toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([input]) => /\/anime\/[^/]+\/workspace$/.test(String(input)))).toHaveLength(0);
  }, 15000);

  it("同一外部 provider ref 关联的两个 owner：已订阅 tab 按各自 managed id/version 结束订阅", async () => {
    expect(mikanActivatedCandidate.activated_managed_anime_id).not.toBe(mikanSecondOwner.activated_managed_anime_id);
    expect(`subscription:${mikanActivatedCandidate.id}`).not.toBe(`subscription:${mikanSecondOwner.id}`);
    candidates = [{ ...mikanActivatedCandidate }, { ...mikanSecondOwner }];
    await renderGrid("/?source=all");
    await flush();

    const unsubscribeButtons = screen.getAllByRole("button", { name: t("grid.card.moreFor", { title: "进击的巨人" }) });
    expect(unsubscribeButtons).toHaveLength(2);

    await openMenu(unsubscribeButtons[1]!);
    fireEvent.click(screen.getByRole("menuitem", { name: t("catalog.endConfirm") }));
    await flushLazyEntry("confirm");
    await flush();
    expect(screen.getByText(t("catalog.endTitle", { title: "进击的巨人" }))).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: t("catalog.endConfirm"), exact: true }));
    await flush();
    await flush();

    const deletes = fetchMock.mock.calls.filter(
      ([input, init]) => init?.method === "DELETE" && /\/anime\/[^/]+\/subscription$/.test(String(input)),
    );
    expect(deletes).toHaveLength(1);
    expect(decodeURIComponent(String(deletes[0]![0]))).toContain("/anime/anime:cand-m2/subscription");
    expect(JSON.parse(String(deletes[0]![1]?.body))).toEqual({ expected_version: 5 });

    const { toast } = await import("sonner");
    expect(toast.success).toHaveBeenCalledWith(t("catalog.ended", { title: "进击的巨人" }));
    // The other owner is still there: only the one clicked was deleted.
    expect(screen.getAllByRole("button", { name: t("grid.card.moreFor", { title: "进击的巨人" }) })).toHaveLength(1);
  });

  it("结束订阅确认后精确 owner 从最新列表消失：不 DELETE，刷新并提示错误", async () => {
    candidates = [{ ...mikanActivatedCandidate }, { ...mikanSecondOwner }];
    const { queryClient } = await renderGrid("/?source=all");
    await flush();

    await openMenu(screen.getAllByRole("button", { name: t("grid.card.moreFor", { title: "进击的巨人" }) })[1]!);
    fireEvent.click(screen.getByRole("menuitem", { name: t("catalog.endConfirm") }));
    await flushLazyEntry("confirm");
    await flush();
    expect(screen.getByText(t("catalog.endTitle", { title: "进击的巨人" }))).toBeInTheDocument();

    const animeGetsBeforeRefresh = fetchMock.mock.calls.filter(
      ([input, init]) => String(input).endsWith("/anime") && (!init?.method || init.method === "GET"),
    ).length;
    deletedManagedAnimeIDs.add("anime:cand-m2");
    await act(async () => {
      await queryClient.invalidateQueries({ queryKey: ["catalog"] });
    });
    await flush();
    const animeGetsAfterRefresh = fetchMock.mock.calls.filter(
      ([input, init]) => String(input).endsWith("/anime") && (!init?.method || init.method === "GET"),
    ).length;
    expect(animeGetsAfterRefresh).toBeGreaterThan(animeGetsBeforeRefresh);
    expect(screen.getByText(t("catalog.endTitle", { title: "进击的巨人" }))).toBeInTheDocument();

    const deletesBeforeConfirm = fetchMock.mock.calls.filter(
      ([input, init]) => init?.method === "DELETE" && /\/anime\/[^/]+\/subscription$/.test(String(input)),
    ).length;
    fireEvent.click(screen.getByRole("button", { name: t("catalog.endConfirm"), exact: true }));
    await flush();

    const deletesAfterConfirm = fetchMock.mock.calls.filter(
      ([input, init]) => init?.method === "DELETE" && /\/anime\/[^/]+\/subscription$/.test(String(input)),
    ).length;
    const animeGetsAfterConfirm = fetchMock.mock.calls.filter(
      ([input, init]) => String(input).endsWith("/anime") && (!init?.method || init.method === "GET"),
    ).length;
    expect(deletesAfterConfirm).toBe(deletesBeforeConfirm);
    expect(animeGetsAfterConfirm).toBeGreaterThan(animeGetsAfterRefresh);
    const { toast } = await import("sonner");
    expect(toast.error).toHaveBeenCalledWith(t("catalog.locateFailed"));
  });

  it("季度在底座地址里：?season= 决定目录季度，切换季度写回地址", async () => {
    const { router } = await renderGrid("/?source=mikan&season=2026-spring");
    await flush();
    const seasonGets = () =>
      fetchMock.mock.calls
        .map(([input]) => String(input))
        .filter((url) => url.includes("/catalog/providers/mikan/seasons/"));
    expect(seasonGets().some((url) => url.includes("/seasons/2026-spring"))).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "上一季" }));
    await flush();
    expect(new URLSearchParams(router.state.location.search).get("season")).toBe("2026-winter");
    expect(new URLSearchParams(router.state.location.search).get("source")).toBe("mikan");
  });

  it("无效的 ?season= 回退当前季度", async () => {
    await renderGrid("/?source=mikan&season=nonsense");
    await flush();
    const urls = fetchMock.mock.calls.map(([input]) => String(input)).filter((url) => url.includes("/seasons/"));
    expect(urls.length).toBeGreaterThan(0);
    expect(urls.every((url) => !url.includes("nonsense"))).toBe(true);
  });
});

// Custom-preset regression: a save failure must produce toast feedback and leave the form filled;
//   deleting a preset must go through the ConfirmDialog double confirm — cancelling deletes
//   nothing, confirming calls store.delete. The candidate store goes through the
//   /anime-config-candidates fetch stub; store.delete IS the DELETE request.
describe("AnimeGrid 自定义预设", () => {
  beforeEach(() => {
    vi.resetModules();
    vi.useFakeTimers();
    vi.setSystemTime(FIXED_NOW);
    suppressions = [];
    candidates = [];
    extraCatalogItems = [];
    deletedManagedAnimeIDs = new Set();
    candidateSaveError = undefined;
    stubFetch();
    installCatalogFetchRoutes();
  });

  afterEach(async () => {
    // Base UI's animation-frame scheduler is a process-level singleton: an rAF that did not finish
    //   before the fake timers are torn down leaves it permanently believing a frame is scheduled,
    //   and later cases' menus/dialogs never mount. Tear down by advancing that frame first.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(50);
    });
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("添加预设保存失败：toast.error 反馈，表单保留输入不关闭", async () => {
    candidateSaveError = { status: 500, body: { code: "INTERNAL", message: "候选存储不可用" } };
    await renderGrid("/?source=custom");
    await flush();

    // The toolbar and the empty state each offer an "add preset" entry; click either.
    fireEvent.click(screen.getAllByRole("button", { name: t("grid.addPreset") })[0]!);
    await flushLazyEntry("add");
    fireEvent.change(screen.getByLabelText(t("field.title")), { target: { value: "保存失败的番" } });
    fireEvent.change(screen.getByLabelText(t("addPreset.fieldLabel")), { target: { value: "客厅版" } });
    fireEvent.change(screen.getByLabelText(t("addPreset.fieldRss")), { target: { value: "https://example.com/feed.xml" } });
    fireEvent.change(screen.getByLabelText(t("addPreset.fieldGroup")), { target: { value: "某字幕组" } });
    fireEvent.click(screen.getByRole("button", { name: t("addPreset.save") }));
    await flush();

    const { toast } = await import("sonner");
    expect(toast.error).toHaveBeenCalledWith(t("error.INTERNAL"), expect.anything());
    expect(toast.success).not.toHaveBeenCalledWith(t("grid.presetSaved"));
    // The form stays filled and the dialog stays open so the user can correct and retry directly.
    expect(screen.getByLabelText(t("field.title"))).toHaveValue("保存失败的番");
    expect(screen.getByText(t("addPreset.title"))).toBeInTheDocument();
  });

  it("删除预设先弹确认框：取消不删，点「删除预设」才发 DELETE", async () => {
    candidates = [customCandidateFixture];
    await renderGrid("/?source=custom");
    await flush();

    const deleteCalls = () => fetchMock.mock.calls.filter(([, init]) => init?.method === "DELETE");

    await openMenu(screen.getByRole("button", { name: t("grid.card.moreFor", { title: "测试番剧" }) }));
    fireEvent.click(screen.getByRole("menuitem", { name: t("grid.card.deletePreset") }));
    await flushLazyEntry("confirm");

    // The confirm dialog lists the config summary (subscription note / group / RSS URL); at this point no DELETE may ever be issued.
    expect(screen.getByText(t("grid.deletePresetTitle", { title: "测试番剧" }))).toBeInTheDocument();
    expect(screen.getByText(/订阅备注「客厅版」/)).toBeInTheDocument();
    expect(screen.getByText(/字幕组「某字幕组」/)).toBeInTheDocument();
    expect(screen.getByText(/https:\/\/example\.com\/feed\.xml/)).toBeInTheDocument();
    expect(deleteCalls()).toHaveLength(0);

    fireEvent.click(screen.getByRole("button", { name: t("act.cancel") }));
    await flush();
    expect(deleteCalls()).toHaveLength(0);

    // Only re-opening and confirming actually deletes.
    await openMenu(screen.getByRole("button", { name: t("grid.card.moreFor", { title: "测试番剧" }) }));
    fireEvent.click(screen.getByRole("menuitem", { name: t("grid.card.deletePreset") }));
    fireEvent.click(screen.getByRole("button", { name: t("grid.deletePresetConfirm") }));
    await flush();

    expect(deleteCalls()).toHaveLength(1);
    expect(String(deleteCalls()[0]![0])).toContain("/anime-config-candidates/cand-1");
    const { toast } = await import("sonner");
    expect(toast.success).toHaveBeenCalledWith(t("grid.presetDeleted", { title: "测试番剧" }));
  });

  it("添加预设成功：save 后把 origin_entry_ref CAS 回写为候选自身 id", async () => {
    await renderGrid("/?source=custom");
    await flush();

    fireEvent.click(screen.getAllByRole("button", { name: t("grid.addPreset") })[0]!);
    await flushLazyEntry("add");
    fireEvent.change(screen.getByLabelText(t("field.title")), { target: { value: "新番" } });
    fireEvent.change(screen.getByLabelText(t("addPreset.fieldLabel")), { target: { value: "客厅版" } });
    fireEvent.change(screen.getByLabelText(t("addPreset.fieldRss")), { target: { value: "https://example.com/feed.xml" } });
    fireEvent.change(screen.getByLabelText(t("addPreset.fieldGroup")), { target: { value: "某字幕组" } });
    fireEvent.click(screen.getByRole("button", { name: t("addPreset.save") }));
    await flush();

    // The candidate id is server-generated, so the first save must carry a null origin_entry_ref.
    const saveCalls = fetchMock.mock.calls.filter(
      ([input, init]) => init?.method === "POST" && String(input).endsWith("/anime-config-candidates"),
    );
    expect(saveCalls).toHaveLength(1);
    const saveDraft = (JSON.parse(String(saveCalls[0]![1]?.body)) as { draft: { origin_entry_ref: string | null } }).draft;
    expect(saveDraft.origin_entry_ref).toBeNull();
    expect((saveDraft as { business?: unknown }).business).toBeTruthy();

    // custom no longer CAS-writes back origin_entry_ref (always null), and there is no PATCH.
    const patchCalls = fetchMock.mock.calls.filter(([, init]) => init?.method === "PATCH");
    expect(patchCalls).toHaveLength(0);

    // Save success: form cleared, dialog closed.
    expect(screen.queryByText(t("addPreset.title"))).not.toBeInTheDocument();
  });
});

// The subscribed tab's four count chips (not-yet-started / running / awaiting metadata match /
//   stopped; mutually exclusive buckets where the hold wins: a hold subdivides only running, while
//   pending/stopped bucket by subscription state even when bindings are missing).
// The data comes from the activated candidates of HTTP /api/v1/anime: fully bound ones count as
//   "running", ones missing BGM/TMDB as "awaiting metadata match"; "stopped" is kept by the
//   subscription PATCH's response state.
describe("AnimeGrid 已订阅计数 chip（G2b）", () => {
  beforeEach(() => {
    vi.resetModules();
    vi.useFakeTimers();
    vi.setSystemTime(FIXED_NOW);
    vi.clearAllMocks();
    suppressions = [];
    candidates = [];
    extraCatalogItems = [];
    deletedManagedAnimeIDs = new Set();
    candidateSaveError = undefined;
    // Earlier cases' subscribe stub activates customCandidateFixture in place; reset it to
    //   inactive here so the premise "unsubscribed candidates don't count" holds.
    customCandidateFixture.activated_managed_anime_id = null;
    stubFetch();
    installCatalogFetchRoutes();
  });

  afterEach(async () => {
    // Base UI's animation-frame scheduler is a process-level singleton: an rAF that did not finish
    //   before the fake timers are torn down leaves it permanently believing a frame is scheduled,
    //   and later cases' menus/dialogs never mount. Tear down by advancing that frame first.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(50);
    });
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  // Fully bound -> running (no hold)
  const activeCandidate = {
    ...customCandidateFixture,
    id: "cand-active",
    origin_entry_ref: "cand-active",
    title: "完好番剧",
    activated_managed_anime_id: "anime:cand-active",
    status: "dormant",
    draft: {
      ...customCandidateFixture.draft,
      origin_entry_ref: "cand-active",
      title: "完好番剧",
      metadata: {
        bgm: { work_id: "123" },
        tmdb: { work_id: "456", media_kind: "tv" as const, season: 1 },
      },
    },
  };
  // Missing bindings -> awaiting metadata match (hold)
  const holdCandidate = {
    ...customCandidateFixture,
    id: "cand-hold",
    origin_entry_ref: "cand-hold",
    title: "挂起番剧",
    activated_managed_anime_id: "anime:cand-hold",
    status: "dormant",
    draft: {
      ...customCandidateFixture.draft,
      origin_entry_ref: "cand-hold",
      title: "挂起番剧",
    },
  };

  // Not started and missing bindings -> not-yet-started (not counted as awaiting metadata match)
  const pendingCandidate = {
    ...holdCandidate,
    id: "cand-pending",
    origin_entry_ref: "cand-pending",
    title: "未开始番剧",
    activated_managed_anime_id: "anime:cand-pending",
    subscription_state: "pending",
    draft: { ...holdCandidate.draft, origin_entry_ref: "cand-pending", title: "未开始番剧" },
  };

  function chip(name: string) {
    return screen.getByRole("button", { name: new RegExp(`^${name}\\s*\\d`) });
  }

  it("计数 chip 只数订阅状态：缺关联的订阅中计入订阅中，卡片单独挂短形阻塞 chip", async () => {
    candidates = [activeCandidate, holdCandidate, pendingCandidate, customCandidateFixture];
    await renderGrid("/?source=all");
    await flush();

    expect(chip(t("anime.state.pending"))).toHaveTextContent("1");
    expect(chip(t("anime.state.active"))).toHaveTextContent("2");
    expect(chip(t("anime.state.stopped"))).toHaveTextContent("0");
    expect(within(screen.getByRole("group", { name: t("grid.statsAria") })).getAllByRole("button")).toHaveLength(3);
    expect(screen.getAllByText(t("anime.metadata.missing", { names: "Bangumi、TMDB" })).length).toBeGreaterThan(0);

    // The inactive custom preset (customCandidateFixture) does not appear in the subscribed tab.
    expect(screen.queryByText("测试番剧")).not.toBeInTheDocument();
  });

  it("点击 chip 过滤卡片，激活态高亮，再点取消恢复", async () => {
    candidates = [activeCandidate, holdCandidate, pendingCandidate];
    await renderGrid("/?source=all");
    await flush();

    expect(screen.getByText("完好番剧")).toBeInTheDocument();
    expect(screen.getByText("挂起番剧")).toBeInTheDocument();

    // "Running" includes the anime missing its bindings and excludes not-yet-started ones
    fireEvent.click(chip(t("anime.state.active")));
    expect(chip(t("anime.state.active"))).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText("完好番剧")).toBeInTheDocument();
    expect(screen.getByText("挂起番剧")).toBeInTheDocument();
    expect(screen.queryByText("未开始番剧")).not.toBeInTheDocument();

    // Click cancel again: everything restored
    fireEvent.click(chip(t("anime.state.active")));
    expect(chip(t("anime.state.active"))).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByText("未开始番剧")).toBeInTheDocument();
  });

  it("「未开始」chip 只留未开始的实例", async () => {
    candidates = [activeCandidate, holdCandidate, pendingCandidate];
    await renderGrid("/?source=all");
    await flush();

    fireEvent.click(chip(t("anime.state.pending")));
    expect(chip(t("anime.state.pending"))).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText("未开始番剧")).toBeInTheDocument();
    expect(screen.queryByText("完好番剧")).not.toBeInTheDocument();
    expect(screen.queryByText("挂起番剧")).not.toBeInTheDocument();
  });

  it("与标题搜索叠加：先搜索再点 chip，过滤链同时生效", async () => {
    candidates = [activeCandidate, holdCandidate];
    await renderGrid("/?source=all");
    await flush();

    // Clicking "running" leaves both cards (a missing binding still counts as running); searching for the held anime leaves only it.
    fireEvent.click(chip(t("anime.state.active")));
    fireEvent.change(screen.getByPlaceholderText(t("grid.filterPlaceholder")), { target: { value: "挂起" } });
    expect(screen.queryByText("完好番剧")).not.toBeInTheDocument();
    expect(screen.getByText("挂起番剧")).toBeInTheDocument();

    // Switching to "stopped": the chip and the search intersect in nothing.
    fireEvent.click(chip(t("anime.state.stopped")));
    expect(screen.queryByText("挂起番剧")).not.toBeInTheDocument();
  });

  it("已订阅墙每天一行：行标签在行首，按周一…周日排序，空行不渲染（owner 2026-10-20）", async () => {
    candidates = [
      { ...activeCandidate, broadcast_day: "sun" },
      { ...holdCandidate, broadcast_day: "thu" },
      { ...pendingCandidate, broadcast_day: "thu" },
    ];
    const { container } = await renderGrid("/?source=all");
    await flush();

    const rows = container.querySelectorAll("section[data-wall-row]");
    expect([...rows].map((row) => row.querySelector("h3")?.textContent)).toEqual([t("grid.weekday.thu"), t("grid.weekday.sun")]);
    // Each row is a region named by its row label, with the cards in the row's horizontal track.
    expect(screen.getByRole("region", { name: t("grid.weekday.thu") })).toBe(rows[0]);
    expect(within(rows[0] as HTMLElement).getAllByRole("listitem")).toHaveLength(2);
    expect(within(rows[1] as HTMLElement).getAllByRole("listitem")).toHaveLength(1);
    // The cards no longer carry the broadcast day themselves: each day's wording appears only on the row labels.
    expect(screen.getAllByText(t("grid.weekday.thu"))).toHaveLength(1);
    expect(screen.getAllByText(t("grid.weekday.sun"))).toHaveLength(1);
  });

  it("已订阅墙按站点目录的播出形式归剧场版行", async () => {
    candidates = [
      { ...activeCandidate, broadcast_day: "mon" },
      { ...holdCandidate, broadcast_day: "none", release_format: "movie" },
    ];
    const { container } = await renderGrid("/?source=all");
    await flush();
    const rows = container.querySelectorAll("section[data-wall-row]");
    expect([...rows].map((row) => row.querySelector("h3")?.textContent)).toEqual([t("grid.weekday.mon"), t("grid.wallRow.movie")]);
    expect(within(rows[1] as HTMLElement).getByText("挂起番剧")).toBeInTheDocument();
  });

  it("没有放送日的自定义 RSS 订阅进「自定义」行，排在星期行之后", async () => {
    candidates = [
      { ...activeCandidate, broadcast_day: "mon" },
      { ...holdCandidate, broadcast_day: "none" },
    ];
    const { container } = await renderGrid("/?source=all");
    await flush();
    const rows = container.querySelectorAll("section[data-wall-row]");
    expect([...rows].map((row) => row.querySelector("h3")?.textContent)).toEqual([t("grid.weekday.mon"), t("grid.wallRow.custom")]);
    expect(within(rows[1] as HTMLElement).getByText("挂起番剧")).toBeInTheDocument();
  });

  it("按标题筛选后，没有匹配卡片的行消失", async () => {
    candidates = [
      { ...activeCandidate, broadcast_day: "sun" },
      { ...holdCandidate, broadcast_day: "thu" },
    ];
    const { container } = await renderGrid("/?source=all");
    await flush();
    expect(container.querySelectorAll("section[data-wall-row]")).toHaveLength(2);
    const title = activeCandidate.title;
    fireEvent.change(screen.getByPlaceholderText(t("grid.filterPlaceholder")), { target: { value: title } });
    await flush();
    const rows = container.querySelectorAll("section[data-wall-row]");
    expect([...rows].map((row) => row.querySelector("h3")?.textContent)).toEqual([t("grid.weekday.sun")]);
  });

  it("无数据时不渲染 chip 行", async () => {
    candidates = [];
    await renderGrid("/?source=all");
    await flush();

    expect(screen.queryByRole("group", { name: t("grid.statsAria") })).not.toBeInTheDocument();
  });

  it("切走已订阅分栏时 chip 行不显示", async () => {
    candidates = [activeCandidate];
    await renderGrid("/?source=custom");
    await flush();

    expect(screen.queryByRole("group", { name: t("grid.statsAria") })).not.toBeInTheDocument();
  });
});

// Provider keyword search: a standalone search button in the browse area -> a result view (AniBT
//   search returns CatalogItem[] rendered as cards via mapCatalogItem); Mikan answers 422
//   SEARCH_NOT_SUPPORTED, shown as the "not supported" error state; returning to the catalog is
//   possible.
describe("AnimeGrid 加载失败错误态 + 重试（E1）", () => {
  beforeEach(() => {
    vi.resetModules();
    vi.useFakeTimers();
    vi.setSystemTime(FIXED_NOW);
    vi.clearAllMocks();
    suppressions = [];
    candidates = [];
    extraCatalogItems = [];
    deletedManagedAnimeIDs = new Set();
    candidateSaveError = undefined;
    animeListError = undefined;
    customCandidateFixture.activated_managed_anime_id = null;
    searchResult = { status: 422, body: { code: "SEARCH_NOT_SUPPORTED", message: "先搜索" } };
    searchCalled = false;
    stubFetch();
    installCatalogFetchRoutes();
  });

  afterEach(async () => {
    // Base UI's animation-frame scheduler is a process-level singleton: an rAF that did not finish
    //   before the fake timers are torn down leaves it permanently believing a frame is scheduled,
    //   and later cases' menus/dialogs never mount. Tear down by advancing that frame first.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(50);
    });
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("订阅列表读取失败：显示错误 + 重试；重试成功后恢复列表", async () => {
    animeListError = { status: 500, body: { code: "INTERNAL", message: "数据库连接中断" } };
    await renderGrid("/?source=all");
    await flush();

    // An honest error state: the error message is visible, never disguised as an empty state (no "no subscribed anime yet" prompt).
    expect(screen.getByRole("alert")).toHaveTextContent(t("error.INTERNAL"));
    expect(screen.queryByText(t("catalog.emptySubscribed"))).not.toBeInTheDocument();

    const animeGets = () =>
      fetchMock.mock.calls.filter(
        ([input, init]) => String(input).endsWith("/anime") && (!init?.method || init.method === "GET"),
      );
    const before = animeGets().length;

    // Clicking retry after the backend recovers triggers a refetch and returns the list to normal.
    animeListError = undefined;
    fireEvent.click(screen.getByRole("button", { name: t("grid.retry") }));
    await flush();

    expect(animeGets().length).toBeGreaterThan(before);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByText(t("catalog.emptySubscribed"))).toBeInTheDocument();
  });
});
