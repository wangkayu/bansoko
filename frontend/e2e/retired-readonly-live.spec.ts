import { test, expect, type Locator, type Page } from "@playwright/test";
import type { AnimeWorkspace, RSSListView } from "../src/lib/api/client";
import { readDisposableSnapshot, readSessionFixtureIdentity } from "./disposable-db-snapshot";
import { shouldSkipAuthTests } from "./skip-auth-helper";
import { startFromSubscribedToast } from "./live-subscription";
import { closeCardMenu } from "./live-card-menu";
import { csrfToken } from "./session";

// retired 只读工作区的真实浏览器验收：真实订阅一个 custom-RSS 实例，真实刷新出
// 历史条目，然后通过 UI 退役它，最后重新打开同一个 workspace：
//   1. 退役卡的海报点击（「查看 <标题>」）打开的就是同一个 workspace 读模型；
//   2. 显式只读（已退役徽章 + 只读横幅），owner 链与执行记录入口照常可读；
//   3. 配置保存、刷新、扫描、重试、偏移全部不渲染；
//   4. 「删除本地记录」危险入口仍然可用（独立 dialog，不在此提交）。
//
// 该 spec 只通过真实 UI 与只读 API 观察；不直接改数据库终态，也不做任何清理写入。

const explicitBaseURL = process.env.BANSOKO_TEST_BASE_URL?.trim();
const fixtureRSSURL = process.env.BANSOKO_TEST_FIXTURE_RSS_URL?.trim();
const title = process.env.BANSOKO_BROWSER_FIXTURE_TITLE?.trim();
const groupName = process.env.BANSOKO_BROWSER_FIXTURE_GROUP?.trim();
const instanceLabel = process.env.BANSOKO_BROWSER_FIXTURE_INSTANCE_LABEL?.trim();

function livePrerequisite(): string | undefined {
  if (process.env.BANSOKO_E2E_FIXTURES === "1") {
    return "retired read-only live evidence does not run in fixture mode";
  }
  if (process.env.BANSOKO_TEST_DB_DISPOSABLE !== "1") {
    return "BANSOKO_TEST_DB_DISPOSABLE=1 is required: retirement is irreversible";
  }
  if (process.env.BANSOKO_E2E_REAL_EFFECT !== "1") {
    return "BANSOKO_E2E_REAL_EFFECT=1 is required: this suite retires a real instance";
  }
  if (!explicitBaseURL) return "BANSOKO_TEST_BASE_URL must explicitly point at the live authenticated server";
  if (!fixtureRSSURL) return "BANSOKO_TEST_FIXTURE_RSS_URL is required; no RSS source will be invented";
  if (!title || !groupName || !instanceLabel) {
    return "BANSOKO_BROWSER_FIXTURE_TITLE/GROUP/INSTANCE_LABEL are required so lookups stay attributable to this session";
  }
  return undefined;
}

function latestDialog(page: Page): Locator {
  return page.getByRole("dialog").last();
}

function sessionCard(page: Page, label: string): Locator {
  return page.getByRole("article").filter({ hasText: title! }).filter({ hasText: label });
}

async function sessionHeaders(page: Page, method: "GET" | "POST" = "GET"): Promise<Record<string, string>> {
  // The session is an HttpOnly cookie; the only header a script must supply is
  // the double-submit value on an unsafe request.
  return method === "GET" ? {} : { "X-CSRF-Token": await csrfToken(page) };
}

async function addCustomPreset(page: Page) {
  await page.getByRole("button", { name: "自定义", exact: true }).first().click();
  await page.getByRole("button", { name: "添加预设", exact: true }).first().click();

  const dialog = latestDialog(page);
  await expect(dialog.getByText("添加自定义预设", { exact: true })).toBeVisible();
  await dialog.getByLabel("标题", { exact: true }).fill(title!);
  await dialog.getByLabel("订阅备注", { exact: true }).fill(instanceLabel!);
  await dialog.getByLabel("RSS 地址", { exact: true }).fill(fixtureRSSURL!);
  await dialog.getByLabel("字幕组", { exact: true }).fill(groupName!);
  await dialog.getByRole("button", { name: "保存预设", exact: true }).click();
  await expect(page.getByText("自定义预设已保存", { exact: true })).toBeVisible({ timeout: 15000 });
}

