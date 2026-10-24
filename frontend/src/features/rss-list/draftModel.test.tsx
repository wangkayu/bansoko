import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { RSSListGroup, RSSListItem, RSSListView } from "../../lib/api/client";
import { RssListViewPanel } from "./RssListViewPanel";
import { draftWorkspaceModel } from "./draftModel";
import { t } from "../../lib/i18n";

// An unexpanded seed: the server gives only RSS facts — not "structure unavailable" but not yet fetched.
const PENDING_ITEM: RSSListItem = {
  snapshot_id: "snapshot-1",
  stable_seed_key: "seed-1",
  seed: { title: "Show - 03", published_at: "2026-01-01T00:00:00Z", size_bytes: 123, locator_display: "https://example.test/feed" },
  torrent: { state: "pending" },
  evaluation: { state: "pending" },
  files: [],
  download_action: { allowed: false, kind: "none", reason_code: "seed_not_expanded", reason: "该种子尚未展开" },
  library_overlap: { verdict: "undetermined", present_files: null, pending_files: null },
};

const GROUP: RSSListGroup = {
  scope: { provider: "anibt", provider_entry_ref: "entry", provider_group_ref: "group", rss_source_id: "source-1" },
  display_name: "Group",
  selection: { source: "configured" as const, effective_selected: true, selection_mode: "manual", count_in_limit: true },
  offset: { value: 0, editable: true, source: "link" },
  feed: { state: "fresh", snapshot_version: 7, content_hash: "hash" },
  items: [PENDING_ITEM],
};

function viewWith(item: RSSListItem = PENDING_ITEM): RSSListView {
  return {
    mode: "current_input",
    config_identity: "draft:1@1",
    metadata_hold: { held: false, missing: [] },
    versions: { parser: "p", evaluator: "e", renderer: "r", safety: "s", torrent_parser: "t" },
    generated_at: "2026-01-01T00:00:00Z",
    groups: [{ ...GROUP, items: [item] }],
    unused_feed_identities: [],
    truncated: false,
    limit_per_group: 100,
  };
}

/** Opens a group and a seed row: the expansion entry and the file area render only while the seed row is expanded. */
function openRow(label = t("rss.expandFiles")) {
  fireEvent.click(screen.getByRole("button", { name: t("rss.expandGroup", { name: "Group" }) }));
  fireEvent.click(screen.getByRole("button", { name: label }));
}

describe("pending bench start_baseline row", () => {
  it("创建订阅前发布的行显示「订阅前发布」，不算过滤", () => {
    const item = draftWorkspaceModel(
      viewWith({
        ...PENDING_ITEM,
        download_action: { allowed: false, kind: "none", reason_code: "start_baseline", reason: "Published before subscribing." },
      }),
      {},
    ).groups[0]?.items[0];
    expect(item?.status).toEqual({ label: t("verdict.start_baseline"), tone: "muted" });
    expect(item?.filtered).toBe(false);
    expect(item?.statusDetail).toBeUndefined();
  });
});

