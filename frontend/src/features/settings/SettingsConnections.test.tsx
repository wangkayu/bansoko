import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Connection, NetworkRoute } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { SettingsConnections } from "./SettingsConnections";

// Route credential three-state regression: the SOCKS5 password field was once missing, and the
//   transition moved route passwords from the old flat optional shape into the ConnectionSecret
//   three states (keep/replace/remove), with the username required on replace and no flat password
//   in the body anymore.

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

type FetchStub = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

let fetchMock: ReturnType<typeof vi.fn<FetchStub>>;

function stubFetch(options: { connections?: Connection[]; routes?: NetworkRoute[] } = {}) {
  fetchMock = vi.fn<FetchStub>(async (input, init) => {
    const url = String(input);
    if (init?.method === "PUT" && url.includes("/config/network-routes/")) {
      const requestBody = JSON.parse(String(init.body)) as { name: string; host?: string; port?: number };
      const saved = {
        id: url.slice(url.lastIndexOf("/") + 1),
        kind: "socks5",
        name: requestBody.name,
        enabled: true,
        host: requestBody.host ?? "",
        port: requestBody.port ?? 1080,
        credential_configured: true,
        version: 2,
      };
      return new Response(JSON.stringify(saved), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
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

function renderSettings(initialEntries: string[] = ["/settings/connections"]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createMemoryRouter([{ path: "*", element: <SettingsConnections /> }], {
    initialEntries,
    initialIndex: initialEntries.length - 1,
  });
  return { ...render(<QueryClientProvider client={client}><RouterProvider router={router} /></QueryClientProvider>), router };
}

function routePutCalls() {
  return fetchMock.mock.calls.filter(
    ([input, init]) => String(input).includes("/config/network-routes/") && init?.method === "PUT",
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

async function selectOption(label: string, option: string) {
  await userEvent.click(screen.getByRole("combobox", { name: label }));
  await userEvent.click(await screen.findByRole("option", { name: option }));
}

describe("SettingsConnections SOCKS5 路由凭据三态", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("新建 SOCKS5 路由：密码框存在，保存 body 携带 secret replace", async () => {
    stubFetch();
    renderSettings();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.proxy.new") }));
    await selectOption(t("settings.connections.typeLabel"), "SOCKS5");

    const password = screen.getByLabelText(t("settings.proxy.passwordLabel"));
    expect(password).toHaveAttribute("type", "password");

    fireEvent.change(screen.getByLabelText(t("settings.connections.nameLabel")), { target: { value: "家宽出口" } });
    fireEvent.change(screen.getByLabelText(new RegExp(`^${t("settings.proxy.addressLabel")}`)), { target: { value: "socks5://192.168.1.10:1080" } });
    fireEvent.change(screen.getByLabelText(t("settings.proxy.usernameLabel")), { target: { value: "proxy-user" } });
    fireEvent.change(password, { target: { value: "s3cret" } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.connections.save") }));

    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.proxy.saved")));

    expect(routePutCalls()).toHaveLength(1);
    const body = putBody(routePutCalls()[0]!);
    expect(body).toMatchObject({
      kind: "socks5",
      name: "家宽出口",
      host: "192.168.1.10",
      port: 1080,
      username: "proxy-user",
      secret: { mode: "replace", value: "s3cret" },
    });
    expect(body).not.toHaveProperty("password");
  });

  it("新建路由密码留空：body 省略 secret，即无凭据", async () => {
    stubFetch();
    renderSettings();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.proxy.new") }));
    await selectOption(t("settings.connections.typeLabel"), "SOCKS5");
    fireEvent.change(screen.getByLabelText(t("settings.connections.nameLabel")), { target: { value: "裸出口" } });
    fireEvent.change(screen.getByLabelText(new RegExp(`^${t("settings.proxy.addressLabel")}`)), { target: { value: "socks5://192.168.1.11:1080" } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.connections.save") }));

    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.proxy.saved")));

    const body = putBody(routePutCalls()[0]!);
    expect(body).not.toHaveProperty("secret");
    expect(body).not.toHaveProperty("password");
  });

  it("编辑未配置凭据的 SOCKS5 路由：默认替换态，输入后随 CAS 保存", async () => {
    stubFetch({
      routes: [
        {
          id: "route-1",
          kind: "socks5",
          name: "旧路由",
          enabled: true,
          host: "10.0.0.2",
          port: 1081,
          credential_configured: false,
          version: 7,
        },
      ],
    });
    renderSettings();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.connections.edit") }));
    expect(screen.getByText(t("settings.proxy.editTitle"))).toBeInTheDocument();

    // On edit the contract's host/port are composed back into one URL for display.
    expect(screen.getByLabelText(new RegExp(`^${t("settings.proxy.addressLabel")}`))).toHaveValue("socks5://10.0.0.2:1081");

    const password = screen.getByLabelText(t("settings.proxy.passwordLabel"));
    expect(password).toHaveAttribute("type", "password");
    // The server credential is redacted (credential_configured), so no old password fills back.
    expect(password).toHaveValue("");

    fireEvent.change(screen.getByLabelText(t("settings.proxy.usernameLabel")), { target: { value: "u2" } });
    fireEvent.change(password, { target: { value: "new-secret" } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.connections.save") }));

    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.proxy.saved")));

    expect(routePutCalls()).toHaveLength(1);
    const [input, init] = routePutCalls()[0]!;
    expect(String(input)).toContain("/config/network-routes/route-1");
    expect((init?.headers as Headers).get("If-Match")).toBe("7");
    expect(putBody(routePutCalls()[0]!)).toMatchObject({
      kind: "socks5",
      host: "10.0.0.2",
      port: 1081,
      username: "u2",
      secret: { mode: "replace", value: "new-secret" },
    });
  });

  it("编辑已配置凭据的路由：默认保留；切移除后保存发送 remove", async () => {
    stubFetch({
      routes: [
        {
          id: "route-2",
          kind: "http_proxy",
          name: "旧代理",
          enabled: true,
          proxy_url: "http://proxy.local:7890",
          credential_configured: true,
          version: 3,
        },
      ],
    });
    renderSettings();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.connections.edit") }));
    expect(screen.getByText(t("settings.proxy.editTitle"))).toBeInTheDocument();

    // Keep is the default: the password input does not render and the username input is disabled.
    expect(screen.queryByLabelText(t("settings.proxy.passwordLabel"))).not.toBeInTheDocument();
    expect(screen.getByLabelText(t("settings.proxy.usernameLabel"))).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: t("settings.connections.save") }));
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.proxy.saved")));
    expect(putBody(routePutCalls()[0]!)).toMatchObject({ secret: { mode: "keep" } });

    // Reopen and switch to remove credentials.
    fireEvent.click(await screen.findByRole("button", { name: t("settings.connections.edit") }));
    fireEvent.click(screen.getByLabelText(t("secret.remove")));
    fireEvent.click(screen.getByRole("button", { name: t("settings.connections.save") }));
    await waitFor(() => expect(routePutCalls()).toHaveLength(2));
    expect(putBody(routePutCalls()[1]!)).toMatchObject({ secret: { mode: "remove" } });
    expect(putBody(routePutCalls()[1]!)).not.toHaveProperty("password");
  });
});


// Session 3: rss_http drops the user_agent/request_timeout_seconds/max_response_bytes inputs that
//   never took effect, and connection secrets move to the three states (this session's frontend
//   adapts the keep/replace minimum).
describe("SettingsConnections 批次 3 契约", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("新建 rss_http：表单无 UA/超时/字节三字段，保存 body 只含 kind/name/enabled", async () => {
    stubFetch();
    renderSettings();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.connections.new") }));
    // The default type is mikan; this case switches to rss_http explicitly.
    await selectOption(t("settings.connections.typeLabel"), t("settings.connections.rssKind"));
    expect(screen.queryByLabelText("User-Agent")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("超时（秒）")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("最大响应字节")).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText(t("settings.connections.nameLabel")), { target: { value: "RSS 传输" } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.connections.save") }));

    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.connections.saved")));

    expect(connectionPutCalls()).toHaveLength(1);
    const body = putBody(connectionPutCalls()[0]!);
    expect(body).toMatchObject({ kind: "rss_http", name: "RSS 传输", enabled: true });
    expect(body).not.toHaveProperty("user_agent");
    expect(body).not.toHaveProperty("request_timeout_seconds");
    expect(body).not.toHaveProperty("max_response_bytes");
  });

  it("新建连接的类型选项不再包含 qBittorrent（已拆到下载与入库）", async () => {
    stubFetch();
    renderSettings();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.connections.new") }));

    const kindSelect = screen.getByRole("combobox", { name: t("settings.connections.typeLabel") });
    await userEvent.click(kindSelect);
    expect(await screen.findByRole("listbox")).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /qBittorrent/i })).not.toBeInTheDocument();
  });
});

// Session 4: rss_http is a transport-policy connection with a placeholder endpoint and nothing
//   testable, so the row no longer offers a test button; qBittorrent has moved to the acquisition
//   section (covered by SettingsDownload.test.tsx).
describe("SettingsConnections 批次 4 交互", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("rss_http 行无「测试」按钮且 qB 行不再出现在服务连接列表", async () => {
    stubFetch({
      connections: [
        { id: "rss-1", kind: "rss_http", name: "RSS 传输", enabled: true, version: 1 },
        {
          id: "qb-1",
          kind: "qbittorrent",
          name: "QB",
          enabled: true,
          base_url: "http://qb.local:8080",
          username: "u",
          credential_configured: true,
          version: 1,
        },
      ],
    });
    renderSettings();

    expect(await screen.findByText("RSS 传输")).toBeInTheDocument();
    expect(screen.getByText(t("settings.connections.rssRowNote"))).toBeInTheDocument();
    // qB belongs to the acquisition settings section; neither the connection list nor the test button shows it anymore.
    expect(screen.queryByText("QB")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("settings.connections.test") })).not.toBeInTheDocument();
  });
});

// New connections default to a provider (Mikan) rather than RSS HTTP, with each provider's real
//   official address prefilled; a type switch follows the default address, while a user-entered ago
//   proxy address is kept, never overwritten.
describe("SettingsConnections 新建连接默认值（UX-7/8）", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("新建默认 Mikan 预填真实地址；切换类型跟随默认，自定义地址保留", async () => {
    stubFetch();
    renderSettings();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.connections.new") }));
    await userEvent.click(screen.getByRole("combobox", { name: t("settings.connections.typeLabel") }));
    expect(await screen.findByRole("option", { name: "Mikan" })).toHaveAttribute("aria-selected", "true");
    await userEvent.click(await screen.findByRole("option", { name: "Mikan" }));
    expect(screen.getByLabelText(t("settings.connections.siteAddress"))).toHaveValue("https://mikanani.me");

    // Switching to AniBT with the address still at the official default: it follows the switch.
    await selectOption(t("settings.connections.typeLabel"), "AniBT");
    expect(screen.getByLabelText(t("settings.connections.siteAddress"))).toHaveValue("https://anibt.net");

    // After typing a fronting-proxy address, a type switch keeps the custom address.
    fireEvent.change(screen.getByLabelText(t("settings.connections.siteAddress")), { target: { value: "https://proxy.internal/anibt" } });
    await selectOption(t("settings.connections.typeLabel"), "BGM");
    expect(screen.getByLabelText(t("settings.connections.bgmAddress"))).toHaveValue("https://proxy.internal/anibt");

    // Clearing then switching types prefills the new type's default address again.
    fireEvent.change(screen.getByLabelText(t("settings.connections.bgmAddress")), { target: { value: "" } });
    await selectOption(t("settings.connections.typeLabel"), "Mikan");
    expect(screen.getByLabelText(t("settings.connections.siteAddress"))).toHaveValue("https://mikanani.me");
  });
});

// The connection model: the built-in provider/metadata connections (mikan/anibt/bgm/tmdb) have one
//   per kind, the list marks them built-in, the create dialog forbids an existing kind, and the
//   edit dialog offers restore defaults (official address + direct connection).
describe("SettingsConnections 内置连接", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("内置连接行显示「内置」标记；新建弹窗禁选已有内置 kind", async () => {
    stubFetch({
      connections: [
        { id: "mikan-1", kind: "mikan", name: "Mikan", enabled: true, base_url: "https://mikanani.me", version: 1 },
      ],
    });
    renderSettings();

    expect(await screen.findByText(t("settings.connections.builtin"))).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: t("settings.connections.new") }));
    await userEvent.click(screen.getByRole("combobox", { name: t("settings.connections.typeLabel") }));
    const mikanOption = await screen.findByRole("option", { name: new RegExp(`Mikan${t("settings.connections.builtinExisting")}`) });
    expect(mikanOption).toHaveAttribute("aria-disabled", "true");
    // rss_http is a transport-policy connection, outside the built-in uniqueness constraint.
    expect(await screen.findByRole("option", { name: t("settings.connections.rssKind") })).not.toHaveAttribute("aria-disabled", "true");
  });

  it("编辑内置连接：还原默认把 baseurl 恢复官方地址并清除路由绑定", async () => {
    stubFetch({
      connections: [
        {
          id: "mikan-1",
          kind: "mikan",
          name: "Mikan 反代",
          enabled: true,
          base_url: "https://proxy.internal/mikan",
          route_id: "route-9",
          version: 4,
        },
      ],
      routes: [
        { id: "route-9", kind: "direct", name: "家宽出口", enabled: true, version: 1 },
      ],
    });
    renderSettings();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.connections.edit") }));
    expect(screen.getByLabelText(t("settings.connections.siteAddress"))).toHaveValue("https://proxy.internal/mikan");
    await userEvent.click(screen.getByRole("combobox", { name: t("settings.connections.proxyLabel") }));
    expect(await screen.findByRole("option", { name: "家宽出口" })).toHaveAttribute("aria-selected", "true");
    // Close the list first: the next combobox click must open it, not toggle a
    // still-open (or still-closing) list.
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("option", { name: "家宽出口" })).not.toBeInTheDocument());

    fireEvent.click(screen.getByRole("button", { name: t("settings.connections.restoreDefaults") }));
    expect(screen.getByLabelText(t("settings.connections.siteAddress"))).toHaveValue("https://mikanani.me");
    await userEvent.click(screen.getByRole("combobox", { name: t("settings.connections.proxyLabel") }));
    expect(await screen.findByRole("option", { name: t("settings.connections.proxyDirect") })).toHaveAttribute("aria-selected", "true");

    fireEvent.click(screen.getByRole("button", { name: t("settings.connections.save") }));
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.connections.saved")));

    const body = putBody(connectionPutCalls()[0]!);
    expect(body).toMatchObject({ kind: "mikan", base_url: "https://mikanani.me" });
    expect(body.route_id).toBeUndefined();
  });
});

