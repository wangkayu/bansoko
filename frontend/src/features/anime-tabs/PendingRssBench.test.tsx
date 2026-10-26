import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  expandSnapshotSeed,
  fetchOwnerCustomSnapshot,
  previewProviderEntryRSS,
  readAnimeRSSListView,
  readProviderEntryRSS,
  refreshRSSSource,
  retryRelease,
  submitReconciliationScan,
  type AnimeMetadataPreview,
  type AnimeWorkspace,
  type RSSListView,
  type RulesSections,
} from "../../lib/api/client";
import { workspaceCapability } from "../workspace/capability";
import { RssTab } from "./RssTab";
import type { OwnerTabHost } from "./host";
import { t } from "../../lib/i18n";

// A not-yet-started subscription's RSS tab is a test bench: the same workspace as the draft bench,
// reading the server-composed read model per instance (owner / owner_draft with feed identity),
// expanding seeds on demand, with no download, refresh or scan entries.

vi.mock("../../lib/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/api/client")>();
  return {
    ...actual,
    readProviderEntryRSS: vi.fn(),
    previewProviderEntryRSS: vi.fn(),
    readAnimeRSSListView: vi.fn(),
    expandSnapshotSeed: vi.fn(),
    refreshRSSSource: vi.fn(),
    retryRelease: vi.fn(),
    submitReconciliationScan: vi.fn(),
    fetchOwnerCustomSnapshot: vi.fn(),
  };
});
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() } }));
const mockedView = vi.mocked(readAnimeRSSListView);
const mockedRead = vi.mocked(readProviderEntryRSS);
const mockedPreview = vi.mocked(previewProviderEntryRSS);
const mockedExpand = vi.mocked(expandSnapshotSeed);
const mockedOwnerFetch = vi.mocked(fetchOwnerCustomSnapshot);

function pendingHost(origin: "mikan" | "custom" = "mikan"): OwnerTabHost {
  const workspace = {
    managed_anime: { id: "anime-1", title: "测试番", title_override: null, version: 1 },
    subscription: {
      id: "sub-1", managed_anime_id: "anime-1", origin, origin_entry_ref: origin === "custom" ? null : "entry-1",
      instance_label: null, state: "pending", lifecycle_state: "active", lifecycle_epoch: 1, version: 2,
    },
    subgroups: { managed_anime_id: "anime-1", subgroups: { max_rss: 3, entries: [] }, effective_selection: [], rss_sources: [], version: 3 },
    rules: { version: 4 },
    rss_sources: [],
    rss_source_runtimes: [],
    releases: [],
  } as unknown as AnimeWorkspace;
  return { mode: "owner", animeID: "anime-1", workspace, capability: workspaceCapability("active"), onChanged: vi.fn() };
}

function benchView(): RSSListView {
  return {
    mode: "owner",
    config_identity: "owner_bench:anime-1@2.3.4",
    metadata_hold: { held: false, missing: [] },
    versions: { parser: "p", evaluator: "e", renderer: "1", safety: "s", torrent_parser: "t" },
    generated_at: "2026-10-19T10:00:00Z",
    unused_feed_identities: [],
    truncated: false,
    limit_per_group: 100,
    groups: [
      {
        scope: { provider: "mikan", provider_entry_ref: "entry-1", provider_group_ref: "g-1" },
        display_name: "一号组",
        score: 80,
        selection: { effective_selected: true, selection_mode: "auto" },
        offset: { value: 0, editable: false },
        feed: { state: "fresh", snapshot_id: "snap-1", snapshot_version: 1, content_hash: "sha256:" + "b".repeat(64) },
        items: [
          {
            snapshot_id: "snap-1",
            stable_seed_key: "seed-01",
            seed: { title: "[一号组] 测试番 - 01", published_at: "2026-10-01T00:00:00Z", locator_display: "https://torrent.example.test/01.torrent" },
            torrent: { state: "pending" },
            evaluation: { state: "evaluated", verdict: "accepted_pending_download", reason_code: "accepted", parsed_facts: { episode: 1 } },
            files: [],
            download_action: { allowed: false, kind: "none", reason_code: "not_started" },
          },
        ],
      },
    ],
  } as unknown as RSSListView;
}

function renderBench(host: OwnerTabHost, rulesPreviewSections?: RulesSections) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const ui = (sections?: RulesSections) => (
    <QueryClientProvider client={client}>
      <RssTab host={host} rulesPreviewSections={sections} onExitRulesPreview={() => {}} active />
    </QueryClientProvider>
  );
  const view = render(ui(rulesPreviewSections));
  return { rerender: (sections?: RulesSections) => view.rerender(ui(sections)) };
}

