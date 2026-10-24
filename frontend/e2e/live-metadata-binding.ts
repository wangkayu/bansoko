import { expect, type Locator, type Page } from "@playwright/test";
import { csrfToken } from "./session";

// Shared binding step for the authenticated real-effect live specs.
//
// Acceptance refuses to start a download while the anime is under the metadata
// hold, so an unbound anime can only ever reach `held / waiting_metadata`
// (`frontend/e2e/local-data-cleanup-live.spec.ts` reproduced exactly that: the
// release row stayed at「等待匹配元数据（未关联 Bangumi、TMDB）」and `重试`
// answered `outcome=held reason=metadata_incomplete`, which the retry contract
// treats as a result, not an error). A spec that needs the real download/file
// chain must bind the identity first, through the same real UI the product uses.

export async function matchMetadataInUI(page: Page, workspace: Locator, provider: "bgm" | "tmdb", query: string) {
  const matchButton = workspace.getByRole("button", { name: provider === "bgm" ? "匹配 Bangumi" : "匹配 TMDB", exact: true });
  await expect(matchButton).toBeVisible();
  await matchButton.click();
  // The parent dialog goes aria-hidden while the nested match dialog is open,
  // so address the match dialog by its accessible name instead of a count.
  const dialog = page.getByRole("dialog", { name: `匹配 ${provider === "bgm" ? "Bangumi" : "TMDB"} 条目`, exact: true });
  await expect(dialog).toBeVisible();
  const searchResponse = page.waitForResponse((r) => new URL(r.url()).pathname.endsWith("/metadata/search") && r.request().method() === "GET");
  await dialog.getByLabel("标题查询", { exact: true }).fill(query);
  await dialog.getByRole("button", { name: "查询", exact: true }).click();
  expect((await searchResponse).status()).toBe(200);
  const option = dialog.getByRole("listbox", { name: "匹配结果" }).getByRole("option").first();
  await expect(option).toBeVisible({ timeout: 30000 });
  await option.click();
  if (provider === "tmdb") {
    // A fresh pick with no inferred season falls back to the "自定义" editor
    // with an empty custom field; fill it so the confirm button is enabled.
    const customSeason = dialog.getByLabel("自定义季度", { exact: true });
    if (await customSeason.isVisible().catch(() => false)) {
      await customSeason.fill("1");
    }
  }
  const confirmButton = dialog.getByRole("button", { name: "确认匹配", exact: true });
  await expect(confirmButton).toBeEnabled();
  await confirmButton.click();
  await expect(dialog).toBeHidden();
}

// BGM needs no credential, so it is always matched through the real UI search.
// TMDB needs an operator-configured connection this session has not verified
// working; with BANSOKO_E2E_TMDB_LIVE=1 it is matched the same real way,
// otherwise the TMDB half of the binding is written through the same owner
// metadata PATCH the UI's save issues (reusing its CAS version and whatever BGM
// binding it just wrote), so the real download/effect chain below still
// exercises a fully-bound workspace. TMDB 265010 is a stable public TV entry
// used only for this direct-write fallback. The function returns the workspace
// locator to use afterwards: the direct write ends with a reload (see below).
export async function bindWorkspaceMetadata(page: Page, workspace: Locator, managedAnimeID: string): Promise<Locator> {
  const liveTMDB = process.env.BANSOKO_E2E_TMDB_LIVE === "1";
  await workspace.getByRole("tab", { name: "元数据", exact: true }).click();
  await matchMetadataInUI(page, workspace, "bgm", "海贼王");
  if (liveTMDB) await matchMetadataInUI(page, workspace, "tmdb", "One Piece");
  // The anime's own season is an owner-edited value: no binding derives it, and
  // the shipped naming layout puts a season directory in the target path, so an
  // empty season fails the placement render (「季目录为空，路径无效」). A
  // subscription created from a provider catalog carries the provider's season;
  // a custom preset carries none, so set it here as the owner would, before the
  // single metadata save carries it.
  const seasonInput = workspace.getByRole("textbox", { name: "季度", exact: true });
  if ((await seasonInput.count()) > 0 && (await seasonInput.inputValue()) === "") {
    await seasonInput.fill("1");
    await expect(seasonInput).toHaveValue("1");
  }
  const patchResponse = page.waitForResponse((r) => new URL(r.url()).pathname.endsWith("/metadata") && r.request().method() === "PATCH");
  await workspace.getByRole("button", { name: "保存", exact: true }).click();
  const saved = await patchResponse;
  expect(saved.status(), await saved.text()).toBe(200);
  if (!liveTMDB) {
    const savedBody = (await saved.json()) as {
      version: number;
      bgm?: { work_id: string } | null;
      total_episodes?: number | null;
      year?: number | null;
      season?: number | null;
      media_kind_override?: "tv" | "movie" | null;
    };
    // The metadata owner is replaced wholesale: an omitted field clears (the
    // contract says so). Carry back every owner value the save just wrote, or
    // this second write would silently undo the season filled above.
    const response = await page.request.patch(`/api/v1/anime/${managedAnimeID}/metadata`, {
      headers: { "X-CSRF-Token": await csrfToken(page) },
      data: {
        expected_version: savedBody.version,
        bgm: savedBody.bgm ?? null,
        tmdb: { work_id: "265010", media_kind: "tv", season: 1 },
        total_episodes: savedBody.total_episodes ?? null,
        year: savedBody.year ?? null,
        season: savedBody.season ?? null,
        media_kind_override: savedBody.media_kind_override ?? null,
      },
    });
    expect(response.status(), await response.text()).toBe(200);
  }
  // subscribeAndOpenWorkspace leaves the dialog on the RSS tab, and
  // refreshExpandAndDownload (the caller's next step) relies on that; restore
  // it here since matching switched to the 元数据 tab to reach the dialogs. The
  // tab lives in the route (#/?anime=…&tab=rss), so setting it here is also what
  // makes the reload below land back on the RSS tab.
  await workspace.getByRole("tab", { name: "RSS", exact: true }).click();
  await expect(workspace.getByRole("tab", { name: "RSS", exact: true })).toHaveAttribute("aria-selected", "true", { timeout: 30000 });
  if (liveTMDB) return workspace;
  // The TMDB half above never passed through the tab's form, so the next
  // workspace re-read (the owner chain keeps polling) makes the form look like it
  // holds an unsaved edit against the newer server value. The RSS tab then
  // switches to owner_draft preview against metadata that is missing the TMDB
  // half just written, and the release row would show the hold instead of the
  // real chain — a state a user's own save never leaves behind. Reload: the
  // dialog is route-hosted, so it reopens on the RSS tab with the form and the
  // workspace both starting from the saved owner.
  await page.reload();
  const reloaded = page.getByRole("dialog").last();
  await expect(reloaded.getByRole("tab", { name: "RSS", exact: true })).toHaveAttribute("aria-selected", "true", { timeout: 30000 });
  return reloaded;
}
