import { test, expect, type Locator, type Page } from "@playwright/test";
import { shouldSkipAuthTests } from "./skip-auth-helper";
import { anchorNamingAtHarnessLibrary } from "./live-harness-library";
import {
  OPS_BATCH_RELEASE,
  OPS_DENIED_ROOT,
  OPS_SLOW_RELEASE,
  apiJSON,
  captureWidths,
  createOpsPreset,
  ensureOpsQbConnection,
  libraryRoot,
  listRecords,
  moveNamingRoot,
  opsPrerequisite,
  opsRun,
  presetCard,
  readRecord,
  recordForRelease,
  subscribeOpsPreset,
  waitForCondition,
  waitForRecords,
  waitForRecordState,
} from "./live-ops-chain";

// 执行记录的真实链路：
// 1. 订阅下载（一个慢速单集 + 一个含未知文件的合集）：列表在打开期间随实时事件出现并
//    从进行中变为成功，详情打开期间从「下载 · 进行中」走到成功，不需要重新加载；合集里
//    没有集数的视频标注不入库及原因；名称与番剧标题相同时只显示一次。
// 2. 一次性下载入库失败（目标目录无效）：「一次性」分段（服务端筛选）、列表实时变为失败、
//    失败阶段与原因、文件行原因；失败的一次性下载只提示从「浏览种子」重新下载，
//    没有重试 / 重新规划（已移除），标记随结算排空为 retention_pending。
// 每个状态截 1280 与 400 两种宽度，并与 API 返回值比对。

const prerequisite = opsPrerequisite();

function records(page: Page): Locator {
  return page.getByRole("dialog", { name: "执行记录" });
}

function recordRows(page: Page): Locator {
  return records(page).getByRole("list", { name: "执行记录列表" }).getByRole("listitem");
}

function stage(page: Page, name: string): Locator {
  return records(page).getByRole("list", { name: "处理阶段列表" }).getByRole("listitem").filter({ has: page.getByText(name, { exact: true }) });
}

function fileRow(page: Page, path: RegExp): Locator {
  return records(page).getByRole("list", { name: "文件列表" }).getByRole("listitem").filter({ hasText: path });
}

