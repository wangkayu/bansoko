import { defineConfig, devices } from "@playwright/test";

const fixtureMode = process.env.BANSOKO_E2E_FIXTURES === "1";
// Destructive real-effect invocations must be artifact-free across the WHOLE
// invocation, including the auth `setup` project: describe-level options in a
// spec file (custom-rss-live.spec.ts) do not reach dependent projects, and
// auth.setup.ts captures login credentials on screen. Fail closed on
// BANSOKO_E2E_REAL_EFFECT=1 instead of relying on per-spec configuration: those
// runs use the line reporter with preserveOutput "never" (no HTML report or
// retained test artifacts) plus trace/video/screenshot off below.
const realEffectMode = process.env.BANSOKO_E2E_REAL_EFFECT === "1";
// Session scripts point storage state at a private per-session file via
// BANSOKO_E2E_AUTH_STATE_PATH (same value auth.setup.ts writes); the default stays
// the historical shared path.
const authStatePath =
  process.env.BANSOKO_E2E_AUTH_STATE_PATH || "playwright/.auth/state.json";
const outputDir = process.env.BANSOKO_E2E_OUTPUT_DIR || "test-results";
const defaultBaseURL = fixtureMode ? "http://localhost:8081" : "http://localhost:8080";
// Fixture mode must never honor an inherited BANSOKO_TEST_BASE_URL: the lane is
// defined against its own local fixture-mode server, and an inherited value
// would silently point it at another (possibly live) instance. Fixture mode
// ignores the variable for both baseURL and webServer selection; the CI
// wrapper additionally refuses to run the fixture lane with the variable set.
const baseURL = fixtureMode
  ? defaultBaseURL
  : process.env.BANSOKO_TEST_BASE_URL || defaultBaseURL;
// WebServer selection: fixture mode ALWAYS boots its own fixture-mode server
// on port 8081 and never reuses whatever may already listen there, regardless
// of BANSOKO_TEST_BASE_URL. Non-fixture modes keep the historical behavior: honor
// BANSOKO_TEST_BASE_URL, otherwise start/reuse `pnpm dev` on port 8080.
const webServer = fixtureMode
  ? {
      command: "VITE_BANSOKO_E2E_FIXTURES=1 pnpm dev --host 0.0.0.0 --port 8081",
      url: defaultBaseURL,
      reuseExistingServer: false,
      timeout: 120 * 1000,
    }
  : process.env.BANSOKO_TEST_BASE_URL
    ? undefined
    : {
        command: "pnpm dev",
        url: defaultBaseURL,
        reuseExistingServer: !process.env.CI,
        timeout: 120 * 1000,
      };

/**
 * Playwright end-to-end configuration for the React frontend.
 * See https://playwright.dev/docs/intro.
 */
export default defineConfig({
  testDir: "./e2e",
  outputDir,
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: realEffectMode ? 0 : process.env.CI ? 2 : 0,
  // Real-effect invocations run destructive/live specs against a shared
  // backend/filesystem; parallel workers would interleave their effects, so
  // pin them to one worker. Other modes keep the original behavior.
  workers: realEffectMode ? 1 : process.env.CI ? 1 : undefined,
  // Real-effect runs must not persist an HTML report (it embeds test titles
  // and error excerpts of the live flow); the line reporter keeps console
  // output only. Array form so either side stays extensible.
  reporter: realEffectMode ? [["line"]] : [["html"]],
  // Retain failure diagnostics (not passing-run artifacts) for destructive real-effect invocations.
  // Auth screenshots/media remain disabled below; error-context snapshots survive for diagnosis.
  preserveOutput: realEffectMode ? "failures-only" : "always",
  use: {
    baseURL,
    trace: realEffectMode ? "off" : "on-first-retry",
    // Playwright defaults are already "off"; pinned so no project (incl.
    // setup) can emit media artifacts of the credential-bearing login flow.
    video: "off",
    screenshot: "off",
    serviceWorkers: "block",
    // The UI language defaults to the browser language; the specs assert the
    // zh-CN copy, so pin the browser locale.
    locale: "zh-CN",
  },
  projects: [
    { name: "setup", testMatch: /auth\.setup\.ts/, },
    {
      name: "desktop",
      use: {
        ...devices["Desktop Chrome"],
        storageState: authStatePath,
      },
      dependencies: ["setup"],
    },
    {
      name: "mobile",
      use: {
        ...devices["iPhone 13"],
        // WebKit's threaded compositor segfaults on the CI VM's emulated GPU
        // while a card animates; with motion reduced the specs run the same
        // flows without the crash.
        contextOptions: { reducedMotion: "reduce" },
        storageState: authStatePath,
      },
      dependencies: ["setup"],
    },
  ],
  webServer,
});
