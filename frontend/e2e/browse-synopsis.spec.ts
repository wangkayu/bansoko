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

async function mockReaderShell(page: Page): Promise<void> {
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
    json(route, seasonFixture()),
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
});
