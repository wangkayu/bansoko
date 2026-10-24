import { expect, type Page } from "@playwright/test";
import { csrfToken } from "./session";

type GlobalSection = { section_name: string; schema_version: string; value: Record<string, unknown>; version: number };

/**
 * Every naming target is absolute (library_root is retired) and the shipped
 * default layout writes under `/media` (it wrote `/library` before the
 * 2026-10-22 defaults), the library mount of a container deployment. A
 * host-run disposable session has neither root; its library is
 * BANSOKO_HARNESS_LIBRARY_ROOT. Re-anchor the Global naming section's directory
 * templates there (same layout, only the leading library root changes) before
 * any preset or subscription copies the Global rules, so real file effects land
 * in the session library the VM-side assertions read.
 */

const DEFAULT_LIBRARY_ROOTS = ["/media", "/library"];

export async function anchorNamingAtHarnessLibrary(page: Page): Promise<void> {
  const root = process.env.BANSOKO_HARNESS_LIBRARY_ROOT?.trim().replace(/\/+$/, "");
  expect(root, "BANSOKO_HARNESS_LIBRARY_ROOT is required for real file effects").toBeTruthy();
  expect(root!.startsWith("/"), "BANSOKO_HARNESS_LIBRARY_ROOT must be absolute").toBe(true);
  // The session is an HttpOnly cookie the context attaches; the PUT below is
  // the only request that also needs the double-submit echo.
  const headers: Record<string, string> = {};

  const listed = await page.request.get("/api/v1/config/global-sections", { headers });
  expect(listed.status(), await listed.text()).toBe(200);
  const naming = ((await listed.json()) as GlobalSection[]).find((section) => section.section_name === "naming_rules");
  expect(naming, "the Global naming section must be listed").toBeTruthy();

  let rewritten = 0;
  const reanchor = (node: unknown): unknown => {
    if (Array.isArray(node)) return node.map(reanchor);
    if (!node || typeof node !== "object") return node;
    return Object.fromEntries(
      Object.entries(node).map(([key, value]) => {
        if (key === "directory_template" && typeof value === "string") {
          for (const libraryRoot of DEFAULT_LIBRARY_ROOTS) {
            if (value === libraryRoot || value.startsWith(`${libraryRoot}/`)) {
              rewritten += 1;
              return [key, `${root}${value.slice(libraryRoot.length)}`];
            }
          }
        }
        return [key, reanchor(value)];
      }),
    );
  };
  const value = reanchor(naming!.value) as Record<string, unknown>;
  if (rewritten === 0) return; // already anchored (or not the default layout): leave it as is

  const saved = await page.request.put("/api/v1/config/global-sections/naming_rules", {
    headers: {
      ...headers,
      "Content-Type": "application/json",
      "If-Match": String(naming!.version),
      "X-CSRF-Token": await csrfToken(page),
    },
    data: { schema_version: naming!.schema_version, value },
  });
  expect(saved.status(), await saved.text()).toBe(200);
}
