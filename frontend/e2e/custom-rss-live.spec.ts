import { test, expect, type Response, type Request, type Locator, type Page } from "@playwright/test";
import type { AnimeWorkspace, RSSListView, RSSListViewInput } from "../src/lib/api/client";
import { assertReadOnlySnapshots, readDisposableSnapshot, readSessionFixtureIdentity } from "./disposable-db-snapshot";
import fs from "node:fs/promises";
import { shouldSkipAuthTests } from "./skip-auth-helper";
import { startFromSubscribedToast } from "./live-subscription";
import { anchorNamingAtHarnessLibrary } from "./live-harness-library";
import { bindWorkspaceMetadata } from "./live-metadata-binding";

// This suite is intentionally separate from fixture-only UI evidence. The RSS URL
// is fetched by the authenticated server; this spec never intercepts API calls.
const explicitBaseURL = process.env.BANSOKO_TEST_BASE_URL?.trim();
const fixtureRSSURL = process.env.BANSOKO_TEST_FIXTURE_RSS_URL?.trim();
const qbEndpoint = process.env.BANSOKO_QB_ENDPOINT?.trim();
const qbUsername = process.env.BANSOKO_QB_USERNAME?.trim();
const qbPassword = process.env.BANSOKO_QB_PASSWORD?.trim();
// Session-scoped instance identity written by real-effect-browser-session.sh;
// it anchors every card/workspace lookup and the SQL owner assertion to THIS
// disposable session instead of a fixed title match.
const instanceLabel = process.env.BANSOKO_BROWSER_FIXTURE_INSTANCE_LABEL?.trim();

function pathMapRoots(value: string | undefined): { downloadRoot: string; downloaderRoot: string } | undefined {
  for (const entry of value?.split(",") ?? []) {
    const separator = entry.indexOf("=");
    if (separator <= 0) continue;
    const downloadRoot = entry.slice(0, separator).trim();
    const downloaderRoot = entry.slice(separator + 1).trim();
    if (downloadRoot !== "" && downloaderRoot !== "") return { downloadRoot, downloaderRoot };
  }
  return undefined;
}

const mappedRoots = pathMapRoots(process.env.BANSOKO_QB_PATH_MAP);
const downloadRoot =
  process.env.BANSOKO_QB_DOWNLOAD_ROOT?.trim() ||
  process.env.BANSOKO_QB_SAVE_ROOT?.trim() ||
  mappedRoots?.downloadRoot ||
  "/downloads";
const downloaderRoot =
  process.env.BANSOKO_QB_DOWNLOADER_ROOT?.trim() ||
  mappedRoots?.downloaderRoot ||
  process.env.BANSOKO_HARNESS_DOWNLOAD_ROOT?.trim();

function livePrerequisite(): string | undefined {
  if (process.env.BANSOKO_E2E_FIXTURES === "1") {
    return "custom RSS live evidence does not run in fixture mode";
  }
  if (process.env.BANSOKO_TEST_DB_DISPOSABLE !== "1") {
    return "BANSOKO_TEST_DB_DISPOSABLE=1 is required: the destructive real-effect chain must never touch a persistent database";
  }
  if (process.env.BANSOKO_E2E_REAL_EFFECT !== "1") {
    return "BANSOKO_E2E_REAL_EFFECT=1 is required: this suite performs real download and file effects";
  }
  if (!explicitBaseURL) return "BANSOKO_TEST_BASE_URL must explicitly point at the live authenticated server";
  if (!fixtureRSSURL) return "BANSOKO_TEST_FIXTURE_RSS_URL is required; no RSS source will be invented";
  try {
    const parsed = new URL(fixtureRSSURL);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      return "BANSOKO_TEST_FIXTURE_RSS_URL must be an http(s) URL";
    }
  } catch {
    return "BANSOKO_TEST_FIXTURE_RSS_URL must be a valid URL";
  }
  if (!qbEndpoint || !qbUsername || !qbPassword) {
    return "BANSOKO_QB_ENDPOINT, BANSOKO_QB_USERNAME, and BANSOKO_QB_PASSWORD are required";
  }
  if (!downloaderRoot) {
    return "a qB path map or BANSOKO_QB_DOWNLOADER_ROOT/BANSOKO_HARNESS_DOWNLOAD_ROOT is required";
  }
  if (!instanceLabel) {
    return "BANSOKO_BROWSER_FIXTURE_INSTANCE_LABEL is required so effect evidence stays attributable to this disposable session";
  }
  return undefined;
}

