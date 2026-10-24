import { test, expect, type Page } from "@playwright/test";

// auth gate 规格只适用于强制认证的 live 实例：skip-auth 开发实例的匿名 probe
// 会直接进 shell（无闸门），此时按前提不成立跳过。

const GATE_TITLE = /创建管理员|^登录$/;

/** 等 probe 出结果：返回 "gate"（出现认证闸门）或 "shell"（skip-auth 直接进壳）。 */
async function probeOutcome(page: Page): Promise<"gate" | "shell"> {
  const gate = page.locator("h1").filter({ hasText: GATE_TITLE }).first();
  const shell = page.getByRole("navigation", { name: "主导航" });
  return Promise.race([
    gate.waitFor({ state: "visible", timeout: 10000 }).then(() => "gate" as const),
    shell.waitFor({ state: "visible", timeout: 10000 }).then(() => "shell" as const),
  ]);
}

test.describe("auth gate", () => {
  test.beforeEach(() => {
    test.skip(process.env.BANSOKO_E2E_FIXTURES === "1", "auth gate uses the live bootstrap API");
  });

  test("shows the bootstrap or login gate when not authenticated", async ({ page }) => {
    await page.goto("/");
    if ((await probeOutcome(page)) === "shell") {
      test.skip(true, "Instance runs in skip-auth development mode; no auth gate");
    }
    // The gate title is either "创建管理员" (bootstrap) or "登录" (ready).
    await expect(page.locator("h1").filter({ hasText: GATE_TITLE }).first()).toBeVisible();
  });

  test("login form has username and password fields", async ({ page }) => {
    await page.goto("/");
    if ((await probeOutcome(page)) === "shell") {
      test.skip(true, "Instance runs in skip-auth development mode; no auth gate");
    }
    const gateTitle = page.locator("h1").filter({ hasText: GATE_TITLE }).first();
    await expect(gateTitle).toBeVisible();
    const title = await gateTitle.textContent();
    if (title?.includes("创建管理员")) {
      test.skip(true, "Instance is not bootstrapped yet");
    }
    await expect(page.getByLabel("用户名")).toBeVisible();
    await expect(page.locator("input[type='password']").first()).toBeVisible();
    await expect(page.getByRole("button", { name: "登录" })).toBeVisible();
  });
});
