import { test, expect, type Locator, type Page, type Request } from "@playwright/test";
import fs from "node:fs/promises";
import path from "node:path";
import { shouldSkipAuthTests } from "./skip-auth-helper";
import { startFromSubscribedToast } from "./live-subscription";
import { closeCardMenu } from "./live-card-menu";
import { anchorNamingAtHarnessLibrary } from "./live-harness-library";
import { bindWorkspaceMetadata } from "./live-metadata-binding";

// 本地记录清理的真实浏览器验收：通过真实 UI 完成 custom-RSS owner 的真实
// qB + 文件链（订阅 → 绑定元数据 → RSS 刷新 → 重试种子 → 已入库），退役后：
//   1. retired 卡只读，只保留「删除本地记录」危险动作；
//   2. 打开独立 danger confirmation Dialog，明确展示「不删除 qB 种子、下载数据、
//      媒体库文件」，检查位置/尺寸/overlay 合理且不与其他 Dialog 重叠；
//   3. 取消不产生任何 DELETE /local-data 请求；
//   4. 再次打开确认，提交精确的 retired lifecycle epoch；
//   5. 成功后实例从「已订阅」消失，custom 候选按设计保留（detach）；
//   6. 浏览器只读断言 receipt / owner 图清理 / candidate detach + revision 保留；
//      qB torrent / 下载字节 / 媒体文件 hash 不变由 VM 侧 read-only helper 复核。
//
// 该 spec 只通过真实 UI 与只读 API 观察；不直接改数据库终态。

const explicitBaseURL = process.env.BANSOKO_TEST_BASE_URL?.trim();
const fixtureRSSURL = process.env.BANSOKO_TEST_FIXTURE_RSS_URL?.trim();
const qbEndpoint = process.env.BANSOKO_QB_ENDPOINT?.trim();
const qbUsername = process.env.BANSOKO_QB_USERNAME?.trim();
const qbPassword = process.env.BANSOKO_QB_PASSWORD?.trim();
const instanceLabel = process.env.BANSOKO_BROWSER_FIXTURE_INSTANCE_LABEL?.trim();
const downloadRoot =
  process.env.BANSOKO_QB_DOWNLOAD_ROOT?.trim() || process.env.BANSOKO_QB_SAVE_ROOT?.trim() || "/downloads";
const downloaderRoot =
  process.env.BANSOKO_QB_DOWNLOADER_ROOT?.trim() || process.env.BANSOKO_HARNESS_DOWNLOAD_ROOT?.trim();

function livePrerequisite(): string | undefined {
  if (process.env.BANSOKO_E2E_FIXTURES === "1") {
    return "local cleanup live evidence does not run in fixture mode";
  }
  if (process.env.BANSOKO_TEST_DB_DISPOSABLE !== "1") {
    return "BANSOKO_TEST_DB_DISPOSABLE=1 is required: this suite drives the destructive local cleanup";
  }
  if (process.env.BANSOKO_E2E_REAL_EFFECT !== "1") {
    return "BANSOKO_E2E_REAL_EFFECT=1 is required: this suite completes a real download and file chain";
  }
  if (!explicitBaseURL) return "BANSOKO_TEST_BASE_URL must explicitly point at the live authenticated server";
  if (!fixtureRSSURL) return "BANSOKO_TEST_FIXTURE_RSS_URL is required; no RSS source will be invented";
  if (!qbEndpoint || !qbUsername || !qbPassword) {
    return "BANSOKO_QB_ENDPOINT, BANSOKO_QB_USERNAME, and BANSOKO_QB_PASSWORD are required";
  }
  if (!downloaderRoot) return "a qB path map or BANSOKO_QB_DOWNLOADER_ROOT/BANSOKO_HARNESS_DOWNLOAD_ROOT is required";
  if (!instanceLabel) return "BANSOKO_BROWSER_FIXTURE_INSTANCE_LABEL is required";
  return undefined;
}

function settingsDialog(page: Page): Locator {
  return page.getByRole("dialog").first();
}

function latestDialog(page: Page): Locator {
  return page.getByRole("dialog").last();
}

