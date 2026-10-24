import type { Page } from "@playwright/test";
import { t } from "./copy";

// Catalog navigation shared by specs. Desktop: the browse sections (AniBT /
// Mikan / 自定义) and 已订阅 are top-bar buttons. Phone: 已订阅 / 发现 live in
// the bottom tab bar and the browse sections are an in-page Segmented (radio
// semantics) that only appears behind 发现. PHONE_MEDIA_QUERY is
// (width <= 600px), kept in sync with src/lib/useMediaQuery.ts.

export type BrowseSection = "anibt" | "mikan" | "custom";

export function isPhoneLayout(page: Page): boolean {
  return (page.viewportSize()?.width ?? 1280) <= 600;
}

export function browseSectionLabel(section: BrowseSection): string {
  if (section === "anibt") return "AniBT";
  if (section === "mikan") return "Mikan";
  return t("grid.tabCustom");
}

export function bottomTabBar(page: Page) {
  return page.getByRole("navigation", { name: t("nav.tabbarAria") });
}

/** Select a browse section; on phones open 发现 first when the Segmented is not shown. */
export async function selectBrowseSection(page: Page, section: BrowseSection): Promise<void> {
  const label = browseSectionLabel(section);
  if (isPhoneLayout(page)) {
    const segmented = page.getByRole("radiogroup", { name: t("nav.mainNavAria") });
    if ((await segmented.count()) === 0) {
      await bottomTabBar(page).getByRole("button", { name: t("nav.discover"), exact: true }).click();
    }
    await segmented.getByRole("radio", { name: label, exact: true }).click();
    return;
  }
  await page.getByRole("button", { name: label, exact: true }).click();
}

/** Open the subscribed wall (已订阅). */
export async function selectSubscribed(page: Page): Promise<void> {
  const name = t("grid.tabAll");
  if (isPhoneLayout(page)) {
    await bottomTabBar(page).getByRole("button", { name, exact: true }).click();
    return;
  }
  await page.getByRole("button", { name, exact: true }).click();
}
