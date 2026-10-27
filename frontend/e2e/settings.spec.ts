import { test, expect, type Page, type Route } from "@playwright/test";
import type { AutomationSchedule, CatalogPreference, DiagnosticEpisodePage, GlobalSectionView, NamingTemplateCatalog, NamingTemplateLibraryView, ObservabilityRecordPage } from "../src/lib/api/client";
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
  { group: "系统", labels: ["账户", "自动任务", "通知", "版本与升级", "数据备份", "API 令牌"] },
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
    getOnly(route, () => route.fulfill({ status: 200, contentType: "application/json", body: "[]" })),
  );
  await page.route("**/api/v1/config/network-routes", (route) =>
    getOnly(route, () => route.fulfill({ status: 200, contentType: "application/json", body: "[]" })),
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
  await page.route("**/api/v1/template-catalogs/naming", (route) =>
    getOnly(route, () =>
      route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ language: "js-naming-1", fields: [], functions: [], shorthands: [], samples: [], vars_keys: [], vars_loaded_at: null } satisfies NamingTemplateCatalog) }),
    ),
  );
  await page.route("**/api/v1/config/naming-templates", (route) =>
    getOnly(route, () =>
      route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ default_key: "default", templates: [{ key: "default", name: "Default", rules: { language: "js-naming-1", tv: { directory_template: "{title}", video_filename_template: "{title}" }, movie: { directory_template: "{title}", video_filename_template: "{title}" }, unknown: { directory_template: "{title}", video_filename_template: "{title}" }, subtitle_suffix_template: "" } }], version: 1 } satisfies NamingTemplateLibraryView) }),
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

  test("桌面端展示 section sidebar，十四个分区齐全", async ({ page }) => {
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

  test("下载与入库 / 订阅数量与对齐 / 默认规则 可达并渲染 mock 数据", async ({ page }) => {
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
});
