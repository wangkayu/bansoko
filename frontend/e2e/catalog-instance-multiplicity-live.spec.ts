import { test, expect, type Locator, type Page } from "@playwright/test";
import { shouldSkipAuthTests } from "./skip-auth-helper";
import { closeCardMenu } from "./live-card-menu";

const baseURL = process.env.BANSOKO_TEST_BASE_URL?.trim();
const fixtureRSSURL = process.env.BANSOKO_TEST_FIXTURE_RSS_URL?.trim();
const fixtureTitle = process.env.BANSOKO_BROWSER_FIXTURE_TITLE?.trim();
const fixtureGroup = process.env.BANSOKO_BROWSER_FIXTURE_GROUP?.trim();
const fixtureLabel = process.env.BANSOKO_BROWSER_FIXTURE_INSTANCE_LABEL?.trim();

function livePrerequisite(): string | undefined {
  if (process.env.BANSOKO_E2E_FIXTURES === "1") return "catalog owner associations require the authenticated server";
  if (process.env.BANSOKO_TEST_DB_DISPOSABLE !== "1") {
    return "BANSOKO_TEST_DB_DISPOSABLE=1 is required for disposable owner-association evidence";
  }
  if (!baseURL) return "BANSOKO_TEST_BASE_URL must point at the authorized disposable server";
  if (!fixtureRSSURL) return "BANSOKO_TEST_FIXTURE_RSS_URL is required";
  if (!fixtureTitle || !fixtureGroup || !fixtureLabel) {
    return "BANSOKO_BROWSER_FIXTURE_TITLE, BANSOKO_BROWSER_FIXTURE_GROUP, and BANSOKO_BROWSER_FIXTURE_INSTANCE_LABEL are required";
  }
  try {
    const parsed = new URL(fixtureRSSURL);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      return "BANSOKO_TEST_FIXTURE_RSS_URL must be an http(s) URL";
    }
  } catch {
    return "BANSOKO_TEST_FIXTURE_RSS_URL must be a valid URL";
  }
  return undefined;
}

function card(page: Page, title: string, label: string): Locator {
  return page.getByRole("article").filter({ hasText: title }).filter({ hasText: label });
}

// 已订阅卡的管理动作收在右上 ⋯ 菜单里（10-19 卡片重设计），卡面只有整卡
// 打开配置一个目标；菜单项是不带标题的短文案。
async function cardMenuItem(page: Page, card: Locator, title: string, item: string): Promise<void> {
  await card.getByRole("button", { name: `更多操作 ${title}`, exact: true }).click();
  await page.getByRole("menuitem", { name: item, exact: true }).click();
}

async function addPreset(page: Page, title: string, label: string): Promise<Locator> {
  await page.getByRole("button", { name: "自定义", exact: true }).first().click();
  await page.getByRole("button", { name: "添加预设", exact: true }).first().click();

  const dialog = page.getByRole("dialog").last();
  await expect(dialog.getByText("添加自定义预设", { exact: true })).toBeVisible();
  await dialog.getByLabel("标题", { exact: true }).fill(title);
  await dialog.getByLabel("订阅备注", { exact: true }).fill(label);
  await dialog.getByLabel("RSS 地址", { exact: true }).fill(fixtureRSSURL!);
  await dialog.getByLabel("字幕组", { exact: true }).fill(fixtureGroup!);
  await dialog.getByRole("button", { name: "保存预设", exact: true }).click();
  await expect(page.getByText("自定义预设已保存", { exact: true })).toBeVisible({ timeout: 15000 });

  const result = card(page, title, label);
  await expect(result).toBeVisible({ timeout: 15000 });
  return result;
}

type SubscribeOwner = {
  managedAnimeID: string;
  subscriptionID: string;
};

async function subscribePreset(page: Page, target: Locator, title: string): Promise<SubscribeOwner> {
  const responsePromise = page.waitForResponse(
    (response) => {
      const pathname = new URL(response.url()).pathname;
      return pathname.includes("/api/v1/anime-config-candidates/") && pathname.endsWith("/subscribe") && response.request().method() === "POST";
    },
    { timeout: 15000 },
  );
  // 卡面没有订阅按钮：点卡片打开预设草稿，在弹窗里订阅。
  await target.getByRole("button", { name: `打开配置 ${title}`, exact: true }).dispatchEvent("click");
  const draft = page.getByRole("dialog").last();
  await expect(draft.getByRole("button", { name: "订阅", exact: true })).toBeEnabled({ timeout: 15000 });
  await draft.getByRole("button", { name: "订阅", exact: true }).click();
  const response = await responsePromise;
  expect([200, 201]).toContain(response.status());
  const body = await response.json() as {
    managed_anime: { id: string };
    subscription: { id: string; managed_anime_id: string };
  };
  expect(body.managed_anime.id).toBeTruthy();
  expect(body.subscription.id).toBeTruthy();
  expect(body.subscription.managed_anime_id).toBe(body.managed_anime.id);
  await expect(page.getByText(`已添加《${title}》，尚未开始`, { exact: true })).toBeVisible({ timeout: 15000 });
  return {
    managedAnimeID: body.managed_anime.id,
    subscriptionID: body.subscription.id,
  };
}

test.describe.configure({ mode: "serial" });