function sanitizeRecordEvidence(value: unknown, key?: string): unknown {
  if (key && /(id|ref|correlation|causation)/i.test(key)) return "<redacted>";
  if (Array.isArray(value)) return value.map((item) => sanitizeRecordEvidence(item));
  if (value && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value).map(([childKey, childValue]) => [childKey, sanitizeRecordEvidence(childValue, childKey)]),
    );
  }
  if (typeof value === "string" && /(https?:\/\/|magnet:|\/home\/|\/users\/|\/private\/|\/downloads\/|\/library\/)/i.test(value)) {
    return "<redacted>";
  }
  return value;
}

async function saveRecordEvidence(recordDialog: Locator, response: Response): Promise<void> {
  const evidenceDir = process.env.BANSOKO_E2E_EVIDENCE_DIR?.trim();
  if (!evidenceDir) return;
  await fs.mkdir(evidenceDir, { recursive: true });
  const payload = sanitizeRecordEvidence(await response.json());
  await fs.writeFile(`${evidenceDir}/execution-record.json`, `${JSON.stringify(payload, null, 2)}\n`, "utf8");
  await expect
    .poll(async () => (await recordDialog.boundingBox())?.width ?? 0, { timeout: 15000 })
    .toBeGreaterThan(600);
  await expect
    .poll(async () => (await recordDialog.boundingBox())?.height ?? 0, { timeout: 15000 })
    .toBeGreaterThan(500);
  await recordDialog.screenshot({
    path: `${evidenceDir}/execution-record-dialog.png`,
    animations: "disabled",
    scale: "css",
  });
}

function settingsDialog(page: Page): Locator {
  return page.getByRole("dialog").first();
}

function latestDialog(page: Page): Locator {
  return page.getByRole("dialog").last();
}

