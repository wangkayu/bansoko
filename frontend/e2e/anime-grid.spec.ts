import { test, expect, type Page, type Route } from "@playwright/test";
import type {
  AnimeConfigCandidate,
  AnimeDraft,
  AnimeListItem,
  AnimeMetadata,
  AnimeWorkspace,
  CatalogItem,
  CatalogWorkspace,
  GlobalSectionView,
  MetadataHold,
  SubscribeResult,
} from "../src/lib/api/client";
import { preset as businessPreset } from "../src/testing/businessPreset";
import { ownerRSSView } from "./fixtures/ownerRSSView";
import { isPhoneLayout, selectBrowseSection, selectSubscribed } from "./catalog-nav";
import { joinList, t } from "./copy";
import { mockSignedIn } from "./session";

// This suite is a deterministic fixture UI contract. Live provider evidence is
// kept in live-smoke.spec.ts so online data cannot invalidate fixture assertions.
const fixtureOnly = process.env.BANSOKO_E2E_FIXTURES === "1";

// Current catalog flow. Fixture mode owns catalog subscriptions in the
// adapter's process memory; workspace reads are mocked here at the HTTP boundary
// so these tests verify the UI without a database or a live login.


// Batch3 终审（M2）：fixture mock 一律 fail-closed——只放行真实契约接受的
// HTTP method，其余方法返回显式 405，绝不让错误方法的请求静默拿到成功数据。
function fulfillMethodNotAllowed(route: Route, allowed: string): Promise<void> {
  return route.fulfill({
    status: 405,
    contentType: "application/json",
    body: JSON.stringify({
      code: "METHOD_NOT_ALLOWED",
      message: `fixture 仅支持 ${allowed}`,
    }),
  });
}

// mock 响应以 openapi 生成类型约束（契约门禁，见 tsconfig.e2e.json）：
// 契约字段增删或枚举漂移会在 pnpm typecheck 时直接失败，不再等浏览器 lane。
type FixtureListItem = AnimeListItem;
type FixtureState = {
  subscriptions: FixtureListItem[];
};

// Keep all fixture-only state behind the HTTP routes. A per-page store avoids
// cross-test leakage while allowing candidate activation, catalog reads, and
// workspace reads to observe the same server-side lifecycle.
const fixtureStates = new WeakMap<Page, FixtureState>();

function fixtureStateFor(page: Page): FixtureState {
  const existing = fixtureStates.get(page);
  if (existing) return existing;
  const created: FixtureState = { subscriptions: [] };
  fixtureStates.set(page, created);
  return created;
}

function workspaceFixture(id: string, title: string, state?: FixtureState): AnimeWorkspace {
  const persisted = state?.subscriptions.find(
    (item) => item.managed_anime.id === id,
  );
  const persistedSubscription = persisted?.subscription;
  const persistedMetadata = persisted?.metadata;
  const workspaceState = persistedSubscription?.state ?? "active";
  const workspaceSubscriptionVersion = persistedSubscription?.version ?? 1;
  const workspaceMetadata: Pick<AnimeMetadata, "bgm" | "tmdb" | "title_override" | "version"> =
    persistedMetadata ?? { bgm: null, tmdb: null, title_override: null, version: 1 };
  const metadataMissing: MetadataHold["missing"] = [
    ...(workspaceMetadata.bgm == null ? ["bgm" as const] : []),
    ...(workspaceMetadata.tmdb == null ? ["tmdb" as const] : []),
  ];
  return {
    managed_anime: {
      id,
      title,
      title_override: persistedMetadata?.title_override ?? null,
      version: 1,
    },
    subscription: {
      id: `subscription-${id}`,
      managed_anime_id: id,
      created_at: "2026-10-01T00:00:00Z",
      origin: "anibt",
      origin_entry_ref: "anibt:stellar-mail-ella",
      instance_label: title,
      state: workspaceState,
      // Lifecycle boundary: separate from the reversible Subscription.state.
      lifecycle_state: "active",
      lifecycle_epoch: 1,
      version: workspaceSubscriptionVersion,
      new_releases_only: false,
    },
    metadata: {
      managed_anime_id: id,
      bgm: workspaceMetadata.bgm ?? null,
      tmdb: workspaceMetadata.tmdb ?? null,
      title_override: workspaceMetadata.title_override ?? null,
      version: workspaceMetadata.version ?? 1,
    },
    metadata_hold: { held: metadataMissing.length > 0, missing: metadataMissing },
    subgroups: {
      managed_anime_id: id,
      subgroups: {
        max_rss: 3,
        entries: [
          {
            provider: "anibt",
            group_ref: "ranked-group",
            display_name: "Ranked 字幕组",
            selection_mode: "auto",
            manual_selected: null,
            effective_selected: true,
            score: 90,
          },
          {
            provider: "anibt",
            group_ref: "always-group",
            display_name: "Always 字幕组",
            selection_mode: "manual",
            manual_selected: true,
            effective_selected: true,
            score: 70,
          },
          {
            provider: "anibt",
            group_ref: "never-group",
            display_name: "Never 字幕组",
            selection_mode: "manual",
            manual_selected: false,
            effective_selected: false,
            score: 50,
          },
        ],
      },
      effective_selection: [
        {
          id: `selection-ranked-${id}`,
          subscription_id: `subscription-${id}`,
          provider: "anibt",
          group_ref: "ranked-group",
          display_name: "Ranked 字幕组",
          selection_mode: "auto",
          manual_selected: null,
          effective_selected: true,
          effective_score: 90,
          available: true,
          availability_generation: 1,
          version: 1,
        },
        {
          id: `selection-always-${id}`,
          subscription_id: `subscription-${id}`,
          provider: "anibt",
          group_ref: "always-group",
          display_name: "Always 字幕组",
          selection_mode: "manual",
          manual_selected: true,
          effective_selected: true,
          effective_score: 70,
          available: true,
          availability_generation: 1,
          version: 1,
        },
        {
          id: `selection-never-${id}`,
          subscription_id: `subscription-${id}`,
          provider: "anibt",
          group_ref: "never-group",
          display_name: "Never 字幕组",
          selection_mode: "manual",
          manual_selected: false,
          effective_selected: false,
          effective_score: 50,
          available: true,
          availability_generation: 1,
          version: 1,
        },
      ],
      rss_sources: [
        {
          id: `source-${id}`,
          subscription_id: `subscription-${id}`,
          source_kind: "provider_group",
          locator_display: "https://example.test/rss",
          group_name: "北宇治字幕组",
          title,
          provider_provenance: {
            provider: "anibt",
            provider_entry_ref: "anibt:stellar-mail-ella",
            provider_group_ref: "ranked-group",
            discovery_snapshot_version: "fixture-snapshot-1",
          },
          episode_offset: 0,
          version: 1,
        },
      ],
      version: 1,
    },
    rules: {
      managed_anime_id: id,
      // owner 读始终返回每一段的完整值（一次性输入，没有使用默认这一态）。
      sections: {
        parser: { mode: "replace", value: { episode_patterns: [] } },
        release_filter: { mode: "replace", value: { must_contain: "", not_in: "" } },
        file_filter: { mode: "replace", value: { must_contain: "", not_in: "" } },
        naming: {
          mode: "replace",
          value: {
            language: "js-naming-1",
            unknown: { directory_template: "{title}", video_filename_template: "{title}" },
            tv: { directory_template: "{title}", video_filename_template: "{title}" },
            movie: { directory_template: "{title}", video_filename_template: "{title}" },
            subtitle_suffix_template: "",
          },
        },
        file_effect: { mode: "replace", value: { operation: "hardlink", collision_policy: "versioned" } },
        tag_filters: { mode: "replace", value: { anibt: { require: [], exclude: [] }, title: { require: [], exclude: [] } } },
      },
      effective: {
        parser: { episode_patterns: [] },
        release_filter: { must_contain: "", not_in: "" },
        file_filter: { must_contain: "", not_in: "" },
        naming: {
          language: "js-naming-1",
          unknown: { directory_template: "{title}", video_filename_template: "{title}" },
          tv: { directory_template: "{title}", video_filename_template: "{title}" },
          movie: { directory_template: "{title}", video_filename_template: "{title}" },
          subtitle_suffix_template: "",
        },
        file_effect: { operation: "hardlink", collision_policy: "versioned" },
      },
      // AnimeRules 契约必填：effective_tag_filters / has_anibt_source，缺失会让整个
      // 番剧配置弹窗崩溃（fixture 漂移的教训）。
      effective_tag_filters: {
        anibt: { require: [], exclude: [] },
        title: { require: [], exclude: [] },
      },
      has_anibt_source: true,
      version: 1,
    },
    rss_sources: [
      {
        id: `source-${id}`,
        subscription_id: `subscription-${id}`,
        source_kind: "provider_group",
        locator_display: "https://example.test/rss",
        group_name: "北宇治字幕组",
        title,
        provider_provenance: {
          provider: "anibt",
          provider_entry_ref: "anibt:stellar-mail-ella",
          provider_group_ref: "ranked-group",
          discovery_snapshot_version: "fixture-snapshot-1",
        },
        episode_offset: 0,
        version: 1,
      },
    ],
    rss_source_runtimes: [{ rss_source_id: `source-${id}`, health: "ready", version: 1 }],
    releases: [
      {
        id: `release-${id}`,
        rss_source_id: `source-${id}`,
        stable_item_key: `release-key-${id}`,
        raw_facts: { title: `[北宇治字幕组] ${title} - 01`, published_at: "2026-07-20T00:00:00Z" },
        source_facts: { identity_kind: "guid", identity_value: `release-key-${id}`, group_name: "北宇治字幕组" },
        parsed_facts: { episode: 1, group_name: "北宇治字幕组", quality: "1080p", languages: ["简"], special: false },
        verdict: "waiting_metadata",
        reason_code: "metadata_incomplete",
        reason_detail: "bgm,tmdb",
        version: 1,
      },
    ],
    download_tasks: [],
    artifacts: [],
    file_operations: [],
    placement_handoffs: [],
    logs: [
      {
        id: `log-${id}`,
        subscription_id: `subscription-${id}`,
        managed_anime_id: id,
        kind: "subscription_created",
        outcome: "success",
        occurred_at: "2026-07-20T00:00:00Z",
        anime_title: "Fixture",
        source: "subscription",
        message_code: "subscription_created",
        message_params: {},
        details: { item_count: 1 },
        dedupe_key: `log-${id}`,
        retention_until: "2027-07-20T00:00:00Z",
      },
    ],
    episode_progress: null,
    config_waiting_files: 0,
  };
}

function addSubscription(
  state: FixtureState,
  item: FixtureListItem,
  sourceDrafts: AnimeDraft["rss_sources"] = [],
): void {
  state.subscriptions.push({
    ...item,
    subscribed_groups: sourceDrafts.map((source) => source.group_name),
    rss_health: sourceDrafts.length > 0 ? "ready" : null,
    rss_health_reason: null,
  });
}

function subscriptionListItemFromCandidate(
  candidate: CandidateRecord,
  managedAnimeID: string,
  subscriptionID: string,
  initialState: "pending" | "active" | "stopped",
): FixtureListItem {
  const draft = candidate.draft;
  return {
    managed_anime: {
      id: managedAnimeID,
      title: draft.title,
      title_override: draft.title_override ?? null,
      version: 1,
    },
    subscription: {
      id: subscriptionID,
      managed_anime_id: managedAnimeID,
      created_at: "2026-10-01T00:00:00Z",
      origin: draft.origin,
      origin_entry_ref: draft.origin_entry_ref ?? null,
      instance_label: draft.instance_label ?? null,
      state: initialState,
      version: 1,
      // Lifecycle boundary: separate from the reversible Subscription.state.
      lifecycle_state: "active",
      lifecycle_epoch: 1,
      new_releases_only: draft.new_releases_only === true,
    },
    metadata: {
      managed_anime_id: managedAnimeID,
      bgm: draft.metadata.bgm ?? null,
      tmdb: draft.metadata.tmdb ?? (draft.origin === "anibt" || draft.origin === "mikan" ? { work_id: "tmdb-fixture", media_kind: "tv", season: 1 } : null),
      total_episodes: draft.metadata.total_episodes ?? null,
      title_override: draft.title_override ?? null,
      version: 1,
    },
    metadata_hold: {
      held: draft.metadata.bgm == null || (draft.metadata.tmdb == null && draft.origin !== "anibt" && draft.origin !== "mikan"),
      missing: [
        ...(draft.metadata.bgm == null ? ["bgm" as const] : []),
        ...(draft.metadata.tmdb == null && draft.origin !== "anibt" && draft.origin !== "mikan" ? ["tmdb" as const] : []),
      ],
    },
    // AnimeListItem.broadcast_day 契约必填；fixture 订阅无放送日事实，落 none 组
    // （与此前缺字段时 AnimeGrid 的 ?? "none" 分组行为一致）。
    broadcast_day: "none",
  };
}

