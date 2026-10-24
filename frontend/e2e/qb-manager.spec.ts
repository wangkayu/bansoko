import { test, expect, type Page, type Route } from "@playwright/test";
import { mockSignedIn } from "./session";

// 下载器管理弹窗的 fixture 浏览器 smoke（fixture-only）：route mock 一个
// qBittorrent connection 与 qB WebUI 代理端点，验证 TopNav 入口出现、弹窗打开、
// 任务列表/服务器状态渲染、文件列表展开，以及至少一个 pause/resume 透传请求与
// 删除确认。不依赖真实 qB、外网或真实登录。
//
// 运维透传视图契约：这里只 mock qB 代理读端点与 pause/resume/delete 的 204 应答，
// 不伪造 DownloadTask 所有者真相，也不触碰 execution_ready 的部署执行流。
// 所有 fixture route 均 fail-closed 校验 HTTP method：非契约方法一律 405 应答，
// 绝不 fallback/continue 穿透到未启动的后端。

const fixtureOnly = process.env.BANSOKO_E2E_FIXTURES === "1";

const QB_CONNECTION_ID = "conn-qb-e2e";
const TORRENT_A_HASH = "abc123e2e";

const qbConnection = {
  id: QB_CONNECTION_ID,
  kind: "qbittorrent",
  name: "本地 qB",
  enabled: true,
  base_url: "http://qb.local:8080",
  username: "admin",
  credential_configured: true,
  version: 1,
};

const torrents = [
  {
    hash: TORRENT_A_HASH,
    name: "番剧 A - 01",
    state: "downloading",
    progress: 0.5,
    size: 1024,
    downloaded: 512,
    dlspeed: 100,
    eta: 5,
    category: "anime",
    origin: "subscription",
    added_on: 5,
  },
  {
    hash: "def456e2e",
    name: "番剧 B - 02",
    state: "pausedDL",
    progress: 0,
    size: 2048,
    origin: "subscription",
    added_on: 4,
  },
  // 其余分段：一次性下载、来源不明（下载记录已清理）、在下载器里手动添加的任务。
  { hash: "one111e2e", name: "番剧 C 特别篇", state: "uploading", progress: 1, size: 4096, upspeed: 2048, origin: "one_shot", added_on: 3 },
  { hash: "unk222e2e", name: "番剧 D - 01（旧）", state: "pausedUP", progress: 1, size: 4096, origin: "unknown", added_on: 2 },
  { hash: "ext333e2e", name: "ubuntu-24.04-desktop-amd64.iso", state: "stalledUP", progress: 1, size: 6_000_000_000, origin: "external", added_on: 1 },
];

const torrentFiles = [{ name: "file.mkv", size: 1024, progress: 1.0, priority: 7 }];

const serverStatus = {
  version: "4.6.5",
  connection_status: "connected",
  dl_speed: 500,
  up_speed: 100,
  free_space: 1073741824,
};

type QBRequestRecorder = {
  pause: string[];
  resume: string[];
  remove: string[];
};

function json(body: unknown) {
  return { status: 200, contentType: "application/json", body: JSON.stringify(body) };
}

// fixture route 的 fail-closed method 门禁：只放行契约方法；其余方法直接
// 405（带 Allow 头与 JSON 错误体），不走 fallback/continue——fixture lane
// 没有后端，穿透只会打到未启动的服务。handler（含 recorder 记录）只在门禁
// 放行后执行，因此 recorder 永远只记录正确方法的请求。
function withMethods(allowed: string[], handler: (route: Route) => Promise<void>) {
  return async (route: Route) => {
    const method = route.request().method();
    if (!allowed.includes(method)) {
      await route.fulfill({
        status: 405,
        headers: { Allow: allowed.join(", ") },
        contentType: "application/json",
        body: JSON.stringify({ error: "fixture_method_not_allowed", method, allowed }),
      });
      return;
    }
    await handler(route);
  };
}