function sessionCard(page: Page, title: string, label: string): Locator {
  return page.getByRole("article").filter({ hasText: title }).filter({ hasText: label });
}

function cleanupDialog(page: Page): Locator {
  // ConfirmDialog renders a blocking Dialog with role="alertdialog" (see
  // ConfirmDialog.tsx / anime-grid.spec.ts); that is the single dedicated danger
  // confirmation surface.
  return page.getByRole("alertdialog");
}

async function selectOption(page: Page, container: Locator, field: string, option: string) {
  const select = container.getByRole("combobox", { name: field, exact: true });
  await select.click();
  await page.getByRole("option", { name: option, exact: true }).click();
}

async function saveQbConnection(page: Page, name: string) {
  await page.goto("/#/settings/download");
  const dialog = settingsDialog(page);
  await expect(dialog).toBeVisible({ timeout: 15000 });
  await expect(dialog.getByRole("button", { name: "新建下载器", exact: true })).toBeVisible({ timeout: 15000 });

  const existing = dialog.locator("li").filter({ hasText: name }).first();
  if (await existing.count()) {
    await existing.getByRole("button", { name: "编辑", exact: true }).click();
  } else {
    await dialog.getByRole("button", { name: "新建下载器", exact: true }).click();
  }

  const editor = latestDialog(page);
  await expect(editor).toBeVisible();
  await editor.getByLabel("名称").fill(name);
  await editor.getByLabel("qBittorrent 地址").fill(qbEndpoint!);
  await editor.getByLabel("用户名").fill(qbUsername!);
  await editor.getByLabel(/下载目录（可选）/).fill(downloadRoot);
  await editor.getByLabel(/对应本机路径（可选）/).fill(downloaderRoot!);

  const replaceCredential = editor.getByLabel("替换为新值");
  if (await replaceCredential.count()) {
    await replaceCredential.check();
  }
  await editor.getByLabel("密码").fill(qbPassword!);
  await editor.getByRole("button", { name: "保存", exact: true }).click();
  await expect(page.getByText("下载器已保存", { exact: true })).toBeVisible({ timeout: 15000 });

  const row = dialog.locator("li").filter({ hasText: name }).first();
  await expect(row).toBeVisible({ timeout: 15000 });
  const testResponse = page.waitForResponse(
    (response) => {
      const pathname = new URL(response.url()).pathname;
      return pathname.includes("/api/v1/config/connections/") && pathname.endsWith("/test") && response.request().method() === "POST";
    },
    { timeout: 15000 },
  );
  await row.getByRole("button", { name: "测试", exact: true }).click();
  const tested = await testResponse;
  expect(tested.status()).toBe(200);
  await expect(row.getByText("正常", { exact: true })).toBeVisible({ timeout: 15000 });
}

async function saveFilePolicy(page: Page) {
  const dialog = settingsDialog(page);
  const fileSection = dialog.locator("section").filter({ hasText: "整理方式" }).first();
  await expect(fileSection.getByRole("heading", { name: "整理方式", exact: true })).toBeVisible({ timeout: 15000 });
  await fileSection.getByRole("button", { name: "编辑", exact: true }).click();

  const editor = latestDialog(page);
  await expect(editor).toBeVisible();
  await selectOption(page, editor, "整理方式", "复制");
  await selectOption(page, editor, "同名文件处理", "加版本号");
  await editor.getByRole("button", { name: "保存", exact: true }).click();
  await expect(page.getByText("整理方式已保存", { exact: true })).toBeVisible({ timeout: 15000 });

  await expect(fileSection.getByRole("definition").filter({ hasText: /^复制$/ })).toBeVisible({ timeout: 15000 });
  await expect(fileSection.getByRole("definition").filter({ hasText: /^加版本号$/ })).toBeVisible();
}

