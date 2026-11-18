import { test, expect, type Locator, type Page } from "@playwright/test";
import type { RuntimeLogLine } from "../src/lib/api/client";
import { installRuntimeLogFixture } from "./fixtures/runtimeLogFeed";
import { mockSignedIn } from "./session";

// 运行日志历史的 fixture 浏览器回归：最近页、加载更早、空结果保留水位、
// 5 秒 after 增量、阅读锚点、任务一键追踪、跨来源重置与桌面/手机几何。
// fixture 明确实现游标契约，不能用「每次都返回全量」掩盖无游标刷新。

const fixtureOnly = process.env.BANSOKO_E2E_FIXTURES === "1";

function runtimeLine(index: number, extra: Partial<RuntimeLogLine> = {}): RuntimeLogLine {
  return {
    at: new Date(Date.UTC(2026, 10, 4, 6, 0, 0) + index * 1000).toISOString(),
    level: "info",
    message: `runtime line ${index}`,
    component: "worker",
    operation: "tick",
    ...extra,
  };
}

async function openRuntimeLog(page: Page) {
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
  await page.goto("/#/?log=raw");
  const dialog = page.getByRole("dialog", { name: "日志" });
  await expect(dialog).toBeVisible();
  const region = dialog.getByRole("region", { name: "运行日志" });
  await expect(region).toBeVisible();
  return { dialog, region };
}

type Anchor = { key: string; top: number };

async function visibleAnchor(region: Locator): Promise<Anchor | null> {
  return region.evaluate((element) => {
    const top = element.getBoundingClientRect().top;
    const rows = [...element.querySelectorAll<HTMLElement>("[data-runtime-row]")];
    const row = rows.find((candidate) => candidate.getBoundingClientRect().bottom >= top + 1);
    if (!row) return null;
    return { key: row.dataset.runtimeKey ?? "", top: row.getBoundingClientRect().top };
  });
}

async function anchorTop(region: Locator, key: string): Promise<number | null> {
  return region.evaluate((element, runtimeKey) => {
    const row = element.querySelector<HTMLElement>(`[data-runtime-key="${runtimeKey}"]`);
    return row ? row.getBoundingClientRect().top : null;
  }, key);
}

