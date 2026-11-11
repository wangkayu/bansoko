import { test, expect, type Page, type Route } from "@playwright/test";
import type {
  FileManagerListing,
  FileManagerRecognitionPreview,
  FileManagerRoot,
  NamingTemplateLibraryView,
  ParserCatalogEntry,
} from "../src/lib/api/client";
import { t } from "./copy";
import { isPhoneLayout } from "./catalog-nav";
import { mockSignedIn } from "./session";

// The recognition window's file actions (load metadata / set file numbering)
// act on the preview rows, so they sit in a toolbar directly above that list
// instead of the settings column. Geometry is asserted here in a real browser:
// jsdom can see the buttons but not where they land.

const fixtureOnly = process.env.BANSOKO_E2E_FIXTURES === "1";

const root: FileManagerRoot = { id: "root-anime", label: "anime", path: "/media/anime", available: true };

const listing: FileManagerListing = {
  path: "/media/anime",
  truncated: false,
  entries: [
    { name: "Show - 01.mkv", path: "/media/anime/Show - 01.mkv", kind: "file", file_kind: "video", state: "detached", size_bytes: 2048, locked: false },
    { name: "Show - 02.mkv", path: "/media/anime/Show - 02.mkv", kind: "file", file_kind: "video", state: "detached", size_bytes: 2048, locked: false },
    { name: "Show - 03.mkv", path: "/media/anime/Show - 03.mkv", kind: "file", file_kind: "video", state: "detached", size_bytes: 2048, locked: false },
    { name: "Show - 01.zh.ass", path: "/media/anime/Show - 01.zh.ass", kind: "file", file_kind: "subtitle", state: "detached", size_bytes: 32, locked: false },
  ],
};

const library: NamingTemplateLibraryView = {
  default_key: "standard",
  version: 1,
  templates: [
    {
      key: "standard",
      name: "标准",
      rules: {
        language: "js-naming-1",
        unknown: { directory_template: "${anime.title}", video_filename_template: "${file.stem}" },
        tv: { directory_template: "${anime.title}/Season 1", video_filename_template: "${anime.title} S01E${ep.number}" },
        movie: { directory_template: "${anime.title}", video_filename_template: "${file.stem}" },
        subtitle_suffix_template: "",
      },
    },
  ],
};

const preview: FileManagerRecognitionPreview = {
  directory: "/media/anime",
  unknown_route: "skip",
  plan_digest: "digest-1",
  conflicts: [],
  title_hint: "示例作品",
  rows: [
    {
      path: "/media/anime/Show - 01.mkv",
      name: "Show - 01.mkv",
      kind: "video",
      group: "g1",
      revision: "r1",
      episode: { raw: "01", number: "1", offset: 0, source: "parsed" },
      target_path: "/media/anime/示例作品/Season 1/示例作品 S01E01.mkv",
      target_name: "示例作品 S01E01.mkv",
      disposition: "rename",
    },
    {
      path: "/media/anime/Show - 02.mkv",
      name: "Show - 02.mkv",
      kind: "video",
      group: "g2",
      revision: "r2",
      episode: { raw: "02", number: "2", offset: 0, source: "parsed" },
      target_path: "/media/anime/示例作品/Season 1/示例作品 S01E02.mkv",
      target_name: "示例作品 S01E02.mkv",
      disposition: "rename",
    },
    {
      path: "/media/anime/Show - 03.mkv",
      name: "Show - 03.mkv",
      kind: "video",
      group: "g3",
      revision: "r3",
      episode: { raw: "03", number: "3", offset: 0, source: "parsed" },
      target_path: "/media/anime/示例作品/Season 1/示例作品 S01E03.mkv",
      target_name: "示例作品 S01E03.mkv",
      disposition: "rename",
    },
    {
      path: "/media/anime/Show - 01.zh.ass",
      name: "Show - 01.zh.ass",
      kind: "subtitle",
      group: "g1",
      revision: "r4",
      target_path: "/media/anime/示例作品/Season 1/示例作品 S01E01.zh.ass",
      target_name: "示例作品 S01E01.zh.ass",
      disposition: "rename",
    },
  ],
};

function json(route: Route, body: unknown, status = 200): Promise<void> {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
}

function getOnly(route: Route, body: unknown): Promise<void> {
  if (route.request().method() !== "GET") {
    return json(route, { code: "METHOD_NOT_ALLOWED", message: "fixture 仅支持 GET" }, 405);
  }
  return json(route, body);
}

