import type { DownloadTimeouts } from "./api/client";
import { t } from "./i18n";

// Download timeouts (the global download_timeouts setting): values are stored in seconds and
// edited as minutes / hours / days in the UI. The ranges match the server
// (internal/anime/domain/download_timeouts.go).

export type TimeoutUnit = "minutes" | "hours" | "days";

const UNIT_SECONDS: Record<TimeoutUnit, number> = { minutes: 60, hours: 3600, days: 86400 };

export const DOWNLOAD_TIMEOUT_BOUNDS = {
  stall: { min: 600, max: 7 * 86400 },
  wait: { min: 3600, max: 30 * 86400 },
} as const;

export const DEFAULT_DOWNLOAD_TIMEOUTS: DownloadTimeouts = { stall_seconds: 6 * 3600, download_wait_seconds: 7 * 86400 };

export type DurationField = { amount: string; unit: TimeoutUnit };

export type DownloadTimeoutsForm = { stall: DurationField; wait: DurationField };

export function timeoutUnitOptions(): { value: TimeoutUnit; label: string }[] {
  return (["minutes", "hours", "days"] as const).map((unit) => ({ value: unit, label: t(`downloadTimeouts.unit.${unit}`) }));
}

/** Reads the server value; a missing field or a non-positive-integer falls back to the default. */
export function downloadTimeoutsValue(value: Record<string, unknown> | undefined): DownloadTimeouts {
  const read = (key: keyof DownloadTimeouts) => {
    const raw = value?.[key];
    return typeof raw === "number" && Number.isInteger(raw) && raw > 0 ? raw : DEFAULT_DOWNLOAD_TIMEOUTS[key];
  };
  return { stall_seconds: read("stall_seconds"), download_wait_seconds: read("download_wait_seconds") };
}

/** Seconds → the largest unit that divides evenly. */
export function durationField(seconds: number): DurationField {
  for (const unit of ["days", "hours", "minutes"] as const) {
    if (seconds % UNIT_SECONDS[unit] === 0) return { amount: String(seconds / UNIT_SECONDS[unit]), unit };
  }
  return { amount: String(Math.round(seconds / 60)), unit: "minutes" };
}

export function formatDuration(seconds: number): string {
  const field = durationField(seconds);
  return t(`downloadTimeouts.amount.${field.unit}`, { n: field.amount });
}

export function downloadTimeoutsForm(value: DownloadTimeouts): DownloadTimeoutsForm {
  return { stall: durationField(value.stall_seconds), wait: durationField(value.download_wait_seconds) };
}

function fieldSeconds(field: DurationField): number | undefined {
  const text = field.amount.trim();
  if (!/^[0-9]+$/.test(text)) return undefined;
  const amount = Number(text);
  return amount > 0 ? amount * UNIT_SECONDS[field.unit] : undefined;
}

export type DownloadTimeoutsResult = { value: DownloadTimeouts } | { error: string };

/** Form → stored value; the ranges and "no-progress timeout within the max download time" are validated here first. */
export function downloadTimeoutsFromForm(form: DownloadTimeoutsForm): DownloadTimeoutsResult {
  const stall = fieldSeconds(form.stall);
  const wait = fieldSeconds(form.wait);
  if (stall === undefined || wait === undefined) return { error: t("downloadTimeouts.invalidNumber") };
  if (stall < DOWNLOAD_TIMEOUT_BOUNDS.stall.min || stall > DOWNLOAD_TIMEOUT_BOUNDS.stall.max) return { error: t("downloadTimeouts.stallRange") };
  if (wait < DOWNLOAD_TIMEOUT_BOUNDS.wait.min || wait > DOWNLOAD_TIMEOUT_BOUNDS.wait.max) return { error: t("downloadTimeouts.waitRange") };
  if (stall > wait) return { error: t("downloadTimeouts.stallOverWait") };
  return { value: { stall_seconds: stall, download_wait_seconds: wait } };
}
