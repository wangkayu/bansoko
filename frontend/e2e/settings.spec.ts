import { test, expect, type Page, type Route } from "@playwright/test";
import type { AutomationSchedule, CatalogPreference, DiagnosticEpisodePage, GlobalSectionView, NamingTemplateCatalog, NamingTemplateLibraryView, ObservabilityRecordPage, ReplacementCompareVocabulary, ReplacementPolicyDefaultView } from "../src/lib/api/client";
import { shouldSkipAuthTests } from "./skip-auth-helper";
import { mockSignedIn } from "./session";

// 全局设置是盖在目录之上的全屏 Dialog，分区由 /settings/:category 承载：
// 可深链、可刷新、关闭回到目录。应用走 HashRouter，所以地址是 /#/settings/...，
// 服务端只交付 index.html，不需要也没有 SPA history fallback。
// 两个 project（desktop / 390px mobile）都跑这个文件，所以视口相关的断言各自 skip 掉另一侧。
//
// 两种运行形态：
// - live（默认）：真实后端 + 登录态（BANSOKO_E2E_USERNAME/PASSWORD），无凭据时整组 skip。
// - fixture（BANSOKO_E2E_FIXTURES=1）：无后端；shell 与设置分区的 GET 读全部在 HTTP
//   边界 mock，会话 token 由 addInitScript 注入以绕过 AuthGate 探测（与
//   anime-grid.spec.ts 的 mockShellReads 同一思路）。

const fixtureOnly = process.env.BANSOKO_E2E_FIXTURES === "1";

// 产品实际 15 个分区（SettingsDialog categories 的用户可见中文入口，
// 按 sidebar 分组顺序排列：服务 2 + 订阅策略 4 + 站点 3 + 系统 6）。
// 「同集多版本」与「未识别集数」是各自独立的分区。
// 「命名变量」当前版本隐藏（恢复时加回「默认规则」之后，见 SettingsDialog.tsx）。
const CATEGORY_GROUPS: { group: string; labels: string[] }[] = [
  { group: "服务", labels: ["服务连接", "下载与入库"] },
  { group: "订阅策略", labels: ["默认规则", "同集多版本", "未识别集数", "订阅数量与对齐"] },
  { group: "站点", labels: ["AniBT 配置", "Mikan 配置", "隐藏的番剧"] },
  { group: "系统", labels: ["账户", "自动任务", "通知", "版本与升级", "解析组件", "数据备份", "API 令牌"] },
];
const CATEGORIES = CATEGORY_GROUPS.flatMap((group) => group.labels);

function isMobile(page: Page): boolean {
  return (page.viewportSize()?.width ?? 0) <= 600;
}

// fixture 模式的 HTTP 边界 mock。覆盖 shell（TopNav/SystemStatusStrip/SSE）
// 与全部设置分区的 GET 读；写端点不 mock（本文件不断言写路径）。
//
// Batch3 终审 M2：只匹配 URL 的 mock 会把错误方法的请求静默放行成 200。
// 本文件全部是 GET-only 读端点，统一经 getOnly 收紧为 fail-closed：非 GET
// 一律返回明确 405 JSON，绝不伪造成功。各 pattern 互不重叠且 handler 总是
// fulfill，page.route 注册顺序不影响匹配结果。
function getOnly(route: Route, fulfill: () => Promise<void>): Promise<void> {
  if (route.request().method() !== "GET") {
    return route.fulfill({
      status: 405,
      contentType: "application/json",
      body: JSON.stringify({ error: "method_not_allowed", allowed: ["GET"] }),
    });
  }
  return fulfill();
}

