import { test, expect, type Locator, type Page } from "@playwright/test";
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

async function openLog(page: Page) {
  await mockSignedIn(page);
  await page.route("**/api/v1/anime", (route) =>
    route.request().method() === "GET"
      ? route.fulfill({ status: 200, contentType: "application/json", body: "[]" })
      : route.fulfill({ status: 405, contentType: "application/json", body: "{}" }),
  );
  await page.route("**/api/v1/observability/diagnostics**", (route) =>
    route.request().method() === "GET"
      ? route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ episodes: [], next_cursor: null }) })
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
  expect(detailBox.width).toBeGreaterThanOrEqual(rowBox.width * 0.75);
  expect(await row.evaluate((element) => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1);
}

test.describe("日志长消息布局（fixture）", () => {
  test.skip(!fixtureOnly, "fixture-only spec");

  test("长统计换行且标题不被挤压", async ({ page }, testInfo) => {
    const dialog = await openLog(page);
    const rows = dialog.getByRole("list", { name: "事件列表" }).locator(":scope > li");
    await expect(rows).toHaveCount(3);

    await assertLongEvent(rows.nth(0), `青之箱 · ${englishGroup} · 入库位置变化`, englishDetail);
    await assertLongEvent(rows.nth(1), "青之箱 · Example · 入库位置变化", chineseDetail);

    // 短结果在桌面仍与标题同一行；手机按既有断点采用上下布局。
    const shortRow = rows.nth(2);
    const shortTitle = shortRow.getByText("青之箱 · 创建订阅", { exact: true });
    const shortResult = shortRow.getByText("已创建订阅", { exact: true });
    const shortTitleBox = await shortTitle.boundingBox();
    const shortResultBox = await shortResult.boundingBox();
    if (!shortTitleBox || !shortResultBox) throw new Error("short event geometry is unavailable");
    if (testInfo.project.name === "desktop") {
      expect(shortResultBox.y).toBeLessThan(shortTitleBox.y + shortTitleBox.height);
      expect(shortTitleBox.y).toBeLessThan(shortResultBox.y + shortResultBox.height);
    } else {
      expect(shortResultBox.y).toBeGreaterThanOrEqual(shortTitleBox.y + shortTitleBox.height);
    }

    expect(await dialog.evaluate((element) => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1);
    expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(1);
    await dialog.screenshot({ path: testInfo.outputPath(`log-long-message-${testInfo.project.name}.png`) });
  });
});
