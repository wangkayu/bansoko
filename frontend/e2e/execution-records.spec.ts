import { test, expect, type Locator, type Page, type Route } from "@playwright/test";
import { mockSignedIn } from "./session";

// 执行记录弹窗的 fixture 浏览器 smoke（fixture-only）：route mock 记录列表、详情与文件页，
// 验证同一番剧的记录按集数 / 种子标题区分、下载中显示进度与速度、入库成功的文件显示入库位置。
// 所有 fixture route 均 fail-closed 校验 HTTP method，不穿透到未启动的后端。
// 设置 BANSOKO_E2E_EVIDENCE_DIR 时，桌面项目额外在 1280 与 400 宽度下截图列表与详情。

const fixtureOnly = process.env.BANSOKO_E2E_FIXTURES === "1";
const evidenceDir = process.env.BANSOKO_E2E_EVIDENCE_DIR;

const anime = { managed_anime_id: "anime-1", title: "葬送的芙莉莲 第二季" };
const base = {
  entry_kind: "automatic",
  record_detail: "full",
  source_title: { state: "visible", value: anime.title },
  anime,
} as const;

const downloading = {
  ...base,
  acquisition_id: "acq-downloading",
  state: "running",
  accepted_at: "2026-10-20T08:30:00Z",
  stage: "download_wait",
  stage_state: "running",
  release: { release_id: "rel-3", title: { state: "visible", value: "[SampleGroup] 进击的巨人 S2 - 03 [WebRip 1080p HEVC-10bit AAC]" } },
  episodes: [3],
  transfer: { completed_bytes: 412_000_000, total_bytes: 1_073_741_824, download_speed: 3_250_000, observed_at: "2026-10-20T08:41:00Z" },
};
const succeeded = {
  ...base,
  acquisition_id: "acq-succeeded",
  state: "succeeded",
  accepted_at: "2026-10-13T08:30:00Z",
  ended_at: "2026-10-13T08:52:00Z",
  release: { release_id: "rel-2", title: { state: "visible", value: "[SampleGroup] 进击的巨人 S2 - 02 [WebRip 1080p HEVC-10bit AAC]" } },
  episodes: [2],
  counts: { total: 2, required: 2, excluded: 0, succeeded: 2, failed: 0 },
};
const batch = {
  ...base,
  acquisition_id: "acq-batch",
  entry_kind: "subscription_add",
  state: "succeeded",
  accepted_at: "2026-10-06T08:30:00Z",
  ended_at: "2026-10-06T09:10:00Z",
  release: { release_id: "rel-1", title: { state: "visible", value: "[Sakurato] 进击的巨人 [01-28][Fin][1080p][CHS]" } },
  episodes: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12],
  counts: { total: 12, required: 12, excluded: 0, succeeded: 12, failed: 0 },
};
const movie = {
  ...base,
  acquisition_id: "acq-movie",
  entry_kind: "one_shot",
  one_shot_id: "osa-1",
  state: "succeeded",
  accepted_at: "2026-10-01T08:30:00Z",
  ended_at: "2026-10-01T08:45:00Z",
  source_title: { state: "visible", value: "[SampleGroup] 葬送的芙莉莲 特别篇 - 「旅途的终点」 [1080P][Baha][WEB-DL][AAC AVC][CHT].mp4" },
  release: { release_id: "rel-0", title: { state: "visible", value: "[SampleGroup] 葬送的芙莉莲 特别篇 - 「旅途的终点」 [1080P][Baha][WEB-DL][AAC AVC][CHT].mp4" } },
  counts: { total: 1, required: 1, excluded: 0, succeeded: 1, failed: 0 },
};

const stages = (download: "running" | "succeeded") => [
  { stage: "submit", availability: "present", state: "succeeded", updated_at: "2026-10-20T08:30:05Z" },
  { stage: "download_wait", availability: "present", state: download, updated_at: "2026-10-20T08:41:00Z" },
  ...(download === "running"
    ? [
        { stage: "inspect", availability: "not_reached" },
        { stage: "plan", availability: "not_reached" },
        { stage: "file_execution", availability: "not_reached" },
        { stage: "settlement", availability: "present", state: "pending" },
        { stage: "completion", availability: "not_reached" },
      ]
    : [
        { stage: "inspect", availability: "present", state: "succeeded" },
        { stage: "plan", availability: "present", state: "succeeded" },
        { stage: "file_execution", availability: "present", state: "succeeded", done: 2, total: 2 },
        { stage: "settlement", availability: "present", state: "succeeded" },
        { stage: "completion", availability: "present", state: "succeeded", reason_code: "seed_kept" },
      ]),
];

