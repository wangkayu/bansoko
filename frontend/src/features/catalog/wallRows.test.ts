import { describe, expect, it } from "vitest";
import { currentWeekday, wallRowLabel, wallRowOf, wallRows } from "./wallRows";

describe("currentWeekday（JST UTC+9 28 时制换日）", () => {
  it("日本时间凌晨 04:00（JST 28:00 / 北京时间 03:00）前仍归属前一日，04:00 起切入新一日", () => {
    // 2026-07-19 is Sunday.
    // Sunday 27:59 JST = Monday 03:59 JST = Sunday 18:59 UTC (Beijing Monday 02:59) -> still Sunday ("sun")
    expect(currentWeekday(new Date("2026-07-19T18:59:59Z"))).toBe("sun");
    // Sunday 28:00 JST = Monday 04:00 JST = Sunday 19:00 UTC (Beijing Monday 03:00) -> switches to Monday ("mon")
    expect(currentWeekday(new Date("2026-07-19T19:00:00Z"))).toBe("mon");
  });
});

describe("wallRowOf", () => {
  it("真实放送日进对应星期行", () => {
    expect(wallRowOf({ broadcastDay: "thu" })).toBe("thu");
    expect(wallRowOf({ broadcastDay: "sun", mediaKind: "tv", format: "tv" })).toBe("sun");
  });

  it("发行形式 movie 进剧场版，优先于放送日与自定义来源", () => {
    expect(wallRowOf({ format: "movie" })).toBe("movie");
    expect(wallRowOf({ format: "movie", broadcastDay: "fri" })).toBe("movie");
    expect(wallRowOf({ format: "movie", custom: true })).toBe("movie");
  });

  it("没有发行形式时，媒体类型 movie（TMDB 或钉住的 override）进剧场版", () => {
    expect(wallRowOf({ mediaKind: "movie" })).toBe("movie");
    expect(wallRowOf({ mediaKind: "movie", format: null, broadcastDay: "fri" })).toBe("movie");
  });

  it("有发行形式时以它为准：tv 不因媒体类型 movie 进剧场版", () => {
    expect(wallRowOf({ format: "tv", mediaKind: "movie", broadcastDay: "wed" })).toBe("wed");
    expect(wallRowOf({ format: "tv", mediaKind: "movie" })).toBe("other");
  });

  it("OVA / ONA 在没有放送日时进 OVA/ONA 行；有放送日时按星期", () => {
    expect(wallRowOf({ format: "ova" })).toBe("ova");
    expect(wallRowOf({ format: "ona", broadcastDay: "tbd" })).toBe("ova");
    expect(wallRowOf({ format: "ova", broadcastDay: "none", custom: true })).toBe("ova");
    expect(wallRowOf({ format: "ova", broadcastDay: "mon" })).toBe("mon");
  });

  it("发行形式缺省或为 null：不是剧场版也没有放送日就进其他", () => {
    expect(wallRowOf({})).toBe("other");
    expect(wallRowOf({ format: null })).toBe("other");
    expect(wallRowOf({ broadcastDay: "tbd" })).toBe("other");
    expect(wallRowOf({ broadcastDay: "none", mediaKind: "tv" })).toBe("other");
  });

  it("tv 不单独成行，落到其他", () => {
    expect(wallRowOf({ format: "tv" })).toBe("other");
    expect(wallRowOf({ format: "tv", broadcastDay: "tbd" })).toBe("other");
  });

  it("自定义来源：没有剧场版也没有放送日时进自定义行", () => {
    expect(wallRowOf({ custom: true })).toBe("custom");
    expect(wallRowOf({ custom: true, broadcastDay: "none", format: null })).toBe("custom");
    expect(wallRowOf({ custom: true, broadcastDay: "tbd", format: "tv" })).toBe("custom");
  });

  it("自定义来源有放送日或剧场版时和其他订阅一样归行", () => {
    expect(wallRowOf({ custom: true, broadcastDay: "wed" })).toBe("wed");
    expect(wallRowOf({ custom: true, mediaKind: "movie" })).toBe("movie");
  });
});

describe("wallRows", () => {
  it("按指定当日开始轮转星期行（后接剧场版、OVA/ONA、自定义、其他），行内保持输入顺序，跳过空行", () => {
    const items = [
      { id: "a", broadcastDay: "sun" as const },
      { id: "b", broadcastDay: "thu" as const },
      { id: "c" },
      { id: "d", broadcastDay: "thu" as const },
      { id: "e", mediaKind: "movie" as const },
      { id: "f", custom: true },
      { id: "g", format: "ova" as const },
    ];
    const rows = wallRows(items, (item) => item, "mon");
    expect(rows.map((row) => [row.key, row.items.map((item) => item.id), Boolean(row.isToday)])).toEqual([
      ["thu", ["b", "d"], false],
      ["sun", ["a"], false],
      ["movie", ["e"], false],
      ["ova", ["g"], false],
      ["custom", ["f"], false],
      ["other", ["c"], false],
    ]);

    const rotatedFromSun = wallRows(items, (item) => item, "sun");
    expect(rotatedFromSun.map((row) => [row.key, row.items.map((item) => item.id), Boolean(row.isToday)])).toEqual([
      ["sun", ["a"], true],
      ["thu", ["b", "d"], false],
      ["movie", ["e"], false],
      ["ova", ["g"], false],
      ["custom", ["f"], false],
      ["other", ["c"], false],
    ]);
  });

  it("空列表没有行", () => {
    expect(wallRows([], () => ({}))).toEqual([]);
  });
});

describe("wallRowLabel", () => {
  it("星期行用放送日文案，其余行用自己的文案", () => {
    expect(wallRowLabel("mon")).toBe("周一");
    expect(wallRowLabel("movie")).toBe("剧场版");
    expect(wallRowLabel("ova")).toBe("OVA/ONA");
    expect(wallRowLabel("custom")).toBe("自定义");
    expect(wallRowLabel("other")).toBe("其他");
  });
});
