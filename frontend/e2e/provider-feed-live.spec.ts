import { test, expect, type Locator, type Page, type Request, type Response } from "@playwright/test";
import { shouldSkipAuthTests } from "./skip-auth-helper";
import { csrfToken } from "./session";
import { startFromSubscribedToast } from "./live-subscription";

// Persistent provider-feed real-effect suite. Unlike the custom-RSS spec this
// drives a REAL saved provider connection (AniBT) against the shared browser
// fixture, which serves the season list, the group discovery and the per-group
// RSS feed, and counts every feed fetch. The counters are the observable proof
// that a unified-workspace read never issues a live provider fetch, and that
// only an explicit update moves the snapshot.
//
// Identity assertions always use product identities: managed_anime.id,
// candidate_id + revision, and the persisted feed snapshot id/version/hash.
// The provider entry ref and the feed URL are only non-unique attributes.
//
// The draft surface is the unified RSS workspace: edits only update the draft;
// an explicit preview can acquire missing snapshots and then render the read.
//
// Scenarios are independent tests, not one serial chain: each one prepares what
// it needs itself (saved draft + persisted snapshot, or an active owner), so
// one failure never skips the others and any single scenario can be re-run with
// `--grep`. They still share ONE live server session, whose fixture entry has a
// one-way lifecycle (unsubscribed -> subscribed -> retired). Hence the
// declaration order: draft scenarios first, owner scenarios last. A provider
// browse card carries no subscription fields, so the retired entry looks like a
// plain card again and always opens a fresh draft. Counters and request logs
// are compared as deltas taken inside each test, never as absolute numbers.
// Re-running the file in the same session after an owner exists fails the
// draft scenarios at a clear precondition: start a fresh browser fixture session.

const explicitBaseURL = process.env.BANSOKO_TEST_BASE_URL?.trim();
const providerBaseURL = process.env.BANSOKO_BROWSER_FIXTURE_PROVIDER_BASE_URL?.trim();
const countersURL = process.env.BANSOKO_BROWSER_FIXTURE_COUNTERS_URL?.trim();
const providerEntryRef = process.env.BANSOKO_BROWSER_PROVIDER_ENTRY_REF?.trim();
const providerGroupName = process.env.BANSOKO_BROWSER_PROVIDER_GROUP_NAME?.trim();
const providerGroupSlug = process.env.BANSOKO_BROWSER_PROVIDER_GROUP_SLUG?.trim();
const providerShowTitle = process.env.BANSOKO_BROWSER_PROVIDER_SHOW_TITLE?.trim();
const providerReleaseTitle = process.env.BANSOKO_BROWSER_PROVIDER_RELEASE_TITLE?.trim();

function livePrerequisite(): string | undefined {
  if (process.env.BANSOKO_E2E_FIXTURES === "1") return "provider feed evidence does not run in fixture mode";
  if (process.env.BANSOKO_TEST_DB_DISPOSABLE !== "1") return "BANSOKO_TEST_DB_DISPOSABLE=1 is required";
  if (process.env.BANSOKO_E2E_REAL_EFFECT !== "1") return "BANSOKO_E2E_REAL_EFFECT=1 is required";
  if (!explicitBaseURL) return "BANSOKO_TEST_BASE_URL must point at the live authenticated server";
  if (!providerBaseURL || !countersURL || !providerEntryRef || !providerGroupName || !providerGroupSlug || !providerShowTitle) {
    return "provider fixture environment (base URL, counters, entry ref, group, title) is required";
  }
  return undefined;
}

type FixtureCounters = Record<string, number>;

async function readCounters(page: Page): Promise<FixtureCounters> {
  const response = await page.request.get(countersURL!);
  expect(response.status()).toBe(200);
  return (await response.json()) as FixtureCounters;
}

function feedFetches(counters: FixtureCounters): number {
  return counters["/rss/anime.xml"] ?? 0;
}

type CandidateBody = { id?: string; current_revision?: number | null };

type RSSListViewRequest = {
  mode?: string;
  input_source?: string;
  managed_anime_id?: string;
  rules_preview?: unknown;
  draft?: {
    rules?: {
      sections?: {
        file_filter?: { mode?: string; value?: { must_contain?: string; not_in?: string } };
      };
    };
  };
  feed?: Array<{
    provider?: string;
    provider_entry_ref?: string;
    provider_group_ref?: string;
    snapshot_id?: string;
    snapshot_version?: number;
    content_hash?: string;
  }>;
};

type SnapshotBody = {
  freshness?: string;
  snapshot_id?: string | null;
  snapshot_version?: number | null;
  content_hash?: string | null;
};

/**
 * Records every composed-read request body so the spec can prove WHICH identity
 * the browser sent. The composed read is the draft-side half of the
 * snapshot-identity contract; the provider fetch counters cover the live-read
 * half, and the rendered group/seed is the server-side acceptance.
 */
function recordRSSListViewRequests(page: Page): {
  all: () => RSSListViewRequest[];
  feeds: () => NonNullable<RSSListViewRequest["feed"]>;
} {
  const bodies: RSSListViewRequest[] = [];
  page.on("request", (request) => {
    const pathname = new URL(request.url()).pathname;
    if (request.method() === "POST" && pathname.endsWith("/api/v1/anime/rss-list-view")) {
      bodies.push(request.postDataJSON() as RSSListViewRequest);
    }
  });
  return {
    all: () => [...bodies],
    feeds: () => bodies.flatMap((body) => body.feed ?? []),
  };
}

