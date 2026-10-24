import { test, expect, type Page } from "@playwright/test";

// Live evidence deliberately bypasses the fixture provider registry. The browser
// project supplies the real authenticated storage state from auth.setup.ts when
// the server requires auth; skip-auth development instances enter the shell
// without a token.

// 桌面顶栏是 button 页签；手机上「已订阅 / 发现」在底部标签栏，浏览分栏
// （AniBT / Mikan / 自定义）是「发现」之后页内的 Segmented（radio 语义）。
// PHONE_MEDIA_QUERY 是 (width <= 600px)，与 src/lib/useMediaQuery.ts 同步。
function isPhoneLayout(page: Page): boolean {
  return (page.viewportSize()?.width ?? 1280) <= 600;
}

async function selectBrowseSection(page: Page, label: "AniBT" | "Mikan") {
  if (isPhoneLayout(page)) {
    const segmented = page.getByRole("radiogroup", { name: "主导航" });
    if ((await segmented.count()) === 0) {
      await page.getByRole("navigation", { name: "底部导航" }).getByRole("button", { name: "发现", exact: true }).click();
    }
    await segmented.getByRole("radio", { name: label, exact: true }).click();
    return;
  }
  await page.getByRole("button", { name: label, exact: true }).click();
}

async function enterLive(page: Page) {
  await page.goto("/");
  if (isPhoneLayout(page)) {
    await expect(page.getByRole("navigation", { name: "底部导航" })).toBeVisible();
  } else {
    await expect(page.getByRole("button", { name: "AniBT", exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Mikan", exact: true })).toBeVisible();
  }
  await selectBrowseSection(page, "AniBT");
  await expect(page.getByRole("button", { name: "刷新 AniBT", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "刷新 AniBT", exact: true })).toBeEnabled();
}

function visibleCatalogStatus(page: Page) {
  // 快照的任何终态都要可见：fresh 渲染为「{n} 部 · …更新」，其余为过期/不可用/空态文案。
  return page
    .locator('[role="status"]:visible, [role="alert"]:visible')
    .filter({ hasText: /\d+ 部|站点数据已过期|站点暂不可用|暂无站点数据|快照已过期|该季度暂无番剧|没有匹配的番剧/ })
    .first();
}

test.describe.configure({ mode: "serial" });

test.describe("live provider smoke", () => {
  test.beforeEach(() => {
    test.skip(process.env.BANSOKO_E2E_FIXTURES === "1", "live provider evidence is separate from fixture UI tests");
  });

  test("reads real AniBT and Mikan catalog projections without fixture data", async ({ page }) => {
    const anibtResponse = page.waitForResponse((response) =>
      response.url().includes("/api/v1/catalog/providers/anibt/seasons/") && response.request().method() === "GET",
    );
    await enterLive(page);
    const anibt = await anibtResponse;
    expect(anibt.ok()).toBe(true);

    await expect(visibleCatalogStatus(page)).toBeVisible({ timeout: 10000 });
    await expect(page.getByText("AniBT", { exact: true }).first()).toBeVisible();

    const mikanResponse = page.waitForResponse((response) =>
      response.url().includes("/api/v1/catalog/providers/mikan/seasons/") && response.request().method() === "GET",
    );
    await selectBrowseSection(page, "Mikan");
    const mikan = await mikanResponse;
    expect(mikan.ok()).toBe(true);
    // Mikan may be unavailable in the test network. Either a real catalog or
    // the persisted provider error must be visible; fixture content is invalid.
    await expect(page.getByText("Mikan", { exact: true }).first()).toBeVisible();
    await expect(visibleCatalogStatus(page)).toBeVisible({ timeout: 10000 });
  });

  test("refreshes the real provider snapshot and renders the resulting state", async ({ page }) => {
    await enterLive(page);
    const refreshResponse = page.waitForResponse((response) =>
      response.url().includes("/api/v1/catalog/providers/anibt/seasons/") &&
      response.url().endsWith("/refresh") &&
      response.request().method() === "POST",
    );
    await page.getByRole("button", { name: "刷新 AniBT", exact: true }).click();
    const response = await refreshResponse;
    expect([200, 500]).toContain(response.status());
    await expect(visibleCatalogStatus(page)).toBeVisible({ timeout: 10000 });
  });

  test("does not overflow at the target mobile width", async ({ page, isMobile }) => {
    test.skip(!isMobile, "mobile-only layout check");
    await enterLive(page);
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);
  });
});
