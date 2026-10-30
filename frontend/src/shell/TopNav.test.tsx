import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Connection, ObservabilityRecord } from "../lib/api/client";
import { useLogDialogStore } from "./logDialogStore";
import { TopNav } from "./TopNav";
import { t } from "../lib/i18n";

vi.mock("../features/qb-manager/QBManagerDialogEntry", () => ({
  QBManagerDialogEntry: ({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) =>
    open ? (
      <div role="dialog">
        <button type="button" aria-label="Close" onClick={() => onOpenChange(false)}>
          Close
        </button>
        <span>顶栏测试任务</span>
      </div>
    ) : null,
}));

vi.mock("./LogDialogEntry", () => ({
  LogDialogEntry: ({
    open,
    onOpenChange,
    view,
    onViewChange,
  }: {
    open: boolean;
    onOpenChange: (open: boolean) => void;
    view?: string;
    onViewChange?: (view: string) => void;
  }) => (
    <div data-testid="mock-log-entry" data-open={String(open)} data-view={view}>
      {open ? (
        <div role="dialog">
          <span>顶栏日志内容</span>
          <button type="button" aria-label="关闭日志" onClick={() => onOpenChange(false)}>
            关闭
          </button>
          <button type="button" onClick={() => onViewChange?.("diagnostics")}>
            切到诊断
          </button>
        </div>
      ) : null}
    </div>
  ),
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

const routineRecord: ObservabilityRecord = {
  record_type: "routine",
  routine: {
    id: "routine-1",
    kind: "catalog_refresh",
    scope_kind: "provider",
    scope_id: "mikan",
    outcome: "success",
    last_executed_at: "2026-07-21T08:00:00Z",
    last_result_code: "ok",
    summary: "刷新完成",
    updated_at: "2026-07-21T08:00:00Z",
  },
};

const businessRecord: ObservabilityRecord = {
  record_type: "business",
  entry: {
    id: "business-1",
    subscription_id: "subscription-1",
    managed_anime_id: "anime-1",
    kind: "subscription_created",
    outcome: "success",
    occurred_at: "2026-07-21T08:00:00Z",
    anime_title: "Example",
    source: "subscription",
    message_code: "subscription_created",
    message_params: {},
    dedupe_key: "dedupe-1",
    retention_until: "2026-07-28T08:00:00Z",
  },
};

const malformedAutomationRecord = {
  record_type: "automation",
} as unknown as ObservabilityRecord;

let fetchMock: ReturnType<typeof vi.fn>;

function stubFetch({
  records = [routineRecord, businessRecord],
  connections = [],
}: {
  records?: ObservabilityRecord[];
  connections?: Connection[];
} = {}) {
  fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    let body: unknown = [];
    if (url.includes("/observability/records")) body = { records, next_cursor: null };
    else if (url.includes("/acquisitions/acq-1")) body = recordDetail;
    else if (url.includes("/acquisitions")) body = { as_of: "2026-10-11T00:00:00Z", items: [recordSummary] };
    else if (url.includes("/observability/diagnostics")) body = { episodes: [], next_cursor: null };
    else if (url.endsWith("/config/connections")) body = connections;
    else if (url.includes("/qbittorrent/status")) {
      body = { version: "4.6.5", connection_status: "connected", dl_speed: 0, up_speed: 0, free_space: 1024 };
    } else if (url.includes("/qbittorrent/torrents")) {
      body = [{ hash: "abc123", name: "顶栏测试任务", state: "downloading", progress: 0.5, size: 1024 }];
    }
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
}

function renderTopNav(initialEntries: string[] = ["/"]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createMemoryRouter([{ path: "*", element: <TopNav username="测试用户" onLogout={() => {}} /> }], {
    initialEntries,
    initialIndex: initialEntries.length - 1,
  });
  const rendered = render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { ...rendered, router };
}

const recordSummary = {
  acquisition_id: "acq-1",
  entry_kind: "automatic",
  state: "succeeded",
  record_detail: "full",
  accepted_at: "2026-10-11T00:00:00Z",
  source_title: { state: "visible", value: "顶栏记录种子" },
};

const recordDetail = {
  ...recordSummary,
  execution_branch: "download",
  hidden: false,
  as_of: "2026-10-11T00:00:00Z",
  stages: [],
  files_available: false,
};

const qbConnection: Connection = {
  id: "conn-qb-1",
  kind: "qbittorrent",
  name: "本地 qB",
  enabled: true,
  base_url: "http://qb.local:8080",
  username: "admin",
  credential_configured: true,
  version: 1,
};

describe("TopNav 术语（R10）", () => {
  afterEach(() => {
    useLogDialogStore.getState().closeDialog();
    vi.unstubAllGlobals();
  });

  it("执行记录在顶栏平铺；文件管理与隐藏的番剧没有顶栏按钮", async () => {
    stubFetch();
    renderTopNav();

    // Flat on the top bar: execution records is a direct button; the former
    // "tools" menu no longer exists, the file manager is hidden in this
    // version and hidden anime live in Settings — none takes a top-bar button.
    expect(screen.queryByRole("button", { name: t("nav.tools") })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("nav.fileManager") })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("nav.records") })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("settings.nav.hidden") })).not.toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "回收站" })).not.toBeInTheDocument();
  });

  it("顶栏图标不带 ↗ 角标；日志和执行记录带文字标签（宽屏显示）", async () => {
    stubFetch();
    const { container } = renderTopNav();
    await screen.findByRole("button", { name: t("nav.log") });
    expect(container.querySelector(".top-nav-opn, .top-nav-search-opn, .tabbar-opn")).toBeNull();
    expect(container.querySelectorAll("header svg.lucide-arrow-up-right")).toHaveLength(0);
    expect(screen.getByRole("button", { name: t("nav.log") })).toHaveTextContent(t("nav.log"));
    expect(screen.getByRole("button", { name: t("nav.records") })).toHaveTextContent(t("nav.records"));
  });

  it("旧的 ?tool=blacklist 链接不再打开任何弹窗，回落到目录底座", async () => {
    stubFetch();
    const { router } = renderTopNav(["/?source=mikan&tool=blacklist"]);
    await waitFor(() => expect(router.state.location.search).toBe("?source=mikan"));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("没有下载器时下载器管理入口仍在，打开得到弹窗（空状态在弹窗里）", async () => {
    stubFetch({ connections: [] });
    const { router } = renderTopNav();
    fireEvent.click(await screen.findByRole("button", { name: "下载器管理" }));
    expect(new URLSearchParams(router.state.location.search).get("tool")).toBe("qbittorrent");
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
  });

  it("首次打开 qB 后保留受控弹窗，关闭后隐藏任务内容", async () => {
    stubFetch({
      connections: [
        {
          id: "conn-qb-1",
          kind: "qbittorrent",
          name: "本地 qB",
          enabled: true,
          base_url: "http://qb.local:8080",
          username: "admin",
          credential_configured: true,
          version: 1,
        },
      ],
    });
    renderTopNav();

    const qbButton = await screen.findByRole("button", { name: "下载器管理" });
    fireEvent.click(qbButton);
    expect(await screen.findByText("顶栏测试任务")).toBeInTheDocument();

    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
  it("首次点击日志才加载入口，关闭后保留挂载但传入 open=false", async () => {
    stubFetch();
    renderTopNav();

    expect(screen.queryByTestId("mock-log-entry")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "日志" }));

    const entry = await screen.findByTestId("mock-log-entry");
    expect(entry).toHaveAttribute("data-open", "true");
    expect(screen.getByText("顶栏日志内容")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "关闭日志" }));
    await waitFor(() => expect(screen.getByTestId("mock-log-entry")).toHaveAttribute("data-open", "false"));
    expect(screen.queryByText("顶栏日志内容")).not.toBeInTheDocument();
  });
  it("未知或不完整的观测记录不应阻断顶栏渲染", async () => {
    stubFetch({ records: [malformedAutomationRecord] });
    renderTopNav();

    expect(screen.getByRole("button", { name: "日志" })).toBeInTheDocument();
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
  });
});

