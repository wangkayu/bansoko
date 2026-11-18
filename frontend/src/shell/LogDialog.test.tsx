import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ObservabilityRecord, RuntimeLogLine } from "../lib/api/client";
import { notificationTriggerKindLabel } from "../lib/labels";
import { tryT, t } from "../lib/i18n";
import { LogDialog } from "./LogDialog";
import { useLogDialogStore } from "./logDialogStore";
import { rawLogText } from "./LogRawView";

let fetchMock: ReturnType<typeof vi.fn>;
let records: ObservabilityRecord[];
let primaryRecords: ObservabilityRecord[] | null;
let batchDetails: ObservabilityRecord[];
let runtimeFeed: RuntimeLogLine[];
let runtimeSource: "file" | "memory";
let runtimeGapNext: boolean;
let runtimeRequests: URL[];

const delivery = {
  record_type: "plugin",
  delivery: {
    id: "del-1",
    plugin_target_config_id: "target-1",
    adapter_kind: "telegram",
    trigger_kind: "download_submitted",
    target_display: "Channel",
    payload_status: "available",
    payload: { facts: { "anime.title": "Example Show", "ep.number": 3 } },
    state: "unknown",
    attempt_count: 0,
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

const runtimeLines = [
  { at: "2026-10-21T08:00:00Z", level: "warn", message: "slow response", component: "httpapi", operation: "request", request_id: "req-1" },
  {
    at: "2026-10-21T08:01:00Z",
    level: "error",
    message: "provider search failed",
    component: "metadata_matcher",
    operation: "tmdb_search",
    outcome: "failure",
    kind: "metadata_match",
    work_item_id: "wi-1",
    stage: "search",
    state_before: "queued",
    state_after: "failed",
    disposition: "retry",
    owner_type: "subscription",
    attempt: 2,
    duration_ms: 1234,
    retryable: false,
  },
] as RuntimeLogLine[];

function runtimeLine(index: number, extra: Partial<RuntimeLogLine> = {}): RuntimeLogLine {
  return {
    at: new Date(Date.UTC(2026, 10, 4, 6, 0, 0) + index * 1000).toISOString(),
    level: "info",
    message: `runtime line ${index}`,
    component: "worker",
    operation: "tick",
    ...extra,
  };
}

type TestCursor = { source: "file" | "memory"; kind: "a" | "b"; index: number };

function encodeTestCursor(source: "file" | "memory", kind: "a" | "b", index: number): string {
  return `${source}:${kind}:${index}`;
}

function decodeTestCursor(value: string | null): TestCursor | null {
  if (!value) return null;
  const [source, kind, raw] = value.split(":");
  if ((source !== "file" && source !== "memory") || (kind !== "a" && kind !== "b")) return null;
  const index = Number(raw);
  if (!Number.isInteger(index) || index < 0) return null;
  return { source, kind, index };
}

function runtimePage(url: URL) {
  const level = url.searchParams.get("level") ?? "all";
  const terms = (url.searchParams.get("q") ?? "").toLowerCase().split(/\s+/).filter(Boolean);
  const workItemId = url.searchParams.get("work_item_id") ?? "";
  const limit = Number(url.searchParams.get("limit") ?? "200");
  const after = decodeTestCursor(url.searchParams.get("after"));
  const before = decodeTestCursor(url.searchParams.get("before"));
  let gap = runtimeGapNext;
  runtimeGapNext = false;

  const indexed = runtimeFeed
    .map((line, index) => ({ line, index }))
    .filter(({ line }) => level !== "error" || line.level === "error")
    .filter(({ line }) => (level !== "warn" ? true : line.level === "warn" || line.level === "error"))
    .filter(({ line }) => workItemId === "" || line.work_item_id === workItemId)
    .filter(({ line }) => terms.every((term) => JSON.stringify(line).toLowerCase().includes(term)));

  let slice: { line: RuntimeLogLine; index: number }[];
  // `after` is inclusive: it names the next unread line. `before` excludes the
  // first line of the region already shown.
  if (before) {
    if (before.source !== runtimeSource) {
      gap = true;
      slice = indexed.slice(-limit);
    } else {
      slice = indexed.filter(({ index }) => index < before.index).slice(-limit);
    }
  } else if (after) {
    if (after.source !== runtimeSource) {
      gap = true;
      slice = indexed.slice(0, limit);
    } else {
      slice = indexed.filter(({ index }) => index >= after.index).slice(0, limit);
    }
  } else {
    slice = indexed.slice(-limit);
  }

  let nextAfter: string;
  if (before) {
    nextAfter = encodeTestCursor(runtimeSource, "a", before.source === runtimeSource ? before.index : runtimeFeed.length);
  } else if (after && slice.length > 0 && slice.length === limit) {
    nextAfter = encodeTestCursor(runtimeSource, "a", slice[slice.length - 1]!.index + 1);
  } else {
    nextAfter = encodeTestCursor(runtimeSource, "a", runtimeFeed.length);
  }

  return {
    lines: slice.map(({ line }) => line),
    source: runtimeSource,
    ...(slice.length === 0 ? {} : { next_before: encodeTestCursor(runtimeSource, "b", slice[0]!.index) }),
    next_after: nextAfter,
    ...(gap ? { gap: true } : {}),
  };
}

function stubFetch() {
  fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost");
    let body: unknown = [];
    if (url.pathname.endsWith("/observability/records")) {
      const primary = url.searchParams.get("event_view") === "primary";
      body = { records: primary && primaryRecords !== null ? primaryRecords : records, next_cursor: null };
    } else if (url.pathname.includes("/observability/release-batches/")) {
      const limit = Number(url.searchParams.get("limit") ?? "50");
      body = { records: batchDetails.slice(0, limit), next_cursor: batchDetails.length > limit ? "c1" : null };
    } else if (url.pathname.endsWith("/plugin-targets")) {
      body = { targets: [{ id: "target-1", name: "Main Channel" }] };
    } else if (url.pathname.endsWith("/plugin-deliveries/del-1")) {
      const base = (delivery as unknown as { delivery: Record<string, unknown> }).delivery;
      body = {
        delivery: {
          ...base,
          rendered_title: "Example Show 第 3 集",
          rendered_body: "Example Show 第 3 集已经提交下载。",
        },
        attempts: [],
      };
    } else if (url.pathname.endsWith("/observability/runtime-log")) {
      runtimeRequests.push(url);
      body = runtimePage(url);
    }
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
    primaryRecords = null;
    batchDetails = [];
    runtimeFeed = [...runtimeLines];
    runtimeSource = "file";
    runtimeGapNext = false;
    runtimeRequests = [];
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
    // The events tab asks for the primary projection; it never loads all and filters locally.
    expect(query.get("event_view")).toBe("primary");
    // The runtime log is fetched only while its tab is visible.
    expect(urlsOf("/observability/runtime-log")).toHaveLength(0);
  });

  it("sends the failure state to the server", async () => {
    renderLogDialog();
    await screen.findByText(tryT("log.msg.subscription_created")!);
    const user = userEvent.setup();
    await user.click(screen.getByRole("combobox", { name: t("log.status") }));
    await user.click(await screen.findByRole("option", { name: t("log.failure") }));
    await waitFor(() => expect(urlsOf("/observability/records").some((url) => url.searchParams.get("state") === "failure")).toBe(true));
  });

  it("filters records by type on the server and keeps the primary projection", async () => {
    renderLogDialog();
    await screen.findByText(tryT("log.msg.subscription_created")!);
    const user = userEvent.setup();
    await user.click(screen.getByRole("combobox", { name: t("log.type") }));
    await user.click(await screen.findByRole("option", { name: t("log.typePlugin") }));
    await waitFor(() =>
      expect(
        urlsOf("/observability/records").some(
          (url) => url.searchParams.getAll("kind").join() === "plugin" && url.searchParams.get("event_view") === "primary",
        ),
      ).toBe(true),
    );
  });

  it("opens a compact delivery detail with the target name and rendered message", async () => {
    renderLogDialog();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: t("log.openDelivery", { kind: notificationTriggerKindLabel("download_submitted") }) }));
    expect(await screen.findByText("Example Show 第 3 集")).toBeInTheDocument();
    const detail = screen.getByRole("dialog", { name: t("settings.delivery.detail.title") });
    expect(within(detail).getByText(/Main Channel（Telegram）/)).toBeInTheDocument();
    expect(within(detail).getByText(/已经提交下载/)).toBeInTheDocument();
  });

  it("labels a one-shot acquisition", async () => {
    records = [
      business("s1", { kind: "acquisition_settled", message_code: "acquisition_succeeded", release_id: "r1", source: "one_shot" }),
    ];
    renderLogDialog();
    expect(await screen.findByText(t("log.oneShot"))).toBeInTheDocument();
  });

  it("a release batch row expands to its first details and opens the full list", async () => {
    records = [
      business("bh1", {
        kind: "release_batch_processed",
        outcome: "info",
        message_code: "release_batch_first",
        message_params: { count: 25 },
        subject_id: "bk-1",
        batch_counts: { total: 25, by_verdict: { filtered: 25 }, by_reason: { release_not_in_matched: 25 } },
      }),
    ];
    batchDetails = Array.from({ length: 25 }, (_, index) =>
      business(`d${index}`, {
        kind: "release_processed",
        outcome: "info",
        message_code: "release_filtered",
        occurred_at: `2026-10-21T07:${String(59 - index).padStart(2, "0")}:00Z`,
        details: { release_title: `[Group] Example - ${String(index + 1).padStart(2, "0")} [1080p]` },
      }),
    );
    renderLogDialog();
    const row = await screen.findByRole("button", { name: new RegExp(t("log.msg.release_batch_first", { count: 25 })) });
    const user = userEvent.setup();
    await user.click(row);
    // The preview stops at 6 so the head, its details and the link share one
    // screen; the rest are behind the dialog.
    expect(await screen.findByText(/Example - 01 \[1080p\]/)).toBeInTheDocument();
    expect(screen.queryByText(/Example - 07 \[1080p\]/)).not.toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: new RegExp(t("log.batch.viewAll", { n: 25 })) }));
    expect(await screen.findByText(new RegExp(t("log.batch.detailsTitle")))).toBeInTheDocument();
    expect(await screen.findByText(/Example - 07 \[1080p\]/)).toBeInTheDocument();
    expect(await screen.findByText(/Example - 21 \[1080p\]/)).toBeInTheDocument();
  });

  it("raw log search stacks with the level and sends every term", async () => {
    renderLogDialog();
    fireEvent.click(await screen.findByRole("tab", { name: t("log.tabRaw") }));
    expect(await screen.findByText(/slow response/)).toBeInTheDocument();
    // Every projected safe field is rendered, including task-lifecycle facts.
    expect(screen.getByText("state_before")).toBeInTheDocument();
    expect(screen.getByText("queued")).toBeInTheDocument();
    expect(screen.getByText("work_item_id")).toBeInTheDocument();
    expect(screen.getByText("wi-1")).toBeInTheDocument();
    expect(screen.getByText("retryable")).toBeInTheDocument();
    expect(screen.getByText("false")).toBeInTheDocument();
    const user = userEvent.setup();
    await user.type(screen.getByRole("searchbox", { name: t("log.rawlog.search") }), "provider RETRY");
    await waitFor(() =>
      expect(
        urlsOf("/observability/runtime-log").some((url) => url.searchParams.get("q") === "provider RETRY" && url.searchParams.get("level") === "all"),
      ).toBe(true),
    );
    expect(await screen.findByText(/provider search failed/)).toBeInTheDocument();
    expect(screen.queryByText(/slow response/)).not.toBeInTheDocument();
  });

  it("raw log reads the runtime log with the chosen level", async () => {
    renderLogDialog();
    fireEvent.click(await screen.findByRole("tab", { name: t("log.tabRaw") }));
    expect(await screen.findByText(/slow response/)).toBeInTheDocument();
    expect(urlsOf("/observability/runtime-log")[0]!.searchParams.get("level")).toBe("all");
    const user = userEvent.setup();
    await user.click(screen.getByRole("combobox", { name: t("log.rawlog.level") }));
    await user.click(await screen.findByRole("option", { name: t("log.rawlog.levelError") }));
    await waitFor(() => expect(urlsOf("/observability/runtime-log").some((url) => url.searchParams.get("level") === "error")).toBe(true));
    fireEvent.click(screen.getByRole("tab", { name: t("log.tabRecords") }));
    expect(await screen.findByText(tryT("log.msg.subscription_created")!)).toBeInTheDocument();
  });

  it("refreshes the runtime log every 5 seconds while its tab is visible", async () => {
    renderLogDialog();
    fireEvent.click(await screen.findByRole("tab", { name: t("log.tabRaw") }));
    expect(await screen.findByText(/slow response/)).toBeInTheDocument();
    const before = runtimeRequests.length;
    // Two polls: the page is empty, so the same next_after watermark must be
    // reused instead of falling back to an uncursored full read.
    await waitFor(() => expect(runtimeRequests.length).toBeGreaterThanOrEqual(before + 2), { timeout: 12000, interval: 200 });
    const [firstPoll, secondPoll] = runtimeRequests.slice(before);
    expect(firstPoll!.searchParams.get("after")).toBe("file:a:2");
    expect(firstPoll!.searchParams.get("before")).toBeNull();
    expect(secondPoll!.searchParams.get("after")).toBe(firstPoll!.searchParams.get("after"));
  }, 14_000);

  it("loads older history with the before cursor and prepends it once", async () => {
    runtimeFeed = Array.from({ length: 260 }, (_, index) => runtimeLine(index));
    renderLogDialog();
    fireEvent.click(await screen.findByRole("tab", { name: t("log.tabRaw") }));
    expect(await screen.findByText("runtime line 259", { exact: true })).toBeInTheDocument();
    expect(document.querySelectorAll("[data-runtime-row]")).toHaveLength(200);
    expect(screen.queryByText("runtime line 0", { exact: true })).not.toBeInTheDocument();
    expect(runtimeRequests[0]!.searchParams.get("before")).toBeNull();
    expect(runtimeRequests[0]!.searchParams.get("after")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: t("log.rawlog.loadOlder") }));
    expect(await screen.findByText("runtime line 0", { exact: true })).toBeInTheDocument();
    expect(document.querySelectorAll("[data-runtime-row]")).toHaveLength(260);
    expect(screen.getAllByText("runtime line 60", { exact: true })).toHaveLength(1);
    expect(screen.getByText(t("log.rawlog.shownCount", { n: 260 }))).toBeInTheDocument();
    const older = runtimeRequests.filter((url) => url.searchParams.get("before") !== null);
    expect(older).toHaveLength(1);
    expect(older[0]!.searchParams.get("before")).toBe("file:b:60");
  });

  it("traces one task at all levels, clears text, and returns to the previous filters", async () => {
    runtimeFeed = [
      runtimeLine(0, { level: "error", message: "provider task failed", work_item_id: "wi-9" }),
      runtimeLine(1, { level: "info", message: "provider task step", work_item_id: "wi-9" }),
      runtimeLine(2, { level: "warn", message: "provider task warning", work_item_id: "wi-9" }),
      runtimeLine(3, { level: "error", message: "provider other failure" }),
    ];
    renderLogDialog();
    fireEvent.click(await screen.findByRole("tab", { name: t("log.tabRaw") }));
    expect(await screen.findByText("provider task failed")).toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getByRole("combobox", { name: t("log.rawlog.level") }));
    await user.click(await screen.findByRole("option", { name: t("log.rawlog.levelError") }));
    await user.type(screen.getByRole("searchbox", { name: t("log.rawlog.search") }), "provider task");
    await waitFor(() =>
      expect(
        runtimeRequests.some(
          (url) => url.searchParams.get("level") === "error" && url.searchParams.get("q") === "provider task",
        ),
      ).toBe(true),
    );
    await waitFor(() => expect(screen.queryByText("provider task warning")).not.toBeInTheDocument());

    await user.click(screen.getAllByRole("button", { name: t("log.rawlog.viewTask") })[0]!);
    await waitFor(() =>
      expect(
        runtimeRequests.some(
          (url) =>
            url.searchParams.get("work_item_id") === "wi-9" &&
            url.searchParams.get("level") === "all" &&
            url.searchParams.get("q") === null,
        ),
      ).toBe(true),
    );
    expect(await screen.findByText(t("log.rawlog.taskFilter", { id: "wi-9" }))).toBeInTheDocument();
    expect(screen.getByRole("searchbox", { name: t("log.rawlog.search") })).toHaveValue("");
    expect(await screen.findByText("provider task warning")).toBeInTheDocument();
    expect(screen.queryByText("provider other failure")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: t("log.rawlog.backToFilters") }));
    await waitFor(() =>
      expect(
        runtimeRequests.some(
          (url) =>
            url.searchParams.get("work_item_id") === null &&
            url.searchParams.get("level") === "error" &&
            url.searchParams.get("q") === "provider task",
        ),
      ).toBe(true),
    );
    expect(screen.getByRole("searchbox", { name: t("log.rawlog.search") })).toHaveValue("provider task");
    expect(screen.queryByText(t("log.rawlog.taskFilter", { id: "wi-9" }))).not.toBeInTheDocument();
  });

  it("restarts without mixing sources and explains the gap", async () => {
    runtimeFeed = Array.from({ length: 260 }, (_, index) => runtimeLine(index));
    renderLogDialog();
    fireEvent.click(await screen.findByRole("tab", { name: t("log.tabRaw") }));
    expect(await screen.findByText("runtime line 259", { exact: true })).toBeInTheDocument();

    // The stored cursor belongs to the file source; the next read answers from
    // memory with a gap. The window must be replaced, not concatenated.
    runtimeSource = "memory";
    fireEvent.click(screen.getByRole("button", { name: t("log.rawlog.loadOlder") }));
    expect(await screen.findByText(t("log.rawlog.sourceMemory"))).toBeInTheDocument();
    expect(screen.getByText(t("log.rawlog.historyGap"))).toBeInTheDocument();
    expect(document.querySelectorAll("[data-runtime-row]")).toHaveLength(200);
    expect(screen.getAllByText("runtime line 60", { exact: true })).toHaveLength(1);
  });

  it("shows the in-memory fallback notice", async () => {
    runtimeSource = "memory";
    renderLogDialog();
    fireEvent.click(await screen.findByRole("tab", { name: t("log.tabRaw") }));
    expect(await screen.findByText(t("log.rawlog.sourceMemory"))).toBeInTheDocument();
  });
});

describe("rawLogText", () => {
  it("builds one plain line per entry with the structured fields", () => {
    const text = rawLogText(runtimeLines as never);
    expect(text).toBe(
      [
        "2026-10-21T08:00:00Z WARN httpapi.request slow response request_id=req-1",
        "2026-10-21T08:01:00Z ERROR metadata_matcher.tmdb_search provider search failed kind=metadata_match work_item_id=wi-1 stage=search state_before=queued state_after=failed disposition=retry owner_type=subscription attempt=2 duration_ms=1234 retryable=false outcome=failure",
      ].join("\n"),
    );
  });
});