async function openAndSubscribe(page: Page) {
  const card = sessionCard(page, instanceLabel!);
  await expect(card).toBeVisible({ timeout: 15000 });
  await card.getByRole("button", { name: `打开配置 ${title}`, exact: true }).click();

  const dialog = latestDialog(page);
  await expect(dialog.getByRole("tab", { name: "RSS 预览", exact: true })).toBeVisible({ timeout: 15000 });
  // 预设保存已经写入候选与不可变修订；只有在草稿确实有未保存修改时才再保存一次。
  const saveButton = dialog.getByRole("button", { name: "保存配置", exact: true });
  if (await saveButton.isEnabled()) {
    await saveButton.click();
    await expect(dialog.getByText("已保存", { exact: true })).toBeVisible({ timeout: 15000 });
  }
  await expect(dialog.getByRole("button", { name: "订阅", exact: true })).toBeEnabled();

  const subscribeResponse = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname.includes("/api/v1/anime-config-candidates/") &&
      new URL(response.url()).pathname.endsWith("/subscribe") &&
      response.request().method() === "POST",
    { timeout: 15000 },
  );
  await dialog.getByRole("button", { name: "订阅", exact: true }).click();
  const subscribed = await subscribeResponse;
  expect([200, 201]).toContain(subscribed.status());
  const subscribedBody = await subscribed.json() as { managed_anime?: { id?: string } };
  const managedAnimeID = subscribedBody.managed_anime?.id;
  expect(managedAnimeID).toBeTruthy();
  await expect(dialog).toBeHidden({ timeout: 15000 });
  await startFromSubscribedToast(page, managedAnimeID!);
  return managedAnimeID!;
}

async function refreshUntilRelease(page: Page, managedAnimeID: string): Promise<{ workspace: Locator; releaseID: string; sourceID: string; releaseTitle: string }> {
  const fixture = await readSessionFixtureIdentity();
  await page.getByRole("button", { name: "已订阅", exact: true }).click();
  const card = sessionCard(page, instanceLabel!);
  await expect(card).toBeVisible({ timeout: 30000 });
  await card.getByRole("button", { name: `打开配置 ${title}`, exact: true }).click();
  const workspace = latestDialog(page);
  await expect(workspace.getByRole("tab", { name: "RSS", exact: true })).toBeVisible({ timeout: 30000 });
  await workspace.getByRole("tab", { name: "RSS", exact: true }).click();
  await expect(workspace.getByRole("tab", { name: "RSS", exact: true })).toHaveAttribute("aria-selected", "true", { timeout: 30000 });
  for (let attempt = 0; attempt < 20; attempt += 1) {
    const refresh = workspace.getByRole("button", { name: "刷新 RSS", exact: true });
    if (await refresh.isVisible().catch(() => false)) {
      const refreshResponse = page.waitForResponse((r) => new URL(r.url()).pathname.includes("/api/v1/rss-sources/") && new URL(r.url()).pathname.endsWith("/refresh") && r.request().method() === "POST", { timeout: 15000 });
      await refresh.click();
      expect([200, 202, 409]).toContain((await refreshResponse).status());
    }
    const group = workspace.getByRole("button", { name: new RegExp(`^(?:展开|折叠) ${groupName!.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`) });
    if (await group.count()) {
      if (await group.getAttribute("aria-expanded") !== "true") await group.click();
      const state = await page.request.get(`/api/v1/anime/${managedAnimeID}/workspace`, { headers: await sessionHeaders(page) });
      expect(state.status()).toBe(200);
      const body = await state.json() as AnimeWorkspace;
      const matches = body.releases.filter((r) => r.raw_facts.title === fixture.title && r.stable_item_key === fixture.stable_item_key && body.rss_sources.some((s) => s.id === r.rss_source_id));
      if (matches.length === 0) { await page.waitForTimeout(1000); continue; }
      expect(matches).toHaveLength(1);
      const exactSeed = workspace.locator("li.seed-row").filter({ has: page.getByText(fixture.title, { exact: true }) });
      await expect(exactSeed).toHaveCount(1, { timeout: 20000 });
      // 种子仍在等待元数据，没有受理获取，所以行上没有「查看处理过程」。
      await expect(exactSeed.getByRole("button", { name: "查看处理过程", exact: true })).toHaveCount(0);
      return { workspace, releaseID: matches[0].id, sourceID: matches[0].rss_source_id, releaseTitle: fixture.title };
    }
    await page.waitForTimeout(1000);
  }
  throw new Error("RSS refresh never produced the exact fixture release");
}

