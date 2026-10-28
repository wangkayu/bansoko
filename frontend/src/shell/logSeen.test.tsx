import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DiagnosticEpisode, ObservabilityRecord } from "../lib/api/client";
import { badgeText, markLogSeen, newBadgeRecords, newOpenEpisodes, useLogFailureBadge } from "./logSeen";
import { deliveryState, entryState, isBadgeRecord } from "./logState";

// Badge rule (a): failed acquisition results, failed or unconfirmed notifications, and open
// conditions whose first failure is newer than the last time the tab was seen.

function settled(id: string, outcome: "success" | "failure", occurredAt: string): ObservabilityRecord {
  return {
    record_type: "business",
    entry: {
      id,
      subscription_id: "sub-1",
      managed_anime_id: "anime-1",
      anime_title: "Example",
      source: "subscription",
      kind: "acquisition_settled",
      outcome,
      occurred_at: occurredAt,
      message_code: outcome === "success" ? "acquisition_succeeded" : "acquisition_failed",
      message_params: {},
      dedupe_key: id,
      retention_until: "2026-12-01T00:00:00Z",
    },
  };
}

function delivery(id: string, state: string, updatedAt: string): ObservabilityRecord {
  return { record_type: "plugin", delivery: { id, state, created_at: updatedAt, updated_at: updatedAt } } as unknown as ObservabilityRecord;
}

function episode(id: string, first: string, last = first, recovered?: string): DiagnosticEpisode {
  return {
    id,
    kind: "downloader",
    variant: "unreachable",
    target: { kind: "downloader", id: "d1", name: "qB" },
    recent: [],
    first_failed_at: first,
    last_failed_at: last,
    succeeded_after_at: recovered ?? null,
    occurrence_count: 1,
    retry: { mode: "resume" },
    severity: "error",
    retention_until: "2026-12-01T00:00:00Z",
  } as DiagnosticEpisode;
}

describe("badge predicates", () => {
  it("counts only failed acquisition results and failed or unconfirmed notifications", () => {
    expect(isBadgeRecord(settled("a", "failure", "2026-10-20T08:00:00Z"))).toBe(true);
    expect(isBadgeRecord(settled("b", "success", "2026-10-20T08:00:00Z"))).toBe(false);
    expect(isBadgeRecord(delivery("c", "failed", "2026-10-20T08:00:00Z"))).toBe(true);
    expect(isBadgeRecord(delivery("d", "unknown", "2026-10-20T08:00:00Z"))).toBe(true);
    expect(isBadgeRecord(delivery("e", "delivered", "2026-10-20T08:00:00Z"))).toBe(false);
    expect(isBadgeRecord(delivery("f", "waiting_retry", "2026-10-20T08:00:00Z"))).toBe(false);
  });

  it("every badge record is a record the failure filter lists", () => {
    const all = [
      settled("a", "failure", "2026-10-20T08:00:00Z"),
      settled("b", "success", "2026-10-20T08:00:00Z"),
      delivery("c", "unknown", "2026-10-20T08:00:00Z"),
      delivery("d", "delivered", "2026-10-20T08:00:00Z"),
    ];
    for (const record of all.filter(isBadgeRecord)) {
      const state = record.record_type === "business" ? entryState(record.entry) : deliveryState((record as Extract<ObservabilityRecord, { record_type: "plugin" }>).delivery);
      expect(state).toBe("failure");
    }
  });

  it("only records newer than the seen mark count", () => {
    const records = [settled("old", "failure", "2026-10-20T08:00:00Z"), settled("new", "failure", "2026-10-20T10:00:00Z")];
    expect(newBadgeRecords(records, null)).toHaveLength(2);
    expect(newBadgeRecords(records, "2026-10-20T09:00:00Z")).toHaveLength(1);
  });

  it("repeats of a known condition do not count, a new condition adds one, recovery changes nothing", () => {
    const mark = "2026-10-20T09:00:00Z";
    const known = episode("k", "2026-10-20T08:00:00Z", "2026-10-20T12:00:00Z");
    expect(newOpenEpisodes([known], mark)).toHaveLength(0);
    expect(newOpenEpisodes([known, episode("n", "2026-10-20T11:00:00Z")], mark)).toHaveLength(1);
    const recovered = episode("r", "2026-10-20T10:00:00Z", "2026-10-20T10:00:00Z", "2026-10-20T10:30:00Z");
    expect(newOpenEpisodes([known, recovered], mark)).toHaveLength(0);
  });
});

describe("useLogFailureBadge", () => {
  let records: ObservabilityRecord[];
  let episodes: DiagnosticEpisode[];

  beforeEach(() => {
    localStorage.clear();
    records = [settled("f1", "failure", "2026-10-20T08:00:00Z")];
    episodes = [episode("d1", "2026-10-20T08:30:00Z"), episode("d2", "2026-10-20T06:00:00Z", "2026-10-20T06:00:00Z", "2026-10-20T06:30:00Z")];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = new URL(String(input), "http://localhost");
        const body = url.pathname.endsWith("/observability/records")
          ? { records, next_cursor: null }
          : { episodes, internal: { occurrence_count: 4, last_failed_at: "2026-10-20T09:00:00Z" }, next_cursor: null };
        return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
      }),
    );
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  function setup() {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
    return renderHook(() => useLogFailureBadge(), { wrapper });
  }

  it("counts failures and open conditions, never recovered ones or the internal row", async () => {
    const { result } = setup();
    await waitFor(() => expect(result.current).toBe(2));
  });

  it("clears once both tabs were seen", async () => {
    markLogSeen("records", "2026-10-20T08:00:00Z");
    markLogSeen("diagnostics", "2026-10-20T08:30:00Z");
    const { result } = setup();
    await waitFor(() => expect(result.current).toBe(0));
  });
});

describe("badgeText", () => {
  it("caps at 99+", () => {
    expect(badgeText(7)).toBe("7");
    expect(badgeText(120)).toBe("99+");
  });
});