describe("未开始订阅的 RSS 测试台", () => {
  beforeEach(() => {
    mockedView.mockReset();
    mockedRead.mockReset();
    mockedPreview.mockReset();
    mockedExpand.mockReset();
    mockedOwnerFetch.mockReset();
  });

  it("按实例读取测试台读模型，按需展开种子，没有下载、刷新或扫描入口", async () => {
    mockedView.mockResolvedValue(benchView());
    mockedExpand.mockResolvedValue({ state: "available" } as never);
    renderBench(pendingHost());

    // The not-started note is one line at the dialog's top; the bench itself has only a HelpTip.
    expect(screen.getByRole("button", { name: t("rss.workspaceHelpLabel") })).toBeInTheDocument();
    await waitFor(() => expect(mockedView).toHaveBeenCalledTimes(1));
    expect(mockedView.mock.calls[0]![0]).toMatchObject({ input_source: "owner", managed_anime_id: "anime-1", feed: [] });
    expect(mockedView.mock.calls[0]![0]).not.toHaveProperty("draft");
    await screen.findByText("一号组");

    expect(screen.queryByRole("button", { name: new RegExp(t("rss.refresh")) })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: new RegExp(t("rss.scan")) })).not.toBeInTheDocument();

    // A seed's torrent expands on demand: one fetch at click time, then a re-read.
    fireEvent.click(screen.getByRole("button", { name: /展开/ }));
    fireEvent.click(await screen.findByRole("button", { name: t("rss.expandFiles") }));
    fireEvent.click(await screen.findByRole("button", { name: t("rss.expandSeed") }));
    await waitFor(() => expect(mockedExpand).toHaveBeenCalledWith("snap-1", "seed-01", expect.objectContaining({ provider: "mikan", provider_group_ref: "g-1" })));
    await waitFor(() => expect(mockedView).toHaveBeenCalledTimes(2));

    expect(vi.mocked(refreshRSSSource)).not.toHaveBeenCalled();
    expect(vi.mocked(retryRelease)).not.toHaveBeenCalled();
    expect(vi.mocked(submitReconciliationScan)).not.toHaveBeenCalled();
  });

  it("未保存的规则修改随读取提交（owner_draft），配置变化后标为过时", async () => {
    mockedView.mockResolvedValue(benchView());
    const { rerender } = renderBench(pendingHost());
    await screen.findByText("一号组");

    const sections = { release_filter: { mode: "replace", value: { must_contain: "", not_in: "SP" } } } as unknown as RulesSections;
    rerender(sections);
    expect(screen.getByText(t("rss.pendingBenchPreview"))).toBeInTheDocument();
    expect(await screen.findByText(t("rss.configChanged"))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("rss.preview") }));
    await waitFor(() => expect(mockedView).toHaveBeenCalledTimes(2));
    expect(mockedView.mock.calls[1]![0]).toMatchObject({ input_source: "owner_draft", managed_anime_id: "anime-1", rules_preview: sections });
  });

  it("未保存的元数据随读取提交（owner_draft metadata_preview），不需要规则预览", async () => {
    mockedView.mockResolvedValue(benchView());
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const host = pendingHost();
    const ui = (metadataPreview?: AnimeMetadataPreview) => (
      <QueryClientProvider client={client}>
        <RssTab host={host} metadataPreview={metadataPreview} onExitRulesPreview={() => {}} active />
      </QueryClientProvider>
    );
    const { rerender } = render(ui());
    await screen.findByText("一号组");
    const metadata: AnimeMetadataPreview = {
      bgm: { work_id: "bgm-1" },
      tmdb: { work_id: "265010", media_kind: "tv", season: 3 },
      year: 2031,
      title_override: null,
      media_kind_override: null,
      total_episodes: null,
    };
    rerender(ui(metadata));
    expect(screen.getByText(t("rss.pendingBenchPreviewEdits"))).toBeInTheDocument();
    // With only metadata changes there is no "exit preview": the preview ends only after saving or discarding.
    expect(screen.queryByRole("button", { name: t("rss.exitPreview") })).not.toBeInTheDocument();
    expect(await screen.findByText(t("rss.configChanged"))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("rss.preview") }));
    await waitFor(() => expect(mockedView).toHaveBeenCalledTimes(2));
    expect(mockedView.mock.calls[1]![0]).toMatchObject({ input_source: "owner_draft", managed_anime_id: "anime-1", metadata_preview: metadata });
    expect(mockedView.mock.calls[1]![0]).not.toHaveProperty("rules_preview");
  });

  it("未开始的 custom 订阅同样是测试台：缺快照时按订阅抓取一次源快照，再带身份重读", async () => {
    const customScope = { provider: "custom", provider_entry_ref: "custom-source:abc", provider_group_ref: "custom-group:abc" };
    const required = benchView();
    required.groups = [{ ...required.groups[0]!, scope: customScope, display_name: "自定义源", feed: { state: "snapshot_required" }, items: [] } as never];
    const fetched = benchView();
    fetched.groups = [{ ...fetched.groups[0]!, scope: customScope, display_name: "自定义源" } as never];
    mockedView.mockResolvedValueOnce(required).mockResolvedValue(fetched);
    mockedOwnerFetch.mockResolvedValue({
      provider: "custom", provider_entry_ref: "custom-source:abc", group_ref: "custom-group:abc",
      snapshot_id: "snap-c", snapshot_version: 1, content_hash: "sha256:" + "c".repeat(64), freshness: "fresh",
    } as never);
    renderBench(pendingHost("custom"));

    await waitFor(() => expect(mockedView).toHaveBeenCalledTimes(2));
    await screen.findByText("自定义源");
    expect(mockedOwnerFetch).toHaveBeenCalledTimes(1);
    expect(mockedOwnerFetch).toHaveBeenCalledWith("anime-1");
    expect(mockedRead).not.toHaveBeenCalled();
    expect(mockedPreview).not.toHaveBeenCalled();
    expect(mockedView.mock.calls[0]![0]).toMatchObject({ input_source: "owner", managed_anime_id: "anime-1", feed: [] });
    expect(mockedView.mock.calls[1]![0]).toMatchObject({
      input_source: "owner",
      feed: [expect.objectContaining({ provider: "custom", provider_entry_ref: "custom-source:abc", provider_group_ref: "custom-group:abc", snapshot_id: "snap-c" })],
    });
    // The bench has no download, refresh or scan entries.
    expect(screen.queryByRole("button", { name: new RegExp(t("rss.scan")) })).not.toBeInTheDocument();
    expect(vi.mocked(refreshRSSSource)).not.toHaveBeenCalled();
  });
});