async function mockQBManagerShell(page: Page, recorder: QBRequestRecorder) {
  // --- shell reads（TopNav / SystemStatusStrip / SSE）---
  await page.route(
    "**/api/v1/events/stream",
    withMethods(["GET"], (route) =>
      route.fulfill({ status: 200, contentType: "text/event-stream", body: ": fixture stream\n\n" }),
    ),
  );
  // 含一个 qBittorrent connection：TopNav 据此渲染「下载器管理」入口，
  // 弹窗据此解析唯一连接（单连接不显示切换 Select）。
  await page.route(
    "**/api/v1/config/connections",
    withMethods(["GET"], (route) => route.fulfill(json([qbConnection]))),
  );
  await page.route("**/api/v1/automation", withMethods(["GET"], (route) => route.fulfill(json([]))));
  await page.route(
    "**/api/v1/observability/diagnostics**",
    withMethods(["GET"], (route) => route.fulfill(json({ episodes: [], next_cursor: null }))),
  );
  await page.route(
    "**/api/v1/observability/records**",
    withMethods(["GET"], (route) => route.fulfill(json({ records: [], next_cursor: null }))),
  );
  // 「/」路由下 AnimeGrid 挂载在 TopNav 之下：「已订阅」分栏（候选列表）与
  // 隐藏列表 hook 挂载即拉取。不 mock 会穿透 vite proxy 打到未启动的后端
  // （fixture lane 无后端），与 settings.spec.ts 对 candidates 的处理同一理由。
  await page.route(
    "**/api/v1/anime-config-candidates**",
    withMethods(["GET"], (route) => route.fulfill(json({ candidates: [] }))),
  );
  await page.route(
    "**/api/v1/catalog/suppressions",
    withMethods(["GET"], (route) => route.fulfill(json([]))),
  );

  // --- qBittorrent 代理端点（运维透传视图）---
  await page.route(
    "**/api/v1/config/connections/*/qbittorrent/torrents",
    withMethods(["GET"], (route) => route.fulfill(json(torrents))),
  );
  await page.route(
    "**/api/v1/config/connections/*/qbittorrent/status",
    withMethods(["GET"], (route) => route.fulfill(json(serverStatus))),
  );
  await page.route(
    "**/api/v1/config/connections/*/qbittorrent/torrents/*/files",
    withMethods(["GET"], (route) => route.fulfill(json(torrentFiles))),
  );
  await page.route(
    "**/api/v1/config/connections/*/qbittorrent/torrents/*/pause",
    withMethods(["POST"], async (route) => {
      recorder.pause.push(route.request().url());
      await route.fulfill({ status: 204, body: "" });
    }),
  );
  await page.route(
    "**/api/v1/config/connections/*/qbittorrent/torrents/*/resume",
    withMethods(["POST"], async (route) => {
      recorder.resume.push(route.request().url());
      await route.fulfill({ status: 204, body: "" });
    }),
  );
  // DELETE .../torrents/{hash}（含 ?delete_files=true 变体）。Playwright glob
  // 的 `*` 不跨 `/`，本路由与更深的 /files、/pause、/resume 路径互斥，不会
  // 吞掉它们；再叠加 DELETE-only 门禁双保险：即使路径意外重叠，非 DELETE
  // 也只会得到 405，绝不记入 remove recorder。
  await page.route(
    "**/api/v1/config/connections/*/qbittorrent/torrents/*",
    withMethods(["DELETE"], async (route) => {
      recorder.remove.push(route.request().url());
      await route.fulfill({ status: 204, body: "" });
    }),
  );
}

async function enterAppWithQB(page: Page, recorder: QBRequestRecorder) {
  await mockSignedIn(page);
  await mockQBManagerShell(page, recorder);
  await page.goto("/");
}

// PHONE_MEDIA_QUERY 是 (width <= 600px)，与 src/lib/useMediaQuery.ts 同步。
const evidenceDir = process.env.BANSOKO_E2E_EVIDENCE_DIR;

async function capture(page: Page, name: string) {
  if (!evidenceDir || test.info().project.name !== "desktop") return;
  for (const width of [1280, 400]) {
    await page.setViewportSize({ width, height: width === 400 ? 860 : 900 });
    await page.waitForTimeout(150);
    await page.screenshot({ path: `${evidenceDir}/${name}-${width}.png` });
  }
}

function isPhoneLayout(page: Page): boolean {
  return (page.viewportSize()?.width ?? 1280) <= 600;
}

