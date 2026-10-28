import { test, expect, type Page, type Route } from "@playwright/test";
import type { AnimeListItem, AnimeWorkspace, CatalogWorkspace, DiagnosticEpisodePage, GlobalSectionView, NamingTemplateCatalog, NamingTemplateLibraryView, ObservabilityRecordPage, ReleaseRetryResult, RSSRefreshAccepted } from "../src/lib/api/client";
import { ownerRSSView } from "./fixtures/ownerRSSView";
import { selectSubscribed } from "./catalog-nav";
import { mockSignedIn } from "./session";

// Fixture-only browser evidence for the subscribed RSS owner path:
// finite source refresh and row-level manual download remain reachable through
// the shared RSS renderer without the retired draft one-shot activation flow.
const fixtureOnly = process.env.BANSOKO_E2E_FIXTURES === "1";
const TITLE = "星际邮差艾拉";
const GROUP = "北宇治字幕组";
const ANIME_ID = "fixture-anime-owner";

function ok(route: Route, status: number, body: unknown) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
}

function methodNotAllowed(route: Route, allowed: string) {
  return ok(route, 405, { code: "METHOD_NOT_ALLOWED", message: `fixture 仅支持 ${allowed}` });
}

function ownerWorkspace(retried: boolean): AnimeWorkspace {
  const subscriptionID = `subscription-${ANIME_ID}`;
  const sourceID = `source-${ANIME_ID}`;
  const releaseID = `release-${ANIME_ID}`;
  return {
    managed_anime: { id: ANIME_ID, title: TITLE, title_override: null, version: 1 },
    subscription: {
      id: subscriptionID,
      managed_anime_id: ANIME_ID,
      origin: "anibt",
      origin_entry_ref: "anibt:stellar-mail-ella",
      instance_label: TITLE,
      state: "active",
      lifecycle_state: "active",
      lifecycle_epoch: 1,
      version: 1,
      new_releases_only: false,
    },
    metadata: { managed_anime_id: ANIME_ID, bgm: null, tmdb: null, version: 1 },
    metadata_hold: { held: true, missing: ["bgm", "tmdb"] },
    subgroups: {
      managed_anime_id: ANIME_ID,
      subgroups: {
        max_rss: 3,
        entries: [{
          provider: "anibt",
          group_ref: "ranked-group",
          display_name: GROUP,
          selection_mode: "auto",
          manual_selected: null,
          effective_selected: true,
          score: 90,
        }],
      },
      effective_selection: [{
        id: `selection-${ANIME_ID}`,
        subscription_id: subscriptionID,
        provider: "anibt",
        group_ref: "ranked-group",
        display_name: GROUP,
        selection_mode: "auto",
        manual_selected: null,
        effective_selected: true,
        effective_score: 90,
        available: true,
        availability_generation: 1,
        version: 1,
      }],
      rss_sources: [{
        id: sourceID,
        subscription_id: subscriptionID,
        source_kind: "provider_group",
        locator_display: "https://example.test/rss",
        group_name: GROUP,
        title: TITLE,
        provider_provenance: {
          provider: "anibt",
          provider_entry_ref: "anibt:stellar-mail-ella",
          provider_group_ref: "ranked-group",
          discovery_snapshot_version: "fixture-snapshot-1",
        },
        episode_offset: 0,
        version: 1,
      }],
      version: 1,
    },
    rules: {
      managed_anime_id: ANIME_ID,
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
      // AnimeRules 契约必填；缺失会让 RulesTab 读 .action 抛错、整个配置弹窗崩溃。
      effective_tag_filters: {
        anibt: { require: [], exclude: [] },
        title: { require: [], exclude: [] },
      },
      has_anibt_source: true,
      version: 1,
    },
    rss_sources: [{
      id: sourceID,
      subscription_id: subscriptionID,
      source_kind: "provider_group",
      locator_display: "https://example.test/rss",
      group_name: GROUP,
      title: TITLE,
      provider_provenance: {
        provider: "anibt",
        provider_entry_ref: "anibt:stellar-mail-ella",
        provider_group_ref: "ranked-group",
        discovery_snapshot_version: "fixture-snapshot-1",
      },
      episode_offset: 0,
      version: 1,
    }],
    rss_source_runtimes: [{ rss_source_id: sourceID, health: "ready", version: 1 }],
    releases: [{
      id: releaseID,
      rss_source_id: sourceID,
      stable_item_key: `release-key-${ANIME_ID}`,
      raw_facts: { title: `[${GROUP}] ${TITLE} - 01`, published_at: "2026-07-20T00:00:00Z" },
      source_facts: { identity_kind: "guid", identity_value: `release-key-${ANIME_ID}`, group_name: GROUP },
      parsed_facts: { episode: 1, group_name: GROUP, quality: "1080p", languages: ["简"], special: false },
      verdict: "accepted_pending_download",
      reason_code: "accepted",
      ...(retried ? { download_task_id: `task-${ANIME_ID}` } : {}),
      version: 1,
    }],
    download_tasks: retried
      ? [{
          id: `task-${ANIME_ID}`,
          release_id: releaseID,
          frozen_input: {
            release_stable_item_key: `release-key-${ANIME_ID}`,
            source_locator_display: "https://example.test/rss",
          },
          frozen_policy: {
            rules_version: 1,
            metadata_version: 1,
            effective_sections: {},
            section_versions: {},
            section_schemas: {},
            effective_digest: "sha256:" + "d".repeat(64),
            config_identity: { identity_kind: "instance_materialized", managed_anime_id: ANIME_ID, effective_digest: "sha256:" + "d".repeat(64) },
          },
          state: "queued",
          idempotency_key: "idem-owner-download-1",
          version: 1,
        }]
      : [],
    artifacts: [],
    file_operations: [],
    placement_handoffs: [],
    logs: [],
    episode_progress: null,
    config_waiting_files: 0,
  };
}