/** Records provider-entry feed reads; GET is a pure snapshot read, POST fetches provider. */
function recordProviderFeedReads(page: Page): { count: (method: "GET" | "POST") => number } {
  const reads: string[] = [];
  page.on("response", (response) => {
    const pathname = new URL(response.url()).pathname;
    if (pathname.includes("/catalog/providers/") && pathname.endsWith("/rss")) {
      reads.push(response.request().method());
    }
  });
  return { count: (method) => reads.filter((entry) => entry === method).length };
}

async function saveAniBTConnection(page: Page): Promise<void> {
  await page.goto("/#/settings/connections");
  const dialog = page.getByRole("dialog").first();
  await expect(dialog).toBeVisible({ timeout: 15000 });
  const row = dialog.locator("li").filter({ hasText: "AniBT" }).first();
  await expect(row).toBeVisible({ timeout: 15000 });
  await row.getByRole("button", { name: "编辑", exact: true }).click();
  const editor = page.getByRole("dialog").last();
  await expect(editor).toBeVisible();
  await editor.getByLabel("站点地址").fill(providerBaseURL!);
  await editor.getByRole("button", { name: "保存", exact: true }).click();
  await expect(page.getByText("连接已保存", { exact: true })).toBeVisible({ timeout: 15000 });
  await expect(dialog.locator("li").filter({ hasText: "AniBT" }).first()).toContainText(providerBaseURL!);
  await page.keyboard.press("Escape");
}

function providerCard(page: Page): Locator {
  return page.getByRole("article").filter({ hasText: providerShowTitle! });
}

async function openProviderTab(page: Page): Promise<Locator> {
  await page.getByRole("button", { name: "AniBT", exact: true }).first().click();
  const refresh = page.getByRole("button", { name: "刷新 AniBT", exact: true });
  if (await refresh.count()) {
    await refresh.click();
  }
  const card = providerCard(page);
  await expect(card.first()).toBeVisible({ timeout: 30000 });
  await expect(card.first().getByText(providerShowTitle!, { exact: true })).toBeVisible();
  return card.first();
}

async function openProviderDialog(page: Page): Promise<Locator> {
  const card = await openProviderTab(page);
  // Draft scenarios need the fixture entry unsubscribed: an active owner's card
  // (已订阅) opens the owner workspace instead of a draft.
  await expect(
    card.getByText("已订阅", { exact: true }),
    "draft scenarios need the fixture entry unsubscribed; start a fresh browser fixture session",
  ).toHaveCount(0);
  // A coordinate click can land on the freshly mounted dialog when the modal
  // appears under the pointer (click-through), which would fire a panel action
  // the flow never asked for. Dispatch the click on the button itself: same
  // product path, no coordinate routing.
  await card.getByRole("button", { name: `打开配置 ${providerShowTitle!}`, exact: true }).dispatchEvent("click");
  const dialog = page.getByRole("dialog").last();
  await expect(dialog.getByRole("tab", { name: "RSS 预览", exact: true })).toBeVisible({ timeout: 30000 });
  return dialog;
}

async function closeDialog(page: Page): Promise<void> {
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0, { timeout: 15000 });
}

function candidateMutationResponse(page: Page): Promise<Response> {
  return page.waitForResponse(
    (response) => {
      const pathname = new URL(response.url()).pathname;
      const isCandidate = pathname.includes("/api/v1/anime-config-candidates");
      const method = response.request().method();
      return (
        (method === "POST" || method === "PATCH") &&
        (isCandidate && (pathname.endsWith("/anime-config-candidates") || /\/anime-config-candidates\/[^/]+$/.test(pathname))) &&
        response.status() >= 200 &&
        response.status() < 300
      );
    },
    { timeout: 30000 },
  );
}

function snapshotRefreshResponse(page: Page): Promise<Response> {
  return page.waitForResponse(
    (response) => {
      const pathname = new URL(response.url()).pathname;
      return pathname.endsWith("/rss") && pathname.includes("/catalog/providers/") && response.request().method() === "POST";
    },
    { timeout: 30000 },
  );
}

async function discoverGroups(dialog: Locator, page: Page): Promise<void> {
  await dialog.getByRole("tab", { name: "字幕组", exact: true }).click();
  await dialog.getByRole("button", { name: "刷新字幕组", exact: true }).click();
  await expect(page.getByText(/已发现 \d+ 个字幕组/)).toBeVisible({ timeout: 30000 });
  // Select the discovered group explicitly (必选): the saved revision must then
  // carry an effective-selected provider group, which is what makes the exact
  // feed snapshot identity mandatory for a composed read.
  const policy = dialog.getByRole("radiogroup", { name: `${providerGroupName!} 选择策略` });
  await expect(policy).toBeVisible({ timeout: 15000 });
  // The discovery toast can sit over the row, and a hovering pointer pauses
  // its auto-dismiss (so click retries would keep it alive): move away and let
  // the toasts leave before clicking.
  await page.mouse.move(0, 0);
  await expect(page.locator("[data-sonner-toast]")).toHaveCount(0, { timeout: 20000 });
  await policy.getByRole("radio", { name: "必选", exact: true }).click();
}

/**
 * One explicit preview. Identities the panel does not hold yet are resolved from
 * the persisted snapshot first (pure read, no provider traffic); only a group
 * with no persisted snapshot costs one provider fetch. Either way the panel then
 * re-posts exactly once with the resolved identity.
 *
 * `acquireExpected`: true = the provider must be fetched once (no persisted
 * snapshot yet); false = it must not; "any" = either is fine and the result
 * reports which happened (a scenario that cannot know whether an earlier
 * scenario already persisted the snapshot).
 */