async function mockWorkspace(page: Page) {
  const state = fixtureStateFor(page);
  await page.route("**/api/v1/anime/rss-list-view", async (route) => {
    if (route.request().method() !== "POST") return fulfillMethodNotAllowed(route, "POST");
    const input = route.request().postDataJSON() as { input_source: string; managed_anime_id: string };
    if (input.input_source !== "owner") return route.fulfill({ status: 422, contentType: "application/json", body: JSON.stringify({ code: "INVALID_INPUT" }) });
    const workspace = workspaceFixture(input.managed_anime_id, "星际邮差艾拉", state);
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(ownerRSSView(workspace)) });
  });
  await page.route("**/api/v1/anime/**/workspace", async (route) => {
    const request = route.request();
    // workspace 是 GET-only 读模型；错误方法显式 405。
    if (request.method() !== "GET") {
      await fulfillMethodNotAllowed(route, "GET");
      return;
    }
    const id = request.url().split("/anime/")[1]?.split("/")[0] ?? "fixture-anime";
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(workspaceFixture(id, "星际邮差艾拉", state)),
    });
  });
}

// 契约束缚的候选记录：字段集与 AnimeConfigCandidate 一致（含 current_revision
// 不可变修订身份，见 anime_config_candidates.current_revision；可变 CAS version
// 与不可变 revision 各自推进，激活总是携带预览时的精确 revision）。
type CandidateRecord = AnimeConfigCandidate;

// Candidates live on the server (anime_config_candidates). Fixture mode has no
// backend, so the candidate service is mocked here at the HTTP boundary rather
// than branching inside the store: the store under test stays the same one the
// product ships. Semantics mirror the server -- POST is pure insert, list is
// newest-first, patch/activation use CAS, and an activated candidate can be
// neither patched nor deleted.
async function mockCandidates(page: Page) {
  const candidates: CandidateRecord[] = [];
  const state = fixtureStateFor(page);
  const stamp = "2026-07-20T00:00:00Z";
  let seq = 0;
  const activations = new Map<
    string,
    { requestHash: string; result: Record<string, unknown> }
  >();

  await page.route("**/api/v1/anime-config-candidates**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const rest = url.pathname.replace("/api/v1/anime-config-candidates", "");
    const method = request.method();

    const ok = (status: number, body: unknown) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
    const fail = (status: number, code: string, message: string, extra: object = {}) =>
      route.fulfill({
        status,
        contentType: "application/json",
        body: JSON.stringify({ code, message, ...extra }),
      });

    // Route shape + method gate 先于资源查找（镜像 openapi 契约）：collection
    // 支持 GET/POST；/{candidate_id} 支持 GET/PATCH/DELETE；
    // /{candidate_id}/subscribe 仅 POST。合法 shape 上的不支持方法显式 405，
    // 方法门禁不得被 target 查找绕过；未知 path 显式 404，不穿透。
    const subscribeMatch = /^\/([^/]+)\/subscribe$/.exec(rest);
    const itemMatch = subscribeMatch ? null : /^\/([^/]+)$/.exec(rest);
    let allowedMethods: string[] | null;
    if (rest === "") {
      allowedMethods = ["GET", "POST"];
    } else if (subscribeMatch) {
      allowedMethods = ["POST"];
    } else if (itemMatch) {
      allowedMethods = ["GET", "PATCH", "DELETE"];
    } else {
      allowedMethods = null;
    }
    if (!allowedMethods) {
      return fail(404, "NOT_FOUND", "未知候选路径");
    }
    if (!allowedMethods.includes(method)) {
      await fulfillMethodNotAllowed(route, allowedMethods.join(" / "));
      return;
    }

    if (rest === "" && method === "GET") {
      const origin = url.searchParams.get("origin");
      const entryRef = url.searchParams.get("origin_entry_ref");
      const items = candidates
        .filter(
          (item) =>
            (!origin || item.origin === origin) &&
            (!entryRef || item.origin_entry_ref === entryRef),
        )
        .sort((left, right) =>
          right.updated_at.localeCompare(left.updated_at) || right.id.localeCompare(left.id),
        );
      return ok(200, { candidates: items });
    }

    if (rest === "" && method === "POST") {
      const draft = (request.postDataJSON() as { draft: AnimeDraft }).draft;
      const entryRef = draft.origin_entry_ref ?? null;
      seq += 1;
      const created: CandidateRecord = {
        id: `candidate-${seq}`,
        origin: draft.origin,
        origin_entry_ref: entryRef,
        title: draft.title,
        title_override: draft.title_override ?? null,
        instance_label: draft.instance_label ?? null,
        draft,
        version: 1,
        current_revision: 1,
        current_revision_digest: "sha256:" + "1".repeat(64),
        activated_managed_anime_id: null,
        created_at: stamp,
        updated_at: stamp,
        status: "working",
        draft_digest: "sha256:" + "0".repeat(64),
        last_activity: stamp,
        retention_until: "2026-08-19T00:00:00Z",
        cleanup_eligible_at: null,
        cleanup_reason: null,
      };
      candidates.push(created);
      return ok(201, created);
    }

    const id = subscribeMatch ? subscribeMatch[1] : itemMatch?.[1] ?? "";
    const target = candidates.find((item) => item.id === id);
    if (!target) {
      return fail(404, "NOT_FOUND", "候选不存在");
    }

    if (subscribeMatch) {
      const ifMatchHeader = request.headers()["if-match"];
      const idempotencyKey = request.headers()["idempotency-key"];
      if (ifMatchHeader === undefined || idempotencyKey === undefined) {
        return fail(422, "REQUEST_INVALID", "激活候选必须提供 If-Match 和 Idempotency-Key。");
      }
      const expectedVersion = Number(ifMatchHeader);
      const input = (request.postDataJSON() ?? {}) as { initial_state?: "pending" | "active" | "stopped"; start?: boolean };
      // 镜像服务端：缺省建为未开始，start: true 才直接运行。
      const initialState = input.initial_state ?? (input.start ? "active" : "pending");
      const requestHash = JSON.stringify({ id, expectedVersion, initialState });
      const replay = activations.get(idempotencyKey);
      if (replay) {
        if (replay.requestHash !== requestHash) {
          return fail(409, "IDEMPOTENCY_KEY_REUSED", "Idempotency-Key 已用于不同的激活请求。");
        }
        return ok(201, replay.result);
      }
      if (expectedVersion !== target.version) {
        return fail(409, "VERSION_CONFLICT", "候选版本冲突", { current_version: target.version });
      }
      if (target.activated_managed_anime_id) {
        return fail(409, "CANDIDATE_ALREADY_ACTIVATED", "候选已激活，不能创建第二个实例。");
      }
      if (initialState !== "pending" && initialState !== "active" && initialState !== "stopped") {
        return fail(422, "REQUEST_INVALID", "initial_state 必须是 pending、active 或 stopped。");
      }
      seq += 1;
      const managedAnimeID = `fixture-anime-${seq}`;
      const subscriptionID = `fixture-subscription-${seq}`;
      const createdSubscription = subscriptionListItemFromCandidate(target, managedAnimeID, subscriptionID, initialState);
      const result: SubscribeResult = {
        managed_anime: createdSubscription.managed_anime,
        subscription: createdSubscription.subscription,
        metadata: createdSubscription.metadata,
        rss_sources: target.draft.rss_sources.map((source, index) => ({
          id: `${subscriptionID}-source-${index}`,
          subscription_id: subscriptionID,
          source_kind: source.source_kind,
          locator_display: source.locator_display ?? source.locator,
          group_name: source.group_name,
          title: source.title,
          episode_offset: source.episode_offset,
          anibt_aux_mode: source.anibt_aux_mode ?? null,
          provider_provenance: source.provider_provenance ?? null,
          http_feed_connection_ref: source.http_feed_connection_ref ?? null,
          network_route_ref: source.network_route_ref ?? null,
          version: 1,
        })),
        effective_config_versions: {
          subgroups: 1,
          parser: 1,
          release_filter: 1,
          file_filter: 1,
          naming: 1,
          file_effect: 1,
        },
      };
      target.activated_managed_anime_id = managedAnimeID;
      target.status = "dormant";
      target.version += 1;
      target.updated_at = stamp;
      addSubscription(state, createdSubscription);
      activations.set(idempotencyKey, { requestHash, result });
      return ok(201, result);
    }

    if (method === "GET") {
      return ok(200, target);
    }

    if (method === "PATCH") {
      // 客户端走 PATCH + If-Match 版本头（patchAnimeConfigCandidate），语义同服务端
      // PatchAnimeConfigCandidate：CAS 校验后整份 draft 替换，origin 与
      // origin_entry_ref 随 draft 一并更新。
      const body = request.postDataJSON() as { draft: AnimeDraft };
      const ifMatch = Number(request.headers()["if-match"]);
      if (ifMatch !== target.version) {
        return fail(409, "VERSION_CONFLICT", "候选版本冲突", { current_version: target.version });
      }
      if (target.activated_managed_anime_id) {
        return fail(409, "CANDIDATE_ALREADY_ACTIVATED", "候选已激活，不能再修改；请直接编辑正式配置。");
      }
      Object.assign(target, {
        origin: body.draft.origin,
        origin_entry_ref: body.draft.origin_entry_ref ?? null,
        title: body.draft.title,
        title_override: body.draft.title_override ?? null,
        instance_label: body.draft.instance_label ?? null,
        draft: body.draft,
        version: target.version + 1,
        // Every save appends an immutable revision; activation must carry the
        // current one, so advance it alongside the mutable CAS version.
        current_revision: (target.current_revision ?? 0) + 1,
        current_revision_digest: "sha256:" + ((target.current_revision ?? 0) + 1).toString(16).padStart(64, "0"),
        updated_at: stamp,
      });
      return ok(200, target);
    }

    if (method === "DELETE") {
      if (target.activated_managed_anime_id) {
        return fail(409, "CANDIDATE_ALREADY_ACTIVATED", "候选已激活，不能删除。");
      }
      candidates.splice(candidates.indexOf(target), 1);
      return route.fulfill({ status: 204, body: "" });
    }

    // Gate 之后不可达；防御性兜底——未来往 allowedMethods 加方法而漏加分支时仍 fail-closed。
    return fail(405, "METHOD_NOT_ALLOWED", `不支持的方法 ${method}`);
  });
}