// qB 管理入口：桌面是顶栏「下载器管理」图标按钮；手机是底部标签栏的「下载」项。
function qbEntry(page: Page) {
  return isPhoneLayout(page)
    ? page.getByRole("navigation", { name: "底部导航" }).getByRole("button", { name: "下载", exact: true })
    : page.getByRole("button", { name: "下载器管理" });
}
test.describe("qb-manager dialog", () => {
  test.skip(!fixtureOnly, "fixture UI contract runs only with BANSOKO_E2E_FIXTURES=1");

  test("TopNav 入口打开管理弹窗并透传 pause/resume/delete", async ({ page }) => {
    const recorder: QBRequestRecorder = { pause: [], resume: [], remove: [] };
    await enterAppWithQB(page, recorder);

    // 连接列表返回 qBittorrent 后，TopNav 才出现管理入口。
    const qbButton = qbEntry(page);
    await expect(qbButton).toBeVisible({ timeout: 10000 });
    await qbButton.click();

    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible({ timeout: 10000 });
    await expect(dialog.getByText("下载器管理", { exact: true })).toBeVisible();

    // 服务器状态条：版本 + 连接状态 Tag。
    await expect(dialog.getByText("v4.6.5", { exact: true })).toBeVisible();
    await expect(dialog.getByText("已连接", { exact: true })).toBeVisible();

    // 任务列表渲染两行（下载中 / 已暂停）。
    const list = dialog.getByRole("list", { name: "下载任务列表" });
    await expect(list).toBeVisible();
    await expect(list.getByText("番剧 A - 01", { exact: true })).toBeVisible();
    await expect(list.getByText("番剧 B - 02", { exact: true })).toBeVisible();

    // 展开第一行文件列表（懒加载 GET .../files）。
    await list.getByRole("button", { name: "展开文件列表" }).first().click();
    await expect(dialog.getByRole("list", { name: "文件列表" }).getByText("file.mkv", { exact: true })).toBeVisible();

    // 透传操作：第一行暂停、第二行恢复；busy 期间按钮禁用由 actionability 自动等待。
    // 请求记录在 route 层，与点击异步收敛，用可重试断言。
    await list.getByRole("button", { name: "暂停", exact: true }).click();
    await list.getByRole("button", { name: "恢复", exact: true }).click();
    await expect.poll(() => recorder.pause.length, { timeout: 5000 }).toBe(1);
    await expect.poll(() => recorder.resume.length, { timeout: 5000 }).toBe(1);
    expect(recorder.pause[0]).toContain(`/config/connections/${QB_CONNECTION_ID}/qbittorrent/torrents/${TORRENT_A_HASH}/pause`);

    // 删除确认是第二个稳定断言：确认框出现 → 确认后发出 DELETE（默认不带 delete_files）。
    await list.getByRole("button", { name: "删除任务 番剧 A - 01" }).click();
    const confirmDialog = page.getByRole("dialog").filter({ hasText: "删除任务「番剧 A - 01」？" });
    await expect(confirmDialog).toBeVisible();
    await confirmDialog.getByRole("button", { name: "删除", exact: true }).click();
    await expect.poll(() => recorder.remove.length, { timeout: 5000 }).toBe(1);
    expect(recorder.remove[0]).toContain(`/config/connections/${QB_CONNECTION_ID}/qbittorrent/torrents/${TORRENT_A_HASH}`);
    expect(recorder.remove[0]).not.toContain("delete_files=true");
  });

  test("订阅 / 一次性 / 其他分段：按来源显示与计数，来源不明的任务在「其他」里标注，深链带分段", async ({ page }) => {
    const recorder: QBRequestRecorder = { pause: [], resume: [], remove: [] };
    await enterAppWithQB(page, recorder);
    await page.goto("/#/?tool=qbittorrent");
    const dialog = page.getByRole("dialog", { name: "下载器管理" });
    const segments = dialog.getByRole("radiogroup", { name: "任务来源" });
    await expect(segments.getByRole("radio", { name: "订阅 2" })).toBeChecked({ timeout: 10000 });
    await expect(segments.getByRole("radio", { name: "一次性 1" })).toBeVisible();
    await expect(segments.getByRole("radio", { name: "其他 2" })).toBeVisible();
    const list = dialog.getByRole("list", { name: "下载任务列表" });
    await expect(list.getByRole("listitem")).toHaveCount(2);
    await capture(page, "qb-segment-subscription");

    await segments.getByRole("radio", { name: "一次性 1" }).click();
    await expect(page).toHaveURL(/qb_segment=one_shot/);
    await expect(list.getByText("番剧 C 特别篇", { exact: true })).toBeVisible();
    await capture(page, "qb-segment-one_shot");

    await segments.getByRole("radio", { name: "其他 2" }).click();
    await expect(list.getByRole("listitem")).toHaveCount(2);
    await expect(list.getByText("来源不明", { exact: true })).toHaveCount(1);
    const noHorizontalScroll = await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth);
    expect(noHorizontalScroll).toBe(true);
    await capture(page, "qb-segment-other");

    // 深链直接打开某个分段。
    await page.goto("/#/?tool=qbittorrent&qb_segment=one_shot");
    await expect(segments.getByRole("radio", { name: "一次性 1" })).toBeChecked({ timeout: 10000 });
  });

  test("空分段显示说明", async ({ page }) => {
    const recorder: QBRequestRecorder = { pause: [], resume: [], remove: [] };
    await enterAppWithQB(page, recorder);
    await page.route(
      "**/api/v1/config/connections/*/qbittorrent/torrents",
      withMethods(["GET"], (route) => route.fulfill(json(torrents.filter((item) => item.origin !== "one_shot")))),
    );
    await page.goto("/#/?tool=qbittorrent&qb_segment=one_shot");
    const dialog = page.getByRole("dialog", { name: "下载器管理" });
    await expect(dialog.getByText("没有一次性下载的任务", { exact: true })).toBeVisible({ timeout: 10000 });
    await expect(dialog.getByText("从「浏览种子」下载的种子会显示在这里", { exact: true })).toBeVisible();
    await capture(page, "qb-segment-empty");
  });

  test("qB 管理由路由承载：深链 ?tool=qbittorrent 打开，刷新恢复，返回关闭", async ({ page }) => {
    const recorder: QBRequestRecorder = { pause: [], resume: [], remove: [] };
    await enterAppWithQB(page, recorder);
    const qbButton = qbEntry(page);
    await expect(qbButton).toBeVisible({ timeout: 10000 });
    await qbButton.click();
    const dialog = page.getByRole("dialog", { name: "下载器管理" });
    await expect(dialog).toBeVisible({ timeout: 10000 });
    await expect(page).toHaveURL(/#\/\?(.*&)?tool=qbittorrent/);

    await page.reload();
    await expect(dialog).toBeVisible({ timeout: 10000 });
    await page.goBack();
    await expect(dialog).toHaveCount(0);
    await expect(page).not.toHaveURL(/tool=/);

    await page.goto("/#/?source=anibt&tool=qbittorrent");
    await expect(dialog).toBeVisible({ timeout: 10000 });
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
    await expect(page).toHaveURL(/#\/\?source=anibt$/);
  });

  test("未配置下载器时入口保留，打开显示空状态和“去设置”", async ({ page }) => {
    const recorder: QBRequestRecorder = { pause: [], resume: [], remove: [] };
    await mockSignedIn(page);
    await mockQBManagerShell(page, recorder);
    // 覆盖连接列表为空（后注册者优先），其余 mock 复用。
    await page.route(
      "**/api/v1/config/connections",
      withMethods(["GET"], (route) => route.fulfill(json([]))),
    );
    await page.goto("/");

    if (isPhoneLayout(page)) {
      await expect(page.getByRole("navigation", { name: "底部导航" })).toBeVisible({ timeout: 10000 });
    } else {
      await expect(page.getByRole("button", { name: "AniBT", exact: true })).toBeVisible({ timeout: 10000 });
      // UI 评审 Q4：状态点如实写「未配置下载器」。
      await expect(page.getByRole("button", { name: "服务状态" })).toHaveText(/未配置下载器/);
    }
    await qbEntry(page).click();
    const dialog = page.getByRole("dialog", { name: "下载器管理" });
    await expect(dialog).toBeVisible({ timeout: 10000 });
    await expect(page).toHaveURL(/#\/\?(.*&)?tool=qbittorrent/);
    await expect(dialog.getByText("还没有下载器", { exact: true })).toBeVisible();
    await dialog.getByRole("button", { name: "去设置", exact: true }).click();
    await expect(page).toHaveURL(/#\/settings\/download/);
    await expect(page.getByRole("dialog", { name: "设置" })).toBeVisible();
  });
});