async function previewDraft(
  dialog: Locator,
  page: Page,
  acquireExpected: boolean | "any",
): Promise<{ requests: RSSListViewRequest[]; snapshot?: SnapshotBody; acquired: boolean }> {
  const requests: RSSListViewRequest[] = [];
  const responses: Response[] = [];
  const isRead = (method: string, url: string) => method === "POST" && new URL(url).pathname.endsWith("/api/v1/anime/rss-list-view");
  const listener = (request: Request) => {
    if (isRead(request.method(), request.url())) requests.push(request.postDataJSON() as RSSListViewRequest);
  };
  // Responses are collected by a listener rather than a late waitForResponse:
  // the automatic read and its snapshot-resolved re-read can both finish
  // inside the settle window below.
  const responseListener = (response: Response) => {
    if (isRead(response.request().method(), response.url())) responses.push(response);
  };
  page.on("request", listener);
  page.on("response", responseListener);
  const nthResponse = async (n: number): Promise<Response> => {
    await expect.poll(() => responses.length, { timeout: 60000 }).toBeGreaterThanOrEqual(n);
    return responses[n - 1]!;
  };
  const acquisition = acquireExpected === false ? undefined : snapshotRefreshResponse(page);
  const acquired: { response?: Response } = {};
  // A wait nobody awaits (the "any" case without acquisition) must not surface
  // its timeout as an unhandled rejection.
  acquisition?.then((response) => { acquired.response = response; }, () => undefined);
  // The read lives behind the RSS panel. Since 9b5b45a6 (2026-10-03, before
  // the Base UI migration) the panel reads by itself the first time its tab
  // is activated for an observation, so one "preview" is either that
  // automatic read or, when the tab was already loaded, an explicit 预览 —
  // never both (the explicit click during the automatic read added a third
  // rss-list-view read in B8).
  await dialog.getByRole("tab", { name: "RSS 预览", exact: true }).click();
  await dialog.page().waitForTimeout(1500);
  if (requests.length === 0) {
    await dialog.getByRole("button", { name: "预览", exact: true }).click();
  }
  const firstResponse = await nthResponse(1);
  expect(firstResponse.status(), await firstResponse.text()).toBe(200);
  const firstBody = (await firstResponse.json()) as {
    groups?: Array<{ scope?: { provider_group_ref?: string }; feed?: { state?: string } }>;
  };
  const required = firstBody.groups?.filter((group) => group.feed?.state === "snapshot_required") ?? [];
  let snapshot: SnapshotBody | undefined;
  if (required.length > 0) {
    if (acquireExpected === true) {
      // No persisted snapshot exists yet: the panel must fetch the provider once.
      expect(acquisition).toBeDefined();
      const response = await acquisition!;
      expect(response.status()).toBe(200);
      snapshot = (await response.json()) as SnapshotBody;
    }
    const secondResponse = await nthResponse(2);
    expect(secondResponse.status(), await secondResponse.text()).toBe(200);
    await expect.poll(() => requests.length, { timeout: 30000 }).toBe(2);
    expect(requests[1]?.feed).toEqual([
      expect.objectContaining({
        provider: "anibt",
        provider_entry_ref: providerEntryRef,
        provider_group_ref: required[0]?.scope?.provider_group_ref,
      }),
    ]);
    // The acquisition response precedes the re-read that carries its identity,
    // so by now it has been observed if it happened at all.
    if (acquireExpected === "any" && acquired.response) {
      expect(acquired.response.status()).toBe(200);
      snapshot = (await acquired.response.json()) as SnapshotBody;
    }
  } else {
    expect(acquireExpected).not.toBe(true);
    expect(requests).toHaveLength(1);
  }
  page.off("request", listener);
  page.off("response", responseListener);
  return { requests, snapshot, acquired: snapshot !== undefined };
}

type SnapshotIdentity = { snapshot_id: string; snapshot_version: number; content_hash: string };

/** The exact persisted identity the last composed read carried. */
function identityOf(preview: { requests: RSSListViewRequest[] }): SnapshotIdentity {
  const feed = preview.requests.at(-1)?.feed?.[0];
  expect(feed?.snapshot_id, "the re-read must carry the resolved snapshot identity").toBeTruthy();
  return {
    snapshot_id: feed!.snapshot_id!,
    snapshot_version: feed!.snapshot_version!,
    content_hash: feed!.content_hash!,
  };
}

/** Expands the discovered group: the workspace renders groups → seeds → files
 *  directly, with no intermediate "查看种子" layer. */
async function expandGroup(dialog: Locator): Promise<void> {
  const groupButton = dialog.getByRole("button", { name: `展开 ${providerGroupName!}`, exact: true });
  await expect(groupButton).toBeVisible({ timeout: 30000 });
  await groupButton.click();
}

async function makeDraftDirty(dialog: Locator, regex: string): Promise<void> {
  await dialog.getByRole("tab", { name: "规则", exact: true }).click();
  // The episode-recognition input only renders under the regex parser; the
  // provider default (reltitle) has no regex to edit.
  const regexParser = dialog.getByRole("radio", { name: "正则", exact: true });
  if (!(await regexParser.isChecked())) {
    await regexParser.click();
  }
  const patterns = dialog.getByRole("textbox", { name: "集数识别规则", exact: true });
  await patterns.fill(regex);
  await patterns.press("Enter");
}

