import { test, expect, type Locator, type Page } from "@playwright/test";
import { shouldSkipAuthTests } from "./skip-auth-helper";
import { anchorNamingAtHarnessLibrary } from "./live-harness-library";
import {
  apiJSON,
  captureWidths,
  createOpsPreset,
  ensureOpsQbConnection,
  opsPrerequisite,
  opsRun,
  opsSlowTorrentName,
  qbConnectionInput,
  subscribeOpsPreset,
  waitForRecords,
  waitForRecordState,
  type QbConnectionRow,
} from "./live-ops-chain";

// 下载器管理的真实链路：订阅下载进行中（进度、速度、剩余时间、占用来源与删除保护，
// 与 qB 接口一致）、暂停/恢复、下载完成并入库后释放占用（做种中、可删除、无剩余时间）、
// 展开文件、删除任务，以及下载器断开后错误态与恢复。每个状态截 1280 与 400 两种宽度。

type Torrent = { hash: string; name: string; state: string; progress: number; acquisition_owner?: string; origin: string };

const prerequisite = opsPrerequisite();

function manager(page: Page): Locator {
  return page.getByRole("dialog", { name: "下载器管理" });
}

function taskRow(page: Page, name: string): Locator {
  return manager(page).getByRole("list", { name: "下载任务列表" }).getByRole("listitem").filter({ hasText: name });
}

async function torrents(page: Page, connectionID: string): Promise<Torrent[]> {
  const listed = await apiJSON<Torrent[]>(page, "GET", `/config/connections/${connectionID}/qbittorrent/torrents`);
  expect(listed.status, JSON.stringify(listed.body)).toBe(200);
  return listed.body;
}

async function moveConnection(page: Page, connection: QbConnectionRow, baseURL: string): Promise<QbConnectionRow> {
  const current = (await apiJSON<QbConnectionRow[]>(page, "GET", "/config/connections")).body.find((c) => c.id === connection.id)!;
  const saved = await apiJSON<QbConnectionRow>(page, "PUT", `/config/connections/${connection.id}`, qbConnectionInput(current.name, baseURL, true), {
    "If-Match": String(current.version),
  });
  expect(saved.status, JSON.stringify(saved.body)).toBe(200);
  return saved.body;
}