describe("pending seed row", () => {
  it("未展开的行只给中性徽章，不给文件列表与结构摘要", () => {
    const item = draftWorkspaceModel(viewWith(), {}).groups[0]?.items[0];
    expect(item?.status).toEqual({ label: t("rss.notExpanded"), tone: "muted" });
    // Unexpanded is neither filtered nor failed: no verdict note row and neither a structure summary nor an unavailable verdict.
    expect(item?.statusDetail).toBeUndefined();
    expect(item?.filtered).toBe(false);
    expect(item?.manifestSummary).toBeUndefined();
    expect(item?.manifestUnavailable).toBeUndefined();
    expect(item?.files).toEqual([]);
    // No expansion entry renders when the adapter provides no handler.
    expect(item?.expand).toBeUndefined();
  });

  it("测试台没有下载入口", () => {
    const item = draftWorkspaceModel(viewWith(), { onExpand: () => {} }).groups[0]?.items[0];
    expect(item?.download).toBeUndefined();
  });

  it("展开失败仍算未展开：入口变成重试并带出原因", () => {
    const onExpand = vi.fn();
    const item = draftWorkspaceModel(
      viewWith(),
      { onExpand, expandStates: new Map([["snapshot-1:seed-1", { state: "failed", error: t("reason.torrent_fetch_failed") }]]) },
    ).groups[0]?.items[0];
    expect(item?.status).toEqual({ label: t("rss.notExpanded"), tone: "muted" });
    expect(item?.expand).toMatchObject({ state: "failed", label: t("rss.expandRetry"), hint: t("rss.expandHint"), error: t("reason.torrent_fetch_failed") });
  });

  it("展开进行中时入口转圈", () => {
    const item = draftWorkspaceModel(viewWith(), { onExpand: () => {}, expandStates: new Map([["snapshot-1:seed-1", { state: "loading" }]]) }).groups[0]?.items[0];
    expect(item?.expand).toMatchObject({ state: "loading", label: t("rss.expandLoading") });
  });

  it("适配器要求打开的行由模型带出 open", () => {
    const opened = draftWorkspaceModel(viewWith(), { onExpand: () => {}, openedSeedKey: "snapshot-1:seed-1" }).groups[0]?.items[0];
    const other = draftWorkspaceModel(viewWith(), { onExpand: () => {}, openedSeedKey: "snapshot-1:other" }).groups[0]?.items[0];
    expect(opened?.open).toBe(true);
    expect(other?.open).toBe(false);
  });

  it("结构不可用的行给原因，且没有展开入口", () => {
    const unavailable: RSSListItem = {
      ...PENDING_ITEM,
      torrent: { state: "unavailable", error_code: "torrent_payload_unavailable" },
      evaluation: { state: "unavailable" },
      download_action: { allowed: false, kind: "none", reason_code: "torrent_payload_unavailable", reason: "缺少种子文件" },
    };
    const item = draftWorkspaceModel(viewWith(unavailable), { onExpand: () => {} }).groups[0]?.items[0];
    expect(item?.manifestUnavailable).toBe(t("reason.torrent_payload_unavailable"));
    expect(item?.expand).toBeUndefined();
  });
});

describe("pending seed row rendering", () => {
  it("展开入口只在行展开后出现，点击调用适配器", () => {
    const onExpand = vi.fn();
    render(<RssListViewPanel model={draftWorkspaceModel(viewWith(), { onExpand })} showFiltered={false} />);

    // With the row collapsed the file area (including the expansion entry) does not render.
    expect(screen.queryByRole("button", { name: t("rss.expandSeed") })).toBeNull();
    openRow();

    expect(screen.getByText(t("rss.expandHint"))).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: t("rss.expandSeed") }));
    expect(onExpand).toHaveBeenCalledTimes(1);
  });

  it("展开失败在行内给出原因与重试，没有文件列表", () => {
    const onExpand = vi.fn();
    const model = draftWorkspaceModel(
      viewWith(),
      { onExpand, expandStates: new Map([["snapshot-1:seed-1", { state: "failed", error: t("reason.torrent_missing") }]]) },
    );
    render(<RssListViewPanel model={model} showFiltered={false} />);
    openRow();

    const row = screen.getByText("Show - 03").closest("li");
    expect(row).not.toBeNull();
    expect(within(row as HTMLElement).getByRole("alert")).toHaveTextContent(t("reason.torrent_missing"));
    expect(within(row as HTMLElement).queryByText(t("rss.manifestMissing"))).toBeNull();
    fireEvent.click(within(row as HTMLElement).getByRole("button", { name: t("rss.expandRetry") }));
    expect(onExpand).toHaveBeenCalledTimes(1);
  });

  it("展开进行中时入口禁用", () => {
    const model = draftWorkspaceModel(viewWith(), { onExpand: () => {}, expandStates: new Map([["snapshot-1:seed-1", { state: "loading" }]]) });
    render(<RssListViewPanel model={model} showFiltered={false} />);
    openRow();

    expect(screen.getByRole("button", { name: t("rss.expandLoading") })).toBeDisabled();
  });
});

