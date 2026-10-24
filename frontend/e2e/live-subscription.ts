import { expect, type Page } from "@playwright/test";

/**
 * A new subscription starts 未开始 (pending): subscribing announces
 * 「已添加《…》，尚未开始」 with a 开始订阅 action and nothing refreshes or
 * downloads until it is started. Live specs that need a working owner start it
 * through that toast action (the product's own entry) and wait for the
 * pending → active CAS on the exact owner. Returns the started version.
 */
export async function startFromSubscribedToast(page: Page, managedAnimeID: string): Promise<number> {
  const announcement = page
    .locator("[data-sonner-toast]")
    .filter({ hasText: /已添加《.+》，尚未开始/ })
    .last();
  await expect(announcement).toBeVisible({ timeout: 15000 });
  const startResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "PATCH" &&
      new URL(response.url()).pathname === `/api/v1/anime/${encodeURIComponent(managedAnimeID)}/subscription`,
    { timeout: 15000 },
  );
  await announcement.getByRole("button", { name: "开始订阅", exact: true }).click();
  const response = await startResponse;
  expect(response.status(), await response.text()).toBe(200);
  const subscription = (await response.json()) as { state?: string; version?: number };
  expect(subscription.state).toBe("active");
  await expect(page.getByText("已开始订阅", { exact: true })).toBeVisible({ timeout: 15000 });
  return subscription.version!;
}
