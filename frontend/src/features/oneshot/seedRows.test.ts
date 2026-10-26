import { describe, expect, it } from "vitest";
import type { RSSListGroup, RSSListItem, RSSListView } from "../../lib/api/client";
import {
  factField,
  filterSeedGroups,
  groupKeyOf,
  groupNeedsRefresh,
  latestPublishedAt,
  seedGroupsOf,
  seedKeyOf,
  seedRowCount,
  selectableRows,
  shortTarget,
  toggleKey,
} from "./seedRows";

// seedRows is a pure projection: these cases lock in "carry over the server's judgments, never re-judge".

function item(overrides: Partial<RSSListItem> = {}): RSSListItem {
  return {
    snapshot_id: "snap-1",
    stable_seed_key: "seed-a",
    seed: { title: "[组A] 番剧 - 01 [1080p]", locator_display: "https://example.invalid/a", size_bytes: 1024 },
    torrent: { state: "available" },
    evaluation: { state: "evaluated", verdict: "accepted_pending_download" },
    files: [],
    download_action: { allowed: true, kind: "one_shot" },
    // Empty files means this structure holds no comparable target at all: the server's verdict is
//     undetermined, and the fixture matches.
    library_overlap: { verdict: "undetermined", present_files: null, pending_files: null, reason_code: "no_rendered_targets" },
    ...overrides,
  };
}

function group(overrides: Partial<RSSListGroup> = {}): RSSListGroup {
  return {
    scope: { provider: "anibt", provider_entry_ref: "entry-1", provider_group_ref: "g-1" },
    display_name: "组A",
    selection: { source: "configured", effective_selected: true, selection_mode: "auto" },
    offset: { value: 0, editable: true, source: "link" },
    feed: { state: "fresh", snapshot_id: "snap-1", snapshot_version: 3, content_hash: "hash-1" },
    items: [item()],
    ...overrides,
  };
}

function view(groups: RSSListGroup[]): RSSListView {
  return {
    mode: "current_input",
    config_identity: "identity",
    metadata_hold: { held: false, missing: [] },
    versions: { parser: "p", evaluator: "e", renderer: "r", safety: "s", torrent_parser: "t" },
    generated_at: "2026-10-17T00:00:00Z",
    groups,
    unused_feed_identities: [],
    truncated: false,
    limit_per_group: 100,
  };
}

describe("seedRows", () => {
  it("行的选择身份是快照 + 稳定种子键，分组身份是作用域三元组", () => {
    const [rows] = seedGroupsOf(view([group()]));
    expect(seedKeyOf(item())).toBe("snap-1:seed-a");
    expect(groupKeyOf(group())).toBe("anibt:entry-1:g-1");
    expect(rows.rows[0].key).toBe("snap-1:seed-a");
    expect(rows.feedState).toBe("fresh");
  });

  it("沿用服务端返回的分组与条目顺序", () => {
    const groups = seedGroupsOf(
      view([
        group({ scope: { provider: "anibt", provider_entry_ref: "entry-1", provider_group_ref: "g-2" }, display_name: "组B" }),
        group({ scope: { provider: "anibt", provider_entry_ref: "entry-1", provider_group_ref: "g-1" }, display_name: "组A" }),
      ]),
    );
    expect(groups.map((entry) => entry.name)).toEqual(["组B", "组A"]);
  });

  it("形状异常时返回空数组，不抛错", () => {
    expect(seedGroupsOf(undefined)).toEqual([]);
    expect(seedGroupsOf({ groups: "bad" } as unknown as RSSListView)).toEqual([]);
  });

  it("筛选只按标题子串，命中为空的分组不渲染", () => {
    const groups = seedGroupsOf(
      view([
        group({ items: [item({ stable_seed_key: "a", seed: { title: "番剧 01", locator_display: "x" } })] }),
        group({
          scope: { provider: "anibt", provider_entry_ref: "entry-1", provider_group_ref: "g-2" },
          display_name: "组B",
          items: [item({ stable_seed_key: "b", seed: { title: "别的番 02", locator_display: "x" } })],
        }),
      ]),
    );
    const filtered = filterSeedGroups(groups, "番剧");
    expect(filtered.map((entry) => entry.name)).toEqual(["组A"]);
    expect(filterSeedGroups(groups, "  ")).toHaveLength(2);
    expect(filterSeedGroups(groups, "不存在")).toEqual([]);
  });

  it("可选行只取服务端允许下载的行", () => {
    const groups = seedGroupsOf(
      view([
        group({
          items: [
            item({ stable_seed_key: "ok" }),
            item({
              stable_seed_key: "no",
              download_action: { allowed: false, kind: "none", reason_code: "group_not_selected" },
            }),
          ],
        }),
      ]),
    );
    expect(seedRowCount(groups)).toBe(2);
    expect(selectableRows(groups).map((row) => row.stableSeedKey)).toEqual(["ok"]);
  });

  it("只有 fresh 的组不需要刷新", () => {
    const [fresh] = seedGroupsOf(view([group()]));
    const [stale] = seedGroupsOf(view([group({ feed: { state: "refresh_required" } })]));
    const [missing] = seedGroupsOf(view([group({ feed: { state: "snapshot_required" } })]));
    expect(groupNeedsRefresh(fresh)).toBe(false);
    expect(groupNeedsRefresh(stale)).toBe(true);
    expect(groupNeedsRefresh(missing)).toBe(true);
  });

  it("选择按身份增删，不改变已有集合", () => {
    const first = toggleKey(new Set<string>(), "a");
    const second = toggleKey(first, "b");
    const third = toggleKey(second, "a");
    expect([...first]).toEqual(["a"]);
    expect([...second]).toEqual(["a", "b"]);
    expect([...third]).toEqual(["b"]);
  });
});

describe("组行摘要与第二步预览的辅助", () => {
  it("最新发布时间：取最大的有效时间，没有时间时不给", () => {
    expect(
      latestPublishedAt([
        { publishedAt: "2026-10-02T10:00:00Z" },
        { publishedAt: undefined },
        { publishedAt: "2026-10-19T07:12:57Z" },
        { publishedAt: "not-a-date" },
      ]),
    ).toBe("2026-10-19T07:12:57Z");
    expect(latestPublishedAt([{ publishedAt: undefined }])).toBeUndefined();
    expect(latestPublishedAt([])).toBeUndefined();
  });

  it("目标路径短写只留最后一段", () => {
    expect(shortTarget("/library/tv/番剧/番剧 E03.mkv")).toBe("…/番剧 E03.mkv");
    expect(shortTarget("番剧 E03.mkv")).toBe("番剧 E03.mkv");
    expect(shortTarget("/番剧 E03.mkv")).toBe("/番剧 E03.mkv");
  });

  it("空值按来源对应到表单项，其余跳到命名", () => {
    expect(factField("anime.year")).toBe("year");
    expect(factField("anime.title")).toBe("title");
    expect(factField("anime.kind")).toBe("kind");
    expect(factField("anime.season_path")).toBe("season");
    expect(factField("anime.season")).toBe("season");
    expect(factField("bgm.title_cn")).toBe("bgm");
    expect(factField("tmdb.title")).toBe("tmdb");
    expect(factField("ep.number")).toBe("naming");
    expect(factField("file.media.resolution")).toBe("naming");
  });
});