test.describe("catalog owner association collisions (authenticated disposable)", () => {
  test.beforeEach(() => {
    test.skip(shouldSkipAuthTests(), "auth.setup did not receive BANSOKO_E2E_USERNAME/BANSOKO_E2E_PASSWORD");
    const reason = livePrerequisite();
    test.skip(reason !== undefined, reason);
  });

  test("duplicate custom RSS locator keeps distinct managed-owner actions exact", async ({ page }) => {
    test.setTimeout(60000);
    const firstTitle = `${fixtureTitle} owner association first`;
    const secondTitle = `${fixtureTitle} owner association second`;
    const firstLabel = `${fixtureLabel} first`;
    const secondLabel = `${fixtureLabel} second`;

    await page.goto("/");
    await expect(page.getByRole("button", { name: "已订阅", exact: true }).first()).toBeVisible({ timeout: 15000 });

    const firstPreset = await addPreset(page, firstTitle, firstLabel);
    const firstOwner = await subscribePreset(page, firstPreset, firstTitle);
    const secondPreset = await addPreset(page, secondTitle, secondLabel);
    const secondOwner = await subscribePreset(page, secondPreset, secondTitle);
    expect(secondOwner.managedAnimeID).not.toBe(firstOwner.managedAnimeID);
    expect(secondOwner.subscriptionID).not.toBe(firstOwner.subscriptionID);

    await page.getByRole("button", { name: "已订阅", exact: true }).first().click();
    const firstCard = card(page, firstTitle, firstLabel);
    const secondCard = card(page, secondTitle, secondLabel);
    await expect(firstCard).toBeVisible({ timeout: 15000 });
    await expect(secondCard).toBeVisible({ timeout: 15000 });

    const deleteResponse = page.waitForResponse(
      (response) => response.request().method() === "DELETE" && new URL(response.url()).pathname.endsWith("/subscription"),
      { timeout: 15000 },
    );
    await cardMenuItem(page, secondCard, secondTitle, "结束订阅");
    await page.getByRole("button", { name: "结束订阅", exact: true }).click();
    const deleted = await deleteResponse;
    expect(deleted.status()).toBe(204);
    const deletedPath = new URL(deleted.url()).pathname;
    expect(deletedPath).toContain(`/anime/${encodeURIComponent(secondOwner.managedAnimeID)}/subscription`);
    expect(deletedPath).not.toContain(encodeURIComponent(firstOwner.managedAnimeID));

    await expect(page.getByText(`已结束订阅《${secondTitle}》`, { exact: true })).toBeVisible({ timeout: 15000 });

    // Retirement is not deletion: the instance stays in the library for
    // historical inspection, is unmistakably marked 已退役, and exposes no
    // mutation entry — no start/stop, no config (poster/title targets included)
    // and no unsubscribe. The sibling active owner is untouched.
    await expect(secondCard).toBeVisible({ timeout: 15000 });
    await expect(secondCard.getByText("已结束", { exact: true })).toBeVisible({ timeout: 15000 });
    // 退役卡仍可打开管理菜单（清理本地记录；只读查看走海报），但不再提供停止/启动与结束订阅。
    await secondCard.getByRole("button", { name: `更多操作 ${secondTitle}`, exact: true }).click();
    await expect(page.getByRole("menuitem", { name: "删除本地记录", exact: true })).toBeVisible();
    await expect(page.getByRole("menuitem", { name: "停止", exact: true })).toHaveCount(0);
    await expect(page.getByRole("menuitem", { name: "启动", exact: true })).toHaveCount(0);
    await expect(page.getByRole("menuitem", { name: "结束订阅", exact: true })).toHaveCount(0);
    await closeCardMenu(page);
    await expect(secondCard.getByRole("button", { name: `打开配置 ${secondTitle}`, exact: true })).toHaveCount(0);
    await expect(secondCard.getByRole("button", { name: `查看 ${secondTitle}`, exact: true })).toBeVisible();

    await expect(firstCard).toBeVisible({ timeout: 15000 });
    await expect(firstCard.getByText("已结束", { exact: true })).toHaveCount(0);
    await expect(firstCard.getByRole("button", { name: `更多操作 ${firstTitle}`, exact: true })).toBeVisible();
    await firstCard.getByRole("button", { name: `更多操作 ${firstTitle}`, exact: true }).click();
    await expect(page.getByRole("menuitem", { name: "结束订阅", exact: true })).toBeVisible();
    await closeCardMenu(page);

    // The read model keeps the retired owner's lifecycle boundary separate from
    // the reversible Subscription state. The session is an HttpOnly cookie, so
    // this same-context request is authenticated with no header at all.
    const retiredListResponse = await page.request.get("/api/v1/anime");
    expect(retiredListResponse.status()).toBe(200);
    const retiredItems = await retiredListResponse.json() as Array<{
      managed_anime: { id: string };
      subscription: { state: string; lifecycle_state: string; lifecycle_epoch: number };
    }>;
    const retiredItem = retiredItems.find((item) => item.managed_anime.id === secondOwner.managedAnimeID);
    expect(retiredItem).toBeTruthy();
    expect(retiredItem!.subscription.lifecycle_state).toBe("retired");
    expect(retiredItem!.subscription.lifecycle_epoch).toBeGreaterThanOrEqual(2);
    expect(retiredItem!.subscription.state).not.toBe("retired");
  });
});