test.describe("运行日志历史（fixture）", () => {
  test.skip(!fixtureOnly, "fixture-only spec");

  test("最近页、空结果水位、加载更早、增量与阅读锚点", async ({ page }, testInfo) => {
    test.setTimeout(90_000);
    const feed = await installRuntimeLogFixture(page, Array.from({ length: 260 }, (_, index) => runtimeLine(index)));
    const { dialog, region } = await openRuntimeLog(page);
    const rows = region.locator("[data-runtime-row]");

    // 最近页：不带游标，200 行，最旧 60、最新 259。
    await expect(rows).toHaveCount(200);
    await expect(dialog.getByText("runtime line 259", { exact: true })).toBeVisible();
    expect(feed.requests[0]!.searchParams.get("before")).toBeNull();
    expect(feed.requests[0]!.searchParams.get("after")).toBeNull();
    expect(feed.requests[0]!.searchParams.get("limit")).toBe("200");

    // 空增量页推进到扫描末：下一次轮询使用服务端返回的新 next_after 水位。
    const increments = () => feed.requests.filter((url) => url.searchParams.get("after") !== null);
    await expect.poll(() => increments().length, { timeout: 8000 }).toBeGreaterThanOrEqual(1);
    const watermark = increments()[0]!.searchParams.get("after");
    expect(watermark).toBe("file:a:260");
    await expect.poll(() => increments().length, { timeout: 8000 }).toBeGreaterThanOrEqual(2);
    expect(increments()[1]!.searchParams.get("after")).toBe("file:a:260");

    // 加载更早：before 游标、顺序不变、阅读锚点不跳。
    await region.evaluate((element) => {
      element.scrollTop = 100;
    });
    await expect.poll(() => region.evaluate((element) => element.scrollTop)).toBeGreaterThanOrEqual(99);
    const anchor = await visibleAnchor(region);
    expect(anchor).not.toBeNull();
    await dialog.getByRole("button", { name: "加载更早" }).click();
    await expect(rows).toHaveCount(260);
    const older = feed.requests.filter((url) => url.searchParams.get("before") !== null);
    expect(older).toHaveLength(1);
    expect(older[0]!.searchParams.get("before")).toBe("file:b:60");
    await expect(rows.first()).toContainText("runtime line 0");
    await expect(rows.last()).toContainText("runtime line 259");
    expect(Math.abs((await anchorTop(region, anchor!.key))! - anchor!.top)).toBeLessThanOrEqual(2);

    // 阅读锚点在上方时，新行只追加在下方，不抢回底部。
    const beforeAppend = increments().length;
    feed.addLines([runtimeLine(260), runtimeLine(261)]);
    await expect.poll(() => increments().length, { timeout: 8000 }).toBeGreaterThan(beforeAppend);
    await expect(rows).toHaveCount(262);
    await expect(dialog.getByText("runtime line 261", { exact: true })).toBeVisible();
    expect(Math.abs((await anchorTop(region, anchor!.key))! - anchor!.top)).toBeLessThanOrEqual(2);
    expect(increments().every((url) => url.searchParams.get("after") !== null)).toBe(true);

    // 翻到底部后继续跟随新行；一次到达超过一页时连续追赶，不丢中间页。
    await region.evaluate((element) => {
      element.scrollTop = element.scrollHeight;
    });
    const beforeBurst = increments().length;
    feed.addLines(Array.from({ length: 250 }, (_, offset) => runtimeLine(263 + offset)));
    await expect.poll(() => increments().length, { timeout: 10000 }).toBeGreaterThanOrEqual(beforeBurst + 2);
    await expect(rows).toHaveCount(512);
    await expect
      .poll(() => region.evaluate((element) => element.scrollHeight - element.scrollTop - element.clientHeight))
      .toBeLessThanOrEqual(2);

    // 有界窗口与横向几何。
    expect(await region.evaluate((element) => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1);
    expect(await dialog.evaluate((element) => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1);
    expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(1);
    await dialog.screenshot({ path: testInfo.outputPath(`runtime-history-${testInfo.project.name}.png`) });
  });

  test("任务一键追踪与跨来源边界", async ({ page }, testInfo) => {
    test.setTimeout(90_000);
    const specialLevels = ["error", "info", "warn", "info", "error", "debug"] as const;
    const lines = Array.from({ length: 260 }, (_, index) =>
      index >= 240 && index <= 245
        ? runtimeLine(index, { level: specialLevels[index - 240]!, message: `task line ${index}`, work_item_id: "wi-42" })
        : runtimeLine(index),
    );
    const feed = await installRuntimeLogFixture(page, lines);
    const { dialog, region } = await openRuntimeLog(page);
    const rows = region.locator("[data-runtime-row]");
    await expect(rows).toHaveCount(200);

    // 先建立可返回的原过滤：仅错误 + 文本。
    await dialog.getByRole("combobox", { name: "级别" }).click();
    await page.getByRole("option", { name: "仅错误" }).click();
    const search = dialog.getByRole("searchbox", { name: "搜索日志" });
    await search.fill("task");
    await expect
      .poll(() => feed.requests.some((url) => url.searchParams.get("level") === "error" && url.searchParams.get("q") === "task"))
      .toBe(true);
    await expect(rows).toHaveCount(2);
    await expect(dialog.getByText("task line 240", { exact: true })).toBeVisible();

    // 一键查看任务：全部级别、清空文本、按精确工作项读取。
    await dialog.getByRole("button", { name: "查看此任务" }).first().click();
    await expect
      .poll(() =>
        feed.requests.some(
          (url) =>
            url.searchParams.get("work_item_id") === "wi-42" &&
            url.searchParams.get("level") === "all" &&
            url.searchParams.get("q") === null,
        ),
      )
      .toBe(true);
    await expect(dialog.getByText("仅显示任务 wi-42")).toBeVisible();
    await expect(search).toHaveValue("");
    await expect(dialog.getByRole("combobox", { name: "级别" })).toContainText("全部级别");
    await expect(rows).toHaveCount(6);
    await expect(dialog.getByText("task line 242", { exact: true })).toBeVisible();
    expect(await dialog.evaluate((element) => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1);
    await dialog.screenshot({ path: testInfo.outputPath(`runtime-task-filter-${testInfo.project.name}.png`) });

    // 返回原过滤。
    await dialog.getByRole("button", { name: "返回原过滤" }).click();
    await expect
      .poll(() =>
        feed.requests.some(
          (url) =>
            url.searchParams.get("work_item_id") === null &&
            url.searchParams.get("level") === "error" &&
            url.searchParams.get("q") === "task",
        ),
      )
      .toBe(true);
    await expect(search).toHaveValue("task");
    await expect(dialog.getByRole("combobox", { name: "级别" })).toContainText("仅错误");
    await expect(rows).toHaveCount(2);

    // 跨来源：文件游标落到内存来源，必须重置窗口并提示，而不是拼接两组。
    await dialog.getByRole("combobox", { name: "级别" }).click();
    await page.getByRole("option", { name: "全部级别" }).click();
    await search.fill("");
    await expect
      .poll(() => feed.requests.some((url) => url.searchParams.get("level") === "all" && url.searchParams.get("q") === null))
      .toBe(true);
    await expect(rows).toHaveCount(200);
    feed.setSource("memory");
    await dialog.getByRole("button", { name: "加载更早" }).click();
    await expect(dialog.getByText("磁盘历史不可用，正在显示内存中的最近日志")).toBeVisible();
    await expect(dialog.getByText("更早的日志已被清理，视图已从现有历史重新开始")).toBeVisible();
    await expect(rows).toHaveCount(200);
    await expect(dialog.getByText("runtime line 60", { exact: true })).toHaveCount(1);
    await dialog.screenshot({ path: testInfo.outputPath(`runtime-source-gap-${testInfo.project.name}.png`) });
  });
});
