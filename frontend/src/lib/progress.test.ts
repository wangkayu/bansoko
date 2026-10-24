import { describe, expect, it } from "vitest";

import { t } from "./i18n";
import { importedCount, progressDone, progressRatio, progressSummary } from "./progress";

// Progress counts by number only: imported episodes are compared against the total, never aligned by episode number and never inferring gaps.

describe("importedCount", () => {
  it("按去重后的集数计", () => {
    expect(importedCount(undefined)).toBe(0);
    expect(importedCount({ episodes: [] })).toBe(0);
    expect(importedCount({ episodes: [1, 2, 2, 7] })).toBe(3);
  });
});

describe("progressSummary", () => {
  it("没有已入库集 -> 等待首集", () => {
    expect(progressSummary(undefined)).toBe(t("grid.progress.waiting"));
    expect(progressSummary({ episodes: [], total: 12 }, true)).toBe(t("grid.progress.waiting"));
  });

  it("有总集数 -> 已入库 n/total；短摘要 n/total", () => {
    expect(progressSummary({ episodes: [1, 2, 5], total: 12 })).toBe(t("grid.progress.summary", { stored: 3, total: 12 }));
    expect(progressSummary({ episodes: [1, 2, 5], total: 12 }, true)).toBe(t("grid.progress.summaryShort", { stored: 3, total: 12 }));
  });

  it("无总集数（含 0）-> 已入库 n 集", () => {
    expect(progressSummary({ episodes: [1, 2, 3, 4] })).toBe(t("grid.progress.summaryNoTotal", { stored: 4 }));
    expect(progressSummary({ episodes: [9], total: 0 }, true)).toBe(t("grid.progress.summaryNoTotal", { stored: 1 }));
  });
});

describe("progressDone / progressRatio 按数量", () => {
  it("集号再大也不会单独凑满：只看数量", () => {
    expect(progressDone({ episodes: [12], total: 12 })).toBe(false);
    expect(progressDone({ episodes: [1, 2, 5], total: 3 })).toBe(true);
    expect(progressDone({ episodes: [1, 2, 3] })).toBe(false);
  });

  it("优先读服务端 done", () => {
    expect(progressDone({ episodes: [1], total: 12, done: true })).toBe(true);
  });

  it("填充比例 = 数量 / 总集数，封顶 1；总集数未知时无比例", () => {
    expect(progressRatio({ episodes: [1, 2, 3], total: 12 })).toBe(0.25);
    expect(progressRatio({ episodes: [1, 2, 3, 4], total: 3 })).toBe(1);
    expect(progressRatio({ episodes: [1, 2, 3] })).toBeUndefined();
  });
});