async function mockSettingsReads(page: Page) {
  await page.route("**/api/v1/events/stream", (route) =>
    getOnly(route, () =>
      route.fulfill({ status: 200, contentType: "text/event-stream", body: ": fixture stream\n\n" }),
    ),
  );
  await page.route("**/api/v1/config/connections", (route) =>
    getOnly(route, () =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([
          { id: "builtin-github", kind: "github", name: "GitHub", base_url: "https://raw.githubusercontent.com", enabled: true, version: 1 },
        ]),
      }),
    ),
  );
  await page.route("**/api/v1/config/connections/*/test", (route) =>
    route.request().method() === "POST"
      ? route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ connection_id: "builtin-github", kind: "github", health: "ready", checked_at: "2026-11-07T00:00:00Z" }),
        })
      : route.fulfill({ status: 405, contentType: "application/json", body: JSON.stringify({ error: "method_not_allowed", allowed: ["POST"] }) }),
  );
  await page.route("**/api/v1/config/network-routes", (route) =>
    getOnly(route, () => route.fulfill({ status: 200, contentType: "application/json", body: "[]" })),
  );
  // 字幕组字典（本地读）：名单弹窗打开即浏览，字典/名单共用这一页。
  await page.route("**/api/v1/config/subgroups/groups**", (route) =>
    getOnly(route, () =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          candidates: [
            { provider: "mikan", group_ref: "901", display_name: "示例字幕组 A", source: "collected", available: true },
            { provider: "mikan", group_ref: "902", display_name: "示例字幕组 B", source: "user", available: true },
          ],
          next_offset: null,
          total: 2,
        }),
      }),
    ),
  );
  // 当前契约的八个全局 section 全量返回：下载与入库/规则/订阅策略读各自的 section，
  // AniBT/Mikan provider 页读 subgroups。
  await page.route("**/api/v1/config/global-sections", (route) =>
    getOnly(route, () =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([
          { section_name: "subgroups", schema_version: "v1", value: { max_rss: 3, entries: [], provider_defaults: {}, name_keywords: {} }, version: 1, saved: false },
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
      }),
    ),
  );
  // 标题解析行的候选值来自这份固定词表；默认值两行为空时也照常展示。
  await page.route("**/api/v1/config/tag-filters/title-vocabulary", (route) =>
    getOnly(route, () =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          implementation: "reltitle@fixture",
          config_digest: "lexicon:sha256:fixture",
          dimensions: [
            { dimension: "resolution", values: ["2160p", "1080p", "720p", "480p"] },
            { dimension: "source_type", values: ["WEB-DL", "WEBRip", "BDRip"] },
            { dimension: "sub_lang", values: ["SC", "TC", "JPSC", "ENG"] },
          ],
        }),
      }),
    ),
  );
  await page.route("**/api/v1/file-maintenance/replacement/default", (route) =>
    getOnly(route, () =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          admission_mode: "single_copy",
          quality_profile: {
            version: "profile-v1",
            items: [
              { key: "anibt.language", ranks: [["CHS", "CHT"], ["CHS"], ["JP"], ["EN"]] },
              { key: "rt.sub_lang", ranks: [["jpsc"], ["sc"], ["tc"], ["eng"]] },
              { key: "release.resolution", ranks: [["2160p"], ["1080p"]] },
            ],
          },
          version: 1,
        } satisfies ReplacementPolicyDefaultView),
      }),
    ),
  );
  await page.route("**/api/v1/file-maintenance/replacement/compare-vocabulary**", (route) =>
    getOnly(route, () =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: [
            { key: "anibt.language", values: ["CHS", "CHT", "JP", "EN"] },
            { key: "rt.sub_lang", values: ["jpsc", "jptc", "sc", "tc", "eng", "jpn"] },
          ],
        } satisfies ReplacementCompareVocabulary),
      }),
    ),
  );
  await page.route("**/api/v1/template-catalogs/naming", (route) =>
    getOnly(route, () =>
      route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ language: "js-naming-1", fields: [], functions: [], shorthands: [], samples: [], vars_keys: [], vars_loaded_at: null } satisfies NamingTemplateCatalog) }),
    ),
  );
  await page.route("**/api/v1/config/naming-templates", (route) =>
    getOnly(route, () =>
      route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ default_key: "default", templates: [{ key: "default", name: "Default", rules: { language: "js-naming-1", tv: { directory_template: "/library/${a.title}", video_filename_template: "示例动画 [${r.res}]" }, movie: { directory_template: "{title}", video_filename_template: "{title}" }, unknown: { directory_template: "{title}", video_filename_template: "{title}" }, subtitle_suffix_template: "" } }], version: 1 } satisfies NamingTemplateLibraryView) }),
    ),
  );
  await page.route("**/api/v1/catalog/suppressions", (route) =>
    getOnly(route, () => route.fulfill({ status: 200, contentType: "application/json", body: "[]" })),
  );
  // 目录候选（AnimeGrid 自定义预设列表挂载即拉取）：不 mock 会穿透 vite proxy
  // 打到真实后端吃 401，触发全局 clearToken 把会话踢回登录门。
  await page.route("**/api/v1/anime-config-candidates**", (route) =>
    getOnly(route, () =>
      route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ candidates: [] }) }),
    ),
  );
  // provider 页的目录自动刷新偏好（season 段是动态当季值，用通配）。
  await page.route("**/api/v1/catalog/providers/*/seasons/*/preference", (route) =>
    getOnly(route, () =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ season: "fixture-season", refresh_enabled: false, schedule: null, version: 1, updated_at: "2026-08-01T00:00:00Z" } satisfies CatalogPreference),
      }),
    ),
  );
  // 三个固定调度：SystemStatusStrip 与「自动任务」分区共用这一响应。
  await page.route("**/api/v1/automation", (route) =>
    getOnly(route, () =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify([
          { kind: "group_discovery", enabled: true, interval_value: 6, interval_unit: "hour", next_run_at: null, last_dispatch_at: null, last_run_outcome: null, version: 1 },
          { kind: "rss_refresh", enabled: true, interval_value: 30, interval_unit: "minute", next_run_at: null, last_dispatch_at: null, last_run_outcome: null, version: 1 },
          { kind: "metadata_match", enabled: false, interval_value: 12, interval_unit: "hour", next_run_at: null, last_dispatch_at: null, last_run_outcome: null, version: 1 },
        ] satisfies AutomationSchedule[]),
      }),
    ),
  );
  await page.route("**/api/v1/observability/diagnostics**", (route) =>
    getOnly(route, () =>
      route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ episodes: [], next_cursor: null } satisfies DiagnosticEpisodePage) }),
    ),
  );
  await page.route("**/api/v1/observability/records**", (route) =>
    getOnly(route, () =>
      route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ records: [], next_cursor: null } satisfies ObservabilityRecordPage) }),
    ),
  );
  await page.route("**/api/v1/plugin-targets", (route) =>
    getOnly(route, () => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ targets: [] }) })),
  );
  await page.route("**/api/v1/config/notifications/trigger-kinds", (route) =>
    getOnly(route, () => route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ trigger_kinds: ["download_submitted"] }),
    })),
  );
  await page.route("**/api/v1/template-catalogs/notification/*", (route) =>
    getOnly(route, () => route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        language_version: "bansoko-template-1",
        catalog_version: "notification-catalog-v1",
        trigger_kind: "download_submitted",
        variables: [
          { name: "anime.title", label: "标题", type: "string", nullable: false, source: "anime", sample_value: "示例动画", phase: "release", label_key: "facts.anime.title.label" },
          { name: "image", label: "节目图片", type: "string", nullable: false, source: "system", phase: "release", label_key: "templateFacts.semantic.image.label", description_key: "templateFacts.semantic.image.description" },
          { name: "link", label: "元数据链接", type: "string", nullable: true, source: "system", phase: "release", label_key: "templateFacts.semantic.link.label", description_key: "templateFacts.semantic.link.description" },
        ],
        functions: [],
        default_template: { mode: "custom", title_template: "默认标题", body_template: "{{image}}\n默认正文\n{{link}}" },
        sample: { values: {} },
      }),
    })),
  );
  await page.route("**/api/v1/template-previews/notification", (route) => {
    if (route.request().method() !== "POST") {
      return route.fulfill({ status: 405, contentType: "application/json", body: JSON.stringify({ error: "method_not_allowed", allowed: ["POST"] }) });
    }
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        language_version: "bansoko-template-1",
        catalog_version: "notification-catalog-v1",
        title: "预览标题",
        body: "默认正文",
        valid: true,
        errors: [],
        warnings: [],
        variables: [],
      }),
    });
  });
  // 默认内嵌升级页：更新状态接口未接入时返回 404，页面展示默认空态。
  await page.route("**/api/v1/update/status", (route) =>
    getOnly(route, () =>
      route.fulfill({
        status: 404,
        contentType: "application/json",
        body: JSON.stringify({ error: "not_found" }),
      }),
    ),
  );
  // 应用更新行的连通性测试复用检查更新：返回已是最新版本。
  await page.route("**/api/v1/update/check", (route) =>
    getOnly(route, () =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          status: "up_to_date",
          current: { version: "0.1.0", release_id: "rel-1", image_compatibility: "v1" },
          compatibility: { kind: "overlay", min_image_version: "v1", reason: "" },
          checked_at: "2026-11-07T00:00:00Z",
        }),
      }),
    ),
  );
  // 解析组件面板：内置解析器、未检查通道的默认读模型。
  await page.route("**/api/v1/title-parser", (route) =>
    getOnly(route, () =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          source: "builtin",
          version: "reltitle@v0.0.3",
          line: "0.0",
          config_digest: "lexicon:sha256:e2e",
          binary_path: "/config/rt/reltitle",
          note: "",
          external_failure: "",
          lane: "stable",
          message: "",
        }),
      }),
    ),
  );
  await page.route("**/api/v1/api-tokens", (route) =>
    getOnly(route, () => route.fulfill({ status: 200, contentType: "application/json", body: "[]" })),
  );
}

