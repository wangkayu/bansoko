import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { listAutomationSchedules, type AutomationSchedule } from "./api/client";
import { automationKeys } from "./queryKeys";

type ScheduledRun = AutomationSchedule & { next_run_at: string };

// A shown next_run_at goes stale at that very moment: the dispatcher picks the
// schedule up on its next tick and only moves next_run_at forward when the run
// finishes, and no realtime event reports it. So the schedules query re-reads
// one step after the earliest enabled next_run_at, then every step while that
// time is still in the past (the run is still going).
const SCHEDULE_SETTLE_MS = 30_000;
// Long intervals (days) would overflow a browser timer; re-read hourly instead.
const SCHEDULE_MAX_WAIT_MS = 60 * 60_000;

/** The enabled schedule that runs next, or undefined when none is scheduled. */
export function earliestScheduledRun(schedules: readonly AutomationSchedule[] | undefined): ScheduledRun | undefined {
  return (schedules ?? [])
    .filter((s): s is ScheduledRun => s.enabled && Boolean(s.next_run_at))
    .sort((a, b) => a.next_run_at.localeCompare(b.next_run_at))[0];
}

/** Milliseconds until the schedules should be re-read, or false when nothing is scheduled. */
export function scheduleRefetchDelay(schedules: readonly AutomationSchedule[] | undefined, now = Date.now()): number | false {
  const next = earliestScheduledRun(schedules);
  if (!next) return false;
  const untilDue = Math.max(Date.parse(next.next_run_at) - now, 0);
  return Math.min(untilDue + SCHEDULE_SETTLE_MS, SCHEDULE_MAX_WAIT_MS);
}

/** The automation schedules, re-read once each shown next_run_at has passed. */
export function useAutomationSchedules() {
  return useQuery({
    queryKey: automationKeys.schedules,
    queryFn: listAutomationSchedules,
    refetchInterval: (query) => scheduleRefetchDelay(query.state.data),
  });
}

/**
 * The next automation run for a status surface. Opening the surface re-reads
 * the schedules, so a tab left in the background (where timers pause) never
 * shows a time that has already passed.
 */
export function useNextAutomationRun(open: boolean): ScheduledRun | undefined {
  const schedules = useAutomationSchedules();
  const { refetch } = schedules;
  useEffect(() => {
    if (open) void refetch();
  }, [open, refetch]);
  return earliestScheduledRun(schedules.data);
}
