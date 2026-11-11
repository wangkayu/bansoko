import { test, expect, type Page, type Route } from "@playwright/test";
import type {
  AnimeConfigCandidate,
  AnimeDraft,
  CatalogWorkspace,
  DiagnosticEpisodePage,
  GlobalSectionView,
  NamingTemplateCatalog,
  NamingTemplateLibraryView,
  ObservabilityRecordPage,
  OneShotAdmissionResult,
  ProviderGroupDiscoveryResult,
  ProviderRSSPreviewResult,
  RSSListView,
} from "../src/lib/api/client";
import { preset as businessPreset, loadDraftFromBusinessPreset } from "../src/testing/businessPreset";
import { mockSignedIn } from "./session";

// Fixture-only browser evidence for the one-shot download dialog: a persisted
// seed whose only strong identity is a canonical SHA-256 payload digest is
// downloadable from the catalog, the browser submits snapshot/config identities
// without echoing a locator or digest, and a server answer that adopted an
// existing exact target set (no DownloadTask) is one submission, not two.
// All API calls are mocked at the network boundary; no backend is required.
const fixtureOnly = process.env.BANSOKO_E2E_FIXTURES === "1";

const TITLE = "星际邮差艾拉";
const ENTRY_REF = "anibt:stellar-mail-ella";
const GROUP = "北宇治字幕组";
const GROUP_REF = "ranked-group";
const SEED_DIGEST = "sha256:" + "e".repeat(64);
const SNAPSHOT_HASH = "sha256:" + "a".repeat(64);

// 契约束缚的候选记录：字段集与 AnimeConfigCandidate 一致。
type CandidateRecord = AnimeConfigCandidate;

function ok(route: Route, status: number, body: unknown) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
}