async function mockShellReads(page: Page) {
  await page.route("**/api/v1/config/business-preset", async (route) => {
    if (route.request().method() !== "GET") return fulfillMethodNotAllowed(route, "GET");
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(businessPreset) });
  });
  // 以下均为契约 GET-only 读端点；非 GET 一律 405。
  await page.route("**/api/v1/events/stream**", (route) =>
    route.request().method() === "GET"
      ? route.fulfill({ status: 200, contentType: "text/event-stream", body: ": fixture stream\n\n" })
      : fulfillMethodNotAllowed(route, "GET"),
  );
  await page.route("**/api/v1/config/connections", (route) =>
    route.request().method() === "GET"
      ? route.fulfill({ status: 200, contentType: "application/json", body: "[]" })
      : fulfillMethodNotAllowed(route, "GET"),
  );
  await page.route("**/api/v1/config/global-sections", (route) =>
    route.request().method() === "GET"
      ? route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify([
            { section_name: "parser", schema_version: "v1", value: { episode_patterns: [] }, version: 1, saved: false },
            { section_name: "release_rules", schema_version: "v1", value: { include_keywords: [], exclude_keywords: [] }, version: 1, saved: false },
            { section_name: "file_rules", schema_version: "v1", value: { include_extensions: [], exclude_keywords: [] }, version: 1, saved: false },
            {
              section_name: "tag_filters",
              schema_version: "v1",
              value: { anibt: { require: [], exclude: [] }, title: { require: [], exclude: [] } },
              version: 1,
              saved: false,
            },
            {
              section_name: "naming_rules",
              schema_version: "v1",
              value: {
                tv: { directory_template: "{title}", video_filename_template: "{title}" },
                movie: { directory_template: "{title}", video_filename_template: "{title}" },
                subtitle_suffix_template: "",
              },
              version: 1,
              saved: false,
            },
            { section_name: "file_effect", schema_version: "v1", value: { operation: "hardlink", collision_policy: "versioned" }, version: 1, saved: false },
            { section_name: "download_completion", schema_version: "download-completion-v2", value: { action: "seed" }, version: 1, saved: false },
          ] satisfies GlobalSectionView[]),
        })
      : fulfillMethodNotAllowed(route, "GET"),
  );
  await page.route("**/api/v1/template-catalogs/naming", (route) =>
    route.request().method() === "GET"
      ? route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ fields: [], functions: [], samples: [], vars_keys: [], vars_loaded_at: null }) })
      : fulfillMethodNotAllowed(route, "GET"),
  );
  await page.route("**/api/v1/config/naming-templates", (route) =>
    route.request().method() === "GET"
      ? route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ default_key: "default", templates: [{ key: "default", name: "Default", rules: { language: "js-naming-1", tv: { directory_template: "{title}", video_filename_template: "{title}" }, movie: { directory_template: "{title}", video_filename_template: "{title}" }, unknown: { directory_template: "{title}", video_filename_template: "{title}" }, subtitle_suffix_template: "" } }], version: 1 }) })
      : fulfillMethodNotAllowed(route, "GET"),
  );
  await page.route("**/api/v1/catalog/suppressions", (route) =>
    route.request().method() === "GET"
      ? route.fulfill({ status: 200, contentType: "application/json", body: "[]" })
      : fulfillMethodNotAllowed(route, "GET"),
  );
  await page.route("**/api/v1/automation", (route) =>
    route.request().method() === "GET"
      ? route.fulfill({ status: 200, contentType: "application/json", body: "[]" })
      : fulfillMethodNotAllowed(route, "GET"),
  );
  await page.route("**/api/v1/observability/diagnostics**", (route) =>
    route.request().method() === "GET"
      ? route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ episodes: [], next_cursor: null }) })
      : fulfillMethodNotAllowed(route, "GET"),
  );
  await page.route("**/api/v1/observability/records**", (route) =>
    route.request().method() === "GET"
      ? route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ records: [], next_cursor: null }) })
      : fulfillMethodNotAllowed(route, "GET"),
  );
}

function fixtureCatalogProvider(url: URL): "mikan" | "anibt" | null {
  const provider = /^\/api\/v1\/catalog\/providers\/([^/]+)\//.exec(url.pathname)?.[1];
  return provider === "mikan" || provider === "anibt" ? provider : null;
}

async function fulfillUnknownCatalogProvider(route: Route): Promise<void> {
  await route.fulfill({
    status: 422,
    contentType: "application/json",
    body: JSON.stringify({
      code: "PROVIDER_UNSUPPORTED",
      message: "provider must be one of: anibt, mikan.",
    }),
  });
}

async function mockCatalogAndAnime(page: Page) {
  const catalogItems = {
    mikan: [
      {
        kind: "mikan_entry",
        provider: "mikan",
        title: "进击的巨人",
        original_title: "進撃の巨人",
        provider_entry_ref: "mikan:sample-giant",
        bgm_id: "486109",
        poster_url: null,
        cover_url: null,
        broadcast_day: "sat",
        release_state: "has_releases",
        update_text: "2026/07/18",
        catalog_state: "present",
        preference: "neutral",
        allowed_actions: ["prepare_subscribe"],
        instances: [],
      },
      {
        // Mikan 灰显条目（此番组下暂无作品）：卡片加「暂无种子」遮罩，仍可点击。
        kind: "mikan_entry",
        provider: "mikan",
        title: "着迷男",
        provider_entry_ref: "mikan:3896",
        poster_url: null,
        cover_url: null,
        broadcast_day: "sun",
        release_state: "no_releases",
        catalog_state: "present",
        preference: "neutral",
        allowed_actions: ["prepare_subscribe"],
        instances: [],
      },
    ],
    anibt: [
      {
        kind: "anibt_entry",
        provider: "anibt",
        title: "星际邮差艾拉",
        original_title: "星間配達人エラ",
        provider_entry_ref: "anibt:stellar-mail-ella",
        bgm_id: "518900",
        poster_url: null,
        cover_url: null,
        broadcast_day: "sat",
        release_state: "has_releases",
        release_count: 12,
        catalog_state: "present",
        preference: "neutral",
        allowed_actions: ["prepare_subscribe"],
        instances: [],
      },
      {
        kind: "anibt_entry",
        provider: "anibt",
        title: "雾港来信",
        provider_entry_ref: "anibt:fog-harbor",
        bgm_id: "530001",
        poster_url: null,
        cover_url: null,
        broadcast_day: "sun",
        release_state: "no_releases",
        release_count: 0,
        catalog_state: "present",
        preference: "neutral",
        allowed_actions: ["prepare_subscribe"],
        instances: [],
      },
    ],
  } satisfies Record<"mikan" | "anibt", CatalogItem[]>;
  const state = fixtureStateFor(page);
  const subscriptions = state.subscriptions;
  await page.route("**/api/v1/catalog/providers/*/seasons/*", async (route) => {
    if (route.request().method() !== "GET") {
      await fulfillMethodNotAllowed(route, "GET");
      return;
    }
    const url = new URL(route.request().url());
    const provider = fixtureCatalogProvider(url);
    if (!provider) {
      await fulfillUnknownCatalogProvider(route);
      return;
    }
    const season = url.pathname.split("/").at(-1) ?? "2026-summer";
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(({
        season,
        snapshot: {
          id: `${provider}-${season}`,
          season,
          status: "fresh",
          provider,
          observed_at: "2026-07-15T12:00:00Z",
          source_version: "fixture",
          contract_version: "v2",
          health_state: "ready",
          health_checked_at: "2026-07-15T12:00:00Z",
          version: 1,
          stale: false,
        },
        items: catalogItems[provider].map((item) => {
          // Mirror the backend catalog projection: one external entry projects
          // to 0..N owner associations. The form owner identity is
          // managed_anime_ref (managed_anime.id), never the provider ref, so
          // this is the only way the browse card can learn it is subscribed.
          const instances = subscriptions
            .filter(
              (sub) =>
                sub.subscription.origin === provider &&
                sub.subscription.origin_entry_ref === item.provider_entry_ref,
            )
            .map((sub) => ({
              managed_anime_ref: sub.managed_anime.id,
              subscription_ref: sub.subscription.id,
              subscription_state: sub.subscription.state,
              subscription_version: sub.subscription.version,
              lifecycle_state: sub.subscription.lifecycle_state,
              lifecycle_epoch: sub.subscription.lifecycle_epoch,
            }));
          return { ...item, instances };
        }),
      } satisfies CatalogWorkspace)),
    });
  });
  await page.route("**/api/v1/anime", async (route) => {
    if (route.request().method() !== "GET") {
      await fulfillMethodNotAllowed(route, "GET");
      return;
    }
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(subscriptions) });
  });
  await page.route("**/api/v1/anime/*/subscription", async (route) => {
    const request = route.request();
    const id = new URL(request.url()).pathname.split("/").at(-2) ?? "";
    const item = subscriptions.find((candidate) => (candidate.managed_anime as { id?: string } | undefined)?.id === id);
    if (request.method() === "PATCH") {
      if (!item) {
        await route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ code: "NOT_FOUND", message: "订阅不存在" }) });
        return;
      }
      const patch = request.postDataJSON() as { state: "active" | "stopped"; expected_version?: number };
      const subscription = item.subscription as { state: "pending" | "active" | "stopped"; version: number };
      if (patch.expected_version !== subscription.version) {
        await route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ code: "OWNER_VERSION_CONFLICT", message: "订阅版本冲突", current_version: subscription.version }) });
        return;
      }
      // 镜像服务端：未开始的订阅只能开始，不能停止。
      if (subscription.state === "pending" && patch.state !== "active") {
        await route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ code: "SUBSCRIPTION_NOT_STARTED", message: "订阅未开始" }) });
        return;
      }
      subscription.state = patch.state;
      subscription.version += 1;
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(subscription) });
      return;
    }
    if (request.method() === "DELETE") {
      if (!item) {
        await route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ code: "NOT_FOUND", message: "订阅不存在" }) });
        return;
      }
      const input = request.postDataJSON() as { expected_version?: number };
      const subscription = item.subscription as { version: number };
      if (input.expected_version !== subscription.version) {
        await route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ code: "OWNER_VERSION_CONFLICT", message: "订阅版本冲突", current_version: subscription.version }) });
        return;
      }
      const index = subscriptions.indexOf(item);
      if (index >= 0) subscriptions.splice(index, 1);
      await route.fulfill({ status: 204, body: "" });
      return;
    }
    await fulfillMethodNotAllowed(route, "PATCH / DELETE");
  });
  await page.route("**/api/v1/anime/subscribe", async (route) => {
    if (route.request().method() !== "POST") {
      await fulfillMethodNotAllowed(route, "POST");
      return;
    }
    const body = route.request().postDataJSON() as { draft: AnimeDraft };
    const index = subscriptions.length + 1;
    const id = `fixture-anime-${index}`;
    const subscriptionID = `fixture-subscription-${index}`;
    const item: FixtureListItem = {
      managed_anime: { id, title: body.draft.title, title_override: body.draft.title_override ?? null, version: 1 },
      subscription: {
        id: subscriptionID,
        managed_anime_id: id,
        created_at: "2026-10-01T00:00:00Z",
        origin: body.draft.origin,
        origin_entry_ref: body.draft.origin_entry_ref ?? null,
        instance_label: body.draft.instance_label ?? null,
        state: "active",
        lifecycle_state: "active",
        lifecycle_epoch: 1,
        version: 1,
        new_releases_only: false,
      },
      metadata: { managed_anime_id: id, bgm: body.draft.metadata.bgm ?? null, tmdb: body.draft.metadata.tmdb ?? null, version: 1 },
      metadata_hold: { held: true, missing: ["bgm", "tmdb"] },
      broadcast_day: "none",
    };
    subscriptions.push(item);
    await route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify(({
        managed_anime: item.managed_anime,
        subscription: item.subscription,
        metadata: item.metadata,
        rss_sources: [],
        effective_config_versions: {
          subgroups: 1,
          parser: 1,
          release_filter: 1,
          file_filter: 1,
          naming: 1,
          file_effect: 1,
        },
      } satisfies SubscribeResult)),
    });
  });
  await page.route("**/api/v1/catalog/providers/*/search**", async (route) => {
    if (route.request().method() !== "GET") {
      await fulfillMethodNotAllowed(route, "GET");
      return;
    }
    const url = new URL(route.request().url());
    const provider = fixtureCatalogProvider(url);
    if (!provider) {
      await fulfillUnknownCatalogProvider(route);
      return;
    }
    const query = url.searchParams.get("q") ?? "";
    await route.fulfill({
      status: provider === "mikan" ? 422 : 200,
      contentType: "application/json",
      body: JSON.stringify(
        provider === "mikan"
          ? { code: "SEARCH_NOT_SUPPORTED", message: "Mikan 暂不支持搜索" }
          : [
              {
                kind: "anibt_entry",
                provider: "anibt",
                title: query || "星际邮差艾拉",
                original_title: "星間配達人エラ",
                provider_entry_ref: "anibt:stellar-mail-ella",
                bgm_id: "518900",
                poster_url: null,
                cover_url: null,
                broadcast_day: "sat",
                catalog_state: "present",
                preference: "neutral",
                allowed_actions: ["prepare_subscribe"],
              },
            ],
      ),
    });
  });

  await page.route("**/api/v1/metadata/search**", async (route) => {
    if (route.request().method() !== "GET") {
      await fulfillMethodNotAllowed(route, "GET");
      return;
    }
    const url = new URL(route.request().url());
    const provider = url.searchParams.get("provider") === "tmdb" ? "tmdb" : "bgm";
    const query = url.searchParams.get("query") ?? "";
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ provider, query, candidates: [{ provider, provider_work_id: provider === "bgm" ? "486109" : "265010", title: provider === "bgm" ? "进击的巨人" : "星际邮差艾拉", media_kind: "tv", details_status: provider === "tmdb" ? "available" : "not_applicable", seasons: provider === "tmdb" ? [{ season_number: 1, name: "第一季", episode_count: 12, air_date: "2024-01-01", is_special: false }] : undefined, suggested_season: provider === "tmdb" ? 1 : undefined, score: 0.96 }] }) });
  });
}
// 目录分栏导航（桌面顶栏 button / 手机底部标签栏 + 页内 Segmented）见 ./catalog-nav。

