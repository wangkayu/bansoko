import { describe, expect, it } from "vitest";
import type { AnimeListItem } from "../../lib/api/client";
import {
  emptyManageFilters,
  filterManageRows,
  manageCompleted,
  manageOriginOptions,
  manageRowsFrom,
  manageSeasonOptions,
  orderManageRows,
  pausable,
  startable,
} from "./subscribedManage";

// 已订阅管理表行模型：入口 × 季度两个独立维度的组合筛选、同作品相邻分组、
// 已完成（有总集数且已入库 > 1）与批量资格。真实作品名 + 构造状态（不取自任何 RSS 采集结果）。

function item(overrides: {
  id: string;
  title: string;
  origin: AnimeListItem["subscription"]["origin"];
  originEntryRef?: string | null;
  entrySeason?: string | null;
  instanceLabel?: string | null;
  state?: AnimeListItem["subscription"]["state"];
  lifecycle?: "active" | "retired";
  showSeason?: number | null;
  rssHealth?: "ready" | "degraded" | "failed" | null;
  episodes?: number[];
  total?: number | null;
  createdAt: string;
}): AnimeListItem {
  return {
    managed_anime: { id: overrides.id, title: overrides.title, version: 1 },
    subscription: {
      id: `sub:${overrides.id}`,
      managed_anime_id: overrides.id,
      created_at: overrides.createdAt,
      origin: overrides.origin,
      origin_entry_ref: overrides.originEntryRef ?? null,
      entry_season: overrides.entrySeason ?? null,
      instance_label: overrides.instanceLabel ?? null,
      state: overrides.state ?? "active",
      lifecycle_state: overrides.lifecycle ?? "active",
      lifecycle_epoch: 1,
      version: 7,
      new_releases_only: false,
    },
    metadata: {
      managed_anime_id: overrides.id,
      bgm: null,
      tmdb: null,
      title_override: null,
      total_episodes: null,
      media_kind: null,
      year: null,
      season: overrides.showSeason ?? null,
      version: 1,
    },
    metadata_hold: { held: false, missing: [] },
    broadcast_day: "none",
    rss_health: overrides.rssHealth ?? null,
    // done 与服务端同规则派生（读时按数量）。
    episode_progress:
      overrides.episodes === undefined
        ? null
        : {
            episodes: overrides.episodes,
            ...(overrides.total != null ? { total: overrides.total } : {}),
            done:
              (overrides.total ?? 0) > 0 &&
              overrides.episodes.length > 0 &&
              overrides.episodes.length >= (overrides.total ?? 0),
          },
  };
}

const fixtures = [
  item({ id: "a1", title: "药屋少女的呢喃 第三季", origin: "anibt", originEntryRef: "anibt:kh", entrySeason: "2026-fall", showSeason: 3, episodes: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11], total: 24, createdAt: "2026-10-05T00:00:00Z" }),
  item({ id: "a1b", title: "药屋少女的呢喃 第三季", origin: "anibt", originEntryRef: "anibt:kh", entrySeason: "2026-fall", showSeason: 3, instanceLabel: "B 盘", state: "pending", episodes: [], total: 24, createdAt: "2026-10-20T00:00:00Z" }),
  item({ id: "m1", title: "致不灭的你 第三季", origin: "mikan", originEntryRef: "mikan:fn", entrySeason: "2026-summer", showSeason: 3, state: "stopped", rssHealth: "degraded", episodes: [1, 2, 3, 4, 5, 6], total: 13, createdAt: "2026-09-12T00:00:00Z" }),
  // 搜索创建：provider 入口但没有季度。
  item({ id: "a2", title: "转生哥布林有问题吗？", origin: "anibt", originEntryRef: "anibt:gb", episodes: [1, 2, 3, 4, 5], createdAt: "2026-10-11T00:00:00Z" }),
  item({ id: "c1", title: "落语朱音", origin: "custom", instanceLabel: "落语朱音", showSeason: null, createdAt: "2026-10-26T00:00:00Z" }),
  // 剧场版：1/1 完成但不进「已完成」筛选。
  item({ id: "m2", title: "灌篮高手（剧场版）", origin: "mikan", originEntryRef: "mikan:sl", entrySeason: "2022-winter", episodes: [1], total: 1, createdAt: "2026-09-02T00:00:00Z" }),
];

describe("manageRowsFrom（列表项 → 管理行）", () => {
  it("入口、季度、节目季、标签与进度按原样映射", () => {
    const rows = manageRowsFrom(fixtures);
    expect(rows.map((row) => row.key)).toEqual(["a1", "a1b", "m1", "a2", "c1", "m2"]);
    const first = rows[0];
    expect(first.origin).toBe("anibt");
    expect(first.entrySeason).toBe("2026-fall");
    expect(first.showSeason).toBe(3);
    expect(first.version).toBe(7);
    expect(rows[1].instanceLabel).toBe("B 盘");
    expect(rows[3].entrySeason).toBeUndefined();
    expect(rows[4].origin).toBe("custom");
    expect(rows[2].rssHealth).toBe("degraded");
  });
});

