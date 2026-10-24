import { createHash } from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { expect, type Locator, type Page, type TestInfo } from "@playwright/test";
import { startFromSubscribedToast } from "./live-subscription";
import { csrfToken } from "./session";

// Shared steps for the operations-surface live specs (qb-manager-live,
// execution-records-live, log-dialog-live). They drive a disposable
// real-effect browser session: the fixture's `/ops/feed.xml?run=…&items=…`
// serves torrents that are unique per run value, so every run gets its own
// qBittorrent task:
//   slow  — "[BrowserFixture] Browser Ops Show - 02 [1080p]", one 2 MiB video
//           behind a throttled web seed, visibly downloading for about a minute;
//   batch — "[BrowserFixture] Browser Ops Show - 03 [1080p]", a folder with the
//           episode and an NCOP video that has no episode number (unknown file).
// Setup that is not the surface under test (naming root, metadata binding,
// a broken naming root for failure cases) goes through the same owner APIs the
// UI calls; everything the specs assert on is read from the UI and compared
// with the API.

export const OPS_GROUP = "BrowserFixture";
export const OPS_SLOW_RELEASE = "[BrowserFixture] Browser Ops Show - 02 [1080p]";
export const OPS_BATCH_RELEASE = "[BrowserFixture] Browser Ops Show - 03 [1080p]";
/** A naming root no process can create directories under. */
export const OPS_DENIED_ROOT = "/proc/bansoko-ops-denied";

const env = (name: string) => process.env[name]?.trim() || undefined;

export function opsPrerequisite(): string | undefined {
  if (process.env.BANSOKO_E2E_FIXTURES === "1") return "operations live specs do not run in fixture mode";
  if (process.env.BANSOKO_E2E_REAL_EFFECT !== "1") return "BANSOKO_E2E_REAL_EFFECT=1 is required";
  if (process.env.BANSOKO_TEST_DB_DISPOSABLE !== "1") return "BANSOKO_TEST_DB_DISPOSABLE=1 is required";
  if (!env("BANSOKO_TEST_BASE_URL")) return "BANSOKO_TEST_BASE_URL must point at the live session server";
  if (!env("BANSOKO_TEST_FIXTURE_RSS_URL")?.endsWith("/feed.xml")) return "BANSOKO_TEST_FIXTURE_RSS_URL (…/feed.xml) is required";
  if (!env("BANSOKO_QB_ENDPOINT") || !env("BANSOKO_QB_USERNAME") || !env("BANSOKO_QB_PASSWORD")) {
    return "BANSOKO_QB_ENDPOINT, BANSOKO_QB_USERNAME and BANSOKO_QB_PASSWORD are required";
  }
  if (!env("BANSOKO_QB_DOWNLOADER_ROOT") && !env("BANSOKO_HARNESS_DOWNLOAD_ROOT")) return "BANSOKO_QB_DOWNLOADER_ROOT is required";
  if (!env("BANSOKO_HARNESS_LIBRARY_ROOT")) return "BANSOKO_HARNESS_LIBRARY_ROOT is required";
  if (!env("BANSOKO_BROWSER_PROVIDER_BGM_ID")) return "BANSOKO_BROWSER_PROVIDER_BGM_ID is required";
  return undefined;
}