describe("file target notes", () => {
  const file = { file_ref: "f1", ordinal: 0, source_path: "Show - 03.mkv", filename: "Show - 03.mkv", included: true, safety: "valid" as const };

  it("渲染成功的目标附带空值说明", () => {
    const item = { ...PENDING_ITEM, files: [{ ...file, rendered_target: "/media/Show/Season 01/Show - S01E03.mkv", empty_facts: ["anime.year"] }] };
    const row = draftWorkspaceModel(viewWith(item), {}).groups[0]?.items[0]?.files[0];
    expect(row?.targetNote).toBe("空值：年份");
    expect(row?.targetError).toBeUndefined();
  });

  it("空值导致路径无效时列出全部空值", () => {
    const item = {
      ...PENDING_ITEM,
      files: [{ ...file, included: false, safety: "invalid" as const, reason_code: "naming_fact_missing_anime_year", render_error: "raw", empty_facts: ["anime.year", "rt.title"] }],
    };
    const row = draftWorkspaceModel(viewWith(item), {}).groups[0]?.items[0]?.files[0];
    expect(row?.targetNote).toBeUndefined();
    expect(row?.targetError).toMatch(/^年份、.*为空，路径无效$/);
  });

  it("未定义的变量按模板写法列出", () => {
    const item = {
      ...PENDING_ITEM,
      files: [{ ...file, included: false, safety: "invalid" as const, reason_code: "naming_fact_undefined_bgm_id", render_error: "raw", empty_facts: ["bgm.id"] }],
    };
    const row = draftWorkspaceModel(viewWith(item), {}).groups[0]?.items[0]?.files[0];
    expect(row?.targetError).toBe("未定义的变量：bgm.id");
  });
});

describe("group feed state on the bench", () => {
  function unselectedView(): RSSListView {
    const view = viewWith();
    view.groups = [{
      ...GROUP,
      selection: { source: "configured" as const, effective_selected: false, selection_mode: "manual", count_in_limit: false, reason_code: "policy_never" },
      feed: { state: "missing" },
      items: [],
    }];
    return view;
  }

  it("未选字幕组不读取种子：状态写明不读取，不说过期，也没有刷新入口", () => {
    const onRefreshGroup = vi.fn();
    const model = draftWorkspaceModel(unselectedView(), { onRefreshGroup });
    const group = model.groups[0];
    expect(group.feed.label).toBe(t("rss.feed.notSelected"));
    expect(group.feed.label).not.toBe(t("rss.feed.expired"));
    expect(group.onRefresh).toBeUndefined();
    expect(group.emptyText).toBe(t("rss.emptyNotSelected"));
    expect(group.feedDetail).toBe(t("rss.groupSummaryNotRead", { selected: t("rss.notSelected"), offset: 0 }));
  });

  it("已选字幕组的过期标签不再声称点击即可刷新，刷新是组内的显式按钮", () => {
    const view = viewWith();
    view.groups = [{ ...GROUP, feed: { state: "stale", snapshot_version: 7, content_hash: "hash" } }];
    const onRefreshGroup = vi.fn();
    render(<RssListViewPanel model={draftWorkspaceModel(view, { onRefreshGroup })} showFiltered={false} />);
    expect(screen.getByText(t("rss.feed.expired"))).toBeVisible();
    expect(t("rss.feed.expired")).not.toMatch(/点击|click/i);
    fireEvent.click(screen.getByRole("button", { name: t("rss.expandGroup", { name: "Group" }) }));
    fireEvent.click(screen.getByRole("button", { name: t("rss.groupRefresh") }));
    expect(onRefreshGroup).toHaveBeenCalledTimes(1);
  });

  it("刷新进行中时组状态显示刷新中", () => {
    const model = draftWorkspaceModel(viewWith(), { onRefreshGroup: vi.fn(), refreshingGroup: "anibt:entry:group" });
    expect(model.groups[0].feed.label).toBe(t("rss.feed.refreshing"));
    expect(model.groups[0].refreshBusy).toBe(true);
  });
});