describe("已完成与进度筛选", () => {
  it("已完成 = 有总集数且已入库 > 1；1/1 的剧场版不算", () => {
    const rows = manageRowsFrom(fixtures);
    expect(manageCompleted(rows[5])).toBe(false); // 灌篮高手 1/1
    const full = manageRowsFrom([
      item({ id: "f1", title: "佐佐木与文鸟小哔 第二季", origin: "mikan", originEntryRef: "mikan:szk", episodes: Array.from({ length: 12 }, (_, i) => i + 1), total: 12, createdAt: "2026-09-30T00:00:00Z" }),
    ]);
    expect(manageCompleted(full[0])).toBe(true);
  });

  it("进度筛选：进行中 / 已完成 / 未入库", () => {
    const rows = manageRowsFrom(fixtures);
    const byProgress = (progress: "doing" | "done" | "none") =>
      filterManageRows(rows, { ...emptyManageFilters, progress }).map((row) => row.key);
    expect(byProgress("none")).toEqual(["a1b", "c1"]);
    expect(byProgress("doing")).toEqual(["a1", "m1", "a2"]);
    // 无一行满足已完成（11/24、6/13、5/—、1/1 都不算）。
    expect(byProgress("done")).toEqual([]);
  });
});

describe("入口 × 季度组合筛选", () => {
  it("入口与季度独立取交集：mikan+26春 与 anibt+26春 是不同批次", () => {
    const rows = manageRowsFrom([
      ...fixtures,
      item({ id: "m3", title: "魔法★探险家", origin: "mikan", originEntryRef: "mikan:mk", entrySeason: "2026-fall", state: "stopped", episodes: [1], total: 12, createdAt: "2026-10-04T00:00:00Z" }),
    ]);
    const pick = (origin: string, season: string) =>
      filterManageRows(rows, { ...emptyManageFilters, origin: origin as never, season }).map((row) => row.key);
    expect(pick("mikan", "2026-fall")).toEqual(["m3"]);
    expect(pick("anibt", "2026-fall")).toEqual(["a1", "a1b"]);
    // 无季度：搜索创建与自定义。
    expect(pick("all", "none")).toEqual(["a2", "c1"]);
    expect(manageOriginOptions(rows)).toEqual(["anibt", "mikan", "custom"]);
  });

  it("季度选项新→旧排序并标记无季度", () => {
    const rows = manageRowsFrom(fixtures);
    const options = manageSeasonOptions(rows);
    expect(options.seasons).toEqual(["2026-fall", "2026-summer", "2022-winter"]);
    expect(options.hasNone).toBe(true);
  });

  it("搜索匹配标题、实例标签与入口", () => {
    const rows = manageRowsFrom(fixtures);
    const hit = (query: string) => filterManageRows(rows, { ...emptyManageFilters, query }).map((row) => row.key);
    expect(hit("B 盘")).toEqual(["a1b"]);
    expect(hit("mikan")).toEqual(["m1", "m2"]);
    expect(hit("不存在")).toEqual([]);
  });
});

describe("同作品相邻分组与排序", () => {
  it("同一作品的实例相邻（原件在前），组间按创建时间倒序", () => {
    const rows = manageRowsFrom(fixtures);
    const ordered = orderManageRows(rows, true);
    expect(ordered.map(({ row }) => row.key)).toEqual(["c1", "a1", "a1b", "a2", "m1", "m2"]);
    expect(ordered.map(({ grouped }) => grouped)).toEqual([false, true, true, false, false, false]);
    const ascending = orderManageRows(rows, false);
    expect(ascending.map(({ row }) => row.key)).toEqual(["m2", "m1", "a1", "a1b", "a2", "c1"]);
  });
});

describe("批量资格", () => {
  it("未开始可开始不可暂停；订阅中可暂停；已结束实例不可操作", () => {
    const rows = manageRowsFrom([
      item({ id: "p1", title: "装备我最强", origin: "anibt", originEntryRef: "anibt:zq", state: "pending", createdAt: "2026-10-28T00:00:00Z" }),
      item({ id: "s1", title: "弱弱老师", origin: "anibt", originEntryRef: "anibt:rr", state: "stopped", createdAt: "2026-10-27T00:00:00Z" }),
      item({ id: "r1", title: "迦楠大人的白给是恶魔级", origin: "anibt", originEntryRef: "anibt:kn", state: "active", lifecycle: "retired", createdAt: "2026-09-08T00:00:00Z" }),
    ]);
    const [pending, stopped, retired] = rows;
    expect(startable(pending)).toBe(true);
    expect(pausable(pending)).toBe(false);
    expect(startable(stopped)).toBe(true);
    expect(pausable(stopped)).toBe(false);
    expect(startable(retired)).toBe(false);
    expect(pausable(retired)).toBe(false);
  });
});