// 壳层工具入口：「日志」桌面是顶栏独立图标按钮，手机在底部标签栏的「更多」sheet 里。
// 隐藏的番剧是设置里的分区（/settings/hidden），不在账户菜单、「更多」或 ?tool= 里。
async function openShellTool(page: Page, name: "日志") {
  if (isPhoneLayout(page)) {
    await page.getByRole("navigation", { name: "底部导航" }).getByRole("button", { name: "更多", exact: true }).click();
    const sheet = page.getByRole("dialog", { name: "更多" });
    await expect(sheet).toBeVisible();
    await sheet.getByRole("button", { name, exact: true }).click();
    return;
  }
  await page.getByRole("button", { name: "日志", exact: true }).click();
}

async function enterApp(page: Page) {
  await mockSignedIn(page);
  await mockCandidates(page);
  await mockShellReads(page);
  await mockCatalogAndAnime(page);
  await page.goto("/");
  if (isPhoneLayout(page)) {
    await expect(page.getByRole("navigation", { name: "底部导航" })).toBeVisible();
    return;
  }
  await expect(page.getByRole("button", { name: "AniBT", exact: true })).toBeVisible();
}

async function openProvider(page: Page, provider: "AniBT" | "Mikan", requestedTitle?: string) {
  await selectBrowseSection(page, provider === "AniBT" ? "anibt" : "mikan");
  const title = requestedTitle ?? (provider === "AniBT" ? "星际邮差艾拉" : "进击的巨人");
  const card = page.locator("article").filter({ hasText: title }).first();
  await expect(card).toBeVisible();
  return card;
}

// 新订阅默认未开始；需要运行中实例的流程从订阅提示里开始它（与用户路径一致）。
// 卡面没有订阅按钮（owner 2026-10-20）：点卡片打开草稿配置，在弹窗里订阅；还没有候选的
// 新条目由订阅按钮先创建候选再激活。
async function subscribeCard(card: ReturnType<Page["locator"]>, { start = true }: { start?: boolean } = {}) {
  const page = card.page();
  const dialog = await openConfigFromCard(page, card);
  const subscribe = dialog.getByRole("button", { name: "订阅", exact: true });
  await expect(subscribe).toBeEnabled();
  await subscribe.click();
  // 订阅成功后弹窗自关，目录重读合并订阅状态：卡片带上「已订阅」后才继续，否则随后
  // 打开配置会以陈旧 entry 落成 draft 模式弹窗（mobile 弱 CPU 下的竞态）。
  await expect(pageDialog(page)).toHaveCount(0);
  await expect(card.getByText("已订阅", { exact: true })).toBeVisible();
  if (!start) return;
  const started = page.waitForResponse(
    (response) => response.request().method() === "PATCH" && /\/api\/v1\/anime\/[^/]+\/subscription$/.test(new URL(response.url()).pathname),
  );
  await page.getByRole("button", { name: "开始订阅", exact: true }).click();
  expect((await started).status()).toBe(200);
  await expect(page.getByText("已开始订阅", { exact: true })).toBeVisible();
}

// 浏览卡只标「已订阅」、点开仍是候选草稿（owner 2026-10-21）：订阅自己的配置从
// 已订阅墙上的卡片打开。
async function openSubscribedConfig(page: Page, title: string) {
  await selectSubscribed(page);
  const instance = page.locator("article").filter({ hasText: title }).first();
  await expect(instance).toBeVisible();
  return openConfigFromCard(page, instance);
}

async function openConfigFromCard(page: Page, card: ReturnType<Page["locator"]>) {
  // 浏览卡重构后整卡是一个 aria-label="打开配置 <标题>" 的覆盖按钮，标题 h3
  // 是它的视觉层；一级详情弹窗已移除，点卡片直接打开四 Tab 配置弹窗。
  await card.getByRole("button", { name: /打开配置/ }).click();
  return pageDialog(page).last();
}

function pageDialog(page: Page) {
  return page.getByRole("dialog");
}

