import { test as setup, expect } from "@playwright/test";
import path from "node:path";
import fs from "node:fs";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
// Disposable browser sessions redirect both artifacts to a private
// per-session directory via BANSOKO_E2E_AUTH_STATE_PATH / BANSOKO_E2E_AUTH_MARKER_PATH
// (absolute paths from scripts/verify/real-effect-browser-session.sh) so
// concurrent sessions never share one storage state. Defaults keep the
// historical shared paths for fixture/default lanes.
const authFile =
  process.env.BANSOKO_E2E_AUTH_STATE_PATH ||
  path.join(__dirname, "../playwright/.auth/state.json");
const markerFile =
  process.env.BANSOKO_E2E_AUTH_MARKER_PATH ||
  path.join(__dirname, "../playwright/.auth/skip-auth");

const username = process.env.BANSOKO_E2E_USERNAME;
const password = process.env.BANSOKO_E2E_PASSWORD;

setup("authenticate", async ({ page, baseURL }) => {
  // Ensure both artifact directories exist (covers redirected session paths
  // whose parents may not exist yet).
  fs.mkdirSync(path.dirname(authFile), { recursive: true });
  fs.mkdirSync(path.dirname(markerFile), { recursive: true });

  // Fixture browser lane (BANSOKO_E2E_FIXTURES=1): fixture-only specs run against
  // the static fixture dev server with no backend and no auth gate. Clear the
  // skip marker, hand out an empty storage state, and return without touching
  // the network or any credentials.
  if (process.env.BANSOKO_E2E_FIXTURES === "1") {
    fs.rmSync(markerFile, { force: true });
    fs.writeFileSync(authFile, JSON.stringify({ cookies: [], origins: [] }));
    return;
  }

  if (!username || !password) {
    // No credentials: write an empty auth state and a marker so dependent tests
    // can skip instead of failing.
    fs.writeFileSync(authFile, JSON.stringify({ cookies: [], origins: [] }));
    fs.writeFileSync(markerFile, "1");
    setup.skip();
    // setup.skip() 只是标记，不收窄类型；显式 return 同时满足控制流与类型收窄。
    return;
  }

  fs.rmSync(markerFile, { force: true });

  await page.goto(baseURL ?? "/");
  // The gate is rendered only after the frontend probes /bootstrap/status, so
  // wait for it instead of racing the probe (an immediate isVisible() can miss
  // the gate and silently skip login). Skip-auth instances have no gate at all.
  const usernameInput = page.getByLabel("用户名");
  const passwordInput = page.locator('input[type="password"]').first();
  const loginButton = page.getByRole("button", { name: "登录" });

  try {
    await usernameInput.waitFor({ state: "visible", timeout: 10000 });
  } catch {
    // No gate: only a skip-auth development instance enters the shell directly.
    // Confirm the main navigation actually rendered before treating this as
    // skip-auth; a failed asset load, page crash, or backend error must fail
    // the setup instead of being persisted as an authenticated storage state.
    await expect(
      page.getByRole("navigation", { name: "主导航" }),
      "live page shows neither the login gate nor the main shell",
    ).toBeVisible({ timeout: 10000 });
    await page.context().storageState({ path: authFile });
    return;
  }
  await usernameInput.fill(username);
  await passwordInput.fill(password);
  await loginButton.click();
  await page.waitForURL("**/", { timeout: 10000 });
  // 当前 UI 的登录后落点是番剧网格（已订阅/Mikan/AniBT/自定义分栏）。
  await expect(page.getByRole("button", { name: "已订阅" }).first()).toBeVisible({ timeout: 10000 });

  await page.context().storageState({ path: authFile });
});
