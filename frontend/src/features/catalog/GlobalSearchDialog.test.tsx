import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { CatalogItem, SeedSearchSnapshotSeed, SeedSearchSnapshotView } from "../../lib/api/client";
import { GlobalSearchDialog } from "./GlobalSearchDialog";
import { t } from "../../lib/i18n";
import { formatBytes } from "../../lib/labels";
import { preset as businessPreset } from "../../testing/businessPreset";

// GlobalSearchDialog (the header's global search): two modes over both sites.
//   番剧 mode — the query bar, the result state machine (idle/loading/empty/errors/populated
//   candidate list), a candidate click -> close the dialog and reuse AnimeConfigDialogEntry (the
//   same result->config path as AnimeGrid), and the browse-seeds icon -> the one-shot entry.
//   种子 mode — one POST persists the keyword search snapshot, windows page by snapshot identity +
//   cursor, a stale identity collapses into "search again", and a row's only action is the
//   single-seed one-shot confirmation (SearchSeedOneShotDialog), never a subscription path.
//   The fetch stubs match AnimeGrid.test.tsx's style; the lazily loaded config dialog is warmed at
//   module top level, moving the dialog's dependency-chain import cost out of the assertion wait
//   window (the same approach as TopNav.test.tsx).

await import("../anime-config/AnimeConfigDialogEntry");

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
let expandResult: { status?: number; body?: unknown };
let expandCalls: { url: string; body: unknown }[] = [];
let downloadResult: { status?: number; body?: unknown };
let downloadCalls: { url: string; body: unknown; idempotencyKey: string | null }[] = [];

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

const seedOne: SeedSearchSnapshotSeed = {
  stable_seed_key: "seed-key-1",
  title: "[Example Group] 示例作品 01 [1080p]",
  published_at: "2026-10-24T12:00:00Z",
  download_url: "https://example.invalid/1.torrent",
  size_bytes: 500 * 1024 * 1024,
  torrent_state: "available",
  group_name: "Example Group",
  resolution: "1080p",
};
const seedTwo: SeedSearchSnapshotSeed = { ...seedOne, stable_seed_key: "seed-key-2", title: "[Example Group] 示例作品 02 [1080p]" };
const seedThree: SeedSearchSnapshotSeed = { ...seedOne, stable_seed_key: "seed-key-3", title: "[Example Group] 示例作品 03 [1080p]" };

