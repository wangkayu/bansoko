import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SettingsAILab } from "./SettingsAILab";
import { t } from "../../lib/i18n";
import { toast } from "sonner";

// API tokens: create, list, revoke (a dangerous confirmation; revoking hard-deletes — the row
//   vanishes from the list with no tombstone), rotate (delete old and create new in one
//   transaction), and long-term display + copy.
//   The backend stores the raw key encrypted; the list row reads it on demand
//   (GET /api-tokens/{id}/raw), the whole key masked by default with the eye revealing the full
//   text and copy always usable; the metadata list endpoint never includes raw (the frontend does
//   not expose it).

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

type FetchCall = { method: string; url: string; body?: string };

type StubToken = { id: string; name: string; created_at: string; last_used_at?: string | null; scopes?: string[] };

type GuideStub = { guide?: string; skill?: string; status?: number };

function stubApiTokens(options?: {
  list?: StubToken[];
  raws?: Record<string, string>;
  createToken?: string;
  rotateToken?: string;
  guide?: GuideStub;
}) {
  let guideStub: GuideStub = options?.guide ?? {};
  const calls: FetchCall[] = [];
  const list = options?.list ?? [
    {
      id: "token-1",
      name: "my-agent",
      created_at: "2026-08-07T00:00:00Z",
      last_used_at: null,
    },
  ];
  let listState = list;
  let raws: Record<string, string> = {
    "token-1": "raw-token-1",
    ...(options?.raws ?? {}),
  };

  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = (init?.method ?? "GET").toUpperCase();
      calls.push({ method, url, body: init?.body as string | undefined });

      // The English note: GET /agent-guide[?format=skill], text/markdown.
      if (method === "GET" && url.includes("/agent-guide")) {
        if (guideStub.status && guideStub.status >= 400) {
          return new Response(JSON.stringify({ code: "INTERNAL", message: "boom" }), { status: guideStub.status });
        }
        const body = url.includes("format=skill") ? (guideStub.skill ?? "---\nname: bansoko-agent\n---\n") : (guideStub.guide ?? "# Bansoko agent guide\n");
        return new Response(body, { status: 200, headers: { "Content-Type": "text/markdown" } });
      }

      // The inline raw read on demand: GET /api-tokens/{id}/raw
      const rawMatch = url.match(/\/api-tokens\/([^/]+)\/raw$/);
      if (method === "GET" && rawMatch) {
        const id = rawMatch[1];
        const raw = raws[id];
        if (!raw) {
          return new Response(JSON.stringify({ code: "NOT_FOUND", message: "raw unavailable" }), { status: 404 });
        }
        return new Response(JSON.stringify({ token: raw }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (method === "GET" && url.includes("/api-tokens")) {
        return new Response(JSON.stringify(listState.map((token) => ({ ...token, scopes: token.scopes ?? ["read", "write"] }))), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (method === "POST" && url.includes("/api-tokens/token-1")) {
        // Rotate: the old row hard-deletes and a new row with the same name appears (the backend's atomic semantics).
        const target = listState.find((t) => t.id === "token-1");
        listState = listState.filter((t) => t.id !== "token-1");
        listState = [
          ...listState,
          { id: "token-new", name: target?.name ?? "my-agent", created_at: "2026-08-07T03:00:00Z", last_used_at: null, scopes: target?.scopes },
        ];
        delete raws["token-1"];
        raws["token-new"] = options?.rotateToken ?? "raw-rotated";
        return new Response(
          JSON.stringify({ token: options?.rotateToken ?? "raw-rotated", token_id: "token-new", created_at: "2026-08-07T03:00:00Z", scopes: target?.scopes ?? ["read", "write"] }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      if (method === "POST" && url.includes("/api-tokens")) {
        const createBody = JSON.parse(init?.body as string) as { name: string; scopes?: string[] };
        const raw = options?.createToken ?? "raw-token-64hex";
        const scopes = createBody.scopes ?? ["read", "write"];
        listState = [
          ...listState,
          { id: "token-new", name: createBody.name, created_at: "2026-08-07T01:00:00Z", last_used_at: null, scopes },
        ];
        raws["token-new"] = raw;
        return new Response(
          JSON.stringify({ token: raw, token_id: "token-new", created_at: "2026-08-07T01:00:00Z", scopes }),
          { status: 201, headers: { "Content-Type": "application/json" } },
        );
      }
      if (method === "DELETE" && url.includes("/api-tokens/token-1")) {
        listState = listState.filter((t) => t.id !== "token-1");
        delete raws["token-1"];
        return new Response(null, { status: 204 });
      }
      return new Response(JSON.stringify({ code: "NOT_FOUND", message: "unexpected " + url }), { status: 404 });
    }),
  );
  return {
    calls,
    setGuide: (next: GuideStub) => {
      guideStub = next;
    },
  };
}

function renderLab() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SettingsAILab />
    </QueryClientProvider>,
  );
}

describe("SettingsAILab", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    vi.clearAllMocks();
    Reflect.deleteProperty(URL, "createObjectURL");
    Reflect.deleteProperty(URL, "revokeObjectURL");
  });

  it("列表行内 raw key 默认遮罩，可点眼睛查看全文，复制始终可用", async () => {
    stubApiTokens();
    renderLab();

    // The initial list is visible; the raw key is masked whole by default (the plaintext is never exposed directly).
    expect(await screen.findByText("my-agent")).toBeInTheDocument();
    expect(await screen.findByText(/••/)).toBeInTheDocument();
    expect(screen.queryByText("raw-token-1")).not.toBeInTheDocument();

    // The eye reveals the full text.
    fireEvent.click(screen.getByRole("button", { name: t("settings.token.showKey") }));
    expect(await screen.findByText("raw-token-1")).toBeInTheDocument();

    // Copy always copies the full text (independent of reveal state).
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.assign(navigator, { clipboard: { writeText } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.token.copy") }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("raw-token-1"));
    expect(await screen.findByText(t("settings.token.copied"))).toBeInTheDocument();
  });

  it("创建后新行出现且行内 raw key 遮罩，揭示后可见全文", async () => {
    const { calls } = stubApiTokens({ createToken: "raw-token-64hex" });
    renderLab();

    fireEvent.change(screen.getByLabelText(t("settings.token.nameLabel")), { target: { value: "new-bot" } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.token.create") }));

    expect(await screen.findByText("new-bot")).toBeInTheDocument();
    // Masked by default; revealing inside the new row shows the full text.
    const row = (await screen.findAllByRole("listitem")).find((item) => within(item).queryByText("new-bot"));
    if (!row) throw new Error("new token row not found");
    expect(within(row).queryByText("raw-token-64hex")).not.toBeInTheDocument();
    fireEvent.click(within(row).getByRole("button", { name: t("settings.token.showKey") }));
    expect(await within(row).findByText("raw-token-64hex")).toBeInTheDocument();
    const createCall = calls.find((c) => c.method === "POST");
    expect(createCall?.body).toContain('"name":"new-bot"');
    // Default permissions: view + write, without read-sensitive-values.
    expect(JSON.parse(createCall?.body ?? "{}").scopes).toEqual(["read", "write"]);
    expect(within(row).getByText(t("settings.token.scope.list", { scopes: [t("settings.token.scope.read"), t("settings.token.scope.write")].join(t("settings.token.scope.separator")) }))).toBeInTheDocument();
  });

  it("创建时可选权限：取消修改、勾选显示敏感值，按所选发送并在列表显示", async () => {
    const { calls } = stubApiTokens();
    renderLab();

    fireEvent.change(screen.getByLabelText(t("settings.token.nameLabel")), { target: { value: "reader" } });
    fireEvent.click(screen.getByRole("checkbox", { name: new RegExp(`^${t("settings.token.scope.write")}`) }));
    fireEvent.click(screen.getByRole("checkbox", { name: new RegExp(`^${t("settings.token.scope.secrets")}`) }));
    expect(screen.getByRole("checkbox", { name: new RegExp(`^${t("settings.token.scope.read")}`) })).toHaveAttribute("aria-disabled", "true");
    fireEvent.click(screen.getByRole("button", { name: t("settings.token.create") }));

    const row = await waitFor(() => {
      const found = screen.getAllByRole("listitem").find((item) => within(item).queryByText("reader"));
      if (!found) throw new Error("reader row not found");
      return found;
    });
    const createCall = calls.find((c) => c.method === "POST");
    expect(JSON.parse(createCall?.body ?? "{}").scopes).toEqual(["read", "secrets"]);
    expect(within(row).getByText(t("settings.token.scope.list", { scopes: [t("settings.token.scope.read"), t("settings.token.scope.secrets")].join(t("settings.token.scope.separator")) }))).toBeInTheDocument();
    // After creation the selection resets to the default.
    expect(screen.getByRole("checkbox", { name: new RegExp(`^${t("settings.token.scope.write")}`) })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: new RegExp(`^${t("settings.token.scope.secrets")}`) })).not.toBeChecked();
  });

  it("撤销需确认，确认后该行从列表消失（硬删除，无 tombstone 置灰）", async () => {
    stubApiTokens();
    renderLab();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.token.delete") }));
    expect(await screen.findByText(t("settings.token.deleteTitle", { name: "my-agent" }))).toBeInTheDocument();
    expect(screen.getByText(t("settings.token.deleteHint").split("\n")[1])).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("settings.token.delete") }));
    // After revoking the row is removed from the list entirely — no button or greyed-out row remains.
    await waitFor(() => expect(screen.queryByText("my-agent")).not.toBeInTheDocument());
    await waitFor(() => expect(screen.queryByRole("button", { name: t("settings.token.delete") })).not.toBeInTheDocument());
  });

  it("取消撤销确认不改变列表", async () => {
    stubApiTokens();
    renderLab();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.token.delete") }));
    fireEvent.click(screen.getByRole("button", { name: t("settings.token.cancel") }));
    await waitFor(() => expect(screen.queryByText(t("settings.token.deleteTitle", { name: "my-agent" }))).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: t("settings.token.delete") })).toBeInTheDocument();
    expect(screen.getByText("my-agent")).toBeInTheDocument();
  });

  it("轮换：确认后旧行消失，新行显示新 raw key（遮罩默认）", async () => {
    const { calls } = stubApiTokens({ rotateToken: "raw-rotated" });
    renderLab();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.token.rotate") }));
    expect(await screen.findByText(t("settings.token.rotateTitle", { name: "my-agent" }))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("settings.token.rotate") }));

    // The old row hard-deletes and the new one appears (masked by default).
    await waitFor(() => expect(screen.queryByText("raw-token-1")).not.toBeInTheDocument());
    expect(await screen.findByRole("button", { name: t("settings.token.showKey") })).toBeInTheDocument();
    expect(screen.queryByText("raw-rotated")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("settings.token.showKey") }));
    expect(await screen.findByText("raw-rotated")).toBeInTheDocument();
    const rotateCall = calls.find((c) => c.method === "POST" && c.url.includes("/api-tokens/token-1"));
    expect(rotateCall).toBeTruthy();
  });

  it("raw 不可读（部署密钥未配置）时行内显示降级提示，auth 不受影响", async () => {
    // This token has no encrypted raw (a deployment secret left unconfigured), so the raw read returns 404.
    stubApiTokens({
      list: [{ id: "no-raw", name: "my-agent", created_at: "2026-08-07T00:00:00Z", last_used_at: null }],
    });
    renderLab();

    expect(await screen.findByText("my-agent")).toBeInTheDocument();
    expect(await screen.findByText(t("settings.token.rawUnavailable"))).toBeInTheDocument();
    // The metadata list still works (a missing raw never fails the whole row).
    expect(screen.getByText(t("settings.token.createTitle"))).toBeInTheDocument();
  });

  it("列表为空时展示空态；页面直接显示 MCP 地址，令牌注意事项收在“?”里", async () => {
    stubApiTokens({ list: [] });
    renderLab();

    expect(await screen.findByText(t("settings.token.empty"))).toBeInTheDocument();
    // Not expanded by default: the commands and internal tool names are absent from the page; the MCP address is directly visible.
    expect(screen.queryByText(/claude mcp add/)).not.toBeInTheDocument();
    expect(screen.queryByText("search_anime")).not.toBeInTheDocument();
    expect(screen.getByText(t("settings.token.purpose"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.token.connect.endpoint"))).toBeInTheDocument();
    expect(screen.getByText(`${window.location.origin}/api/v1/mcp`)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: t("settings.token.helpLabel") }));
    expect(await screen.findByText(t("settings.token.help.intro"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.token.help.cli"))).toBeInTheDocument();
  });

  it("复制说明：没有新建令牌时带占位符，新建后带刚生成的令牌", async () => {
    stubApiTokens({ createToken: "raw-token-64hex" });
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.assign(navigator, { clipboard: { writeText } });
    renderLab();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.token.connect.copySetup") }));
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    const placeholderText = writeText.mock.calls[0][0] as string;
    expect(placeholderText).toContain(`URL: ${window.location.origin}/api/v1/mcp`);
    expect(placeholderText).toContain("Authorization: Bearer <API-TOKEN>");
    expect(placeholderText).toContain("bansoko://agent-guide");
    expect(await screen.findByText(t("settings.token.connect.setupCopied"))).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText(t("settings.token.nameLabel")), { target: { value: "new-bot" } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.token.create") }));
    expect(await screen.findByText("new-bot")).toBeInTheDocument();
    // After one click the button briefly shows the copied state; either name counts as the same button.
    fireEvent.click(screen.getByRole("button", { name: new RegExp(`${t("settings.token.connect.copySetup")}|${t("settings.token.connect.setupCopied")}`) }));
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(2));
    const freshText = writeText.mock.calls[1][0] as string;
    expect(freshText).toContain("Authorization: Bearer raw-token-64hex");
    expect(freshText).not.toContain("<API-TOKEN>");
  });

  it("复制说明失败时提示手动复制", async () => {
    stubApiTokens();
    Object.assign(navigator, { clipboard: { writeText: vi.fn().mockRejectedValue(new Error("denied")) } });
    renderLab();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.token.connect.copySetup") }));
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(t("settings.token.copyFailed")));
  });

  it("下载 skill：请求 ?format=skill 并以 SKILL.md 保存", async () => {
    const { calls } = stubApiTokens({ guide: { skill: "---\nname: bansoko-agent\ndescription: x\n---\n\n# Guide\n" } });
    const createObjectURL = vi.fn<(blob: Blob) => string>(() => "blob:skill");
    const revokeObjectURL = vi.fn();
    Object.assign(URL, { createObjectURL, revokeObjectURL });
    let download: { href: string; download: string } | undefined;
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      download = { href: this.href, download: this.download };
    });
    renderLab();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.token.connect.downloadSkill") }));
    await waitFor(() => expect(download).toEqual({ href: "blob:skill", download: "SKILL.md" }));
    expect(calls.some((call) => call.method === "GET" && call.url.endsWith("/agent-guide?format=skill"))).toBe(true);
    expect(await createObjectURL.mock.calls[0][0].text()).toContain("name: bansoko-agent");
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:skill");
    Reflect.deleteProperty(URL, "createObjectURL");
    Reflect.deleteProperty(URL, "revokeObjectURL");
  });

  it("下载 skill 失败时提示，不保存文件", async () => {
    stubApiTokens({ guide: { status: 500 } });
    const createObjectURL = vi.fn(() => "blob:never");
    Object.assign(URL, { createObjectURL, revokeObjectURL: vi.fn() });
    renderLab();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.token.connect.downloadSkill") }));
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect(createObjectURL).not.toHaveBeenCalled();
    Reflect.deleteProperty(URL, "createObjectURL");
    Reflect.deleteProperty(URL, "revokeObjectURL");
  });

  it("查看说明：只读弹窗原样显示英文说明，可关闭", async () => {
    const guideText = "# Bansoko agent guide\n\n| Step | MCP tool |\n|---|---|\n| list | `get_subscriptions` |\n";
    const { calls } = stubApiTokens({ guide: { guide: guideText } });
    renderLab();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.token.connect.viewGuide") }));
    const dialog = await screen.findByRole("dialog", { name: t("settings.token.connect.guideTitle") });
    expect(within(dialog).getByText(t("settings.token.connect.guideHint"))).toBeInTheDocument();
    // Verbatim: the preformatted block holds unrendered Markdown source.
    const block = await within(dialog).findByText((_, element) => element?.tagName === "PRE" && element.textContent === guideText);
    expect(block).toBeInTheDocument();
    expect(block).toHaveAttribute("lang", "en");
    expect(calls.some((call) => call.method === "GET" && call.url.endsWith("/agent-guide"))).toBe(true);
    // Read-only: the dialog holds no inputs.
    expect(within(dialog).queryByRole("textbox")).not.toBeInTheDocument();

    fireEvent.click(within(dialog).getAllByRole("button", { name: t("ui.close") })[0]);
    await waitFor(() => expect(screen.queryByRole("dialog", { name: t("settings.token.connect.guideTitle") })).not.toBeInTheDocument());
  });

  it("查看说明加载失败时显示错误并可重试", async () => {
    const { setGuide } = stubApiTokens({ guide: { status: 500 } });
    renderLab();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.token.connect.viewGuide") }));
    const dialog = await screen.findByRole("dialog", { name: t("settings.token.connect.guideTitle") });
    expect(await within(dialog).findByText(t("settings.token.connect.guideFailed"))).toBeInTheDocument();

    setGuide({ guide: "# Recovered\n" });
    fireEvent.click(within(dialog).getByRole("button", { name: t("settings.token.connect.guideRetry") }));
    expect(await within(dialog).findByText("# Recovered", { exact: false })).toBeInTheDocument();
  });

  it("撤销后从列表消失，行内 raw 也不再渲染", async () => {
    stubApiTokens({ list: [{ id: "token-1", name: "my-agent", created_at: "2026-08-07T00:00:00Z", last_used_at: null }] });
    renderLab();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.token.delete") }));
    fireEvent.click(screen.getByRole("button", { name: t("settings.token.delete") }));
    await waitFor(() => expect(screen.queryByText("my-agent")).not.toBeInTheDocument());
    expect(screen.getByText(t("settings.token.empty"))).toBeInTheDocument();
  });
});