async function mockShellReads(page: Page) {
  await page.route("**/api/v1/config/business-preset/load", async (route) => {
    if (route.request().method() !== "POST") return ok(route, 405, { code: "METHOD_NOT_ALLOWED" });
    const body = route.request().postDataJSON() as { draft: AnimeDraft; reset_to_global?: boolean };
    return ok(route, 200, loadDraftFromBusinessPreset(body.draft, businessPreset, body.reset_to_global));
  });
  await page.route("**/api/v1/config/business-preset", async (route) => {
    if (route.request().method() !== "GET") return ok(route, 405, { code: "METHOD_NOT_ALLOWED" });
    return ok(route, 200, businessPreset);
  });
  await page.route("**/api/v1/events/stream**", (route) =>
    route.fulfill({ status: 200, contentType: "text/event-stream", body: ": fixture stream\n\n" }),
  );
  // 已订阅列表：目录投影先读订阅，再读外部目录。
  await page.route("**/api/v1/anime", async (route) => {
    if (route.request().method() !== "GET") {
      return ok(route, 405, { code: "METHOD_NOT_ALLOWED", message: "只支持 GET" });
    }
    await ok(route, 200, []);
  });
  await page.route("**/api/v1/catalog/providers/*/seasons/*", async (route) => {
    const url = new URL(route.request().url());
    const season = url.pathname.split("/").at(-1) ?? "2026-summer";
    await ok(route, 200, {
      season,
      snapshot: {
        id: `anibt-${season}`,
        season,
        status: "fresh",
        provider: "anibt",
        observed_at: "2026-09-01T12:00:00Z",
        source_version: "fixture",
        contract_version: "v2",
        health_state: "ready",
        health_checked_at: "2026-09-01T12:00:00Z",
        version: 1,
        stale: false,
      },
      items: [
        {
          kind: "anibt_entry",
          provider: "anibt",
          title: TITLE,
          original_title: "星間配達人エラ",
          provider_entry_ref: ENTRY_REF,
          bgm_id: null,
          poster_url: null,
          cover_url: null,
          broadcast_day: "sat",
          catalog_state: "present",
          preference: "neutral",
          allowed_actions: ["prepare_subscribe"],
          instances: [],
        },
      ],
    } satisfies CatalogWorkspace);
  });
  await page.route("**/api/v1/anime/rss-list-view", async (route) => {
    if (route.request().method() !== "POST") {
      return ok(route, 405, { code: "METHOD_NOT_ALLOWED", message: "只支持 POST" });
    }
    await ok(route, 200, unifiedView());
  });
  await page.route("**/api/v1/config/global-sections", (route) =>
    ok(route, 200, [
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
  );
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

async function mockCandidates(page: Page) {
  const candidates: CandidateRecord[] = [];
  const stamp = "2026-09-01T00:00:00Z";
  let seq = 0;
  await page.route("**/api/v1/anime-config-candidates**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const rest = url.pathname.replace("/api/v1/anime-config-candidates", "");
    const method = request.method();
    if (rest === "" && method === "GET") {
      return ok(route, 200, { candidates });
    }
    if (rest === "" && method === "POST") {
      const draft = (request.postDataJSON() as { draft: AnimeDraft }).draft;
      seq += 1;
      const created: CandidateRecord = {
        id: `candidate-${seq}`,
        origin: draft.origin,
        origin_entry_ref: draft.origin_entry_ref ?? null,
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
        retention_until: "2027-09-01T00:00:00Z",
        cleanup_eligible_at: null,
        cleanup_reason: null,
      };
      candidates.push(created);
      return ok(route, 200, created);
    }
    return ok(route, 404, { code: "NOT_FOUND", message: "候选不存在" });
  });
}

// The provider-group discovery populates the draft's subgroup entries, which is
// what makes the seed preview list the group at all.
async function mockDiscovery(page: Page) {
  await page.route("**/api/v1/catalog/providers/*/entries/*/groups:discover", async (route) => {
    if (route.request().method() !== "POST") {
      return ok(route, 405, { code: "METHOD_NOT_ALLOWED", message: "只支持 POST" });
    }
    await ok(route, 200, ({
      provider: "anibt",
      provider_entry_ref: ENTRY_REF,
      observed_at: "2026-09-01T12:00:00Z",
      source_version: "fixture",
      schema_version: "v1",
      snapshot_version: 1,
      groups: [
        {
          provider: "anibt",
          group_ref: GROUP_REF,
          display_name: GROUP,
          feed_url: "https://example.test/feed.xml",
          observed_at: "2026-09-01T12:00:00Z",
        },
      ],
    } satisfies ProviderGroupDiscoveryResult));
  });
}

// A real provider preview row whose only strong identity is the canonical
// payload digest: `info_hash` is deliberately absent.
async function mockSeedRead(page: Page) {
  const snapshot = {
    provider: "anibt",
    provider_entry_ref: ENTRY_REF,
    group_ref: GROUP_REF,
    observed_at: "2026-09-01T12:00:00Z",
    snapshot_id: "snap-adopt-1",
    snapshot_version: 3,
    content_hash: SNAPSHOT_HASH,
    freshness: "fresh",
    seeds: [
      {
        stable_seed_key: "seed-digest-only",
        title: `[${GROUP}] ${TITLE} - 01 [1080p]`,
        published_at: "2026-09-01T12:00:00Z",
        download_url: "https://torrent.example.test/digest-only.torrent",
        size_bytes: 734003200,
        expected_payload_digest: SEED_DIGEST,
      },
    ],
  } satisfies ProviderRSSPreviewResult;
  await page.route("**/api/v1/catalog/providers/*/entries/*/rss*", async (route) => {
    const method = route.request().method();
    if (method !== "GET" && method !== "POST") {
      return ok(route, 405, { code: "METHOD_NOT_ALLOWED", message: "只支持 GET/POST" });
    }
    await ok(route, 200, snapshot);
  });
}

async function mockSnapshotDownload(page: Page) {
  let submitted: unknown;
  let requestPath: string | undefined;
  let idempotencyKey: string | undefined;
  let count = 0;
  await page.route("**/api/v1/provider-feed-snapshots/*/seeds/*/download", async (route) => {
    if (route.request().method() !== "POST") {
      return ok(route, 405, { code: "METHOD_NOT_ALLOWED", message: "只支持 POST" });
    }
    count += 1;
    submitted = route.request().postDataJSON();
    idempotencyKey = route.request().headers()["idempotency-key"];
    requestPath = new URL(route.request().url()).pathname;
    await ok(route, 201, ({
      one_shot_id: "os-adopted-1",
      managed_anime_id: "anime-adopted-1",
      subscription_id: "sub-adopted-1",
      rss_source_id: "src-adopted-1",
      release_id: "rel-adopted-1",
      download_task_id: null,
      state: "reclaim_pending",
    } satisfies OneShotAdmissionResult));
  });
  return {
    submittedRequest: () => submitted as Record<string, unknown> | undefined,
    submittedIdempotencyKey: () => idempotencyKey,
    submittedPath: () => requestPath,
    submissions: () => count,
  };
}

function unifiedView(): RSSListView {
  const title = `[${GROUP}] ${TITLE} - 01 [1080p]`;
  return {
    mode: "current_input",
    config_identity: "draft:fixture",
    metadata_hold: { held: false, missing: [] },
    versions: {
      parser: "parser-v1",
      evaluator: "evaluator-v1",
      renderer: "renderer-v1",
      safety: "path-safety-v1",
      torrent_parser: "torrent-parser-v1",
    },
    generated_at: "2026-09-01T12:00:00Z",
    unused_feed_identities: [],
    truncated: false,
    limit_per_group: 100,
    groups: [
      {
        scope: {
          provider: "anibt",
          provider_entry_ref: ENTRY_REF,
          provider_group_ref: GROUP_REF,
        },
        display_name: GROUP,
        score: 50,
        selection: {
          source: "configured",
          effective_selected: true,
          selection_mode: "manual",
        },
        offset: {
          value: 0,
          editable: false,
          source: "draft_source",
        },
        feed: {
          state: "fresh",
          snapshot_id: "snap-adopt-1",
          snapshot_version: 3,
          content_hash: SNAPSHOT_HASH,
        },
        items: [
          {
            snapshot_id: "snap-adopt-1",
            stable_seed_key: "seed-digest-only",
            seed: {
              title,
              published_at: "2026-09-01T12:00:00Z",
              size_bytes: 734003200,
              expected_payload_digest: SEED_DIGEST,
              locator_display: "torrent://digest-only",
            },
            torrent: {
              state: "available",
              manifest: {
                payload_digest: SEED_DIGEST,
                parser_version: "torrent-parser-v1",
                torrent_version: "v1",
                torrent_name: `${TITLE} - 01`,
                total_size_bytes: 734003200,
                file_count: 1,
                projection_digest: "sha256:" + "3".repeat(64),
              },
            },
            evaluation: {
              state: "evaluated",
              verdict: "accepted_pending_download",
              reason_code: "accepted",
              parsed_facts: { episode: 1, group_name: GROUP, quality: "1080p", languages: ["简"], special: false },
            },
            files: [
              {
                file_ref: "sha256:" + "4".repeat(64),
                ordinal: 0,
                source_path: `${TITLE} - 01.mkv`,
                filename: `${TITLE} - 01.mkv`,
                size_bytes: 734003200,
                media_kind: "video",
                included: true,
                rendered_target: `${TITLE}/Season 01/${TITLE} - E01 [1080p].mkv`,
                safety: "valid",
              },
            ],
            download_action: { allowed: true, kind: "one_shot" },
            // 写集是那个 included 的视频；fixture 的库为空，所以是 absent 0/1。
            library_overlap: { verdict: "absent", present_files: 0, pending_files: 1 },
          },
        ],
      },
    ],
  };
}

function numberingView(): RSSListView {
  const view = unifiedView();
  const item = view.groups[0].items[0];
  const first = `${TITLE} - 01.mkv`;
  const second = `${TITLE} - 02.mkv`;
  const subtitle = `${TITLE} - 01.zh-Hans.ass`;
  if (item.torrent?.manifest) item.torrent.manifest.file_count = 3;
  item.manifest_files = [
    { source_path: first, size_bytes: 734003200, media_kind: "video", selectable: true, default_selected: true, recognition: "episode", episodes: [1] },
    { source_path: second, size_bytes: 734003200, media_kind: "video", selectable: true, default_selected: true, recognition: "episode", episodes: [2] },
    { source_path: subtitle, size_bytes: 2000, media_kind: "subtitle", selectable: true, default_selected: true, bound_video_source_path: first },
  ];
  item.files = [first, second, subtitle].map((path, ordinal) => ({
    file_ref: `sha256:${String(ordinal + 4).repeat(64)}`,
    ordinal,
    source_path: path,
    filename: path,
    size_bytes: path.endsWith(".ass") ? 2000 : 734003200,
    media_kind: path.endsWith(".ass") ? "subtitle" as const : "video" as const,
    included: true,
    rendered_target: `${TITLE}/Season 01/${path}`,
    safety: "valid" as const,
    ...(path.endsWith(".ass") ? { bound_video_source_path: first } : {}),
  }));
  return view;
}

test.describe("统一 RSS 工作区持久化种子下载（fixture UI 走查）", () => {
  test.beforeEach(() => {
    test.skip(!fixtureOnly, "fixture UI walkthrough runs only with BANSOKO_E2E_FIXTURES=1");
  });

  test("digest-only 种子勾选后进入第二步并提交精确快照身份", async ({ page }) => {
    await mockSignedIn(page);
    await mockShellReads(page);
    await mockCandidates(page);
    await mockDiscovery(page);
    await mockSeedRead(page);
    const download = await mockSnapshotDownload(page);

    await page.goto("/");
    // 桌面顶栏是 button 页签；手机浏览分栏是页内 Segmented radio（先点底部「发现」）。
    if ((page.viewportSize()?.width ?? 1280) <= 600) {
      await page.getByRole("navigation", { name: "底部导航" }).getByRole("button", { name: "发现", exact: true }).click();
      const segmented = page.getByRole("radiogroup", { name: "主导航" });
      await expect(segmented.getByRole("radio", { name: "AniBT", exact: true })).toBeVisible();
      await segmented.getByRole("radio", { name: "AniBT", exact: true }).click();
    } else {
      await expect(page.getByRole("button", { name: "AniBT", exact: true })).toBeVisible();
      await page.getByRole("button", { name: "AniBT", exact: true }).click();
    }
    const card = page.getByRole("listitem").filter({ hasText: TITLE }).first();
    await expect(card).toBeVisible();
    // 「浏览种子」是一次性任务的入口：直接装载全局配置，不经过订阅草稿。
    await card.getByRole("button", { name: `浏览《${TITLE}》的种子` }).click();

    const task = page.getByRole("dialog", { name: "浏览种子" });
    await expect(task).toBeVisible();
    // 第一步是纯目录：只有一个字幕组时直接进入它；行内没有下载动作，选择只靠勾选加
    // 「下一步」，不投递。
    await expect(task.getByText("第 1 步，共 2 步 · 选择种子")).toBeVisible();
    const title = `[${GROUP}] ${TITLE} - 01 [1080p]`;
    await expect(task.getByRole("region", { name: GROUP }).getByText(title, { exact: true })).toBeVisible();
    await expect(task.getByRole("button", { name: `下载 ${title}` })).toHaveCount(0);
    await task.getByRole("checkbox", { name: title }).click();
    await task.getByRole("button", { name: "下一步（1）" }).click();
    expect(download.submittedRequest()).toBeUndefined();

    // 第二步：预览优先（宽栏），配置在旁；预览来自读模型对这颗种子的渲染。投递只在这里提交。
    await expect(task.getByText("第 2 步，共 2 步 · 设置与预览")).toBeVisible();
    await expect(task.getByRole("region", { name: "预览" }).getByText(title, { exact: true })).toBeVisible();
    await expect(task.getByRole("button", { name: /^高级/ })).toHaveAttribute("aria-expanded", "false");
    await expect(task.getByText("1 个视频")).toBeVisible();
    await task.getByRole("button", { name: "开始下载（1）" }).click();
    await expect(page.getByText("已提交 1 个一次性下载", { exact: true })).toBeVisible();
    await expect.poll(() => download.submittedRequest()).toBeTruthy();
    expect(download.submittedRequest()).toMatchObject({
      provider: "anibt",
      provider_entry_ref: ENTRY_REF,
      provider_group_ref: GROUP_REF,
      snapshot_version: 3,
      content_hash: SNAPSHOT_HASH,
      input_source: "current_input",
      draft: expect.any(Object),
    });
    expect(download.submittedPath()).toBe(
      "/api/v1/provider-feed-snapshots/snap-adopt-1/seeds/seed-digest-only/download",
    );
    expect(download.submittedRequest()).not.toHaveProperty("selected_seed");
    expect(JSON.stringify(download.submittedRequest())).not.toContain(SEED_DIGEST);
    expect(JSON.stringify(download.submittedRequest())).not.toContain("torrent.example.test");
    expect(download.submittedIdempotencyKey()).toBeTruthy();
    // 服务端采纳了已有的精确目标集（download_task_id 为空）：结果就地显示，提交后不能
    // 回到第一步，也没有可再次提交的种子，所以只投递了一次。
    await expect(task.getByText("已开始下载")).toBeVisible();
    await expect(task.getByRole("button", { name: "上一步" })).toHaveCount(0);
    await expect(task.getByRole("button", { name: /^开始下载/ })).toHaveCount(0);
    await expect(task.getByRole("button", { name: "完成" })).toBeVisible();
    expect(download.submissions()).toBe(1);
  });

  test("按文件编号在桌面和手机保持可用，预览与提交使用同一覆盖", async ({ page }) => {
    await mockSignedIn(page);
    await mockShellReads(page);
    await mockCandidates(page);
    await mockDiscovery(page);
    await mockSeedRead(page);
    const download = await mockSnapshotDownload(page);
    const previews: { file_selections?: { overrides?: { path: string; season?: number; episode: number }[] }[] }[] = [];
    await page.route("**/api/v1/anime/rss-list-view", async (route) => {
      const body = route.request().postDataJSON();
      previews.push(body);
      const view = numberingView();
      const overrides = body.file_selections?.[0]?.overrides ?? [];
      for (const file of view.groups[0].items[0].files) {
        const override = overrides.find((candidate: { path: string }) => candidate.path === file.source_path);
        if (override) file.rendered_target = `${TITLE}/Specials/${TITLE} - S00E${String(override.episode).padStart(2, "0")}`;
      }
      await ok(route, 200, view);
    });

    await page.goto("/");
    if ((page.viewportSize()?.width ?? 1280) <= 600) {
      await page.getByRole("navigation", { name: "底部导航" }).getByRole("button", { name: "发现", exact: true }).click();
      await page.getByRole("radiogroup", { name: "主导航" }).getByRole("radio", { name: "AniBT", exact: true }).click();
    } else {
      await page.getByRole("button", { name: "AniBT", exact: true }).click();
    }
    const card = page.getByRole("listitem").filter({ hasText: TITLE }).first();
    await card.getByRole("button", { name: `浏览《${TITLE}》的种子` }).click();
    const task = page.getByRole("dialog", { name: "浏览种子" });
    const title = `[${GROUP}] ${TITLE} - 01 [1080p]`;
    await task.getByRole("checkbox", { name: title }).click();
    await task.getByRole("button", { name: "下一步（1）" }).click();

    await task.getByRole("button", { name: "设置文件编号", exact: true }).click();
    const editor = page.getByRole("dialog", { name: "设置文件编号" });
    await expect(editor).toBeVisible();
    await expect(editor.locator("[data-numbering-row]")).toHaveCount(2);
    const geometry = await editor.evaluate((element) => {
      const rect = element.getBoundingClientRect();
      const rows = [...element.querySelectorAll("[data-numbering-row]")].map((row) => {
        const box = row.getBoundingClientRect();
        return { left: box.left, right: box.right, scrollWidth: (row as HTMLElement).scrollWidth, clientWidth: (row as HTMLElement).clientWidth };
      });
      return { left: rect.left, right: rect.right, viewport: window.innerWidth, pageWidth: document.documentElement.scrollWidth, rows };
    });
    expect(geometry.left).toBeGreaterThanOrEqual(0);
    expect(geometry.right).toBeLessThanOrEqual(geometry.viewport);
    expect(geometry.pageWidth).toBeLessThanOrEqual(geometry.viewport);
    for (const row of geometry.rows) {
      expect(row.left).toBeGreaterThanOrEqual(geometry.left);
      expect(row.right).toBeLessThanOrEqual(geometry.right);
      expect(row.scrollWidth).toBeLessThanOrEqual(row.clientWidth + 1);
    }
    await editor.screenshot({ path: test.info().outputPath(`file-numbering-${test.info().project.name}.png`), animations: "disabled" });

    await editor.getByRole("button", { name: "全选筛选结果" }).click();
    await editor.getByRole("spinbutton", { name: "统一季号" }).fill("0");
    await editor.getByRole("spinbutton", { name: "起始集号" }).fill("7");
    await editor.getByRole("button", { name: "勾选项顺序填入" }).click();
    await editor.getByRole("textbox", { name: "按路径筛选文件" }).fill("02");
    await expect(editor.locator("[data-numbering-row]")).toHaveCount(1);
    await expect(editor.getByRole("spinbutton", { name: `集数 ${TITLE} - 02.mkv` })).toHaveValue("8");
    await editor.getByRole("button", { name: "取消筛选结果" }).click();
    await editor.getByRole("textbox", { name: "按路径筛选文件" }).clear();
    await expect(editor.getByRole("spinbutton", { name: `集数 ${TITLE} - 01.mkv` })).toHaveValue("7");
    await editor.getByRole("button", { name: "应用勾选修改（1）" }).click();

    const expected = [{ path: `${TITLE} - 01.mkv`, season: 0, episode: 7 }];
    await expect.poll(() => previews.at(-1)?.file_selections?.[0]?.overrides).toEqual(expected);
    await expect(task.getByRole("region", { name: "预览" }).getByText(`${TITLE}/Specials/${TITLE} - S00E07`, { exact: true })).toBeVisible();
    await task.getByRole("button", { name: "开始下载（1）" }).click();
    await expect.poll(() => download.submittedRequest()).toBeTruthy();
    expect(download.submittedRequest()).toMatchObject({ overrides: expected });
    expect(download.submittedRequest()).not.toHaveProperty("file_selection");
  });
});