function seedSnapshotView(overrides: Partial<SeedSearchSnapshotView> = {}): SeedSearchSnapshotView {
  return {
    provider: "anibt",
    scope_ref: "search-scope:0123456789abcdef",
    query_ref: "search-query:fedcba9876543210",
    query: "示例作品",
    snapshot_id: "search-snap-1",
    snapshot_version: 1,
    content_hash: "sha256:0123456789abcdef",
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

const expandAvailable = {
  state: "available",
  torrent: {
    state: "available",
    manifest: {
      payload_digest: "sha256:aaaa",
      parser_version: "v1",
      torrent_version: "v1",
      torrent_name: "示例作品 01",
      total_size_bytes: 2048,
      file_count: 2,
      projection_digest: "sha256:bbbb",
    },
  },
  cached: true,
};

function stubFetch() {
  fetchMock = vi.fn<FetchStub>(async (input, init) => {
    const url = String(input);
    const seedFetchMatch = url.match(/\/catalog\/providers\/([^/]+)\/seed-search-snapshots:fetch/);
    if (seedFetchMatch) {
      seedFetchCalls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
      return new Response(JSON.stringify(seedFetchResult.body ?? {}), {
        status: seedFetchResult.status ?? 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    const seedReadMatch = url.match(/\/catalog\/providers\/([^/]+)\/seed-search-snapshots\/([^/?]+)/);
    if (seedReadMatch) {
      seedReadCalls.push(url);
      return new Response(JSON.stringify(seedReadResult.body ?? {}), {
        status: seedReadResult.status ?? 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (url.match(/\/provider-feed-snapshots\/([^/]+)\/seeds\/([^/]+)\/expand/)) {
      expandCalls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
      return new Response(JSON.stringify(expandResult.body ?? {}), {
        status: expandResult.status ?? 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (url.match(/\/provider-feed-snapshots\/([^/]+)\/seeds\/([^/]+)\/download/)) {
      const headers = init?.headers;
      downloadCalls.push({
        url,
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
        idempotencyKey: headers instanceof Headers ? headers.get("Idempotency-Key") : null,
      });
      return new Response(JSON.stringify(downloadResult.body ?? {}), {
        status: downloadResult.status ?? 200,
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

describe("GlobalSearchDialog（顶栏全局搜索）", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    searchResult = { status: 200, body: [anibtHit] };
    searchCalls = [];
    searchPendingResolve = null;
    suppressions = [];
    seedFetchResult = { status: 200, body: seedSnapshotView() };
    seedFetchCalls = [];
    seedReadResult = { status: 200, body: seedSnapshotView({ seeds: [seedThree], next_cursor: null }) };
    seedReadCalls = [];
    expandResult = { status: 200, body: expandAvailable };
    expandCalls = [];
    downloadResult = { status: 200, body: { one_shot_id: "os-1" } };
    downloadCalls = [];
    stubFetch();
  });

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

  it("节目检索支持 Mikan 站点", async () => {
    renderDialog();
    fireEvent.click(screen.getByRole("radio", { name: "Mikan" }));
    await submitSearch("BanG Dream");

    const url = new URL(searchCalls[0]!, "http://localhost");
    expect(url.pathname).toBe("/api/v1/catalog/providers/mikan/search");
  });

  it("切换站点清空上一站点的结果", async () => {
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
  beforeEach(() => {
    vi.clearAllMocks();
    searchResult = { status: 200, body: [anibtHit] };
    searchCalls = [];
    searchPendingResolve = null;
    suppressions = [];
    seedFetchResult = { status: 200, body: seedSnapshotView() };
    seedFetchCalls = [];
    seedReadResult = { status: 200, body: seedSnapshotView({ seeds: [seedThree], next_cursor: null }) };
    seedReadCalls = [];
    expandResult = { status: 200, body: expandAvailable };
    expandCalls = [];
    downloadResult = { status: 200, body: { one_shot_id: "os-1" } };
    downloadCalls = [];
    stubFetch();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("种子模式：POST 创建搜索快照，渲染标题、元信息、汇总与每行的下载入口", async () => {
    renderDialog();
    switchToSeedMode();
    await submitSeedSearch("示例作品");

    expect(seedFetchCalls).toHaveLength(1);
    expect(seedFetchCalls[0]!.url).toContain("/api/v1/catalog/providers/anibt/seed-search-snapshots:fetch");
    expect(seedFetchCalls[0]!.body).toEqual({ query: "示例作品", limit: 20 });

    expect(await screen.findByText(seedOne.title)).toBeInTheDocument();
    expect(screen.getByText(seedTwo.title)).toBeInTheDocument();
    expect(screen.getByText(t("globalSearch.resultSummary", { n: 3 }))).toBeInTheDocument();
    const rows = screen.getAllByRole("listitem");
    expect(within(rows[0]!).getByRole("button", { name: t("globalSearch.downloadSeed") })).toBeInTheDocument();
    // 元信息：大小与字幕组、分辨率并列展示。
    expect(within(rows[0]!).getByText(new RegExp(`${formatBytes(500 * 1024 * 1024)} · Example Group · 1080p`))).toBeInTheDocument();
    // 搜索行只有一次性下载入口，没有订阅按钮。
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
    expect(url.pathname).toBe("/api/v1/catalog/providers/anibt/seed-search-snapshots/search-snap-1");
    expect(url.searchParams.get("scope_ref")).toBe("search-scope:0123456789abcdef");
    expect(url.searchParams.get("query_ref")).toBe("search-query:fedcba9876543210");
    expect(url.searchParams.get("snapshot_version")).toBe("1");
    expect(url.searchParams.get("content_hash")).toBe("sha256:0123456789abcdef");
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

  it("下载种子：展开校验后确认标题与集数偏移，按快照身份提交一次性下载", async () => {
    renderDialog();
    switchToSeedMode();
    await submitSeedSearch("示例作品");
    await screen.findByText(seedOne.title);

    const rows = screen.getAllByRole("listitem");
    fireEvent.click(within(rows[0]!).getByRole("button", { name: t("globalSearch.downloadSeed") }));

    // 展开请求只携带身份：snapshot_id + stable_seed_key 定位，scope/query 引用原样回传。
    await waitFor(() => expect(expandCalls).toHaveLength(1));
    expect(expandCalls[0]!.url).toContain("/api/v1/provider-feed-snapshots/search-snap-1/seeds/seed-key-1/expand");
    expect(expandCalls[0]!.body).toEqual({
      provider: "anibt",
      provider_entry_ref: "search-scope:0123456789abcdef",
      provider_group_ref: "search-query:fedcba9876543210",
      snapshot_version: 1,
      content_hash: "sha256:0123456789abcdef",
    });

    const dialog = await screen.findByRole("dialog", { name: t("oneshot.searchTitle") });
    expect(within(dialog).getByDisplayValue(seedOne.title)).toBeInTheDocument();
    expect(
      within(dialog).getByText(t("oneshot.searchFiles", { count: 2, size: formatBytes(2048) })),
    ).toBeInTheDocument();
    // 一次性确认没有订阅配置入口。
    expect(within(dialog).queryByRole("tab")).not.toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole("button", { name: t("oneshot.downloadOne") }));
    await waitFor(() => expect(downloadCalls).toHaveLength(1));

    expect(downloadCalls[0]!.url).toContain("/api/v1/provider-feed-snapshots/search-snap-1/seeds/seed-key-1/download");
    const body = downloadCalls[0]!.body as Record<string, unknown>;
    expect(body).toMatchObject({
      provider: "anibt",
      provider_entry_ref: "search-scope:0123456789abcdef",
      provider_group_ref: "search-query:fedcba9876543210",
      snapshot_version: 1,
      content_hash: "sha256:0123456789abcdef",
      input_source: "current_input",
    });
    const draft = body.draft as { origin: string; title: string; instance_label: string; rss_sources: unknown[] };
    expect(draft.origin).toBe("oneshot");
    expect(draft.title).toBe(seedOne.title);
    expect(draft.instance_label).toBe(seedOne.title);
    // 搜索种子不是 RSS 来源：不伪造 locator，draft 的来源列表为空；默认偏移 0 不携带顶层字段。
    expect(draft.rss_sources).toEqual([]);
    expect(body.episode_offset).toBeUndefined();

    // 成功后确认弹窗关闭，搜索列表保留（可继续下载其它行）。
    await waitFor(() => expect(screen.queryByRole("dialog", { name: t("oneshot.searchTitle") })).not.toBeInTheDocument());
    expect(screen.getByText(seedOne.title)).toBeInTheDocument();
  });

  it("展开时快照身份过期：关闭确认弹窗并提示重新搜索", async () => {
    expandResult = { status: 422, body: { code: "SEED_PREVIEW_STALE", message: "过期" } };
    renderDialog();
    switchToSeedMode();
    await submitSeedSearch("示例作品");
    await screen.findByText(seedOne.title);

    const rows = screen.getAllByRole("listitem");
    fireEvent.click(within(rows[0]!).getByRole("button", { name: t("globalSearch.downloadSeed") }));
    await waitFor(() => expect(expandCalls).toHaveLength(1));

    expect(await screen.findByText(t("globalSearch.stale"))).toBeInTheDocument();
    expect(screen.queryByRole("dialog", { name: t("oneshot.searchTitle") })).not.toBeInTheDocument();
  });

  it("偏移留空视为 0：初始为空、不报校验错误，可直接提交且不带顶层字段", async () => {
    renderDialog();
    switchToSeedMode();
    await submitSeedSearch("示例作品");
    await screen.findByText(seedOne.title);
    fireEvent.click(within(screen.getAllByRole("listitem")[0]!).getByRole("button", { name: t("globalSearch.downloadSeed") }));
    const dialog = await screen.findByRole("dialog", { name: t("oneshot.searchTitle") });

    // 检索种子没有基线：偏移初始为空（占位 0），空即无修正，不提示错误。
    // type=number 的空值在 DOM 中读作 null。
    expect(within(dialog).getByLabelText(t("oneshot.offset"))).toHaveValue(null);
    expect(within(dialog).queryByText(t("rss.offsetInvalid"))).not.toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole("button", { name: t("oneshot.downloadOne") }));
    await waitFor(() => expect(downloadCalls).toHaveLength(1));
    expect((downloadCalls[0]!.body as Record<string, unknown>).episode_offset).toBeUndefined();
  });

  it("非零集数偏移走请求顶层字段，不进 draft 的来源列表", async () => {
    renderDialog();
    switchToSeedMode();
    await submitSeedSearch("示例作品");
    await screen.findByText(seedOne.title);
    fireEvent.click(within(screen.getAllByRole("listitem")[0]!).getByRole("button", { name: t("globalSearch.downloadSeed") }));
    const dialog = await screen.findByRole("dialog", { name: t("oneshot.searchTitle") });

    fireEvent.change(within(dialog).getByLabelText(t("oneshot.offset")), { target: { value: "2" } });
    fireEvent.click(within(dialog).getByRole("button", { name: t("oneshot.downloadOne") }));
    await waitFor(() => expect(downloadCalls).toHaveLength(1));

    const body = downloadCalls[0]!.body as Record<string, unknown>;
    expect(body.episode_offset).toBe(2);
    expect((body.draft as { rss_sources: unknown[] }).rss_sources).toEqual([]);
  });

  it("非法集数偏移：显示校验错误并阻止提交", async () => {
    renderDialog();
    switchToSeedMode();
    await submitSeedSearch("示例作品");
    await screen.findByText(seedOne.title);
    fireEvent.click(within(screen.getAllByRole("listitem")[0]!).getByRole("button", { name: t("globalSearch.downloadSeed") }));
    const dialog = await screen.findByRole("dialog", { name: t("oneshot.searchTitle") });

    fireEvent.change(within(dialog).getByLabelText(t("oneshot.offset")), { target: { value: "1001" } });
    expect(await within(dialog).findByText(t("rss.offsetInvalid"))).toBeInTheDocument();
    const submit = within(dialog).getByRole("button", { name: t("oneshot.downloadOne") });
    expect(submit).toBeDisabled();
    fireEvent.click(submit);
    expect(downloadCalls).toHaveLength(0);
  });

  it("失败重试复用同一幂等键；请求体改变后换新键", async () => {
    downloadResult = { status: 500, body: { code: "INTERNAL", message: "失败" } };
    renderDialog();
    switchToSeedMode();
    await submitSeedSearch("示例作品");
    await screen.findByText(seedOne.title);
    fireEvent.click(within(screen.getAllByRole("listitem")[0]!).getByRole("button", { name: t("globalSearch.downloadSeed") }));
    const dialog = await screen.findByRole("dialog", { name: t("oneshot.searchTitle") });

    fireEvent.click(within(dialog).getByRole("button", { name: t("oneshot.downloadOne") }));
    await waitFor(() => expect(downloadCalls).toHaveLength(1));
    const firstKey = downloadCalls[0]!.idempotencyKey;
    expect(firstKey).toBeTruthy();

    // 同一标题与偏移的重试：同一请求体 → 同一幂等键，服务端按它去重。
    fireEvent.click(within(dialog).getByRole("button", { name: t("oneshot.downloadOne") }));
    await waitFor(() => expect(downloadCalls).toHaveLength(2));
    expect(downloadCalls[1]!.idempotencyKey).toBe(firstKey);

    // 标题改变 → 请求体变化 → 新键。
    fireEvent.change(within(dialog).getByDisplayValue(seedOne.title), { target: { value: "改名后" } });
    fireEvent.click(within(dialog).getByRole("button", { name: t("oneshot.downloadOne") }));
    await waitFor(() => expect(downloadCalls).toHaveLength(3));
    expect(downloadCalls[2]!.idempotencyKey).not.toBe(firstKey);
  });

  it("父组件重渲染不会重复展开同一颗种子", async () => {
    renderDialog();
    switchToSeedMode();
    await submitSeedSearch("示例作品");
    await screen.findByText(seedOne.title);
    fireEvent.click(within(screen.getAllByRole("listitem")[0]!).getByRole("button", { name: t("globalSearch.downloadSeed") }));
    await screen.findByRole("dialog", { name: t("oneshot.searchTitle") });
    await waitFor(() => expect(expandCalls).toHaveLength(1));

    // 修改搜索框输入触发父组件重渲染（每次渲染 onStale 都是新函数，但不得重跑展开）。
    fireEvent.change(screen.getByPlaceholderText(t("globalSearch.seedPlaceholder")), { target: { value: "另一个关键词" } });
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(expandCalls).toHaveLength(1);
  });
});
