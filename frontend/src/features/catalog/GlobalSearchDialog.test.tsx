import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { CatalogItem } from "../../lib/api/client";
import { GlobalSearchDialog } from "./GlobalSearchDialog";
import { t } from "../../lib/i18n";
import { preset as businessPreset } from "../../testing/businessPreset";

// GlobalSearchDialog (the header's global search): the query bar (input + search button with one
//   site note beneath, Enter submitting the same as the button), the result state machine
//   (idle/loading/empty/errors including SEARCH_NOT_SUPPORTED/populated candidate list), and a
//   candidate click -> close the dialog and reuse AnimeConfigDialogEntry to open the config dialog
//   (the same result->config path as AnimeGrid).
//   The fetch stubs match AnimeGrid.test.tsx's style; the lazily loaded config dialog is warmed at
//   module top level, moving the dialog's dependency-chain import cost out of the assertion wait
//   window (the same approach as TopNav.test.tsx).

await import("../anime-config/AnimeConfigDialogEntry");

type FetchStub = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

let fetchMock: ReturnType<typeof vi.fn<FetchStub>>;
let searchResult: { status?: number; body?: unknown; pending?: boolean };
let searchCalls: string[] = [];
let suppressions: unknown[] = [];

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

function stubFetch() {
  fetchMock = vi.fn<FetchStub>(async (input) => {
    const url = String(input);
    const searchMatch = url.match(/\/catalog\/providers\/([^/]+)\/search\?q=/);
    if (searchMatch) {
      searchCalls.push(url);
      if (searchResult.pending) return new Promise<Response>(() => {});
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

describe("GlobalSearchDialog（顶栏全局搜索）", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    searchResult = { status: 200, body: [anibtHit] };
    searchCalls = [];
    suppressions = [];
    stubFetch();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("关闭时不渲染弹窗内容", () => {
    renderDialog(false);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("打开后显示标题、检索栏，只有一个站点时写一行说明而不是单选项下拉", () => {
    renderDialog();
    expect(screen.getByRole("dialog", { name: t("nav.search") })).toBeInTheDocument();
    expect(screen.getByPlaceholderText(t("globalSearch.placeholder"))).toBeInTheDocument();
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    expect(screen.getByText(t("search.inSite", { site: "AniBT" }))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("grid.search") })).toBeInTheDocument();
  });

  it("使用 M 档固定高度，输入或出结果时弹窗不跳动", () => {
    renderDialog();
    const className = screen.getByRole("dialog", { name: t("nav.search") }).className;
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

  it("搜索中显示 busy 状态", async () => {
    searchResult = { pending: true };
    renderDialog();
    fireEvent.change(screen.getByPlaceholderText(t("globalSearch.placeholder")), { target: { value: "慢响应" } });
    fireEvent.submit(screen.getByPlaceholderText(t("globalSearch.placeholder")).closest("form")!);

    expect(await screen.findByText(t("grid.searchingNow"))).toBeInTheDocument();
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
//     title (the same AnimeConfigDialogEntry, whose heading accessible name includes sourceLabel —
//     matched by regex).
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