async function openSettings(page: Page, hashPath = "/#/settings") {
  if (fixtureOnly) {
    await mockSignedIn(page);
    await mockSettingsReads(page);
  }
  await page.goto(hashPath);
}

test.describe("settings dialog", () => {
  test.skip(!fixtureOnly && shouldSkipAuthTests(), "BANSOKO_E2E_USERNAME and BANSOKO_E2E_PASSWORD are not set");

  test.beforeEach(async ({ page }) => {
    await openSettings(page);
    await expect(page).toHaveURL(/\/settings\/connections$/, { timeout: 10000 });
  });

  test("/#/settings 重定向到第一个分区并以 dialog 打开", async ({ page }) => {
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible({ timeout: 10000 });
    // 弹窗标题由 ui/base Dialog 渲染；断言标题文本本身。
    await expect(dialog.getByText("设置", { exact: true })).toBeVisible();
    await expect(page).toHaveURL(/\/settings\/connections$/);
  });

  test("目录留在下层，不被设置替换", async ({ page }) => {
    await expect(page.getByRole("dialog")).toBeVisible({ timeout: 10000 });
    // AnimeGrid 仍然挂载在 dialog 之下（DOM 中存在，只是被遮罩盖住）。
    await expect(page.locator("main")).toBeAttached();
  });

  test("桌面端展示 section sidebar，十五个分区齐全", async ({ page }) => {
    test.skip(isMobile(page), "手机是两级导航，另有用例覆盖");
    const nav = page.getByRole("navigation", { name: "设置分类" });
    await expect(nav).toBeVisible({ timeout: 10000 });
    for (const label of CATEGORIES) {
      await expect(nav.getByRole("link", { name: label })).toBeVisible();
    }
    // 分组：每组标题下按顺序列出它的分区。
    for (const { group, labels } of CATEGORY_GROUPS) {
      await expect(nav.getByRole("group", { name: group }).getByRole("link")).toHaveText(labels);
    }
  });

  test("手机：先看到分区列表，进入分区后内容页带返回", async ({ page }) => {
    test.skip(!isMobile(page), "桌面端保留 sidebar，另有用例覆盖");
    // 从顶栏入口（/#/settings）打开：先是分区列表，内容页还不可见。
    const nav = page.getByRole("navigation", { name: "设置分类" });
    const content = page.getByTestId("settings-content");
    await expect(nav).toBeVisible({ timeout: 10000 });
    await expect(content).toBeHidden();

    await nav.getByRole("link", { name: "自动任务" }).click();
    await expect(page).toHaveURL(/\/settings\/automation$/);
    await expect(content).toBeVisible();
    await expect(nav).toBeHidden();

    // 返回分区列表。
    await page.getByRole("button", { name: "全部设置" }).click();
    await expect(nav).toBeVisible();
    await expect(content).toBeHidden();
  });

  test("手机：深链直接进入分区内容", async ({ page }) => {
    test.skip(!isMobile(page), "桌面端另有深链用例覆盖");
    await page.goto("/#/settings/notifications");
    await expect(page.getByTestId("settings-content")).toBeVisible({ timeout: 10000 });
    await expect(page.getByRole("navigation", { name: "设置分类" })).toBeHidden();
  });

  test("桌面端网格几何：导航列 11rem，内容区不被挤压", async ({ page }) => {
    test.skip(isMobile(page), "手机是两级导航（列表 → 内容），几何断言不适用");
    // 回归守卫：导航列与内容列塌陷时（内容区掉进 11rem 窄列、正则逐字竖排），
    // 可见性断言拦不住，必须断言几何。
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible({ timeout: 10000 });
    const nav = dialog.getByRole("navigation", { name: "设置分类" });
    await expect(nav).toBeVisible();

    // 桌面网格模板是 11rem(=176px) + minmax(0,1fr)，允许滚动条/边框的 ±8px 容差。
    await expect
      .poll(async () => (await nav.boundingBox())?.width ?? 0, { timeout: 5000 })
      .toBeGreaterThanOrEqual(168);
    await expect
      .poll(async () => (await nav.boundingBox())?.width ?? 0, { timeout: 5000 })
      .toBeLessThanOrEqual(184);

    // 内容列宽度必须远大于导航列：塌陷时它会被挤到 ~176px。
    const content = dialog.getByTestId("settings-content");
    await expect
      .poll(async () => (await content.boundingBox())?.width ?? 0, { timeout: 5000 })
      .toBeGreaterThan(480);

    // dialog 不得把页面撑出横向滚动。
    const { scrollWidth, clientWidth } = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
    }));
    expect(scrollWidth).toBeLessThanOrEqual(clientWidth + 1);
  });

  test("分区可深链，刷新后停在同一分区", async ({ page }) => {
    await page.goto("/#/settings/notifications");
    await expect(page).toHaveURL(/\/settings\/notifications$/, { timeout: 10000 });
    await expect(page.getByRole("dialog")).toBeVisible({ timeout: 10000 });
    await page.reload();
    await expect(page.getByRole("dialog")).toBeVisible({ timeout: 10000 });
    await expect(page).toHaveURL(/\/settings\/notifications$/);
  });

  // 「命名变量」当前版本隐藏：导航没有入口，深链回落到服务连接。
  test("命名变量分区隐藏，深链回落到服务连接", async ({ page }) => {
    await expect(page.getByRole("navigation", { name: "设置分类" }).getByRole("link", { name: "命名变量" })).toHaveCount(0);
    await page.goto("/#/settings/vars");
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible({ timeout: 10000 });
    await expect(dialog.getByRole("heading", { name: "服务连接" }).last()).toBeVisible({ timeout: 10000 });
    await expect(dialog.getByRole("heading", { name: "命名变量" })).toHaveCount(0);
  });

  // 应用更新是普通 GitHub 连接：地址（可填反向代理）与网络代理都在连接行里
  // 编辑，连通性测试走连接测试，与其他连接行同形。
  test("GitHub 更新连接是普通连接行，地址与代理在编辑弹窗里", async ({ page }) => {
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible({ timeout: 10000 });
    if (isMobile(page)) {
      // 手机是两级导航：先停在分类列表，点进「服务连接」才渲染内容。
      await dialog.getByRole("navigation", { name: "设置分类" }).getByRole("link", { name: "服务连接" }).click();
    }
    const section = dialog.getByRole("heading", { name: "服务连接" }).locator("xpath=..");
    const row = section.getByRole("listitem").filter({ hasText: "GitHub" });
    await expect(row).toBeVisible();
    await expect(row.getByText("https://raw.githubusercontent.com")).toBeVisible();
    await expect(row.getByText("内置", { exact: true })).toBeVisible();
    // 不再有单独的「GitHub 更新」行，也不会重复出现两条更新行。
    await expect(section.getByRole("listitem").filter({ hasText: "GitHub 更新" })).toHaveCount(0);
    await expect(section.getByRole("listitem")).toHaveCount(1);

    // 新行不得把设置内容撑出横向滚动，行右缘必须落在内容区内。
    const rowBox = await row.boundingBox();
    const contentBox = await dialog.getByTestId("settings-content").boundingBox();
    expect(rowBox).not.toBeNull();
    expect(contentBox).not.toBeNull();
    expect(rowBox!.x).toBeGreaterThanOrEqual(contentBox!.x - 1);
    expect(rowBox!.x + rowBox!.width).toBeLessThanOrEqual(contentBox!.x + contentBox!.width + 1);
    const pageOverflow = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
    }));
    expect(pageOverflow.scrollWidth).toBeLessThanOrEqual(pageOverflow.clientWidth + 1);

    await row.getByRole("button", { name: "测试" }).click();
    await expect(row.getByText("正常", { exact: true })).toBeVisible();

    await row.getByRole("button", { name: "编辑" }).click();
    const editor = page.getByRole("dialog", { name: "编辑连接" });
    await expect(editor).toBeVisible();
    await expect(editor.getByLabel("更新地址")).toHaveValue("https://raw.githubusercontent.com");
    await expect(editor.getByRole("combobox", { name: "网络代理（可选）" })).toContainText("直连（不使用代理）");
  });

  test("关闭设置回到目录", async ({ page }) => {
    await expect(page.getByRole("dialog")).toBeVisible({ timeout: 10000 });
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toBeHidden();
    await expect(page).toHaveURL(/#\/$/);
  });
});