async function setAnimeFileNotIn(page: Page, managedAnimeID: string, notIn: string): Promise<void> {
  // The session is an HttpOnly cookie: GETs need no header, the PATCH below
  // needs only the double-submit echo.
  const headers = {};
  const workspaceResponse = await page.request.get(`/api/v1/anime/${managedAnimeID}/workspace`, { headers });
  expect(workspaceResponse.status(), await workspaceResponse.text()).toBe(200);
  const workspace = (await workspaceResponse.json()) as {
    rules: {
      version: number;
      sections: Record<string, unknown>;
      effective: { file_filter: { must_contain: string; not_in: string } };
    };
  };
  const rules = workspace.rules;
  const response = await page.request.patch(`/api/v1/anime/${managedAnimeID}/rules`, {
    headers: { ...headers, "If-Match": String(rules.version), "X-CSRF-Token": await csrfToken(page) },
    data: {
      expected_version: rules.version,
      sections: {
        ...rules.sections,
        file_filter: {
          mode: "replace",
          value: { ...rules.effective.file_filter, not_in: notIn },
        },
      },
    },
  });
  expect(response.status(), await response.text()).toBe(200);
}

function subscribeResponse(page: Page): Promise<Response> {
  return page.waitForResponse(
    (response) => {
      const pathname = new URL(response.url()).pathname;
      return (
        pathname.includes("/api/v1/anime-config-candidates/") &&
        pathname.endsWith("/subscribe") &&
        response.request().method() === "POST"
      );
    },
    { timeout: 30000 },
  );
}


const FILE_FILTER_TARGET = "Browser Fixture Show - 01";

/** A parser pattern no earlier save in this session can equal, so the edit is
 *  always a real change (the draft is only dirty when it differs from the saved
 *  revision). The alternation keeps matching exactly what `base` matches. */
function uniqueParserPattern(base: string): string {
  return `${base}|q${Date.now()}`;
}

async function startSession(page: Page): Promise<void> {
  await page.goto("/");
  await expect(page.getByRole("navigation", { name: "主导航" })).toBeVisible({ timeout: 15000 });
}

