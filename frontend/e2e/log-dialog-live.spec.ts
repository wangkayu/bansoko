import { test, expect, type Locator, type Page } from "@playwright/test";
import { shouldSkipAuthTests } from "./skip-auth-helper";
import { anchorNamingAtHarnessLibrary } from "./live-harness-library";
import {
  OPS_DENIED_ROOT,
  apiJSON,
  captureWidths,
  createOpsPreset,
  ensureOpsQbConnection,
  libraryRoot,
  moveNamingRoot,
  opsPrerequisite,
  opsRun,
  subscribeOpsPreset,
  waitForCondition,
  waitForRecords,
  waitForRecordState,
} from "./live-ops-chain";

// 日志（事件 + 诊断）的真实链路：日志在下载进行时就打开并按番剧筛选，不重新加载，
// 订阅、元数据、种子依次出现，一次下载（下载、校验、入库、入库位置、替换版本、获取
// 结果）合成一行「字幕组 · 第 N 集 · 结果」，展开按时间列出各步（日志可读性 10-21）；
// 条目文案可读（筛选到单番时不重复番剧名、无内部词与原始代码，成功条目不重复原因），
// 行数与 API 条目按同一分组规则对应。入库失败的订阅显示失败行，展开后的入库步骤带
// 原因。诊断页签可读。每个状态截 1280 与 400 两种宽度。

type BusinessEntry = {
  kind: string;
  outcome: string;
  title: string;
  summary: string;
  reason_code?: string;
  release_id?: string | null;
};

// 一次下载包含的记录类型；只有入库位置或替换版本的序列不算一次下载。
const RUN_KINDS = new Set([
  "download_changed",
  "artifact_verified",
  "file_alignment_completed",
  "file_alignment_failed",
  "placement_changed",
  "replacement_decided",
  "acquisition_settled",
]);
const CORE_RUN_KINDS = new Set(["download_changed", "artifact_verified", "file_alignment_completed", "file_alignment_failed", "acquisition_settled"]);

/** 事件页签的行数：一次获取一行（每个种子一次获取），其余条目各一行。 */
function expectedRowCount(entries: BusinessEntry[]): number {
  const runReleases = new Set(
    entries.filter((entry) => entry.release_id && CORE_RUN_KINDS.has(entry.kind)).map((entry) => entry.release_id!),
  );
  const singles = entries.filter((entry) => !(entry.release_id && RUN_KINDS.has(entry.kind) && runReleases.has(entry.release_id)));
  return singles.length + runReleases.size;
}
type ObservabilityRecord = { record_type: string; entry?: BusinessEntry };

const prerequisite = opsPrerequisite();

function logDialog(page: Page): Locator {
  return page.getByRole("dialog", { name: "日志" });
}

// 只数顶层行：展开的下载步骤是行内的嵌套列表。
function logRows(page: Page): Locator {
  return logDialog(page).getByRole("list", { name: "事件列表" }).locator(":scope > li");
}

function runRows(page: Page): Locator {
  return logRows(page).filter({ has: page.getByText("下载", { exact: true }) });
}

async function filterAnime(page: Page, title: string) {
  await logDialog(page).getByRole("combobox", { name: "番剧" }).click();
  await page.getByRole("option", { name: title, exact: true }).click();
}

async function businessEntries(page: Page, managedID: string): Promise<BusinessEntry[]> {
  const listed = await apiJSON<{ records: ObservabilityRecord[] }>(
    page,
    "GET",
    `/observability/records?kind=business&kind=plugin&managed_anime_id=${managedID}&limit=50`,
  );
  expect(listed.status).toBe(200);
  return listed.body.records.flatMap((record) => (record.record_type === "business" && record.entry ? [record.entry] : []));
}