describe("顶栏弹窗由路由承载", () => {
  afterEach(() => {
    useLogDialogStore.getState().closeDialog();
    vi.unstubAllGlobals();
  });

  it("点击入口写入 ?tool=，保留 ?source=；浏览器返回即关闭", async () => {
    stubFetch({ connections: [qbConnection] });
    const { router } = renderTopNav(["/?source=mikan"]);

    fireEvent.click(await screen.findByRole("button", { name: "下载器管理" }));
    expect(new URLSearchParams(router.state.location.search).get("tool")).toBe("qbittorrent");
    expect(router.state.location.pathname).toBe("/");
    expect(new URLSearchParams(router.state.location.search).get("source")).toBe("mikan");

    await act(() => router.navigate(-1));
    await waitFor(() => expect(router.state.location.search).toBe("?source=mikan"));
  });

  it("深链打开工具弹窗；关闭后替换为底座地址并保留 ?source=", async () => {
    stubFetch({ connections: [qbConnection] });
    const { router } = renderTopNav(["/?source=anibt&tool=qbittorrent"]);

    expect(await screen.findByText("顶栏测试任务")).toBeInTheDocument();
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    // A deep link has no in-app history to go back to: replace the current entry so no forward entry can reopen the dialog.
    expect(router.state.location.search).toBe("?source=anibt");
    expect(router.state.historyAction).toBe("REPLACE");
  });

  it("从目录打开后关闭回到上一条历史（不叠加新条目）", async () => {
    stubFetch();
    const { router } = renderTopNav(["/?source=mikan"]);

    fireEvent.click(screen.getByRole("button", { name: "日志" }));
    const entry = await screen.findByTestId("mock-log-entry");
    expect(entry).toHaveAttribute("data-open", "true");
    expect(entry).toHaveAttribute("data-view", "records");
    expect(new URLSearchParams(router.state.location.search).get("log")).toBe("records");

    fireEvent.click(screen.getByRole("button", { name: "切到诊断" }));
    await waitFor(() => expect(screen.getByTestId("mock-log-entry")).toHaveAttribute("data-view", "diagnostics"));
    expect(new URLSearchParams(router.state.location.search).get("log")).toBe("diagnostics");
    expect(router.state.historyAction).toBe("REPLACE");

    fireEvent.click(screen.getByRole("button", { name: "关闭日志" }));
    await waitFor(() => expect(screen.getByTestId("mock-log-entry")).toHaveAttribute("data-open", "false"));
    expect(router.state.historyAction).toBe("POP");
    expect(router.state.location.search).toBe("?source=mikan");
  });

  it("深链 ?log=diagnostics 直接落在诊断视图", async () => {
    stubFetch();
    renderTopNav(["/?log=diagnostics"]);
    const entry = await screen.findByTestId("mock-log-entry");
    expect(entry).toHaveAttribute("data-open", "true");
    expect(entry).toHaveAttribute("data-view", "diagnostics");
  });

  it("未知工具、未知日志视图的深链被去掉；没有 qB 连接时 ?tool=qbittorrent 仍打开", async () => {
    stubFetch();
    const unknown = renderTopNav(["/?source=mikan&tool=nope"]);
    await waitFor(() => expect(unknown.router.state.location.search).toBe("?source=mikan"));
    unknown.unmount();

    const badLog = renderTopNav(["/?log=everything"]);
    await waitFor(() => expect(badLog.router.state.location.search).toBe(""));
    expect(screen.queryByTestId("mock-log-entry")).not.toBeInTheDocument();
    badLog.unmount();

    // The entry stays and the deep link stays; the dialog gives an empty state with a go-to-settings action.
    const noQb = renderTopNav(["/?tool=qbittorrent"]);
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(noQb.router.state.location.search).toBe("?tool=qbittorrent");
    noQb.unmount();

    // The file manager is hidden in this version: its deep links (with path/file/op) fall back as an unknown tool.
    const files = renderTopNav(["/?source=mikan&tool=files&path=%2Fmedia&file=%2Fmedia%2Fa.mkv&op=match"]);
    await waitFor(() => expect(files.router.state.location.search).toBe("?source=mikan"));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("非目录底座路径上的弹窗参数不生效", async () => {
    stubFetch();
    renderTopNav(["/settings/connections?tool=qbittorrent"]);
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});

describe("顶栏状态条用语", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("状态点弹层：连接名称与启用状态、下一次自动任务都使用统一用语", async () => {
    const rssConnection = { id: "c-rss", kind: "rss_http", name: "RSS", enabled: true, version: 1 } as unknown as Connection;
    const tmdbConnection = { id: "c-tmdb", kind: "tmdb", name: "TMDB", enabled: true, version: 1 } as unknown as Connection;
    const bgmConnection = { id: "c-bgm", kind: "bgm", name: "BGM", enabled: false, version: 1 } as unknown as Connection;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        let body: unknown = [];
        if (url.includes("/observability/records")) body = { records: [], next_cursor: null };
        else if (url.includes("/observability/diagnostics")) body = { episodes: [], next_cursor: null };
        else if (url.includes("/config/connections/c-tmdb/test")) {
          body = { connection_id: "c-tmdb", kind: "tmdb", health: "ready", checked_at: "2026-07-21T08:00:00Z" };
        }
        else if (url.endsWith("/config/connections")) body = [rssConnection, tmdbConnection, bgmConnection];
        else if (url.endsWith("/automation")) {
          body = [{ kind: "group_discovery", enabled: true, next_run_at: "2026-07-21T08:00:00Z" }];
        }
        return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
      }),
    );
    renderTopNav();

    // The status bar lives in the top-bar status dot's popover. Without a
    // usable downloader the dot says "no downloader configured", ahead of
    // "partially disabled".
    const trigger = await screen.findByRole("button", { name: t("nav.statusAria") });
    await waitFor(() => expect(trigger).toHaveTextContent(t("nav.statusNoDownloader")));
    expect(trigger.querySelector(".system-status-dot-warning")).not.toBeNull();
    fireEvent.click(trigger);

    // Seven services listed flat: not configured (qB/AniBT/Mikan/Nyaa/Jackett),
    // disabled (Bangumi), enabled (TMDB) — the three configuration states;
    // enabled connections get a real connection test when the popover opens.
    // RSS access is not a service and does not enter the status.
    expect(await screen.findByText(t("nav.connection.bgm"))).toBeInTheDocument();
    expect(screen.queryByText(t("nav.connection.rss_http"))).not.toBeInTheDocument();
    expect(screen.getByText(t("nav.connection.qbittorrent"))).toBeInTheDocument();
    expect(screen.getByText(t("nav.connection.tmdb"))).toBeInTheDocument();
    expect(screen.getByText(t("common.enabled"))).toBeInTheDocument();
    expect(screen.getByText(t("common.disabled"))).toBeInTheDocument();
    expect(screen.getByText(t("nav.connection.nyaa"))).toBeInTheDocument();
    expect(screen.getByText(t("nav.connection.jackett"))).toBeInTheDocument();
    expect(screen.getAllByText(t("nav.statusNotConfigured")).length).toBe(5);
    // The downloader row carries a go-to-settings action; clicking it lands on the download section (the row itself is the button).
    const qbRow = screen
      .getAllByRole("button", { name: new RegExp(`^${t("nav.connection.qbittorrent")}`) })
      .find((el) => el.className.includes("system-status-row"))!;
    expect(qbRow).toHaveTextContent(t("nav.goSettings"));
    expect(qbRow.querySelector(".system-status-dot-warning")).not.toBeNull();
    // TMDB is enabled: a ready test result shows as reachable.
    expect(await screen.findByText(t("nav.statusReachable"))).toBeInTheDocument();
    // Disabled or unconfigured services get no reachability test.
    expect(screen.queryByText(t("nav.statusUnreachable"))).not.toBeInTheDocument();
    expect(screen.getByText(new RegExp(`^${t("nav.automationKind.group_discovery")} · `))).toBeInTheDocument();
    expect(screen.queryByText(/RSS 网络|节目列表|BGM|已启用 \/ 未启用|未启用/)).not.toBeInTheDocument();
  });

  it("状态点弹层：每行跳到该服务的设置分区", async () => {
    const connections = [
      { id: "c-qb", kind: "qbittorrent", name: "qB", enabled: true, version: 1 },
      { id: "c-tmdb", kind: "tmdb", name: "TMDB", enabled: true, version: 1 },
      { id: "c-anibt", kind: "anibt", name: "AniBT", enabled: true, version: 1 },
      { id: "c-mikan", kind: "mikan", name: "Mikan", enabled: true, version: 1 },
      { id: "c-bgm", kind: "bgm", name: "Bangumi", enabled: true, version: 1 },
    ] as unknown as Connection[];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        let body: unknown = [];
        if (url.includes("/observability/records")) body = { records: [], next_cursor: null };
        else if (url.includes("/observability/diagnostics")) body = { episodes: [], next_cursor: null };
        else if (url.endsWith("/test")) body = { connection_id: "c", kind: "rss_http", health: "ready", checked_at: "2026-07-21T08:00:00Z" };
        else if (url.endsWith("/config/connections")) body = connections;
        else if (url.endsWith("/automation")) body = [];
        return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
      }),
    );
    const { router } = renderTopNav();

    const cases: { label: string; pathname: string; search?: string }[] = [
      { label: t("nav.connection.qbittorrent"), pathname: "/settings/download" },
      { label: t("nav.connection.tmdb"), pathname: "/settings/connections", search: "?connection=c-tmdb" },
      { label: t("nav.connection.anibt"), pathname: "/settings/anibt" },
      { label: t("nav.connection.mikan"), pathname: "/settings/mikan" },
      { label: t("nav.connection.bgm"), pathname: "/settings/connections", search: "?connection=c-bgm" },
    ];
    for (const item of cases) {
      fireEvent.click(await screen.findByRole("button", { name: t("nav.statusAria") }));
      // The service names (AniBT/Mikan) collide with the catalog section buttons; take the rows inside the popover.
      const row = (await screen.findAllByRole("button", { name: new RegExp(`^${item.label}`) }))
        .find((el) => el.className.includes("system-status-row"));
      expect(row, `status row for ${item.label}`).toBeTruthy();
      fireEvent.click(row!);
      await waitFor(() => expect(router.state.location.pathname).toBe(item.pathname));
      if (item.search) expect(router.state.location.search).toBe(item.search);
      await act(() => router.navigate("/"));
    }

    // The next-automation-task row lands on the automation section.
    fireEvent.click(await screen.findByRole("button", { name: t("nav.statusAria") }));
    fireEvent.click(await screen.findByRole("button", { name: new RegExp(t("nav.automationEmpty")) }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/settings/automation"));
  });
});

