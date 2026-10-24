import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DiagnosticEpisode, ObservabilityRecord } from "../lib/api/client";
import { observabilityKeys } from "../lib/queryKeys";
import { t } from "../lib/i18n";
import { LogDialog } from "./LogDialog";
import { useLogFailureBadge } from "./logSeen";

// The log entry badge: only failures visible in the log, and only those after they were last seen in this browser.

function business(id: string, outcome: "success" | "failure", occurredAt: string): ObservabilityRecord {
  return {
    record_type: "business",
    entry: {
      id,
      subscription_id: "subscription-1",
      managed_anime_id: "anime-1",
      kind: "release_processed",
      outcome,
      occurred_at: occurredAt,
      title: `标题 ${id}`,
      summary: `摘要 ${id}`,
      dedupe_key: id,
      retention_until: "2026-12-01T00:00:00Z",
    },
  };
}

const routineFailure: ObservabilityRecord = {
  record_type: "routine",
  routine: {
    id: "routine-1",
    kind: "catalog_refresh",
    scope_kind: "provider",
    scope_id: "mikan",
    outcome: "failure",
    last_executed_at: "2026-10-20T09:00:00Z",
    last_result_code: "catalog_refresh_failed",
    summary: "刷新失败",
    updated_at: "2026-10-20T09:00:00Z",
  },
};

const automationFailure = {
  record_type: "automation",
  run: { id: "run-1", kind: "rss_refresh", trigger: "schedule", state: "failed", created_at: "2026-10-20T09:00:00Z" },
} as unknown as ObservabilityRecord;

function diagnostic(id: string, lastFailedAt: string, succeededAfterAt?: string): DiagnosticEpisode {
  return {
    id,
    component: "rss",
    operation: "refresh",
    severity: "error",
    error_code: "rss_fetch_failed",
    first_failed_at: lastFailedAt,
    last_failed_at: lastFailedAt,
    occurrence_count: 1,
    details_status: "available",
    ...(succeededAfterAt ? { succeeded_after_at: succeededAfterAt } : {}),
  } as DiagnosticEpisode;
}

let records: ObservabilityRecord[];
let episodes: DiagnosticEpisode[];
let fetchMock: ReturnType<typeof vi.fn>;

function serverOutcome(record: ObservabilityRecord): string {
  if (record.record_type === "business") return record.entry.outcome;
  if (record.record_type === "routine") return record.routine.outcome;
  if (record.record_type === "plugin") return record.delivery.state === "failed" ? "failure" : record.delivery.state;
  return "failure";
}

// A fake server: narrowed by kind / outcome / severity, in descending time order.
function stubFetch() {
  fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost");
    let body: unknown = [];
    if (url.pathname.endsWith("/observability/records")) {
      const kinds = url.searchParams.getAll("kind");
      const outcome = url.searchParams.get("outcome");
      body = {
        records: records.filter(
          (record) => (kinds.length === 0 || kinds.includes(record.record_type)) && (!outcome || serverOutcome(record) === outcome),
        ),
        next_cursor: null,
      };
    } else if (url.pathname.endsWith("/observability/diagnostics")) {
      const severity = url.searchParams.get("severity");
      body = { episodes: episodes.filter((episode) => !severity || episode.severity === severity), next_cursor: null };
    }
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  });
  vi.stubGlobal("fetch", fetchMock);
}

function Harness() {
  const count = useLogFailureBadge();
  const [open, setOpen] = useState(false);
  const [view, setView] = useState("records");
  return (
    <>
      <span data-testid="badge">{count}</span>
      <button type="button" onClick={() => setOpen(true)}>
        打开日志
      </button>
      <LogDialog open={open} onOpenChange={setOpen} view={view} onViewChange={setView} />
    </>
  );
}

function renderHarness() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <Harness />
    </QueryClientProvider>,
  );
  return client;
}

async function expectBadge(value: number) {
  await waitFor(() => expect(screen.getByTestId("badge")).toHaveTextContent(String(value)));
}

describe("日志入口徽标", () => {
  beforeEach(() => {
    localStorage.clear();
    records = [routineFailure, automationFailure, business("b-old", "failure", "2026-10-20T08:00:00Z"), business("b-ok", "success", "2026-10-20T07:00:00Z")];
    episodes = [diagnostic("d-open", "2026-10-20T08:30:00Z"), diagnostic("d-recovered", "2026-10-20T06:00:00Z", "2026-10-20T06:30:00Z")];
    stubFetch();
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("不算 routine/automation 失败和已恢复的诊断", async () => {
    renderHarness();
    await expectBadge(2);
    const recordURLs = fetchMock.mock.calls.map(([input]) => String(input)).filter((url) => url.includes("/observability/records"));
    expect(recordURLs.length).toBeGreaterThan(0);
    for (const url of recordURLs) expect(url).not.toMatch(/kind=(routine|automation)/);
  });

  it("打开日志后清零，之后的新失败再次计入并标“新”", async () => {
    const client = renderHarness();
    await expectBadge(2);

    fireEvent.click(screen.getByRole("button", { name: "打开日志" }));
    await screen.findByText("标题 b-old");
    await expectBadge(1);
    fireEvent.click(screen.getByRole("tab", { name: t("log.tabDiagnostics") }));
    await expectBadge(0);
    // The first opening has no old mark: nothing is tagged as new.
    expect(screen.queryByText(t("log.new"))).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: t("log.tabRecords") }));
    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
    await waitFor(() => expect(screen.queryByText("标题 b-old")).not.toBeInTheDocument());

    records = [business("b-new", "failure", "2026-10-20T10:00:00Z"), ...records];
    await act(() => client.invalidateQueries({ queryKey: observabilityKeys.all }));
    await expectBadge(1);

    fireEvent.click(screen.getByRole("button", { name: "打开日志" }));
    await screen.findByText("标题 b-new");
    expect(screen.getAllByText(t("log.new"))).toHaveLength(1);
    await expectBadge(0);
  });

  it("localStorage 不可用时照常渲染，计入全部失败", async () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    renderHarness();
    await expectBadge(2);
    fireEvent.click(screen.getByRole("button", { name: "打开日志" }));
    await screen.findByText("标题 b-old");
    await expectBadge(2);
  });
});