test.describe("AnimeGrid", () => {
  test.beforeEach(() => {
    test.skip(!fixtureOnly, "fixture UI contract runs only with BANSOKO_E2E_FIXTURES=1");
  });

  test("keeps subscribed-management filters and long titles inside their layout", async ({ page }) => {
    const longTitle = "被放逐的作弊赋予魔术师享受随心所欲的第二人生。";
    const state = fixtureStateFor(page);
    addSubscription(state, {
      managed_anime: { id: "manage-long-title", title: longTitle, title_override: null, version: 1 },
      subscription: {
        id: "manage-long-subscription",
        managed_anime_id: "manage-long-title",
        created_at: "2026-10-31T00:00:00Z",
        entry_season: "2026-fall",
        origin: "anibt",
        origin_entry_ref: "anibt:manage-long-title",
        instance_label: "收藏版",
        state: "active",
        lifecycle_state: "active",
        lifecycle_epoch: 1,
        version: 1,
        new_releases_only: false,
      },
      metadata: {
        managed_anime_id: "manage-long-title",
        bgm: null,
        tmdb: null,
        title_override: null,
        total_episodes: 12,
        season: 1,
        version: 1,
      },
      metadata_hold: { held: false, missing: [] },
      broadcast_day: "none",
      episode_progress: { episodes: [1, 2, 3, 4], total: 12, done: false },
    });
    // 第二个实例的备注被写成了标题（历史 provider 草稿的默认）：与标题相同，视作没有备注。
    addSubscription(state, {
      managed_anime: { id: "manage-note-title", title: longTitle, title_override: null, version: 1 },
      subscription: {
        id: "manage-note-subscription",
        managed_anime_id: "manage-note-title",
        created_at: "2026-10-30T00:00:00Z",
        entry_season: "2026-fall",
        origin: "anibt",
        origin_entry_ref: "anibt:manage-long-title",
        instance_label: longTitle,
        state: "active",
        lifecycle_state: "active",
        lifecycle_epoch: 1,
        version: 1,
        new_releases_only: false,
      },
      metadata: {
        managed_anime_id: "manage-note-title",
        bgm: null,
        tmdb: null,
        title_override: null,
        total_episodes: 12,
        season: 1,
        version: 1,
      },
      metadata_hold: { held: false, missing: [] },
      broadcast_day: "none",
      episode_progress: { episodes: [1, 2], total: 12, done: false },
    });
    await enterApp(page);
    await selectSubscribed(page);
    await page.getByRole("button", { name: t("grid.manage.entry"), exact: true }).first().click();
    const dialog = page.getByRole("dialog", { name: t("grid.manage.title") });
    // 两个实例各一条标题；备注重复标题的第二条不再输出备注（否则是三条）。
    await expect(dialog.getByText(longTitle, { exact: true })).toHaveCount(2);
    const noteBox = await dialog.getByText("收藏版", { exact: true }).boundingBox();
    // 长标题只压缩自己；备注限宽但不被标题挤掉。
    expect(noteBox!.width).toBeGreaterThan(20);
    expect(noteBox!.width).toBeLessThanOrEqual(100);

    const viewport = await page.evaluate(() => ({ width: window.innerWidth, scrollWidth: document.documentElement.scrollWidth }));
    expect(viewport.scrollWidth).toBeLessThanOrEqual(viewport.width);
    if (isPhoneLayout(page)) {
      await expect(dialog.getByRole("table")).toHaveCount(0);
      await expect(dialog.getByRole("list")).toBeVisible();
      const titleBox = await dialog.getByText(longTitle, { exact: true }).first().boundingBox();
      expect(titleBox!.width).toBeGreaterThan(60);
      const stateBox = await dialog.getByText(t("anime.state.active"), { exact: true }).first().boundingBox();
      expect(stateBox!.height).toBeLessThan(30);
      expect(stateBox!.width).toBeGreaterThan(40);
      const stateFilters = dialog.getByRole("group", { name: t("grid.manage.stateAria") });
      const stateFiltersBox = await stateFilters.boundingBox();
      expect(stateFiltersBox!.width).toBeGreaterThan(300);
      // One height across the row's controls, the same 32px as the desktop toolbar. The
      // scrolling chip line itself pads 6px above the buttons so the corner counts are not
      // clipped; its buttons are what line up with the other controls.
      const [originBox, seasonBox, sortBox, searchBox, progressBox, chipBox] = await Promise.all([
        dialog.getByLabel(t("grid.manage.originAria"), { exact: true }).boundingBox(),
        dialog.getByLabel(t("grid.manage.seasonAria"), { exact: true }).boundingBox(),
        dialog.getByLabel(t("grid.manage.sortAria")).boundingBox(),
        dialog.getByRole("searchbox", { name: t("grid.manage.searchAria") }).boundingBox(),
        dialog.getByRole("radiogroup", { name: t("grid.manage.progressAria") }).boundingBox(),
        stateFilters.locator("button").first().boundingBox(),
      ]);
      const toolbarHeights = [chipBox!, originBox!, seasonBox!, sortBox!, searchBox!, progressBox!].map((box) => Math.round(box.height));
      expect(new Set(toolbarHeights).size).toBe(1);
      expect(toolbarHeights[0]).toBe(32);
      const fontSizes = await Promise.all([
        stateFilters.locator("button").first().evaluate((el) => getComputedStyle(el).fontSize),
        dialog.getByRole("radiogroup", { name: t("grid.manage.progressAria") }).locator('[role="radio"]').first().evaluate((el) => getComputedStyle(el).fontSize),
        dialog.getByLabel(t("grid.manage.originAria"), { exact: true }).evaluate((el) => getComputedStyle(el).fontSize),
        dialog.getByLabel(t("grid.manage.seasonAria"), { exact: true }).evaluate((el) => getComputedStyle(el).fontSize),
        dialog.getByLabel(t("grid.manage.sortAria")).evaluate((el) => getComputedStyle(el).fontSize),
        dialog.getByRole("searchbox", { name: t("grid.manage.searchAria") }).evaluate((el) => getComputedStyle(el).fontSize),
      ]);
      expect(new Set(fontSizes)).toEqual(new Set(["12px"]));
      const chipCenterOffset = await stateFilters.locator("button").first().evaluate((el) => {
        const button = el.getBoundingClientRect();
        const range = document.createRange();
        range.selectNodeContents(el.firstChild!);
        const text = range.getBoundingClientRect();
        return Math.abs(text.x + text.width / 2 - (button.x + button.width / 2));
      });
      expect(chipCenterOffset).toBeLessThanOrEqual(1);
      // The count badge rides outside the chip: the scrolling line pads its height so the
      // digits are whole instead of being clipped by `overflow-x-auto`.
      const badge = stateFilters.locator("button").first().locator("span");
      const badgeBox = await badge.boundingBox();
      expect(badgeBox!.y).toBeGreaterThanOrEqual(stateFiltersBox!.y);
      // The shrunken selects pack one row tighter: the sort never sits alone on its line,
      // so the filters end one row earlier than the fixed-width version.
      expect(sortBox!.y).toBe(seasonBox!.y);
      expect(searchBox!.y - chipBox!.y).toBeLessThan(130);
      // The selection summary takes its own line above the actions.
      const [phoneSummaryBox, startBox] = await Promise.all([
        dialog.getByText(t("grid.manage.selNone"), { exact: true }).boundingBox(),
        dialog.getByRole("button", { name: t("grid.manage.start"), exact: true }).boundingBox(),
      ]);
      expect(phoneSummaryBox!.y + phoneSummaryBox!.height).toBeLessThanOrEqual(startBox!.y);
      return;
    }

    const origin = dialog.getByLabel(t("grid.manage.originAria"), { exact: true });
    const season = dialog.getByLabel(t("grid.manage.seasonAria"), { exact: true });
    const search = dialog.getByRole("searchbox", { name: t("grid.manage.searchAria") });
    const [originBox, seasonBox, searchBox] = await Promise.all([
      origin.boundingBox(),
      season.boundingBox(),
      search.boundingBox(),
    ]);
    expect(originBox).not.toBeNull();
    expect(seasonBox).not.toBeNull();
    expect(searchBox).not.toBeNull();
    expect(originBox!.width).toBeLessThanOrEqual(140);
    expect(seasonBox!.width).toBeLessThanOrEqual(140);
    expect(searchBox!.width).toBeLessThanOrEqual(210);
    expect(originBox!.y).toBe(seasonBox!.y);

    // The sticky header paints the dialog surface (not the page background) in either theme.
    const dialogBox = await dialog.boundingBox();
    const [headerBackground, surfaceBackground] = await Promise.all([
      dialog.locator("thead").evaluate((el) => getComputedStyle(el).backgroundColor),
      dialog.evaluate((el) => getComputedStyle(el).backgroundColor),
    ]);
    expect(headerBackground).toBe(surfaceBackground);
    // The selection summary sits at the content's left edge, the actions at the right.
    const desktopSummaryBox = await dialog.getByText(t("grid.manage.selNone"), { exact: true }).boundingBox();
    expect(Math.abs(desktopSummaryBox!.x - (dialogBox!.x + 25))).toBeLessThanOrEqual(1);

    // Every toolbar control shares one height (32px): state chips, progress segment,
    // entry/season selects, sort and search.
    const sort = dialog.getByLabel(t("grid.manage.sortAria"));
    await expect(sort).toContainText(t("grid.manage.sortNameAsc"));
    const progressFilters = dialog.getByRole("radiogroup", { name: t("grid.manage.progressAria") });
    const stateChips = dialog.getByRole("group", { name: t("grid.manage.stateAria") });
    const [sortBox, progressBox, chipsBox] = await Promise.all([
      sort.boundingBox(),
      progressFilters.boundingBox(),
      stateChips.boundingBox(),
    ]);
    const toolbarHeights = [originBox!, seasonBox!, searchBox!, sortBox!, progressBox!, chipsBox!].map((box) => Math.round(box.height));
    expect(new Set(toolbarHeights).size).toBe(1);
    expect(toolbarHeights[0]).toBe(32);
    // The first filter control shares the header/footer's 24px content line.
    expect(Math.abs(chipsBox!.x - (dialogBox!.x + 25))).toBeLessThanOrEqual(1);

    const fontSizes = await Promise.all([
      stateChips.locator("button").first().evaluate((el) => getComputedStyle(el).fontSize),
      progressFilters.locator('[role="radio"]').first().evaluate((el) => getComputedStyle(el).fontSize),
      origin.evaluate((el) => getComputedStyle(el).fontSize),
      season.evaluate((el) => getComputedStyle(el).fontSize),
      sort.evaluate((el) => getComputedStyle(el).fontSize),
      search.evaluate((el) => getComputedStyle(el).fontSize),
    ]);
    expect(new Set(fontSizes)).toEqual(new Set(["12px"]));

    // The chip's label sits at the button center; the count rides at the corner instead of
    // shifting the label left (the segmented control's text is centered the same way).
    const chipCenterOffset = await stateChips.locator("button").first().evaluate((el) => {
      const button = el.getBoundingClientRect();
      const range = document.createRange();
      range.selectNodeContents(el.firstChild!);
      const text = range.getBoundingClientRect();
      return Math.abs(text.x + text.width / 2 - (button.x + button.width / 2));
    });
    expect(chipCenterOffset).toBeLessThanOrEqual(1);

    const row = dialog.getByRole("row").filter({ hasText: longTitle }).first();
    const titleBox = await row.locator("td").nth(1).boundingBox();
    const originCellBox = await row.locator("td").nth(2).boundingBox();
    const titleTextBox = await row.locator("td").nth(1).locator("span").first().boundingBox();
    expect(titleTextBox!.x + titleTextBox!.width).toBeLessThanOrEqual(originCellBox!.x);
    expect(titleBox!.width).toBeGreaterThan(160);
  });

  test("shows the four current catalog sections", async ({ page }) => {
    await enterApp(page);
    if (isPhoneLayout(page)) {
      // 手机：「已订阅 / 发现」在底部标签栏；三个浏览分栏是页内 Segmented radio，
      // 先点「发现」让它们出现。
      const tabbar = page.getByRole("navigation", { name: "底部导航" });
      await expect(tabbar.getByRole("button", { name: "已订阅", exact: true })).toBeVisible();
      await expect(tabbar.getByRole("button", { name: "发现", exact: true })).toBeVisible();
      await tabbar.getByRole("button", { name: "发现", exact: true }).click();
      const segmented = page.getByRole("radiogroup", { name: "主导航" });
      for (const label of ["AniBT", "Mikan", "自定义"]) {
        await expect(segmented.getByRole("radio", { name: label, exact: true })).toBeVisible();
      }
      return;
    }
    for (const label of ["已订阅", "Mikan", "AniBT", "自定义"]) {
      await expect(page.getByRole("button", { name: label, exact: true })).toBeVisible();
    }
  });

  test("keeps the season picked from the dropdown on the browse toolbar", async ({ page }) => {
    // Regression: touch-picking a season made the Select report a stray null, writing season=null
    // to the address and dropping the page back to the current season.
    test.skip(!isPhoneLayout(page), "触屏选择只在手机布局下出现");
    await enterApp(page);
    await openProvider(page, "AniBT");
    const seasonOf = () => new URLSearchParams(new URL(page.url()).hash.split("?")[1] ?? "").get("season");
    const trigger = page.getByRole("combobox", { name: "选择季度" });
    for (let i = 0; i < 8; i++) {
      await trigger.tap();
      const option = page.getByRole("option").nth(i);
      const label = (await option.innerText()).trim();
      await option.tap();
      await expect(trigger).toHaveText(label);
      await page.waitForTimeout(500);
      expect(seasonOf()).toMatch(/^\d{4}-(winter|spring|summer|fall)$/);
      await expect(trigger).toHaveText(label);
    }
  });

  test("keeps provider fixtures isolated", async ({ page }) => {
    await enterApp(page);
    const anibtCard = await openProvider(page, "AniBT");
    await expect(anibtCard.getByText("星間配達人エラ", { exact: true })).toBeVisible();
    await expect(page.getByText("进击的巨人", { exact: true })).toHaveCount(0);

    const mikanCard = await openProvider(page, "Mikan");
    await expect(mikanCard.getByText("進撃の巨人", { exact: true })).toBeVisible();
    await expect(page.getByText("星际邮差艾拉", { exact: true })).toHaveCount(0);
  });

  test("masks catalog cards the site marks as having no torrents", async ({ page }) => {
    await enterApp(page);
    const anibtCard = await openProvider(page, "AniBT");
    await expect(anibtCard.getByText("12 个种子", { exact: true })).toBeVisible();
    await expect(anibtCard).not.toHaveClass(/browse-card-no-releases/);
    const anibtEmpty = await openProvider(page, "AniBT", "雾港来信");
    await expect(anibtEmpty).toHaveClass(/browse-card-no-releases/);
    await expect(anibtEmpty.getByText("暂无种子", { exact: true })).toBeVisible();
    await expect(anibtEmpty.getByText("0 个种子", { exact: true })).toHaveCount(0);

    const mikanCard = await openProvider(page, "Mikan");
    await expect(mikanCard.getByText("更新 2026/07/18", { exact: true })).toBeVisible();
    const mikanEmpty = await openProvider(page, "Mikan", "着迷男");
    await expect(mikanEmpty.getByText("暂无种子", { exact: true })).toBeVisible();
    // 遮罩不拦点击：整卡仍打开配置。
    const dialog = await openConfigFromCard(page, mikanEmpty);
    await expect(dialog).toBeVisible();
  });

  test("saves a custom preset, then keeps it after subscription", async ({ page }) => {
    await enterApp(page);
    await selectBrowseSection(page, "custom");
    await page.getByRole("button", { name: "添加预设", exact: true }).first().click();

    const dialog = pageDialog(page);
    await expect(dialog.getByText("添加自定义预设")).toBeVisible();
    const inputs = dialog.locator("input");
    await inputs.nth(0).fill("进击的巨人");
    await inputs.nth(1).fill("客厅版");
    await inputs.nth(2).fill("https://example.com/custom-e2e.xml");
    await inputs.nth(3).fill("示例字幕组");
    await dialog.getByRole("button", { name: "保存预设", exact: true }).click();

    const card = page.locator("article").filter({ hasText: "进击的巨人" }).first();
    await expect(card).toBeVisible();
    await expect(card.getByText("客厅版", { exact: true })).toBeVisible();
    await subscribeCard(card);
    await expect(card.getByText("进击的巨人", { exact: true })).toBeVisible();
  });

  test("manages the subscribed lifecycle from the library tab", async ({ page }) => {
    await enterApp(page);
    const card = await openProvider(page, "AniBT");
    await subscribeCard(card);

    // 已订阅墙是实例管理入口：计数 chip 只数生命周期（未开始/订阅中/已停止），
    // 元数据阻塞不单列一段（UI 评审 B3），完整绑定的订阅归「订阅中」。
    await selectSubscribed(page);
    const instance = page.locator("article").filter({ hasText: "星际邮差艾拉" }).first();
    await expect(instance).toBeVisible();
    const chips = page.getByRole("group", { name: t("grid.statsAria") });
    const chip = (state: "pending" | "active" | "stopped") =>
      chips.getByRole("button", { name: new RegExp(t(`anime.state.${state}`)) });
    await expect(chips.getByRole("button")).toHaveCount(3);
    await expect(chip("pending")).toContainText("0");
    await expect(chip("active")).toContainText("1");
    await expect(chip("stopped")).toContainText("0");

    // 管理动作（停止/启动、结束订阅）收在卡片右上 ⋯ 菜单里（10-19 卡片重设计），
    // 卡面只有整卡打开配置一个目标。
    const openCardMenu = async () => {
      await instance.getByRole("button", { name: t("grid.card.moreFor", { title: "星际邮差艾拉" }), exact: true }).click();
    };

    // 停止/启动双向切换：目录重读后菜单项与计数同步翻转。
    await openCardMenu();
    await page.getByRole("menuitem", { name: t("grid.card.pause"), exact: true }).click();
    await expect(chip("stopped")).toContainText("1");
    await expect(chip("active")).toContainText("0");
    await openCardMenu();
    await expect(page.getByRole("menuitem", { name: t("grid.card.start"), exact: true })).toBeVisible();
    await page.getByRole("menuitem", { name: t("grid.card.start"), exact: true }).click();
    await expect(chip("active")).toContainText("1");
    await openCardMenu();
    await expect(page.getByRole("menuitem", { name: t("grid.card.pause"), exact: true })).toBeVisible();

    // 结束订阅：阻塞确认后实例从已订阅分栏消失，回到诚实空态。菜单已开着，
    // 直接在同一份菜单里点，避免重新开菜单时与上一步的关闭动画抢 DOM。
    await page.getByRole("menuitem", { name: t("catalog.endConfirm"), exact: true }).click();
    const confirm = page.getByRole("alertdialog");
    await expect(confirm.getByText(t("catalog.endTitle", { title: "星际邮差艾拉" }))).toBeVisible();
    await confirm.getByRole("button", { name: t("catalog.endConfirm"), exact: true }).click();
    // 结束后目录重读才移除卡片；负载高的 runner 上会超过默认 5 秒。
    await expect(instance).toHaveCount(0, { timeout: 15_000 });
    await expect(page.getByText(t("catalog.emptySubscribed"), { exact: true })).toBeVisible();
  });

  // 手机上的海报卡只有 ~170px 宽，持有态徽章原先 nowrap + ellipsis，把
  // 「未关联 Bangumi、TMDB」切成省略号，看不到缺的是哪一个。
  // 窄卡片（@container max-width: 14rem）下徽章换行而不截断，全短语可见。
  test("shows the whole metadata-hold phrase on a narrow card", async ({ page }) => {
    await enterApp(page);
    // 自定义预设没有绑定任何元数据：订阅后持有态是「两个都缺」这一最长短语。
    await selectBrowseSection(page, "custom");
    await page.getByRole("button", { name: "添加预设", exact: true }).first().click();
    const dialog = pageDialog(page);
    const inputs = dialog.locator("input");
    await inputs.nth(0).fill("Narrow Hold Show");
    await inputs.nth(1).fill("客厅版");
    await inputs.nth(2).fill("https://example.com/narrow-hold.xml");
    await inputs.nth(3).fill("示例字幕组");
    await dialog.getByRole("button", { name: "保存预设", exact: true }).click();
    const card = page.locator("article").filter({ hasText: "Narrow Hold Show" }).first();
    await subscribeCard(card);
    await selectSubscribed(page);
    const instance = page.locator("article").filter({ hasText: "Narrow Hold Show" }).first();
    const chip = instance.locator(".catalog-pw-chip-hold");
    await expect(chip).toBeVisible();
    await expect(chip).toHaveText(t("anime.metadata.missing", { names: "Bangumi、TMDB" }));
    // 不截断：渲染宽度没有溢出徽章的盒子（需要换行时文本落在多行里）。
    expect(
      await chip.evaluate((element) => element.scrollWidth > element.clientWidth + 1),
    ).toBe(false);
  });

  test("opens the provider draft editor and renders RSS preview", async ({ page }) => {
    await enterApp(page);
    const card = await openProvider(page, "Mikan");
    const dialog = await openConfigFromCard(page, card);
    await expect(dialog.getByRole("tab", { name: "元数据" })).toBeVisible();
    await expect(dialog.getByRole("tab", { name: "字幕组" })).toBeVisible();
    await expect(dialog.getByRole("tab", { name: "规则" })).toBeVisible();
    await expect(dialog.getByRole("tab", { name: "RSS 预览" })).toBeVisible();
    await dialog.getByRole("tab", { name: "RSS 预览" }).click();
    await expect(dialog.getByRole("button", { name: "显示被过滤", exact: true })).toBeVisible();
    await expect(dialog.getByRole("button", { name: "预览", exact: true })).toBeVisible();
    // 激活 RSS tab 即默认读取选中字幕组的种子；夹具没有这个读模型，所以读取失败
    // 必须被明确显示（而不是空白），再点「预览」是显式重试，结果相同。
    await expect(dialog.getByRole("alert")).toContainText("操作失败，请重试");
    await dialog.getByRole("button", { name: "预览", exact: true }).click();
    await expect(dialog.getByRole("alert")).toContainText("操作失败，请重试");
  });

  test("guards every close path when the provider draft is dirty", async ({ page }) => {
    await enterApp(page);
    const card = await openProvider(page, "Mikan");
    const dialog = await openConfigFromCard(page, card);
    await dialog.getByRole("tab", { name: "规则", exact: true }).click();

    const parser = dialog.locator("section").filter({ hasText: "解析" }).first();
    await parser.getByLabel("集数识别规则", { exact: true }).fill("自定义解析规则");
    await parser.getByLabel("集数识别规则", { exact: true }).press("Enter");
    await expect(dialog.getByText(/^有 \d+ 处未保存$/)).toBeVisible();
    await page.keyboard.press("Escape");

    await expect(page.getByRole("alertdialog")).toBeVisible();
    await expect(page.getByRole("alertdialog").getByText("放弃未保存的修改？", { exact: true })).toBeVisible();
    await page.getByRole("alertdialog").getByRole("button", { name: "取消", exact: true }).click();
    await expect(dialog).toBeVisible();
    // 弹窗有两个「关闭」：标题栏 X（aria-label=关闭）与页脚关闭按钮。
    // 这里点页脚按钮——排除带 aria-label 的 X，避免 strict mode 撞名。
    await dialog
      .getByRole("button", { name: "关闭", exact: true })
      .and(dialog.locator("button:not([aria-label])"))
      .click();
    await expect(page.getByRole("alertdialog")).toBeVisible();
    await page.getByRole("alertdialog").getByRole("button", { name: "放弃修改", exact: true }).click();
    // 「丢弃并关闭」直接关闭配置弹窗（一级详情弹窗已移除，无中间层）。
    await expect(pageDialog(page)).toHaveCount(0);
  });

  test("keeps provider rules draft changes when opening RSS preview", async ({ page }) => {
    await enterApp(page);
    const card = await openProvider(page, "Mikan");
    const dialog = await openConfigFromCard(page, card);
    await dialog.getByRole("tab", { name: "规则", exact: true }).click();

    const parser = dialog.locator("section").filter({ hasText: "解析" }).first();
    await parser.getByLabel("集数识别规则", { exact: true }).fill("自定义解析规则");
    await parser.getByLabel("集数识别规则", { exact: true }).press("Enter");

    await dialog.getByRole("tab", { name: "RSS 预览", exact: true }).click();
    await expect(dialog.getByRole("button", { name: "显示被过滤", exact: true })).toBeVisible();
    await expect(dialog.getByRole("button", { name: "预览", exact: true })).toBeVisible();
    // The RSS workspace is the only draft RSS surface; switching to it keeps
    // the dirty marker and does not expose the retired parallel preview block.
    await expect(dialog.getByText(/^有 \d+ 处未保存$/)).toBeVisible();
    await expect(dialog.getByText("预览结果，尚未写入任何数据", { exact: true })).toHaveCount(0);
  });

  test("saves subgroup strategy and max_rss as a complete Subgroups owner patch", async ({ page }) => {
    await mockWorkspace(page);
    let patchBody: unknown;
    await page.route("**/api/v1/anime/*/subgroups", async (route) => {
      const request = route.request();
      // 字幕组 owner 保存走 PATCH；错误方法显式 405。
      if (request.method() !== "PATCH") {
        await fulfillMethodNotAllowed(route, "PATCH");
        return;
      }
      patchBody = request.postDataJSON();
      const id = new URL(request.url()).pathname.split("/").at(-2) ?? "";
      const current = workspaceFixture(id, "星际邮差艾拉", fixtureStateFor(page));
      const patch = patchBody as { expected_version: number; subgroups: unknown };
      if (patch.expected_version !== current.subgroups.version) {
        await route.fulfill({
          status: 409,
          contentType: "application/json",
          body: JSON.stringify({ code: "OWNER_VERSION_CONFLICT", message: "字幕组版本冲突", current_version: current.subgroups.version }),
        });
        return;
      }
      const nextVersion = current.subgroups.version + 1;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          managed_anime_id: id,
          subgroups: patch.subgroups,
          effective_selection: current.subgroups.effective_selection,
          rss_sources: current.subgroups.rss_sources,
          version: nextVersion,
        }),
      });
    });

    await enterApp(page);
    const card = await openProvider(page, "AniBT");
    await subscribeCard(card);
    const dialog = await openSubscribedConfig(page, "星际邮差艾拉");
    await dialog.getByRole("tab", { name: "字幕组", exact: true }).click();
    await dialog.getByRole("button", { name: "最多订阅字幕组数", exact: true }).click();
    await expect(page.getByRole("dialog", { name: "最多订阅字幕组数" })).toBeVisible();
    await page.getByRole("dialog", { name: "最多订阅字幕组数" }).getByLabel("最多订阅字幕组数", { exact: true }).fill("5");
    await page.getByRole("dialog", { name: "最多订阅字幕组数" }).getByRole("button", { name: "应用", exact: true }).click();
    // 行内 SegmentedControl 调策略：Ranked 组 → 必选（always）产生 GroupAction force_include。
    const row = dialog.locator("li").filter({ hasText: "Ranked 字幕组" }).first();
    await row.getByRole("radio", { name: "必选", exact: true }).click();
    await dialog.getByRole("button", { name: "保存", exact: true }).click();

    await expect(page.getByText("字幕组设置已保存", { exact: true })).toBeVisible();
    expect(Object.keys(patchBody as Record<string, unknown>).sort()).toEqual([
      "expected_version",
      "group_actions",
      "subgroups",
    ]);
    expect(patchBody).toEqual({
      expected_version: 1,
      group_actions: [
        { provider: "anibt", group_ref: "ranked-group", action: "force_include" },
      ],
      subgroups: {
        max_rss: 5,
        entries: [
          {
            provider: "anibt",
            group_ref: "ranked-group",
            display_name: "Ranked 字幕组",
            selection_mode: "manual",
            manual_selected: true,
            effective_selected: true,
            score: 90,
          },
          {
            provider: "anibt",
            group_ref: "always-group",
            display_name: "Always 字幕组",
            selection_mode: "manual",
            manual_selected: true,
            effective_selected: true,
            score: 70,
          },
          {
            provider: "anibt",
            group_ref: "never-group",
            display_name: "Never 字幕组",
            selection_mode: "manual",
            manual_selected: false,
            effective_selected: false,
            score: 50,
          },
        ],
      },
    });
  });

  test("preserves owner rules edits after a version conflict", async ({ page }) => {
    await mockWorkspace(page);
    let patchBody: unknown;
    await page.route("**/api/v1/anime/*/rules", async (route) => {
      const request = route.request();
      // 规则 owner 保存走 PATCH；错误方法显式 405，不落入冲突分支。
      if (request.method() !== "PATCH") {
        await fulfillMethodNotAllowed(route, "PATCH");
        return;
      }
      patchBody = request.postDataJSON();
      await route.fulfill({
        status: 409,
        contentType: "application/json",
        body: JSON.stringify({
          code: "OWNER_VERSION_CONFLICT",
          message: "规则版本冲突",
          current_version: 7,
        }),
      });
    });

    await enterApp(page);
    const card = await openProvider(page, "AniBT");
    await subscribeCard(card);
    const dialog = await openSubscribedConfig(page, "星际邮差艾拉");
    await dialog.getByRole("tab", { name: "规则", exact: true }).click();
    const parser = dialog.locator("section").filter({ hasText: "解析" }).first();
    await parser.getByLabel("集数识别规则", { exact: true }).fill("冲突后仍保留的规则");
    await parser.getByLabel("集数识别规则", { exact: true }).press("Enter");
    await dialog.getByRole("button", { name: "保存", exact: true }).click();

    await expect(
      dialog.getByText("内容已被修改，请刷新后重新编辑", { exact: true }).first(),
    ).toBeVisible();
    await expect(parser.getByText("冲突后仍保留的规则", { exact: true })).toBeVisible();

    const body = patchBody as {
      expected_version: number;
      sections: Record<string, unknown>;
    };
    expect(body.expected_version).toBe(1);
    expect(Object.keys(body.sections).sort()).toEqual([
      "file_effect",
      "file_filter",
      "naming",
      "parser",
      "release_filter",
      "tag_filters",
    ]);
    expect(body.sections.parser).toEqual({
      mode: "replace",
      value: { episode_patterns: ["冲突后仍保留的规则"] },
    });
    // 其余各段原样上送番剧自己的完整值。
    expect(body.sections.release_filter).toEqual({ mode: "replace", value: { must_contain: "", not_in: "" } });
    expect(body.sections.file_filter).toEqual({ mode: "replace", value: { must_contain: "", not_in: "" } });
    expect(body.sections.tag_filters).toEqual({ mode: "replace", value: { anibt: { require: [], exclude: [] }, title: { require: [], exclude: [] } } });
    expect(body.sections.download_completion).toBeUndefined();
    expect(body.sections.file_effect).toEqual({ mode: "replace", value: { operation: "hardlink", collision_policy: "versioned" } });
  });

  test("reaches the owner metadata tab and saves a title override", async ({ page }) => {
    await mockWorkspace(page);
    // 服务端语义镜像：PATCH 落到内存，refetch 读回 title_override，展示断言
    // 不与 workspace 重读竞态（行内交互细节由组件测试覆盖，这里验证浏览器接线）。
    let titleOverride: string | null = null;
    let metadataVersion = 1;
    let metadataPatch: unknown;
    const state = fixtureStateFor(page);
    await page.route("**/api/v1/anime/**/workspace", async (route) => {
      const request = route.request();
      if (request.method() !== "GET") {
        await fulfillMethodNotAllowed(route, "GET");
        return;
      }
      const id = request.url().split("/anime/")[1]?.split("/")[0] ?? "fixture-anime";
      const body = workspaceFixture(id, "星际邮差艾拉", state);
      Object.assign(body.metadata, { bgm: null, tmdb: null, title_override: titleOverride, version: metadataVersion });
      // 服务端由绑定推导 hold：两项都未绑定 → 阻塞并写明缺哪两项。
      body.metadata_hold = { held: true, missing: ["bgm", "tmdb"] };
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(body),
      });
    });
    await page.route("**/api/v1/anime/**/metadata", async (route) => {
      const request = route.request();
      if (request.method() !== "PATCH") {
        await route.fulfill({
          status: 405,
          contentType: "application/json",
          body: JSON.stringify({ code: "METHOD_NOT_ALLOWED", message: "fixture 仅支持 PATCH" }),
        });
        return;
      }
      const patch = request.postDataJSON() as { title_override: string | null };
      metadataPatch = patch;
      titleOverride = patch.title_override;
      metadataVersion += 1;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          managed_anime_id: new URL(request.url()).pathname.split("/").at(-2),
          bgm: null,
          tmdb: null,
          title_override: titleOverride,
          version: metadataVersion,
        }),
      });
    });

    await enterApp(page);
    const card = await openProvider(page, "AniBT");
    await subscribeCard(card);
    const dialog = await openSubscribedConfig(page, "星际邮差艾拉");

    // 自动化阻塞只说一次（UI 评审 D1）：订阅中且缺绑定时，信息行换成一行长形警告
    // （自动下载已暂停：未关联 Bangumi、TMDB）加修复动作，修复先指向第一项缺失。
    const missingNames = joinList(["Bangumi", "TMDB"]);
    await expect(dialog.getByRole("status").filter({ hasText: t("anime.metadata.paused", { names: missingNames }) })).toBeVisible();
    await expect(dialog.getByRole("button", { name: t("anime.metadata.link", { name: "Bangumi" }), exact: true })).toBeVisible();
    await dialog.getByRole("tab", { name: t("tabs.names.metadata"), exact: true }).click();

    // 标题是固定值：常态只读，点「编辑」才出现输入框；「保存标题」写回元数据 tab
    // 草稿，footer 的唯一保存发一次 PATCH 上送 title_override，绑定回传基线值；
    // 只改标题不安排重新评估。
    const titleRow = dialog.locator('section[aria-labelledby="owner-title-label"]');
    await expect(titleRow.getByText("星际邮差艾拉", { exact: true })).toBeVisible();
    await titleRow.getByRole("button", { name: t("tabs.metadata.editTitleAria"), exact: true }).click();
    const titleInput = titleRow.getByLabel(t("tabs.metadata.title"), { exact: true });
    await expect(titleInput).toHaveValue("星际邮差艾拉");
    await titleInput.fill("星际邮差艾拉 覆盖");
    await titleRow.getByRole("button", { name: t("tabs.metadata.saveTitle"), exact: true }).click();
    await expect(dialog.getByText(t("act.unsavedCount", { n: 1 }), { exact: true })).toBeVisible();
    await dialog.locator(".anime-config-dialog-footer-owner").getByRole("button", { name: t("act.save"), exact: true }).click();

    await expect(page.getByText(t("tabs.metadata.saved"), { exact: true })).toBeVisible();
    await expect(titleRow.getByText("星际邮差艾拉 覆盖", { exact: true })).toBeVisible();
    await expect(dialog.getByText(t("act.unsavedCount", { n: 1 }), { exact: true })).toHaveCount(0);
    expect(metadataPatch).toEqual({
      expected_version: 1,
      bgm: null,
      tmdb: null,
      title_override: "星际邮差艾拉 覆盖",
      total_episodes: null,
      year: null,
      // 季度是 owner 字段（与 TMDB id 解耦），同一 PATCH 回送当前值。
      season: null,
      // 类型行与标题行共用同一 PATCH：省略即在服务端清除固定值，故每次都要回送当前值。
      media_kind: null,
    });
  });

  test("saves the file-effect policy from the rules tab library section", async ({ page }) => {
    // 产品契约没有第五个 Files tab：文件维护入口就是规则 tab 的「入库」段
    // （copy/move/hardlink 与同名文件处理）。这里覆盖 owner 模式下该入口的编辑与保存。
    await mockWorkspace(page);
    let patchBody: unknown;
    await page.route("**/api/v1/anime/*/rules", async (route) => {
      const request = route.request();
      // 规则 owner 保存走 PATCH；错误方法显式 405。
      if (request.method() !== "PATCH") {
        await fulfillMethodNotAllowed(route, "PATCH");
        return;
      }
      patchBody = request.postDataJSON();
      const id = new URL(request.url()).pathname.split("/").at(-2) ?? "";
      const current = workspaceFixture(id, "星际邮差艾拉", fixtureStateFor(page));
      const patch = patchBody as { expected_version: number; sections: Record<string, unknown> };
      if (patch.expected_version !== current.rules.version) {
        await route.fulfill({
          status: 409,
          contentType: "application/json",
          body: JSON.stringify({ code: "OWNER_VERSION_CONFLICT", message: "规则版本冲突", current_version: current.rules.version }),
        });
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          rules: { ...current.rules, sections: patch.sections, version: current.rules.version + 1 },
          reevaluation: { managed_anime_id: id, accepted: true, scheduled_count: 1 },
        }),
      });
    });

    await enterApp(page);
    const card = await openProvider(page, "AniBT");
    await subscribeCard(card);
    const dialog = await openSubscribedConfig(page, "星际邮差艾拉");
    await dialog.getByRole("tab", { name: "规则", exact: true }).click();

    const fileEffect = dialog.locator("section").filter({ has: page.getByRole("combobox", { name: "整理方式", exact: true }) }).first();
    await fileEffect.getByRole("combobox", { name: "整理方式", exact: true }).click();
    await page.getByRole("option", { name: "移动", exact: true }).click();
    await dialog.getByRole("button", { name: "保存", exact: true }).click();

    await expect(page.getByText("规则已保存，已安排重新评估 1 个种子", { exact: true })).toBeVisible();
    const body = patchBody as {
      expected_version: number;
      sections: Record<string, unknown>;
    };
    expect(body.expected_version).toBe(1);
    // 覆盖值以服务端生效值为底（hardlink/versioned），只替换用户改过的操作。
    expect(body.sections.file_effect).toEqual({
      mode: "replace",
      value: { operation: "move", collision_policy: "versioned" },
    });
  });

  test("searches and selects metadata from the draft editor", async ({ page }) => {
    await enterApp(page);
    const card = await openProvider(page, "Mikan");
    const dialog = await openConfigFromCard(page, card);
    await expect(dialog.getByText("Bangumi 条目", { exact: true })).toBeVisible();
    await dialog.getByRole("button", { name: "清除 Bangumi 条目 关联" }).click();
    // 草稿元数据查看态有「匹配 BGM」按钮，打开受控 MetadataMatchDialog（provider 版）。
    await dialog.getByRole("button", { name: "匹配 Bangumi", exact: true }).click();
    const matchDialog = pageDialog(page).last();
    await matchDialog.getByRole("button", { name: "自动查询", exact: true }).click();
    // 本地演示候选（fixture searcher）命中进击的巨人，点选写回草稿绑定。
    const candidate = matchDialog.getByRole("listbox", { name: "匹配结果" }).getByText("进击的巨人", { exact: true });
    await expect(candidate).toBeVisible();
    await candidate.click();
    await expect(matchDialog.getByRole("button", { name: "确认匹配", exact: true })).toBeEnabled();
    await matchDialog.getByRole("button", { name: "确认匹配", exact: true }).click();
    await expect(dialog.getByRole("button", { name: "清除 Bangumi 条目 关联" })).toBeVisible();
  });

  test("opens subscribed workspace with four tabs and no embedded log", async ({ page }) => {
    await mockWorkspace(page);
    await enterApp(page);
    const card = await openProvider(page, "AniBT");
    await subscribeCard(card);
    const dialog = await openSubscribedConfig(page, "星际邮差艾拉");
    for (const label of ["元数据", "字幕组", "规则", "RSS"]) {
      await expect(dialog.getByRole("tab", { name: label, exact: true })).toBeVisible();
    }
    await expect(dialog.getByRole("tab", { name: "Log", exact: true })).toHaveCount(0);
    await expect(dialog.getByText("WorkItem")).toHaveCount(0);
  });

  test("a pending subscription's RSS tab is the test bench until it is started", async ({ page }) => {
    await mockWorkspace(page);
    const benchReads: Array<Record<string, unknown>> = [];
    // 后注册的路由优先：测试台读按实例 + feed 身份读（owner 模式），返回服务端组合的种子视图。
    await page.route("**/api/v1/anime/rss-list-view", async (route) => {
      const input = route.request().postDataJSON() as Record<string, unknown>;
      if (!Array.isArray(input.feed)) return route.fallback();
      benchReads.push(input);
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          mode: "owner",
          config_identity: `owner_bench:${String(input.managed_anime_id)}@1`,
          metadata_hold: { held: false, missing: [] },
          versions: { parser: "p", evaluator: "e", renderer: "1", safety: "s", torrent_parser: "t" },
          generated_at: "2026-10-19T00:00:00Z",
          unused_feed_identities: [],
          truncated: false,
          limit_per_group: 100,
          groups: [{
            scope: { provider: "anibt", provider_entry_ref: "fixture", provider_group_ref: "bench-group" },
            display_name: "测试台字幕组",
            score: 50,
            selection: { effective_selected: true, selection_mode: "auto" },
            offset: { value: 0, editable: false },
            feed: { state: "fresh", snapshot_id: "snap-bench", snapshot_version: 1, content_hash: "sha256:" + "a".repeat(64) },
            items: [{
              snapshot_id: "snap-bench",
              stable_seed_key: "seed-bench",
              seed: { title: "[测试台字幕组] 星际邮差艾拉 - 01", published_at: "2026-10-01T00:00:00Z", locator_display: "https://torrent.example.test/01.torrent" },
              torrent: { state: "pending" },
              evaluation: { state: "evaluated", verdict: "accepted_pending_download", reason_code: "accepted", parsed_facts: { episode: 1 } },
              files: [],
              download_action: { allowed: false, kind: "none", reason_code: "not_started" },
            }],
          }],
        }),
      });
    });
    await enterApp(page);
    const card = await openProvider(page, "AniBT");
    await subscribeCard(card, { start: false });
    await expect(page.getByText(t("grid.subscribedPending", { title: "星际邮差艾拉" }))).toBeVisible();
    const dialog = await openSubscribedConfig(page, "星际邮差艾拉");
    await expect(dialog.getByText(t("anime.state.pending"), { exact: true }).first()).toBeVisible();
    // 未开始只在弹窗顶部说一行（UI 评审 D3），测试台说明收进 HelpTip，RSS tab 不再叠横幅。
    const pendingLine = dialog.getByText(t("anime.pending.banner"), { exact: true });
    await expect(pendingLine).toBeVisible();
    await dialog.getByRole("tab", { name: t("tabs.names.rss"), exact: true }).click();
    await expect(pendingLine).toBeVisible();
    await expect(dialog.getByText("测试台字幕组", { exact: true })).toBeVisible();
    expect(benchReads[0]).toMatchObject({ input_source: "owner" });
    await expect(dialog.getByRole("button", { name: /^刷新/ })).toHaveCount(0);

    await dialog.getByRole("button", { name: t("anime.start"), exact: true }).click();
    await expect(page.getByText(t("anime.started"), { exact: true })).toBeVisible();
    await expect(pendingLine).toHaveCount(0);
    await expect(dialog.getByRole("button", { name: t("anime.start"), exact: true })).toHaveCount(0);
  });

  test("shows RSS source facts, saves explicit zero offset, and submits finite refresh", async ({ page }) => {
    await mockWorkspace(page);
    await enterApp(page);
    const card = await openProvider(page, "AniBT");
    await subscribeCard(card);
    const dialog = await openSubscribedConfig(page, "星际邮差艾拉");
    await dialog.getByRole("tab", { name: "RSS", exact: true }).click();
    await expect(dialog.getByText("北宇治字幕组", { exact: true })).toBeVisible();
    await expect(dialog.getByText("https://example.test/rss", { exact: true })).toHaveCount(0);

    await page.route("**/api/v1/anime/*/rss-sources/*/offset", async (route) => {
      // 偏移保存走 PATCH；错误方法显式 405。
      if (route.request().method() !== "PATCH") {
        await fulfillMethodNotAllowed(route, "PATCH");
        return;
      }
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({}) });
    });
    let refreshedSourceID: string | undefined;
    let refreshIdempotencyKey: string | undefined;
    let automationCalled = false;
    await page.route("**/api/v1/rss-sources/*/refresh", async (route) => {
      // 刷新提交走 POST（携带 Idempotency-Key）；错误方法显式 405。
      if (route.request().method() !== "POST") {
        await fulfillMethodNotAllowed(route, "POST");
        return;
      }
      refreshedSourceID = new URL(route.request().url()).pathname.split("/").at(-2);
      refreshIdempotencyKey = route.request().headers()["idempotency-key"];
      await route.fulfill({
        status: 202,
        contentType: "application/json",
        body: JSON.stringify({ accepted: true, work_item_id: "fixture-work-item" }),
      });
    });
    await page.route("**/api/v1/automation/**", async (route) => {
      // Canary：证明有限刷新走 rss-source 级端点，旧全局 automation 端点不被
      // 调用。仅「真实契约中存在的方法 × 正确 shape」视为踩中 canary——保持
      // 500 并置 automationCalled=true，让末尾断言失败暴露回归；其余方法按
      // 真实服务端语义显式 405 且不计入 canary。契约来源：generated mux 注册
      // 的 PATCH /automation/{kind}、POST /automation/{kind}/run、
      // GET /automation/runs/{run_id}（GET /automation 列表由 shell mock 承接，
      // 不落入本 ** 路由）。
      const request = route.request();
      const rest = new URL(request.url()).pathname.split("/api/v1/automation/")[1] ?? "";
      const method = request.method();
      const correctAutomationCall =
        (method === "PATCH" && /^[^/]+$/.test(rest)) ||
        (method === "POST" && /^[^/]+\/run$/.test(rest)) ||
        (method === "GET" && /^runs\/[^/]+$/.test(rest));
      if (!correctAutomationCall) {
        await fulfillMethodNotAllowed(route, "PATCH /{kind}、POST /{kind}/run、GET /runs/{run_id}");
        return;
      }
      automationCalled = true;
      await route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({}) });
    });

    await dialog.getByRole("button", { name: /调整 .* 偏移/ }).click();
    await expect(page.getByText("该 RSS 链接的集数偏移，0 表示不偏移")).toBeVisible();
    const offsetInput = page.getByRole("spinbutton", { name: "集数偏移" });
    await offsetInput.fill("0");
    await page.getByRole("button", { name: "保存偏移", exact: true }).click();
    await expect(page.getByText("RSS 偏移已保存", { exact: true })).toBeVisible();

    await dialog.getByRole("button", { name: "刷新 RSS", exact: true }).click();
    await expect(page.getByText("RSS 刷新已提交（1 个 RSS 源）", { exact: true })).toBeVisible();
    expect(refreshedSourceID).toBe("source-fixture-anime-2");
    expect(refreshIdempotencyKey).toBeTruthy();
    expect(automationCalled).toBe(false);
  });

  test("opens nested release details from the subscribed card", async ({ page }) => {
    await mockWorkspace(page);
    await enterApp(page);
    const card = await openProvider(page, "AniBT");
    await subscribeCard(card);

    // 订阅后配置弹窗：RSS tab 展示 owner 链（RssOwnerChain），展开组看到 release 行。
    const detail = await openSubscribedConfig(page, "星际邮差艾拉");
    await detail.getByRole("tab", { name: "RSS", exact: true }).click();
    await detail.getByRole("button", { name: /展开 北宇治字幕组/ }).click();
    await expect(detail.getByText(/星际邮差艾拉 - 01/)).toBeVisible();
  });

  test("topbar tools open honest side surfaces", async ({ page }) => {
    await enterApp(page);
    // 隐藏的番剧只在设置分区里：账户菜单与「更多」都没有它。
    if (isPhoneLayout(page)) {
      await page.getByRole("navigation", { name: "底部导航" }).getByRole("button", { name: "更多", exact: true }).click();
      const sheet = page.getByRole("dialog", { name: "更多" });
      await expect(sheet).toBeVisible();
      await expect(sheet.getByRole("button", { name: "隐藏的番剧" })).toHaveCount(0);
      await sheet.getByRole("button", { name: "关闭", exact: true }).click();
    } else {
      await page.getByRole("button", { name: "账户", exact: true }).click();
      await expect(page.getByRole("menu")).toBeVisible();
      await expect(page.getByRole("menuitem", { name: "隐藏的番剧" })).toHaveCount(0);
      await page.keyboard.press("Escape");
    }
    await expect(pageDialog(page)).toHaveCount(0);

    await page.goto("/#/settings/hidden");
    const settings = page.getByRole("dialog", { name: "设置" });
    await expect(settings.getByRole("heading", { level: 2, name: "隐藏的番剧" }).last()).toBeVisible();
    await expect(settings.getByLabel("站点番剧 ID")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(pageDialog(page)).toHaveCount(0);

    await openShellTool(page, "日志");
    const recordsDialog = pageDialog(page);
    await expect(recordsDialog.getByText("日志", { exact: true })).toBeVisible();
    await expect(recordsDialog.getByRole("tab", { name: "事件", exact: true })).toBeVisible();
    await expect(recordsDialog.getByRole("tab", { name: "诊断", exact: true })).toBeVisible();
  });

  test("route-hosts the topbar tools and the log: deep link, refresh, Back", async ({ page }) => {
    await enterApp(page);
    await selectBrowseSection(page, "mikan");
    await expect(page).toHaveURL(/#\/\?source=mikan$/);

    await openShellTool(page, "日志");
    await expect(page.getByRole("dialog", { name: "日志" })).toBeVisible();
    await expect(page).toHaveURL(/#\/\?source=mikan&log=records$/);
    await page.goBack();
    await expect(pageDialog(page)).toHaveCount(0);
    await expect(page).toHaveURL(/#\/\?source=mikan$/);

    // 旧的 ?tool=blacklist 链接不再打开任何弹窗，回落到目录底座。
    await page.goto("/#/?source=mikan&tool=blacklist");
    await expect(page).toHaveURL(/#\/\?source=mikan$/);
    await expect(pageDialog(page)).toHaveCount(0);

    const cases: { query: string; name: string }[] = [
      { query: "tool=search", name: "搜索番剧" },
      { query: "log=diagnostics", name: "日志" },
    ];
    for (const { query, name } of cases) {
      await page.goto(`/#/?source=mikan&${query}`);
      const dialog = page.getByRole("dialog", { name });
      await expect(dialog).toBeVisible();
      await page.reload();
      await expect(dialog).toBeVisible();
      if (query === "log=diagnostics") {
        await expect(dialog.getByRole("tab", { name: "诊断", exact: true })).toHaveAttribute("aria-selected", "true");
        await dialog.getByRole("tab", { name: "事件", exact: true }).click();
        await expect(page).toHaveURL(/log=records/);
      }
      await page.keyboard.press("Escape");
      await expect(pageDialog(page)).toHaveCount(0);
      await expect(page).toHaveURL(/#\/\?source=mikan$/);
    }
  });

  test("route-hosts the anime config and guards Back when it holds unsaved edits", async ({ page }) => {
    await enterApp(page);
    const card = await openProvider(page, "Mikan");
    const dialog = await openConfigFromCard(page, card);
    await expect(dialog.getByRole("tab", { name: "元数据" })).toBeVisible();
    await expect(page).toHaveURL(/[?&]anime=[^&]+/);
    await expect(page).toHaveURL(/[?&]tab=metadata/);
    await expect(page).toHaveURL(/[?&]source=mikan/);

    await dialog.getByRole("tab", { name: "规则", exact: true }).click();
    await expect(page).toHaveURL(/[?&]tab=rules/);
    await page.reload();
    const restored = page.getByRole("dialog", { name: "进击的巨人" });
    await expect(restored).toBeVisible();
    await expect(restored.getByRole("tab", { name: "规则", exact: true })).toHaveAttribute("aria-selected", "true");
    const openedUrl = page.url();

    const parser = restored.locator("section").filter({ hasText: "解析" }).first();
    await parser.getByLabel("集数识别规则", { exact: true }).fill("自定义解析规则");
    await parser.getByLabel("集数识别规则", { exact: true }).press("Enter");
    await expect(restored.getByText(/^有 \d+ 处未保存$/)).toBeVisible();

    await page.goBack();
    const confirm = page.getByRole("alertdialog");
    await expect(confirm.getByText("放弃未保存的修改？", { exact: true })).toBeVisible();
    // Blocking sheets have no drag handle (phone).
    await expect(confirm.locator("[data-drag-handle]")).toHaveCount(0);
    await confirm.getByRole("button", { name: "取消", exact: true }).click();
    await expect(confirm).toHaveCount(0);
    await expect(restored).toBeVisible();
    await expect.poll(() => page.url()).toBe(openedUrl);
    await expect(restored.getByText(/^有 \d+ 处未保存$/)).toBeVisible();

    await page.goBack();
    await page.getByRole("alertdialog").getByRole("button", { name: "放弃修改", exact: true }).click();
    await expect(pageDialog(page)).toHaveCount(0);
    await expect(page).toHaveURL(/#\/\?source=mikan$/);
  });

  test("keeps a dirty anime config open when the address is edited by hand", async ({ page }) => {
    await enterApp(page);
    const card = await openProvider(page, "Mikan");
    const dialog = await openConfigFromCard(page, card);
    await dialog.getByRole("tab", { name: "规则", exact: true }).click();
    const parser = dialog.locator("section").filter({ hasText: "解析" }).first();
    await parser.getByLabel("集数识别规则", { exact: true }).fill("自定义解析规则");
    await parser.getByLabel("集数识别规则", { exact: true }).press("Enter");
    await expect(dialog.getByText(/^有 \d+ 处未保存$/)).toBeVisible();

    // A hand-typed hash creates a history entry react-router did not create, so
    // its blocker cannot stop it; the dialog stays and asks instead.
    await page.evaluate(() => {
      window.location.hash = "#/?source=mikan";
    });
    const confirm = page.getByRole("alertdialog");
    await expect(confirm.getByText("放弃未保存的修改？", { exact: true })).toBeVisible();
    // Still mounted under the confirmation (modal siblings are aria-hidden).
    await expect(page.getByRole("dialog", { name: "进击的巨人", includeHidden: true })).toHaveCount(1);
    await confirm.getByRole("button", { name: "取消", exact: true }).click();
    await expect(dialog).toBeVisible();
    await expect(page).toHaveURL(/[?&]anime=[^&]+/);
    await expect(page).toHaveURL(/[?&]tab=rules/);
    await expect(dialog.getByText(/^有 \d+ 处未保存$/)).toBeVisible();

    await page.evaluate(() => {
      window.location.hash = "#/?source=mikan";
    });
    await page.getByRole("alertdialog").getByRole("button", { name: "放弃修改", exact: true }).click();
    await expect(pageDialog(page)).toHaveCount(0);
    await expect(page).toHaveURL(/#\/\?source=mikan$/);
  });

  test("drags a phone bottom sheet down to close it, springs back on a short drag", async ({ page, isMobile }) => {
    test.skip(!isMobile, "phone bottom sheet only");
    await enterApp(page);
    await selectBrowseSection(page, "custom");
    await page.getByRole("button", { name: "添加预设", exact: true }).first().click();
    const sheet = page.getByRole("dialog", { name: "添加自定义预设" });
    await expect(sheet).toBeVisible();
    const handle = sheet.locator("[data-drag-handle]");
    await expect(handle).toBeVisible();
    const before = await sheet.boundingBox();
    const box = await handle.boundingBox();
    if (!before || !box) throw new Error("sheet not laid out");
    const x = box.x + box.width / 2;
    const y = box.y + box.height / 2;

    // Short, slow drag: springs back.
    await page.mouse.move(x, y);
    await page.mouse.down();
    await page.mouse.move(x, y + 30, { steps: 10 });
    await page.waitForTimeout(150);
    await page.mouse.move(x, y + 31);
    await page.mouse.up();
    await expect.poll(async () => (await sheet.boundingBox())?.y).toBeCloseTo(before.y, 0);
    await expect(sheet).toBeVisible();

    // Past the distance threshold: closes.
    await page.mouse.move(x, y);
    await page.mouse.down();
    await page.mouse.move(x, y + 200, { steps: 12 });
    await page.mouse.up();
    await expect(sheet).toHaveCount(0);
  });

  test("does not overflow at mobile width", async ({ page, isMobile }) => {
    test.skip(!isMobile, "mobile-only layout check");
    await enterApp(page);
    await openProvider(page, "AniBT");
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);
  });
});

