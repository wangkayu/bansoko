import { describe, expect, it } from "vitest";
import { t } from "./i18n";
import { downloadTimeoutsForm, downloadTimeoutsFromForm, downloadTimeoutsValue, durationField, formatDuration } from "./downloadTimeouts";

describe("downloadTimeouts", () => {
  it("picks the largest whole unit", () => {
    expect(durationField(604800)).toEqual({ amount: "7", unit: "days" });
    expect(durationField(21600)).toEqual({ amount: "6", unit: "hours" });
    expect(durationField(5400)).toEqual({ amount: "90", unit: "minutes" });
    expect(formatDuration(600)).toBe(t("downloadTimeouts.amount.minutes", { n: "10" }));
  });

  it("falls back to the defaults for a missing or malformed value", () => {
    expect(downloadTimeoutsValue(undefined)).toEqual({ stall_seconds: 21600, download_wait_seconds: 604800 });
    expect(downloadTimeoutsValue({ stall_seconds: "1", download_wait_seconds: 86400 })).toEqual({ stall_seconds: 21600, download_wait_seconds: 86400 });
  });

  it("round-trips through the form and enforces the bounds", () => {
    const form = downloadTimeoutsForm({ stall_seconds: 600, download_wait_seconds: 2592000 });
    expect(downloadTimeoutsFromForm(form)).toEqual({ value: { stall_seconds: 600, download_wait_seconds: 2592000 } });
    expect(downloadTimeoutsFromForm({ stall: { amount: "9", unit: "minutes" }, wait: { amount: "1", unit: "days" } })).toEqual({ error: t("downloadTimeouts.stallRange") });
    expect(downloadTimeoutsFromForm({ stall: { amount: "10", unit: "minutes" }, wait: { amount: "59", unit: "minutes" } })).toEqual({ error: t("downloadTimeouts.waitRange") });
    expect(downloadTimeoutsFromForm({ stall: { amount: "2", unit: "hours" }, wait: { amount: "1", unit: "hours" } })).toEqual({ error: t("downloadTimeouts.stallOverWait") });
    expect(downloadTimeoutsFromForm({ stall: { amount: "", unit: "hours" }, wait: { amount: "1", unit: "days" } })).toEqual({ error: t("downloadTimeouts.invalidNumber") });
  });
});