async function mockShell(page: Page) {
  await page.route("**/api/v1/events/stream**", (route) =>
    route.fulfill({ status: 200, contentType: "text/event-stream", body: ": fixture stream\n\n" }),
  );
  await page.route("**/api/v1/anime", async (route) => {
    if (route.request().method() !== "GET") return methodNotAllowed(route, "GET");
    const workspace = ownerWorkspace(false);
    return ok(route, 200, [{
      managed_anime: workspace.managed_anime,
      subscription: workspace.subscription,
      metadata: workspace.metadata,
      metadata_hold: workspace.metadata_hold,
      subscribed_groups: [GROUP],
      rss_health: "ready",
      episode_progress: null,
      broadcast_day: "none",
    } satisfies AnimeListItem]);
  });
  await page.route("**/api/v1/catalog/providers/*/seasons/*", async (route) => {
    if (route.request().method() !== "GET") return methodNotAllowed(route, "GET");
    const season = new URL(route.request().url()).pathname.split("/").at(-1) ?? "2026-summer";
    return ok(route, 200, {
      season,
      snapshot: {
        id: `anibt-${season}`,
        season,
        status: "fresh",
        provider: "anibt",
        observed_at: "2026-07-15T12:00:00Z",
        source_version: "fixture",
        contract_version: "v2",
        health_state: "ready",
        health_checked_at: "2026-07-15T12:00:00Z",
        version: 1,
        stale: false,
      },
      items: [{
        kind: "anibt_entry",
        provider: "anibt",
        title: TITLE,
        original_title: "星間配達人エラ",
        provider_entry_ref: "anibt:stellar-mail-ella",
        bgm_id: null,
        poster_url: null,
        cover_url: null,
        broadcast_day: "sat",
        catalog_state: "present",
        preference: "neutral",
        allowed_actions: ["open_workspace"],
        instances: [{
          managed_anime_ref: ANIME_ID,
          subscription_ref: `subscription-${ANIME_ID}`,
          subscription_state: "active",
          subscription_version: 1,
          lifecycle_state: "active",
          lifecycle_epoch: 1,
        }],
      }],
    } satisfies CatalogWorkspace);
  });
  await page.route("**/api/v1/config/global-sections", (route) => ok(route, 200, [] satisfies GlobalSectionView[]));
  await page.route("**/api/v1/template-catalogs/naming", (route) =>
    ok(route, 200, { language: "js-naming-1", fields: [], functions: [], shorthands: [], samples: [], vars_keys: [], vars_loaded_at: null } satisfies NamingTemplateCatalog),
  );
  await page.route("**/api/v1/config/naming-templates", (route) =>
    ok(route, 200, { default_key: "default", templates: [{ key: "default", name: "Default", rules: { language: "js-naming-1", tv: { directory_template: "{title}", video_filename_template: "{title}" }, movie: { directory_template: "{title}", video_filename_template: "{title}" }, unknown: { directory_template: "{title}", video_filename_template: "{title}" }, subtitle_suffix_template: "" } }], version: 1 } satisfies NamingTemplateLibraryView),
  );
  await page.route("**/api/v1/catalog/suppressions", (route) => ok(route, 200, []));
  await page.route("**/api/v1/automation", (route) => ok(route, 200, []));
  await page.route("**/api/v1/observability/diagnostics**", (route) =>
    ok(route, 200, { episodes: [], next_cursor: null } satisfies DiagnosticEpisodePage),
  );
  await page.route("**/api/v1/observability/records**", (route) =>
    ok(route, 200, { records: [], next_cursor: null } satisfies ObservabilityRecordPage),
  );
}