async function retire(page: Page, workspace: Locator) {
  await workspace.getByRole("button", { name: "关闭", exact: true }).last().click();
  await expect(workspace).toBeHidden({ timeout: 15000 });

  // Only a CAS conflict is a supported retry; a server error stays red.
  let lastStatus = 0;
  for (let attempt = 0; attempt < 4; attempt += 1) {
    const card = sessionCard(page, instanceLabel!);
    // 管理动作收在卡片右上 ⋯ 菜单里（海报墙 UI-038）。
    await expect(card.getByRole("button", { name: `更多操作 ${title}`, exact: true })).toBeVisible({ timeout: 20000 });
    await card.getByRole("button", { name: `更多操作 ${title}`, exact: true }).click();
    const entry = page.getByRole("menuitem", { name: "结束订阅", exact: true });
    await expect(entry).toBeVisible({ timeout: 20000 });
    await entry.click();
    const confirmDialog = page.getByRole("alertdialog");
    await expect(confirmDialog.getByText(`结束订阅《${title}》？`, { exact: true })).toBeVisible({ timeout: 15000 });
    await expect(confirmDialog.getByText("将结束该订阅，停止自动刷新、评估和下载", { exact: true })).toBeVisible();
    await expect(confirmDialog.getByText(/下载器中的种子和媒体库文件都会保留/)).toBeVisible();

    const deleteResponse = page.waitForResponse(
      (response) =>
        response.request().method() === "DELETE" && new URL(response.url()).pathname.endsWith("/subscription"),
      { timeout: 20000 },
    );
    await confirmDialog.getByRole("button", { name: "结束订阅", exact: true }).click();
    const deleteResult = await deleteResponse;
    lastStatus = deleteResult.status();
    if (lastStatus === 204) {
      await expect(page.getByText(`已结束订阅《${title}》`, { exact: true })).toBeVisible({ timeout: 15000 });
      return;
    }
    expect(lastStatus, "retirement server errors must not become retry-green").toBe(409);
    await page.waitForTimeout(2000);
  }
  throw new Error(`retirement never committed; last DELETE status=${lastStatus}`);
}

test.use({ trace: "off", video: "off", screenshot: "off" });
test.describe.configure({ mode: "serial" });