// P2.3 functional closure: the automation section shows exactly the three
// fixed schedules with run buttons and summaries, and the notification
// section loads its target surface.
test.describe("settings functional closure", () => {
  test.skip(!fixtureOnly && shouldSkipAuthTests(), "BANSOKO_E2E_USERNAME and BANSOKO_E2E_PASSWORD are not set");

  test("automation section shows exactly three fixed schedules", async ({ page }) => {
    await openSettings(page, "/#/settings/automation");
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible({ timeout: 10000 });
    // 三个固定自动任务：自动发现 / 订阅刷新 / 元数据匹配。
    await expect(dialog.getByText("字幕组发现", { exact: false }).first()).toBeVisible({ timeout: 10000 });
    await expect(dialog.getByText("RSS 刷新", { exact: false }).first()).toBeVisible();
    await expect(dialog.getByText("元数据自动匹配", { exact: false }).first()).toBeVisible();
    // 不含被删除的 replacement_scan 语义（第四类任务）。
    await expect(dialog.getByText("replacement", { exact: false })).toHaveCount(0);
  });

  test("notification section renders and targets dialog opens", async ({ page }) => {
    await openSettings(page, "/#/settings/notifications");
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible({ timeout: 10000 });
    // 目标列表区域（空态或列表）与“添加预设”入口。
    await expect(dialog.getByRole("button", { name: "新建渠道" })).toBeVisible({ timeout: 10000 });
  });
});

