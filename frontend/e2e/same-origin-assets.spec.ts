import { test, expect } from "@playwright/test";
import { shouldSkipAuthTests } from "./skip-auth-helper";
import { selectBrowseSection, selectSubscribed } from "./catalog-nav";

// UI-038: the page loads nothing from another origin. Fonts ship with the
// bundle and provider images arrive through /api/v1/images/*, so a browser run
// shows only same-origin requests and no CSP violation (img-src 'self' data:
// blob:, font-src 'self').
test.describe("same-origin assets", () => {
  test.beforeEach(() => {
    if (shouldSkipAuthTests()) {
      test.skip(true, "BANSOKO_E2E_USERNAME and BANSOKO_E2E_PASSWORD are not set");
    }
  });

  test("catalog browsing makes no third-party request and trips no CSP", async ({ page, baseURL }) => {
    const origin = new URL(baseURL ?? "http://localhost:8080").origin;
    const foreign: string[] = [];
    const violations: string[] = [];
    const fontRequests: string[] = [];
    page.on("request", (request) => {
      const url = request.url();
      if (url.startsWith("data:") || url.startsWith("blob:")) return;
      if (new URL(url).origin !== origin) foreign.push(url);
      if (/\.woff2?(\?|#|$)/i.test(url)) fontRequests.push(url);
    });
    page.on("console", (message) => {
      const text = message.text();
      if (/content security policy|refused to (load|connect|apply)/i.test(text)) violations.push(text);
    });

    await page.goto("/");
    for (const open of [
      () => selectBrowseSection(page, "mikan"),
      () => selectBrowseSection(page, "anibt"),
      () => selectSubscribed(page),
    ]) {
      await open();
      // The app keeps an event stream open, so "networkidle" never settles; give
      // the provider request and the image fetches a moment instead.
      await page.waitForTimeout(1500);
    }

    // The bundled fonts, not a remote stylesheet, supply the UI typeface.
    const loadedFamilies = await page.evaluate(async () => {
      await document.fonts.ready;
      return [...document.fonts].filter((face) => face.status === "loaded").map((face) => face.family.replace(/"/g, ""));
    });
    expect(loadedFamilies).toContain("Plus Jakarta Sans Variable");
    // Every fetched font file must come from this origin, not a CDN.
    expect(fontRequests.length).toBeGreaterThan(0);
    for (const url of fontRequests) {
      expect(new URL(url).origin, `font loaded cross-origin: ${url}`).toBe(origin);
    }
    expect(foreign, `third-party requests: ${foreign.join(", ")}`).toEqual([]);
    expect(violations, `CSP violations: ${violations.join(" | ")}`).toEqual([]);
  });
});
