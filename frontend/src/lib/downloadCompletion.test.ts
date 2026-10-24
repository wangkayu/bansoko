import { describe, expect, it } from "vitest";
import {
  downloadCompletionForm,
  downloadCompletionNote,
  downloadCompletionRules,
  downloadCompletionValue,
  seedLimitsSummary,
} from "./downloadCompletion";
import { t } from "./i18n";

describe("downloadCompletion", () => {
  it("读取存储值：未知处理方式按继续做种，限制只跟随继续做种", () => {
    expect(downloadCompletionRules(undefined)).toEqual({ action: "seed" });
    expect(downloadCompletionRules({ action: "wipe" })).toEqual({ action: "seed" });
    expect(downloadCompletionRules({ action: "pause", seed_limits: { ratio: 2, at_limit: "stop" } })).toEqual({ action: "pause" });
    expect(downloadCompletionRules({ action: "seed", seed_limits: { ratio: 2, at_limit: "pause" } })).toEqual({ action: "seed" });
    expect(downloadCompletionRules({ action: "seed", seed_limits: { seeding_time_minutes: 90, ratio: 2, at_limit: "remove" } }))
      .toEqual({ action: "seed", seed_limits: { seeding_time_minutes: 90, ratio: 2, at_limit: "remove" } });
  });

  it("表单与存储值互相换算：整小时显示小时，否则显示分钟", () => {
    const hours = downloadCompletionForm({ action: "seed", seed_limits: { seeding_time_minutes: 2880, at_limit: "stop" } });
    expect(hours).toMatchObject({ limited: true, time: "48", unit: "hours", ratio: "" });
    expect(downloadCompletionValue(hours)).toEqual({ value: { action: "seed", seed_limits: { seeding_time_minutes: 2880, at_limit: "stop" } } });

    const minutes = downloadCompletionForm({ action: "seed", seed_limits: { seeding_time_minutes: 90, ratio: 1.5, at_limit: "remove_with_data" } });
    expect(minutes).toMatchObject({ time: "90", unit: "minutes", ratio: "1.5", atLimit: "remove_with_data" });
    expect(downloadCompletionValue(minutes)).toEqual({ value: { action: "seed", seed_limits: { seeding_time_minutes: 90, ratio: 1.5, at_limit: "remove_with_data" } } });
  });

  it("不设上限或不是继续做种时只保存处理方式", () => {
    const form = downloadCompletionForm({ action: "seed" });
    expect(downloadCompletionValue({ ...form, time: "5" })).toEqual({ value: { action: "seed" } });
    expect(downloadCompletionValue({ ...form, action: "pause", limited: true, time: "5" })).toEqual({ value: { action: "pause" } });
  });

  it("上限校验：至少一项，时间为整分钟且不超过一年，分享率在范围内", () => {
    const form = { ...downloadCompletionForm({ action: "seed" }), limited: true };
    expect(downloadCompletionValue(form)).toEqual({ error: t("downloadCompletion.limitRequired") });
    expect(downloadCompletionValue({ ...form, time: "0" })).toEqual({ error: t("downloadCompletion.timeInvalid") });
    expect(downloadCompletionValue({ ...form, time: "1.5", unit: "minutes" })).toEqual({ error: t("downloadCompletion.timeInvalid") });
    expect(downloadCompletionValue({ ...form, time: "8761" })).toEqual({ error: t("downloadCompletion.timeInvalid") });
    expect(downloadCompletionValue({ ...form, time: "1.5" })).toEqual({ value: { action: "seed", seed_limits: { seeding_time_minutes: 90, at_limit: "stop" } } });
    expect(downloadCompletionValue({ ...form, ratio: "0" })).toEqual({ error: t("downloadCompletion.ratioInvalid") });
    expect(downloadCompletionValue({ ...form, ratio: "abc" })).toEqual({ error: t("downloadCompletion.ratioInvalid") });
  });

  it("摘要和说明", () => {
    expect(seedLimitsSummary(undefined)).toBe(t("downloadCompletion.noLimit"));
    expect(seedLimitsSummary({ seeding_time_minutes: 120, ratio: 2, at_limit: "stop" }))
      .toBe(`${t("downloadCompletion.timeHours", { n: 2 })} · ${t("downloadCompletion.ratioValue", { n: 2 })}`);
    expect(seedLimitsSummary({ seeding_time_minutes: 45, at_limit: "stop" })).toBe(t("downloadCompletion.timeMinutes", { n: 45 }));
    expect(downloadCompletionNote("remove_with_data")).toBe(t("downloadCompletion.removeDataWarning"));
    expect(downloadCompletionNote("seed", "move")).toBe(t("downloadCompletion.moveSeedNote"));
    expect(downloadCompletionNote("seed", "hardlink")).toBeUndefined();
  });
});