// fixture-only 内容覆盖：provider 配置页与版本/升级默认空态 / AI Lab 在 mock 读下
// 可达。live 模式不断言这些分区内容（真实实例的更新服务状态不定）。
test.describe("settings fixture coverage", () => {
  test.skip(!fixtureOnly, "内容断言依赖 fixture HTTP mock");

  test("通知模板预览说明图片与链接的验证方式且不溢出", async ({ page }, testInfo) => {
    await openSettings(page, "/#/settings/notifications");
    const settings = page.getByRole("dialog");
    await settings.getByRole("button", { name: "新建渠道" }).click();
    const editor = page.getByRole("dialog", { name: "新建通知渠道" });
    await expect(editor).toBeVisible();
    await editor.getByRole("combobox", { name: "下载已提交的模板模式" }).click();
    await page.getByRole("option", { name: "自定义模板" }).click();
    await expect(editor.getByLabel("正文模板")).toHaveValue("{{image}}\n默认正文\n{{link}}");
    await editor.getByRole("button", { name: "预览", exact: true }).click();

    const hint = editor.getByText("此处仅预览文本；图片与链接请保存后通过测试发送检查。");
    await expect(hint).toBeVisible();
    await expect(editor.getByText("正文：默认正文", { exact: true })).toBeVisible();
    await hint.scrollIntoViewIfNeeded();
    await expect(hint).toBeInViewport();
    const hintBox = await hint.boundingBox();
    const editorBox = await editor.boundingBox();
    expect(hintBox).not.toBeNull();
    expect(editorBox).not.toBeNull();
    expect(hintBox!.x).toBeGreaterThanOrEqual(editorBox!.x - 1);
    expect(hintBox!.x + hintBox!.width).toBeLessThanOrEqual(editorBox!.x + editorBox!.width + 1);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true);
    const screenshot = testInfo.outputPath("notification-template-preview.png");
    await page.screenshot({ path: screenshot, fullPage: true });
    await testInfo.attach("notification-template-preview", { path: screenshot, contentType: "image/png" });
  });

  test("provider 配置页可达：AniBT 与 Mikan", async ({ page }) => {
    test.skip(isMobile(page), "sidebar 链接交互是桌面形态，手机另有两级导航用例");
    await openSettings(page, "/#/settings/anibt");
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible({ timeout: 10000 });
    // sr-only 分区标题与页面 h2 同名，取第一个；「字幕组名单」卡证明 subgroups
    // section 已加载进 provider 草稿。
    await expect(dialog.getByRole("heading", { name: "AniBT 配置" }).first()).toBeVisible();
    await expect(dialog.getByText("字幕组名单", { exact: true })).toBeVisible();

    await page.getByRole("navigation", { name: "设置分类" }).getByRole("link", { name: "Mikan 配置" }).click();
    await expect(page).toHaveURL(/\/settings\/mikan$/);
    await expect(dialog.getByRole("heading", { name: "Mikan 配置" }).first()).toBeVisible();
    await expect(dialog.getByText("字幕组名单", { exact: true })).toBeVisible();
  });

  // 名单直接显示在卡片里，编辑表单（模式/默认分/字典点选/移除）在二级弹窗里
  // （桌面/手机共用）：弹窗在视口内、页面不横向滚动，添加规则确认后落成卡片里的一行。
  test("名单在卡片里展示、在弹窗里编辑，窄视口不横向溢出", async ({ page }) => {
    await openSettings(page, "/#/settings/mikan");
    const settingsDialog = page.getByRole("dialog");
    await expect(settingsDialog.getByRole("heading", { name: "Mikan 配置" }).first()).toBeVisible({ timeout: 10000 });

    await settingsDialog.getByRole("button", { name: "管理 Mikan 字幕组名单" }).click();
    const roster = page.getByRole("dialog", { name: "Mikan 字幕组名单" });
    await expect(roster).toBeVisible();
    await expect(roster.getByRole("radiogroup", { name: "名单模式" })).toBeVisible();
    await expect(roster.getByRole("textbox", { name: "Mikan 字典搜索" })).toBeVisible();
    // 打开即浏览字典，不用先输入关键词；点一行即加入名单。
    const sampleRow = roster.getByRole("button", { name: "把字幕组 示例字幕组 A 加入名单" });
    await expect(sampleRow).toBeVisible();
    await sampleRow.click();
    await expect(sampleRow).toBeDisabled();
    await expect(sampleRow.getByText("已在名单")).toBeVisible();
    const box = await roster.boundingBox();
    const viewport = page.viewportSize()!;
    expect(box).not.toBeNull();
    expect(box!.x).toBeGreaterThanOrEqual(0);
    expect(box!.width).toBeLessThanOrEqual(viewport.width);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await roster.getByRole("button", { name: "完成" }).click();
    await expect(roster).toBeHidden();
    // 完成后面包的名单直接显示在卡片里，不需要再打开弹窗。
    const rosterCard = settingsDialog.getByRole("group", { name: "字幕组名单" });
    await expect(rosterCard.getByRole("list", { name: "名单" })).toContainText("示例字幕组 A");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

    await settingsDialog.getByRole("button", { name: "添加 Mikan 名称评分规则" }).click();
    const ruleDialog = page.getByRole("dialog", { name: "添加名称评分规则" });
    await expect(ruleDialog).toBeVisible();
    await ruleDialog.getByLabel("关键词").fill("简繁");
    await ruleDialog.getByLabel("评分（-100 到 100）").fill("-30");
    await ruleDialog.getByRole("button", { name: "添加" }).click();
    await expect(ruleDialog).toBeHidden();
    await expect(settingsDialog.getByLabel("Mikan 规则 1 关键词")).toHaveValue("简繁");
    await expect(settingsDialog.getByLabel("Mikan 规则 1 评分")).toHaveValue("-30");
  });

  test("下载与入库 / 订阅数量与对齐 / 默认规则 可达并渲染 mock 数据", async ({ page }, testInfo) => {
    test.skip(isMobile(page), "sidebar 链接交互是桌面形态，手机另有两级导航用例");
    await openSettings(page, "/#/settings/download");
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible({ timeout: 10000 });
    // 整理方式卡来自 mock 的 file_effect section，证明分区读已加载。
    await expect(dialog.getByRole("heading", { name: "整理方式" })).toBeVisible();

    const nav = page.getByRole("navigation", { name: "设置分类" });
    await nav.getByRole("link", { name: "订阅数量与对齐" }).click();
    await expect(page).toHaveURL(/\/settings\/policy$/);
    // max_rss 卡证明 subgroups section 已进入策略草稿。
    await expect(dialog.getByRole("heading", { name: "最多订阅字幕组数" })).toBeVisible();

    await nav.getByRole("link", { name: "默认规则" }).click();
    await expect(page).toHaveURL(/\/settings\/rules$/);
    // sr-only 分区标题与页面 h2 同名，取第一个；解析规则卡来自 mock parser section。
    await expect(dialog.getByRole("heading", { name: "默认规则" }).first()).toBeVisible();
    await expect(dialog.getByRole("heading", { name: "解析规则" })).toBeVisible();

    // 标签过滤卡的分区编辑弹窗里，标题解析行展示词表的固定候选，与是否采集到标签无关。
    const tagCard = dialog
      .locator("li.settings-shared-card")
      .filter({ has: page.getByRole("heading", { name: "标签过滤", exact: true }) });
    await expect(tagCard).toBeVisible();
    await tagCard.getByRole("button", { name: "编辑" }).click();
    const editDialog = page.getByRole("dialog", { name: "编辑 · 标签过滤" });
    await expect(editDialog).toBeVisible();
    const titleRow = editDialog.locator('section[aria-label="标题解析"]');
    await expect(titleRow).toBeVisible();
    for (const candidate of ["2160p", "720p", "WEBRip", "JPSC"]) {
      await expect(titleRow.getByRole("button", { name: candidate })).toBeVisible();
    }
    // 候选增多不改变弹窗宽度：行不越出弹窗，也没有横向溢出。
    const [dialogBox, rowBox] = await Promise.all([editDialog.boundingBox(), titleRow.boundingBox()]);
    if (!dialogBox || !rowBox) throw new Error("tag filter row has no box");
    expect(rowBox.x).toBeGreaterThanOrEqual(dialogBox.x);
    expect(rowBox.x + rowBox.width).toBeLessThanOrEqual(dialogBox.x + dialogBox.width + 1);
    expect(await titleRow.evaluate((element) => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1);
    await titleRow.scrollIntoViewIfNeeded();
    await editDialog.screenshot({ path: testInfo.outputPath("rules-tag-filter-candidates.png") });
  });

  test("语言大卡拖动预览保持原卡片尺寸", async ({ page }, testInfo) => {
    test.skip(isMobile(page), "原生 HTML5 卡片拖动在桌面项目验证");
    await openSettings(page, "/#/settings/replacement");
    const list = page.getByRole("list", { name: "比较顺序", exact: true });
    const preview = page.locator("[data-quality-compare-drag-preview]");

    for (const [label, screenshot] of [
      ["AniBT 标签", "anibt-language-drag-preview.png"],
      ["reltitle", "rt-sub-language-drag-preview.png"],
    ] as const) {
      const card = list.locator(":scope > li").filter({ hasText: label });
      await expect(card).toBeVisible();
      await card.scrollIntoViewIfNeeded();
      const box = await card.boundingBox();
      if (!box) throw new Error(`${label} card has no box`);
      await page.mouse.move(box.x + 14, box.y + 20);
      await page.mouse.down();
      await page.mouse.move(box.x + 80, box.y + 48, { steps: 12 });

      await expect(preview).toBeAttached();
      const previewBox = await preview.boundingBox();
      if (!previewBox) throw new Error(`${label} drag preview has no box`);
      expect(Math.abs(previewBox.width - box.width)).toBeLessThanOrEqual(1);
      expect(Math.abs(previewBox.height - box.height)).toBeLessThanOrEqual(1);
      const overflow = await preview.evaluate((element) => ({
        horizontal: element.scrollWidth - element.clientWidth,
        vertical: element.scrollHeight - element.clientHeight,
      }));
      expect(overflow.horizontal).toBeLessThanOrEqual(1);
      expect(overflow.vertical).toBeLessThanOrEqual(1);

      // 原生 ghost 不进入 page.screenshot；把同一份 setDragImage 节点临时移入视口，
      // 直接截取浏览器实际用于拖动预览的 DOM，并不断言页面截图里存在原生 ghost。
      await preview.evaluate((element) => {
        const style = (element as HTMLElement).style;
        style.left = "16px";
        style.top = "16px";
        style.zIndex = "10000";
      });
      await preview.screenshot({ path: testInfo.outputPath(screenshot) });
      await page.mouse.up();
      await expect(preview).toHaveCount(0);
    }

    // 修复只覆盖两张大语言卡，普通比较项仍使用浏览器默认拖动预览。
    const ordinary = list.locator(":scope > li").filter({ hasText: "内置规则" });
    const ordinaryBox = await ordinary.boundingBox();
    if (!ordinaryBox) throw new Error("ordinary comparison card has no box");
    await page.mouse.move(ordinaryBox.x + 14, ordinaryBox.y + 20);
    await page.mouse.down();
    await page.mouse.move(ordinaryBox.x + 80, ordinaryBox.y + 48, { steps: 12 });
    await expect(preview).toHaveCount(0);
    await page.mouse.up();
  });

  test("版本与升级默认空态与 AI Lab 可达", async ({ page }) => {
    test.skip(isMobile(page), "sidebar 链接交互是桌面形态，手机另有两级导航用例");
    await openSettings(page, "/#/settings/updates");
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible({ timeout: 10000 });
    // 内嵌更新状态未提供时，版本与升级页展示默认空态。
    await expect(dialog.getByText("检查是否有新版本", { exact: true })).toBeVisible();

    await page.getByRole("navigation", { name: "设置分类" }).getByRole("link", { name: "API 令牌" }).click();
    await expect(page).toHaveURL(/\/settings\/ai$/);
    await expect(dialog.getByRole("heading", { name: "创建令牌" })).toBeVisible();
    await expect(dialog.getByText("还没有 API 令牌", { exact: true })).toBeVisible();
  });

  test("解析组件分区展示当前解析器与未检查空态", async ({ page }) => {
    await openSettings(page, "/#/settings/titleParser");
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible({ timeout: 10000 });
    await expect(dialog.getByText("标题解析 reltitle", { exact: true })).toBeVisible();
    await expect(dialog.getByText("内置", { exact: true })).toBeVisible();
    await expect(dialog.getByText("0.0.3")).toBeVisible();
    await expect(dialog.getByText("尚未检查过更新", { exact: true })).toBeVisible();
    await expect(dialog.getByRole("button", { name: "检查更新" })).toBeVisible();
  });

  test("命名目录使用短根，分辨率预览区分真实值与极简占位", async ({ page }, testInfo) => {
    const pageErrors: string[] = [];
    page.on("pageerror", (error) => pageErrors.push(error.message));
    await openSettings(page, "/#/settings/rules");

    await page.route("**/api/v1/template-catalogs/naming**", (route) =>
      getOnly(route, () =>
        route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            language: "js-naming-1",
            fields: [
              { path: "a.title", label: "标题", type: "string", nullable: false, source: "anime", sample_value: "示例动画", phase: "release" },
              { path: "r.res", label: "分辨率", type: "string", nullable: true, source: "release", sample_value: "1080p", phase: "release" },
              { path: "anibt.res", label: "分辨率", type: "string", nullable: true, source: "anibt", phase: "release" },
              { path: "rt.res", label: "分辨率", type: "string", nullable: true, source: "rt", phase: "release" },
              { path: "media.res", label: "分辨率", type: "string", nullable: true, source: "media", phase: "execution" },
            ],
            functions: [],
            shorthands: [{ name: "e00", expansion: "pad(ep.number, 2)", example: "01" }],
            samples: [{ ref: "sample-tv", label: "剧集样例" }],
            vars_keys: [],
            vars_loaded_at: null,
          } satisfies NamingTemplateCatalog),
        }),
      ),
    );
    await page.route("**/api/v1/template-previews/naming", (route) => {
      if (route.request().method() !== "POST") {
        return route.fulfill({ status: 405, contentType: "application/json", body: JSON.stringify({ error: "method_not_allowed", allowed: ["POST"] }) });
      }
      const { template_source: source } = route.request().postDataJSON() as { template_source: string };
      const rendered = {
        "/library/${a.title}": "/library/示例动画",
        "示例动画 [${r.res}]": "示例动画 [1080p].mkv",
        "示例动画 [${anibt.res}-${rt.res}-${media.res}].mkv": "示例动画 [⟨res⟩-⟨res⟩-⟨res⟩].mkv",
      }[source] ?? source;
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ valid: true, rendered, warnings: [], errors: [] }),
      });
    });

    const settingsDialog = page.getByRole("dialog");
    const namingCard = settingsDialog.locator("li.settings-shared-card").filter({ has: page.getByRole("heading", { name: "命名规则", exact: true }) });
    await expect(namingCard).toBeVisible({ timeout: 10000 });
    await namingCard.getByRole("button", { name: "编辑" }).click();
    const editor = page.getByRole("dialog", { name: "编辑 · 命名规则" });
    await expect(editor).toBeVisible();

    await editor.getByRole("button", { name: /可用变量 5 个/ }).click();
    for (const path of ["a.title", "r.res", "anibt.res", "rt.res", "media.res"]) {
      await expect(editor.locator(".naming-palette-item").filter({ hasText: path })).toHaveCount(1);
    }
    for (const oldPath of ["anime.title", "release.res"]) {
      await expect(editor.locator(".naming-palette-item").filter({ hasText: oldPath })).toHaveCount(0);
    }
    await expect(editor.getByRole("button", { name: "${e00} → 01" })).toBeVisible();

    const directoryPreview = editor.locator(".naming-preview").filter({ hasText: "目录预览" });
    const filePreview = editor.locator(".naming-preview").filter({ hasText: "文件名预览" });
    await expect(directoryPreview).toContainText("/library/示例动画");
    await expect(filePreview.locator(".naming-preview-value")).toHaveText("示例动画 [1080p].mkv");
    await expect(filePreview.locator("mark")).toHaveCount(0);

    await editor.getByLabel("文件名模板").fill("示例动画 [${anibt.res}-${rt.res}-${media.res}].mkv");
    await expect(filePreview.locator("mark")).toHaveText(["〈res〉", "〈res〉", "〈res〉"]);
    await expect(filePreview.locator(".naming-preview-value")).toHaveText("示例动画 [〈res〉-〈res〉-〈res〉].mkv");
    await expect(filePreview).not.toContainText(/anibt\.res|rt\.res|media\.res/);
    await expect(filePreview.locator("mark").first()).toHaveAttribute("title", "下载后确定的值：res");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect(pageErrors).toEqual([]);
    await editor.locator(".naming-palette-body").screenshot({ path: testInfo.outputPath("naming-short-root-catalog.png") });
    await filePreview.scrollIntoViewIfNeeded();
    await filePreview.screenshot({ path: testInfo.outputPath("naming-minimal-placeholders.png") });
  });
});
