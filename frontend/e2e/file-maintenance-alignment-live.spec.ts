import { expect, test, type Locator, type Page } from "@playwright/test";

const baseURL = process.env.BANSOKO_TEST_BASE_URL?.trim();
const selectedScenario = process.env.BANSOKO_FM_E2E_UI_SCENARIO;
// Absolute path of the file this scenario moved on disk before the browser
// step. The preview must name exactly that file, so the assertion is about the
// drift the run produced rather than about any row at all.
const driftPath = process.env.BANSOKO_FM_E2E_UI_DRIFT_PATH?.trim();
// The placement this scenario moved on disk. The files dialog lists every
// imported file of the anime, so the row is addressed by the placement the
// runner drove rather than by a name that the alignment itself changes.
const placementID = process.env.BANSOKO_FM_E2E_UI_PLACEMENT_ID?.trim();

if (!baseURL) {
  throw new Error("BANSOKO_TEST_BASE_URL is required for FILE-022 browser scenarios");
}
if (!placementID) {
  throw new Error("BANSOKO_FM_E2E_UI_PLACEMENT_ID is required for FILE-022 browser scenarios");
}
if (selectedScenario !== "5" && selectedScenario !== "6") {
  throw new Error("BANSOKO_FM_E2E_UI_SCENARIO must select scenario 5 or 6");
}

// Serial: the two scenarios share one seeded anime and one library. The
// timeout is generous because a scenario waits for a real scan, calibration
// and alignment to settle on the VM, not for a fixture to answer.
test.describe.configure({ mode: "serial", timeout: 300_000 });

// The confirmation dialog lists every row the preview would change. This
// resolves the row for the file the runner moved, by basename substring so
// bracket-bearing file names need no regex escaping.
function driftRow(confirmDialog: Locator) {
  const basename = driftPath?.split("/").pop();
  if (!basename) {
    throw new Error("BANSOKO_FM_E2E_UI_DRIFT_PATH is required for FILE-022 browser scenarios");
  }
  return confirmDialog.locator("p", { hasText: basename }).filter({ hasText: "将变更：" }).first();
}

// The scanned-file row for the placement under test. Every other row in the
// dialog belongs to another file of the same work, so the assertions scope to
// this one instead of to "some file somewhere".
function fileRow(filesDialog: Locator) {
  return filesDialog.locator(`[data-placement-id="${placementID}"]`);
}

// The owner's scan action. The RSS tab's 扫描媒体库 button opens the same files
// dialog with scanOnOpen, but its toolbar only renders once the RSS list has
// loaded, and this fixture's feed is an unreachable example host — so the card's
// own 文件 entry is the entry point that does not depend on the feed. The dialog
// submits one scan and polls while the scan, the calibration and the alignment
// it admits run, rescaining once an alignment settles.
async function openFilesAndScan(page: Page, ownerCard: Locator): Promise<Locator> {
  await ownerCard.getByRole("button", { name: /更多操作/ }).click();
  await page.getByRole("menuitem", { name: "文件", exact: true }).click();
  const filesDialog = page.getByRole("dialog", { name: /《进击的巨人》的文件/ });
  await expect(filesDialog).toBeVisible({ timeout: 30_000 });
  const scanButton = filesDialog.getByRole("button", { name: "扫描", exact: true });
  await expect(scanButton).toBeEnabled({ timeout: 30_000 });
  await scanButton.click();
  return filesDialog;
}

// Evidence for a failed run: an app-level crash would otherwise surface only as
// an unrelated locator timeout, hiding the real cause.
const appErrors: string[] = [];

test.beforeEach(() => {
  appErrors.length = 0;
});

test.afterEach(() => {
  if (appErrors.length > 0) {
    console.log("FILE-022 browser page errors:", JSON.stringify(appErrors));
  }
});

