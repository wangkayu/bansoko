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

  it("编辑下载器：默认 keep，替换/移除三态分别组装 replace/remove", async () => {
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

    // Choosing remove credentials means remove.
    fireEvent.click(await screen.findByRole("button", { name: t("settings.download.edit") }));
    const thirdDialog = await screen.findByRole("dialog");
    fireEvent.click(within(thirdDialog).getByLabelText(t("secret.remove")));
    fireEvent.click(within(thirdDialog).getByRole("button", { name: t("settings.download.save") }));
    await waitFor(() => expect(connectionPutCalls()).toHaveLength(3));
    expect(putBody(connectionPutCalls()[2]!)).toMatchObject({
      kind: "qbittorrent",
      secret: { mode: "remove" },
    });
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

