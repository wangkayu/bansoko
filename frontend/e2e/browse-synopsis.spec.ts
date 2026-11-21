import { test, expect, type Page, type Route } from "@playwright/test";
import type { CatalogEntrySynopsis, CatalogWorkspace } from "../src/lib/api/client";
import { mockSignedIn } from "./session";
import { selectBrowseSection } from "./catalog-nav";
import { t } from "./copy";

const fixtureOnly = process.env.BANSOKO_E2E_FIXTURES === "1";

// The synopsis reader's fixture contract. The browse card's book entry opens the reader (never
// the config), and the header poster stays inside its small box: the shared .poster atom is
// absolutely positioned, so its container must be the positioning context — an escape covers the
// whole dialog with one big poster, which no jsdom unit test can see. The wall needs a minimal
// shell (session, top bar, subscribed list) before the browse section renders its cards.

const SYNOPSIS_TEXT = "一段用于视觉验证的简介文本，应当出现在阅读栏里。";

// A 1x1 PNG so the poster's <img> decodes instead of falling back to the initial.
const COVER_PNG = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==",
  "base64",
);

function seasonFixture(): CatalogWorkspace {
  return {
    season: "2026-fall",
    snapshot: {
      id: "snap-synopsis",
      season: "2026-fall",
      status: "fresh",
      provider: "anibt",
      observed_at: "2026-11-04T00:00:00Z",
      source_version: "1",
      contract_version: "1",
      health_state: "ready",
      health_checked_at: "2026-11-04T00:00:00Z",
      stale: false,
      version: 1,
    },
    items: [
      {
        kind: "anibt_entry",
        provider: "anibt",
        title: "星屑邮递员",
        original_title: "星屑邮递员 Original",
        provider_entry_ref: "anibt:synopsis-fixture",
        cover_url: "/api/v1/images/cover/synopsis-fixture",
        broadcast_day: "wed",
        release_format: "tv",
        release_state: "has_releases",
        release_count: 12,
        score: 8.2,
        catalog_state: "listed",
        preference: "neutral",
        allowed_actions: ["browse"],
        instances: [],
      },
    ],
  };
}

function synopsisFixture(): CatalogEntrySynopsis {
  return {
    provider: "anibt",
    provider_entry_ref: "anibt:synopsis-fixture",
    source_url: "https://anibt.example/anime/synopsis-fixture",
    synopsis: `${SYNOPSIS_TEXT}[简介原文]これは原文の紹介文です。`,
    episodes: 12,
    weekday: "wed",
    season: "2026-fall",
    tags: ["科幻"],
    official_site: "https://official.example",
    bgm_url: "https://bgm.example/subject/synopsis-fixture",
  };
}

async function mockReaderShell(page: Page, workspace = seasonFixture()): Promise<void> {
  // The last-registered route wins, so the specific reads come after the shell reads.
  const json = (route: Route, body: unknown) =>
    route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
  await page.route("**/api/v1/automation", (route) => json(route, []));
  await page.route("**/api/v1/config/connections", (route) => json(route, []));
  await page.route("**/api/v1/anime", (route) => json(route, []));
  await page.route("**/api/v1/observability/records**", (route) => json(route, { items: [] }));
  await page.route("**/api/v1/observability/diagnostics**", (route) => json(route, { items: [] }));
  await page.route("**/api/v1/catalog/suppressions", (route) => json(route, []));
  await mockSignedIn(page);
  await page.route("**/api/v1/images/**", (route) =>
    route.fulfill({ status: 200, contentType: "image/png", body: COVER_PNG }),
  );
  await page.route(/\/api\/v1\/catalog\/providers\/anibt\/seasons\/[^/]+$/, (route) =>
    json(route, workspace),
  );
  await page.route("**/api/v1/catalog/providers/anibt/entries/*/synopsis", (route) =>
    json(route, synopsisFixture()),
  );
}