test.describe("执行记录（真实链路）", () => {
  test.skip(shouldSkipAuthTests(), "auth setup was skipped");
  test.skip(Boolean(prerequisite), prerequisite ?? "");
  test.use({ viewport: { width: 1280, height: 900 } });

  test.beforeEach(async ({ page }) => {
    await page.goto("/");
    await expect(page.getByRole("navigation", { name: "主导航" })).toBeVisible({ timeout: 15000 });
    await anchorNamingAtHarnessLibrary(page);
    await ensureOpsQbConnection(page);
  });

  test("订阅下载：打开期间实时更新，成功后显示阶段、计数与未知文件", async ({ page, isMobile }, testInfo) => {
    test.skip(isMobile, "the desktop project captures both widths");
    test.setTimeout(12 * 60_000);
    const run = opsRun("rec");
    const title = `Ops Records ${run}`;
    await createOpsPreset(page, { title, label: run, run, items: ["slow", "batch"] });
    const managedID = await subscribeOpsPreset(page, title);

    // The list is open before any record exists and is never reloaded.
    await page.goto(`/#/?record=list&record_anime=${managedID}&record_segment=subscription`);
    await expect(records(page)).toBeVisible();
    await expect(recordRows(page)).toHaveCount(2, { timeout: 180000 });
    const all = await waitForRecords(page, managedID, 2);
    const slow = await recordForRelease(page, all, OPS_SLOW_RELEASE);
    const batch = await recordForRelease(page, all, OPS_BATCH_RELEASE);
    // A subscription record is named after the anime: the title shows once.
    for (const row of await recordRows(page).all()) {
      await expect(row.getByText(title, { exact: true })).toHaveCount(1);
      await expect(row.getByText("订阅自动下载", { exact: true })).toBeVisible();
    }
    const slowRow = recordRows(page).filter({ hasText: "进行中" }).first();
    await expect(slowRow).toBeVisible({ timeout: 60000 });
    await expect(slowRow.getByText(/^下载 · 进行中$/)).toBeVisible({ timeout: 60000 });
    await captureWidths(page, testInfo, "records-list-running");

    // Detail of the running download, kept open until it settles.
    await page.goto(`/#/?record=${slow.acquisition_id}`);
    await expect(records(page).getByText(OPS_SLOW_RELEASE, { exact: true })).toBeVisible({ timeout: 15000 });
    const running = await readRecord(page, slow.acquisition_id);
    if (running.state === "running") {
      await expect(records(page).getByText("进行中", { exact: true }).first()).toBeVisible();
      await captureWidths(page, testInfo, "records-detail-running");
    }
    await waitForRecordState(page, slow.acquisition_id, ["succeeded"], 6 * 60_000);
    await expect(records(page).getByText(/^成功$/).first()).toBeVisible({ timeout: 30000 });
    for (const name of ["提交下载", "下载", "校验文件", "生成入库计划", "入库", "汇总结果"]) {
      await expect(stage(page, name).getByText("完成", { exact: true })).toBeVisible();
    }
    await expect(records(page).getByText("共 1 个文件，需入库 1 个：成功 1 个，失败 0 个", { exact: true })).toBeVisible();
    await expect(fileRow(page, /Browser Ops Show - 02/).getByText("已入库", { exact: true })).toBeVisible({ timeout: 30000 });
    await expect(records(page).getByText(/未知原因|undefined|null/)).toHaveCount(0);
    await captureWidths(page, testInfo, "records-detail-succeeded");

    // The batch: the NCOP video has no episode number and is not placed.
    const settled = await waitForRecordState(page, batch.acquisition_id, ["succeeded"], 6 * 60_000);
    expect(settled.outcome?.counts).toMatchObject({ total: 2, required: 1, excluded: 1, succeeded: 1, failed: 0 });
    await page.goto(`/#/?record=${batch.acquisition_id}`);
    await expect(records(page).getByText("共 2 个文件，需入库 1 个：成功 1 个，失败 0 个", { exact: true })).toBeVisible({ timeout: 30000 });
    const unknown = fileRow(page, /NCOP/);
    await expect(unknown.getByText("不入库", { exact: true })).toBeVisible({ timeout: 30000 });
    await expect(unknown.getByText("未知文件，未保留", { exact: true })).toBeVisible();
    await expect(fileRow(page, /Show - 03 \[1080p\]\.mkv/).getByText("已入库", { exact: true })).toBeVisible();
    await unknown.scrollIntoViewIfNeeded();
    await captureWidths(page, testInfo, "records-detail-unknown-file");

    // Back to the list (the detail was opened by address, so no anime filter
    // carries over): both rows of this anime settled.
    await records(page).getByRole("button", { name: "全部执行记录", exact: true }).click();
    const settledRows = recordRows(page).filter({ hasText: title });
    await expect(settledRows).toHaveCount(2, { timeout: 30000 });
    await expect(settledRows.filter({ has: page.getByText("成功", { exact: true }) })).toHaveCount(2, { timeout: 30000 });
    await captureWidths(page, testInfo, "records-list-succeeded");
  });

  test("一次性下载：入库失败的阶段、文件原因与恢复入口", async ({ page, isMobile }, testInfo) => {
    test.skip(isMobile, "the desktop project captures both widths");
    test.setTimeout(12 * 60_000);
    const run = opsRun("os");
    const title = `Ops OneShot ${run}`;
    // The preset copies the Global naming when it is saved: save it while the
    // naming root is one no directory can be created under.
    await moveNamingRoot(page, libraryRoot(), OPS_DENIED_ROOT);
    try {
      await createOpsPreset(page, { title, label: run, run, items: ["batch"] });
    } finally {
      await moveNamingRoot(page, OPS_DENIED_ROOT, libraryRoot());
    }

    await page.goto("/");
    await page.getByRole("button", { name: "自定义", exact: true }).first().click();
    await presetCard(page, title).getByRole("button", { name: `浏览《${title}》的种子`, exact: true }).click();
    const browse = page.getByRole("dialog", { name: "浏览种子" });
    await expect(browse).toBeVisible();
    // Step 1 is the seed directory: selection is checkbox + 下一步 (the wizard
    // form used by oneshot-adopt-fixture.spec.ts; there is no per-row download
    // button), and the fetch button only exists before the list is loaded.
    const fetchList = browse.getByRole("button", { name: "获取种子列表", exact: true });
    const seed = browse.getByRole("checkbox", { name: OPS_BATCH_RELEASE });
    await expect(fetchList.or(seed).first()).toBeVisible({ timeout: 60000 });
    if (await fetchList.isVisible()) await fetchList.click();
    await seed.click();
    await browse.getByRole("button", { name: "下一步（1）", exact: true }).click();
    // 自定义预设不带季：季度是 owner 填的值（同 live-metadata-binding 的理由），而
    // TV 命名布局的季目录为空时第 2 步预览直接拒绝（「季目录为空，路径无效」），
    // 所以在任务草稿的季度行补上，owner 在这一步也会这么做。
    await browse.getByRole("button", { name: "编辑季度" }).click();
    await browse.getByRole("spinbutton", { name: "季度", exact: true }).fill("1");
    await browse.getByRole("button", { name: "保存季度", exact: true }).click();
    await expect(browse.getByText(new RegExp(`^${OPS_DENIED_ROOT}/`))).toBeVisible({ timeout: 30000 });
    await browse.getByRole("button", { name: "开始下载（1）", exact: true }).click();
    await expect(page.getByText("已提交 1 个一次性下载", { exact: true })).toBeVisible({ timeout: 30000 });
    await browse.getByRole("button", { name: "查看执行记录", exact: true }).click();

    // The list arrives on the 一次性 segment (filtered by the server).
    await expect(records(page)).toBeVisible();
    await expect(page).toHaveURL(/record_segment=one_shot/);
    await expect(records(page).getByRole("radio", { name: /^一次性 \d+$/ })).toBeChecked();
    const row = recordRows(page).filter({ hasText: title });
    await expect(row).toBeVisible({ timeout: 60000 });
    let oneShot = (await listRecords(page, "entry_kind=one_shot")).find((item) => item.anime?.title === title);
    await waitForCondition(
      async () => {
        oneShot = (await listRecords(page, "entry_kind=one_shot")).find((item) => item.anime?.title === title);
        return Boolean(oneShot);
      },
      60000,
      "the one-shot record",
    );
    const failed = await waitForRecordState(page, oneShot!.acquisition_id, ["failed"], 6 * 60_000);
    expect(failed.outcome?.counts).toMatchObject({ total: 2, required: 1, excluded: 1, failed: 1 });
    await expect(row.getByText("失败", { exact: true })).toBeVisible({ timeout: 30000 });
    await expect(row.getByText(/入库 · 失败 · 目标目录无效/)).toBeVisible();
    await captureWidths(page, testInfo, "records-oneshot-list-failed");

    await row.getByRole("button").first().click();
    await expect(stage(page, "入库").getByText("失败", { exact: true })).toBeVisible({ timeout: 30000 });
    await expect(stage(page, "入库").getByText(/目标目录无效/)).toBeVisible();
    const failedFile = fileRow(page, /Show - 03 \[1080p\]\.mkv/);
    await expect(failedFile.getByText("入库失败", { exact: true }).first()).toBeVisible({ timeout: 30000 });
    await expect(failedFile.getByText("目标目录无效", { exact: true }).first()).toBeVisible();
    await expect(fileRow(page, /NCOP/).getByText("未知文件，未保留", { exact: true })).toBeVisible();
    await expect(records(page).getByText(/一次性下载失败后不能继续/)).toBeVisible({ timeout: 30000 });
    await expect(records(page).getByRole("button", { name: "重新规划", exact: true })).toHaveCount(0);
    await captureWidths(page, testInfo, "records-oneshot-failed");

    // The settlement pass drains the marker at once (R-6), not on the ten-minute tick.
    type Marker = { state?: string; terminal_outcome?: string | null; failed_stage?: string | null };
    let marker: Marker = {};
    await waitForCondition(
      async () => {
        marker = (await apiJSON<Marker>(page, "GET", `/one-shot-acquisitions/${failed.one_shot_id}`)).body;
        return marker.state === "retention_pending";
      },
      60000,
      "the drained one-shot marker",
    );
    expect(marker).toMatchObject({ state: "retention_pending", terminal_outcome: "failed", failed_stage: "file_execution" });
  });
});