const details: Record<string, unknown> = {
  "acq-downloading": { ...downloading, execution_branch: "download", as_of: "2026-10-20T08:42:00Z", stages: stages("running"), files_available: false },
  "acq-succeeded": {
    ...succeeded,
    execution_branch: "download",
    as_of: "2026-10-20T08:42:00Z",
    stages: stages("succeeded"),
    requirements: { status: "sealed", file_count: 2, required_count: 2, excluded_count: 0 },
    outcome: {
      outcome: "succeeded",
      requirements_status: "sealed",
      counts: succeeded.counts,
      effects_complete: true,
      has_confirmed_effect: true,
      has_unknown_effect: false,
      settled_at: "2026-10-13T08:52:00Z",
    },
    files_available: true,
  },
};

const delivered = { outcome: "succeeded", completion_mode: "executed", effect_class: "confirmed", source_cleanup: "not_required" };
const files = {
  acquisition_id: "acq-succeeded",
  as_of: "2026-10-20T08:42:00Z",
  items: [
    {
      ordinal: 0,
      file_ref: "f0",
      relative_path: { state: "visible", value: "[SampleGroup] 进击的巨人 S2 - 02 [WebRip 1080p HEVC-10bit AAC].mkv" },
      size_bytes: 1_073_741_824,
      media_kind: "video",
      requirement: "required",
      operation: { state: "completed", operation: "hardlink" },
      delivery: delivered,
      handoff: { state: "adopted" },
      placed_path: { state: "visible", value: "/media/anime/葬送的芙莉莲 (2023)/Season 02/葬送的芙莉莲 S02E02.mkv" },
    },
    {
      ordinal: 1,
      file_ref: "f1",
      relative_path: { state: "visible", value: "[SampleGroup] 进击的巨人 S2 - 02 [WebRip 1080p HEVC-10bit AAC].chs.ass" },
      size_bytes: 52_000,
      media_kind: "subtitle",
      requirement: "required",
      operation: { state: "completed", operation: "hardlink" },
      delivery: delivered,
      placed_path: { state: "visible", value: "/media/anime/葬送的芙莉莲 (2023)/Season 02/葬送的芙莉莲 S02E02.chs.ass" },
    },
  ],
};

function json(body: unknown, status = 200) {
  return { status, contentType: "application/json", body: JSON.stringify(body) };
}

function withMethods(allowed: string[], handler: (route: Route) => Promise<void>) {
  return async (route: Route) => {
    const method = route.request().method();
    if (!allowed.includes(method)) {
      await route.fulfill({ status: 405, headers: { Allow: allowed.join(", ") }, contentType: "application/json", body: JSON.stringify({ error: "fixture_method_not_allowed" }) });
      return;
    }
    await handler(route);
  };
}

async function enterRecords(page: Page, record: string) {
  await mockSignedIn(page);
  await page.route("**/api/v1/events/stream", withMethods(["GET"], (route) => route.fulfill({ status: 200, contentType: "text/event-stream", body: ": fixture stream\n\n" })));
  await page.route("**/api/v1/config/connections", withMethods(["GET"], (route) => route.fulfill(json([]))));
  await page.route("**/api/v1/automation", withMethods(["GET"], (route) => route.fulfill(json([]))));
  await page.route("**/api/v1/observability/diagnostics**", withMethods(["GET"], (route) => route.fulfill(json({ episodes: [], next_cursor: null }))));
  await page.route("**/api/v1/observability/records**", withMethods(["GET"], (route) => route.fulfill(json({ records: [], next_cursor: null }))));
  await page.route("**/api/v1/anime-config-candidates**", withMethods(["GET"], (route) => route.fulfill(json({ candidates: [] }))));
  await page.route("**/api/v1/catalog/suppressions", withMethods(["GET"], (route) => route.fulfill(json([]))));
  // The server splits by origin (subscription = automatic + subscription_add) and counts both.
  await page.route("**/api/v1/acquisitions?**", withMethods(["GET"], async (route) => {
    const origin = new URL(route.request().url()).searchParams.get("origin");
    const all = [downloading, succeeded, batch, movie];
    const items = all.filter((item) => !origin || (origin === "one_shot") === (item.entry_kind === "one_shot"));
    await route.fulfill(json({ as_of: "2026-10-20T08:42:00Z", items, origin_counts: { subscription: 3, one_shot: 1 } }));
  }));
  await page.route("**/api/v1/acquisitions/*/files**", withMethods(["GET"], (route) => route.fulfill(json(files))));
  await page.route("**/api/v1/acquisitions/*", withMethods(["GET"], async (route) => {
    const id = new URL(route.request().url()).pathname.split("/").pop() ?? "";
    await route.fulfill(details[id] ? json(details[id]) : json({ code: "ACQUISITION_NOT_FOUND", message: "missing" }, 404));
  }));
  await page.goto(`/#/?record=${record}`);
}

