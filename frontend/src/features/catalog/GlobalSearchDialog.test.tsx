import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { CatalogItem, RSSListItem, RSSListView, SeedSearchSnapshotSeed, SeedSearchSnapshotView } from "../../lib/api/client";
import { GlobalSearchDialog } from "./GlobalSearchDialog";
import { t } from "../../lib/i18n";
import { formatBytes } from "../../lib/labels";
import { preset as businessPreset } from "../../testing/businessPreset";

// GlobalSearchDialog (the header's global search): two modes over both sites.
//   番剧 mode — the query bar, the result state machine (idle/loading/empty/errors/populated
//   candidate list), a candidate click -> close the dialog and reuse AnimeConfigDialogEntry (the
//   same result->config path as AnimeGrid), and the browse-seeds icon -> the one-shot entry.
//   种子 mode — one POST persists the keyword search snapshot, windows page by snapshot identity +
//   cursor, a stale identity collapses into "search again", and a row click opens the standard
//   two-step OneShotDialog seeded from the search snapshot (never a subscription path, never a
//   simplified form).
//   The fetch stubs match AnimeGrid.test.tsx's style; the lazily loaded dialogs are warmed at
//   module top level, moving the dialog's dependency-chain import cost out of the assertion wait
//   window (the same approach as TopNav.test.tsx).

await import("../anime-config/AnimeConfigDialogEntry");
await import("../oneshot/OneShotDialogEntry");

type FetchStub = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

let fetchMock: ReturnType<typeof vi.fn<FetchStub>>;
let searchResult: { status?: number; body?: unknown; pending?: boolean };
let searchCalls: string[] = [];
// Resolves the in-flight pending program search (set by the fetch stub when searchResult.pending).
let searchPendingResolve: (() => void) | null = null;
let suppressions: unknown[] = [];
let seedFetchResult: { status?: number; body?: unknown };
let seedFetchCalls: { url: string; body: unknown }[] = [];
let seedReadResult: { status?: number; body?: unknown };
let seedReadCalls: string[] = [];
let rssViewResult: { status?: number; body?: unknown };
let rssViewCalls: { url: string; body: unknown }[] = [];

const anibtHit: CatalogItem = {
  kind: "anibt_entry",
  provider: "anibt",
  title: "全局搜索番剧",
  original_title: "Global Search Anime",
  provider_entry_ref: "global-hit-1",
  broadcast_day: "none",
  catalog_state: "present",
  preference: "neutral",
  allowed_actions: ["prepare_subscribe"],
  instances: [],
};

// Search-snapshot fixtures use placeholder names only (never real releases, groups or refs).
const escapeRegExp = (value: string) => value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
const SCOPE_REF = "search-scope:0123456789abcdef";
const QUERY_REF = "search-query:fedcba9876543210";
const CONTENT_HASH = "sha256:00112233445566778899aabb";
const SEARCH_SNAPSHOT_ID = "search-snap-1";

const seedOne: SeedSearchSnapshotSeed = {
  stable_seed_key: "seed-key-1",
  title: "[Example Group] 示例作品 01 [1080p]",
  published_at: "2026-10-24T12:00:00Z",
  download_url: "https://example.invalid/1.torrent",
  size_bytes: 500 * 1024 * 1024,
  torrent_state: "available",
  group_name: "Example Group",
  resolution: "1080p",
  title_hint: "示例作品",
};
const seedTwo: SeedSearchSnapshotSeed = { ...seedOne, stable_seed_key: "seed-key-2", title: "[Example Group] 示例作品 02 [1080p]" };
const seedThree: SeedSearchSnapshotSeed = { ...seedOne, stable_seed_key: "seed-key-3", title: "[Example Group] 示例作品 03 [1080p]" };