test.describe("browse synopsis reader", () => {
  test.beforeEach(() => {
    test.skip(!fixtureOnly, "fixture UI contract runs only with BANSOKO_E2E_FIXTURES=1");
  });

  test("opens from the card and keeps the header poster inside its box", async ({ page }) => {
    await mockReaderShell(page);
    await page.goto("/");
    await selectBrowseSection(page, "anibt");
    await page
      .getByRole("button", { name: t("grid.card.viewSynopsisFor", { title: "星屑邮递员" }) })
      .click();

    const dialog = page.getByRole("dialog").last();
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText(SYNOPSIS_TEXT)).toBeVisible();
    // The split rule: both sides have content, so the reader shows both tabs.
    await expect(dialog.getByRole("tab", { name: t("grid.card.synopsis.summary"), exact: true })).toBeVisible();
    await expect(dialog.getByRole("tab", { name: t("grid.card.synopsis.original"), exact: true })).toBeVisible();

    const boxes = await dialog.evaluate((root) => {
      const rect = (selector: string) => {
        const el = root.querySelector(selector);
        if (!el) return null;
        const r = el.getBoundingClientRect();
        return { w: r.width, h: r.height, x: r.x, y: r.y };
      };
      return {
        panel: root.getBoundingClientRect().toJSON(),
        container: rect(".synopsis-poster"),
        poster: rect(".synopsis-poster .poster"),
        img: rect(".synopsis-poster img"),
      };
    });
    // A small header thumbnail, never a dialog-filling poster: the atom fills exactly its
    // container and stays far smaller than the panel.
    expect(boxes.container).not.toBeNull();
    expect(boxes.container!.w).toBeLessThan(80);
    expect(boxes.poster).toEqual(boxes.container);
    expect(boxes.img).not.toBeNull();
    expect(boxes.img!.w).toBe(boxes.container!.w);
    expect(boxes.panel.width).toBeGreaterThan(boxes.container!.w * 3);
  });

  test("enlarges the catalog poster and keeps the list position", async ({ page }, testInfo) => {
    if (testInfo.project.name === "mobile") await page.setViewportSize({ width: 320, height: 650 });
    const workspace = seasonFixture();
    workspace.items.unshift(...Array.from({ length: 24 }, (_, index) => ({
      ...workspace.items[0], title: `星屑测试条目 ${index}`, original_title: null,
      provider_entry_ref: `anibt:fixture-${index}`, cover_url: null,
    })));
    await mockReaderShell(page, workspace);
    await page.goto("/?season=2026-fall");
    await selectBrowseSection(page, "anibt");
    await page.getByRole("button", { name: t("grid.list.entry"), exact: true }).click();
    const list = page.getByRole("dialog", { name: t("grid.list.title", { source: "AniBT" }) });
    const filter = list.getByRole("searchbox");
    const available = list.getByRole("button", { name: t("grid.availableOnly"), exact: true });
    await filter.fill('星屑 -"测试条目 0"');
    const row = list.locator("[data-catalog-row]").filter({ hasText: "星屑邮递员" });
    await expect(list.locator("[data-catalog-row]")).toHaveCount(24);
    const actions = row.locator("[data-catalog-actions]").getByRole("button");
    await expect(actions).toHaveCount(3);
    const actionKeys = ["grid.card.viewSynopsisFor", "grid.list.browseFor", "grid.list.subscribeFor"] as const;
    for (const [index, key] of actionKeys.entries()) {
      await expect(actions.nth(index)).toHaveAccessibleName(t(key, { title: "星屑邮递员" }));
    }
    const [filterBox, availableBox] = await Promise.all([filter.boundingBox(), available.boundingBox()]);
    if (testInfo.project.name === "mobile") {
      expect(availableBox!.y).toBeCloseTo(filterBox!.y);
      expect(availableBox!.height).toBeCloseTo(filterBox!.height);
      expect(availableBox!.x).toBeGreaterThan(filterBox!.x + filterBox!.width);
      const [statusBox, formatBox] = await Promise.all([
        list.getByRole("radiogroup", { name: t("grid.statsAria") }).boundingBox(),
        list.getByRole("combobox", { name: t("grid.list.format") }).boundingBox(),
      ]);
      expect(formatBox!.y).toBeCloseTo(statusBox!.y);
      expect(statusBox!.y).toBeGreaterThan(filterBox!.y + filterBox!.height);
    } else {
      expect(availableBox!.x + availableBox!.width).toBeLessThan(filterBox!.x);
    }
    const scroll = list.locator("[data-catalog-list]");
    await scroll.evaluate((element) => { element.scrollTop = element.scrollHeight; });
    const checked = row.getByRole("checkbox");
    await checked.click();
    const offset = await scroll.evaluate((element) => element.scrollTop);
    expect(offset).toBeGreaterThan(0);
    expect(await scroll.evaluate((element) => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1);
    const score = (await row.locator("[data-catalog-score]").boundingBox())!;
    const detail = (await row.locator("[data-catalog-detail]").boundingBox())!;
    expect(score.y).toBeGreaterThanOrEqual(detail.y + detail.height - 1);
    await list.screenshot({ path: testInfo.outputPath("catalog-list.png"), animations: "disabled" });
    await row.getByRole("button", { name: t("grid.card.viewSynopsisFor", { title: "星屑邮递员" }) }).click();
    const synopsis = page.getByRole("dialog", { name: "星屑邮递员", exact: true });
    const zoomButton = synopsis.getByRole("button", { name: t("grid.card.synopsis.zoomPoster") });
    const thumbnail = synopsis.locator(".synopsis-poster img");
    await expect(zoomButton).toBeVisible();
    const src = await thumbnail.getAttribute("src");
    const thumbnailBox = (await thumbnail.boundingBox())!;
    await zoomButton.click();
    const zoom = page.getByRole("dialog", { name: t("grid.card.synopsis.posterTitle", { title: "星屑邮递员" }) });
    const large = zoom.getByRole("img", { name: "星屑邮递员" });
    await expect(large).toHaveAttribute("src", src!);
    await expect(large).toHaveCSS("object-fit", "contain");
    const image = (await large.boundingBox())!;
    expect(Math.min(image.width, image.height)).toBeGreaterThan(thumbnailBox.height * 2);
    expect(image.y + image.height).toBeLessThanOrEqual(page.viewportSize()!.height + 1);
    await zoom.screenshot({ path: testInfo.outputPath("poster-enlarged.png"), animations: "disabled" });
    await zoom.getByRole("button", { name: t("ui.close"), exact: true }).click();
    await expect(zoomButton).toBeFocused();
    await synopsis.getByRole("button", { name: t("ui.close"), exact: true }).first().click();
    await expect(filter).toHaveValue('星屑 -"测试条目 0"');
    await expect(checked).toBeChecked();
    expect(await scroll.evaluate((element) => element.scrollTop)).toBe(offset);
  });
});