test.describe("日志（真实链路）", () => {
  test.skip(shouldSkipAuthTests(), "auth setup was skipped");
  test.skip(Boolean(prerequisite), prerequisite ?? "");
  test.use({ viewport: { width: 1280, height: 900 } });

  test("订阅下载与入库失败的事件，以及诊断", async ({ page, isMobile }, testInfo) => {
    test.skip(isMobile, "the desktop project captures both widths");
    test.setTimeout(15 * 60_000);
    await page.goto("/");
    await expect(page.getByRole("navigation", { name: "主导航" })).toBeVisible({ timeout: 15000 });
    await anchorNamingAtHarnessLibrary(page);
    await ensureOpsQbConnection(page);

    // --- 成功的订阅：日志在完成前打开并保持打开 ------------------------------------------
    const run = opsRun("log");
    const title = `Ops Log ${run}`;
    await createOpsPreset(page, { title, label: run, run, items: ["batch"] });
    const managedID = await subscribeOpsPreset(page, title);
    await page.goto("/#/?log=records");
    await expect(logDialog(page)).toBeVisible();
    await filterAnime(page, title);
    await expect(logRows(page).filter({ hasText: "订阅已创建" })).toHaveCount(1, { timeout: 30000 });
    // No reload from here: entries arrive through realtime invalidation.
    const runRow = runRows(page);
    await expect(runRow).toHaveCount(1, { timeout: 6 * 60_000 });
    const [record] = await waitForRecords(page, managedID, 1);
    await waitForRecordState(page, record.acquisition_id, ["succeeded"]);
    let entries: BusinessEntry[] = [];
    await waitForCondition(
      async () => {
        entries = await businessEntries(page, managedID);
        return entries.some((entry) => entry.kind === "acquisition_settled") && entries.some((entry) => entry.kind === "replacement_decided");
      },
      60000,
      "the settled and replacement entries",
    );
    await expect(runRow.getByText("已入库", { exact: true })).toBeVisible({ timeout: 30000 });
    // Filtered to this anime: the run title names the subgroup and episode, not the anime.
    await expect(runRow.getByRole("button", { name: new RegExp(`^${title.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`) })).toHaveCount(0);

    // Rows follow the API entries under the same grouping: one row per download, singles in order.
    await expect(logRows(page)).toHaveCount(expectedRowCount(entries), { timeout: 30000 });
    const singles = entries.filter((entry) => !(entry.release_id && RUN_KINDS.has(entry.kind)));
    for (const entry of singles) {
      await expect(logRows(page).filter({ hasText: entry.summary }).first()).toBeVisible();
    }
    // The expanded run lists its steps in order with concrete content.
    await runRow.getByRole("button", { expanded: false }).click();
    const steps = runRow.getByRole("list", { name: "下载步骤" });
    await expect(steps).toBeVisible();
    await expect(steps).toContainText("下载状态变化");
    await expect(steps).toContainText("获取结果");
    await expect(steps).toContainText(/替换版本：/);
    await expect(steps).toContainText(/已入库：/);
    // Readable copy: no internal terms or raw codes, no reason repeated on success rows.
    const replacement = entries.find((entry) => entry.kind === "replacement_decided")!;
    expect(replacement.release_id).toBeTruthy();
    expect(replacement.summary).toMatch(/^替换版本：/);
    const placement = entries.find((entry) => entry.kind === "placement_changed")!;
    expect(placement).toMatchObject({ title, summary: "已记录入库位置" });
    expect(placement.release_id).toBeTruthy();
    expect(entries.find((entry) => entry.kind === "metadata_applied")?.summary).toBe("已关联 Bangumi、TMDB");
    const list = logDialog(page).getByRole("list", { name: "事件列表" });
    await expect(list.getByText(/洗版|BGM\/TMDB|：(queue|keep|retain|replace|blocked)$|未知原因|原因：/)).toHaveCount(0);
    await expect(list.getByText("订阅记录", { exact: true })).toHaveCount(0);
    await captureWidths(page, testInfo, "log-records-subscription");

    // --- 入库失败的订阅：失败条目带原因 ----------------------------------------------
    const failRun = opsRun("logf");
    const failTitle = `Ops Log Fail ${failRun}`;
    await moveNamingRoot(page, libraryRoot(), OPS_DENIED_ROOT);
    try {
      await createOpsPreset(page, { title: failTitle, label: failRun, run: failRun, items: ["batch"] });
    } finally {
      await moveNamingRoot(page, OPS_DENIED_ROOT, libraryRoot());
    }
    const failedID = await subscribeOpsPreset(page, failTitle);
    await page.goto("/#/?log=records");
    await expect(logDialog(page)).toBeVisible();
    await filterAnime(page, failTitle);
    const failedRow = runRows(page);
    await expect(failedRow).toHaveCount(1, { timeout: 6 * 60_000 });
    const [failedRecord] = await waitForRecords(page, failedID, 1);
    await waitForRecordState(page, failedRecord.acquisition_id, ["failed"]);
    // 失败事件的状态与原因合并在同一段文本里（「获取失败 · 原因：目标目录无效」），
    // 不能按整字匹配单个词；行首状态徽标同理（「失败」可能带「新」角标）。
    await expect(failedRow.getByText(/获取失败/)).toBeVisible({ timeout: 60000 });
    await expect(failedRow.getByText(/^失败/).first()).toBeVisible();
    await failedRow.getByRole("button", { expanded: false }).click();
    const failedSteps = failedRow.getByRole("list", { name: "下载步骤" });
    await expect(failedSteps).toContainText("入库失败");
    await expect(failedSteps).toContainText("原因：目标目录无效");
    let failedEntries: BusinessEntry[] = [];
    await waitForCondition(
      async () => {
        failedEntries = await businessEntries(page, failedID);
        return failedEntries.some((entry) => entry.kind === "acquisition_settled");
      },
      60000,
      "the failed acquisition's settled entry",
    );
    const alignment = failedEntries.find((entry) => entry.kind === "file_alignment_failed")!;
    expect(alignment.outcome).toBe("failure");
    expect(alignment.reason_code?.toLowerCase()).toBe("target_parent_invalid");
    expect(failedEntries.find((entry) => entry.kind === "acquisition_settled")?.outcome).toBe("failure");
    await expect(logRows(page)).toHaveCount(expectedRowCount(failedEntries), { timeout: 30000 });
    await expect(logDialog(page).getByText(/未知原因/)).toHaveCount(0);
    await captureWidths(page, testInfo, "log-records-failure");

    // --- 诊断 ------------------------------------------------------------------
    await logDialog(page).getByRole("tab", { name: "诊断", exact: true }).click();
    await expect(
      logDialog(page).getByRole("list", { name: "诊断列表" }).or(logDialog(page).getByText("暂无诊断记录", { exact: true })),
    ).toBeVisible({ timeout: 30000 });
    await expect(logDialog(page).getByText(/undefined|NaN/)).toHaveCount(0);
    await captureWidths(page, testInfo, "log-diagnostics");
  });
});