function seedSnapshotView(overrides: Partial<SeedSearchSnapshotView> = {}): SeedSearchSnapshotView {
  return {
    provider: "anibt",
    scope_ref: SCOPE_REF,
    query_ref: QUERY_REF,
    query: "示例作品",
    snapshot_id: SEARCH_SNAPSHOT_ID,
    snapshot_version: 1,
    content_hash: CONTENT_HASH,
    fetched_at: "2026-10-24T12:00:00Z",
    fresh_until: "2026-10-25T12:00:00Z",
    freshness: "fresh",
    total_seeds: 3,
    window: 2,
    next_cursor: "cursor-2",
    retention_seconds: 86400,
    seeds: [seedOne, seedTwo],
    ...overrides,
  };
}

/** The read model's projection of the search snapshot: one group named by the placeholder query_ref. */
function searchRssView(): RSSListView {
  const item = (key: string, title: string): RSSListItem => ({
    snapshot_id: SEARCH_SNAPSHOT_ID,
    stable_seed_key: key,
    seed: { title, locator_display: `https://example.invalid/${key}.torrent`, size_bytes: 1024 * 1024 * 700 },
    torrent: { state: "available" },
    evaluation: { state: "evaluated", verdict: "accepted_pending_download" },
    files: [
      {
        file_ref: `${key}-video`,
        ordinal: 0,
        source_path: `[Example Group] ${title}.mkv`,
        filename: `${title}.mkv`,
        media_kind: "video",
        included: true,
        rendered_target: `/media/示例作品/${title}.mkv`,
        safety: "valid",
      },
    ],
    download_action: { allowed: true, kind: "one_shot" },
    library_overlap: { verdict: "absent", present_files: 0, pending_files: 1 },
  });
  return {
    mode: "current_input",
    config_identity: "identity",
    metadata_hold: { held: false, missing: [] },
    versions: { parser: "p", evaluator: "e", renderer: "r", safety: "s", torrent_parser: "t" },
    generated_at: "2026-10-24T12:00:00Z",
    groups: [
      {
        scope: { provider: "anibt", provider_entry_ref: SCOPE_REF, provider_group_ref: QUERY_REF },
        display_name: QUERY_REF,
        selection: { source: "configured", effective_selected: true, selection_mode: "auto", count_in_limit: false },
        offset: { value: 0, editable: true, source: "link" },
        feed: { state: "fresh", snapshot_id: SEARCH_SNAPSHOT_ID, snapshot_version: 1, content_hash: CONTENT_HASH },
        items: [
          item("seed-key-1", "示例作品 - 01"),
          item("seed-key-2", "示例作品 - 02"),
        ],
      },
    ],
    unused_feed_identities: [],
    truncated: false,
    limit_per_group: 100,
  };
}

