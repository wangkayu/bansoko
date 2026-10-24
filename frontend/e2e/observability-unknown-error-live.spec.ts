import { randomUUID } from "node:crypto";
import { execFile as execFileCallback } from "node:child_process";
import fs from "node:fs/promises";
import fsSync from "node:fs";
import path from "node:path";
import { promisify } from "node:util";
import { expect, test, type Page } from "@playwright/test";
import { shouldSkipAuthTests } from "./skip-auth-helper";

const execFile = promisify(execFileCallback);
const explicitBaseURL = process.env.BANSOKO_TEST_BASE_URL?.trim() ?? "";
const sessionDir = process.env.BANSOKO_BROWSER_SESSION_STATE_DIR?.trim() ?? "";
const testDatabaseURL = process.env.BANSOKO_TEST_DATABASE_URL?.trim() ?? "";
const originalTable = "managed_anime";

// This suite is intentionally limited to the disposable real-effect browser
// session. The real-effect Playwright config also pins all projects to one
// worker, so desktop and mobile cannot rename the shared table concurrently.
function livePrerequisite(): string | undefined {
  if (process.env.BANSOKO_E2E_FIXTURES === "1") return "OBS-016 evidence does not run in fixture mode";
  if (process.env.BANSOKO_E2E_REAL_EFFECT !== "1") return "BANSOKO_E2E_REAL_EFFECT=1 is required";
  if (process.env.BANSOKO_TEST_DB_DISPOSABLE !== "1") return "BANSOKO_TEST_DB_DISPOSABLE=1 is required";
  if (!explicitBaseURL) return "BANSOKO_TEST_BASE_URL must explicitly point at the disposable live server";
  if (!sessionDir) return "BANSOKO_BROWSER_SESSION_STATE_DIR is required";
  if (!testDatabaseURL) return "BANSOKO_TEST_DATABASE_URL is required";
  if (shouldSkipAuthTests()) return "auth.setup did not produce an authenticated storage state";
  if (!fsSync.existsSync(path.join(sessionDir, "browser-session.json"))) {
    return "browser-session.json is required; this is not a live browser session";
  }
  if (!fsSync.existsSync(path.join(sessionDir, "server.log"))) {
    return "the disposable session server.log is required";
  }
  return undefined;
}

function quoteIdentifier(value: string): string {
  return `"${value.replaceAll('"', '""')}"`;
}

function databaseNameFromURL(value: string): string {
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    throw new Error("BANSOKO_TEST_DATABASE_URL is not a PostgreSQL URL");
  }
  if (parsed.protocol !== "postgres:" && parsed.protocol !== "postgresql:") {
    throw new Error("BANSOKO_TEST_DATABASE_URL is not a PostgreSQL URL");
  }

  let databaseName: string;
  try {
    databaseName = decodeURIComponent(parsed.pathname.replace(/^\/+/, ""));
  } catch {
    throw new Error("BANSOKO_TEST_DATABASE_URL has an invalid database path");
  }
  if (!/^[A-Za-z0-9_][A-Za-z0-9_-]*$/.test(databaseName)) {
    throw new Error("BANSOKO_TEST_DATABASE_URL has an invalid database name");
  }
  return databaseName;
}

async function readSessionPGID(): Promise<string> {
  let metadata: unknown;
  try {
    metadata = JSON.parse(await fs.readFile(path.join(sessionDir, "browser-session.json"), "utf8")) as unknown;
  } catch {
    throw new Error("browser session metadata could not be read");
  }
  if (!metadata || typeof metadata !== "object" || Array.isArray(metadata)) {
    throw new Error("browser session metadata is invalid");
  }
  const pgID = (metadata as Record<string, unknown>).pg_id;
  if (typeof pgID !== "string" || !/^[A-Za-z0-9][A-Za-z0-9_.-]*$/.test(pgID)) {
    throw new Error("browser session metadata has no usable pg_id");
  }
  return pgID;
}

async function execPsql(pgID: string, databaseName: string, sql: string): Promise<string> {
  try {
    const result = await execFile(
      "docker",
      [
        "exec",
        pgID,
        "psql",
        "-U",
        "harness",
        "-d",
        databaseName,
        "-v",
        "ON_ERROR_STOP=1",
        "-X",
        "-q",
        "-A",
        "-t",
        "-c",
        sql,
      ],
      { maxBuffer: 1024 * 1024 },
    );
    return result.stdout.trim();
  } catch {
    // Do not let psql/docker diagnostics echo a DSN, password, or raw driver
    // error through the Playwright reporter.
    throw new Error("docker exec psql failed");
  }
}