async function addCustomPreset(page: Page, title: string, groupName: string, label: string) {
  // Leave the settings route entirely instead of relying on Escape: the
  // settings surface is route-driven, and a direct navigation cannot leave a
  // stale dialog behind to overlap the preset dialog.
  await page.goto("/");
  await expect(page.getByRole("button", { name: "已订阅", exact: true }).first()).toBeVisible({ timeout: 15000 });
  await page.getByRole("button", { name: "自定义", exact: true }).first().click();
  await page.getByRole("button", { name: "添加预设", exact: true }).first().click();

  const dialog = latestDialog(page);
  await expect(dialog.getByText("添加自定义预设", { exact: true })).toBeVisible();
  await dialog.getByLabel("标题", { exact: true }).fill(title);
  await dialog.getByLabel("订阅备注", { exact: true }).fill(label);
  await dialog.getByLabel("RSS 地址", { exact: true }).fill(fixtureRSSURL!);
  await dialog.getByLabel("字幕组", { exact: true }).fill(groupName);
  await dialog.getByRole("button", { name: "保存预设", exact: true }).click();
  await expect(page.getByText("自定义预设已保存", { exact: true })).toBeVisible({ timeout: 15000 });

  const card = sessionCard(page, title, label);
  await expect(card).toBeVisible({ timeout: 15000 });
  await card.getByRole("button", { name: `打开配置 ${title}`, exact: true }).click();
}

async function assertDraftTabsAndPreview(page: Page, groupName: string) {
  const dialog = latestDialog(page);
  for (const name of ["元数据", "字幕组", "规则", "RSS 预览"]) {
    await expect(dialog.getByRole("tab", { name, exact: true })).toBeVisible();
  }
  await expect(dialog.getByRole("tab")).toHaveCount(4);

  await dialog.getByRole("tab", { name: "规则", exact: true }).click();
  // The preset carries the complete Global business, including the copy/versioned
  // file effect configured in settings.
  // The rules tab renders every section from the draft; the file-effect and
  // title-filter inputs are unique in the dialog, so they need no section wrapper.
  await expect(dialog.getByRole("combobox", { name: "整理方式", exact: true })).toHaveText(/复制/);
  await expect(dialog.getByRole("combobox", { name: "同名文件处理", exact: true })).toHaveText(/加版本号/);
  // A real unsaved edit that can never exclude a fixture release.
  await dialog.getByRole("textbox", { name: "标题不能包含", exact: true }).fill("^__e2e_never_matches__$");

  // A custom RSS source is read from its own URL: opening the tab renders the
  // preset's own group in the read model. The read is side-effect-free — the
  // preset, revision and owner set stay untouched until 保存配置/订阅.
  await dialog.getByRole("tab", { name: "RSS 预览", exact: true }).click();
  await expect(dialog.getByRole("button", { name: `展开 ${groupName}` })).toContainText("共 1 个种子", { timeout: 15000 });
  await expect(dialog.getByRole("button", { name: "订阅", exact: true })).toBeDisabled();

  await dialog.getByRole("button", { name: "保存配置", exact: true }).click();
  await expect(dialog.getByText("已保存", { exact: true })).toBeVisible({ timeout: 15000 });
  await expect(dialog.getByRole("button", { name: "订阅", exact: true })).toBeEnabled();
}

type SubscribedOwner = { workspace: Locator; managedAnimeID: string; subscriptionID: string; subscriptionVersion: number };

