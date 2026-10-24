import { useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Connection, QBittorrentTorrent } from "../../lib/api/client";
import { QBManagerDialog } from "./QBManagerDialog";
import { t } from "../../lib/i18n";
import { toast } from "sonner";

// The downloader-management dialog: covers task-list rendering, pause/resume actions, empty and error states.

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

type FetchStub = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

let fetchMock: ReturnType<typeof vi.fn<FetchStub>>;

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }),
});

const qbConnection = {
  id: "conn-qb-1",
  kind: "qbittorrent" as const,
  name: "本地 qB",
  enabled: true,
  base_url: "http://qb.local:8080",
  username: "admin",
  credential_configured: true,
  version: 1,
} satisfies Connection;

const qbConnectionTwo = {
  ...qbConnection,
  id: "conn-qb-2",
  name: "远程 qB",
  base_url: "http://qb-remote.local:8080",
} satisfies Connection;

// Fixtures may omit origin: the stub answers it as a subscription task, so the default
// The subscription segment shows them. Segment tests set origin explicitly.
type TorrentFixture = Omit<QBittorrentTorrent, "origin"> & Partial<Pick<QBittorrentTorrent, "origin">>;
const withOrigin = (list: TorrentFixture[]): QBittorrentTorrent[] =>
  list.map((item) => ({ ...item, origin: item.origin ?? "subscription" }));

const torrents: TorrentFixture[] = [
  {
    hash: "abc123",
    name: "番剧 A - 01",
    state: "downloading",
    progress: 0.5,
    size: 1024,
    downloaded: 512,
    dlspeed: 100,
    eta: 5,
    category: "anime",
  },
  {
    hash: "def456",
    name: "番剧 B - 02",
    state: "pausedDL",
    progress: 0,
    size: 2048,
  },
];
const secondConnectionTorrents: TorrentFixture[] = [
  {
    hash: "ghi789",
    name: "番剧 C - 03",
    state: "uploading",
    progress: 1,
    size: 4096,
  },
];

