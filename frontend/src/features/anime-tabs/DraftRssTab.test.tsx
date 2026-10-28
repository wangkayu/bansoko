import { useState } from "react";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  discoverProviderEntryGroups,
  downloadSnapshotSeed,
  expandSnapshotSeed,
  fetchCustomSnapshot,
  previewProviderEntryRSS,
  readAnimeRSSListView,
  readProviderEntryRSS,
  refreshRSSSource,
  retryRelease,
  type AnimeDraft,
  type PreviewFeedSnapshotRef,
  type ProviderRSSPreviewResult,
  type RSSListObservationRef,
  type RSSListView,
} from "../../lib/api/client";
import { RssTab } from "./RssTab";
import type { DraftTabHost } from "./host";
import { t } from "../../lib/i18n";

// The host-layer boundary of the pre-subscription RSS workspace: it is not two panels but one
// read model.
// - a custom draft is the same workspace: the source snapshot is fetched through the saved
//   preset's revision, and when unsaved it only asks to save first;
// - a provider draft first reads the snapshot identity purely, then reads the server-composed
//   RSSListView with that identity;
// - no read may write the shared draft or trigger any owner action.

vi.mock("../../lib/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/api/client")>();
  return {
    ...actual,
    readProviderEntryRSS: vi.fn(),
    previewProviderEntryRSS: vi.fn(),
    discoverProviderEntryGroups: vi.fn(),
    readAnimeRSSListView: vi.fn(),
    downloadSnapshotSeed: vi.fn(),
    expandSnapshotSeed: vi.fn(),
    fetchCustomSnapshot: vi.fn(),
    // Owner actions are mocked as spies so the draft branch's pure-read boundary assertions mean something.
    refreshRSSSource: vi.fn(),
    retryRelease: vi.fn(),
  };
});
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() } }));
const mockedRead = vi.mocked(readProviderEntryRSS);
const mockedPreview = vi.mocked(previewProviderEntryRSS);
const mockedDiscover = vi.mocked(discoverProviderEntryGroups);
const mockedView = vi.mocked(readAnimeRSSListView);
const mockedDownload = vi.mocked(downloadSnapshotSeed);
const mockedExpand = vi.mocked(expandSnapshotSeed);
const mockedCustomFetch = vi.mocked(fetchCustomSnapshot);

function providerDraft(): AnimeDraft {
  return {
    origin: "anibt",
    origin_entry_ref: "entry-1",
    title: "测试番",
    title_override: null,
    instance_label: null,
    metadata: { bgm: null, tmdb: null, total_episodes: null },
    subgroups: {
      max_rss: 3,
      entries: [
        {
          provider: "anibt",
          group_ref: "g-1",
          display_name: "一号组",
          selection_mode: "auto",
          manual_selected: null,
          effective_selected: true,
          score: 80,
        },
      ],
    },
    rules: { sections: {} },
    rss_sources: [],
  } as unknown as AnimeDraft;
}

function customDraft(): AnimeDraft {
  return {
    origin: "custom",
    origin_entry_ref: null,
    title: "自定义番",
    title_override: null,
    instance_label: "自定义番",
    metadata: { bgm: null, tmdb: null, total_episodes: null },
    subgroups: { max_rss: 3, entries: [] },
    rules: { sections: {} },
    rss_sources: [
      {
        source_kind: "custom_rss",
        locator: "https://feeds.example.test/rss.xml",
        locator_display: "https://feeds.example.test/rss.xml",
        group_name: t("field.subgroup"),
        title: "自定义番",
        episode_offset: 0,
      },
    ],
  } as unknown as AnimeDraft;
}

function renderRssTab({
  draft,
  subgroupDiscovery,
  customSnapshot,
  previewable = true,
}: {
  draft: AnimeDraft;
  subgroupDiscovery?: DraftTabHost["subgroupDiscovery"];
  customSnapshot?: DraftTabHost["customSnapshot"];
  previewable?: boolean;
}) {
  const onDraftChange = vi.fn();
  const host: DraftTabHost = {
    mode: "draft",
    draft,
    onDraftChange,
    previewable,
    subgroupDiscovery,
    customSnapshot,
    feedSnapshots: [],
    onFeedSnapshotsChange: () => {},
    observedGroups: [], onDiscovered: () => {},
  };
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RssTab host={host} onExitRulesPreview={() => {}} />
    </QueryClientProvider>,
  );
  return { onDraftChange };
}

const SNAPSHOT: ProviderRSSPreviewResult = {
  provider: "anibt",
  provider_entry_ref: "entry-1",
  group_ref: "g-1",
  observed_at: "2026-08-25T00:00:00Z",
  snapshot_id: "snap-1",
  snapshot_version: 1,
  content_hash: "sha256:" + "b".repeat(64),
  freshness: "fresh",
  seeds: [],
};

