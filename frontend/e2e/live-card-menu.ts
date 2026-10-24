import { expect, type Page } from "@playwright/test";

/**
 * Close an open card ⋯ menu and wait until it is really gone.
 *
 * The first Escape after the menu appears can be lost (observed on the live
 * server: the menu stays open), and the next click on the same or another
 * card's ⋯ trigger then only dismisses the still-open menu instead of opening
 * its own. Keep pressing Escape until no menu remains.
 */
export async function closeCardMenu(page: Page): Promise<void> {
  await expect(async () => {
    await page.keyboard.press("Escape");
    await expect(page.getByRole("menu")).toHaveCount(0, { timeout: 1000 });
  }).toPass({ timeout: 10000 });
}