test(`FILE-022 scenario ${selectedScenario}: alignment owner UI`, async ({ page }) => {
  page.on("pageerror", (error) => appErrors.push(String(error)));
  await page.goto(`${baseURL}/#/`);
  await expect(page.getByRole("navigation", { name: "主导航" })).toBeVisible();
  await page.getByRole("button", { name: "已订阅" }).click();
  // The fixture seeds two instances of the same work, so the card's title alone
  // is ambiguous: the TV instance the runner drives is labelled "E2E", the movie
  // one "剧场版". Pick the owner under test by its subscription label.
  const ownerCard = page.getByRole("article").filter({ hasText: "E2E" }).first();
  await expect(ownerCard.getByRole("button", { name: /打开配置/ })).toBeVisible();
  await ownerCard.getByRole("button", { name: /打开配置/ }).click();
  const dialog = page.getByRole("dialog").last();
  const tabStrip = dialog.locator("#anime-config-tabs");
  // Diagnostics only: these reads resolve immediately and never throw, so they
  // cannot replace the real failure with a diagnostic error.
  const tabDiagnostics = {
    tabStripCount: await page.locator("#anime-config-tabs").count(),
    tabs: await page.locator('#anime-config-tabs [role="tab"]').allTextContents(),
    dialog: await dialog.ariaSnapshot().catch(() => ""),
    appErrors,
  };
  console.log("FILE-022 anime tabs DOM:", JSON.stringify(tabDiagnostics));
  await tabStrip.getByRole("tab", { name: "规则" }).click();
  await expect(page.getByText("开启后，扫描发现命名与目标不一致时会自动重命名或移动本地文件")).toBeVisible();

  // Both scenarios enable alignment the same way: the switch only stages the
  // change in the rules tab's draft, the confirm dialog shows the drift the
  // preview would move, and the footer's single 保存 commits it.
  await page.getByRole("switch", { name: "自动对齐", exact: true }).click();
  const confirmDialog = page.getByRole("dialog", { name: "开启自动对齐？" });
  await expect(confirmDialog).toBeVisible();
  await expect(driftRow(confirmDialog)).toBeVisible();
  await confirmDialog.getByRole("button", { name: "开启自动对齐" }).click();
  await expect(page.getByRole("switch", { name: "自动对齐", exact: true })).toBeChecked();
  await dialog.locator(".anime-config-dialog-footer-owner").getByRole("button", { name: "保存", exact: true }).click();
  await expect(page.getByText("已保存", { exact: true })).toBeVisible({ timeout: 15000 });
  // The files dialog is the card's own surface, not a config tab: close the
  // config dialog first so the card menu is reachable again.
  // Two controls are named 关闭 (the header's close icon and the footer's button),
  // so address the icon by the label only it carries.
  await dialog.getByLabel("关闭").click();
  await expect(dialog).toBeHidden({ timeout: 15_000 });

  const filesDialog = await openFilesAndScan(page, ownerCard);
  const row = fileRow(filesDialog);
  await expect(row).toBeVisible({ timeout: 60_000 });

  if (selectedScenario === "5") {
    // Alignment is on, the drifted file has a free canonical target: the scan
    // the dialog submits admits the move, the dialog rescans when it settles,
    // and the row reads 已对齐 — the owner sees the outcome without asking.
    await expect(row.getByText("已对齐", { exact: true })).toBeVisible({ timeout: 180_000 });
    await expect(row.getByText(/对齐失败/)).toHaveCount(0);
    await page.screenshot({ path: test.info().outputPath("file022-scenario-5.png"), fullPage: true });
    return;
  }

  // The canonical target is occupied by a foreign file, so the same owner action
  // can only leave the row where it is: the owner's outcome is that the file did
  // not move, which the row keeps showing as 待对齐. The 对齐失败 pill is scoped
  // to the generation whose alignment settled, and the dialog rescans as soon as
  // that alignment ends, so the pill is not what survives — if it is on screen at
  // all it must name the occupied target, never another reason. The runner
  // independently proves the operation settled blocked/target_occupied.
  await expect(row.getByText("待对齐", { exact: true })).toBeVisible({ timeout: 180_000 });
  await expect(row.getByText(/对齐失败：(?!目标位置已被占用)/)).toHaveCount(0);
  await page.screenshot({ path: test.info().outputPath("file022-scenario-6.png"), fullPage: true });
});
