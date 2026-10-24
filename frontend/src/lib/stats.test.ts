import { describe, expect, it } from "vitest";

import type { CatalogEntryView } from "./providers/viewmodels";
import { applyStatsFilter, statsCounts, statsSegment, type StatsFilter } from "./stats";

// Three mutually exclusive segments, counted by priority: a hold wins (active+held lands on waiting-for-metadata and no longer counts as active).

function entry(overrides: Partial<CatalogEntryView>): CatalogEntryView {
  return {
    providerEntryRef: "mikan:e1",
    source: "mikan",
    title: "测试番",
    metadataMatch: { matched: false },
    ...overrides,
  };
}

describe("statsSegment 互斥口径", () => {
  it("active 且无 hold -> active", () => {
    expect(statsSegment(entry({ subscriptionState: "active", metadataHold: { held: false, missing: [] } }))).toBe("active");
    expect(statsSegment(entry({ subscriptionState: "active" }))).toBe("active");
  });

  it("active 且 hold.held 仍是 active（阻塞不是订阅状态）", () => {
    expect(statsSegment(entry({ subscriptionState: "active", metadataHold: { held: true, missing: ["bgm"] } }))).toBe("active");
  });

  it("stopped -> stopped（即使有 hold 事实也归 stopped）", () => {
    expect(statsSegment(entry({ subscriptionState: "stopped" }))).toBe("stopped");
    expect(statsSegment(entry({ subscriptionState: "stopped", metadataHold: { held: true, missing: ["bgm"] } }))).toBe("stopped");
  });

  it("pending -> pending（即使有 hold 事实也归 pending）", () => {
    expect(statsSegment(entry({ subscriptionState: "pending" }))).toBe("pending");
    expect(statsSegment(entry({ subscriptionState: "pending", metadataHold: { held: true, missing: ["tmdb"] } }))).toBe("pending");
  });

  it("未订阅 -> 不属于任何段", () => {
    expect(statsSegment(entry({}))).toBeUndefined();
  });
});

describe("statsCounts", () => {
  it("只数订阅状态：缺关联的订阅中计入 active；未订阅不计数", () => {
    const entries = [
      entry({ providerEntryRef: "a", subscriptionState: "active" }),
      entry({ providerEntryRef: "b", subscriptionState: "active", metadataHold: { held: true, missing: ["tmdb"] } }),
      entry({ providerEntryRef: "c", subscriptionState: "active", metadataHold: { held: false, missing: [] } }),
      entry({ providerEntryRef: "d", subscriptionState: "stopped" }),
      entry({ providerEntryRef: "f", subscriptionState: "pending", metadataHold: { held: true, missing: ["bgm"] } }),
      entry({ providerEntryRef: "e" }), // an unsubscribed custom preset: not counted
    ];
    expect(statsCounts(entries)).toEqual({ pending: 1, active: 3, stopped: 1 });
  });

  it("空列表全 0", () => {
    expect(statsCounts([])).toEqual({ pending: 0, active: 0, stopped: 0 });
  });
});

describe("applyStatsFilter", () => {
  const rows = [
    { entry: entry({ providerEntryRef: "a", subscriptionState: "active" }) },
    { entry: entry({ providerEntryRef: "b", subscriptionState: "active", metadataHold: { held: true, missing: ["bgm"] } }) },
    { entry: entry({ providerEntryRef: "c", subscriptionState: "stopped" }) },
    { entry: entry({ providerEntryRef: "d" }) },
    { entry: entry({ providerEntryRef: "e", subscriptionState: "pending" }) },
  ];
  const refs = (list: typeof rows) => list.map((row) => row.entry.providerEntryRef);

  it("null 不过滤（含未订阅条目）", () => {
    expect(refs(applyStatsFilter(rows, null))).toEqual(["a", "b", "c", "d", "e"]);
  });

  it("active 留下全部 active（含缺关联的）", () => {
    expect(refs(applyStatsFilter(rows, "active"))).toEqual(["a", "b"]);
  });

  it("stopped 只留 stopped", () => {
    expect(refs(applyStatsFilter(rows, "stopped"))).toEqual(["c"]);
  });

  it("pending 只留 pending", () => {
    expect(refs(applyStatsFilter(rows, "pending"))).toEqual(["e"]);
  });

  it.each(["pending", "active", "stopped"] as StatsFilter[])("%s 过滤结果都是原 rows 的子集且不重复计数", (filter) => {
    const filtered = applyStatsFilter(rows, filter);
    const counts = statsCounts(filtered.map((row) => row.entry));
    expect(filtered.length).toBe(counts[filter]);
  });
});