test.describe("browse subscription badge", () => {
  test.skip(!fixtureOnly, "fixture UI contract runs only with BANSOKO_E2E_FIXTURES=1");

  test("keeps the compact badge below metadata without covering card controls", async ({ page }, testInfo) => {
    const workspace = seasonFixture();
    const entry = workspace.items[0];
    const titles = ["Skyline Notes", "Forest Post", "Moon Archive"];
    workspace.items = titles.map((title, index) => ({
      ...entry,
      title,
      provider_entry_ref: `anibt:badge-fixture-${index}`,
      broadcast_day: index === 0 ? "wed" : "none",
      score: index === 1 ? undefined : 8.2,
      instances: [{
        managed_anime_ref: `badge-owner-${index}`,
        subscription_ref: `badge-subscription-${index}`,
        subscription_state: "active",
        subscription_version: 1,
        lifecycle_state: "active",
        lifecycle_epoch: 1,
      }],
    }));
    await page.addInitScript(() => localStorage.setItem("locale", "en"));
    await mockReaderShell(page, workspace);
    await page.goto("/");
    if (testInfo.project.name === "mobile") {
      await page.getByRole("navigation", { name: "Bottom navigation" }).getByRole("button", { name: "Discover" }).click();
      await page.getByRole("radio", { name: "AniBT", exact: true }).click();
    } else {
      await page.getByRole("button", { name: "AniBT", exact: true }).click();
    }
    await page.evaluate(() => document.fonts.ready);

    for (const [index, title] of titles.entries()) {
      const card = page.locator(".browse-card").filter({ has: page.getByRole("heading", { name: title, exact: true }) });
      const badge = card.getByText("Subscribed", { exact: true });
      await expect(badge).toBeVisible();
      await expect(badge).toHaveCSS("font-size", "11px");
      await expect(badge).toHaveCSS("border-radius", "4px");
      const layout = await badge.evaluate((element) => {
        const card = element.closest(".browse-card")!;
        const corner = element.parentElement!;
        const chip = element.getBoundingClientRect();
        const label = document.createRange();
        label.selectNodeContents(element);
        const textRight = label.getBoundingClientRect().right;
        const paddedRight = chip.right - parseFloat(getComputedStyle(element).paddingRight);
        const facts = Array.from(corner.querySelectorAll(".catalog-corner-badge, .catalog-corner-badge-score"),
          (fact) => fact.getBoundingClientRect());
        const controls = Array.from(card.querySelectorAll(".browse-card-synopsis, .catalog-pw-more"),
          (button) => button.getBoundingClientRect());
        return {
          height: chip.height,
          remaining: paddedRight - textRight,
          clearance: Math.min(...controls.map((r) => r.left)) - Math.max(chip.right, ...facts.map((r) => r.right)),
          controls: controls.length,
          factTops: facts.map((r) => r.top),
          chipX: chip.left - corner.getBoundingClientRect().left,
          chipY: chip.top - Math.max(corner.getBoundingClientRect().top, ...facts.map((r) => r.bottom)),
        };
      });
      expect(layout.height).toBeCloseTo(16);
      expect(layout.remaining).toBeGreaterThanOrEqual(-0.1);
      expect(layout.controls).toBe(2);
      expect(layout.clearance).toBeGreaterThanOrEqual(1);
      expect(layout.factTops).toHaveLength(index === 0 ? 2 : index === 1 ? 0 : 1);
      expect(new Set(layout.factTops).size).toBe(index === 1 ? 0 : 1);
      expect(layout.chipX).toBeCloseTo(0);
      expect(layout.chipY).toBeCloseTo(index === 1 ? 0 : 3);
      if (index === 0) await card.screenshot({ path: testInfo.outputPath("subscription-badge.png"), animations: "disabled" });
    }

    const retiredTitle = "Winter Broadcast";
    await page.route("**/api/v1/anime-config-candidates?origin=custom", (route) => route.fulfill({ json: { candidates: [{
      id: "retired-preset",
      title: retiredTitle,
      activated_managed_anime_id: "retired-owner",
      draft: { rss_sources: [] },
    }] } }));
    await page.route("**/api/v1/anime", (route) => route.fulfill({ json: [{
      managed_anime: { id: "retired-owner", title: retiredTitle },
      subscription: { id: "retired-subscription", origin: "custom", state: "stopped", lifecycle_state: "retired", lifecycle_epoch: 2, version: 2 },
      metadata_hold: { held: false, missing: [] },
      metadata: {},
      broadcast_day: "wed",
    }] }));
    await page.getByRole(testInfo.project.name === "mobile" ? "radio" : "button", { name: "Custom", exact: true }).click();
    const retiredCard = page.locator(".browse-card").filter({ has: page.getByRole("heading", { name: retiredTitle }) });
    const ended = retiredCard.getByText("Ended", { exact: true });
    await expect(ended).toBeVisible();
    await expect(ended).toHaveCSS("font-size", "11px");
    await expect(ended).toHaveCSS("border-radius", "4px");
    await expect(ended).toHaveClass(/\bcatalog-pw-chip-muted\b/);
    const retiredLayout = await ended.evaluate((element) => {
      const badge = element.getBoundingClientRect();
      const day = element.parentElement!.querySelector(".catalog-corner-badge")!.getBoundingClientRect();
      return { height: badge.height, rowGap: badge.top - day.bottom, leftOffset: badge.left - day.left };
    });
    expect(retiredLayout).toEqual({ height: 16, rowGap: 3, leftOffset: 0 });
    await expect(retiredCard.getByText("Subscribed", { exact: true })).toHaveCount(0);
    await retiredCard.screenshot({ path: testInfo.outputPath("ended-badge.png"), animations: "disabled" });
  });
});