async function capture(page: Page, name: string, focus?: Locator) {
  if (!evidenceDir || test.info().project.name !== "desktop") return;
  for (const width of [1280, 400]) {
    await page.setViewportSize({ width, height: width === 400 ? 860 : 900 });
    await page.waitForTimeout(150);
    if (focus) await focus.scrollIntoViewIfNeeded();
    await page.screenshot({ path: `${evidenceDir}/${name}-${width}.png` });
  }
}

test.describe("execution records", () => {
  test.skip(!fixtureOnly, "fixture UI contract runs only with BANSOKO_E2E_FIXTURES=1");

  test("列表按集数或种子标题区分同一番剧的记录，下载中显示进度与速度", async ({ page }) => {
    await enterRecords(page, "list");
    const list = page.getByRole("list", { name: "执行记录列表" });
    await expect(list).toBeVisible({ timeout: 10000 });
    await expect(list.getByText("第 3 集", { exact: true })).toBeVisible();
    await expect(list.getByText("第 2 集", { exact: true })).toBeVisible();
    await expect(list.getByText("第 01-12 集", { exact: true })).toBeVisible();
    await expect(list.getByText(/已下载 38% · 3\.1 MB\/s/)).toBeVisible();
    const noHorizontalScroll = await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth);
    expect(noHorizontalScroll).toBe(true);
    await capture(page, "records-list");
  });

  test("订阅 / 一次性分段：服务端筛选、显示计数，分段写进链接并在刷新后保留", async ({ page }) => {
    await enterRecords(page, "list");
    const dialog = page.getByRole("dialog", { name: "执行记录" });
    const segments = dialog.getByRole("radiogroup", { name: "记录来源" });
    await expect(segments.getByRole("radio", { name: "订阅 3" })).toBeChecked({ timeout: 10000 });
    await expect(segments.getByRole("radio", { name: "一次性 1" })).toBeVisible();
    const list = page.getByRole("list", { name: "执行记录列表" });
    await expect(list.getByRole("listitem")).toHaveCount(3);
    await capture(page, "records-segment-subscription");

    await segments.getByRole("radio", { name: "一次性 1" }).click();
    await expect(page).toHaveURL(/record_segment=one_shot/);
    await expect(list.getByRole("listitem")).toHaveCount(1);
    await expect(dialog.getByText("来源", { exact: true })).toHaveCount(0);
    const noHorizontalScroll = await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth);
    expect(noHorizontalScroll).toBe(true);
    await capture(page, "records-segment-one_shot");

    await page.reload();
    await expect(segments.getByRole("radio", { name: "一次性 1" })).toBeChecked({ timeout: 10000 });
  });

  test("详情的下载阶段显示进度，文件行显示入库位置", async ({ page }) => {
    await enterRecords(page, "acq-downloading");
    const stagesList = page.getByRole("list", { name: "处理阶段列表" });
    await expect(stagesList.getByText(/已下载 38% · 3\.1 MB\/s/)).toBeVisible({ timeout: 10000 });
    await capture(page, "records-detail-downloading");

    await enterRecords(page, "acq-succeeded");
    const fileList = page.getByRole("list", { name: "文件列表" });
    await expect(fileList.getByText("入库位置：/media/anime/葬送的芙莉莲 (2023)/Season 02/葬送的芙莉莲 S02E02.mkv")).toBeVisible({ timeout: 10000 });
    await expect(page.getByRole("list", { name: "处理阶段列表" }).getByText(/已下载/)).toHaveCount(0);
    const noHorizontalScroll = await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth);
    expect(noHorizontalScroll).toBe(true);
    await capture(page, "records-detail-succeeded", fileList);
  });
});
