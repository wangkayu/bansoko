import fs from "node:fs";
import { test, expect, type Locator, type Page } from "@playwright/test";
import type { RuntimeLogLine } from "../src/lib/api/client";
import { installRuntimeLogFixture } from "./fixtures/runtimeLogFeed";
import { mockSignedIn } from "./session";

// 长事件消息的 fixture 浏览器几何回归：右侧只保留短状态，完整统计在下一行自然换行；
// 标题不能被挤成逐字竖排，长英文字幕组名也不能造成横向溢出。桌面与手机项目都实际执行。
const fixtureOnly = process.env.BANSOKO_E2E_FIXTURES === "1";

const chineseDetail = "已按新的集数偏移整理 Example：1 个文件换到其他集，删除 1 个，加版本号 0 个，覆盖 0 个，跳过 0 个";
const englishGroup = "ExampleSubtitleGroupWithAnExtremelyLongUnbrokenName";
const englishDetail = `已按新的集数偏移整理 ${englishGroup}：3 个文件换到其他集，删除 3 个，加版本号 0 个，覆盖 0 个，跳过 0 个`;

function business(id: string, messageCode: string, occurredAt: string, extra: Record<string, unknown> = {}) {
  return {
    record_type: "business",
    entry: {
      id,
      subscription_id: "sub-1",
      managed_anime_id: "anime-1",
      anime_title: "青之箱",
      source: "subscription",
      kind: "placement_changed",
      outcome: "success",
      occurred_at: occurredAt,
      message_code: messageCode,
      message_params: {},
      dedupe_key: id,
      retention_until: "2026-12-01T00:00:00Z",
      ...extra,
    },
  };
}

function offsetEntry(id: string, groupName: string, occurredAt: string, rekeyed: number, deleted: number) {
  return business(id, "offset_aligned", occurredAt, {
    details: { group_name: groupName },
    message_params: { group_name: groupName, rekeyed, deleted, versioned: 0, overwritten: 0, skipped: 0 },
  });
}

async function openLog(page: Page, extraRecords: ReturnType<typeof business>[] = []) {
  await mockSignedIn(page);
  await page.route("**/api/v1/anime", (route) =>
    route.request().method() === "GET"
      ? route.fulfill({ status: 200, contentType: "application/json", body: "[]" })
      : route.fulfill({ status: 405, contentType: "application/json", body: "{}" }),
  );
  await page.route("**/api/v1/observability/records**", (route) =>
    route.request().method() === "GET"
      ? route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            records: [
              offsetEntry("offset-en", englishGroup, "2026-11-04T06:02:00Z", 3, 3),
              offsetEntry("offset-zh", "Example", "2026-11-04T06:01:00Z", 1, 1),
              business("created", "subscription_created", "2026-11-04T06:00:00Z", { kind: "subscription_created" }),
              ...extraRecords,
            ],
            next_cursor: null,
          }),
        })
      : route.fulfill({ status: 405, contentType: "application/json", body: "{}" }),
  );
  await page.goto("/#/?log=records");
  const dialog = page.getByRole("dialog", { name: "日志" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText("已按新偏移整理", { exact: true }).first()).toBeVisible();
  return dialog;
}