function viewFixture(): RSSListView {
  return {
    mode: "current_input",
    config_identity: "draft:test",
    metadata_hold: { held: false, missing: [] },
    versions: { parser: "parser-v1", evaluator: "evaluator-v1", renderer: "1", safety: "path-safety-v1", torrent_parser: "torrent-parser-v1" },
    generated_at: "2026-09-22T10:00:00Z",
    groups: [
      {
        scope: { provider: "anibt", provider_entry_ref: "entry-1", provider_group_ref: "g-1" },
        display_name: "一号组",
        score: 80,
        selection: { source: "configured" as const, effective_selected: true, selection_mode: "auto" },
        offset: { value: 1, editable: false },
        feed: { state: "fresh", snapshot_id: "snap-1", snapshot_version: 1, content_hash: "sha256:" + "b".repeat(64) },
        items: [
          {
            snapshot_id: "snap-1",
            stable_seed_key: "seed-01",
            seed: {
              title: "[一号组] 测试番 - 16",
              published_at: "2026-07-29T16:30:47Z",
              size_bytes: 760600000,
              info_hash: "abcdef1234567890abcdef1234567890abcdef12",
              locator_display: "https://torrent.example.test/16.torrent",
            },
            torrent: {
              state: "available",
              manifest: {
                payload_digest: "sha256:" + "c".repeat(64),
                parser_version: "torrent-parser-v1",
                torrent_version: "v1",
                torrent_name: "测试番 S1",
                info_hash_v1: "abcdef1234567890abcdef1234567890abcdef12",
                total_size_bytes: 760600000,
                file_count: 2,
                projection_digest: "sha256:" + "d".repeat(64),
              },
            },
            evaluation: {
              state: "evaluated",
              verdict: "accepted_pending_download",
              reason_code: "accepted",
              parsed_facts: { episode: 16, group_name: "一号组", special: false },
            },
            files: [
              {
                file_ref: "sha256:" + "e".repeat(64),
                ordinal: 0,
                source_path: "测试番 S1/16.mkv",
                filename: "16.mkv",
                size_bytes: 760000000,
                media_kind: "video",
                included: true,
                safety: "valid",
                rendered_target: "测试番/Season 01/测试番 - E16 [1080p].mkv",
              },
              {
                file_ref: "sha256:" + "f".repeat(64),
                ordinal: 1,
                source_path: "测试番 S1/16.zh-Hans.ass",
                filename: "16.zh-Hans.ass",
                size_bytes: 600,
                media_kind: "subtitle",
                language: "zh-Hans",
                included: false,
                reason_code: "file_extension_not_included",
                safety: "valid",
              },
            ],
            download_action: { allowed: true, kind: "one_shot" },
          },
        ],
      },
    ],
  } as unknown as RSSListView;
}