async function subscribeAndOpenWorkspace(page: Page, title: string, label: string): Promise<SubscribedOwner> {
  const draftDialog = latestDialog(page);
  const subscribeResponse = page.waitForResponse(
    (response) => {
      const pathname = new URL(response.url()).pathname;
      return pathname.includes("/api/v1/anime-config-candidates/") && pathname.endsWith("/subscribe") && response.request().method() === "POST";
    },
    { timeout: 15000 },
  );
  await draftDialog.getByRole("button", { name: "订阅", exact: true }).click();
  const subscribed = await subscribeResponse;
  expect([200, 201]).toContain(subscribed.status());
  const body = await subscribed.json() as {
    managed_anime: { id: string };
    subscription: { id: string; managed_anime_id: string; version: number; lifecycle_epoch: number };
  };
  expect(body.managed_anime.id).toBeTruthy();
  expect(body.subscription.managed_anime_id).toBe(body.managed_anime.id);
  await expect(draftDialog).toBeHidden({ timeout: 15000 });
  const startedVersion = await startFromSubscribedToast(page, body.managed_anime.id);

  await page.getByRole("button", { name: "已订阅", exact: true }).click();
  const card = sessionCard(page, title, label);
  await expect(card).toBeVisible({ timeout: 30000 });
  await card.getByRole("button", { name: `打开配置 ${title}`, exact: true }).click();
  const workspace = latestDialog(page);
  await expect(workspace.getByRole("tab", { name: "RSS", exact: true })).toBeVisible({ timeout: 30000 });
  await workspace.getByRole("tab", { name: "RSS", exact: true }).click();
  await expect(workspace.getByRole("tab", { name: "RSS", exact: true })).toHaveAttribute("aria-selected", "true", { timeout: 30000 });
  return {
    workspace,
    managedAnimeID: body.managed_anime.id,
    subscriptionID: body.subscription.id,
    subscriptionVersion: startedVersion,
  };
}

async function requestWorkspaceRefresh(page: Page, workspace: Locator): Promise<void> {
  const refresh = workspace.getByRole("button", { name: "刷新 RSS", exact: true });
  if (!(await refresh.isVisible().catch(() => false))) return;
  const refreshResponse = page.waitForResponse(
    (response) => {
      const pathname = new URL(response.url()).pathname;
      return pathname.includes("/api/v1/rss-sources/") && pathname.endsWith("/refresh") && response.request().method() === "POST";
    },
    { timeout: 15000 },
  );
  await refresh.click();
  const refreshed = await refreshResponse;
  expect([200, 202, 409]).toContain(refreshed.status());
}

async function waitForRelease(page: Page, workspace: Locator, title: string, groupName: string): Promise<{ workspace: Locator; release: Locator }> {
  for (let attempt = 0; attempt < 45; attempt += 1) {
    const expandedGroup = workspace.getByRole("button", { name: `展开 ${groupName}`, exact: true });
    if (await expandedGroup.count()) {
      await expandedGroup.click();
    }
    const release = workspace.getByRole("button", { name: "展开文件与状态", exact: true }).first();
    if (await release.isVisible().catch(() => false)) return { workspace, release };
    await requestWorkspaceRefresh(page, workspace);
    await page.waitForTimeout(2000);
  }
  throw new Error(`release did not appear after refreshing RSS for ${title}`);
}

async function refreshExpandAndDownload(page: Page, initialWorkspace: Locator, title: string, groupName: string): Promise<Locator> {
  let workspace = initialWorkspace;
  for (const name of ["元数据", "字幕组", "规则", "RSS"]) {
    await expect(workspace.getByRole("tab", { name, exact: true })).toBeVisible();
  }

  let refreshedStatus = 0;
  for (let attempt = 0; attempt < 10; attempt += 1) {
    const refreshResponse = page.waitForResponse(
      (response) => {
        const pathname = new URL(response.url()).pathname;
        return pathname.includes("/api/v1/rss-sources/") && pathname.endsWith("/refresh") && response.request().method() === "POST";
      },
      { timeout: 15000 },
    );
    await workspace.getByRole("button", { name: "刷新 RSS", exact: true }).click();
    const refreshed = await refreshResponse;
    refreshedStatus = refreshed.status();
    if ([200, 202].includes(refreshedStatus)) break;
    expect(refreshedStatus).toBe(409);
    await page.waitForTimeout(1000);
  }
  expect([200, 202]).toContain(refreshedStatus);
  await expect(page.getByText(/RSS 刷新已提交（1 个 RSS 源）/, { exact: true })).toBeVisible({ timeout: 15000 });

  // Ordinary RSS admission acquires the release by itself once the metadata
  // binding is in place (the refresh above re-evaluates with it), so the chain
  // never presses 重试; a manual retry would mask a broken automatic chain.
  const retryRequests: string[] = [];
  const observeRetry = (request: Request) => {
    const pathname = new URL(request.url()).pathname;
    if (pathname.startsWith("/api/v1/releases/") && pathname.endsWith("/retry")) retryRequests.push(request.url());
  };
  page.on("request", observeRetry);
  const located = await waitForRelease(page, workspace, title, groupName);
  workspace = located.workspace;
  const releaseTitle = (await located.release.textContent())?.trim();
  expect(releaseTitle).toBeTruthy();
  await located.release.click();
  await expect(workspace.getByRole("button", { name: "折叠文件与状态", exact: true }).first()).toBeVisible();

  const releaseItem = workspace.locator("li").filter({ hasText: releaseTitle! }).first();
  await expect
    .poll(async () => (await releaseItem.textContent()) ?? "", { timeout: 180000, intervals: [1000, 2000, 5000] })
    .toContain("已入库");
  page.off("request", observeRetry);
  expect(retryRequests).toEqual([]);
  return workspace;
}

