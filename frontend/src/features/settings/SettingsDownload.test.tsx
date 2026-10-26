import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Connection, NetworkRoute } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { SettingsDownload } from "./SettingsDownload";

// The qBittorrent downloader has moved from service connections into the acquisition section:
//   edit/create/test and the credential three-state (keep/replace/remove) behavior migrated whole,
//   with regression coverage here.

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

type FetchStub = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

let fetchMock: ReturnType<typeof vi.fn<FetchStub>>;

const qbConnection: Connection = {
  id: "qb-1",
  kind: "qbittorrent",
  name: "QB",
  enabled: true,
  base_url: "http://qb.local:8080",
  username: "u",
  credential_configured: true,
  category: "anime",
  user_tags: ["bangumi", "v2"],
  version: 5,
};

function stubFetch(options: { connections?: Connection[]; routes?: NetworkRoute[] } = {}) {
  fetchMock = vi.fn<FetchStub>(async (input, init) => {
    const url = String(input);
    if (init?.method === "PUT" && url.includes("/config/connections/")) {
      const requestBody = JSON.parse(String(init.body)) as { kind: string; name: string; enabled: boolean; base_url?: string };
      const saved: Record<string, unknown> = {
        id: url.slice(url.lastIndexOf("/") + 1),
        kind: requestBody.kind,
        name: requestBody.name,
        enabled: requestBody.enabled,
        version: 2,
      };
      if (requestBody.base_url !== undefined) saved.base_url = requestBody.base_url;
      if (requestBody.kind === "qbittorrent") {
        saved.username = "u";
        saved.credential_configured = true;
      }
      return new Response(JSON.stringify(saved), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    let body: unknown = [];
    if (url.includes("/config/connections")) body = options.connections ?? [];
    else if (url.includes("/config/network-routes")) body = options.routes ?? [];
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
}

function renderSettings() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SettingsDownload />
    </QueryClientProvider>,
  );
}

function connectionPutCalls() {
  return fetchMock.mock.calls.filter(
    ([input, init]) => String(input).includes("/config/connections/") && init?.method === "PUT",
  );
}

function putBody(call: [input: RequestInfo | URL, init?: RequestInit]) {
  return JSON.parse(String(call[1]?.body)) as Record<string, unknown>;
}

describe("SettingsDownload qBittorrent 下载器", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("新建下载器：契约字段齐全，保存 body 带 secret replace", async () => {
    stubFetch({ routes: [{ id: "route-1", kind: "direct", name: "直连出口", enabled: true, version: 1 }] });
    renderSettings();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.download.newDownloader") }));
    const dialog = await screen.findByRole("dialog");

    fireEvent.change(within(dialog).getByLabelText(t("settings.download.name")), { target: { value: "主 qB" } });
    fireEvent.change(within(dialog).getByLabelText(t("settings.download.address")), { target: { value: "http://qb.local:8080" } });
    fireEvent.change(within(dialog).getByLabelText(t("settings.download.username")), { target: { value: "admin" } });
    fireEvent.change(within(dialog).getByLabelText(t("settings.download.downloadDir")), { target: { value: "/downloads" } });
    fireEvent.change(within(dialog).getByLabelText(t("settings.download.localDir")), { target: { value: "/user/downloads" } });
    fireEvent.change(within(dialog).getByLabelText(t("settings.download.category")), { target: { value: "anime" } });
    fireEvent.change(within(dialog).getByLabelText(t("settings.download.tagsAria")), { target: { value: "bangumi" } });
    fireEvent.keyDown(within(dialog).getByLabelText(t("settings.download.tagsAria")), { key: "Enter" });
    fireEvent.change(within(dialog).getByLabelText(t("settings.download.password")), { target: { value: "s3cret" } });
    await userEvent.click(within(dialog).getByLabelText(t("settings.download.proxyLabel")));
    await userEvent.click(await screen.findByRole("option", { name: "直连出口" }));
    fireEvent.click(within(dialog).getByRole("button", { name: t("settings.download.save") }));

    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.download.downloaderSaved")));

    expect(connectionPutCalls()).toHaveLength(1);
    expect(putBody(connectionPutCalls()[0]!)).toMatchObject({
      kind: "qbittorrent",
      name: "主 qB",
      enabled: true,
      base_url: "http://qb.local:8080",
      username: "admin",
      download_root: "/downloads",
      downloader_root: "/user/downloads",
      category: "anime",
      user_tags: ["bangumi"],
      route_id: "route-1",
      secret: { mode: "replace", value: "s3cret" },
    });
  });

  it("编辑下载器：默认 keep，替换组装 replace，且没有移除凭据", async () => {
    stubFetch({ connections: [qbConnection] });
    renderSettings();

    // Keep existing credentials is selected by default; saving directly means keep.
    fireEvent.click(await screen.findByRole("button", { name: t("settings.download.edit") }));
    const firstDialog = await screen.findByRole("dialog");
    expect(within(firstDialog).getByLabelText(t("secret.keep"))).toBeChecked();
    // The saved category/user_tags fill back into the form.
    expect(within(firstDialog).getByDisplayValue("anime")).toBeInTheDocument();
    expect(within(firstDialog).getByText("bangumi")).toBeInTheDocument();
    fireEvent.click(within(firstDialog).getByRole("button", { name: t("settings.download.save") }));

    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.download.downloaderSaved")));
    expect(connectionPutCalls()).toHaveLength(1);
    expect(putBody(connectionPutCalls()[0]!)).toMatchObject({
      kind: "qbittorrent",
      secret: { mode: "keep" },
      category: "anime",
      user_tags: ["bangumi", "v2"],
    });

    // Choosing replace-with-new-value reveals the secret input, meaning replace.
    fireEvent.click(await screen.findByRole("button", { name: t("settings.download.edit") }));
    const secondDialog = await screen.findByRole("dialog");
    fireEvent.click(within(secondDialog).getByLabelText(t("secret.replace")));
    fireEvent.change(within(secondDialog).getByLabelText(t("settings.download.password")), { target: { value: "new-pass" } });
    fireEvent.click(within(secondDialog).getByRole("button", { name: t("settings.download.save") }));
    await waitFor(() => expect(connectionPutCalls()).toHaveLength(2));
    expect(putBody(connectionPutCalls()[1]!)).toMatchObject({
      kind: "qbittorrent",
      secret: { mode: "replace", value: "new-pass" },
    });

    // A downloader cannot run without a password, so there is no remove state.
    fireEvent.click(await screen.findByRole("button", { name: t("settings.download.edit") }));
    const thirdDialog = await screen.findByRole("dialog");
    expect(within(thirdDialog).getByLabelText(t("secret.keep"))).toBeInTheDocument();
    expect(within(thirdDialog).queryByLabelText(t("secret.remove"))).not.toBeInTheDocument();
  });

  it("保存 409 显示 ConflictBlock，刷新载入最新重拉列表并清编辑态", async () => {
    fetchMock = vi.fn<FetchStub>(async (input, init) => {
      const url = String(input);
      if (init?.method === "PUT" && url.includes("/config/connections/")) {
        return new Response(
          JSON.stringify({ code: "VERSION_CONFLICT", message: "版本冲突", current_version: 9, request_id: "r-1" }),
          { status: 409, headers: { "Content-Type": "application/json" } },
        );
      }
      let body: unknown = [];
      if (url.includes("/config/connections")) body = [qbConnection];
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    renderSettings();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.download.edit") }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("settings.download.save") }));

    expect(await screen.findByText(t("common.versionConflict"))).toBeInTheDocument();
    expect(screen.getByText(t("common.versionConflict"))).toBeInTheDocument();

    const connectionGets = () =>
      fetchMock.mock.calls.filter(
        ([input, init]) => String(input).includes("/config/connections") && (!init?.method || init.method === "GET"),
      ).length;
    const getsBefore = connectionGets();
    fireEvent.click(screen.getByRole("button", { name: t("common.refreshLatest") }));

    // The editing state is cleared (the dialog closes) and the list re-fetches.
    await waitFor(() => expect(screen.queryByText(t("settings.download.editDownloader"))).not.toBeInTheDocument());
    await waitFor(() => {
      expect(connectionGets()).toBeGreaterThan(getsBefore);
    });
  });

  it("新建下载器：本机路径的详细说明收进“?”提示，保留前缀给出用户语言提示", async () => {
    stubFetch();
    renderSettings();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.download.newDownloader") }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).queryByText(t("settings.download.localDirHelp"))).not.toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: t("settings.download.localDirHelpLabel") }));
    expect(await screen.findByText(t("settings.download.localDirHelp"))).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");

    fireEvent.change(within(dialog).getByLabelText(t("settings.download.tagsAria")), { target: { value: "bansoko-x" } });
    fireEvent.keyDown(within(dialog).getByLabelText(t("settings.download.tagsAria")), { key: "Enter" });
    expect(await within(dialog).findByText(t("settings.download.tagReserved"))).toBeInTheDocument();
  });

  it("下载器行有「测试」按钮", async () => {
    stubFetch({ connections: [qbConnection] });
    renderSettings();

    expect(await screen.findByText("QB")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("settings.download.test") })).toBeInTheDocument();
  });
});

describe("SettingsDownload 文件效果", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("冲突策略提供契约允许的 fail/versioned/overwrite 三态", async () => {
    fetchMock = vi.fn<FetchStub>(async (input) => {
      const url = String(input);
      let body: unknown = [];
      if (url.includes("/config/global-sections")) {
        body = [{ section_name: "file_effect", schema_version: "1", value: { operation: "copy", collision_policy: "fail" }, version: 1 }];
      }
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    renderSettings();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.download.edit") }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByRole("combobox", { name: t("file.collisionTitle") }));
    const options = await screen.findAllByRole("option");
    expect(options.map((option) => option.textContent)).toEqual([
      t("file.collision.fail"),
      t("file.collision.versioned"),
      t("file.collision.overwrite"),
    ]);
    expect(within(dialog).queryByText(t("file.collisionOverwriteNote"))).toBeNull();
    await userEvent.click(options[2]);
    expect(within(dialog).getByText(t("file.collisionOverwriteNote"))).toBeInTheDocument();
  });
});


async function openNewDownloader() {
  fireEvent.click(await screen.findByRole("button", { name: t("settings.download.newDownloader") }));
  return screen.findByRole("dialog");
}

function fillDownloader(dialog: HTMLElement, overrides: Partial<Record<"name" | "address" | "username" | "password" | "downloadDir" | "localDir", string>> = {}) {
  const values = { name: "主 qB", address: "http://qb.local:8080", username: "admin", password: "pw", downloadDir: "", localDir: "", ...overrides };
  fireEvent.change(within(dialog).getByLabelText(t("settings.download.name")), { target: { value: values.name } });
  fireEvent.change(within(dialog).getByLabelText(t("settings.download.address")), { target: { value: values.address } });
  fireEvent.change(within(dialog).getByLabelText(t("settings.download.username")), { target: { value: values.username } });
  fireEvent.change(within(dialog).getByLabelText(t("settings.download.password")), { target: { value: values.password } });
  fireEvent.change(within(dialog).getByLabelText(t("settings.download.downloadDir")), { target: { value: values.downloadDir } });
  fireEvent.change(within(dialog).getByLabelText(t("settings.download.localDir")), { target: { value: values.localDir } });
}

describe("SettingsDownload 下载器对话框校验与提交", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("用户名为空：禁用保存并提示", async () => {
    stubFetch();
    renderSettings();
    const dialog = await openNewDownloader();
    fillDownloader(dialog, { username: "" });

    expect(within(dialog).getByText(t("settings.download.usernameRequired"))).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: t("settings.download.save") })).toBeDisabled();
  });

  it("地址不是 http/https URL：禁用保存并提示", async () => {
    stubFetch();
    renderSettings();
    const dialog = await openNewDownloader();

    for (const address of ["qb.local:8080", "ftp://qb.local", "http://user:pw@qb.local:8080"]) {
      fillDownloader(dialog, { address });
      expect(within(dialog).getByText(t("settings.download.addressInvalid"))).toBeInTheDocument();
      expect(within(dialog).getByRole("button", { name: t("settings.download.save") })).toBeDisabled();
    }
    fillDownloader(dialog, { address: "https://qb.local:8080" });
    expect(within(dialog).getByRole("button", { name: t("settings.download.save") })).toBeEnabled();
  });

  it("下载目录或访问路径含 ..：禁用保存并提示", async () => {
    stubFetch();
    renderSettings();
    const dialog = await openNewDownloader();

    fillDownloader(dialog, { downloadDir: "/data/../etc" });
    expect(within(dialog).getByText(t("settings.download.pathInvalid"))).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: t("settings.download.save") })).toBeDisabled();

    fillDownloader(dialog, { downloadDir: "/data/a..b", localDir: "..\\x" });
    expect(within(dialog).getByText(t("settings.download.pathInvalid"))).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: t("settings.download.save") })).toBeDisabled();

    fillDownloader(dialog, { downloadDir: "/data/a..b", localDir: "/user/downloads" });
    expect(within(dialog).getByRole("button", { name: t("settings.download.save") })).toBeEnabled();
  });

  it("已有下载器的凭据选择只有保留与替换，没有移除", async () => {
    stubFetch({ connections: [qbConnection] });
    renderSettings();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.download.edit") }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText(t("secret.keep"))).toBeInTheDocument();
    expect(within(dialog).getByLabelText(t("secret.replace"))).toBeInTheDocument();
    expect(within(dialog).queryByLabelText(t("secret.remove"))).not.toBeInTheDocument();
  });

  it("对话框是表单：提交（Enter 触发）在信息不完整时不发请求，完整后才保存", async () => {
    stubFetch();
    renderSettings();
    const dialog = await openNewDownloader();
    const form = document.getElementById("downloader-form") as HTMLFormElement;
    expect(form.tagName).toBe("FORM");
    const save = within(dialog).getByRole("button", { name: t("settings.download.save") });
    expect(save).toHaveAttribute("type", "submit");
    expect(save).toHaveAttribute("form", "downloader-form");

    fillDownloader(dialog, { username: "" });
    fireEvent.submit(form);
    expect(connectionPutCalls()).toHaveLength(0);

    fillDownloader(dialog);
    fireEvent.submit(form);
    await waitFor(() => expect(connectionPutCalls()).toHaveLength(1));
  });

  it("保存进行中：取消与关闭按钮禁用", async () => {
    let release: () => void = () => {};
    stubFetch();
    const base = fetchMock.getMockImplementation()!;
    fetchMock.mockImplementation(async (input, init) => {
      if (init?.method === "PUT") await new Promise<void>((resolve) => { release = resolve; });
      return base(input, init);
    });
    renderSettings();
    const dialog = await openNewDownloader();
    fillDownloader(dialog);

    fireEvent.click(within(dialog).getByRole("button", { name: t("settings.download.save") }));
    await waitFor(() => expect(within(dialog).getByRole("button", { name: t("settings.download.cancel") })).toBeDisabled());
    expect(within(dialog).getByRole("button", { name: t("ui.close") })).toBeDisabled();
    release();
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("保存失败：错误显示在对话框内并滚动到可见处", async () => {
    const scrollIntoView = vi.fn();
    Element.prototype.scrollIntoView = scrollIntoView;
    stubFetch();
    const base = fetchMock.getMockImplementation()!;
    fetchMock.mockImplementation(async (input, init) => {
      if (init?.method === "PUT") {
        return new Response(JSON.stringify({ code: "BAD", message: "连不上", request_id: "r" }), { status: 400, headers: { "Content-Type": "application/json" } });
      }
      return base(input, init);
    });
    renderSettings();
    const dialog = await openNewDownloader();
    fillDownloader(dialog);

    fireEvent.click(within(dialog).getByRole("button", { name: t("settings.download.save") }));
    expect(await within(dialog).findByRole("alert")).toBeInTheDocument();
    expect(scrollIntoView).toHaveBeenCalled();
    delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
  });

  it("字段顺序：名称、地址、用户名、密码、网络代理、下载目录、访问路径、分类、标签、启用", async () => {
    stubFetch();
    renderSettings();
    const dialog = await openNewDownloader();

    const order = [
      within(dialog).getByLabelText(t("settings.download.name")),
      within(dialog).getByLabelText(t("settings.download.address")),
      within(dialog).getByLabelText(t("settings.download.username")),
      within(dialog).getByLabelText(t("settings.download.password")),
      within(dialog).getByRole("combobox", { name: t("settings.download.proxyLabel") }),
      within(dialog).getByLabelText(t("settings.download.downloadDir")),
      within(dialog).getByLabelText(t("settings.download.localDir")),
      within(dialog).getByLabelText(t("settings.download.category")),
      within(dialog).getByLabelText(t("settings.download.tagsAria")),
      within(dialog).getByRole("checkbox"),
    ];
    for (let i = 1; i < order.length; i++) {
      expect(order[i - 1]!.compareDocumentPosition(order[i]!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    }
  });

  it("名称、密码、分类、路径输入带长度上限", async () => {
    stubFetch();
    renderSettings();
    const dialog = await openNewDownloader();

    const limits: Array<[string, string]> = [
      [t("settings.download.name"), "128"],
      [t("settings.download.password"), "1024"],
      [t("settings.download.category"), "256"],
      [t("settings.download.downloadDir"), "1024"],
      [t("settings.download.localDir"), "1024"],
    ];
    for (const [label, max] of limits) {
      expect(within(dialog).getByLabelText(label)).toHaveAttribute("maxlength", max);
    }
  });
});