function sessionCard(page: Page, title: string, label: string): Locator {
  // The session-scoped instance label is rendered on custom cards (FactLine),
  // so title+label pins exactly one card. No .first(): a strict-mode
  // multi-match must fail loudly instead of silently picking a stale card.
  return page.getByRole("article").filter({ hasText: title }).filter({ hasText: label });
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


async function fixtureCounters(page: Page) {
  const url = process.env.BANSOKO_BROWSER_FIXTURE_COUNTERS_URL?.trim();
  if (!url) throw new Error("fixture counters URL is required for no-provider-fetch evidence");
  const response = await page.request.get(url);
  expect(response.status()).toBe(200);
  return response.json() as Promise<Record<string, number>>;
}

async function sessionHeaders(page: Page): Promise<Record<string, string>> {
  // No credential to read: the session is an HttpOnly cookie the context
  // attaches. These callers only issue GETs, so no CSRF echo is needed.
  return {};
}

async function assertOwnerDraftIsReadOnly(page: Page, workspace: Locator, managedAnimeID: string) {
  // Placement enqueues follow-up work (replacement evaluation) after the file
  // is already shown as placed. Invariance is measured from a quiescent owner,
  // otherwise the chain's own completion reads as a preview side effect.
  await expect
    .poll(
      async () => (await readDisposableSnapshot()).work_items
        .filter((row) => row.managed_anime_id === managedAnimeID && (row.state === "available" || row.state === "leased"))
        .map((row) => `${row.owner_type}/${row.kind}/${row.state}`),
      { timeout: 60000, intervals: [1000, 2000, 5000] },
    )
    .toEqual([]);
  const beforeResponse = await page.request.get(`/api/v1/anime/${managedAnimeID}/workspace`, { headers: await sessionHeaders(page) });
  expect(beforeResponse.status()).toBe(200);
  const before = await beforeResponse.json() as AnimeWorkspace;
  const fixture = await readSessionFixtureIdentity();
  const matches = before.releases.filter((row) => row.raw_facts.title === fixture.title && row.stable_item_key === fixture.stable_item_key && before.rss_sources.some((source) => source.id === row.rss_source_id));
  expect(matches).toHaveLength(1);
  const targetRelease = matches[0];
  if (!targetRelease) throw new Error("owner_draft needs a persisted session release");
  const target = { managed_id: managedAnimeID, source_id: targetRelease.rss_source_id, release_id: targetRelease.id };
  const dbBefore = await readDisposableSnapshot(target);
  const countersBefore = await fixtureCounters(page);
  const forbidden: string[] = [];
  const observeRequest = (request: Request) => {
    const pathname = new URL(request.url()).pathname;
    if (request.method() === "POST" && pathname === "/api/v1/anime/rss-list-view") return;
    if (/\/catalog\/providers\//.test(pathname) || /\/rss-sources\//.test(pathname) || (pathname.startsWith("/api/v1/") && !["GET", "HEAD"].includes(request.method()))) forbidden.push(`${request.method()} ${pathname}`);
  };
  page.on("request", observeRequest);
  try {
    await workspace.getByRole("tab", { name: "规则", exact: true }).click();
    // 每段都是番剧自己的值，直接编辑（没有使用默认 / 自定义切换）。
    const section = workspace.locator("section.rules-card").filter({ has: page.getByText("标题过滤", { exact: true }) });
    // A unique case-sensitive substring of this raw feed title, not an unchanged
    // saved rules object. The unsaved delta must change the evaluation below.
    const keyword = targetRelease.raw_facts.title;
    if (!keyword) throw new Error("raw release title is required");
    // The release filter is one Go RE2 not_in pattern: match the raw title
    // literally by escaping its metacharacters.
    const pattern = keyword.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    const exclude = section.getByRole("textbox", { name: "标题不能包含", exact: true });
    await exclude.fill(pattern);
    await expect(exclude).toHaveValue(pattern);
    await expect(workspace.locator(".anime-config-dialog-footer-owner").getByRole("button", { name: "保存", exact: true })).toBeEnabled();
    const requestPromise = page.waitForRequest((request) => request.method() === "POST" && new URL(request.url()).pathname === "/api/v1/anime/rss-list-view" && (request.postDataJSON() as RSSListViewInput).input_source === "owner_draft");
    await workspace.getByRole("button", { name: "预览规则", exact: true }).click();
    const request = await requestPromise;
    const input = request.postDataJSON() as RSSListViewInput;
    expect(input.input_source).toBe("owner_draft");
    expect(input.managed_anime_id).toBe(managedAnimeID);
    const filter = input.rules_preview?.release_filter;
    expect(filter?.mode).toBe("replace");
    if (!filter || filter.mode !== "replace") throw new Error("missing changed release filter");
    expect(filter.value.not_in).toBe(pattern);
    expect(input.rules_preview).not.toEqual(before.rules.sections);
    const response = await request.response();
    expect(response?.status()).toBe(200);
    if (!response) throw new Error("owner_draft response missing");
    const result = await response.json() as RSSListView;
    expect(result.mode).toBe("owner_draft");
    const items = result.groups.flatMap((group) => group.items);
    const item = items.find((row) => row.snapshot_id === `release:${targetRelease.id}`);
    expect(item?.seed.title).toBe(keyword);
    expect(item?.evaluation.verdict).toBe("filtered");
    expect(targetRelease.verdict).not.toBe("filtered");
    expect(items.every((row) => row.download_action.kind === "none" && row.download_action.reason_code === "preview_mode")).toBe(true);
    // Preview deliberately disables the filter toggle too; do not click it.
    // Evaluation is proven by the exact release response above, independently
    // of presentation hiding. Toolbar actions remain visible and disabled.
    await expect(workspace.getByRole("checkbox", { name: "显示被过滤", exact: true })).toBeDisabled();
    for (const name of ["刷新 RSS", "扫描媒体库"]) {
      const action = workspace.getByRole("button", { name, exact: true });
      await expect(action).toBeVisible();
      await expect(action).toBeDisabled();
    }
    const disabled = workspace.locator('button[title="预览模式下不可操作"]');
    expect(await disabled.count()).toBeGreaterThan(0);
    for (const button of await disabled.all()) await expect(button).toBeDisabled();
    const afterResponse = await page.request.get(`/api/v1/anime/${managedAnimeID}/workspace`, { headers: await sessionHeaders(page) });
    expect(afterResponse.status()).toBe(200);
    const after = await afterResponse.json() as AnimeWorkspace;
    expect(after.subscription).toEqual(before.subscription);
    expect(after.rules).toEqual(before.rules);
    assertReadOnlySnapshots(dbBefore, await readDisposableSnapshot(target));
    expect(await fixtureCounters(page)).toEqual(countersBefore);
    expect(forbidden).toEqual([]);
  } finally {
    page.off("request", observeRequest);
  }
}


async function addCustomPreset(page: Page, title: string, groupName: string, label: string) {
  await page.keyboard.press("Escape");
  await expect(settingsDialog(page)).toBeHidden({ timeout: 10000 });
  await page.getByRole("button", { name: "自定义", exact: true }).first().click();
  await page.getByRole("button", { name: "添加预设", exact: true }).first().click();
  const dialog = latestDialog(page);
  await expect(dialog.getByText("添加自定义预设", { exact: true })).toBeVisible();
  await dialog.getByLabel("标题", { exact: true }).fill(title);
  await dialog.getByLabel("订阅备注", { exact: true }).fill(label);
  await dialog.getByLabel("RSS 地址", { exact: true }).fill(fixtureRSSURL!);
  await dialog.getByLabel("字幕组", { exact: true }).fill(groupName);
  const saveResponse = page.waitForResponse(
    (response) => new URL(response.url()).pathname.endsWith("/api/v1/anime-config-candidates") && response.request().method() === "POST",
    { timeout: 15000 },
  );
  await dialog.getByRole("button", { name: "保存预设", exact: true }).click();
  const saved = await saveResponse;
  expect(saved.ok(), await saved.text()).toBe(true);
  const savedBody = await saved.json() as { current_revision?: number | null };
  expect(savedBody.current_revision).toBe(1);
  await expect(page.getByText("自定义预设已保存", { exact: true })).toBeVisible({ timeout: 15000 });
  const card = sessionCard(page, title, label);
  await expect(card).toBeVisible({ timeout: 15000 });
  // Candidate creation is outside the explanatory-only window. Start the
  // observer after this explicit save and before opening the draft.
  return card;
}



async function assertDraftExplanationIsReadOnly(page: Page, card: Locator, title: string, groupName: string) {
  const before = await readDisposableSnapshot();
  const countersBefore = await fixtureCounters(page);
  const forbidden: string[] = [];
  const observeRequest = (request: Request) => {
    const pathname = new URL(request.url()).pathname;
    // The contract declares this POST a side-effect-free naming preview; the
    // unchanged DB snapshot below still proves it wrote nothing.
    if (request.method() === "POST" && pathname === "/api/v1/template-previews/naming") return;
    // The draft bench's own reads: the list read model, and the saved custom
    // source's snapshot fetch (the URL is resolved server-side). Both are reads —
    // the DB snapshot below proves no business state is written and the counters
    // below prove no provider endpoint is touched.
    if (request.method() === "POST" && (pathname === "/api/v1/anime/rss-list-view" || pathname === "/api/v1/catalog/custom/feed-snapshots:fetch")) return;
    if (/\/catalog\/providers\//.test(pathname) || pathname === "/api/v1/anime/rss-list-view" || /\/rss-sources\//.test(pathname) || /\/anime\/[^/]+\/workspace$/.test(pathname) || /\/work-items(?:\/|$)/.test(pathname) || (pathname.startsWith("/api/v1/") && !["GET", "HEAD"].includes(request.method()))) forbidden.push(`${request.method()} ${pathname}`);
  };
  page.on("request", observeRequest);
  try {
    await card.getByRole("button", { name: `打开配置 ${title}`, exact: true }).click();
    const dialog = latestDialog(page);
    for (const name of ["元数据", "字幕组", "规则", "RSS 预览"]) {
      await expect(dialog.getByRole("tab", { name, exact: true })).toBeVisible();
      await dialog.getByRole("tab", { name, exact: true }).click();
    }
    await expect(dialog.getByRole("tab")).toHaveCount(4);
    await dialog.getByRole("tab", { name: "规则", exact: true }).click();
    // The preset was saved with the complete Global business, so the draft
    // already carries the copy/versioned file effect configured in settings.
    // The rules tab renders every section from the draft; the file-effect and
    // title-filter inputs are unique in the dialog, so they need no section wrapper.
    await expect(dialog.getByRole("combobox", { name: "整理方式", exact: true })).toHaveText(/复制/);
    await expect(dialog.getByRole("combobox", { name: "同名文件处理", exact: true })).toHaveText(/加版本号/);
    // A real unsaved edit that can never exclude a fixture release.
    const exclude = dialog.getByRole("textbox", { name: "标题不能包含", exact: true });
    await exclude.fill("^__e2e_never_matches__$");
    await expect(exclude).toHaveValue("^__e2e_never_matches__$");
    await dialog.getByRole("tab", { name: "RSS 预览", exact: true }).click();
    // Opening the tab reads the saved custom source once (a read, not a save):
    // the read model renders the preset's own group and its seed count. The
    // unsaved rule edit above only marks the view stale — a draft change never
    // recomputes on its own.
    await expect(dialog.getByRole("button", { name: `展开 ${groupName}` })).toContainText("共 1 个种子", { timeout: 15000 });
    await expect(dialog.getByText("配置已修改，点击「预览」刷新", { exact: true })).toBeVisible();
    // The draft bench is a test bench: no download entry anywhere in it.
    await expect(dialog.getByRole("button", { name: /下载/ })).toHaveCount(0);
    await expect(dialog.getByRole("button", { name: "订阅", exact: true })).toBeDisabled();
    // Include the complete interaction window through final observations. An
    // explicit candidate Save occurs only AFTER this explanatory-only window.
    assertReadOnlySnapshots(before, await readDisposableSnapshot());
    // The custom source is read from its own fixture URL; no provider endpoint
    // may be touched by a custom preset's draft.
    const countersAfter = await fixtureCounters(page);
    for (const path of ["/rss/anime.xml", "/api/seasons/anime", "/api/anime/groups"]) {
      expect(countersAfter[path] ?? 0, `${path} must not be fetched`).toBe(countersBefore[path] ?? 0);
    }
    expect(countersAfter["/feed.xml"] ?? 0).toBeGreaterThan(countersBefore["/feed.xml"] ?? 0);
    expect(forbidden).toEqual([]);
  } finally {
    page.off("request", observeRequest);
  }
  const dialog = latestDialog(page);
  await dialog.getByRole("button", { name: "保存配置", exact: true }).click();
  await expect(dialog.getByText("已保存", { exact: true })).toBeVisible({ timeout: 15000 });
  await expect(dialog.getByRole("button", { name: "订阅", exact: true })).toBeEnabled();
}

async function subscribeAndOpenWorkspace(page: Page, title: string, label: string) {
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
  const subscribedBody = await subscribed.json() as { managed_anime?: { id?: string } };
  const managedAnimeID = subscribedBody.managed_anime?.id;
  expect(managedAnimeID).toBeTruthy();
  await expect(draftDialog).toBeHidden({ timeout: 15000 });
  await startFromSubscribedToast(page, managedAnimeID!);
  await page.getByRole("button", { name: "已订阅", exact: true }).click();
  const card = sessionCard(page, title, label);
  await expect(card).toBeVisible({ timeout: 30000 });
  await card.getByRole("button", { name: `打开配置 ${title}`, exact: true }).click();
  const workspace = latestDialog(page);
  await expect(workspace.getByRole("tab", { name: "RSS", exact: true })).toBeVisible({ timeout: 30000 });
  await workspace.getByRole("tab", { name: "RSS", exact: true }).click();
  await expect(workspace.getByRole("tab", { name: "RSS", exact: true })).toHaveAttribute("aria-selected", "true", { timeout: 30000 });
  return { workspace, managedAnimeID: managedAnimeID! };
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

async function waitForRelease(
  page: Page,
  workspace: Locator,
  title: string,
  groupName: string,
): Promise<{ workspace: Locator; release: Locator }> {
  const currentWorkspace = workspace;
  for (let attempt = 0; attempt < 45; attempt += 1) {
    const expandedGroup = currentWorkspace.getByRole("button", { name: `展开 ${groupName}`, exact: true });
    if (await expandedGroup.count()) {
      await expandedGroup.click();
    }
    const release = currentWorkspace.getByRole("button", { name: "展开文件与状态", exact: true }).first();
    if (await release.isVisible().catch(() => false)) return { workspace: currentWorkspace, release };
    await requestWorkspaceRefresh(page, currentWorkspace);
    await page.waitForTimeout(2000);
  }
  throw new Error(`release did not appear after refreshing RSS for ${title}`);
}

// 执行记录（只读）取证：执行记录列表 → 第一条 → 详情的固定处理阶段。
// 顶栏的「执行记录」按钮与这里的路由深链打开同一个 dialog；列表与详情断言不变。
async function captureExecutionRecordEvidence(page: Page, workspaceDialog: Locator): Promise<void> {
  await workspaceDialog.locator(".anime-config-dialog-footer-owner").getByRole("button", { name: "关闭", exact: true }).click();
  const discard = page.getByRole("alertdialog");
  await expect(discard.getByText("放弃未保存的修改？", { exact: true })).toBeVisible();
  await discard.getByRole("button", { name: "放弃修改", exact: true }).click();
  await expect(discard).toBeHidden();
  await expect(workspaceDialog).toBeHidden({ timeout: 15000 });

  await page.goto("/#/?record=list&record_segment=subscription");
  const recordDialog = page.getByRole("dialog", { name: "执行记录", exact: true });
  const recordList = recordDialog.getByRole("list", { name: "执行记录列表", exact: true });
  await expect(recordList).toBeVisible({ timeout: 30000 });
  const detailResponsePromise = page.waitForResponse(
    (response) => {
      const pathname = new URL(response.url()).pathname;
      return /^\/api\/v1\/acquisitions\/[^/]+$/.test(pathname) && response.request().method() === "GET";
    },
    { timeout: 30000 },
  );
  await recordList.getByRole("button").first().click();
  const detailResponse = await detailResponsePromise;
  expect(detailResponse.status()).toBe(200);
  await expect(recordDialog.getByRole("list", { name: "处理阶段列表", exact: true })).toBeVisible({ timeout: 30000 });
  await expect(recordDialog.getByRole("button", { name: /重试/ })).toHaveCount(0);
  await saveRecordEvidence(recordDialog, detailResponse);
}

async function refreshExpandAndDownload(page: Page, workspace: Locator, title: string, groupName: string) {
  for (const name of ["元数据", "字幕组", "规则", "RSS"]) {
    await expect(workspace.getByRole("tab", { name, exact: true })).toBeVisible();
  }
  await expect(workspace.getByRole("tab")).toHaveCount(4);

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

  // The instance was activated from a revision carrying the complete Global
  // business, so ordinary RSS admission acquires the release by itself. Any
  // manual release retry here would mask a broken automatic chain.
  const retryRequests: string[] = [];
  const observeRetry = (request: Request) => {
    const pathname = new URL(request.url()).pathname;
    if (pathname.startsWith("/api/v1/releases/") && pathname.endsWith("/retry")) retryRequests.push(request.url());
  };
  page.on("request", observeRetry);
  const located = await waitForRelease(page, workspace, title, groupName);
  const currentWorkspace = located.workspace;
  const releaseTitle = (await located.release.textContent())?.trim();
  expect(releaseTitle).toBeTruthy();
  // The expand button changes its accessible name after the click; resolve
  // the row from its stable release title instead.
  const releaseItem = currentWorkspace.locator("li").filter({ hasText: releaseTitle! }).first();
  await located.release.click();
  await expect(currentWorkspace.getByRole("button", { name: "折叠文件与状态", exact: true }).first()).toBeVisible();
  // A feed may legitimately omit parsed file facts; the expanded seed row
  // itself is the evidence that the seed/file surface is reachable.
  const seedFiles = currentWorkspace.getByText("种子文件", { exact: true });
  if (await seedFiles.count()) await expect(seedFiles.first()).toBeVisible();
  const observedStates = new Set<string>();
  await expect
    .poll(
      async () => {
        const text = (await releaseItem.textContent()) ?? "";
        for (const state of ["排队中", "下载中", "下载完成", "已下载", "校验中", "入库中", "已入库", "获取失败", "校验失败", "入库失败"]) {
          if (text.includes(state)) observedStates.add(state);
        }
        return text;
      },
      { timeout: 180000, intervals: [1000, 2000, 5000] },
    )
    .toContain("已入库");
  expect(observedStates.has("已入库")).toBe(true);
  page.off("request", observeRetry);
  expect(retryRequests).toEqual([]);
}

// Destructive real-effect evidence: never retried (see describe below), and no
// Playwright trace/video artifacts that could carry credentials (storage state,
// qB password fields) out of the disposable session. The explicit trace JSON and
// nested-dialog screenshot are recursively sanitized before being written. Fixture
// suites elsewhere keep their own project defaults. This must stay at file top level:
// Playwright rejects test.use() inside a describe group.
test.use({ trace: "off", video: "off", screenshot: "off" });

test.describe.configure({ mode: "serial" });

test.describe("custom RSS authenticated real chain", () => {
  // Destructive real-effect evidence is never retried.
  test.describe.configure({ retries: 0 });

  test.beforeEach(({ isMobile }) => {
    test.skip(isMobile, "the destructive real-effect chain runs once on desktop");
    test.skip(shouldSkipAuthTests(), "auth.setup did not receive BANSOKO_E2E_USERNAME/BANSOKO_E2E_PASSWORD");
    const reason = livePrerequisite();
    test.skip(reason !== undefined, reason);
  });

  test("login → qB settings → custom preset → preview → subscribe → RSS → qB/file owners", async ({ page }, testInfo) => {
    test.setTimeout(240000);
    const runID = `${Date.now()}-${testInfo.workerIndex}-${testInfo.retry}`;
    const title = process.env.BANSOKO_BROWSER_FIXTURE_TITLE?.trim();
    const groupName = process.env.BANSOKO_BROWSER_FIXTURE_GROUP?.trim();
    if (!title || !groupName || !instanceLabel) throw new Error("fixture title/group metadata and BANSOKO_BROWSER_FIXTURE_INSTANCE_LABEL are required");
    const qbName = `E2E qB ${runID}`;

    // The storageState comes from auth.setup.ts. Requiring the real shell here
    // makes an expired/missing session fail instead of becoming fixture evidence.
    await page.goto("/");
    await expect(page.getByRole("navigation", { name: "主导航" })).toBeVisible({ timeout: 15000 });
    await expect(page.getByRole("button", { name: "已订阅", exact: true }).first()).toBeVisible();
    await anchorNamingAtHarnessLibrary(page);

    await saveQbConnection(page, qbName);
    await saveFilePolicy(page);
    const presetCard = await addCustomPreset(page, title, groupName, instanceLabel);
    await assertDraftExplanationIsReadOnly(page, presetCard, title, groupName);

    const owner = await subscribeAndOpenWorkspace(page, title, instanceLabel);
    const workspace = await bindWorkspaceMetadata(page, owner.workspace, owner.managedAnimeID);
    await refreshExpandAndDownload(page, workspace, title, groupName);
    await assertOwnerDraftIsReadOnly(page, workspace, owner.managedAnimeID);
    await captureExecutionRecordEvidence(page, workspace);
  });
});