describe("bound subtitles in the seed file list", () => {
  const EXPANDED: RSSListItem = {
    ...PENDING_ITEM,
    torrent: { state: "available", manifest: { file_count: 5 } as NonNullable<RSSListItem["torrent"]["manifest"]> },
    evaluation: { state: "evaluated", verdict: "accepted_pending_download" },
    files: [
      { file_ref: "v1", ordinal: 0, source_path: "Show/Show - 03.mkv", filename: "Show - 03.mkv", size_bytes: 1000, media_kind: "video", included: true, safety: "valid", rendered_target: "/media/Show/Show - 03.mkv" },
      { file_ref: "s1", ordinal: 1, source_path: "Show/Show - 03.sc.ass", filename: "Show - 03.sc.ass", size_bytes: 20, media_kind: "subtitle", language: "zh-Hans", included: true, safety: "valid", rendered_target: "/media/Show/Show - 03.zh-Hans.ass", bound_video_source_path: "Show/Show - 03.mkv" },
      { file_ref: "s2", ordinal: 2, source_path: "Show/Show - 03.chs.ass", filename: "Show - 03.chs.ass", size_bytes: 20, media_kind: "subtitle", language: "zh-Hans", included: false, reason_code: "target_duplicate", safety: "valid", bound_video_source_path: "Show/Show - 03.mkv" },
      { file_ref: "s3", ordinal: 3, source_path: "Show/Show - 03.ass", filename: "Show - 03.ass", size_bytes: 20, media_kind: "subtitle", included: true, safety: "valid", rendered_target: "/media/Show/Show - 03.ass", bound_video_source_path: "Show/Show - 03.mkv" },
      { file_ref: "s4", ordinal: 4, source_path: "Show/notes.ass", filename: "notes.ass", size_bytes: 20, media_kind: "subtitle", included: false, reason_code: "subtitle_binding_missing", safety: "valid" },
    ],
  };

  it("shows bound subtitles as format badges on the video row; unbound subtitles stay flat", () => {
    render(<RssListViewPanel model={draftWorkspaceModel(viewWith(EXPANDED), {})} showFiltered />);
    openRow();
    const video = screen.getByText("Show/Show - 03.mkv").closest("li") as HTMLElement;
    expect(screen.queryByText("Show/Show - 03.sc.ass")).toBeNull();
    expect(screen.getByText("Show/notes.ass").closest("li")).not.toBe(video);
    const badges = Array.from(video.querySelectorAll<HTMLElement>("[data-subtitle-badge]"));
    expect(badges.map((badge) => badge.dataset.subtitleBadge)).toEqual(["placed", "duplicate", "placed"]);
    expect(badges[1]!.querySelector("s")).toHaveTextContent("ASS");
    expect(badges[1]).toHaveTextContent(t("ui.subtitles.duplicate"));
    // A subtitle without a language is still nothing more than an ASS badge.
    expect(badges[2]).toHaveTextContent(/^ASS$/);
    // The seed summary counts by video unit.
    expect(screen.getByText([
      t("ui.fileUnits.videos", { n: 1 }),
      t("ui.fileUnits.subtitlesPartial", { placed: 2, total: 3 }),
      t("ui.fileUnits.others", { n: 1 }),
    ].join(" · "))).toBeVisible();

    const toggle = within(video).getByRole("button", { name: t("ui.subtitles.expand", { name: "Show/Show - 03.mkv", n: 3 }) });
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(within(video).getByText("→ /media/Show/Show - 03.zh-Hans.ass")).toBeVisible();
    const duplicate = within(video).getByText("Show/Show - 03.chs.ass").closest("li") as HTMLElement;
    expect(duplicate).toHaveTextContent(t("ui.subtitles.duplicate"));
    const bare = within(video).getByText("Show/Show - 03.ass").closest("li") as HTMLElement;
    expect(bare).toHaveTextContent(t("ui.subtitles.noLanguage"));
  });
});