test.describe("下载器管理（真实链路）", () => {
  test.skip(shouldSkipAuthTests(), "auth setup was skipped");
  test.skip(Boolean(prerequisite), prerequisite ?? "");
  test.use({ viewport: { width: 1280, height: 900 } });

  test("下载中、暂停与恢复、完成后释放占用、删除，以及下载器断开与恢复", async ({ page, isMobile }, testInfo) => {
    test.skip(isMobile, "the desktop project captures both widths");
    test.setTimeout(12 * 60_000);
    await page.goto("/");
    await expect(page.getByRole("navigation", { name: "主导航" })).toBeVisible({ timeout: 15000 });
    await anchorNamingAtHarnessLibrary(page);
    const connection = await ensureOpsQbConnection(page);

    const run = opsRun("qb");
    const title = `Ops QB ${run}`;
    await createOpsPreset(page, { title, label: run, run, items: ["slow"] });
    const managedID = await subscribeOpsPreset(page, title);
    const name = opsSlowTorrentName(run);

    // --- 下载中：订阅分段、删除保护、速度与剩余时间，与 qB 接口一致 ---------------------
    await page.goto("/#/?tool=qbittorrent&qb_segment=subscription");
    await expect(manager(page)).toBeVisible();
    const row = taskRow(page, name);
    await expect(row).toBeVisible({ timeout: 180000 });
    await expect(row.getByText("下载中", { exact: true })).toBeVisible({ timeout: 60000 });
    const deleteButton = row.getByRole("button", { name: `删除任务 ${name}`, exact: true });
    // 任务在「订阅」分段里；占用方与分段相同，所以不再单独标注。
    await expect(manager(page).getByRole("radio", { name: /^订阅 \d+$/ })).toBeChecked();
    await expect(row.getByText("订阅", { exact: true })).toHaveCount(0);
    await expect(deleteButton).toBeDisabled({ timeout: 15000 });
    await expect(row.getByText("后处理完成前不能删除", { exact: true })).toBeVisible();
    await expect(row.getByText(/^↓\d/)).toBeVisible();
    await expect(row.getByText(/^剩余时间 \d/)).toBeVisible();
    await expect(manager(page).getByText(/∞|NaN|undefined/)).toHaveCount(0);
    await expect(manager(page).getByRole("status").getByText(/^可用 -|^↓-|^↑-/)).toHaveCount(0);
    const downloading = (await torrents(page, connection.id)).find((item) => item.name === name)!;
    expect(downloading.acquisition_owner, "API owner while the subscription download runs").toBe("subscription");
    expect(downloading.origin, "API origin of a subscription download").toBe("subscription");
    expect(downloading.progress).toBeLessThan(1);
    // The server refuses the delete too, not only the button.
    const refused = await apiJSON<{ code?: string }>(page, "DELETE", `/config/connections/${connection.id}/qbittorrent/torrents/${downloading.hash}`);
    expect(refused.status).toBe(409);
    expect(refused.body.code).toBe("QB_TORRENT_IN_USE");
    await captureWidths(page, testInfo, "qb-downloading");

    // --- 暂停与恢复 --------------------------------------------------------------
    await row.getByRole("button", { name: "暂停", exact: true }).click();
    await expect(row.getByText("已暂停", { exact: true })).toBeVisible({ timeout: 20000 });
    await expect(row.getByText(/^剩余时间|^↓/)).toHaveCount(0);
    await expect(row.getByRole("button", { name: "恢复", exact: true })).toBeVisible();
    expect((await torrents(page, connection.id)).find((item) => item.name === name)?.state).toMatch(/^(pausedDL|stoppedDL)$/);
    await captureWidths(page, testInfo, "qb-paused");
    await row.getByRole("button", { name: "恢复", exact: true }).click();
    await expect(row.getByText(/^(下载中|等待中|做种中)$/)).toBeVisible({ timeout: 30000 });

    // --- 完成并入库后：做种、释放占用、可删除、没有剩余时间 ------------------------------
    const [record] = await waitForRecords(page, managedID, 1);
    await waitForRecordState(page, record.acquisition_id, ["succeeded"], 6 * 60_000);
    await expect(row.getByText("做种中", { exact: true })).toBeVisible({ timeout: 60000 });
    await expect(row.getByText("订阅", { exact: true })).toHaveCount(0, { timeout: 30000 });
    await expect(deleteButton).toBeEnabled();
    await expect(row.getByText("100%", { exact: true })).toBeVisible();
    await expect(row.getByText(/剩余时间|∞/)).toHaveCount(0);
    const seeding = (await torrents(page, connection.id)).find((item) => item.name === name)!;
    expect(seeding.acquisition_owner).toBeUndefined();
    // A finished download keeps its origin: it stays in the 订阅 segment while it seeds.
    expect(seeding.origin).toBe("subscription");
    expect(seeding.progress).toBe(1);

    await row.getByRole("button", { name: "展开文件列表", exact: true }).click();
    const files = row.getByRole("list", { name: "文件列表" });
    await expect(files.getByText(name, { exact: true })).toBeVisible({ timeout: 15000 });
    await expect(files.getByText("100%", { exact: true })).toBeVisible();
    await captureWidths(page, testInfo, "qb-seeding");

    // --- 删除任务（保留文件） -------------------------------------------------------
    await deleteButton.click();
    const confirm = page.getByRole("dialog", { name: `删除任务「${name}」？` });
    await expect(confirm.getByRole("checkbox", { name: "同时删除已下载的文件" })).not.toBeChecked();
    await confirm.getByRole("button", { name: /删\s*除/ }).click();
    await expect(page.getByText("已删除任务", { exact: true })).toBeVisible({ timeout: 15000 });
    await expect(row).toHaveCount(0, { timeout: 15000 });
    expect((await torrents(page, connection.id)).some((item) => item.name === name)).toBe(false);

    // --- 下载器断开：打开中的管理窗口显示无法连接，地址恢复后自动回到任务列表 ---------------
    const endpoint = process.env.BANSOKO_QB_ENDPOINT!.trim();
    try {
      await moveConnection(page, connection, "http://127.0.0.1:1");
      await page.goto("/#/?tool=qbittorrent");
      // Status and task list fail together; the message shows once.
      await expect(manager(page).getByText("无法连接下载器，请检查地址和网络")).toHaveCount(1, { timeout: 60000 });
      await expect(manager(page).getByText(/登录失败/)).toHaveCount(0);
      await captureWidths(page, testInfo, "qb-unreachable");
    } finally {
      await moveConnection(page, connection, endpoint);
    }
    // No reload: the open manager keeps polling and recovers by itself.
    await expect(manager(page).getByRole("status").getByText("已连接", { exact: true })).toBeVisible({ timeout: 60000 });
    await expect(manager(page).getByText("无法连接下载器，请检查地址和网络")).toHaveCount(0);
    await captureWidths(page, testInfo, "qb-recovered");
  });
});