async function restoreManagedAnimeTable(pgID: string, databaseName: string, temporaryTable: string): Promise<void> {
  const quotedTemporaryTable = quoteIdentifier(temporaryTable);
  await execPsql(
    pgID,
    databaseName,
    `DO $unknown_error_restore$
BEGIN
  IF to_regclass('public.${originalTable}') IS NULL
     AND to_regclass('public.${temporaryTable}') IS NOT NULL THEN
    EXECUTE 'ALTER TABLE public.${quotedTemporaryTable} RENAME TO ${originalTable}';
  END IF;
END
$unknown_error_restore$;`,
  );
}

async function tableExists(pgID: string, databaseName: string, tableName: string): Promise<boolean> {
  const result = await execPsql(
    pgID,
    databaseName,
    `SELECT to_regclass('public.${tableName}') IS NOT NULL;`,
  );
  return result === "t";
}

type DiagnosticEpisode = {
  id: string;
  severity: string;
  component: string;
  operation: string;
  error_code: string;
  scope_kind: string;
  scope_id: string;
  occurrence_count: number;
  last_error: string;
};

function parseDiagnosticEpisodes(payload: unknown): DiagnosticEpisode[] {
  if (!payload || typeof payload !== "object" || Array.isArray(payload)) {
    throw new Error("diagnostic API returned an invalid page");
  }
  const rawEpisodes = (payload as Record<string, unknown>).episodes;
  if (!Array.isArray(rawEpisodes)) throw new Error("diagnostic API returned no episode list");

  return rawEpisodes.flatMap((rawEpisode) => {
    if (!rawEpisode || typeof rawEpisode !== "object" || Array.isArray(rawEpisode)) return [];
    const episode = rawEpisode as Record<string, unknown>;
    if (
      typeof episode.id !== "string" ||
      typeof episode.severity !== "string" ||
      typeof episode.component !== "string" ||
      typeof episode.operation !== "string" ||
      typeof episode.error_code !== "string" ||
      typeof episode.scope_kind !== "string" ||
      typeof episode.scope_id !== "string" ||
      typeof episode.occurrence_count !== "number" ||
      typeof episode.last_error !== "string"
    ) {
      return [];
    }
    return [episode as unknown as DiagnosticEpisode];
  });
}

async function readDiagnosticEpisodes(page: Page): Promise<DiagnosticEpisode[]> {
  const response = await page.request.get("/api/v1/observability/diagnostics?component=httpapi&limit=100");
  if (response.status() !== 200) throw new Error("diagnostic API did not return 200");
  let payload: unknown;
  try {
    payload = JSON.parse(await response.text()) as unknown;
  } catch {
    throw new Error("diagnostic API returned invalid JSON");
  }
  return parseDiagnosticEpisodes(payload);
}

async function waitForDiagnosticEpisode(
  page: Page,
  baselineEpisodeID: string | undefined,
  baselineOccurrence: number,
): Promise<DiagnosticEpisode> {
  const deadline = Date.now() + 60000;
  while (Date.now() < deadline) {
    try {
      const episodes = await readDiagnosticEpisodes(page);
      const episode = episodes.find(
        (candidate) =>
          candidate.severity === "error" &&
          candidate.component === "httpapi" &&
          candidate.operation === "request" &&
          candidate.error_code === "http_500" &&
          candidate.scope_kind === "global" &&
          candidate.scope_id === "global" &&
          (candidate.id !== baselineEpisodeID || candidate.occurrence_count > baselineOccurrence),
      );
      if (episode) return episode;
    } catch {
      // The recorder/API may still be committing after the HTTP response.
    }
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  throw new Error("diagnostic API did not expose the new global http_500 episode");
}

type RuntimeEvent = Record<string, unknown>;

async function readStructuredRuntimeEvents(): Promise<RuntimeEvent[]> {
  let contents: string;
  try {
    contents = await fs.readFile(path.join(sessionDir, "server.log"), "utf8");
  } catch {
    return [];
  }
  const events: RuntimeEvent[] = [];
  for (const line of contents.split(/\r?\n/)) {
    if (!line.trim()) continue;
    try {
      const parsed = JSON.parse(line) as unknown;
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        events.push(parsed as RuntimeEvent);
      }
    } catch {
      // Build/runtime noise is not a structured runtime event.
    }
  }
  return events;
}