async function assertFixedLogControls(dialog: Locator, rows: Locator, controls: Locator[]) {
  const measureControls = async () => {
    const boxes = await Promise.all(controls.map((control) => control.boundingBox()));
    if (boxes.some((box) => !box)) throw new Error("log control geometry is unavailable");
    return boxes.map((box) => box!.y);
  };
  const before = await measureControls();
  // Drive every overflowing scroll container along the result list's path.
  // This catches a second outer scrollbar even if scrolling the inner list works.
  const moved = await rows.evaluate((element) => {
    let count = 0;
    for (let node = element as HTMLElement | null; node && node.getAttribute("role") !== "dialog"; node = node.parentElement) {
      if (!/^(auto|scroll)$/.test(getComputedStyle(node).overflowY) || node.scrollHeight <= node.clientHeight + 1) continue;
      const initial = node.scrollTop;
      node.scrollTop = initial > 80 ? initial - 80 : initial + 80;
      if (Math.abs(node.scrollTop - initial) > 1) count++;
    }
    return count;
  });
  const after = await measureControls();
  expect(after.map((y, index) => Math.round(y - before[index]!)), "controls must not move when results scroll").toEqual(before.map(() => 0));
  expect(moved, "only the result list should scroll").toBe(1);
  expect(await dialog.evaluate((element) => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1);
}

async function assertLongEvent(row: Locator, titleText: string, detailText: string) {
  const title = row.getByText(titleText, { exact: true });
  const detail = row.getByText(detailText, { exact: true });
  await expect(title).toBeVisible();
  await expect(detail).toBeVisible();
  const titleBox = await title.boundingBox();
  const detailBox = await detail.boundingBox();
  const rowBox = await row.boundingBox();
  if (!titleBox || !detailBox || !rowBox) throw new Error("log event geometry is unavailable");

  // 长标题只自然折成少数几行并占据合理横向份额；完整统计必须位于标题下方的独立说明行。
  expect(titleBox.height).toBeLessThanOrEqual(60);
  expect(titleBox.width).toBeGreaterThanOrEqual(rowBox.width * 0.5);
  expect(detailBox.y).toBeGreaterThanOrEqual(titleBox.y + titleBox.height);
  expect(detailBox.width).toBeGreaterThanOrEqual(rowBox.width * 0.6);
  expect(await row.evaluate((element) => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1);
}

test.describe("日志长消息布局（fixture）", () => {
  test.skip(!fixtureOnly, "fixture-only spec");

  test("独立替换展开后显示双方文件，长文件名不溢出", async ({ page }, testInfo) => {
    const winner = `${"ExampleKeptVersion".repeat(9)}.mkv`;
    const loser = `${"ExampleReplacedVersion".repeat(9)}.mkv`;
    const dialog = await openLog(page, [business("replacement", "replacement_replaced", "2026-11-04T06:03:00Z", {
      kind: "replacement_decided",
      subject_id: "decision-1",
      release_id: "release-1",
      details: {
        replacement_winner: { file_name: winner },
        replaced_targets: [{ file_name: loser }],
        replacement_trigger: "library_reconciliation",
      },
    })]);
    const row = dialog.locator("[data-event-row]").filter({ hasText: "已替换旧版本" });
    const details = [`保留：${winner}`, `替换：${loser}`, "触发：核对媒体库文件"]
      .map((text) => row.getByText(text, { exact: true }));
    await expect(details[0]!).toHaveCount(0);
    await row.getByRole("button", { expanded: false }).click();
    await expect(row.getByRole("button", { expanded: true })).toBeVisible();
    const rowBox = await row.boundingBox();
    if (!rowBox) throw new Error("replacement row geometry is unavailable");
    let bottom = rowBox.y;
    for (const detail of details) {
      await expect(detail).toBeVisible();
      const box = await detail.boundingBox();
      if (!box) throw new Error("replacement detail geometry is unavailable");
      expect(box.y).toBeGreaterThanOrEqual(bottom - 1);
      expect(box.x + box.width).toBeLessThanOrEqual(rowBox.x + rowBox.width + 1);
      expect(await detail.evaluate((element) => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1);
      bottom = box.y + box.height;
    }
    expect(await dialog.evaluate((element) => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1);
    await dialog.screenshot({ path: testInfo.outputPath("replacement-file-details.png") });
    await row.getByRole("button", { expanded: true }).click();
    await expect(details[0]!).toHaveCount(0);
  });

  test("滚动长事件列表时页签和筛选保持固定", async ({ page }, testInfo) => {
    const dialog = await openLog(page, Array.from({ length: 24 }, (_, index) =>
      business(`created-${index}`, "subscription_created", new Date(Date.UTC(2026, 10, 4, 5, index)).toISOString(), {
        kind: "subscription_created",
        managed_anime_id: `anime-${index + 2}`,
      }),
    ));
    const list = dialog.getByRole("list", { name: "事件列表" });
    await expect(list.locator(":scope > li")).toHaveCount(27);
    await assertFixedLogControls(dialog, list, [
      dialog.getByRole("tablist"),
      dialog.getByRole("combobox", { name: "类型" }),
      dialog.getByRole("combobox", { name: "状态" }),
      dialog.getByRole("searchbox", { name: "搜索" }),
      dialog.getByRole("button", { name: /时间范围/ }),
    ]);
    await dialog.screenshot({ path: testInfo.outputPath("event-fixed-filters.png") });
  });

  test("长统计换行且标题不被挤压", async ({ page }, testInfo) => {
    const dialog = await openLog(page);
    const rows = dialog.getByRole("list", { name: "事件列表" }).locator(":scope > li");
    await expect(rows).toHaveCount(3);

    await assertLongEvent(rows.nth(0), `青之箱 · ${englishGroup} · 入库位置变化`, englishDetail);
    await assertLongEvent(rows.nth(1), "青之箱 · Example · 入库位置变化", chineseDetail);

    // 两行结构在所有断点一致：第一行标题与结果，第二行说明与时间。
    const shortRow = rows.nth(2);
    const titleLine = shortRow.locator('[data-event-line="title"]');
    const contextLine = shortRow.locator('[data-event-line="context"]');
    const shortTitle = shortRow.getByText("青之箱 · 创建订阅", { exact: true });
    const shortResult = shortRow.getByText("已创建订阅", { exact: true });
    const shortTitleBox = await shortTitle.boundingBox();
    const shortResultBox = await shortResult.boundingBox();
    const titleLineBox = await titleLine.boundingBox();
    const contextLineBox = await contextLine.boundingBox();
    if (!shortTitleBox || !shortResultBox || !titleLineBox || !contextLineBox) throw new Error("short event geometry is unavailable");
    expect(shortResultBox.y).toBeLessThan(shortTitleBox.y + shortTitleBox.height);
    expect(shortTitleBox.y).toBeLessThan(shortResultBox.y + shortResultBox.height);
    expect(contextLineBox.y).toBeGreaterThanOrEqual(titleLineBox.y + titleLineBox.height - 1);

    // 类型、状态、搜索与时间按钮在同一行，且不溢出。
    const filters = [
      dialog.getByRole("combobox", { name: "类型" }),
      dialog.getByRole("combobox", { name: "状态" }),
      dialog.getByRole("searchbox", { name: "搜索" }),
      dialog.getByRole("button", { name: /时间范围/ }),
    ];
    const boxes = await Promise.all(filters.map((item) => item.boundingBox()));
    if (boxes.some((box) => !box)) throw new Error("log filter geometry is unavailable");
    const centers = boxes.map((box) => box!.y + box!.height / 2);
    expect(Math.max(...centers) - Math.min(...centers)).toBeLessThanOrEqual(2);
    const filtersRight = Math.max(...boxes.map((box) => box!.x + box!.width));
    const dialogBox = await dialog.boundingBox();
    if (!dialogBox) throw new Error("log dialog geometry is unavailable");
    expect(filtersRight).toBeLessThanOrEqual(dialogBox.x + dialogBox.width + 1);

    await page.waitForTimeout(250);
    expect(await dialog.evaluate((element) => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1);
    expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(1);
    await dialog.screenshot({ path: testInfo.outputPath(`log-long-message-${testInfo.project.name}.png`) });
  });
});

test.describe("通知投递详情（fixture）", () => {
  test.skip(!fixtureOnly, "fixture-only spec");

  test("事件行两行展示，投递详情紧凑并折叠发送尝试", async ({ page }, testInfo) => {
    const pluginRecord = {
      record_type: "plugin",
      delivery: {
        id: "del-1",
        plugin_target_config_id: "target-1",
        plugin_target_config_version: 1,
        adapter_kind: "telegram",
        delivery_kind: "notification",
        trigger_kind: "download_submitted",
        source_owner_type: "release",
        source_owner_id: "release-1",
        state: "unknown",
        version: 1,
        attempt_count: 2,
        payload_status: "available",
        payload: { schema_version: 1, trigger_kind: "download_submitted", facts: { "anime.title": "青之箱", "ep.number": 8 } },
        target_display: "telegram:123456",
        rendered_title: "青之箱 第 8 集",
        rendered_body: "青之箱 第 8 集 已提交下载。",
        created_at: "2026-11-04T06:00:00Z",
        updated_at: "2026-11-04T06:05:00Z",
      },
    };
    const deliveryDetail = {
      delivery: pluginRecord.delivery,
      attempts: [
        {
          id: "attempt-1",
          plugin_delivery_id: "del-1",
          attempt_number: 1,
          started_at: "2026-11-04T06:04:00Z",
          target_display: "telegram:123456",
          payload_digest: "digest-1",
          response_status: 502,
          error: "connect timeout",
          retention_until: "2026-12-04T06:00:00Z",
        },
        {
          id: "attempt-2",
          plugin_delivery_id: "del-1",
          attempt_number: 2,
          started_at: "2026-11-04T06:05:00Z",
          target_display: "telegram:123456",
          payload_digest: "digest-1",
          response_status: 200,
          retention_until: "2026-12-04T06:00:00Z",
        },
      ],
    };
    await mockSignedIn(page);
    await page.route("**/api/v1/anime", (route) =>
      route.request().method() === "GET"
        ? route.fulfill({ status: 200, contentType: "application/json", body: "[]" })
        : route.fulfill({ status: 405, contentType: "application/json", body: "{}" }),
    );
    await page.route("**/api/v1/observability/records**", (route) =>
      route.request().method() === "GET"
        ? route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({ records: [pluginRecord], next_cursor: null }),
          })
        : route.fulfill({ status: 405, contentType: "application/json", body: "{}" }),
    );
    await page.route("**/api/v1/plugin-targets", (route) =>
      route.request().method() === "GET"
        ? route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({ targets: [{ id: "target-1", name: "番剧通知群", adapter_kind: "telegram" }] }),
          })
        : route.fulfill({ status: 405, contentType: "application/json", body: "{}" }),
    );
    await page.route("**/api/v1/plugin-deliveries/del-1", (route) =>
      route.request().method() === "GET"
        ? route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(deliveryDetail) })
        : route.fulfill({ status: 405, contentType: "application/json", body: "{}" }),
    );

    await page.goto("/#/?log=records");
    const dialog = page.getByRole("dialog", { name: "日志" });
    await expect(dialog).toBeVisible();
    const titleLine = dialog.locator('[data-event-line="title"]');
    const contextLine = dialog.locator('[data-event-line="context"]');
    await expect(titleLine).toContainText("青之箱");
    await expect(titleLine).toContainText("第 8 集");
    await expect(contextLine).toContainText("番剧通知群");
    const titleBox = await titleLine.boundingBox();
    const contextBox = await contextLine.boundingBox();
    if (!titleBox || !contextBox) throw new Error("delivery row geometry is unavailable");
    expect(contextBox.y).toBeGreaterThanOrEqual(titleBox.y + titleBox.height - 1);

    await dialog.getByRole("button", { name: /打开发送详情/ }).click();
    const detail = page.getByRole("dialog", { name: "发送详情" });
    await expect(detail).toBeVisible();
    await expect(detail.getByText("番剧通知群（Telegram）")).toBeVisible();
    await expect(detail.getByText("青之箱 第 8 集", { exact: true })).toBeVisible();
    await expect(detail.getByText("青之箱 第 8 集 已提交下载。", { exact: true })).toBeVisible();
    const summary = detail.getByText("发送记录（2 次）");
    await expect(summary).toBeVisible();
    await expect(detail.getByText("第 1 次")).not.toBeVisible();
    await summary.click();
    await expect(detail.getByText("第 1 次")).toBeVisible();
    await expect(detail.getByText("connect timeout")).toBeVisible();

    const detailBox = await detail.boundingBox();
    if (!detailBox) throw new Error("delivery dialog geometry is unavailable");
    if (testInfo.project.name === "desktop") {
      expect(detailBox.width).toBeLessThanOrEqual(441);
    } else {
      expect(detailBox.width).toBeGreaterThanOrEqual((await page.viewportSize())!.width - 1);
    }
    await page.waitForTimeout(250);
    await detail.screenshot({ path: testInfo.outputPath(`delivery-detail-${testInfo.project.name}.png`) });
  });
});

