import { expect, type Page } from "@playwright/test";

// Session helpers for the live specs.
//
// The browser session is an HttpOnly cookie, so a `page.request.*` call made
// from the same BrowserContext already carries the credential — there is no
// token to read out of script, by design. What an unsafe request still needs is
// the double-submit CSRF value, which the server and the page both hold in the
// readable `bansoko_csrf` cookie.

type Method = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

function unsafe(method: Method): boolean {
  return method !== "GET";
}

/** Reads the double-submit value out of the browser cookie jar. */
export async function csrfToken(page: Page): Promise<string> {
  const cookies = await page.context().cookies();
  const cookie = cookies.find((entry) => entry.name === "bansoko_csrf");
  expect(cookie?.value, "the session must have set a bansoko_csrf cookie").toBeTruthy();
  return cookie!.value;
}

/** Fixture-mode sign-in: the gate learns it is signed in only by asking
 *  `GET /auth/session`, since the credential is an HttpOnly cookie no script
 *  can seed. Fixture specs have no backend, so they answer that probe. */
export async function mockSignedIn(page: Page, username = "e2e-owner"): Promise<void> {
  await page.route("**/api/v1/auth/session", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        owner: { id: "e2e-owner", username, role: "owner" },
        expires_at: "2030-01-01T00:00:00Z",
      }),
    }),
  );
}

/** Headers an unsafe session request needs. Same-origin cookies are attached
 *  by the context itself; only the CSRF echo is missing from a bare request. */
export async function sessionHeaders(page: Page, method: Method): Promise<Record<string, string>> {
  return unsafe(method) ? { "X-CSRF-Token": await csrfToken(page) } : {};
}

/** Issues an API request as the signed-in browser session. */
export async function apiRequest(
  page: Page,
  method: Method,
  apiPath: string,
  options: { data?: unknown; headers?: Record<string, string> } = {},
) {
  const path = apiPath.startsWith("/api/v1") ? apiPath : `/api/v1${apiPath}`;
  const headers: Record<string, string> = {
    ...(await sessionHeaders(page, method)),
    ...(options.data === undefined ? {} : { "Content-Type": "application/json" }),
    ...options.headers,
  };
  return page.request.fetch(path, {
    method,
    headers,
    data: options.data === undefined ? undefined : JSON.stringify(options.data),
  });
}