// The shell reads the status strip and the dialog's editors need while the
// catalog canvas stays behind the overlay; its own reads are irrelevant here.
async function mockReads(page: Page) {
  await mockSignedIn(page);
  await page.route("**/api/v1/events/stream**", (route) =>
    route.request().method() === "GET"
      ? route.fulfill({ status: 200, contentType: "text/event-stream", body: ": fixture stream\n\n" })
      : json(route, { code: "METHOD_NOT_ALLOWED", message: "fixture 仅支持 GET" }, 405),
  );
  await page.route("**/api/v1/config/connections", (route) => getOnly(route, []));
  await page.route("**/api/v1/config/naming-templates", (route) => getOnly(route, library));
  await page.route("**/api/v1/template-catalogs/naming", (route) =>
    getOnly(route, { language: "js-naming-1", fields: [], functions: [], shorthands: [], samples: [], vars_keys: [], vars_loaded_at: null }),
  );
  await page.route("**/api/v1/parser-catalog", (route) =>
    getOnly(route, [
      {
        id: "reltitle",
        display_name: "reltitle",
        implementation_version: "1",
        output_contract_version: "1",
        capabilities: ["file_name"],
        config_version: "1",
      } satisfies ParserCatalogEntry,
    ]),
  );
  await page.route("**/api/v1/file-manager/roots", (route) => getOnly(route, { roots: [root] }));
  await page.route("**/api/v1/file-manager/entries**", (route) => getOnly(route, listing));
  await page.route("**/api/v1/file-manager/recognition/preview", (route) =>
    route.request().method() === "POST"
      ? json(route, preview)
      : json(route, { code: "METHOD_NOT_ALLOWED", message: "fixture 仅支持 POST" }, 405),
  );
}

test.describe("file manager recognition actions", () => {
  test.skip(!fixtureOnly, "fixture 模式的 HTTP mock 才能直达识别窗口");

  test("keeps both file actions in a toolbar above the preview rows", async ({ page }, testInfo) => {
    await mockReads(page);
    await page.goto("/#/?tool=files&path=%2Fmedia%2Fanime");
    await page.getByRole("checkbox", { name: t("fileManager.browse.selectFile", { name: "Show - 01.mkv" }) }).waitFor();
    await page.getByRole("checkbox", { name: t("fileManager.browse.selectAll") }).click();
    await page.getByRole("button", { name: t("fileManager.selection.recognize") }).click();

    const dialog = page.getByRole("dialog", { name: t("fileManager.recognize.title") });
    await expect(dialog).toBeVisible();
    const toolbar = dialog.getByRole("toolbar", { name: t("fileManager.recognize.actions") });
    await expect(toolbar).toBeVisible();
    const loadButton = toolbar.getByRole("button", { name: t("fileManager.recognize.runtimeFacts.load") });
    const numberingButton = toolbar.getByRole("button", { name: t("fileNumbering.title") });
    await expect(loadButton).toBeVisible();
    await expect(numberingButton).toBeVisible();

    // The settings column keeps fields only; both actions must not repeat there.
    const panel = dialog.getByRole("region", { name: t("fileManager.recognize.configAria") });
    await expect(panel.getByRole("button", { name: t("fileManager.recognize.runtimeFacts.load") })).toHaveCount(0);
    await expect(panel.getByRole("button", { name: t("fileNumbering.title") })).toHaveCount(0);

    // Rows arrive from the served plan; the toolbar sits above the first one.
    const firstRow = dialog.getByText("/media/anime/Show - 01.mkv").first();
    await expect(firstRow).toBeVisible({ timeout: 5000 });

    const toolbarBox = (await toolbar.boundingBox())!;
    const firstRowBox = (await firstRow.boundingBox())!;
    expect(toolbarBox.y + toolbarBox.height, "工具栏应在预览行上方").toBeLessThanOrEqual(firstRowBox.y + 1);

    for (const button of [loadButton, numberingButton]) {
      const box = (await button.boundingBox())!;
      expect(box.x, "按钮在工具栏左边界内").toBeGreaterThanOrEqual(toolbarBox.x - 1);
      expect(box.x + box.width, "按钮在工具栏右边界内").toBeLessThanOrEqual(toolbarBox.x + toolbarBox.width + 1);
      expect(box.y, "按钮在工具栏上边界内").toBeGreaterThanOrEqual(toolbarBox.y - 1);
      expect(box.y + box.height, "按钮在工具栏下边界内").toBeLessThanOrEqual(toolbarBox.y + toolbarBox.height + 1);
    }

    if (isPhoneLayout(page)) {
      // Phone stacks the settings column under the rows, so the actions still
      // come before the form; the dialog never scrolls sideways.
      const panelBox = (await panel.boundingBox())!;
      expect(panelBox.y, "手机端设置列在行列表之后").toBeGreaterThan(firstRowBox.y);
      const viewport = page.viewportSize()!;
      expect(toolbarBox.width, "工具栏不超出视口").toBeLessThanOrEqual(viewport.width);
      for (const button of [loadButton, numberingButton]) {
        const box = (await button.boundingBox())!;
        expect(box.x + box.width, "手机端按钮不超出视口").toBeLessThanOrEqual(viewport.width);
      }
      const overflow = await dialog.evaluate((element) => element.scrollWidth - element.clientWidth);
      expect(overflow, "手机端对话框不横向滚动").toBeLessThanOrEqual(1);
    } else {
      // Desktop keeps the settings column beside the preview, not above it.
      const panelBox = (await panel.boundingBox())!;
      expect(panelBox.x, "桌面端设置列在预览列右侧").toBeGreaterThan(toolbarBox.x + toolbarBox.width - 1);
    }

    if (process.env.BANSOKO_E2E_SCREENSHOTS === "1") {
      await page.screenshot({
        path: `../.local-artifacts/recognition-toolbar-${testInfo.project.name}.png`,
        fullPage: false,
      });
    }
  });
});