test.describe("retired instance read-only workspace", () => {
  test.describe.configure({ retries: 0 });

  test.beforeEach(({ isMobile }) => {
    test.skip(isMobile, "the destructive retirement evidence runs once on desktop");
    test.skip(shouldSkipAuthTests(), "auth.setup did not receive BANSOKO_E2E_USERNAME/BANSOKO_E2E_PASSWORD");
    const reason = livePrerequisite();
    test.skip(reason !== undefined, reason);
  });

  test("retire a live instance, then reopen its history read-only", async ({ page }) => {
    test.setTimeout(420000);
    await page.goto("/");
    await expect(page.getByRole("navigation", { name: "主导航" })).toBeVisible({ timeout: 15000 });
    await expect(page.getByRole("button", { name: "已订阅", exact: true }).first()).toBeVisible();

    await addCustomPreset(page);
    const managedAnimeID = await openAndSubscribe(page);
    const beforeRetirement = await page.request.get(`/api/v1/anime/${managedAnimeID}/workspace`, { headers: await sessionHeaders(page) });
    expect(beforeRetirement.status()).toBe(200);
    const workspaceIdentity = await beforeRetirement.json() as AnimeWorkspace;
    expect(workspaceIdentity.managed_anime.id).toBe(managedAnimeID);
    const evidence = await refreshUntilRelease(page, managedAnimeID);
    const releaseID = evidence.releaseID;
    const sourceID = evidence.sourceID;
    const ownerSnapshot = await page.request.post("/api/v1/anime/rss-list-view", { headers: await sessionHeaders(page, "POST"), data: { input_source: "owner", managed_anime_id: managedAnimeID, limit_per_group: 100 } });
    expect(ownerSnapshot.status()).toBe(200);
    const ownerView = await ownerSnapshot.json() as RSSListView;
    expect(ownerView.groups?.some((g) => g.scope.rss_source_id === sourceID && g.items?.some((i) => i.snapshot_id === `release:${releaseID}`))).toBe(true);
    const originalItem = ownerView.groups.find((g) => g.scope.rss_source_id === sourceID)?.items.find((i) => i.snapshot_id === `release:${releaseID}`);
    expect(originalItem?.seed.title).toBe(evidence.releaseTitle);
    const beforeRetirementResponse = await page.request.get(`/api/v1/anime/${managedAnimeID}/workspace`, { headers: await sessionHeaders(page) });
    expect(beforeRetirementResponse.status()).toBe(200);
    const beforeRetirementState = await beforeRetirementResponse.json() as typeof workspaceIdentity;
    expect(beforeRetirementState.releases.filter((r) => r.id === releaseID && r.rss_source_id === sourceID)).toHaveLength(1);
    expect(beforeRetirementState.releases.find((r) => r.id === releaseID)?.verdict).toBe("waiting_metadata");
    expect(beforeRetirementState.metadata_hold.held).toBe(true);
    const target = { managed_id: managedAnimeID, source_id: sourceID, release_id: releaseID };
    await readDisposableSnapshot(target); // rejects absent/mismatched DB scope
    // 等待元数据的种子没有执行记录：Release 不带 acquisition_id，按番剧筛选的记录列表为空。
    expect(beforeRetirementState.releases.find((r) => r.id === releaseID)?.acquisition_id ?? null).toBeNull();
    const recordsBefore = await page.request.get(`/api/v1/acquisitions?managed_anime_id=${managedAnimeID}`, { headers: await sessionHeaders(page) });
    expect(recordsBefore.status()).toBe(200);
    expect(((await recordsBefore.json()) as { items: unknown[] }).items).toHaveLength(0);
    await retire(page, evidence.workspace);
    const afterRetirementResponse = await page.request.get(`/api/v1/anime/${managedAnimeID}/workspace`, { headers: await sessionHeaders(page) });
    expect(afterRetirementResponse.status()).toBe(200);
    const afterRetirement = await afterRetirementResponse.json() as typeof workspaceIdentity;
    expect(afterRetirement.managed_anime.id).toBe(managedAnimeID);
    expect(afterRetirement.releases.some((r) => r.id === releaseID && r.rss_source_id === sourceID)).toBe(true);
    expect(afterRetirement.subscription.id).toBe(beforeRetirementState.subscription.id);
    expect(afterRetirement.subscription.version).toBe(beforeRetirementState.subscription.version);
    expect(afterRetirement.subscription.lifecycle_state).toBe("retired");
    await readDisposableSnapshot(target); // same persisted Release scope after retirement
    expect(afterRetirement.subscription.lifecycle_epoch).toBeGreaterThan(beforeRetirementState.subscription.lifecycle_epoch);
    expect(afterRetirement.releases.find((r) => r.id === releaseID)?.raw_facts.title).toBe(evidence.releaseTitle);
    const retiredOwnerResponse = await page.request.post("/api/v1/anime/rss-list-view", { headers: await sessionHeaders(page, "POST"), data: { input_source: "owner", managed_anime_id: managedAnimeID, limit_per_group: 100 } });
    expect(retiredOwnerResponse.status()).toBe(200);
    const retiredOwnerView = await retiredOwnerResponse.json() as typeof ownerView;
    expect(retiredOwnerView.groups?.some((g) => g.scope.rss_source_id === sourceID && g.items?.some((i) => i.snapshot_id === `release:${releaseID}`))).toBe(true);
    const retiredItem = retiredOwnerView.groups.find((g) => g.scope.rss_source_id === sourceID)?.items.find((i) => i.snapshot_id === `release:${releaseID}`);
    expect(retiredItem?.seed.title).toBe(originalItem?.seed.title);
    expect(retiredItem?.stable_seed_key).toBe(originalItem?.stable_seed_key);
    expect(retiredOwnerView.metadata_hold.held).toBe(true);

    // 退役卡：不再有配置/停止/退役入口；⋯ 菜单只剩清理入口，只读查看走海报点击。
    const retiredCard = sessionCard(page, instanceLabel!);
    await expect(retiredCard.getByText("已结束", { exact: true })).toBeVisible({ timeout: 15000 });
    await retiredCard.getByRole("button", { name: `更多操作 ${title}`, exact: true }).click();
    await expect(page.getByRole("menuitem", { name: "删除本地记录", exact: true })).toBeVisible();
    await expect(page.getByRole("menuitem", { name: "停止", exact: true })).toHaveCount(0);
    await expect(page.getByRole("menuitem", { name: "启动", exact: true })).toHaveCount(0);
    await expect(page.getByRole("menuitem", { name: "结束订阅", exact: true })).toHaveCount(0);
    await expect(retiredCard.getByRole("button", { name: `打开配置 ${title}`, exact: true })).toHaveCount(0);

    // 打开同一个 workspace 的只读模式。
    await closeCardMenu(page);
    await retiredCard.getByRole("button", { name: `查看 ${title}`, exact: true }).click();
    const readOnly = latestDialog(page);
    await expect(readOnly.getByRole("tab", { name: "RSS", exact: true })).toBeVisible({ timeout: 30000 });
    await expect(readOnly.getByText("已结束", { exact: true }).first()).toBeVisible({ timeout: 15000 });
    await expect(readOnly.getByText(/当前为只读查看/)).toBeVisible();

    // owner 链（历史）与处理过程入口仍在，mutation 入口一个都没有。
    const group = readOnly.getByRole("button", { name: `展开 ${groupName}`, exact: true });
    await expect(group).toBeVisible({ timeout: 20000 });
    await group.click();
    const release = readOnly.locator("li.seed-row").filter({ has: page.getByText(evidence.releaseTitle, { exact: true }) });
    await expect(release).toBeVisible({ timeout: 20000 });
    await expect(release).toHaveCount(1);
    await expect(release.getByText(evidence.releaseTitle, { exact: true })).toBeVisible();
    await expect(release.getByRole("button", { name: "查看处理过程", exact: true })).toHaveCount(0);
    await expect(readOnly.getByRole("button", { name: "刷新 RSS", exact: true })).toHaveCount(0);
    await expect(readOnly.getByRole("button", { name: "扫描媒体库", exact: true })).toHaveCount(0);
    await expect(readOnly.getByRole("button", { name: "重试", exact: true })).toHaveCount(0);
    await expect(readOnly.getByRole("button", { name: /重试刷新|重试下载|重试校验|重试入库/ })).toHaveCount(0);
    await expect(readOnly.getByRole("button", { name: /调整 .* 偏移/ })).toHaveCount(0);

    // 执行记录入口在只读工作区照常可见（只读，不在此打开以免离开本弹窗）；
    // 退役不产生也不改写任何执行记录。
    await expect(readOnly.locator(".anime-config-dialog-footer-owner").getByRole("button", { name: "执行记录", exact: true })).toBeVisible();
    const recordsAfter = await page.request.get(`/api/v1/acquisitions?managed_anime_id=${managedAnimeID}`, { headers: await sessionHeaders(page) });
    expect(recordsAfter.status()).toBe(200);
    expect(((await recordsAfter.json()) as { items: unknown[] }).items).toHaveLength(0);

    // 配置 tab 只读：没有保存/匹配入口。
    await readOnly.getByRole("tab", { name: "元数据", exact: true }).click();
    await expect(readOnly.getByRole("button", { name: "编辑标题", exact: true })).toHaveCount(0);
    await expect(readOnly.getByRole("button", { name: "保存", exact: true })).toHaveCount(0);
    await readOnly.getByRole("tab", { name: "字幕组", exact: true }).click();
    await expect(readOnly.getByText("字幕组设置只读", { exact: true })).toBeVisible();
    await readOnly.getByRole("tab", { name: "规则", exact: true }).click();
    await expect(readOnly.getByText("规则只读", { exact: true })).toBeVisible();
    await expect(readOnly.getByRole("button", { name: "预览规则", exact: true })).toHaveCount(0);

    // 清理入口仍在，并且仍是独立的危险确认框（只打开，不提交）。
    await readOnly.getByRole("button", { name: "关闭", exact: true }).last().click();
    await expect(readOnly).toBeHidden({ timeout: 15000 });
    await retiredCard.getByRole("button", { name: `更多操作 ${title}`, exact: true }).click();
    await page.getByRole("menuitem", { name: "删除本地记录", exact: true }).click();
    const cleanup = page.getByRole("alertdialog");
    await expect(cleanup.getByText(/下载器和媒体库中的文件不受影响/)).toBeVisible({ timeout: 15000 });
    await cleanup.getByRole("button", { name: "取消", exact: true }).click();
  });
});