/** Escape on a dirty draft hits the discard guard; leave without saving. */
async function discardAndClose(page: Page): Promise<void> {
  await page.keyboard.press("Escape");
  const guard = page.getByRole("alertdialog");
  await expect(guard.getByText("放弃未保存的修改？", { exact: true })).toBeVisible({ timeout: 15000 });
  await guard.getByRole("button", { name: "放弃修改", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0, { timeout: 15000 });
}

async function saveDraft(dialog: Locator, page: Page): Promise<CandidateBody> {
  await expect(dialog.getByRole("button", { name: "保存配置", exact: true })).toBeEnabled({ timeout: 15000 });
  const saved = candidateMutationResponse(page);
  await dialog.getByRole("button", { name: "保存配置", exact: true }).click();
  await expect(dialog.getByText("已保存", { exact: true })).toBeVisible({ timeout: 30000 });
  return (await (await saved).json()) as CandidateBody;
}

/**
 * Setup shared by every scenario after the first: a saved draft revision whose
 * selected group has a persisted feed snapshot. It does whatever the session
 * still needs (acquire the snapshot, or resolve the one an earlier scenario
 * persisted) and leaves no dialog open.
 */
async function prepareSavedDraft(page: Page): Promise<{
  candidateID: string;
  revision: number;
  snapshot: SnapshotIdentity;
}> {
  await saveAniBTConnection(page);
  const dialog = await openProviderDialog(page);
  await discoverGroups(dialog, page);
  await makeDraftDirty(dialog, uniqueParserPattern("\\d+"));
  const candidate = await saveDraft(dialog, page);
  expect(candidate.id).toBeTruthy();
  expect(candidate.current_revision).toBeGreaterThanOrEqual(1);
  const preview = await previewDraft(dialog, page, "any");
  const snapshot = identityOf(preview);
  await closeDialog(page);
  return { candidateID: candidate.id!, revision: candidate.current_revision!, snapshot };
}

async function authHeaders(_page: Page): Promise<Record<string, string>> {
  // The browser context attaches the session cookie itself.
  return {};
}

/** The active owner of the fixture provider entry, if any (server read). */
async function findActiveOwner(page: Page): Promise<string | undefined> {
  const response = await page.request.get("/api/v1/anime", { headers: await authHeaders(page) });
  expect(response.status(), await response.text()).toBe(200);
  const items = (await response.json()) as Array<{
    managed_anime: { id: string; title?: string };
    subscription: { origin?: string; origin_entry_ref?: string | null; lifecycle_state?: string };
  }>;
  return items.find(
    (item) =>
      item.subscription.origin === "anibt" &&
      item.subscription.lifecycle_state === "active" &&
      (item.subscription.origin_entry_ref === providerEntryRef || item.managed_anime.title === providerShowTitle),
  )?.managed_anime.id;
}

async function subscribeFromCard(page: Page, card: Locator): Promise<{ ownerID: string; candidateID: string }> {
  const subscribed = subscribeResponse(page);
  // The card has no 订阅 button: clicking it opens the draft (a fresh one for a
  // retired association) and subscribing happens in the dialog.
  await card.getByRole("button", { name: `打开配置 ${providerShowTitle!}`, exact: true }).dispatchEvent("click");
  const draft = page.getByRole("dialog").last();
  await expect(draft.getByRole("button", { name: "订阅", exact: true })).toBeEnabled({ timeout: 30000 });
  await draft.getByRole("button", { name: "订阅", exact: true }).click();
  const response = await subscribed;
  expect([200, 201]).toContain(response.status());
  const body = (await response.json()) as { managed_anime: { id: string } };
  expect(body.managed_anime.id).toBeTruthy();
  // The owner scenarios read a working owner's RSS surface; a new subscription
  // starts 未开始 and its RSS tab is the pre-start test bench until started.
  await startFromSubscribedToast(page, body.managed_anime.id);
  return {
    ownerID: body.managed_anime.id,
    candidateID: decodeURIComponent(new URL(response.url()).pathname.split("/").at(-2) ?? ""),
  };
}

/**
 * The owner scenarios' setup: reuse the fixture entry's active owner, else
 * create one. The entry gets a saved draft with a persisted snapshot first (so
 * the owner has a group and seeds); this works the same for a never-subscribed
 * entry and for a retired one, because a provider browse card carries no
 * subscription fields and always opens a fresh draft.
 */
async function ensureActiveOwner(page: Page): Promise<{ ownerID: string; candidateID?: string }> {
  const existing = await findActiveOwner(page);
  if (existing) return { ownerID: existing };
  await saveAniBTConnection(page);
  await openProviderTab(page);
  await prepareSavedDraft(page);
  const card = await openProviderTab(page);
  return subscribeFromCard(page, card);
}

/** The fixture entry's not-retired card in the 已订阅 library. */
function activeLibraryCard(page: Page): Locator {
  return page
    .getByRole("article")
    .filter({ hasText: providerShowTitle! })
    .filter({ hasNot: page.getByText("已结束", { exact: true }) })
    .first();
}

function retiredLibraryCards(page: Page): Locator {
  return page.getByRole("article").filter({ hasText: providerShowTitle! }).filter({ hasText: "已结束" });
}

async function openOwnerDialog(page: Page): Promise<Locator> {
  await page.getByRole("button", { name: "已订阅", exact: true }).first().click();
  const ownerCard = activeLibraryCard(page);
  await expect(ownerCard).toBeVisible({ timeout: 30000 });
  await ownerCard.getByRole("button", { name: `打开配置 ${providerShowTitle!}`, exact: true }).dispatchEvent("click");
  return page.getByRole("dialog").last();
}

test.describe("provider feed snapshot identity (authenticated disposable)", () => {
  // Independent tests: no serial mode. A failure here never skips the others.
  test.describe.configure({ retries: 0 });
  test.beforeEach(({ isMobile }) => {
    test.skip(isMobile, "the destructive real-effect chain runs once on desktop");
    test.skip(shouldSkipAuthTests(), "auth.setup did not receive BANSOKO_E2E_USERNAME/BANSOKO_E2E_PASSWORD");
    const reason = livePrerequisite();
    test.skip(reason !== undefined, reason);
  });

  // ---- draft scenarios (the fixture entry must still be unsubscribed) --------

  test("draft: first preview acquires the snapshot once, reopening reuses it without a provider fetch", async ({ page }) => {
    test.setTimeout(300000);
    const listViewRequests = recordRSSListViewRequests(page);
    const feedReads = recordProviderFeedReads(page);
    await startSession(page);

    // --- real saved provider connection ------------------------------------
    await saveAniBTConnection(page);
    const fetchesBefore = feedFetches(await readCounters(page));

    // --- provider browse → draft → group discovery -------------------------
    const dialog = await openProviderDialog(page);
    await discoverGroups(dialog, page);

    // Edits only configure the draft. The first explicit preview discovers
    // snapshot_required, acquires that group (or resolves the snapshot an
    // earlier scenario already persisted), then re-reads its exact identity.
    await makeDraftDirty(dialog, uniqueParserPattern("\\d+"));
    const candidate = await saveDraft(dialog, page);
    expect(candidate.id).toBeTruthy();
    expect(candidate.current_revision).toBeGreaterThanOrEqual(1);
    expect(listViewRequests.all()).toHaveLength(0);
    expect(feedReads.count("POST")).toBe(0);
    expect(feedFetches(await readCounters(page))).toBe(fetchesBefore);

    const first = await previewDraft(dialog, page, "any");
    // A fixture the provider was never fetched from has no persisted snapshot.
    if (fetchesBefore === 0) expect(first.acquired).toBe(true);
    const providerFetches = first.acquired ? 1 : 0;
    const snapshot1 = identityOf(first);
    if (first.acquired) {
      expect(first.snapshot?.freshness).toBe("fresh");
      expect(first.snapshot?.snapshot_id).toBe(snapshot1.snapshot_id);
      expect(first.snapshot?.snapshot_version).toBe(snapshot1.snapshot_version);
      expect(first.snapshot?.content_hash).toBe(snapshot1.content_hash);
      if (fetchesBefore === 0) expect(snapshot1.snapshot_version).toBe(1);
    }
    expect(snapshot1.content_hash).toMatch(/^sha256:[0-9a-f]{64}$/);
    expect(first.requests).toHaveLength(2);
    expect(first.requests[0]).toMatchObject({ input_source: "current_input" });
    expect(first.requests[1]?.feed).toHaveLength(1);
    expect(first.requests[1]?.feed?.[0]).toMatchObject({
      provider: "anibt",
      provider_entry_ref: providerEntryRef,
      provider_group_ref: providerGroupSlug,
      ...snapshot1,
    });
    await expect(dialog.getByText(/共 2 个种子/)).toBeVisible({ timeout: 30000 });
    await expandGroup(dialog);
    // The draft RSS tab is the subscription test bench: both seeds are listed,
    // and it offers no download (one-shots start from 浏览种子).
    await expect(dialog.locator("li.seed-row").filter({ hasText: providerReleaseTitle! })).toBeVisible({ timeout: 15000 });
    await expect(dialog.locator("li.seed-row")).toHaveCount(2);
    await expect(dialog.getByRole("button", { name: /^下载 / })).toHaveCount(0);
    expect(feedReads.count("POST")).toBe(providerFetches);
    expect(feedFetches(await readCounters(page))).toBe(fetchesBefore + providerFetches);
    await closeDialog(page);

    // --- reopen the same saved revision + snapshot: no live provider fetch ---
    const reopenDialog = await openProviderDialog(page);
    // previewDraft activates the tab itself so the automatic read is captured.
    const reopened = await previewDraft(reopenDialog, page, false);
    // Reopening loses the in-memory identity, so the panel re-resolves it from the
    // persisted snapshot (pure read) and re-posts once: 2 posts, still no fetch.
    expect(reopened.requests).toHaveLength(2);
    expect(reopened.requests[1]?.feed).toEqual([expect.objectContaining(snapshot1)]);
    await expect(reopenDialog.getByText(/共 2 个种子/)).toBeVisible({ timeout: 30000 });
    expect(feedReads.count("POST")).toBe(providerFetches);
    expect(feedFetches(await readCounters(page))).toBe(fetchesBefore + providerFetches);
    await closeDialog(page);
  });

  test("draft: a saved parser edit stays stale until an explicit preview", async ({ page }) => {
    test.setTimeout(300000);
    const listViewRequests = recordRSSListViewRequests(page);
    const feedReads = recordProviderFeedReads(page);
    await startSession(page);
    const saved = await prepareSavedDraft(page);
    const postsBefore = feedReads.count("POST");
    const fetchesBefore = feedFetches(await readCounters(page));

    const editDialog = await openProviderDialog(page);
    // This dialog session starts with no shown result, so establish one first:
    // only an existing result can be stale.
    const initialPreview = await previewDraft(editDialog, page, false);
    expect(initialPreview.requests).toHaveLength(2);
    await expect(editDialog.getByText(/共 2 个种子/)).toBeVisible({ timeout: 30000 });
    const readsBeforeParserEdit = listViewRequests.all().length;
    const providerReadsBeforeParserEdit = feedReads.count("GET");
    await makeDraftDirty(editDialog, uniqueParserPattern("\\d{2}"));
    const edited = await saveDraft(editDialog, page);
    expect(edited.id).toBe(saved.candidateID);
    expect(edited.current_revision).toBe(saved.revision + 1);
    await editDialog.getByRole("tab", { name: "RSS 预览", exact: true }).click();
    await expect(editDialog.getByText(/配置已修改，点击「预览」刷新/)).toBeVisible({ timeout: 15000 });
    await page.waitForTimeout(700);
    expect(listViewRequests.all()).toHaveLength(readsBeforeParserEdit);
    expect(feedReads.count("GET")).toBe(providerReadsBeforeParserEdit);
    const parserPreview = await previewDraft(editDialog, page, false);
    expect(parserPreview.requests).toHaveLength(1);
    expect(parserPreview.requests[0]).toMatchObject({
      input_source: "current_input",
      feed: [expect.objectContaining({ snapshot_id: saved.snapshot.snapshot_id, snapshot_version: saved.snapshot.snapshot_version })],
    });
    expect(feedReads.count("POST")).toBe(postsBefore);
    expect(feedFetches(await readCounters(page))).toBe(fetchesBefore);
    await expect(editDialog.getByText(/共 2 个种子/)).toBeVisible({ timeout: 30000 });
    await expect(editDialog.getByText(/配置已修改，点击「预览」刷新/)).not.toBeVisible();
    await closeDialog(page);
  });

  test("draft: a file-filter edit stays stale until an explicit preview, then filters the seed file", async ({ page }) => {
    test.setTimeout(300000);
    const listViewRequests = recordRSSListViewRequests(page);
    const feedReads = recordProviderFeedReads(page);
    await startSession(page);
    await prepareSavedDraft(page);
    const fetchesBefore = feedFetches(await readCounters(page));

    const fileDialog = await openProviderDialog(page);
    // No shown result yet in this session, so create one to stale.
    const initialFilterPreview = await previewDraft(fileDialog, page, false);
    expect(initialFilterPreview.requests).toHaveLength(2);
    await expect(fileDialog.getByText(/共 2 个种子/)).toBeVisible({ timeout: 30000 });
    await fileDialog.getByRole("tab", { name: "规则", exact: true }).click();
    const fileSection = fileDialog.locator("section").filter({ hasText: "文件筛选" }).first();
    await expect(fileSection).toBeVisible({ timeout: 15000 });
    const coverOverride = fileSection.getByRole("radio", { name: "自定义", exact: true });
    if (await coverOverride.count()) {
      await coverOverride.click();
    }
    // Match the fixture file itself, not its release title.
    const excludeInput = fileSection.getByLabel("文件名不能包含");
    const readsBeforeEdit = listViewRequests.all().length;
    const providerReadsBeforeEdit = feedReads.count("GET");
    await excludeInput.fill(FILE_FILTER_TARGET);
    await excludeInput.press("Enter");
    // The staleness notice lives in the RSS panel, so it is only visible once
    // that tab is active; switching tabs must not read either.
    await fileDialog.getByRole("tab", { name: "RSS 预览", exact: true }).click();
    await expect(fileDialog.getByText(/配置已修改，点击「预览」刷新/)).toBeVisible({ timeout: 15000 });
    await page.waitForTimeout(700);
    expect(listViewRequests.all()).toHaveLength(readsBeforeEdit);
    expect(feedReads.count("GET")).toBe(providerReadsBeforeEdit);
    expect(feedFetches(await readCounters(page))).toBe(fetchesBefore);
    const filteredPreview = await previewDraft(fileDialog, page, false);
    expect(filteredPreview.requests).toHaveLength(1);
    expect(filteredPreview.requests[0]).toMatchObject({ input_source: "current_input" });
    expect(feedFetches(await readCounters(page))).toBe(fetchesBefore);
    await expect
      .poll(
        () => listViewRequests.all().some((body) =>
          body.draft?.rules?.sections?.file_filter?.value?.not_in === FILE_FILTER_TARGET,
        ),
        { timeout: 30000, message: "explicit preview must read the edited file filter" },
      )
      .toBe(true);
    await expandGroup(fileDialog);
    const acceptedSeed = fileDialog.locator("li.seed-row").filter({ hasText: providerReleaseTitle! }).first();
    await expect(acceptedSeed).toBeVisible({ timeout: 30000 });
    await acceptedSeed.getByRole("button", { name: "展开文件与状态", exact: true }).click();
    // Seeds expand lazily: opening the row only shows the explicit fetch, and the
    // file rows (with the draft's per-file verdict) come from that fetch.
    await acceptedSeed.getByRole("button", { name: "展开查看文件", exact: true }).click();
    const excludedFile = acceptedSeed.locator("li.seed-file-row").first();
    await expect(excludedFile).toBeVisible({ timeout: 30000 });
    await expect(excludedFile.getByText(/已过滤（文件名包含排除词）/)).toBeVisible({ timeout: 15000 });
    // The filter is a draft-only probe: it is discarded, never saved, so the
    // saved revision (and the owner a later scenario subscribes) stays clean
    // and this scenario can run again.
    await discardAndClose(page);
  });

  // ---- owner scenarios (each reuses or creates the entry's active owner) ------

  test("owner: file-filter verdict comes from the owner rules and the rules preview is read-only", async ({ page }) => {
    test.setTimeout(300000);
    const feedReads = recordProviderFeedReads(page);
    await startSession(page);
    const { ownerID } = await ensureActiveOwner(page);

    // --- owner file-filter verdict and rules preview are server reads --------
    await setAnimeFileNotIn(page, ownerID, FILE_FILTER_TARGET);
    // Open the subscribed owner from its card; the owner RSS read must expose
    // the file-filter verdict from the persisted owner rules.
    const rulesDialog = await openOwnerDialog(page);
    await rulesDialog.getByRole("tab", { name: "RSS", exact: true }).click();
    await expandGroup(rulesDialog);
    // Per-file verdicts live inside the seed row's detail panel, so expand it.
    const ownerSeed = rulesDialog.locator("li.seed-row").filter({ hasText: providerReleaseTitle! }).first();
    await expect(ownerSeed).toBeVisible({ timeout: 30000 });
    await ownerSeed.getByRole("button", { name: "展开文件与状态", exact: true }).click();
    // The owner file row states the filter outcome as a badge and names the
    // exact rule in its hover text, so assert both on that row.
    const excludedOwnerFile = ownerSeed.getByText("规则排除", { exact: true }).first();
    await expect(excludedOwnerFile).toBeVisible({ timeout: 30000 });
    await expect(excludedOwnerFile).toHaveAttribute("title", "文件名包含排除词");
    await expect(ownerSeed.getByText(/Browser Fixture Show - 01 \[1080p\]\.mkv/).first()).toBeVisible();

    // Owner rules preview is a read-only owner_draft read and cannot fetch the provider.
    const countersBeforeRulesPreview = await readCounters(page);
    const providerPostCount = feedReads.count("POST");
    const providerGetCount = feedReads.count("GET");
    // Bind the response to the owner_draft request itself: other owner-mode reads
    // of the same endpoint can be in flight while the dialog is open.
    const ownerDraftRequest = page.waitForRequest((request) => {
      const pathname = new URL(request.url()).pathname;
      if (request.method() !== "POST" || !pathname.endsWith("/api/v1/anime/rss-list-view")) return false;
      return (request.postDataJSON() as { input_source?: string }).input_source === "owner_draft";
    });
    await rulesDialog.getByRole("tab", { name: "规则", exact: true }).click();
    await rulesDialog.getByRole("button", { name: "预览规则", exact: true }).click();
    const ownerDraftRequestSent = await ownerDraftRequest;
    const ownerDraftResponse = await ownerDraftRequestSent.response();
    expect(ownerDraftResponse?.status()).toBe(200);
    const ownerDraftInput = ownerDraftRequestSent.postDataJSON() as {
      input_source?: string;
      managed_anime_id?: string;
      rules_preview?: unknown;
    };
    expect(ownerDraftInput).toMatchObject({ input_source: "owner_draft", managed_anime_id: ownerID });
    expect(ownerDraftInput.rules_preview).toBeTruthy();
    const ownerDraftBody = (await ownerDraftResponse!.json()) as {
      mode?: string;
      groups?: Array<{ items?: Array<{ download_action?: { kind?: string; reason_code?: string; reason?: string } }> }>;
    };
    expect(ownerDraftBody.mode).toBe("owner_draft");
    const ownerDraftItems = ownerDraftBody.groups?.flatMap((group) => group.items ?? []) ?? [];
    expect(ownerDraftItems.length).toBeGreaterThan(0);
    expect(ownerDraftItems.every((item) =>
      item.download_action?.kind === "none" &&
      item.download_action.reason_code === "preview_mode" &&
      item.download_action.reason === "Unsaved rules preview is read-only.",
    )).toBe(true);
    await expect(rulesDialog.getByText("预览模式", { exact: true })).toBeVisible({ timeout: 30000 });
    await expect(rulesDialog.getByText("预览模式下不可操作", { exact: true }).first()).toBeVisible({ timeout: 15000 });
    const disabledActionButtons = rulesDialog.locator('button[title="预览模式下不可操作"]');
    const actionCount = await disabledActionButtons.count();
    expect(actionCount).toBeGreaterThan(0);
    for (const button of await disabledActionButtons.all()) {
      await expect(button).toBeDisabled();
    }
    expect(feedReads.count("POST")).toBe(providerPostCount);
    expect(feedReads.count("GET")).toBe(providerGetCount);
    expect(await readCounters(page)).toEqual(countersBeforeRulesPreview);
    await closeDialog(page);
  });

  test("owner: 显示被过滤 is a presentation-only switch that never reads the provider", async ({ page }) => {
    test.setTimeout(300000);
    await startSession(page);
    await ensureActiveOwner(page);

    // Item-level hiding by verdict is covered by the unit suite: this fixture's
    // releases stay at 等待匹配元数据 (metadata hold), so a release-filter exclusion
    // never reaches a persisted `filtered` verdict to hide. What the owner
    // surface must show here is the server-driven switch itself, and that using
    // it costs no read of the provider.
    expect(providerReleaseTitle, "the fixture must expose the accepted release title").toBeTruthy();
    const filterDialog = await openOwnerDialog(page);
    // The subscribed owner's workspace uses the short RSS tab label; only the
    // pre-subscription draft calls it "RSS 预览".
    await expect(filterDialog.getByRole("tab", { name: "RSS", exact: true })).toBeVisible({ timeout: 30000 });
    await filterDialog.getByRole("tab", { name: "RSS", exact: true }).click();
    await expandGroup(filterDialog);
    await expect(filterDialog.getByText(providerReleaseTitle!, { exact: false }).first()).toBeVisible({
      timeout: 30000,
    });
    const countersBeforeReveal = await readCounters(page);
    // The subscribed owner tab renders the filter switch as a labelled checkbox
    // (the pre-subscription draft uses a button with the same text).
    const filterSwitch = filterDialog.getByRole("checkbox", { name: "显示被过滤" });
    await expect(filterSwitch).toBeVisible();
    await filterSwitch.check();
    await expect(filterSwitch).toBeChecked();
    expect(await readCounters(page)).toEqual(countersBeforeReveal);
    await closeDialog(page);
  });

  // Last on purpose: retiring is one-way for the fixture entry (the browse card
  // that comes back opens a fresh draft), and this scenario leaves a fresh
  // active owner behind.
  test("owner: retiring keeps the owner readable and the browse card re-subscribes a fresh owner", async ({ page }) => {
    test.setTimeout(300000);
    await startSession(page);
    const first = await ensureActiveOwner(page);

    // --- retire the active owner ---------------------------------------------
    await page.getByRole("button", { name: "已订阅", exact: true }).first().click();
    const libraryCard = activeLibraryCard(page);
    await expect(libraryCard).toBeVisible({ timeout: 30000 });
    const retiredBefore = await retiredLibraryCards(page).count();
    // 管理动作收在卡片右上 ⋯ 菜单里（海报墙 UI-038）。
    await libraryCard.getByRole("button", { name: `更多操作 ${providerShowTitle!}`, exact: true }).click();
    await page.getByRole("menuitem", { name: "结束订阅", exact: true }).click();
    await page.getByRole("button", { name: "结束订阅", exact: true }).click();
    await expect(retiredLibraryCards(page)).toHaveCount(retiredBefore + 1, { timeout: 30000 });
    // A retired owner offers neither retire nor stop: its menu carries history
    // and local cleanup only.
    const retiredLibraryCard = page
      .getByRole("article")
      .filter({ hasText: providerShowTitle! })
      .filter({ hasText: "已结束" })
      .first();
    await retiredLibraryCard.getByRole("button", { name: `更多操作 ${providerShowTitle!}`, exact: true }).click();
    await expect(page.getByRole("menuitem", { name: "结束订阅", exact: true })).toHaveCount(0);
    await expect(page.getByRole("menuitem", { name: "停止", exact: true })).toHaveCount(0);
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "已订阅", exact: true }).first().click();

    // The provider browse still offers a fresh subscription for the retired
    // association (a brand-new candidate/owner, never a revival). A provider
    // browse card carries no subscription identity, only the fact that the
    // external entry still has an instance — and a retired owner stays readable
    // on the subscribed wall until local-record cleanup removes it, so the
    // 已订阅 badge stays too. Opening the card still starts a new candidate.
    const retiredBrowse = await openProviderTab(page);
    await expect(retiredBrowse.getByRole("button", { name: `打开配置 ${providerShowTitle!}`, exact: true })).toBeVisible({ timeout: 30000 });
    await expect(retiredBrowse.getByText("已订阅", { exact: true })).toHaveCount(1);
    const second = await subscribeFromCard(page, retiredBrowse);
    expect(second.ownerID).not.toBe(first.ownerID);
    if (first.candidateID) expect(second.candidateID).not.toBe(first.candidateID);

    // The retired owner stays readable and read-only in the library.
    await page.getByRole("button", { name: "已订阅", exact: true }).first().click();
    await expect(retiredLibraryCards(page)).toHaveCount(retiredBefore + 1, { timeout: 30000 });
  });
});