async function waitForRuntimeEvent(requestID: string): Promise<RuntimeEvent> {
  const deadline = Date.now() + 60000;
  while (Date.now() < deadline) {
    const event = (await readStructuredRuntimeEvents()).find(
      (candidate) =>
        candidate.request_id === requestID &&
        candidate.component === "httpapi" &&
        candidate.operation === "request" &&
        candidate.reason_code === "unknown_error",
    );
    if (event) return event;
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  throw new Error("server.log did not contain the request_id on an unknown-error runtime event");
}

const sensitivePatterns = [
  /postgres(?:ql)?:\/\//i,
  /\bpassword\b/i,
  /\bsecret\b/i,
  /\btoken\b/i,
  /\bauthorization\b/i,
  /\bbearer\b/i,
  /\bdsn\b/i,
  /https?:\/\//i,
  /magnet:/i,
  /(?:^|[^A-Za-z0-9])\/(?:home|private|Users)\//,
];

const databaseErrorPatterns = [/\brelation\b/i, /\bsqlstate\b/i, /managed_anime/i, /\bpq:\b/i, /\bpostgres\b/i];

function assertNoSensitiveMaterial(value: string, label: string, assertionMarker: string): void {
  if (value.includes(assertionMarker) || sensitivePatterns.some((pattern) => pattern.test(value))) {
    throw new Error(`${label} contains sensitive material`);
  }
}

function assertNoDatabaseErrorMaterial(value: string, label: string): void {
  if (databaseErrorPatterns.some((pattern) => pattern.test(value))) {
    throw new Error(`${label} contains underlying database error material`);
  }
}

function assertGenericUnknownResponse(responseText: string, payload: Record<string, unknown>, requestID: string, temporaryTable: string): void {
  if (payload.code !== "INTERNAL" || payload.message !== "The request failed.") {
    throw new Error("unknown application failure did not return the generic INTERNAL response");
  }
  if (payload.request_id !== requestID) throw new Error("generic INTERNAL response carried the wrong request_id");
  const lower = responseText.toLowerCase();
  if (lower.includes(temporaryTable.toLowerCase())) {
    throw new Error("generic INTERNAL response leaked the temporary database table name");
  }
  assertNoDatabaseErrorMaterial(responseText, "generic INTERNAL response");
}

async function inspectLogDialog(page: Page, testInfo: { project: { name: string } }, episode: DiagnosticEpisode, marker: string): Promise<void> {
  await page.goto("/");
  await expect(page.getByRole("navigation", { name: "主导航" })).toBeVisible({ timeout: 30000 });
  await page.getByRole("button", { name: "日志", exact: true }).click();

  const logDialog = page.getByRole("dialog").last();
  await expect(logDialog).toBeVisible({ timeout: 15000 });
  await expect(logDialog.getByText("日志", { exact: true })).toBeVisible();
  await logDialog.getByRole("tab", { name: "诊断", exact: true }).click();

  const componentSelect = logDialog.getByRole("combobox", { name: "类型", exact: true });
  await expect(componentSelect).toBeVisible({ timeout: 15000 });
  const filteredDiagnostics = page.waitForResponse(
    (response) => {
      const url = new URL(response.url());
      return (
        url.pathname.endsWith("/api/v1/observability/diagnostics") &&
        response.request().method() === "GET" &&
        url.searchParams.get("component") === "httpapi"
      );
    },
    { timeout: 30000 },
  );
  await componentSelect.click();
  await page.getByRole("option", { name: "接口请求", exact: true }).click();
  const filteredResponse = await filteredDiagnostics;
  if (filteredResponse.status() !== 200) throw new Error("diagnostic component filter did not return 200");

  const episodeItem = logDialog
    .getByRole("list", { name: "诊断列表" }).getByRole("listitem")
    .filter({ hasText: "接口请求 · 请求" })
    .filter({ hasText: "服务内部出错" })
    .first();
  await expect(episodeItem).toBeVisible({ timeout: 30000 });
  const uiText = (await episodeItem.textContent()) ?? "";
  // The migrated UI shows the localized component/operation and the mapped
  // http_500 message instead of the raw codes and the redacted last_error.
  if (!uiText.includes("接口请求 · 请求") || !uiText.includes("服务内部出错") || !/重复 \d+ 次/.test(uiText)) {
    throw new Error("diagnostic UI did not show the HTTP 500 episode");
  }
  assertNoSensitiveMaterial(uiText, "diagnostic UI", marker);
  assertNoDatabaseErrorMaterial(uiText, "diagnostic UI");
  if (!uiText.includes(`重复 ${episode.occurrence_count} 次`)) {
    throw new Error("diagnostic UI occurrence count does not match the diagnostic API");
  }

  const viewport = page.viewportSize();
  if (!viewport) throw new Error("Playwright viewport is unavailable");
  const dialogBox = await logDialog.boundingBox();
  if (!dialogBox) throw new Error("global log dialog has no measurable box");
  if (
    dialogBox.x < -1 ||
    dialogBox.y < -1 ||
    dialogBox.x + dialogBox.width > viewport.width + 1 ||
    dialogBox.y + dialogBox.height > viewport.height + 1
  ) {
    throw new Error("global log dialog is outside the viewport");
  }

  // The migrated Dialog renders its scrolling body as the element after the header.
  const dialogBody = logDialog.locator("header + div").first();
  await expect(dialogBody).toBeVisible();
  const bodyMetrics = await dialogBody.evaluate((element) => {
    const style = window.getComputedStyle(element);
    return {
      overflowY: style.overflowY,
      clientHeight: element.clientHeight,
      scrollHeight: element.scrollHeight,
      clientWidth: element.clientWidth,
      scrollWidth: element.scrollWidth,
    };
  });
  if (
    (bodyMetrics.overflowY !== "auto" && bodyMetrics.overflowY !== "scroll") ||
    bodyMetrics.scrollHeight < bodyMetrics.clientHeight ||
    bodyMetrics.scrollWidth > bodyMetrics.clientWidth + 1
  ) {
    throw new Error("global log dialog body is not a vertical scroll container without horizontal overflow");
  }

  const pageOverflow = await page.evaluate(() => ({
    document: document.documentElement.scrollWidth - document.documentElement.clientWidth,
    body: document.body.scrollWidth - document.body.clientWidth,
  }));
  if (pageOverflow.document > 1 || pageOverflow.body > 1) {
    throw new Error("global log page has horizontal overflow");
  }
  if (await page.locator('input[type="password"]:visible').count()) {
    throw new Error("credential input was visible when evidence was captured");
  }

  await fs.mkdir(path.join(sessionDir, "evidence"), { recursive: true });
  await logDialog.screenshot({
    path: path.join(sessionDir, "evidence", `observability-unknown-error-${testInfo.project.name}.png`),
    animations: "disabled",
  });
}

test.use({ trace: "off", video: "off", screenshot: "off" });
test.describe.configure({ mode: "serial", retries: 0 });

test.describe("OBS-016 unknown HTTP error diagnostic episode", () => {
  test.beforeEach(() => {
    const reason = livePrerequisite();
    test.skip(reason !== undefined, reason);
  });

  test("real authenticated GET 500 → runtime log → global diagnostic UI", async ({ page }, testInfo) => {
    test.setTimeout(180000);

    const pgID = await readSessionPGID();
    const databaseName = databaseNameFromURL(testDatabaseURL);
    const uniqueSuffix = randomUUID().replaceAll("-", "").slice(0, 12);
    const temporaryTable = `managed_anime_unknown_error_${testInfo.project.name}_${uniqueSuffix}`;
    // This opaque request marker is sent only as an HTTP header, never to SQL;
    // it lets every response/log/episode boundary prove the marker stayed out.
    const sensitiveMarker = `unknown-error-sensitive-marker-${uniqueSuffix}`;
    const requestID = `unknown-error-${testInfo.project.name}-${Date.now().toString(36)}-${uniqueSuffix}`;

    await page.goto("/");
    await expect(page.getByRole("navigation", { name: "主导航" })).toBeVisible({ timeout: 30000 });
    const existingEpisodes = await readDiagnosticEpisodes(page);
    const baselineEpisode = existingEpisodes.find(
      (episode) =>
        episode.severity === "error" &&
        episode.component === "httpapi" &&
        episode.operation === "request" &&
        episode.error_code === "http_500" &&
        episode.scope_kind === "global" &&
        episode.scope_id === "global",
    );
    const baselineOccurrence = baselineEpisode?.occurrence_count ?? 0;

    try {
      await execPsql(
        pgID,
        databaseName,
        `ALTER TABLE public.${quoteIdentifier(originalTable)} RENAME TO ${quoteIdentifier(temporaryTable)};`,
      );

      const response = await page.request.get("/api/v1/anime", {
        headers: {
          "X-Request-ID": requestID,
          "X-Sensitive-Marker": sensitiveMarker,
        },
      });
      const responseText = await response.text();
      if (response.status() !== 500) throw new Error("renamed managed-anime table did not produce a real HTTP 500");

      let payload: unknown;
      try {
        payload = JSON.parse(responseText) as unknown;
      } catch {
        throw new Error("unknown HTTP 500 response was not JSON");
      }
      if (!payload || typeof payload !== "object" || Array.isArray(payload)) {
        throw new Error("unknown HTTP 500 response had an invalid body");
      }
      const responsePayload = payload as Record<string, unknown>;
      const responseHeaderRequestID = response.headers()["x-request-id"];
      const responseBodyRequestID = responsePayload.request_id;
      if (
        typeof responseHeaderRequestID !== "string" ||
        responseHeaderRequestID.length === 0 ||
        typeof responseBodyRequestID !== "string" ||
        responseBodyRequestID.length === 0 ||
        responseHeaderRequestID !== responseBodyRequestID
      ) {
        throw new Error("unknown HTTP 500 response did not correlate one request_id in header and body");
      }
      const responseRequestID = responseHeaderRequestID;
      assertGenericUnknownResponse(responseText, responsePayload, responseRequestID, temporaryTable);
      assertNoSensitiveMaterial(responseText, "generic INTERNAL response", sensitiveMarker);

      const runtimeEvent = await waitForRuntimeEvent(responseRequestID);
      if (
        runtimeEvent.level !== "error" ||
        runtimeEvent.request_id !== responseRequestID ||
        runtimeEvent.component !== "httpapi" ||
        runtimeEvent.operation !== "request" ||
        runtimeEvent.outcome !== "failure" ||
        runtimeEvent.reason_code !== "unknown_error" ||
        runtimeEvent.error_class !== "unknown_application_error" ||
        runtimeEvent.message !== "request failed with unknown application error"
      ) {
        throw new Error("server.log contained no matching structured unknown-error event");
      }
      assertNoSensitiveMaterial(JSON.stringify(runtimeEvent), "correlated runtime log", sensitiveMarker);
      assertNoDatabaseErrorMaterial(JSON.stringify(runtimeEvent), "correlated runtime log");
    } finally {
      // This call is unconditional: even a failed assertion or response parse
      // must leave the shared disposable database with its production table name.
      await restoreManagedAnimeTable(pgID, databaseName, temporaryTable);
    }

    if (!(await tableExists(pgID, databaseName, originalTable)) || (await tableExists(pgID, databaseName, temporaryTable))) {
      throw new Error("managed_anime was not restored after the unknown-error request");
    }
    const recoveredResponse = await page.request.get("/api/v1/anime");
    if (recoveredResponse.status() !== 200) throw new Error("authenticated anime API did not recover after table restoration");

    const episode = await waitForDiagnosticEpisode(page, baselineEpisode?.id, baselineOccurrence);
    if (
      episode.severity !== "error" ||
      episode.component !== "httpapi" ||
      episode.operation !== "request" ||
      episode.error_code !== "http_500" ||
      episode.scope_kind !== "global" ||
      episode.scope_id !== "global" ||
      episode.occurrence_count <= baselineOccurrence
    ) {
      throw new Error("diagnostic API returned the wrong episode identity");
    }
    if (episode.last_error !== "<redacted>") {
      throw new Error("diagnostic episode last_error was not fixed redaction");
    }
    assertNoSensitiveMaterial(episode.last_error, "diagnostic episode", sensitiveMarker);
    assertNoDatabaseErrorMaterial(episode.last_error, "diagnostic episode");

    await inspectLogDialog(page, testInfo, episode, sensitiveMarker);
  });
});
