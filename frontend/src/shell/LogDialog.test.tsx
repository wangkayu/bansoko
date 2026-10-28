import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DiagnosticEpisode, ObservabilityRecord } from "../lib/api/client";
import { tryT, t } from "../lib/i18n";
import { LogDialog } from "./LogDialog";
import { useLogDialogStore } from "./logDialogStore";
import { rawLogText } from "./LogRawView";

let fetchMock: ReturnType<typeof vi.fn>;
let records: ObservabilityRecord[];
let episodes: DiagnosticEpisode[];

const delivery = {
  record_type: "plugin",
  delivery: {
    id: "del-1",
    adapter_kind: "telegram",
    trigger_kind: "download_submitted",
    target_display: "Channel",
    state: "unknown",
    created_at: "2026-10-21T08:00:00Z",
    updated_at: "2026-10-21T08:05:00Z",
  },
} as unknown as ObservabilityRecord;

function business(id: string, extra: Record<string, unknown> = {}): ObservabilityRecord {
  return {
    record_type: "business",
    entry: {
      id,
      subscription_id: "sub-1",
      managed_anime_id: "anime-1",
      anime_title: "Example Show",
      source: "subscription",
      kind: "subscription_created",
      outcome: "success",
      occurred_at: "2026-10-21T08:00:00Z",
      message_code: "subscription_created",
      message_params: {},
      dedupe_key: id,
      retention_until: "2026-11-21T08:00:00Z",
      ...extra,
    },
  } as ObservabilityRecord;
}

const episode = {
  id: "ep-1",
  kind: "downloader",
  variant: "unreachable",
  target: { kind: "downloader", id: "d1", name: "qB Main", host: "qb.local" },
  detail: { error_class: "timeout" },
  recent: [{ at: "2026-10-21T08:00:00Z", error_class: "timeout", via: "direct", host: "qb.local" }],
  first_failed_at: "2026-10-21T08:00:00Z",
  last_failed_at: "2026-10-21T09:00:00Z",
  succeeded_after_at: null,
  occurrence_count: 3,
  retry: { mode: "resume" },
  severity: "error",
  retention_until: "2026-11-21T08:00:00Z",
} as DiagnosticEpisode;

const runtimeLines = [
  { at: "2026-10-21T08:00:00Z", level: "warn", message: "slow response", component: "httpapi", operation: "request", request_id: "req-1" },
];

function stubFetch() {
  fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost");
    let body: unknown = [];
    if (url.pathname.endsWith("/observability/records")) body = { records, next_cursor: null };
    else if (url.pathname.endsWith("/observability/diagnostics")) {
      body = { episodes, internal: { occurrence_count: 2, last_failed_at: "2026-10-21T09:30:00Z", last_request_id: "req-9" }, next_cursor: null };
    } else if (url.pathname.endsWith("/observability/runtime-log")) body = { lines: runtimeLines };
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  });
  vi.stubGlobal("fetch", fetchMock);
}

function urlsOf(part: string): URL[] {
  return fetchMock.mock.calls.map(([input]) => new URL(String(input), "http://localhost")).filter((url) => url.pathname.includes(part));
}

function renderLogDialog() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <LogDialog />
    </QueryClientProvider>,
  );
}

describe("LogDialog", () => {
  beforeEach(() => {
    localStorage.clear();
    records = [business("b1"), delivery];
    episodes = [episode];
    stubFetch();
    useLogDialogStore.getState().openDialog();
  });

  afterEach(() => {
    useLogDialogStore.getState().closeDialog();
    vi.unstubAllGlobals();
  });

  it("lists events through their message code and shows an unconfirmed notification as unconfirmed", async () => {
    renderLogDialog();
    expect(await screen.findByText(tryT("log.msg.subscription_created")!)).toBeInTheDocument();
    expect(await screen.findByText(t("log.delivery.unconfirmed"))).toBeInTheDocument();
    const query = urlsOf("/observability/records")[0]!.searchParams;
    expect(query.getAll("kind").sort()).toEqual(["business", "plugin"]);
  });

  it("sends the failure state to the server", async () => {
    renderLogDialog();
    await screen.findByText(tryT("log.msg.subscription_created")!);
    const user = userEvent.setup();
    await user.click(screen.getByRole("combobox", { name: t("log.status") }));
    await user.click(await screen.findByRole("option", { name: t("log.failure") }));
    await waitFor(() => expect(urlsOf("/observability/records").some((url) => url.searchParams.get("state") === "failure")).toBe(true));
  });

  it("labels a one-shot acquisition", async () => {
    records = [
      business("s1", { kind: "acquisition_settled", message_code: "acquisition_succeeded", release_id: "r1", source: "one_shot" }),
    ];
    renderLogDialog();
    expect(await screen.findByText(t("log.oneShot"))).toBeInTheDocument();
  });

  it("diagnostics: one row per condition, worded from the catalog without paths, plus the internal row", async () => {
    renderLogDialog();
    fireEvent.click(await screen.findByRole("tab", { name: t("log.tabDiagnostics") }));
    expect(await screen.findByText(tryT("log.diag.downloader.unreachable", { name: "qB Main", n: 1 })!)).toBeInTheDocument();
    expect(screen.getByText(t("log.diag.internal", { n: 2 }))).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/\/(config|media|data)\b/);
    expect(urlsOf("/observability/diagnostics").length).toBeGreaterThan(0);
  });

  it("raw log sub-view reads the runtime log with the chosen level", async () => {
    renderLogDialog();
    fireEvent.click(await screen.findByRole("tab", { name: t("log.tabDiagnostics") }));
    fireEvent.click(await screen.findByRole("button", { name: t("log.rawlog.link") }));
    expect(await screen.findByText(/slow response/)).toBeInTheDocument();
    expect(urlsOf("/observability/runtime-log")[0]!.searchParams.get("level")).toBe("all");
    const user = userEvent.setup();
    await user.click(screen.getByRole("combobox", { name: t("log.rawlog.level") }));
    await user.click(await screen.findByRole("option", { name: t("log.rawlog.levelError") }));
    await waitFor(() => expect(urlsOf("/observability/runtime-log").some((url) => url.searchParams.get("level") === "error")).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: t("log.rawlog.back") }));
    expect(await screen.findByText(t("log.diag.internal", { n: 2 }))).toBeInTheDocument();
  });
});

describe("rawLogText", () => {
  it("builds one plain line per entry with the structured fields", () => {
    const text = rawLogText(runtimeLines as never);
    expect(text).toBe("2026-10-21T08:00:00Z WARN httpapi.request slow response request_id=req-1");
  });
});