function stubFetch() {
  fetchMock = vi.fn<FetchStub>(async (input, init) => {
    const url = String(input);
    if (url.match(/\/catalog\/providers\/([^/]+)\/seed-search-snapshots:fetch/)) {
      seedFetchCalls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
      return new Response(JSON.stringify(seedFetchResult.body ?? {}), {
        status: seedFetchResult.status ?? 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (url.match(/\/catalog\/providers\/([^/]+)\/seed-search-snapshots\/([^/?]+)/)) {
      seedReadCalls.push(url);
      return new Response(JSON.stringify(seedReadResult.body ?? {}), {
        status: seedReadResult.status ?? 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (url.endsWith("/anime/rss-list-view")) {
      rssViewCalls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
      return new Response(JSON.stringify(rssViewResult.body ?? searchRssView()), {
        status: rssViewResult.status ?? 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    const searchMatch = url.match(/\/catalog\/providers\/([^/]+)\/search\?q=/);
    if (searchMatch) {
      searchCalls.push(url);
      if (searchResult.pending) {
        // A controllable pending response: the test resolves it to prove the in-flight request still lands.
        return new Promise<Response>((resolvePending) => {
          searchPendingResolve = () =>
            resolvePending(
              new Response(JSON.stringify(searchResult.body ?? []), {
                status: searchResult.status ?? 200,
                headers: { "Content-Type": "application/json" },
              }),
            );
        });
      }
      return new Response(JSON.stringify(searchResult.body ?? []), {
        status: searchResult.status ?? 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (url.endsWith("/catalog/suppressions")) {
      return new Response(JSON.stringify(suppressions), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (url.endsWith("/config/business-preset")) {
      return new Response(JSON.stringify(businessPreset), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    // The rules sections carry the draft's own values (editable), and the naming preview requests the server; the preview result is not of interest here.
    if (url.includes("/template-previews/") || url.includes("/template-catalogs/")) {
      return new Response(JSON.stringify({ code: "IGNORED", message: "ignored" }), { status: 400, headers: { "Content-Type": "application/json" } });
    }
    // The config dialog's draft mode reads the candidate list: an empty wrapper is enough to render the four tabs.
    if (url.includes("/anime-config-candidates")) {
      return new Response(JSON.stringify({ candidates: [] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    return new Response(JSON.stringify([]), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
}

function renderDialog(open = true, onOpenChange: (open: boolean) => void = () => {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <GlobalSearchDialog open={open} onOpenChange={onOpenChange} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

async function submitSearch(keyword: string) {
  fireEvent.change(screen.getByPlaceholderText(t("globalSearch.placeholder")), { target: { value: keyword } });
  fireEvent.submit(screen.getByPlaceholderText(t("globalSearch.placeholder")).closest("form")!);
  await waitFor(() => expect(searchCalls.length).toBeGreaterThan(0));
}

function switchToSeedMode() {
  fireEvent.click(screen.getByRole("radio", { name: t("globalSearch.modeSeed") }));
}

async function submitSeedSearch(keyword: string) {
  fireEvent.change(screen.getByPlaceholderText(t("globalSearch.seedPlaceholder")), { target: { value: keyword } });
  fireEvent.submit(screen.getByPlaceholderText(t("globalSearch.seedPlaceholder")).closest("form")!);
  await waitFor(() => expect(seedFetchCalls.length).toBeGreaterThan(0));
}

function resetFixtures() {
  vi.clearAllMocks();
  searchResult = { status: 200, body: [anibtHit] };
  searchCalls = [];
  searchPendingResolve = null;
  suppressions = [];
  seedFetchResult = { status: 200, body: seedSnapshotView() };
  seedFetchCalls = [];
  seedReadResult = { status: 200, body: seedSnapshotView({ seeds: [seedThree], next_cursor: null }) };
  seedReadCalls = [];
  rssViewResult = { status: 200 };
  rssViewCalls = [];
  stubFetch();
}

describe("GlobalSearchDialog（顶栏全局搜索）", () => {
  beforeEach(resetFixtures);

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("关闭时不渲染弹窗内容", () => {
    renderDialog(false);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("打开后显示标题、模式与站点切换、检索栏", () => {
    renderDialog();
    expect(screen.getByRole("dialog", { name: t("search.title") })).toBeInTheDocument();
    expect(screen.getByPlaceholderText(t("globalSearch.placeholder"))).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: t("globalSearch.modeProgram") })).toBeChecked();
    expect(screen.getByRole("radio", { name: t("globalSearch.modeSeed") })).not.toBeChecked();
    expect(screen.getByRole("radio", { name: "AniBT" })).toBeChecked();
    expect(screen.getByRole("radio", { name: "Mikan" })).not.toBeChecked();
    expect(screen.getByRole("button", { name: t("grid.search") })).toBeInTheDocument();
  });

  it("使用 M 档固定高度，输入或出结果时弹窗不跳动", () => {
    renderDialog();
    const className = screen.getByRole("dialog", { name: t("search.title") }).className;
    expect(className).toContain("tw:h-[min(640px,calc(100dvh-2rem))]");
    expect(className).not.toContain("tw:max-h-[min(640px");
  });

  it("点击关闭按钮回调 onOpenChange(false)", () => {
    const onOpenChange = vi.fn();
    renderDialog(true, onOpenChange);
    fireEvent.click(screen.getByRole("button", { name: t("act.close") }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("以正确 provider 与关键词调用搜索接口（回车提交）", async () => {
    renderDialog();
    await submitSearch("BanG Dream");

    expect(searchCalls).toHaveLength(1);
    const url = new URL(searchCalls[0]!, "http://localhost");
    expect(url.pathname).toBe("/api/v1/catalog/providers/anibt/search");
    expect(url.searchParams.get("q")).toBe("BanG Dream");
  });

  it("番剧检索支持 Mikan 站点", async () => {
    renderDialog();
    fireEvent.click(screen.getByRole("radio", { name: "Mikan" }));
    await submitSearch("BanG Dream");

    const url = new URL(searchCalls[0]!, "http://localhost");
    expect(url.pathname).toBe("/api/v1/catalog/providers/mikan/search");
  });

  it("切换站点或模式清空上一批结果", async () => {
    renderDialog();
    await submitSearch("BanG Dream");
    expect(await screen.findByText("全局搜索番剧")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("radio", { name: "Mikan" }));
    expect(screen.queryByText("全局搜索番剧")).not.toBeInTheDocument();
    expect(screen.getByText(t("globalSearch.start"))).toBeInTheDocument();
  });

  it("搜索中显示 busy 状态", async () => {
    searchResult = { pending: true };
    renderDialog();
    fireEvent.change(screen.getByPlaceholderText(t("globalSearch.placeholder")), { target: { value: "慢响应" } });
    fireEvent.submit(screen.getByPlaceholderText(t("globalSearch.placeholder")).closest("form")!);

    expect(await screen.findByText(t("grid.searchingNow"))).toBeInTheDocument();
  });

  it("在途搜索期间重复提交是无效操作：首个请求完成后结果仍落地", async () => {
    searchResult = { status: 200, body: [anibtHit], pending: true };
    renderDialog();
    const input = screen.getByPlaceholderText(t("globalSearch.placeholder"));
    fireEvent.change(input, { target: { value: "慢响应" } });
    const form = input.closest("form")!;
    fireEvent.submit(form);
    fireEvent.submit(form);

    // 二次提交被 busy 挡住：不重复发请求，也不作废第一个请求的序号。
    expect(searchCalls).toHaveLength(1);
    expect(await screen.findByText(t("grid.searchingNow"))).toBeInTheDocument();

    searchResult = { status: 200, body: [anibtHit] };
    searchPendingResolve!();
    expect(await screen.findByText("全局搜索番剧")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("grid.search") })).toBeInTheDocument();
  });

  it("SEARCH_NOT_SUPPORTED 显示不支持文案，不渲染结果", async () => {
    searchResult = { status: 422, body: { code: "SEARCH_NOT_SUPPORTED", message: "不支持" } };
    renderDialog();
    await submitSearch(t("settings.connections.test"));

    expect(await screen.findByRole("alert")).toHaveTextContent(t("error.SEARCH_NOT_SUPPORTED"));
    expect(screen.queryByText("全局搜索番剧")).not.toBeInTheDocument();
  });

  it("其他错误显示错误信息与重试；重试成功后恢复结果列表", async () => {
    searchResult = { status: 500, body: { code: "INTERNAL", message: "数据库连接中断" } };
    renderDialog();
    await submitSearch(t("settings.connections.test"));
    expect(await screen.findByRole("alert")).toHaveTextContent(t("error.INTERNAL"));

    searchResult = { status: 200, body: [anibtHit] };
    fireEvent.click(screen.getByRole("button", { name: t("grid.retry") }));
    expect(await screen.findByText("全局搜索番剧")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("空结果显示无结果空态", async () => {
    searchResult = { status: 200, body: [] };
    renderDialog();
    await submitSearch("不存在的番剧");

    expect(await screen.findByText(t("search.noMatch"))).toBeInTheDocument();
  });

  it("已隐藏的番剧不出现在搜索结果里", async () => {
    suppressions = [
      {
        id: "sup-1",
        provider: "anibt",
        provider_entry_ref: "global-hit-1",
        title_snapshot: "全局搜索番剧",
        suppressed_at: "2026-10-20T00:00:00Z",
      },
    ];
    renderDialog();
    await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => String(input).endsWith("/catalog/suppressions"))).toBe(true));
    await submitSearch("BanG Dream");

    expect(await screen.findByText(t("search.noMatch"))).toBeInTheDocument();
    expect(screen.queryByText("全局搜索番剧")).not.toBeInTheDocument();
  });

  it("结果是海报卡：标题、原名、整卡打开配置与浏览小图标，没有订阅按钮", async () => {
    renderDialog();
    await submitSearch("BanG Dream");

    expect(await screen.findByText("全局搜索番剧")).toBeInTheDocument();
    expect(screen.getByText("Global Search Anime")).toBeInTheDocument();
    const list = screen.getByRole("list");
    expect(within(list).queryByRole("button", { name: t("anime.subscribe") })).not.toBeInTheDocument();
    expect(within(list).getByRole("button", { name: t("grid.card.openConfig", { title: "全局搜索番剧" }) })).toBeInTheDocument();
    expect(within(list).getByRole("button", { name: t("oneshot.actionFor", { title: "全局搜索番剧" }) })).toBeInTheDocument();
  });

  it("点击候选项：回调关闭搜索弹窗，并复用配置弹窗打开该条目", async () => {
    const onOpenChange = vi.fn();
    renderDialog(true, onOpenChange);
    await submitSearch("BanG Dream");

    // The poster is the open-config click area.
    fireEvent.click(await screen.findByRole("button", { name: t("grid.card.openConfig", { title: "全局搜索番剧" }) }));

    // The search dialog received the close instruction; the config dialog opens headed by the entry
    // title (the same AnimeConfigDialogEntry, whose heading accessible name includes sourceLabel —
    // matched by regex).
    expect(onOpenChange).toHaveBeenCalledWith(false);
    // Beyond the card title the config dialog's own title appears (the stubbed onOpenChange never actually closes the search dialog).
    await waitFor(async () => expect((await screen.findAllByRole("heading", { name: /全局搜索番剧/ })).length).toBeGreaterThan(1));
    expect(await screen.findByRole("tab", { name: t("tabs.names.metadata") })).toBeInTheDocument();
  });

  it("候选行的「浏览种子」：关闭搜索弹窗并打开一次性下载入口，不进订阅配置", async () => {
    const onOpenChange = vi.fn();
    renderDialog(true, onOpenChange);
    await submitSearch("BanG Dream");

    fireEvent.click(await screen.findByRole("button", { name: t("oneshot.actionFor", { title: "全局搜索番剧" }) }));

    expect(onOpenChange).toHaveBeenCalledWith(false);
    // The two-step dialog's first step; the subscription config tabs never appear (the two paths never cross).
    expect(await screen.findByText(t("oneshot.stepSeeds"), { exact: false })).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: t("tabs.names.metadata") })).not.toBeInTheDocument();
  });
});

describe("GlobalSearchDialog 种子检索模式", () => {
  beforeEach(resetFixtures);

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("种子模式：POST 创建搜索快照，渲染标题、元信息、汇总；行内没有订阅入口", async () => {
    renderDialog();
    switchToSeedMode();
    await submitSeedSearch("示例作品");

    expect(seedFetchCalls).toHaveLength(1);
    expect(seedFetchCalls[0]!.url).toContain("/api/v1/catalog/providers/anibt/seed-search-snapshots:fetch");
    expect(seedFetchCalls[0]!.body).toEqual({ query: "示例作品", limit: 20 });

    expect(await screen.findByText(seedOne.title)).toBeInTheDocument();
    expect(screen.getByText(seedTwo.title)).toBeInTheDocument();
    expect(screen.getByText(t("globalSearch.resultSummary", { n: 3 }))).toBeInTheDocument();
    // 元信息：大小与字幕组、分辨率并列展示。
    const row = screen.getByRole("button", { name: new RegExp(escapeRegExp(seedOne.title)) });
    expect(within(row).getByText(new RegExp(`${formatBytes(500 * 1024 * 1024)} · Example Group · 1080p`))).toBeInTheDocument();
    // 搜索行没有订阅按钮。
    expect(screen.queryByRole("button", { name: t("anime.subscribe") })).not.toBeInTheDocument();
  });

  it("种子模式支持 Mikan 站点", async () => {
    renderDialog();
    switchToSeedMode();
    fireEvent.click(screen.getByRole("radio", { name: "Mikan" }));
    await submitSeedSearch("示例作品");

    expect(seedFetchCalls[0]!.url).toContain("/api/v1/catalog/providers/mikan/seed-search-snapshots:fetch");
  });

  it("加载更多：按快照身份与游标读取下一窗并追加；没有下一窗后按钮消失", async () => {
    renderDialog();
    switchToSeedMode();
    await submitSeedSearch("示例作品");
    await screen.findByText(seedOne.title);

    fireEvent.click(screen.getByRole("button", { name: t("globalSearch.loadMore") }));
    await waitFor(() => expect(seedReadCalls).toHaveLength(1));

    const url = new URL(seedReadCalls[0]!, "http://localhost");
    expect(url.pathname).toBe(`/api/v1/catalog/providers/anibt/seed-search-snapshots/${SEARCH_SNAPSHOT_ID}`);
    expect(url.searchParams.get("scope_ref")).toBe(SCOPE_REF);
    expect(url.searchParams.get("query_ref")).toBe(QUERY_REF);
    expect(url.searchParams.get("snapshot_version")).toBe("1");
    expect(url.searchParams.get("content_hash")).toBe(CONTENT_HASH);
    expect(url.searchParams.get("cursor")).toBe("cursor-2");

    expect(await screen.findByText(seedThree.title)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("globalSearch.loadMore") })).not.toBeInTheDocument();
  });

  it("结果达到站点上限时说明窗口边界，不伪装全量分页", async () => {
    seedFetchResult = { status: 200, body: seedSnapshotView({ total_seeds: 100, next_cursor: null }) };
    renderDialog();
    switchToSeedMode();
    await submitSeedSearch("示例作品");

    expect(await screen.findByText(t("globalSearch.seedWindowNote"))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("globalSearch.loadMore") })).not.toBeInTheDocument();
  });

  it("种子搜索失败显示错误与重试", async () => {
    seedFetchResult = { status: 502, body: { code: "SEARCH_UPSTREAM_FAILED", message: "上游不可达" } };
    renderDialog();
    switchToSeedMode();
    await submitSeedSearch("示例作品");

    expect(await screen.findByRole("alert")).toHaveTextContent(t("error.SEARCH_UPSTREAM_FAILED"));

    seedFetchResult = { status: 200, body: seedSnapshotView({ next_cursor: null }) };
    fireEvent.click(screen.getByRole("button", { name: t("grid.retry") }));
    expect(await screen.findByText(seedOne.title)).toBeInTheDocument();
  });

  it("种子搜索空结果显示空态", async () => {
    seedFetchResult = { status: 200, body: seedSnapshotView({ seeds: [], total_seeds: 0, next_cursor: null }) };
    renderDialog();
    switchToSeedMode();
    await submitSeedSearch("不存在的作品");

    expect(await screen.findByText(t("globalSearch.noMatchSeed"))).toBeInTheDocument();
  });

  it("加载更多返回 409：结果列表替换为「重新搜索」提示", async () => {
    seedReadResult = { status: 409, body: { code: "SEARCH_SNAPSHOT_SCOPE_INVALID", message: "过期" } };
    renderDialog();
    switchToSeedMode();
    await submitSeedSearch("示例作品");
    await screen.findByText(seedOne.title);

    fireEvent.click(screen.getByRole("button", { name: t("globalSearch.loadMore") }));

    expect(await screen.findByText(t("globalSearch.stale"))).toBeInTheDocument();
    expect(screen.queryByText(seedOne.title)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("globalSearch.searchAgain") })).toBeInTheDocument();
  });

  it("种子行点击：关闭搜索弹窗，进同一个标准两步弹窗（搜索快照为种子源，点击的种子已预选）", async () => {
    const onOpenChange = vi.fn();
    renderDialog(true, onOpenChange);
    switchToSeedMode();
    await submitSeedSearch("示例作品 字幕组甲 03");
    fireEvent.click(await screen.findByRole("button", { name: new RegExp(escapeRegExp(seedOne.title)) }));

    expect(onOpenChange).toHaveBeenCalledWith(false);

    // 标准两步弹窗的第一步：种子列表来自读模型对这次搜索快照的投影。
    const dialog = await screen.findByRole("dialog", { name: t("oneshot.title") });
    expect(await within(dialog).findByText(t("oneshot.stepSeeds"), { exact: false })).toBeInTheDocument();
    // header 呈现前端自己持有的查询文本与站点，不透传服务端的 query_ref 占位名。
    expect(within(dialog).getAllByText("示例作品 字幕组甲 03").length).toBeGreaterThan(0);
    expect(within(dialog).getByText(/AniBT/)).toBeInTheDocument();
    expect(within(dialog).queryByText(new RegExp(QUERY_REF))).not.toBeInTheDocument();
    // 读模型行渲染出来，点击进来的那颗种子已被勾选。
    const rowTitle = "示例作品 - 01";
    expect(await within(dialog).findByRole("checkbox", { name: rowTitle })).toBeChecked();
    expect(within(dialog).getByRole("checkbox", { name: "示例作品 - 02" })).not.toBeChecked();

    // 读模型请求：搜索身份原样回传（scope/query 引用充当 entry/group 引用），草稿是
    // origin=oneshot 形态（无 RSS 源，标题预填解析出的作品名，instance_label 跟随标题）。
    await waitFor(() => expect(rssViewCalls).toHaveLength(1));
    const body = rssViewCalls[0]!.body as {
      input_source: string;
      one_shot?: boolean;
      feed: { provider: string; provider_entry_ref: string; provider_group_ref: string; snapshot_id: string; snapshot_version: number; content_hash: string }[];
      draft: { origin: string; title: string; instance_label: string | null; rss_sources: unknown[] };
    };
    expect(body.input_source).toBe("current_input");
    expect(body.one_shot).toBe(true);
    expect(body.feed).toEqual([
      {
        provider: "anibt",
        provider_entry_ref: SCOPE_REF,
        provider_group_ref: QUERY_REF,
        snapshot_id: SEARCH_SNAPSHOT_ID,
        snapshot_version: 1,
        content_hash: CONTENT_HASH,
      },
    ]);
    expect(body.draft.origin).toBe("oneshot");
    expect(body.draft.title).toBe(seedOne.title_hint);
    expect(body.draft.instance_label).toBe(seedOne.title_hint);
    expect(body.draft.rss_sources).toEqual([]);

    // 订阅配置的页签从不出现（两条路径不交叉）。
    expect(within(dialog).queryByRole("tab")).not.toBeInTheDocument();
  });

  it("解析不出作品名的种子：标题以查询文本预填，第一次读仍然带非空标题与标签", async () => {
    seedFetchResult = { status: 200, body: seedSnapshotView({ seeds: [{ ...seedOne, title_hint: null }], next_cursor: null }) };
    renderDialog();
    switchToSeedMode();
    await submitSeedSearch("示例作品 字幕组甲");
    fireEvent.click(await screen.findByRole("button", { name: new RegExp(escapeRegExp(seedOne.title)) }));

    await screen.findByRole("dialog", { name: t("oneshot.title") });
    await waitFor(() => expect(rssViewCalls).toHaveLength(1));
    const body = rssViewCalls[0]!.body as { draft: { title: string; instance_label: string | null } };
    expect(body.draft.title).toBe("示例作品 字幕组甲");
    expect(body.draft.instance_label).toBe("示例作品 字幕组甲");
  });
});
