import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AnimeListItem } from "../../lib/api/client";
import { SubscribedManageDialog } from "./SubscribedManageDialog";
import { t } from "../../lib/i18n";

// SubscribedManageDialog（已订阅批量管理）：入口 × 季度列、状态/进度行渲染、
// 选择与批量按钮的启用逻辑、移除的二次确认与逐实例 DELETE。fetch 桩按
// AnimeGrid.test.tsx 的风格；真实作品名 + 构造状态。

type FetchStub = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

let fetchMock: ReturnType<typeof vi.fn<FetchStub>>;
const calls: Array<{ url: string; method: string; body?: unknown }> = [];

function item(overrides: {
  id: string;
  title: string;
  origin: AnimeListItem["subscription"]["origin"];
  entrySeason?: string;
  state?: AnimeListItem["subscription"]["state"];
  instanceLabel?: string | null;
  rssHealth?: "ready" | "degraded" | "failed" | null;
  episodes?: number[];
  total?: number;
  createdAt: string;
}): AnimeListItem {
  return {
    managed_anime: { id: overrides.id, title: overrides.title, version: 1 },
    subscription: {
      id: `sub:${overrides.id}`,
      managed_anime_id: overrides.id,
      created_at: overrides.createdAt,
      origin: overrides.origin,
      origin_entry_ref: overrides.origin === "custom" ? null : `${overrides.origin}:ref`,
      ...(overrides.entrySeason ? { entry_season: overrides.entrySeason } : {}),
      instance_label: overrides.instanceLabel ?? null,
      state: overrides.state ?? "active",
      lifecycle_state: "active",
      lifecycle_epoch: 1,
      version: 7,
      new_releases_only: false,
    },
    metadata: { managed_anime_id: overrides.id, bgm: null, tmdb: null, title_override: null, total_episodes: null, media_kind: null, year: null, season: null, version: 1 },
    metadata_hold: { held: false, missing: [] },
    broadcast_day: "none",
    rss_health: overrides.rssHealth ?? null,
    episode_progress:
      overrides.episodes === undefined
        ? null
        : {
            episodes: overrides.episodes,
            ...(overrides.total != null ? { total: overrides.total } : {}),
            done: overrides.total != null && overrides.episodes.length >= overrides.total,
          },
  };
}

const items: AnimeListItem[] = [
  item({ id: "a1", title: "药屋少女的呢喃 第三季", origin: "anibt", entrySeason: "2026-fall", episodes: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11], total: 24, createdAt: "2026-10-05T00:00:00Z" }),
  item({ id: "m1", title: "落语朱音", origin: "mikan", state: "pending", rssHealth: "failed", createdAt: "2026-10-26T00:00:00Z" }),
  item({ id: "c1", title: "铁旅！meet with 铁道少女", origin: "custom", instanceLabel: "铁道少女", createdAt: "2026-10-15T00:00:00Z" }),
];

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function stubFetch() {
  fetchMock = vi.fn<FetchStub>(async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    calls.push({ url, method, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    if (method === "GET" && /\/anime$/.test(url)) return json(items);
    if (url.includes("/subscription")) {
      if (method === "DELETE") return json({});
      return json(items[0].subscription);
    }
    return json([]);
  });
  vi.stubGlobal("fetch", fetchMock);
}

function renderDialog(onMutated: () => void = () => {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SubscribedManageDialog onClose={() => {}} onMutated={onMutated} />
    </QueryClientProvider>,
  );
}

describe("SubscribedManageDialog（已订阅批量管理）", () => {
  beforeEach(() => {
    calls.length = 0;
    stubFetch();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("渲染实例行：入口、冻结季度、状态、RSS 与进度", async () => {
    renderDialog();
    expect(await screen.findByRole("dialog", { name: t("grid.manage.title") })).toBeInTheDocument();

    // 等到行真正渲染（不只是对话框挂载）。
    const table = await screen.findByRole("table");
    const row = within(table).getByText("药屋少女的呢喃 第三季");
    expect(row).toBeInTheDocument();
    expect(within(table).getAllByText("AniBT").length).toBe(1);
    expect(within(table).getAllByText("Mikan").length).toBe(1);
    expect(within(table).getAllByText(t("grid.manage.originCustom")).length).toBe(1);
    // 冻结季度：浏览创建显示 2026 秋；搜索创建与自定义显示 —。
    expect(within(table).getByText("2026 秋")).toBeInTheDocument();
    expect(within(table).getAllByText("—").length).toBeGreaterThanOrEqual(2);
    expect(within(table).getAllByText(t("anime.state.active")).length).toBe(2);
    expect(within(table).getAllByText(t("anime.state.pending")).length).toBe(1);
    expect(within(table).getByText(t("grid.progress.summaryShort", { stored: 11, total: 24 }))).toBeInTheDocument();
    expect(within(table).getByText(t("grid.manage.rssFailed"))).toBeInTheDocument();
  });

  it("未选时批量按钮不可用；全选后可开始/暂停，暂停对纯未开始置灰说明", async () => {
    renderDialog();
    await screen.findByText("落语朱音");
    expect(screen.getByText(t("grid.manage.selNone"))).toBeInTheDocument();
    const start = screen.getByRole("button", { name: t("grid.manage.start") });
    const pause = screen.getByRole("button", { name: t("grid.manage.pause") });
    expect(start).toBeDisabled();
    expect(pause).toBeDisabled();

    fireEvent.click(screen.getByRole("checkbox", { name: t("grid.manage.selectAll") }));
    expect(screen.getByText(t("grid.manage.selInfoStates", { n: 3, active: 2, stopped: 0, pending: 1 }))).toBeInTheDocument();
    expect(start).toBeEnabled();
    expect(pause).toBeEnabled();

    // 只剩未开始的一条（先清除全选，再点它的行）：暂停置灰并给出原因，开始可用。
    fireEvent.click(screen.getByRole("button", { name: t("grid.manage.clearSel") }));
    fireEvent.click(screen.getByText("落语朱音"));
    expect(pause).toBeDisabled();
    expect(pause).toHaveAttribute("title", t("grid.manage.pauseBlocked"));
    expect(start).toBeEnabled();
  });

  it("移除先确认再逐实例 DELETE；取消不发出请求", async () => {
    const onMutated = vi.fn();
    renderDialog(onMutated);
    await screen.findByText("落语朱音");

    fireEvent.click(screen.getByText("落语朱音"));
    fireEvent.click(screen.getByRole("button", { name: t("grid.manage.remove") }));
    const confirm = await screen.findByRole("alertdialog", { name: t("grid.manage.removeTitle", { n: 1 }) });
    expect(confirm).toHaveTextContent("落语朱音");
    fireEvent.click(within(confirm).getByRole("button", { name: t("ui.cancel") }));
    expect(calls.filter((call) => call.method === "DELETE")).toHaveLength(0);

    fireEvent.click(screen.getByRole("button", { name: t("grid.manage.remove") }));
    const again = await screen.findByRole("alertdialog", { name: t("grid.manage.removeTitle", { n: 1 }) });
    fireEvent.click(within(again).getByRole("button", { name: t("grid.manage.removeConfirm") }));
    await waitFor(() => expect(calls.filter((call) => call.method === "DELETE")).toHaveLength(1));
    expect(calls.find((call) => call.method === "DELETE")?.body).toEqual({ expected_version: 7 });
    await waitFor(() => expect(onMutated).toHaveBeenCalled());
  });
});