async function closeWorkspace(page: Page, workspace: Locator) {
  await workspace.locator(".anime-config-dialog-footer-owner").getByRole("button", { name: "关闭", exact: true }).click();
  // The workspace is closed without edits: every value above went through its
  // own save and the metadata binding reloads the dialog, so the owner form is
  // clean against the saved state and 关闭 must not prompt. A discard prompt is
  // still handled rather than ignored — an unanswered alertdialog keeps the
  // shell inert and the next top-nav click can never resolve.
  const discard = page.getByRole("alertdialog");
  await expect
    .poll(async () => {
      if (await discard.isVisible().catch(() => false)) return "discard";
      // Route-hosted dialogs unmount on close, so 关闭 lands in a count of 0.
      const stillOpen = (await workspace.count()) > 0 && (await workspace.isVisible().catch(() => false));
      return stillOpen ? "open" : "closed";
    }, { timeout: 15000 })
    .not.toBe("open");
  if (await discard.isVisible().catch(() => false)) {
    await discard.getByRole("button", { name: "放弃修改", exact: true }).click();
    await expect(discard).toBeHidden();
  }
  await expect(workspace).toBeHidden({ timeout: 15000 });
}

async function retireOwner(page: Page, card: Locator, title: string): Promise<void> {
  const deleteResponse = page.waitForResponse(
    (response) => response.request().method() === "DELETE" && new URL(response.url()).pathname.endsWith("/subscription"),
    { timeout: 15000 },
  );
  // 管理动作收在卡片右上 ⋯ 菜单里（海报墙 UI-038）。
  await card.getByRole("button", { name: `更多操作 ${title}`, exact: true }).click();
  await page.getByRole("menuitem", { name: "结束订阅", exact: true }).click();
  await page.getByRole("button", { name: "结束订阅", exact: true }).click();
  const deleted = await deleteResponse;
  expect(deleted.status()).toBe(204);
  await expect(page.getByText(`已结束订阅《${title}》`, { exact: true })).toBeVisible({ timeout: 15000 });
}

function evidenceDir(): string | undefined {
  const dir = process.env.BANSOKO_E2E_EVIDENCE_DIR?.trim() || process.env.BANSOKO_BROWSER_SESSION_STATE_DIR?.trim();
  return dir ? `${dir}/evidence` : undefined;
}

async function writeCleanupEvidence(payload: Record<string, unknown>) {
  const dir = evidenceDir();
  if (!dir) return;
  await fs.mkdir(dir, { recursive: true });
  await fs.writeFile(path.join(dir, "local-cleanup-evidence.json"), `${JSON.stringify(payload, null, 2)}\n`, "utf8");
}

async function screenshot(page: Page, name: string) {
  const dir = evidenceDir();
  if (!dir) return;
  await fs.mkdir(dir, { recursive: true });
  await page.screenshot({ path: path.join(dir, name), animations: "disabled" });
}

type AnimeListItem = {
  managed_anime: { id: string };
  subscription: { state: string; lifecycle_state: string; lifecycle_epoch: number };
};

async function readOwner(page: Page, managedAnimeID: string): Promise<AnimeListItem | undefined> {
  // The session is an HttpOnly cookie the browser context attaches itself.
  const response = await page.request.get("/api/v1/anime");
  expect(response.status()).toBe(200);
  const items = await response.json() as AnimeListItem[];
  return items.find((item) => item.managed_anime.id === managedAnimeID);
}

