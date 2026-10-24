import { test, expect } from "@playwright/test";
import { shouldSkipAuthTests } from "./skip-auth-helper";

// 390px 下的外壳：手机上没有顶部「主导航」，「已订阅 / 发现 / 更多」在底部标签栏
// （「下载」仅在配置了 qBittorrent 时出现），搜索是顶栏图标按钮，日志、设置等
// 收在「更多」底部面板里。「主导航」只剩「发现」之后页内的浏览分栏分段控件。

test.describe("mobile layout", () => {
  test.skip(shouldSkipAuthTests(), "BANSOKO_E2E_USERNAME and BANSOKO_E2E_PASSWORD are not set");

  test.beforeEach(async ({ page }) => {
    test.skip((page.viewportSize()?.width ?? 0) >= 768, "只在 390px mobile project 下跑");
    await page.goto("/");
  });

  test("底部标签栏与顶栏搜索在 390px 下仍可用", async ({ page }) => {
    const tabbar = page.getByRole("navigation", { name: "底部导航" });
    await expect(tabbar).toBeVisible({ timeout: 10000 });
    await expect(tabbar.getByRole("button", { name: "已订阅", exact: true })).toBeVisible();
    await expect(tabbar.getByRole("button", { name: "发现", exact: true })).toBeVisible();
    // 「更多」可能带未读数徽标（可访问名如「更多 1」），按前缀匹配。
    await expect(tabbar.getByRole("button", { name: /^更多/ })).toBeVisible();
    await expect(page.getByRole("button", { name: "搜索番剧", exact: true })).toBeVisible();
    // 手机上没有桌面顶部导航。
    await expect(page.getByRole("navigation", { name: "主导航" })).toHaveCount(0);
  });

  test("「更多」面板收着日志与设置", async ({ page }) => {
    await page.getByRole("navigation", { name: "底部导航" }).getByRole("button", { name: /^更多/ }).click();
    const sheet = page.getByRole("dialog", { name: "更多" });
    await expect(sheet).toBeVisible({ timeout: 10000 });
    await expect(sheet.getByRole("button", { name: "日志", exact: true })).toBeVisible();
    await expect(sheet.getByRole("button", { name: "设置", exact: true })).toBeVisible();
  });

  test("从「更多」打开设置，得到覆盖在目录上的 dialog", async ({ page }) => {
    await page.getByRole("navigation", { name: "底部导航" }).getByRole("button", { name: /^更多/ }).click();
    await page.getByRole("dialog", { name: "更多" }).getByRole("button", { name: "设置", exact: true }).click();
    await expect(page.getByRole("dialog", { name: "设置" })).toBeVisible({ timeout: 10000 });
    // 手机上从入口先看到分区列表（两级导航的第一级）。
    await expect(page.getByRole("navigation", { name: "设置分类" })).toBeVisible();
  });
});