function stubFetch(options: {
  connections?: Connection[];
  torrents?: TorrentFixture[] | (() => TorrentFixture[]);
  deleteError?: { status: number; code: string };
  torrentsByConnection?: Record<string, TorrentFixture[]>;
  torrentError?: boolean;
  connectionError?: boolean;
  status?: unknown;
  statusError?: boolean;
} = {}) {
  const connections = options.connections ?? [qbConnection];
  const torrentList = () => {
    const value = options.torrents ?? torrents;
    return typeof value === "function" ? value() : value;
  };
  fetchMock = vi.fn<FetchStub>(async (input, init) => {
    const url = String(input);
    const connectionID = url.match(/\/config\/connections\/([^/]+)\/qbittorrent\//)?.[1];
    // Order matters: the qBittorrent routes live under /config/connections/{id}/qbittorrent/...,
    // so they must be matched before the generic /config/connections list endpoint below.
    if (url.includes("/qbittorrent/torrents/") && url.includes("/pause")) {
      return new Response(null, { status: 204 });
    }
    if (url.includes("/qbittorrent/torrents/") && url.includes("/resume")) {
      return new Response(null, { status: 204 });
    }
    if (url.includes("/qbittorrent/torrents/") && url.includes("/files")) {
      return new Response(JSON.stringify([
        { name: "file.mkv", size: 1024, progress: 1.0, priority: 7 },
      ]), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    if (url.includes("/qbittorrent/status")) {
      if (options.statusError) {
        return new Response(JSON.stringify({ code: "QB_UNREACHABLE", message: "status unreachable" }), {
          status: 503,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response(JSON.stringify(options.status ?? {
        version: "4.6.5",
        connection_status: "connected",
        dl_speed: 500,
        up_speed: 100,
        free_space: 1073741824,
      }), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    if (url.includes("/qbittorrent/torrents")) {
      if (options.torrentError) {
        return new Response(JSON.stringify({ code: "QB_UNREACHABLE", message: "无法连接" }), {
          status: 503,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (init?.method === "DELETE") {
        if (options.deleteError) {
          return new Response(JSON.stringify({ code: options.deleteError.code, message: "in use" }), {
            status: options.deleteError.status,
            headers: { "Content-Type": "application/json" },
          });
        }
        return new Response(null, { status: 204 });
      }
      if (options.torrentsByConnection && connectionID) {
        return new Response(JSON.stringify(withOrigin(options.torrentsByConnection[connectionID] ?? [])), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response(JSON.stringify(withOrigin(torrentList())), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (url.includes("/config/connections")) {
      if (options.connectionError) {
        return new Response(JSON.stringify({ code: "DATABASE_UNAVAILABLE", message: "connections unavailable" }), {
          status: 503,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response(JSON.stringify(connections), {
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

function renderDialog({ open = true }: { open?: boolean } = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <QBManagerDialog open={open} onOpenChange={() => {}} />
    </QueryClientProvider>,
  );
}

function renderControlledDialog() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  function ControlledDialog() {
    const [open, setOpen] = useState(true);
    return (
      <>
        <button type="button" onClick={() => setOpen(false)}>
          关闭 qB
        </button>
        <QBManagerDialog open={open} onOpenChange={setOpen} />
      </>
    );
  }
  return render(
    <QueryClientProvider client={client}>
      <ControlledDialog />
    </QueryClientProvider>,
  );
}

describe("QBManagerDialog", () => {
  beforeEach(() => {
    localStorage.clear();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("open=false 时不发连接配置请求", async () => {
    stubFetch();
    renderDialog({ open: false });

    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("外层关闭时隐藏删除确认并停止查询", async () => {
    stubFetch();
    renderControlledDialog();

    fireEvent.click(await screen.findByLabelText(t("qb.deleteAria", { name: "番剧 A - 01" })));
    expect(await screen.findByText(t("qb.deleteTitle", { name: "番剧 A - 01" }))).toBeInTheDocument();

    // While the dialog is open the external button is inert/aria-hidden; query with hidden.
    fireEvent.click(screen.getByRole("button", { name: "关闭 qB", hidden: true }));
    await waitFor(() => {
      expect(screen.queryByText(t("qb.deleteTitle", { name: "番剧 A - 01" }))).not.toBeInTheDocument();
    });
  });
  it("展示任务列表与服务器状态", async () => {
    stubFetch();
    renderDialog();

    expect(await screen.findByText("番剧 A - 01")).toBeInTheDocument();
    expect(screen.getByText("番剧 B - 02")).toBeInTheDocument();
    expect(screen.getByText("v4.6.5")).toBeInTheDocument();
    expect(screen.getByText(t("qb.connected"))).toBeInTheDocument();
  });

  it("展示下载进度百分比", async () => {
    stubFetch();
    renderDialog();

    expect(await screen.findByText("50%")).toBeInTheDocument();
    expect(screen.getByText("0%")).toBeInTheDocument();
  });

  it("任务操作按钮保留可触控的最小命中区", async () => {
    stubFetch();
    renderDialog();

    const pauseButton = await screen.findByLabelText(t("qb.pause"));
    const deleteButton = screen.getByLabelText(t("qb.deleteAria", { name: "番剧 A - 01" }));
    const expandButton = screen.getAllByLabelText(t("qb.expandFiles"))[0];

    for (const button of [pauseButton, deleteButton, expandButton]) {
      // Tailwind generates no styles in jsdom; instead assert the utility class for the 44px minimum hit area.
      expect(button).toHaveClass("tw:min-w-11", "tw:min-h-11");
    }
  });

  it("暂停操作调用 pause 端点", async () => {
    stubFetch();
    renderDialog();

    const pauseButton = await screen.findByLabelText(t("qb.pause"));
    fireEvent.click(pauseButton);

    await waitFor(() => {
      const pauses = fetchMock.mock.calls.filter(
        ([input, init]) => String(input).includes("/pause") && init?.method === "POST",
      );
      expect(pauses).toHaveLength(1);
    });
  });

  it("恢复操作调用 resume 端点", async () => {
    stubFetch();
    renderDialog();

    // Waits for the list to render, then finds the second task's (pausedDL) resume button
    await screen.findByText("番剧 B - 02");
    const resumeButton = screen.getByLabelText(t("qb.resume"));
    fireEvent.click(resumeButton);

    await waitFor(() => {
      const resumes = fetchMock.mock.calls.filter(
        ([input, init]) => String(input).includes("/resume") && init?.method === "POST",
      );
      expect(resumes).toHaveLength(1);
    });
  });

  it("展开行显示文件列表", async () => {
    stubFetch();
    renderDialog();

    const expandButtons = await screen.findAllByLabelText(t("qb.expandFiles"));
    fireEvent.click(expandButtons[0]);

    expect(await screen.findByText("file.mkv")).toBeInTheDocument();
  });

  it("任务列表读取失败时展示错误区块", async () => {
    stubFetch({ torrentError: true });
    renderDialog();

    expect(await screen.findByText(/无法连接/)).toBeInTheDocument();
  });

  it("下载器不可达时只报一次错误，重试同时刷新状态与任务", async () => {
    stubFetch({ statusError: true, torrentError: true });
    renderDialog();

    expect(await screen.findByText(t("error.QB_UNREACHABLE"))).toBeInTheDocument();
    await waitFor(() => expect(screen.getAllByText(t("error.QB_UNREACHABLE"))).toHaveLength(1));
    const retries = screen.getAllByRole("button", { name: new RegExp(t("qb.retry")) });
    expect(retries).toHaveLength(1);
    const before = fetchMock.mock.calls.filter(([input]) => String(input).includes("/qbittorrent/status")).length;
    fireEvent.click(retries[0]);
    await waitFor(() =>
      expect(fetchMock.mock.calls.filter(([input]) => String(input).includes("/qbittorrent/status")).length).toBeGreaterThan(before),
    );
  });

  it("连接配置读取失败时展示明确错误和重试", async () => {
    stubFetch({ connectionError: true });
    renderDialog();

    expect(await screen.findByText(t("error.DATABASE_UNAVAILABLE"))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: new RegExp(t("qb.retry")) })).toBeInTheDocument();
    expect(screen.queryByText(t("qb.notConfigured"))).not.toBeInTheDocument();
  });

  it("服务器状态读取失败时展示可见错误", async () => {
    stubFetch({ statusError: true });
    renderDialog();

    expect(await screen.findByText(t("error.QB_UNREACHABLE"))).toBeInTheDocument();
    expect(screen.getByRole("alert")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: new RegExp(t("qb.retry")) })).toBeInTheDocument();
  });

  it("无 qBittorrent 连接时展示空态和“去设置”", async () => {
    stubFetch({ connections: [] });
    const onOpenSettings = vi.fn();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <QBManagerDialog open onOpenChange={() => {}} onOpenSettings={onOpenSettings} />
      </QueryClientProvider>,
    );

    expect(await screen.findByText(t("qb.notConfigured"))).toBeInTheDocument();
    expect(t("qb.notConfigured")).toBe("还没有下载器");
    fireEvent.click(screen.getByRole("button", { name: t("qb.goSettings") }));
    expect(onOpenSettings).toHaveBeenCalledTimes(1);
  });

  it("切换多连接时请求新连接并清除展开内容", async () => {
    stubFetch({
      connections: [qbConnection, qbConnectionTwo],
      torrentsByConnection: {
        [qbConnection.id]: torrents,
        [qbConnectionTwo.id]: secondConnectionTorrents,
      },
    });
    renderDialog();

    await screen.findByText("番剧 A - 01");
    fireEvent.click((await screen.findAllByLabelText(t("qb.expandFiles")))[0]);
    expect(await screen.findByText("file.mkv")).toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getByRole("combobox", { name: t("qb.selectLabel") }));
    // The option shows the downloader's name, not its address.
    await user.click(await screen.findByRole("option", { name: qbConnectionTwo.name }));

    expect(await screen.findByText("番剧 C - 03")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByText("file.mkv")).not.toBeInTheDocument());
    expect(screen.queryByText("番剧 A - 01")).not.toBeInTheDocument();
    expect(
      fetchMock.mock.calls.some(([input, init]) => {
        return String(input).includes(`/config/connections/${qbConnectionTwo.id}/qbittorrent/torrents`) && init?.method === "GET";
      }),
    ).toBe(true);
  });

  it("删除确认默认不删除文件，并发送 delete_files=false", async () => {
    stubFetch();
    renderDialog();

    fireEvent.click(await screen.findByLabelText(t("qb.deleteAria", { name: "番剧 A - 01" })));
    const title = await screen.findByText(t("qb.deleteTitle", { name: "番剧 A - 01" }));
    const confirmation = title.closest<HTMLElement>('[role="dialog"]');
    expect(confirmation).toBeInTheDocument();
    if (!confirmation) throw new Error("delete confirmation dialog did not render");
    fireEvent.click(within(confirmation).getByRole("button", { name: /删\s*除/ }));

    await waitFor(() => {
      const deletes = fetchMock.mock.calls.filter(([input, init]) => {
        return String(input).includes("/qbittorrent/torrents/abc123") && init?.method === "DELETE";
      });
      expect(deletes).toHaveLength(1);
      expect(String(deletes[0][0])).not.toContain("delete_files=true");
    });
  });

  it("删除确认保留 deleteFiles payload，并在取消后重置复选框", async () => {
    stubFetch();
    renderDialog();

    const deleteButton = await screen.findByLabelText(t("qb.deleteAria", { name: "番剧 A - 01" }));
    fireEvent.click(deleteButton);

    const confirmationTitle = screen.getByText(t("qb.deleteTitle", { name: "番剧 A - 01" }));
    const confirmation = confirmationTitle.closest<HTMLElement>('[role="dialog"]');
    expect(confirmation).toBeInTheDocument();
    if (!confirmation) throw new Error("delete confirmation dialog did not render");
    const deleteFilesCheckbox = within(confirmation).getByRole("checkbox", { name: "同时删除已下载的文件" });
    fireEvent.click(deleteFilesCheckbox);
    expect(deleteFilesCheckbox).toBeChecked();

    fireEvent.click(within(confirmation).getByRole("button", { name: /取\s*消/ }));
    await new Promise((resolve) => setTimeout(resolve, 300));

    fireEvent.click(screen.getByLabelText(t("qb.deleteAria", { name: "番剧 A - 01" })));
    const reopenedTitle = (await screen.findAllByText(t("qb.deleteTitle", { name: "番剧 A - 01" }))).at(-1);
    expect(reopenedTitle).toBeDefined();
    if (!reopenedTitle) throw new Error("delete confirmation title did not reappear");
    const reopenedConfirmation = reopenedTitle.closest<HTMLElement>('[role="dialog"]');
    expect(reopenedConfirmation).toBeInTheDocument();
    if (!reopenedConfirmation) throw new Error("delete confirmation dialog did not reopen");
    const reopenedCheckbox = within(reopenedConfirmation).getByRole("checkbox", { name: "同时删除已下载的文件" });
    expect(reopenedCheckbox).not.toBeChecked();
    fireEvent.click(within(reopenedConfirmation).getByRole("checkbox", { name: "同时删除已下载的文件" }));
    fireEvent.click(within(reopenedConfirmation).getByRole("button", { name: /删\s*除/ }));

    await waitFor(() => {
      const deletes = fetchMock.mock.calls.filter(([input, init]) => {
        const url = String(input);
        return url.includes("/qbittorrent/torrents/abc123") && init?.method === "DELETE";
      });
      expect(deletes).toHaveLength(1);
      expect(String(deletes[0][0])).toContain("delete_files=true");
    });
    expect(screen.queryByText(t("qb.deleteTitle", { name: "番剧 A - 01" }))).not.toBeInTheDocument();
  });
  it("版本号已带 v 前缀时不重复，free_space 未知时不显示可用空间", async () => {
    stubFetch({
      status: {
        version: "v4.6.7",
        connection_status: "firewalled",
        dl_speed: 0,
        up_speed: 0,
        free_space: -1,
      },
    });
    renderDialog();

    expect(await screen.findByText("v4.6.7")).toBeInTheDocument();
    expect(screen.getByText(t("qb.connectedFirewalled"))).toBeInTheDocument();
    expect(screen.getByText("↓0 B/s")).toBeInTheDocument();
    expect(screen.getByText("↑0 B/s")).toBeInTheDocument();
    expect(screen.queryByText(/^可用 /)).not.toBeInTheDocument();
    expect(screen.queryByText(/NaN|undefined|-$/)).not.toBeInTheDocument();
  });

  it("只有下载中的任务显示下载速度与剩余时间，做种显示上传速度，未知剩余时间不显示", async () => {
    stubFetch({
      torrents: [
        { hash: "a", name: "任务一", state: "downloading", progress: 0.4, size: 2048, dlspeed: 41000, eta: 834, added_on: 3 },
        { hash: "b", name: "任务二", state: "stalledUP", progress: 1, size: 2048, dlspeed: 1, upspeed: 0, eta: 8640000, added_on: 2 },
        { hash: "c", name: "任务三", state: "uploading", progress: 1, size: 2048, upspeed: 2048, eta: 8640000, added_on: 1 },
        { hash: "d", name: "任务四", state: "pausedDL", progress: 0.1, size: 2048, eta: 8640000, added_on: 0 },
      ],
    });
    renderDialog();

    const list = await screen.findByRole("list", { name: t("qb.taskList") });
    const row = (name: string) => within(list).getByText(name).closest("li") as HTMLElement;
    expect(within(row("任务一")).getByText("↓40.0 KB/s")).toBeInTheDocument();
    expect(within(row("任务一")).getByText(t("qb.remaining", { eta: "13 分 54 秒" }))).toBeInTheDocument();
    expect(within(row("任务二")).queryByText(/^↓|剩余时间|∞/)).not.toBeInTheDocument();
    expect(within(row("任务三")).getByText("↑2.0 KB/s")).toBeInTheDocument();
    expect(within(row("任务四")).queryByText(/剩余时间|∞/)).not.toBeInTheDocument();
    expect(screen.queryByText(/∞/)).not.toBeInTheDocument();
  });

  it("进度向下取整，未完成的任务不会显示 100%", async () => {
    stubFetch({ torrents: [{ hash: "a", name: "番剧 A - 01", state: "downloading", progress: 0.996, size: 1 }] });
    renderDialog();

    expect(await screen.findByText("99%")).toBeInTheDocument();
    expect(screen.queryByText("100%")).not.toBeInTheDocument();
  });

  it("任务按添加时间从新到旧排列", async () => {
    stubFetch({
      torrents: [
        { hash: "a", name: "较早", state: "uploading", progress: 1, size: 1, added_on: 100 },
        { hash: "b", name: "最新", state: "downloading", progress: 0, size: 1, added_on: 300 },
        { hash: "c", name: "中间", state: "uploading", progress: 1, size: 1, added_on: 200 },
      ],
    });
    renderDialog();

    const list = await screen.findByRole("list", { name: t("qb.taskList") });
    await within(list).findByText("最新");
    const names = within(list)
      .getAllByRole("listitem")
      .map((item) => item.querySelector("span[title]")?.getAttribute("title"));
    expect(names).toEqual(["最新", "中间", "较早"]);
  });

  it("qBittorrent 5 的停止、排队与校验状态有对应标签，停止的任务可以恢复", async () => {
    stubFetch({
      torrents: [
        { hash: "a", name: "停止", state: "stoppedDL", progress: 0.2, size: 1, added_on: 3 },
        { hash: "b", name: "排队", state: "queuedDL", progress: 0, size: 1, added_on: 2 },
        { hash: "c", name: "校验", state: "checkingResumeData", progress: 0, size: 1, added_on: 1 },
      ],
    });
    renderDialog();

    const list = await screen.findByRole("list", { name: t("qb.taskList") });
    const row = (name: string) => within(list).getByText(name).closest("li") as HTMLElement;
    await within(list).findByText("停止");
    expect(within(row("停止")).getByText(t("qb.state.paused"))).toBeInTheDocument();
    expect(within(row("停止")).getByRole("button", { name: t("qb.resume") })).toBeInTheDocument();
    expect(within(row("排队")).getByText(t("qb.state.queued"))).toBeInTheDocument();
    expect(within(row("校验")).getByText(t("qb.state.checking"))).toBeInTheDocument();
    expect(screen.queryByText(t("qb.state.error"))).not.toBeInTheDocument();
  });

  it("未知任务状态显示“未知”，原始值放进 tooltip", async () => {
    stubFetch({ torrents: [{ hash: "zzz", name: "番剧 X - 09", state: "weirdState", progress: 0, size: 1 }] });
    renderDialog();

    const tag = await screen.findByText(t("qb.state.unknown"));
    expect(tag).toHaveAttribute("title", "weirdState");
    expect(screen.queryByText("weirdState")).not.toBeInTheDocument();
  });

  it("不再显示运维透传说明和英文 ETA", async () => {
    stubFetch();
    renderDialog();

    await screen.findByText("番剧 A - 01");
    expect(screen.queryByText(/DownloadTask|WebUI API|ETA/)).not.toBeInTheDocument();
    expect(screen.getByText(t("qb.description"))).toBeInTheDocument();
    expect(screen.getAllByText(/^剩余时间 /).length).toBeGreaterThan(0);
  });

  it("订阅或一次性下载占用的任务禁用删除，释放后恢复；占用方与分段不同时才标注", async () => {
    let list: TorrentFixture[] = [
      { ...torrents[0], acquisition_owner: "subscription" },
      { ...torrents[1], acquisition_owner: "one_shot" },
    ];
    stubFetch({ torrents: () => list });
    renderDialog();

    // Both tasks were created by subscriptions: A's holder IS the subscription (no duplicate labeling); B is held by a one-shot download, so it is labeled.
    expect(await screen.findByText(t("qb.owner.one_shot"))).toBeInTheDocument();
    const taskList = screen.getByRole("list", { name: t("qb.taskList") });
    expect(within(taskList).queryByText(t("qb.owner.subscription"))).not.toBeInTheDocument();
    const ownedDelete = screen.getByLabelText(t("qb.deleteAria", { name: "番剧 A - 01" }));
    expect(ownedDelete).toBeDisabled();
    expect(ownedDelete).toHaveAccessibleDescription(t("qb.deleteBlocked"));
    expect(screen.getByLabelText(t("qb.deleteAria", { name: "番剧 B - 02" }))).toBeDisabled();
    expect(screen.getAllByText(t("qb.deleteBlocked"))).toHaveLength(2);

    // After post-processing finishes, the list no longer carries a source and the next poll restores delete.
    list = [{ ...torrents[0] }, { ...torrents[1], acquisition_owner: "one_shot" }];
    await waitFor(
      () => expect(screen.getByLabelText(t("qb.deleteAria", { name: "番剧 A - 01" }))).toBeEnabled(),
      { timeout: 5000 },
    );
    expect(within(screen.getByRole("list", { name: t("qb.taskList") })).queryByText(t("qb.owner.subscription"))).not.toBeInTheDocument();
    expect(screen.getByLabelText(t("qb.deleteAria", { name: "番剧 B - 02" }))).toBeDisabled();
    expect(screen.getAllByText(t("qb.deleteBlocked"))).toHaveLength(1);
  }, 10000);

  it("删除时被占用（409）提示原因并刷新为禁用状态", async () => {
    let list: TorrentFixture[] = torrents;
    stubFetch({ torrents: () => list, deleteError: { status: 409, code: "QB_TORRENT_IN_USE" } });
    renderDialog();

    fireEvent.click(await screen.findByLabelText(t("qb.deleteAria", { name: "番剧 A - 01" })));
    const title = await screen.findByText(t("qb.deleteTitle", { name: "番剧 A - 01" }));
    const confirmation = title.closest<HTMLElement>('[role="dialog"]');
    if (!confirmation) throw new Error("delete confirmation dialog did not render");
    list = [{ ...torrents[0], acquisition_owner: "one_shot" }, torrents[1]];
    fireEvent.click(within(confirmation).getByRole("button", { name: /删\s*除/ }));

    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect(String(vi.mocked(toast.error).mock.calls[0][0])).toBe(t("error.QB_TORRENT_IN_USE"));
    // The list refreshes immediately after an error: delete inside the confirm dialog is disabled, with a reason given.
    await waitFor(() => expect(within(confirmation).getByRole("button", { name: /删\s*除/ })).toBeDisabled());
    expect(within(confirmation).getByText(t("qb.deleteBlocked"))).toBeInTheDocument();
    expect(await screen.findByText(t("qb.owner.one_shot"))).toBeInTheDocument();
  });
});

describe("QBManagerDialog segments", () => {
  beforeEach(() => {
    localStorage.clear();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  const mixed: TorrentFixture[] = [
    { hash: "s1", name: "订阅任务一", state: "uploading", progress: 1, size: 1, origin: "subscription", added_on: 5 },
    { hash: "s2", name: "订阅任务二", state: "downloading", progress: 0.2, size: 1, origin: "subscription", added_on: 4 },
    { hash: "o1", name: "一次性任务", state: "uploading", progress: 1, size: 1, origin: "one_shot", added_on: 3 },
    { hash: "u1", name: "记录已清理的任务", state: "uploading", progress: 1, size: 1, origin: "unknown", added_on: 2 },
    { hash: "e1", name: "手动添加的任务", state: "pausedUP", progress: 1, size: 1, origin: "external", added_on: 1 },
  ];

  function segmentRadio(segment: "subscription" | "one_shot" | "other") {
    return screen.getByRole("radio", { name: new RegExp(`^${t(`qb.segment.${segment}`)}`) });
  }

  it("按服务端给出的来源分段，每段显示计数；来源不明的任务放在「其他」并标注", async () => {
    stubFetch({ torrents: mixed });
    const user = userEvent.setup();
    renderDialog();

    const list = await screen.findByRole("list", { name: t("qb.taskList") });
    expect(within(list).getAllByRole("listitem")).toHaveLength(2);
    expect(within(list).getByText("订阅任务一")).toBeInTheDocument();
    expect(segmentRadio("subscription")).toHaveAccessibleName(`${t("qb.segment.subscription")} 2`);
    expect(segmentRadio("one_shot")).toHaveAccessibleName(`${t("qb.segment.one_shot")} 1`);
    expect(segmentRadio("other")).toHaveAccessibleName(`${t("qb.segment.other")} 2`);
    expect(segmentRadio("subscription")).toBeChecked();

    await user.click(segmentRadio("one_shot"));
    expect(within(screen.getByRole("list", { name: t("qb.taskList") })).getAllByRole("listitem")).toHaveLength(1);
    expect(screen.getByText("一次性任务")).toBeInTheDocument();

    await user.click(segmentRadio("other"));
    const other = screen.getByRole("list", { name: t("qb.taskList") });
    expect(within(other).getAllByRole("listitem")).toHaveLength(2);
    const unknownRow = within(other).getByText("记录已清理的任务").closest("li") as HTMLElement;
    expect(within(unknownRow).getByText(t("qb.originUnknown"))).toHaveAttribute("title", t("qb.originUnknownHint"));
    const externalRow = within(other).getByText("手动添加的任务").closest("li") as HTMLElement;
    expect(within(externalRow).queryByText(t("qb.originUnknown"))).not.toBeInTheDocument();
  });

  it("空分段显示说明，选择记在本浏览器里", async () => {
    stubFetch({ torrents: mixed.filter((item) => item.origin === "subscription") });
    const user = userEvent.setup();
    const { unmount } = renderDialog();

    await screen.findByText("订阅任务一");
    await user.click(segmentRadio("one_shot"));
    expect(screen.getByText(t("qb.segmentEmpty.one_shot"))).toBeInTheDocument();
    expect(screen.getByText(t("qb.segmentEmpty.one_shotHint"))).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: t("qb.taskList") })).not.toBeInTheDocument();
    unmount();

    renderDialog();
    await waitFor(() => expect(segmentRadio("one_shot")).toBeChecked());
    expect(screen.getByText(t("qb.segmentEmpty.one_shot"))).toBeInTheDocument();
  });

  it("链接里的分段优先于记住的选择，切换时写回链接", async () => {
    localStorage.setItem("bansoko.qb.segment", "one_shot");
    stubFetch({ torrents: mixed });
    const onSegmentChange = vi.fn();
    const user = userEvent.setup();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <QBManagerDialog open onOpenChange={() => {}} segment="other" onSegmentChange={onSegmentChange} />
      </QueryClientProvider>,
    );

    await screen.findByText("手动添加的任务");
    expect(segmentRadio("other")).toBeChecked();
    await user.click(segmentRadio("subscription"));
    expect(onSegmentChange).toHaveBeenCalledWith("subscription");
    expect(localStorage.getItem("bansoko.qb.segment")).toBe("subscription");
  });
});
