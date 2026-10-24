import { test, expect } from "@playwright/test";
import { shouldSkipAuthTests } from "./skip-auth-helper";

// P2.3 authenticated real UX evidence: the automation and notification
// surfaces are exercised against a real auth-enabled server with the real
// generated client — no route fulfillment, no fixture substitution.
test.describe("live settings closure", () => {
  test.beforeEach(() => {
    test.skip(process.env.BANSOKO_E2E_FIXTURES === "1", "live settings evidence requires the auth-enabled server");
  });

  test("automation schedules are readable with the three fixed kinds", async ({ page }) => {
    await page.goto("/#/settings/automation");
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible({ timeout: 10000 });
    // 真实 API 返回三类固定 schedule（kind 由后端契约锁定）。
    await expect(dialog.getByText("字幕组发现", { exact: false }).first()).toBeVisible({ timeout: 10000 });
    await expect(dialog.getByText("RSS 刷新", { exact: false }).first()).toBeVisible();
    await expect(dialog.getByText("元数据自动匹配", { exact: false }).first()).toBeVisible();
  });

  test("notification section loads the target surface from the real API", async ({ page }) => {
    await page.goto("/#/settings/notifications");
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible({ timeout: 10000 });
    await expect(dialog.getByRole("button", { name: "新建渠道" })).toBeVisible({ timeout: 10000 });
  });

  test("manual run request is accepted by the automation API", async ({ page }) => {
    await page.goto("/#/settings/automation");
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible({ timeout: 10000 });
    // 找到“立即执行”按钮并点击：请求被接受（admission），不冒充下游完成。
    const runButtons = dialog.getByRole("button", { name: /立即执行|立即运行/ });
    await expect(runButtons.first()).toBeVisible({ timeout: 10000 });
    const accepted = page.waitForResponse(
      (response) => response.url().includes("/automation/") && response.url().includes("/run") && response.request().method() === "POST",
      { timeout: 10000 },
    );
    await runButtons.first().click();
    const response = await accepted;
    expect([200, 202]).toContain(response.status());
  });
});