describe("执行记录由路由承载", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("顶栏入口写入 ?record=list，点击记录原地切到详情，返回列表也不叠加历史", async () => {
    stubFetch();
    const { router } = renderTopNav(["/?source=mikan"]);

    fireEvent.click(screen.getByRole("button", { name: t("nav.records") }));
    const row = await screen.findByText("顶栏记录种子");
    expect(new URLSearchParams(router.state.location.search).get("record")).toBe("list");
    expect(new URLSearchParams(router.state.location.search).get("source")).toBe("mikan");

    fireEvent.click(row);
    expect(await screen.findByRole("button", { name: t("records.allRecords") })).toBeInTheDocument();
    expect(new URLSearchParams(router.state.location.search).get("record")).toBe("acq-1");
    expect(router.state.historyAction).toBe("REPLACE");

    fireEvent.click(screen.getByRole("button", { name: t("records.allRecords") }));
    await waitFor(() => expect(new URLSearchParams(router.state.location.search).get("record")).toBe("list"));

    await act(() => router.navigate(-1));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: t("records.title") })).not.toBeInTheDocument());
    expect(router.state.location.search).toBe("?source=mikan");
  });

  it("深链 ?record=<id> 直接打开详情；只有 record_anime 的地址被去掉", async () => {
    stubFetch();
    const deep = renderTopNav(["/?record=acq-1"]);
    expect(await screen.findByRole("button", { name: t("records.allRecords") })).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes("/acquisitions/acq-1"))).toBe(true);
    deep.unmount();

    const orphan = renderTopNav(["/?record_anime=anime-1"]);
    await waitFor(() => expect(orphan.router.state.location.search).toBe(""));
  });

  it("分段参数只跟随它的弹窗：无效值或没有对应弹窗时被去掉", async () => {
    stubFetch();
    const orphanRecord = renderTopNav(["/?record_segment=one_shot"]);
    await waitFor(() => expect(orphanRecord.router.state.location.search).toBe(""));
    orphanRecord.unmount();

    const badQB = renderTopNav(["/?tool=qbittorrent&qb_segment=everything"]);
    await waitFor(() => expect(badQB.router.state.location.search).toBe(""));
    badQB.unmount();

    const qbOnRecords = renderTopNav(["/?record=list&qb_segment=other"]);
    await waitFor(() => expect(qbOnRecords.router.state.location.search).toBe(""));
  });
});