describe("订阅前 RSS 工作区", () => {
  beforeEach(() => {
    mockedRead.mockReset();
    mockedView.mockReset();
    mockedPreview.mockReset();
    mockedDownload.mockReset();
    mockedExpand.mockReset();
    mockedCustomFetch.mockReset();
  });

  function customView(state: "snapshot_required" | "fresh"): RSSListView {
    const view = viewFixture();
    view.groups = [{
      ...view.groups[0]!,
      scope: { provider: "custom", provider_entry_ref: "custom-source:abc", provider_group_ref: "custom-group:abc" },
      display_name: "自定义源",
      feed: state === "fresh" ? view.groups[0]!.feed : { state },
      items: state === "fresh" ? view.groups[0]!.items : [],
    }] as RSSListView["groups"];
    return view;
  }

  it("未保存的 custom 草稿同样是测试台：列出该组，提示保存后才读取种子，不抓取", async () => {
    mockedView.mockResolvedValue(customView("snapshot_required"));
    renderRssTab({ draft: customDraft() });
    expect(screen.getByText(t("rss.customBenchSaveFirst"))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.template.preview") }));
    await screen.findByText("自定义源");
    expect(mockedView).toHaveBeenCalledTimes(1);
    expect(mockedView.mock.calls[0]![0]).toMatchObject({ input_source: "current_input", feed: [] });
    expect(mockedView.mock.calls[0]![0]).not.toHaveProperty("candidate_id");
    expect(mockedCustomFetch).not.toHaveBeenCalled();
  });

  it("已保存的 custom 草稿按预设修订抓取一次源快照，再带作用域与身份重读", async () => {
    mockedView.mockResolvedValueOnce(customView("snapshot_required")).mockResolvedValue(customView("fresh"));
    mockedCustomFetch.mockResolvedValue({
      ...SNAPSHOT, provider: "custom", provider_entry_ref: "custom-source:abc", group_ref: "custom-group:abc", snapshot_id: "snap-c",
    } as ProviderRSSPreviewResult);
    renderRssTab({ draft: customDraft(), customSnapshot: { candidate_id: "cand-1", revision: 2, expected_version: 5 } });
    expect(screen.queryByText(t("rss.customBenchSaveFirst"))).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.template.preview") }));
    await waitFor(() => expect(mockedView).toHaveBeenCalledTimes(2));
    expect(mockedCustomFetch).toHaveBeenCalledTimes(1);
    expect(mockedCustomFetch).toHaveBeenCalledWith({ candidate_id: "cand-1", revision: 2, expected_version: 5 });
    expect(mockedRead).not.toHaveBeenCalled();
    expect(mockedPreview).not.toHaveBeenCalled();
    expect(mockedView.mock.calls[0]![0]).toMatchObject({ input_source: "current_input", candidate_id: "cand-1", revision: 2, feed: [] });
    expect(mockedView.mock.calls[1]![0]).toMatchObject({
      input_source: "current_input",
      candidate_id: "cand-1",
      revision: 2,
      feed: [expect.objectContaining({ provider: "custom", provider_entry_ref: "custom-source:abc", provider_group_ref: "custom-group:abc", snapshot_id: "snap-c" })],
    });
    await screen.findByText("自定义源");
  });

  it("草稿不可读时不发起任何读取", () => {
    renderRssTab({
      draft: providerDraft(),
      subgroupDiscovery: { provider: "anibt", providerEntryRef: "entry-1" },
      previewable: false,
    });
    expect(screen.getByText(t("rss.needTitle"))).toBeInTheDocument();
    expect(mockedRead).not.toHaveBeenCalled();
    expect(mockedView).not.toHaveBeenCalled();
  });

  it("草稿不自动读取；测试台没有下载入口；配置变化后标为过时", async () => {
    mockedView.mockResolvedValue(viewFixture());
    const draft = providerDraft();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const hostFor = (nextDraft: AnimeDraft): DraftTabHost => ({
      mode: "draft", draft: nextDraft, onDraftChange: vi.fn(), previewable: true,
      subgroupDiscovery: { provider: "anibt", providerEntryRef: "entry-1" },
      feedSnapshots: [], onFeedSnapshotsChange: () => {}, observedGroups: [], onDiscovered: () => {},
    });
    const { rerender } = render(
      <QueryClientProvider client={client}><RssTab host={hostFor(draft)} onExitRulesPreview={() => {}} /></QueryClientProvider>,
    );
    expect(mockedRead).not.toHaveBeenCalled();
    expect(mockedView).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.template.preview") }));
    await screen.findByText("一号组");
    expect(mockedView).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: /展开/ }));
    expect(await screen.findByText("[一号组] 测试番 - 16")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /下载/ })).toBeNull();
    const edited = structuredClone(draft);
    edited.title = "修改后的标题";
    rerender(<QueryClientProvider client={client}><RssTab host={hostFor(edited)} onExitRulesPreview={() => {}} /></QueryClientProvider>);
    expect(screen.getByText(t("rss.configChanged"))).toBeInTheDocument();
    expect(mockedView).toHaveBeenCalledTimes(1);
  });

  it("未保存的元数据（年份、TMDB 季度）随 current_input 草稿提交，不读已保存的修订", async () => {
    mockedView.mockResolvedValue(viewFixture());
    const draft = providerDraft();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const hostFor = (nextDraft: AnimeDraft): DraftTabHost => ({
      mode: "draft", draft: nextDraft, onDraftChange: vi.fn(), previewable: true,
      subgroupDiscovery: { provider: "anibt", providerEntryRef: "entry-1" },
      feedSnapshots: [], onFeedSnapshotsChange: () => {}, observedGroups: [], onDiscovered: () => {},
    });
    const { rerender } = render(
      <QueryClientProvider client={client}><RssTab host={hostFor(draft)} onExitRulesPreview={() => {}} active /></QueryClientProvider>,
    );
    await waitFor(() => expect(mockedView).toHaveBeenCalledTimes(1));
    const edited = structuredClone(draft);
    edited.metadata = { ...edited.metadata, year: 2031, tmdb: { work_id: "265010", media_kind: "tv", season: 3 } };
    rerender(<QueryClientProvider client={client}><RssTab host={hostFor(edited)} onExitRulesPreview={() => {}} active /></QueryClientProvider>);
    expect(await screen.findByText(t("rss.configChanged"))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("rss.preview") }));
    await waitFor(() => expect(mockedView).toHaveBeenCalledTimes(2));
    const request = mockedView.mock.calls[1]![0];
    expect(request).toMatchObject({ input_source: "current_input", draft: { metadata: { year: 2031, tmdb: { season: 3 } } } });
    expect(request).not.toHaveProperty("candidate_id");
    expect(request).not.toHaveProperty("revision");
  });

  it("按服务端 snapshot_required 获取快照后只重读一次", async () => {
    const required = viewFixture();
    required.groups[0].feed.state = "snapshot_required";
    mockedView.mockResolvedValueOnce(required).mockResolvedValueOnce(viewFixture());
    // With no persisted snapshot, the pure read returns empty identity and only then is the provider fetched.
    mockedRead.mockResolvedValue({ ...SNAPSHOT, snapshot_id: null, snapshot_version: null, content_hash: null } as unknown as ProviderRSSPreviewResult);
    mockedPreview.mockResolvedValue(SNAPSHOT);
    renderRssTab({ draft: providerDraft(), subgroupDiscovery: { provider: "anibt", providerEntryRef: "entry-1" } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.template.preview") }));
    await screen.findByText("一号组");
    expect(mockedView).toHaveBeenCalledTimes(2);
    expect(mockedPreview).toHaveBeenCalledTimes(1);
    expect(mockedPreview).toHaveBeenCalledWith("anibt", "entry-1", { group_ref: "g-1" });
    expect(mockedView.mock.calls[0][0].feed).toEqual([]);
    expect(mockedView.mock.calls[1][0].feed).toMatchObject([{ provider_group_ref: "g-1", snapshot_id: "snap-1" }]);
  });

  it("已有持久化快照身份时纯读补齐，不再抓取 provider", async () => {
    const required = viewFixture();
    required.groups[0].feed.state = "snapshot_required";
    mockedView.mockResolvedValueOnce(required).mockResolvedValueOnce(viewFixture());
    mockedRead.mockResolvedValue(SNAPSHOT);
    renderRssTab({ draft: providerDraft(), subgroupDiscovery: { provider: "anibt", providerEntryRef: "entry-1" } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.template.preview") }));
    await screen.findByText("一号组");
    expect(mockedRead).toHaveBeenCalledWith("anibt", "entry-1", "g-1");
    expect(mockedPreview).not.toHaveBeenCalled();
    expect(mockedView).toHaveBeenCalledTimes(2);
    expect(mockedView.mock.calls[1][0].feed).toMatchObject([{ provider_group_ref: "g-1", snapshot_id: "snap-1" }]);
  });
});

describe("订阅前 RSS 工作区：发现的观察", () => {
  const OBSERVATION: RSSListObservationRef = { provider: "anibt", provider_entry_ref: "entry-1", snapshot_version: 7 };
  const discovery = { provider: "anibt", providerEntryRef: "entry-1" } as const;

  beforeEach(() => {
    mockedRead.mockReset();
    mockedView.mockReset();
    mockedPreview.mockReset();
  });

  function renderWith(observation: RSSListObservationRef | undefined, draft = providerDraft()) {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const hostFor = (next: RSSListObservationRef | undefined): DraftTabHost => ({
      mode: "draft", draft, onDraftChange: vi.fn(), previewable: true, subgroupDiscovery: discovery,
      feedSnapshots: [], onFeedSnapshotsChange: () => {}, observation: next, observedGroups: [], onDiscovered: () => {},
    });
    const view = render(
      <QueryClientProvider client={client}><RssTab host={hostFor(observation)} onExitRulesPreview={() => {}} /></QueryClientProvider>,
    );
    const rerenderWith = (next: RSSListObservationRef | undefined) =>
      view.rerender(<QueryClientProvider client={client}><RssTab host={hostFor(next)} onExitRulesPreview={() => {}} /></QueryClientProvider>);
    return { rerenderWith };
  }

  it("把发现的观察随每次读取发给服务端，包括补齐快照后的重读", async () => {
    const required = viewFixture();
    required.groups[0].feed.state = "snapshot_required";
    mockedView.mockResolvedValueOnce(required).mockResolvedValueOnce(viewFixture());
    mockedRead.mockResolvedValue(SNAPSHOT);
    renderWith(OBSERVATION);
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.template.preview") }));
    await screen.findByText("一号组");
    expect(mockedView).toHaveBeenCalledTimes(2);
    expect(mockedView.mock.calls[0][0].observation).toEqual(OBSERVATION);
    expect(mockedView.mock.calls[1][0].observation).toEqual(OBSERVATION);
  });

  it("没有发现过时不带 observation 字段", async () => {
    mockedView.mockResolvedValue(viewFixture());
    renderWith(undefined);
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.template.preview") }));
    await screen.findByText("一号组");
    expect("observation" in mockedView.mock.calls[0][0]).toBe(false);
  });

  it("重新发现使已读取的预览过时，即使草稿没变", async () => {
    mockedView.mockResolvedValue(viewFixture());
    const { rerenderWith } = renderWith(OBSERVATION);
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.template.preview") }));
    await screen.findByText("一号组");
    expect(screen.queryByText(t("rss.configChanged"))).not.toBeInTheDocument();
    rerenderWith({ ...OBSERVATION, snapshot_version: 8 });
    expect(screen.getByText(t("rss.configChanged"))).toBeInTheDocument();
    expect(mockedView).toHaveBeenCalledTimes(1);
  });

  it("发现的组标明按默认策略，观察不再保留时给出重新刷新的提示", async () => {
    const view = viewFixture();
    view.groups[0].selection = { source: "observed_default", effective_selected: true, selection_mode: "auto" };
    (view as { observation?: unknown }).observation = {
      state: "not_retained", provider: "anibt", provider_entry_ref: "entry-1", snapshot_version: 7, observed_at: null, group_count: 0,
    };
    mockedView.mockResolvedValue(view);
    renderWith(OBSERVATION);
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.template.preview") }));
    await screen.findByText("一号组");
    expect(screen.getByText(new RegExp(t("rss.defaultGroup")))).toBeInTheDocument();
    expect(screen.getByText(t("rss.groupsGone"))).toBeInTheDocument();
  });
});

describe("订阅前 RSS 工作区：快照变化使预览过时", () => {
  beforeEach(() => {
    mockedRead.mockReset();
    mockedView.mockReset();
    mockedPreview.mockReset();
  });

  // Refreshing a group's seeds swaps that group's snapshot identity; the preview was computed on
  // the old snapshot and must be marked stale. The host holds real state — same as the dialog —
  // otherwise the snapshot-identity backfill never takes effect.
  function StatefulHost() {
    const [feed, setFeed] = useState<PreviewFeedSnapshotRef[]>([]);
    const host: DraftTabHost = {
      mode: "draft", draft: providerDraft(), onDraftChange: vi.fn(), previewable: true,
      subgroupDiscovery: { provider: "anibt", providerEntryRef: "entry-1" },
      feedSnapshots: feed, onFeedSnapshotsChange: setFeed, observedGroups: [], onDiscovered: () => {},
    };
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return <QueryClientProvider client={client}><RssTab host={host} onExitRulesPreview={() => {}} /></QueryClientProvider>;
  }

  it("刷新某组种子后立刻带新的种子列表身份重读，不停在旧结果上", async () => {
    mockedView.mockResolvedValue(viewFixture());
    mockedPreview.mockResolvedValue({ ...SNAPSHOT, snapshot_id: "snap-2", snapshot_version: 2 });
    render(<StatefulHost />);
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.template.preview") }));
    await screen.findByText("一号组");
    expect(screen.queryByText(t("rss.configChanged"))).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /展开/ }));
    fireEvent.click(await screen.findByRole("button", { name: t("rss.groupRefresh") }));
    await waitFor(() => expect(mockedPreview).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(mockedView).toHaveBeenCalledTimes(2));
    expect(mockedView.mock.calls[1][0].feed).toMatchObject([{ provider_group_ref: "g-1", snapshot_id: "snap-2", snapshot_version: 2 }]);
    await waitFor(() => expect(screen.queryByText(t("rss.configChanged"))).not.toBeInTheDocument());
  });

  // The read model fails closed page-wide on a stale identity (409
//   FEED_SNAPSHOT_REFRESH_REQUIRED), and the workspace has no entry to refresh it anyway.
//   Dropping the local identity and re-reading once is what makes the server report the group as
//   snapshot_required, giving the backfill logic its chance to decide pure read vs fetch by
//   freshness.
  it("本地保留的快照身份已过期时，丢掉身份重读并重新补齐", async () => {
    const { ApiError } = await import("../../lib/api/client");
    const required = viewFixture();
    required.groups[0].feed.state = "snapshot_required";
    mockedView
      .mockRejectedValueOnce(new ApiError({ status: 409, code: "FEED_SNAPSHOT_REFRESH_REQUIRED", message: "snapshot stale" }))
      .mockResolvedValueOnce(required)
      .mockResolvedValueOnce(viewFixture());
    // The persisted snapshot is still inside the fresh window: a pure read suffices, no network.
    mockedRead.mockResolvedValue(SNAPSHOT);
    render(<StatefulHost />);
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.template.preview") }));
    await screen.findByText("一号组");
    expect(mockedView).toHaveBeenCalledTimes(3);
    expect(mockedView.mock.calls[1][0].feed).toEqual([]);
    expect(mockedRead).toHaveBeenCalledWith("anibt", "entry-1", "g-1");
    expect(mockedPreview).not.toHaveBeenCalled();
    expect(mockedView.mock.calls[2][0].feed).toMatchObject([{ provider_group_ref: "g-1", snapshot_id: "snap-1" }]);
  });

  it("持久化快照已过期时抓取 provider，而不是把过期身份交给读模型", async () => {
    const required = viewFixture();
    required.groups[0].feed.state = "snapshot_required";
    mockedView.mockResolvedValueOnce(required).mockResolvedValueOnce(viewFixture());
    // The identity is complete but outside the fresh window: reading with it would just fail closed again.
    mockedRead.mockResolvedValue({ ...SNAPSHOT, freshness: "stale" });
    mockedPreview.mockResolvedValue({ ...SNAPSHOT, snapshot_id: "snap-2", snapshot_version: 2 });
    renderRssTab({ draft: providerDraft(), subgroupDiscovery: { provider: "anibt", providerEntryRef: "entry-1" } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.template.preview") }));
    await screen.findByText("一号组");
    expect(mockedPreview).toHaveBeenCalledTimes(1);
    expect(mockedView.mock.calls[1][0].feed).toMatchObject([{ provider_group_ref: "g-1", snapshot_id: "snap-2" }]);
  });
});

describe("订阅前 RSS 工作区：激活时默认读取", () => {
  const discovery = { provider: "anibt", providerEntryRef: "entry-1" } as const;

  beforeEach(() => {
    mockedRead.mockReset();
    mockedView.mockReset();
    mockedPreview.mockReset();
  });

  function renderActive(initial: { active: boolean; previewable?: boolean }) {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const hostFor = (previewable: boolean): DraftTabHost => ({
      mode: "draft", draft: providerDraft(), onDraftChange: vi.fn(), previewable, subgroupDiscovery: discovery,
      feedSnapshots: [], onFeedSnapshotsChange: () => {}, observedGroups: [], onDiscovered: () => {},
    });
    const element = (active: boolean, previewable: boolean) => (
      <QueryClientProvider client={client}><RssTab host={hostFor(previewable)} onExitRulesPreview={() => {}} active={active} /></QueryClientProvider>
    );
    const view = render(element(initial.active, initial.previewable ?? true));
    return { setActive: (active: boolean, previewable = initial.previewable ?? true) => view.rerender(element(active, previewable)) };
  }

  it("tab 被激活时不用点预览就读取一次", async () => {
    mockedView.mockResolvedValue(viewFixture());
    renderActive({ active: true });
    await screen.findByText("一号组");
    expect(mockedView).toHaveBeenCalledTimes(1);
  });

  it("所有 tab 常驻挂载：没被激活就不读取，激活时才读", async () => {
    mockedView.mockResolvedValue(viewFixture());
    const { setActive } = renderActive({ active: false });
    expect(mockedView).not.toHaveBeenCalled();
    setActive(true);
    await screen.findByText("一号组");
    expect(mockedView).toHaveBeenCalledTimes(1);
  });

  it("首次读取期间说明会取回并解析种子，不是卡住", async () => {
    let finish: (value: RSSListView) => void = () => {};
    mockedView.mockReturnValue(new Promise<RSSListView>((resolve) => { finish = resolve; }));
    renderActive({ active: true });
    expect(await screen.findByText(t("rss.firstLoad"))).toBeInTheDocument();
    finish(viewFixture());
    await screen.findByText("一号组");
    expect(screen.queryByText(/首次会取回并解析选中字幕组的种子/)).not.toBeInTheDocument();
  });

  it("草稿还不可读时不读取", () => {
    renderActive({ active: true, previewable: false });
    expect(mockedView).not.toHaveBeenCalled();
  });

  // Later config changes only mark stale and require an explicit re-preview: switching back to the tab must not auto-recompute.
  it("读过一次后，离开再回来不会重复读取", async () => {
    mockedView.mockResolvedValue(viewFixture());
    const { setActive } = renderActive({ active: true });
    await screen.findByText("一号组");
    setActive(false);
    setActive(true);
    await waitFor(() => expect(mockedView).toHaveBeenCalledTimes(1));
  });

  // A read failure also auto-retries only once, never in a loop; the user retries explicitly with the preview button.
  it("自动读取失败后不循环重试", async () => {
    mockedView.mockRejectedValue(new Error("读取失败"));
    renderActive({ active: true });
    await screen.findByText(t("rss.loadFailed"));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(mockedView).toHaveBeenCalledTimes(1);
  });
});

describe("订阅前 RSS 工作区：在本 tab 内发现字幕组", () => {
  const discovery = { provider: "anibt", providerEntryRef: "entry-1" } as const;
  const OBS_A: RSSListObservationRef = { provider: "anibt", provider_entry_ref: "entry-1", snapshot_version: 3 };
  const OBS_B: RSSListObservationRef = { ...OBS_A, snapshot_version: 4 };

  beforeEach(() => {
    mockedRead.mockReset();
    mockedView.mockReset();
    mockedPreview.mockReset();
    mockedDiscover.mockReset();
  });

  function renderTab(options: { active: boolean; observation?: RSSListObservationRef; onDiscovered?: DraftTabHost["onDiscovered"] }) {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const element = (observation: RSSListObservationRef | undefined, active: boolean) => {
      const host: DraftTabHost = {
        mode: "draft", draft: providerDraft(), onDraftChange: vi.fn(), previewable: true, subgroupDiscovery: discovery,
        feedSnapshots: [], onFeedSnapshotsChange: () => {}, observation, observedGroups: [],
        onDiscovered: options.onDiscovered ?? (() => {}),
      };
      return <QueryClientProvider client={client}><RssTab host={host} onExitRulesPreview={() => {}} active={active} /></QueryClientProvider>;
    };
    const view = render(element(options.observation, options.active));
    return { update: (observation: RSSListObservationRef | undefined, active = options.active) => view.rerender(element(observation, active)) };
  }

  it("RSS tab 里就有「刷新字幕组」，发现结果交给宿主，不必切到字幕组 tab", async () => {
    mockedDiscover.mockResolvedValue({
      provider: "anibt", provider_entry_ref: "entry-1", observed_at: "2026-10-03T00:00:00Z", source_version: "v1",
      schema_version: "groups-v1", snapshot_version: 3, groups: [{ provider: "anibt", group_ref: "g-1", display_name: "一号组", feed_url: "https://example.test/rss", observed_at: "2026-10-03T00:00:00Z" }],
    });
    const onDiscovered = vi.fn();
    renderTab({ active: false, onDiscovered });
    fireEvent.click(screen.getByRole("button", { name: t("config.discovery.refresh") }));
    await waitFor(() => expect(onDiscovered).toHaveBeenCalledTimes(1));
    expect(onDiscovered.mock.calls[0][1]).toEqual(OBS_A);
  });

  it("每一次新的观察自动读一次，同一次观察不重复读", async () => {
    mockedView.mockResolvedValue(viewFixture());
    const { update } = renderTab({ active: true, observation: OBS_A });
    await screen.findByText("一号组");
    expect(mockedView).toHaveBeenCalledTimes(1);
    expect(mockedView.mock.calls[0][0].observation).toEqual(OBS_A);
    update(OBS_A);
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(mockedView).toHaveBeenCalledTimes(1);
    update(OBS_B);
    await waitFor(() => expect(mockedView).toHaveBeenCalledTimes(2));
    expect(mockedView.mock.calls[1][0].observation).toEqual(OBS_B);
  });

  it("在字幕组 tab 发现的新观察，切到 RSS tab 时读一次", async () => {
    mockedView.mockResolvedValue(viewFixture());
    const { update } = renderTab({ active: false, observation: undefined });
    update(OBS_A, false);
    expect(mockedView).not.toHaveBeenCalled();
    update(OBS_A, true);
    await waitFor(() => expect(mockedView).toHaveBeenCalledTimes(1));
    expect(mockedView.mock.calls[0][0].observation).toEqual(OBS_A);
  });
});

describe("订阅前 RSS 工作区：快照过期的展开恢复", () => {
  beforeEach(() => {
    mockedRead.mockReset();
    mockedView.mockReset();
    mockedPreview.mockReset();
    mockedDownload.mockReset();
    mockedExpand.mockReset();
  });

  // A refresh swaps the snapshot identity, so the host must hold real state (same as the dialog) for the identity backfill to take effect.
  function StatefulHost() {
    const [feed, setFeed] = useState<PreviewFeedSnapshotRef[]>([]);
    const host: DraftTabHost = {
      mode: "draft", draft: providerDraft(), onDraftChange: vi.fn(), previewable: true,
      subgroupDiscovery: { provider: "anibt", providerEntryRef: "entry-1" },
      feedSnapshots: feed, onFeedSnapshotsChange: setFeed, observedGroups: [], onDiscovered: () => {},
    };
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return <QueryClientProvider client={client}><RssTab host={host} onExitRulesPreview={() => {}} /></QueryClientProvider>;
  }

  it("展开遇快照过期给出「刷新种子列表」动作，点击后刷新该组并重读预览", async () => {
    const { ApiError } = await import("../../lib/api/client");
    const { toast: sonnerToast } = await import("sonner");
    // Seeds are left unexpanded so the stale identity surfaces at the expand step.
    const stale = viewFixture();
    stale.groups[0].items[0].torrent = { state: "pending" };
    mockedView.mockResolvedValue(stale);
    mockedExpand.mockRejectedValue(
      new ApiError({ status: 409, code: "FEED_SNAPSHOT_REFRESH_REQUIRED", message: "snapshot stale" }),
    );
    mockedPreview.mockResolvedValue({ ...SNAPSHOT, snapshot_id: "snap-2", snapshot_version: 2 });
    render(<StatefulHost />);
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.template.preview") }));
    await screen.findByText("一号组");
    fireEvent.click(screen.getByRole("button", { name: /展开/ }));
    fireEvent.click(await screen.findByRole("button", { name: t("rss.expandFiles") }));
    fireEvent.click(await screen.findByRole("button", { name: t("rss.expandSeed") }));

    const mockedError = vi.mocked(sonnerToast.error);
    await waitFor(() => expect(mockedError).toHaveBeenCalled());
    const actionCall = mockedError.mock.calls.find(([, data]) => data?.action);
    expect(actionCall).toBeTruthy();
    const action = actionCall![1]!.action as unknown as { label: string; onClick: () => void };
    expect(action.label).toBe(t("rss.refreshSeedsAction"));

    // Click action: refresh that group's seed list (one provider fetch), then re-read the preview and prompt retry.
    action.onClick();
    await waitFor(() => expect(mockedPreview).toHaveBeenCalledWith("anibt", "entry-1", { group_ref: "g-1" }));
    await waitFor(() => expect(mockedView).toHaveBeenCalledTimes(2));
    expect(mockedView.mock.calls[1][0].feed).toMatchObject([{ provider_group_ref: "g-1", snapshot_id: "snap-2" }]);
    await waitFor(() => expect(vi.mocked(sonnerToast.info)).toHaveBeenCalledWith(t("rss.seedsRefreshedRetry")));
  });
});

describe("订阅前 RSS 工作区：草稿偏移编辑与文件行", () => {
  beforeEach(() => {
    mockedRead.mockReset();
    mockedView.mockReset();
    mockedPreview.mockReset();
    mockedDownload.mockReset();
    mockedExpand.mockReset();
  });

  // Carries the group's offset in provider_group_offsets: the write target of a provider draft's offset editing.
  function offsetDraft(): AnimeDraft {
    const draft = providerDraft();
    draft.provider_group_offsets = [{ provider_group_ref: "g-1", episode_offset: 2 }];
    return draft;
  }

  it("组头偏移按钮开 dialog，保存写内存草稿并按新偏移重读预览", async () => {
    mockedView.mockResolvedValue(viewFixture());
    const { onDraftChange } = renderRssTab({
      draft: offsetDraft(),
      subgroupDiscovery: { provider: "anibt", providerEntryRef: "entry-1" },
    });
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.template.preview") }));
    await screen.findByText("一号组");
    expect(mockedView).toHaveBeenCalledTimes(1);

    // The group header's offset entry reads the draft live (current value 2), not the server view's offset.
    fireEvent.click(screen.getByRole("button", { name: t("rss.adjustOffset", { name: "一号组" }) }));
    const dialog = await screen.findByRole("dialog");
    const input = within(dialog).getByLabelText(t("anime.offset.label"));
    expect(input).toHaveValue(2);
    fireEvent.change(input, { target: { value: "5" } });
    fireEvent.click(within(dialog).getByRole("button", { name: t("rss.saveOffset") }));

    // Writes only the group's provider_group_offsets entry, triggering no owner action.
    expect(onDraftChange).toHaveBeenCalledTimes(1);
    const nextDraft = onDraftChange.mock.calls[0][0] as AnimeDraft;
    expect(nextDraft.provider_group_offsets).toEqual([{ provider_group_ref: "g-1", episode_offset: 5 }]);
    expect(nextDraft.rss_sources).toEqual([]);

    // Follows the draft-change → view-re-read channel: the preview re-reads with the new draft immediately.
    await waitFor(() => expect(mockedView).toHaveBeenCalledTimes(2));
    expect((mockedView.mock.calls[1][0].draft as AnimeDraft).provider_group_offsets).toEqual([{ provider_group_ref: "g-1", episode_offset: 5 }]);
  });

  it("偏移越界内联报错，不保存也不重读", async () => {
    mockedView.mockResolvedValue(viewFixture());
    const { onDraftChange } = renderRssTab({
      draft: offsetDraft(),
      subgroupDiscovery: { provider: "anibt", providerEntryRef: "entry-1" },
    });
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.template.preview") }));
    await screen.findByText("一号组");

    fireEvent.click(screen.getByRole("button", { name: t("rss.adjustOffset", { name: "一号组" }) }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(t("anime.offset.label")), { target: { value: "1001" } });
    fireEvent.click(within(dialog).getByRole("button", { name: t("rss.saveOffset") }));

    expect(within(dialog).getByText(t("rss.offsetInvalid"))).toBeInTheDocument();
    expect(onDraftChange).not.toHaveBeenCalled();
    expect(mockedView).toHaveBeenCalledTimes(1);
  });

  it("偏移弹窗按输入实时换算：第 15 集 → 第 20 集，无效输入时隐藏", async () => {
    // The fixture view's seed shows episode 16 under the group's current offset 1, so its raw episode is 15.
    mockedView.mockResolvedValue(viewFixture());
    renderRssTab({
      draft: offsetDraft(),
      subgroupDiscovery: { provider: "anibt", providerEntryRef: "entry-1" },
    });
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.template.preview") }));
    await screen.findByText("一号组");
    fireEvent.click(screen.getByRole("button", { name: t("rss.adjustOffset", { name: "一号组" }) }));
    const dialog = await screen.findByRole("dialog");
    const input = within(dialog).getByLabelText(t("anime.offset.label"));
    expect(within(dialog).getByText(t("rss.offsetConversion", { from: 15, to: 17 }))).toBeInTheDocument();
    fireEvent.change(input, { target: { value: "5" } });
    expect(within(dialog).getByText(t("rss.offsetConversion", { from: 15, to: 20 }))).toBeInTheDocument();
    fireEvent.change(input, { target: { value: "" } });
    expect(dialog.querySelector("p.text-muted")).toBeNull();
  });

  it("组内没有集数时偏移弹窗不显示换算行", async () => {
    const view = viewFixture();
    view.groups[0]!.items[0]!.evaluation.parsed_facts = { group_name: "一号组", special: false };
    mockedView.mockResolvedValue(view);
    renderRssTab({
      draft: offsetDraft(),
      subgroupDiscovery: { provider: "anibt", providerEntryRef: "entry-1" },
    });
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.template.preview") }));
    await screen.findByText("一号组");
    fireEvent.click(screen.getByRole("button", { name: t("rss.adjustOffset", { name: "一号组" }) }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(t("anime.offset.label")), { target: { value: "5" } });
    expect(dialog.querySelector("p.text-muted")).toBeNull();
  });

  it("provider 草稿的偏移改回 0 时移除该组条目", async () => {
    mockedView.mockResolvedValue(viewFixture());
    const { onDraftChange } = renderRssTab({
      draft: offsetDraft(),
      subgroupDiscovery: { provider: "anibt", providerEntryRef: "entry-1" },
    });
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.template.preview") }));
    await screen.findByText("一号组");
    fireEvent.click(screen.getByRole("button", { name: t("rss.adjustOffset", { name: "一号组" }) }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(t("anime.offset.label")), { target: { value: "0" } });
    fireEvent.click(within(dialog).getByRole("button", { name: t("rss.saveOffset") }));
    expect((onDraftChange.mock.calls[0][0] as AnimeDraft).provider_group_offsets).toEqual([]);
  });

  it("展开种子不再显示「来源」行；文件 meta 单行不折行", async () => {
    mockedView.mockResolvedValue(viewFixture());
    renderRssTab({
      draft: providerDraft(),
      subgroupDiscovery: { provider: "anibt", providerEntryRef: "entry-1" },
    });
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.template.preview") }));
    await screen.findByText("一号组");
    fireEvent.click(screen.getByRole("button", { name: t("rss.expandGroup", { name: "一号组" }) }));
    fireEvent.click(await screen.findByRole("button", { name: t("rss.expandFiles") }));

    // The file list renders, but the "source: ..." row is gone.
    expect(await screen.findByText("测试番 S1/16.mkv")).toBeInTheDocument();
    expect(screen.queryByText(/来源：/)).not.toBeInTheDocument();

    // File meta (size · type · language) carries nowrap: single line without wrapping, ellipsis-truncated on narrow panels.
    const meta = document.querySelector(".seed-row-meta-nowrap");
    expect(meta).not.toBeNull();
    expect(meta).toHaveTextContent("视频");
  });
});