async function mockWorkspaceAndActions(page: Page) {
  let retried = false;
  let refreshIdempotencyKey: string | undefined;
  let downloadIdempotencyKey: string | undefined;
  await page.route("**/api/v1/anime/rss-list-view", async (route) => {
    if (route.request().method() !== "POST") return methodNotAllowed(route, "POST");
    const input = route.request().postDataJSON() as { input_source: string; managed_anime_id: string };
    if (input.input_source !== "owner" || input.managed_anime_id !== ANIME_ID) return ok(route, 422, { code: "INVALID_INPUT" });
    return ok(route, 200, ownerRSSView(ownerWorkspace(retried)));
  });

  await page.route("**/api/v1/anime/*/workspace", async (route) => {
    if (route.request().method() !== "GET") return methodNotAllowed(route, "GET");
    return ok(route, 200, ownerWorkspace(retried));
  });
  await page.route("**/api/v1/rss-sources/*/refresh", async (route) => {
    if (route.request().method() !== "POST") return methodNotAllowed(route, "POST");
    refreshIdempotencyKey = route.request().headers()["idempotency-key"];
    return ok(route, 202, { accepted: true, work_item_id: "work-owner-refresh-1" } satisfies RSSRefreshAccepted);
  });
  await page.route("**/api/v1/releases/*/retry", async (route) => {
    if (route.request().method() !== "POST") return methodNotAllowed(route, "POST");
    const releaseID = route.request().url().split("/releases/")[1]?.split("/")[0] ?? "release";
    downloadIdempotencyKey = route.request().headers()["idempotency-key"];
    retried = true;
    return ok(route, 200, {
      release_id: releaseID,
      verdict: "accepted_pending_download",
      outcome: "accepted",
      download_task_id: `task-${ANIME_ID}`,
    } satisfies ReleaseRetryResult);
  });

  return {
    refreshIdempotencyKey: () => refreshIdempotencyKey,
    downloadIdempotencyKey: () => downloadIdempotencyKey,
  };
}

test.describe("RSS owner 种子重试（fixture UI 走查）", () => {
  test.beforeEach(() => {
    test.skip(!fixtureOnly, "fixture UI walkthrough runs only with BANSOKO_E2E_FIXTURES=1");
  });

  test("刷新来源并一键重试种子", async ({ page }) => {
    await mockSignedIn(page);
    await mockShell(page);
    const actions = await mockWorkspaceAndActions(page);

    await page.goto("/");
    // 订阅自己的工作区从已订阅墙打开：AniBT 浏览卡只标「已订阅」，点开是候选草稿
    // （owner 2026-10-21）。
    await selectSubscribed(page);
    const card = page.getByRole("article").filter({ hasText: TITLE }).first();
    await expect(card).toBeVisible();
    await card.getByRole("button", { name: `打开配置 ${TITLE}`, exact: true }).click();

    const dialog = page.getByRole("dialog").last();
    await expect(dialog.getByRole("tab", { name: "RSS", exact: true })).toHaveAttribute("aria-selected", "true");
    await dialog.getByRole("button", { name: "刷新 RSS", exact: true }).click();
    await expect(page.getByText("RSS 刷新已提交（1 个 RSS 源）", { exact: true })).toBeVisible();
    expect(actions.refreshIdempotencyKey()).toBeTruthy();

    await dialog.getByRole("button", { name: `展开 ${GROUP}`, exact: true }).click();
    await expect(dialog.getByText(`[${GROUP}] ${TITLE} - 01`, { exact: true })).toBeVisible();
    // Retry has no confirmation: one click runs normal acceptance again.
    await dialog.getByRole("button", { name: "重新执行", exact: true }).click();
    await expect(page.getByRole("alertdialog")).toHaveCount(0);

    await expect(page.getByText("已开始重新下载", { exact: true })).toBeVisible();
    await expect(dialog.getByText("提交中", { exact: true })).toBeVisible();
    await expect(dialog.getByRole("button", { name: "重新执行", exact: true })).toHaveCount(0);
    expect(actions.downloadIdempotencyKey()).toBeTruthy();
  });
});
