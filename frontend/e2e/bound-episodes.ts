import { expect, type Locator, type Page } from "@playwright/test";
import { t } from "./copy";

export const boundTMDBCandidate = {
  provider: "tmdb", provider_work_id: "265010", title: "北岸来信", media_kind: "tv", details_status: "available",
  seasons: [{ season_number: 2, name: "第二季", episode_count: 2, air_date: "2026-10-01", is_special: false }],
  suggested_season: 2, score: 1,
};

export async function bindTMDB(page: Page, dialog: Locator) {
  const matchEntry = dialog.getByRole("button", { name: t("forms.matchProvider", { provider: "TMDB" }), exact: true });
  await expect(matchEntry).toHaveText(t("forms.matchAction"));
  await matchEntry.click();
  const match = page.getByRole("dialog", { name: t("forms.matchTitle", { provider: "TMDB" }), exact: true });
  await match.getByRole("textbox", { name: t("forms.titleQuery") }).fill(boundTMDBCandidate.title);
  await match.getByRole("button", { name: t("forms.search"), exact: true }).click();
  await match.getByRole("option", { name: /北岸来信/ }).click();
  await match.getByRole("button", { name: t("forms.confirmMatch"), exact: true }).click();
}

export async function mockBoundEpisodes(page: Page) {
  const reads: string[] = [];
  const root = "/api/v1/metadata/tmdb/works/265010/seasons";
  const bodies = {
    "": { seasons: [{ season: 0, episode_count: 1 }, { season: 2, name: "第二季", episode_count: 2 }] },
    "/0/episodes": { season: 0, episodes: [{ episode: 1, title: "冬日特别篇" }] },
    "/2/episodes": { season: 2, episodes: [{ episode: 1, title: "回信" }, { episode: 2, title: "启程" }] },
  };
  await page.route(`**${root}**`, (route) => {
    const suffix = new URL(route.request().url()).pathname.slice(root.length);
    if (route.request().method() !== "GET" || !Object.hasOwn(bodies, suffix)) return route.fulfill({ status: 404 });
    reads.push(suffix);
    return route.fulfill({ json: bodies[suffix as keyof typeof bodies] });
  });
  return reads;
}

export async function checkBoundEpisodes(page: Page, dialog: Locator, reads: string[], screenshot: string) {
  const tmdb = dialog.getByRole("region", { name: t("forms.tmdb"), exact: true });
  const entry = tmdb.getByRole("button", { name: t("forms.browseBoundEpisodes", { provider: "TMDB" }), exact: true });
  const link = tmdb.getByRole("link");
  await expect(link).toHaveAttribute("href", "https://www.themoviedb.org/tv/265010");
  await expect(link).toContainText(t("forms.seasonN", { n: 2 }));
  await entry.scrollIntoViewIfNeeded();
  const buttons = tmdb.getByRole("button");
  await expect(buttons).toHaveCount(3);
  await expect(buttons.nth(0)).toHaveAccessibleName(t("forms.browseBoundEpisodes", { provider: "TMDB" }));
  await expect(buttons.nth(1)).toHaveText(t("forms.matchAction"));
  await expect(buttons.nth(2)).toHaveText(t("forms.clear"));
  const bounds = (await tmdb.boundingBox())!;
  const first = (await buttons.nth(0).boundingBox())!;
  let right = bounds.x;
  for (const button of await buttons.all()) {
    const box = (await button.boundingBox())!;
    expect(box.x).toBeGreaterThanOrEqual(right - 1);
    expect(box.x + box.width).toBeLessThanOrEqual(bounds.x + bounds.width + 1);
    expect(Math.abs(box.y + box.height / 2 - first.y - first.height / 2)).toBeLessThanOrEqual(1);
    right = box.x + box.width;
  }
  const viewport = page.viewportSize()!;
  expect(bounds.x).toBeGreaterThanOrEqual(0);
  expect(bounds.x + bounds.width).toBeLessThanOrEqual(viewport.width + 1);
  const entryText = entry.getByText(t("forms.boundEpisodes"), { exact: true });
  if (viewport.width <= 640) await expect(entryText).toBeHidden();
  else await expect(entryText).toBeVisible();
  expect(reads).toEqual([]);
  if (process.env.BANSOKO_E2E_SCREENSHOTS === "1") await page.screenshot({ path: `../.local-artifacts/bound-episode-app-${screenshot}-row.png` });

  await entry.click();
  const browser = page.getByRole("dialog", { name: t("forms.boundEpisodeTitle"), exact: true });
  const picker = browser.getByRole("combobox", { name: t("forms.browseSeason") });
  await expect(picker).toContainText(t("forms.boundSeasonOption", { season: "第二季" }));
  await expect(browser.getByText("回信", { exact: true })).toBeVisible();
  expect([...reads].sort()).toEqual(["", "/2/episodes"]);
  const filter = browser.getByRole("textbox", { name: t("forms.boundEpisodeFilter") });
  await filter.fill("启程");
  await expect(browser.locator(".metadata-episode-row")).toHaveCount(1);
  expect(reads).toHaveLength(2);
  await filter.clear();
  await picker.click();
  await page.getByRole("option", { name: t("forms.seasonSpecials"), exact: true }).click();
  await expect(browser.getByText("冬日特别篇", { exact: true })).toBeVisible();
  expect(reads.at(-1)).toBe("/0/episodes");
  const box = (await browser.boundingBox())!;
  expect(box.x).toBeGreaterThanOrEqual(-1);
  expect(box.x + box.width).toBeLessThanOrEqual(viewport.width + 1);
  expect(await browser.evaluate((element) => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1);
  if (process.env.BANSOKO_E2E_SCREENSHOTS === "1") await page.screenshot({ path: `../.local-artifacts/bound-episode-app-${screenshot}-list.png` });
  await browser.getByRole("button", { name: t("ui.close"), exact: true }).last().click();
  await expect(browser).toBeHidden();
  await expect(link).toContainText(t("forms.seasonN", { n: 2 }));
  await expect(entry).toBeFocused();
}