test.describe("原始日志搜索（fixture）", () => {
  test.skip(!fixtureOnly, "fixture-only spec");

  test("搜索与级别叠加，复制和下载只包含匹配行", async ({ page }, testInfo) => {
    await page.addInitScript(() => {
      const state = { text: "" };
      (window as unknown as { __bansokoCopied: { text: string } }).__bansokoCopied = state;
      Object.defineProperty(navigator, "clipboard", {
        configurable: true,
        value: {
          async writeText(text: string) {
            state.text = text;
          },
        },
      });
    });
    await mockSignedIn(page);
    const runtimeLines: RuntimeLogLine[] = [
      {
        at: "2026-11-04T06:00:00Z",
        level: "warn",
        message: "slow response",
        component: "httpapi",
        operation: "request",
      },
      {
        at: "2026-11-04T06:01:00Z",
        level: "error",
        message: "provider search failed",
        component: "metadata_matcher",
        operation: "tmdb_search",
        outcome: "failure",
        kind: "metadata_match",
        work_item_id: "wi-1",
        stage: "search",
        state_before: "queued",
        state_after: "failed",
        disposition: "retry",
        owner_type: "subscription",
        attempt: 2,
        duration_ms: 1234,
        retryable: false,
      },
    ];
    const feed = await installRuntimeLogFixture(page, runtimeLines);
    const requested = feed.requests;
    await page.route("**/api/v1/anime", (route) =>
      route.request().method() === "GET"
        ? route.fulfill({ status: 200, contentType: "application/json", body: "[]" })
        : route.fulfill({ status: 405, contentType: "application/json", body: "{}" }),
    );
    await page.route("**/api/v1/observability/records**", (route) =>
      route.request().method() === "GET"
        ? route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ records: [], next_cursor: null }) })
        : route.fulfill({ status: 405, contentType: "application/json", body: "{}" }),
    );

    await page.goto("/#/?log=raw");
    const dialog = page.getByRole("dialog", { name: "日志" });
    await expect(dialog).toBeVisible();
    await dialog.getByRole("tab", { name: "日志" }).click();
    await expect(dialog.getByText("slow response")).toBeVisible();
    await expect(dialog.getByText("provider search failed")).toBeVisible();

    // 日志页签可见时每 5 秒按 next_after 增量，而不是无游标重读整页。
    const beforeRefresh = requested.length;
    await expect.poll(() => requested.length, { timeout: 7000 }).toBeGreaterThan(beforeRefresh);
    expect(requested[requested.length - 1]!.searchParams.get("after")).toBe("file:a:2");
    expect(requested[requested.length - 1]!.searchParams.get("before")).toBeNull();

    const search = dialog.getByRole("searchbox", { name: "搜索日志" });
    await search.fill("PROVIDER retry");
    await expect.poll(() => requested.some((url) => url.searchParams.get("q") === "PROVIDER retry")).toBe(true);
    await expect(dialog.getByText("provider search failed")).toBeVisible();
    await expect(dialog.getByText("slow response")).toHaveCount(0);

    // The level select is a second filter on the same read: errors only keeps the match.
    await dialog.getByRole("combobox", { name: "级别" }).click();
    await page.getByRole("option", { name: "仅错误" }).click();
    await expect.poll(() => requested.some((url) => url.searchParams.get("q") === "PROVIDER retry" && url.searchParams.get("level") === "error")).toBe(true);
    await expect(dialog.getByText("provider search failed")).toBeVisible();

    await dialog.getByRole("button", { name: "复制" }).click();
    await expect(dialog.getByRole("button", { name: "已复制" })).toBeVisible();
    const copied = await page.evaluate(() => (window as unknown as { __bansokoCopied: { text: string } }).__bansokoCopied.text);
    expect(copied).toContain("provider search failed");
    expect(copied).toContain("retryable=false");
    expect(copied).not.toContain("slow response");

    const [download] = await Promise.all([page.waitForEvent("download"), dialog.getByRole("button", { name: "下载" }).click()]);
    expect(download.suggestedFilename()).toBe("bansoko-log.txt");
    const file = await download.path();
    if (!file) throw new Error("download path is unavailable");
    const downloaded = fs.readFileSync(file, "utf8");
    expect(downloaded).toContain("provider search failed");
    expect(downloaded).not.toContain("slow response");

    // The added search control must not overflow the dialog on either viewport.
    expect(await dialog.evaluate((element) => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1);
    expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(1);
    await dialog.screenshot({ path: testInfo.outputPath(`raw-log-search-${testInfo.project.name}.png`) });
  });

  test("短屏滚动任务日志时筛选、历史按钮和底栏保持固定", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: page.viewportSize()!.width, height: 440 });
    const feed = await installRuntimeLogFixture(page, [
      { at: "2026-11-04T05:59:59Z", level: "info", message: "another task" },
      ...Array.from({ length: 40 }, (_, index): RuntimeLogLine => ({
        at: new Date(Date.UTC(2026, 10, 4, 6, 0, index)).toISOString(),
        level: "warn",
        message: `task log ${index}`,
        work_item_id: "fixture-task",
      })),
    ]);
    feed.setSource("memory");
    const dialog = await openLog(page);
    await dialog.getByRole("tab", { name: "日志" }).click();
    const region = dialog.getByRole("region", { name: "运行日志" });
    await expect(region.locator("[data-runtime-row]")).toHaveCount(41);
    await region.getByRole("button", { name: "查看此任务" }).last().click();
    const task = dialog.getByText("仅显示任务 fixture-task");
    await expect(task).toBeVisible();
    await expect(dialog.getByText("磁盘历史不可用，正在显示内存中的最近日志")).toBeVisible();
    await expect(region.locator("[data-runtime-row]")).toHaveCount(40);
    // Fixed controls must still leave enough space to read at least one complete two-line log entry.
    expect(await region.evaluate((element) => element.clientHeight)).toBeGreaterThanOrEqual(64);
    await assertFixedLogControls(dialog, region, [
      dialog.getByRole("tablist"),
      dialog.getByRole("combobox", { name: "级别" }),
      dialog.getByRole("searchbox", { name: "搜索日志" }),
      task,
      dialog.locator("[data-runtime-action=load-older]"),
      dialog.locator("footer"),
    ]);
    await dialog.screenshot({ path: testInfo.outputPath("runtime-fixed-filters.png") });
  });

  test("日志跟底，向上翻阅时刷新保持位置", async ({ page }, testInfo) => {
    const makeLine = (index: number): RuntimeLogLine => ({
      at: new Date(Date.UTC(2026, 10, 4, 6, 0, 0) + index * 1000).toISOString(),
      level: "info",
      message: `line ${index + 1}`,
      component: "worker",
      operation: "tick",
    });
    await mockSignedIn(page);
    await page.route("**/api/v1/anime", (route) =>
      route.request().method() === "GET"
        ? route.fulfill({ status: 200, contentType: "application/json", body: "[]" })
        : route.fulfill({ status: 405, contentType: "application/json", body: "{}" }),
    );
    await page.route("**/api/v1/observability/records**", (route) =>
      route.request().method() === "GET"
        ? route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ records: [], next_cursor: null }) })
        : route.fulfill({ status: 405, contentType: "application/json", body: "{}" }),
    );
    const feed = await installRuntimeLogFixture(page, Array.from({ length: 60 }, (_, index) => makeLine(index)));
    const increments = () => feed.requests.filter((url) => url.searchParams.get("after") !== null);

    await page.goto("/#/?log=raw");
    const dialog = page.getByRole("dialog", { name: "日志" });
    await expect(dialog).toBeVisible();
    await dialog.getByRole("tab", { name: "日志" }).click();
    const region = dialog.getByRole("region", { name: "运行日志" });
    await expect(region).toBeVisible();
    const distanceFromBottom = () => region.evaluate((element) => element.scrollHeight - element.scrollTop - element.clientHeight);
    await expect.poll(distanceFromBottom).toBeLessThanOrEqual(2);

    // 新日志到达时使用上一页给的 after 游标并跟到底部。
    feed.addLines(Array.from({ length: 5 }, (_, offset) => makeLine(60 + offset)));
    await expect.poll(() => increments().length, { timeout: 8000 }).toBeGreaterThanOrEqual(1);
    expect(increments()[0]!.searchParams.get("after")).toBe("file:a:60");
    await expect(dialog.getByText("line 65", { exact: true })).toBeVisible();
    await expect.poll(distanceFromBottom).toBeLessThanOrEqual(2);

    // 向上翻阅后，下一次增量不抢回底部。
    await region.evaluate((element) => {
      element.scrollTop = 100;
    });
    await expect.poll(() => region.evaluate((element) => element.scrollTop)).toBeGreaterThanOrEqual(99);
    const beforeScrollRefresh = increments().length;
    feed.addLines(Array.from({ length: 5 }, (_, offset) => makeLine(65 + offset)));
    await expect.poll(() => increments().length, { timeout: 8000 }).toBeGreaterThan(beforeScrollRefresh);
    expect(await region.evaluate((element) => element.scrollTop)).toBeGreaterThanOrEqual(99);
    expect(await region.evaluate((element) => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1);
    await dialog.screenshot({ path: testInfo.outputPath(`raw-log-follow-${testInfo.project.name}.png`) });
  });
});
