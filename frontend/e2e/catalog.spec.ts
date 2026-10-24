import { test, expect } from "@playwright/test";
import { shouldSkipAuthTests } from "./skip-auth-helper";
import { bottomTabBar, browseSectionLabel, isPhoneLayout, selectBrowseSection } from "./catalog-nav";
import { t } from "./copy";

test.describe("catalog canvas", () => {
  test.beforeEach(async ({ page }) => {
    if (shouldSkipAuthTests()) {
      test.skip(true, "BANSOKO_E2E_USERNAME and BANSOKO_E2E_PASSWORD are not set");
    }
    await page.goto("/");
  });

  test("displays the catalog canvas after login", async ({ page }) => {
    if (isPhoneLayout(page)) {
      // Phone: 已订阅 / 发现 in the bottom tab bar; the browse sections are the
      // Segmented options behind 发现.
      const tabbar = bottomTabBar(page);
      await expect(tabbar.getByRole("button", { name: t("grid.tabAll"), exact: true })).toBeVisible({ timeout: 10000 });
      await tabbar.getByRole("button", { name: t("nav.discover"), exact: true }).click();
      const segmented = page.getByRole("radiogroup", { name: t("nav.mainNavAria") });
      for (const section of ["anibt", "mikan", "custom"] as const) {
        await expect(segmented.getByRole("radio", { name: browseSectionLabel(section), exact: true })).toBeVisible();
      }
      return;
    }
    await expect(page.getByRole("button", { name: t("grid.tabAll") }).first()).toBeVisible({ timeout: 10000 });
    for (const section of ["anibt", "mikan", "custom"] as const) {
      await expect(page.getByRole("button", { name: browseSectionLabel(section) }).first()).toBeVisible();
    }
  });

  test("Add preset dialog opens from the custom section", async ({ page }) => {
    await selectBrowseSection(page, "custom");
    // An empty custom section shows 添加预设 in its empty state (the toolbar
    // copy is hidden then); with presets it sits in the toolbar. Either is the
    // user-facing entry.
    const addButton = page.getByRole("button", { name: t("grid.addPreset"), exact: true }).first();
    await expect(addButton).toBeVisible({ timeout: 10000 });
    await addButton.click();
    const dialog = page.getByRole("dialog").last();
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText(/RSS/).first()).toBeVisible();
  });
});