/** A run value unique to this invocation; it names the fixture torrents. */
export function opsRun(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}`;
}

/** The fixture's per-run name suffix (eight upper-case hex digits). */
export function opsTag(run: string): string {
  return createHash("sha256").update(run).digest("hex").slice(0, 8).toUpperCase();
}

export function opsSlowTorrentName(run: string): string {
  return `Browser Ops Show - 02 [1080p][${opsTag(run)}].mkv`;
}

export function opsBatchTorrentName(run: string): string {
  return `Browser Ops Show - 03 [1080p][${opsTag(run)}]`;
}

export function opsFeedURL(run: string, items: Array<"slow" | "batch">): string {
  const base = env("BANSOKO_TEST_FIXTURE_RSS_URL")!.replace(/\/feed\.xml$/, "");
  return `${base}/ops/feed.xml?run=${encodeURIComponent(run)}&items=${items.join(",")}`;
}

// The session rides an HttpOnly cookie, so `page.request.*` from the same
// BrowserContext is already authenticated and there is no token to read. An
// unsafe request still has to echo the double-submit value (see ./session).
export async function apiJSON<T = unknown>(
  page: Page,
  method: "GET" | "PUT" | "PATCH" | "POST" | "DELETE",
  apiPath: string,
  data?: unknown,
  headers: Record<string, string> = {},
): Promise<{ status: number; body: T }> {
  const response = await page.request.fetch(`/api/v1${apiPath}`, {
    method,
    headers: {
      ...(method === "GET" ? {} : { "X-CSRF-Token": await csrfToken(page) }),
      "Content-Type": "application/json",
      ...headers,
    },
    data: data === undefined ? undefined : JSON.stringify(data),
  });
  const text = await response.text();
  let body: unknown = text;
  try {
    body = text ? JSON.parse(text) : undefined;
  } catch {
    // keep the raw text
  }
  return { status: response.status(), body: body as T };
}

export async function waitForCondition(check: () => Promise<boolean>, timeout: number, message: string, interval = 2000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    if (await check()) return;
    if (Date.now() > deadline) throw new Error(`timed out: ${message}`);
    await new Promise((resolve) => setTimeout(resolve, interval));
  }
}

// --- evidence -----------------------------------------------------------------

function evidenceFile(testInfo: TestInfo, name: string): string {
  const dir = env("BANSOKO_E2E_EVIDENCE_DIR");
  if (!dir) return testInfo.outputPath(name);
  fs.mkdirSync(dir, { recursive: true });
  return path.join(dir, name);
}

/**
 * Screenshots the current state at desktop (1280) and phone (400) width and
 * asserts the phone layout has no horizontal page scroll. Returns to 1280.
 */
export async function captureWidths(page: Page, testInfo: TestInfo, name: string) {
  for (const [width, height] of [
    [1280, 900],
    [400, 860],
  ] as const) {
    await page.setViewportSize({ width, height });
    await page.waitForTimeout(500);
    // Best effort: let transient toasts close so they do not cover the dialog header.
    await expect(page.locator("[data-sonner-toast]")).toHaveCount(0, { timeout: 10000 }).catch(() => undefined);
    if (width === 400) {
      await expect
        .poll(() => page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth), {
          message: `${name}: no horizontal page scroll at phone width`,
        })
        .toBeLessThanOrEqual(0);
    }
    await page.screenshot({ path: evidenceFile(testInfo, `${name}-${width}.png`) });
  }
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.waitForTimeout(300);
}

// --- downloader -----------------------------------------------------------------

export type QbConnectionRow = { id: string; kind: string; name: string; enabled: boolean; version: number };

export function qbConnectionInput(name: string, baseURL: string, enabled: boolean) {
  return {
    kind: "qbittorrent",
    name,
    enabled,
    base_url: baseURL,
    username: env("BANSOKO_QB_USERNAME")!,
    secret: { mode: "keep" },
    download_root: env("BANSOKO_QB_DOWNLOAD_ROOT") ?? "/downloads",
    downloader_root: env("BANSOKO_QB_DOWNLOADER_ROOT") ?? env("BANSOKO_HARNESS_DOWNLOAD_ROOT")!,
  };
}

/**
 * The enabled qBittorrent connection of the session. A fresh session has none;
 * it is created once through the settings UI (the product path), and later
 * specs reuse it.
 */
export async function ensureOpsQbConnection(page: Page): Promise<QbConnectionRow> {
  const find = async () =>
    (await apiJSON<QbConnectionRow[]>(page, "GET", "/config/connections")).body.find((c) => c.kind === "qbittorrent" && c.enabled);
  const existing = await find();
  if (existing) return existing;
  await page.goto("/#/settings/download");
  const dialog = page.getByRole("dialog").first();
  await dialog.getByRole("button", { name: "新建下载器", exact: true }).click();
  const editor = page.getByRole("dialog").last();
  await editor.getByLabel("名称").fill("Ops qB");
  await editor.getByLabel("qBittorrent 地址").fill(env("BANSOKO_QB_ENDPOINT")!);
  await editor.getByLabel("用户名").fill(env("BANSOKO_QB_USERNAME")!);
  await editor.getByLabel(/下载目录（可选）/).fill(env("BANSOKO_QB_DOWNLOAD_ROOT") ?? "/downloads");
  await editor.getByLabel(/对应本机路径（可选）/).fill(env("BANSOKO_QB_DOWNLOADER_ROOT") ?? env("BANSOKO_HARNESS_DOWNLOAD_ROOT")!);
  await editor.getByLabel("密码").fill(env("BANSOKO_QB_PASSWORD")!);
  await editor.getByRole("button", { name: "保存", exact: true }).click();
  await expect(page.getByText("下载器已保存", { exact: true })).toBeVisible({ timeout: 15000 });
  await page.keyboard.press("Escape");
  const created = await find();
  expect(created, "the saved downloader is listed").toBeTruthy();
  return created!;
}

// --- naming ---------------------------------------------------------------------

type GlobalSection = { section_name: string; schema_version: string; value: Record<string, unknown>; version: number };

/** Moves the Global naming directory templates from one absolute root to another. */
export async function moveNamingRoot(page: Page, from: string, to: string) {
  const listed = await apiJSON<GlobalSection[]>(page, "GET", "/config/global-sections");
  expect(listed.status).toBe(200);
  const naming = listed.body.find((section) => section.section_name === "naming_rules")!;
  let moved = 0;
  const rewrite = (node: unknown): unknown => {
    if (Array.isArray(node)) return node.map(rewrite);
    if (!node || typeof node !== "object") return node;
    return Object.fromEntries(
      Object.entries(node).map(([key, value]) => {
        if (key === "directory_template" && typeof value === "string" && value.startsWith(`${from}/`)) {
          moved += 1;
          return [key, `${to}/${value.slice(from.length + 1)}`];
        }
        return [key, rewrite(value)];
      }),
    );
  };
  const value = rewrite(naming.value);
  expect(moved, `naming templates under ${from}`).toBeGreaterThan(0);
  const saved = await apiJSON(page, "PUT", "/config/global-sections/naming_rules", { schema_version: naming.schema_version, value }, {
    "If-Match": String(naming.version),
  });
  expect(saved.status, JSON.stringify(saved.body)).toBe(200);
}

export function libraryRoot(): string {
  return env("BANSOKO_HARNESS_LIBRARY_ROOT")!.replace(/\/+$/, "");
}

// --- custom presets and subscriptions --------------------------------------------

export function presetCard(page: Page, title: string): Locator {
  return page.getByRole("article").filter({ hasText: title }).first();
}

/** Saves a custom RSS preset for the ops feed through the 自定义 tab. */
export async function createOpsPreset(page: Page, input: { title: string; label: string; run: string; items: Array<"slow" | "batch"> }) {
  await page.goto("/");
  await page.getByRole("button", { name: "自定义", exact: true }).first().click();
  await page.getByRole("button", { name: "添加预设", exact: true }).first().click();
  const dialog = page.getByRole("dialog").last();
  await dialog.getByLabel("标题", { exact: true }).fill(input.title);
  await dialog.getByLabel("订阅备注", { exact: true }).fill(input.label);
  await dialog.getByLabel("RSS 地址", { exact: true }).fill(opsFeedURL(input.run, input.items));
  await dialog.getByLabel("字幕组", { exact: true }).fill(OPS_GROUP);
  await dialog.getByRole("button", { name: "保存预设", exact: true }).click();
  await expect(page.getByText("自定义预设已保存", { exact: true })).toBeVisible({ timeout: 15000 });
  await expect(presetCard(page, input.title)).toBeVisible({ timeout: 15000 });
}

/** Subscribes the preset, starts it from the toast and binds the fixture metadata. */
export async function subscribeOpsPreset(page: Page, title: string): Promise<string> {
  await presetCard(page, title).getByRole("button", { name: `打开配置 ${title}`, exact: true }).click();
  const draft = page.getByRole("dialog").last();
  const subscribed = page.waitForResponse((r) => r.url().endsWith("/subscribe") && r.request().method() === "POST");
  await draft.getByRole("button", { name: "订阅", exact: true }).click();
  const response = await subscribed;
  expect(response.status(), await response.text()).toBeLessThan(300);
  const managedID = ((await response.json()) as { managed_anime?: { id?: string } }).managed_anime?.id;
  expect(managedID, "subscribe answers the managed anime").toBeTruthy();
  await startFromSubscribedToast(page, managedID!);
  await bindOpsMetadata(page, managedID!);
  return managedID!;
}

/**
 * Acceptance holds downloads until the anime has its Bangumi and TMDB identity;
 * the fixture's metadata server answers the session's Bangumi id. The patch
 * replaces the metadata owner wholesale, so the owner-edited season rides the
 * same write: no binding derives it, and the shipped TV naming layout refuses a
 * season-less render (naming_fact_missing_anime_season_folder) before any
 * acquisition can start — a custom preset carries no season, so set it here as
 * the owner would (see live-metadata-binding.ts for the same fill in the UI).
 */
export async function bindOpsMetadata(page: Page, managedID: string) {
  const workspace = await apiJSON<{ metadata: { version: number; season?: number | null } }>(
    page, "GET", `/anime/${managedID}/workspace`,
  );
  expect(workspace.status).toBe(200);
  const bgm = env("BANSOKO_BROWSER_PROVIDER_BGM_ID")!;
  const bound = await apiJSON(page, "PATCH", `/anime/${managedID}/metadata`, {
    expected_version: workspace.body.metadata.version,
    bgm: { work_id: bgm },
    tmdb: { work_id: `tmdb-${bgm}`, media_kind: "tv", season: 1 },
    season: workspace.body.metadata.season ?? 1,
  });
  expect(bound.status, JSON.stringify(bound.body)).toBe(200);
}

// --- execution records -----------------------------------------------------------

export type RecordSummary = {
  acquisition_id: string;
  state: string;
  entry_kind: string;
  one_shot_id?: string | null;
  anime?: { managed_anime_id: string; title: string };
};

export async function listRecords(page: Page, query: string): Promise<RecordSummary[]> {
  const listed = await apiJSON<{ items: RecordSummary[] }>(page, "GET", `/acquisitions?${query}`);
  expect(listed.status).toBe(200);
  return listed.body.items;
}

/** Waits until the anime has `count` records and returns them. */
export async function waitForRecords(page: Page, managedID: string, count: number, timeout = 180000): Promise<RecordSummary[]> {
  let items: RecordSummary[] = [];
  await waitForCondition(
    async () => {
      items = await listRecords(page, `managed_anime_id=${managedID}`);
      return items.length >= count;
    },
    timeout,
    `${count} execution record(s) for ${managedID}`,
  );
  return items;
}

export type RecordDetail = {
  acquisition_id: string;
  state: string;
  one_shot_id?: string | null;
  release?: { title?: { state: string; value?: string } };
  stages: Array<{ stage: string; state?: string; availability: string; attempts_started?: number; reason_code?: string }>;
  outcome?: { outcome: string; counts?: { total: number; required: number; excluded: number; succeeded: number; failed: number } };
};

export async function readRecord(page: Page, acquisitionID: string): Promise<RecordDetail> {
  const detail = await apiJSON<RecordDetail>(page, "GET", `/acquisitions/${acquisitionID}`);
  expect(detail.status).toBe(200);
  return detail.body;
}

export async function waitForRecordState(page: Page, acquisitionID: string, states: string[], timeout = 300000): Promise<RecordDetail> {
  let detail: RecordDetail | undefined;
  await waitForCondition(
    async () => {
      detail = await readRecord(page, acquisitionID);
      return states.includes(detail.state);
    },
    timeout,
    `record ${acquisitionID} to reach ${states.join("/")}`,
  );
  return detail!;
}

/** Finds the record of one release by its detail's release title. */
export async function recordForRelease(page: Page, records: RecordSummary[], release: string): Promise<RecordSummary> {
  for (const record of records) {
    const detail = await readRecord(page, record.acquisition_id);
    if (detail.release?.title?.value === release) return record;
  }
  throw new Error(`no record for ${release}`);
}