// The route form takes one unified URL input shared by http_proxy/socks5; a socks5 URL without a
//   port defaults to 1080, and an illegal scheme blocks saving with a hint.
describe("SettingsConnections 路由表单统一 URL 输入", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("socks5 省略端口时按 1080 保存", async () => {
    stubFetch();
    renderSettings();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.proxy.new") }));
    await selectOption(t("settings.connections.typeLabel"), "SOCKS5");
    fireEvent.change(screen.getByLabelText(t("settings.connections.nameLabel")), { target: { value: "无端口出口" } });
    fireEvent.change(screen.getByLabelText(new RegExp(`^${t("settings.proxy.addressLabel")}`)), { target: { value: "socks5://192.168.1.20" } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.connections.save") }));

    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.proxy.saved")));
    expect(putBody(routePutCalls()[0]!)).toMatchObject({ host: "192.168.1.20", port: 1080 });
  });

  it("socks5 填 http scheme：禁存并显示校验提示", async () => {
    stubFetch();
    renderSettings();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.proxy.new") }));
    await selectOption(t("settings.connections.typeLabel"), "SOCKS5");
    fireEvent.change(screen.getByLabelText(t("settings.connections.nameLabel")), { target: { value: "坏出口" } });
    fireEvent.change(screen.getByLabelText(new RegExp(`^${t("settings.proxy.addressLabel")}`)), { target: { value: "http://192.168.1.20:1080" } });

    expect(screen.getByText(t("settings.proxy.socksInvalid"))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("settings.connections.save") })).toBeDisabled();
    expect(routePutCalls()).toHaveLength(0);
  });

  it("http_proxy 沿用统一输入并校验 http/https scheme", async () => {
    stubFetch();
    renderSettings();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.proxy.new") }));
    await selectOption(t("settings.connections.typeLabel"), t("settings.proxy.http"));
    fireEvent.change(screen.getByLabelText(t("settings.connections.nameLabel")), { target: { value: "HTTP 出口" } });
    fireEvent.change(screen.getByLabelText(new RegExp(`^${t("settings.proxy.addressLabel")}`)), { target: { value: "socks5://192.168.1.20:1080" } });
    expect(screen.getByRole("button", { name: t("settings.connections.save") })).toBeDisabled();

    fireEvent.change(screen.getByLabelText(new RegExp(`^${t("settings.proxy.addressLabel")}`)), { target: { value: "http://192.168.1.20:7890" } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.connections.save") }));

    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.proxy.saved")));
    expect(putBody(routePutCalls()[0]!)).toMatchObject({ kind: "http_proxy", proxy_url: "http://192.168.1.20:7890" });
  });
});

describe("SettingsConnections 深链 ?connection=", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("?connection=<id> 打开该连接的编辑弹窗并消费掉参数", async () => {
    stubFetch({
      connections: [
        { id: "tmdb-1", kind: "tmdb", name: "TMDB", enabled: true, base_url: "https://api.themoviedb.org/3", credential_configured: true, version: 3 },
        { id: "bgm-1", kind: "bgm", name: "Bangumi", enabled: true, base_url: "https://api.bgm.tv", version: 1 },
      ],
    });
    const { router } = renderSettings(["/settings/connections?connection=tmdb-1"]);

    // The edit dialog opens directly on the TMDB row (kind select disabled at tmdb).
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(t("settings.connections.editTitle"));
    expect(within(dialog).getByLabelText(t("settings.connections.nameLabel"))).toHaveValue("TMDB");
    // The parameter is consumed (replaced, leaving no back-navigable duplicate opening).
    await waitFor(() => expect(router.state.location.search).not.toContain("connection="));
  });

  it("?connection=<kind> 按类型匹配;找不到行时只落分区不打开弹窗", async () => {
    stubFetch({
      connections: [
        { id: "tmdb-1", kind: "tmdb", name: "TMDB", enabled: true, base_url: "https://api.themoviedb.org/3", credential_configured: true, version: 3 },
      ],
    });
    const found = renderSettings(["/settings/connections?connection=tmdb"]);
    expect((await screen.findByRole("dialog"))).toHaveTextContent(t("settings.connections.editTitle"));
    found.unmount();

    renderSettings(["/settings/connections?connection=rss_http"]);
    await screen.findByText(t("settings.connections.title"));
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