test.use({ trace: "off", video: "off", screenshot: "off" });

test.describe.configure({ mode: "serial" });

test.describe("local data cleanup authenticated real chain", () => {
  test.describe.configure({ retries: 0 });

  test.beforeEach(({ isMobile }) => {
    test.skip(isMobile, "the destructive real chain and cleanup run once on desktop");
    test.skip(shouldSkipAuthTests(), "auth.setup did not receive BANSOKO_E2E_USERNAME/BANSOKO_E2E_PASSWORD");
    const reason = livePrerequisite();
    test.skip(reason !== undefined, reason);
  });

  test("retired owner cleanup: dialog, cancel, exact epoch, list removal, candidate detach", async ({ page }, testInfo) => {
    // The real chain now actually runs to 已入库: RSS refresh + retry + a real
    // qB download + file placement (refreshExpandAndDownload alone polls up to
    // 180s for 已入库), and only then the retire/cleanup assertions start. The
    // old 360s never had to cover the chain because an unbound anime stopped at
    // the metadata hold in ~10s; on the VM the chain measured 6.0m.
    test.setTimeout(600000);
    const title = process.env.BANSOKO_BROWSER_FIXTURE_TITLE?.trim();
    const groupName = process.env.BANSOKO_BROWSER_FIXTURE_GROUP?.trim();
    if (!title || !groupName || !instanceLabel) throw new Error("fixture title/group metadata and BANSOKO_BROWSER_FIXTURE_INSTANCE_LABEL are required");
    const qbName = `E2E cleanup qB ${Date.now()}-${testInfo.workerIndex}`;

    await page.goto("/");
    await expect(page.getByRole("navigation", { name: "主导航" })).toBeVisible({ timeout: 15000 });
    await expect(page.getByRole("button", { name: "已订阅", exact: true }).first()).toBeVisible();
    await anchorNamingAtHarnessLibrary(page);

    // Real download + file chain so the cleanup is proven against real external effects.
    await saveQbConnection(page, qbName);
    await saveFilePolicy(page);
    await addCustomPreset(page, title, groupName, instanceLabel);
    await assertDraftTabsAndPreview(page, groupName);
    const owner = await subscribeAndOpenWorkspace(page, title, instanceLabel);
    // 受理不会在元数据 hold 下开始下载：未绑定身份时 `重试` 只会返回
    // outcome=held / waiting_metadata（契约上 held 是结果不是错误），真实下载与
    // 入库链就走不到，因此先按产品自身的 UI 绑定元数据（与 custom-rss-live 共用）。
    const bound = await bindWorkspaceMetadata(page, owner.workspace, owner.managedAnimeID);
    const workspace = await refreshExpandAndDownload(page, bound, title, groupName);
    await closeWorkspace(page, workspace);

    // Retire first: retirement preserves the graph, the card becomes read-only.
    await page.getByRole("button", { name: "已订阅", exact: true }).click();
    const card = sessionCard(page, title, instanceLabel);
    await expect(card).toBeVisible({ timeout: 30000 });
    await retireOwner(page, card, title);

    await expect(card).toBeVisible({ timeout: 15000 });
    await expect(card.getByText("已结束", { exact: true })).toBeVisible({ timeout: 15000 });
    // 退役卡的菜单只剩历史与清理本地记录，不再提供停止/启动、结束订阅。
    await card.getByRole("button", { name: `更多操作 ${title}`, exact: true }).click();
    await expect(page.getByRole("menuitem", { name: "删除本地记录", exact: true })).toBeVisible();
    await expect(page.getByRole("menuitem", { name: "停止", exact: true })).toHaveCount(0);
    await expect(page.getByRole("menuitem", { name: "启动", exact: true })).toHaveCount(0);
    await expect(page.getByRole("menuitem", { name: "结束订阅", exact: true })).toHaveCount(0);
    await expect(card.getByRole("button", { name: `打开配置 ${title}`, exact: true })).toHaveCount(0);
    await closeCardMenu(page);
    await screenshot(page, "retired-card-with-cleanup-action.png");

    // The exact retired lifecycle epoch is the CAS the dialog must submit.
    const retiredItem = await readOwner(page, owner.managedAnimeID);
    expect(retiredItem, "retired owner must stay readable before cleanup").toBeTruthy();
    expect(retiredItem!.subscription.lifecycle_state).toBe("retired");
    const lifecycleEpoch = retiredItem!.subscription.lifecycle_epoch;
    expect(lifecycleEpoch).toBeGreaterThanOrEqual(2);

    const openCleanup = async () => {
      await card.getByRole("button", { name: `更多操作 ${title}`, exact: true }).click();
      await page.getByRole("menuitem", { name: "删除本地记录", exact: true }).click();
    };
    await openCleanup();

    // Dedicated danger confirmation dialog: wording, layout, overlay.
    const dialog = cleanupDialog(page);
    await expect(dialog).toHaveCount(1, { timeout: 15000 });
    await expect(dialog.getByText(/删除《.*》的本地记录/)).toBeVisible();
    await expect(dialog.getByText(/下载器和媒体库中的文件不受影响/)).toBeVisible();
    await expect(dialog.getByText(/此操作无法撤销/)).toBeVisible();
    const confirmButton = dialog.getByRole("button", { name: "删除本地记录", exact: true });
    const cancelButton = dialog.getByRole("button", { name: "取消", exact: true });
    await expect(confirmButton).toBeVisible();
    await expect(cancelButton).toBeVisible();

    const viewport = page.viewportSize();
    expect(viewport).toBeTruthy();
    const box = await dialog.boundingBox();
    expect(box, "cleanup dialog must have a measurable box").toBeTruthy();
    expect(box!.width).toBeGreaterThan(300);
    expect(box!.height).toBeGreaterThan(140);
    expect(box!.width).toBeLessThanOrEqual(viewport!.width);
    expect(box!.height).toBeLessThanOrEqual(viewport!.height);
    // Horizontally centered and vertically on-screen: no off-screen/overlap trap.
    expect(Math.abs(box!.x + box!.width / 2 - viewport!.width / 2)).toBeLessThan(80);
    expect(box!.y).toBeGreaterThanOrEqual(0);
    expect(box!.y + box!.height).toBeLessThanOrEqual(viewport!.height + 1);
    // Overlay covers the viewport, so the destructive action cannot be mis-clicked
    // on the card underneath.
    // The element hit-tested at the viewport corner must be the full-viewport
    // backdrop, not the page underneath.
    const cornerHit = await page.evaluate(() => {
      const el = document.elementFromPoint(2, 2);
      if (!el) return null;
      const rect = el.getBoundingClientRect();
      return { position: getComputedStyle(el).position, width: rect.width, height: rect.height };
    });
    expect(cornerHit).toBeTruthy();
    expect(cornerHit!.position).toBe("fixed");
    expect(cornerHit!.width).toBeGreaterThanOrEqual(viewport!.width - 2);
    expect(cornerHit!.height).toBeGreaterThanOrEqual(viewport!.height - 2);
    await screenshot(page, "local-cleanup-confirmation-dialog.png");

    // Cancel must produce no cleanup request.
    let cleanupRequests = 0;
    page.on("request", (request) => {
      if (request.method() === "DELETE" && new URL(request.url()).pathname.endsWith("/local-data")) cleanupRequests += 1;
    });
    await cancelButton.click();
    await expect(cleanupDialog(page)).toHaveCount(0, { timeout: 15000 });
    await page.waitForTimeout(1500);
    expect(cleanupRequests, "cancelling the dialog must not call the cleanup API").toBe(0);
    const cancelCleanupRequests = cleanupRequests;
    await expect(card).toBeVisible();

    // Confirm: submit the exact retired epoch, then the instance leaves the
    // list. Background work created by the real chain is fenced by retirement
    // and cannot progress, but a transient scheduler tick may still be observed;
    // retry the dialog the way an operator would until the command is accepted.
    let cleanedStatus = 0;
    let cleanupPath = "";
    let cleanupBody: { expected_lifecycle_epoch?: number; confirmation?: string } = {};
    for (let attempt = 0; attempt < 8; attempt += 1) {
      await openCleanup();
      const confirmDialog = cleanupDialog(page);
      await expect(confirmDialog).toHaveCount(1, { timeout: 15000 });
      const cleanupResponse = page.waitForResponse(
        (response) => response.request().method() === "DELETE" && new URL(response.url()).pathname.endsWith("/local-data"),
        { timeout: 15000 },
      );
      await confirmDialog.getByRole("button", { name: "删除本地记录", exact: true }).click();
      const cleaned = await cleanupResponse;
      cleanedStatus = cleaned.status();
      cleanupPath = new URL(cleaned.url()).pathname;
      cleanupBody = JSON.parse(cleaned.request().postData() ?? "{}") as {
        expected_lifecycle_epoch?: number;
        confirmation?: string;
      };
      if (cleanedStatus === 204) break;
      expect(cleanedStatus).toBe(409);
      await expect(cleanupDialog(page)).toHaveCount(0, { timeout: 15000 });
      await page.waitForTimeout(5000);
    }
    expect(cleanedStatus).toBe(204);
    expect(cleanupPath).toContain(`/anime/${encodeURIComponent(owner.managedAnimeID)}/local-data`);
    expect(cleanupBody.confirmation).toBe("delete-local-data");
    expect(cleanupBody.expected_lifecycle_epoch).toBe(lifecycleEpoch);

    await expect(page.getByText(`已删除《${title}》的本地记录，下载器和媒体库中的文件保留`, { exact: true })).toBeVisible({ timeout: 15000 });
    await expect(sessionCard(page, title, instanceLabel)).toHaveCount(0, { timeout: 15000 });

    // The owner is gone from the list read model; its custom candidate is
    // preserved (detached), per design.
    await expect.poll(async () => (await readOwner(page, owner.managedAnimeID)) === undefined, { timeout: 15000 }).toBe(true);
    const candidateResponse = await page.request.get("/api/v1/anime-config-candidates?origin=custom");
    expect(candidateResponse.status()).toBe(200);
    const candidatePayload = await candidateResponse.json() as {
      candidates: Array<{
        id: string;
        origin: string;
        instance_label: string | null;
        status: string;
        activated_managed_anime_id: string | null;
        current_revision: number | null;
      }>;
    };
    const preserved = (candidatePayload.candidates ?? []).find((candidate) => candidate.instance_label === instanceLabel);
    expect(preserved, "the custom candidate must be preserved after local cleanup").toBeTruthy();
    expect(preserved!.activated_managed_anime_id).toBeNull();
    expect(preserved!.status).toBe("archived");
    expect(preserved!.current_revision).toBeGreaterThanOrEqual(1);

    // The 自定义 tab still shows the preserved (now detached) preset card.
    await page.getByRole("button", { name: "自定义", exact: true }).first().click();
    await expect(sessionCard(page, title, instanceLabel)).toBeVisible({ timeout: 15000 });
    await screenshot(page, "preserved-custom-candidate-after-cleanup.png");

    await writeCleanupEvidence({
      schema: "bansoko/local-cleanup-browser-evidence/v1",
      redacted: true,
      title,
      instance_label: instanceLabel,
      managed_anime_id: owner.managedAnimeID,
      subscription_id: owner.subscriptionID,
      lifecycle_epoch: lifecycleEpoch,
      candidate_id: preserved!.id,
      candidate_status: preserved!.status,
      candidate_current_revision: preserved!.current_revision,
      cleanup_status: cleanedStatus,
      cleanup_path: cleanupPath,
      cleanup_confirmation: cleanupBody.confirmation,
      cleanup_expected_lifecycle_epoch: cleanupBody.expected_lifecycle_epoch,
      cancel_cleanup_requests: cancelCleanupRequests,
      dialog_box: box,
      viewport,
      overlay_hit: cornerHit,
    });
  });
});
