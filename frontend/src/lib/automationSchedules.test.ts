import { describe, expect, it } from "vitest";
import type { AutomationSchedule } from "./api/client";
import { earliestScheduledRun, scheduleRefetchDelay } from "./automationSchedules";

const now = Date.parse("2026-10-29T09:00:00Z");

function schedule(kind: AutomationSchedule["kind"], nextRunAt: string | null, enabled = true): AutomationSchedule {
  return { kind, enabled, next_run_at: nextRunAt } as AutomationSchedule;
}

describe("automation schedule refresh", () => {
  it("picks the earliest enabled run", () => {
    const schedules = [
      schedule("rss_refresh", "2026-10-29T09:20:00Z"),
      schedule("group_discovery", "2026-10-29T09:05:00Z", false),
      schedule("metadata_match", "2026-10-29T09:10:00Z"),
    ];
    expect(earliestScheduledRun(schedules)?.kind).toBe("metadata_match");
  });

  it("re-reads one step after the shown time passes", () => {
    expect(scheduleRefetchDelay([schedule("rss_refresh", "2026-10-29T09:10:00Z")], now)).toBe(10 * 60_000 + 30_000);
  });

  it("keeps re-reading while the shown time is already in the past", () => {
    expect(scheduleRefetchDelay([schedule("rss_refresh", "2026-10-29T07:40:00Z")], now)).toBe(30_000);
  });

  it("caps long waits and stays idle without a scheduled run", () => {
    expect(scheduleRefetchDelay([schedule("rss_refresh", "2026-11-28T09:00:00Z")], now)).toBe(60 * 60_000);
    expect(scheduleRefetchDelay([schedule("rss_refresh", null)], now)).toBe(false);
    expect(scheduleRefetchDelay(undefined, now)).toBe(false);
  });
});
