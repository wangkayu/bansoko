import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { t } from "../../lib/i18n";
import type { PluginTargetConfig } from "../../lib/api/client";
import { SettingsNotifications } from "./SettingsNotifications";

// The Telegram notification target form's regression (the E2 telegram channel): chat_id required,
//   the bot_token secret's three states (create is set / edit is keep, replace, remove), and test
//   delivery keyed by target id + expected_version, independent of adapter. The template catalog
//   request is stubbed with the system default templates throughout.

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

type FetchStub = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

let fetchMock: ReturnType<typeof vi.fn<FetchStub>>;

// The closed trigger-event list matching the backend's plugintarget registry (in catalog order).
const ALL_TRIGGER_KINDS = [
  "download_submitted",
  "file_operation_completed",
  "file_operation_failed",
  "release_discovered",
  "download_failed",
  "subscription_changed",
  "unknown_files_imported",
];

const TEMPLATE_EVENTS = ["download_submitted", "file_operation_completed", "file_operation_failed"];

function templateSet() {
  return {
    language_version: "bansoko-template-1",
    catalog_version: "notification-catalog-v1",
    events: Object.fromEntries(TEMPLATE_EVENTS.map((kind) => [kind, { mode: "system_default" }])),
  };
}

function telegramTarget(overrides: Partial<PluginTargetConfig> = {}): PluginTargetConfig {
  return {
    id: "target-tg-1",
    name: "运维群",
    plugin_kind: "notification",
    adapter_kind: "telegram",
    enabled: true,
    trigger_kinds: ["download_submitted", "file_operation_completed"],
    config_schema_version: 1,
    config_status: "available",
    config: { adapter_kind: "telegram", chat_id: "-1001234567890" },
    template_set: templateSet() as PluginTargetConfig["template_set"],
    has_secret: true,
    version: 3,
    updated_at: "2026-08-02T00:00:00Z",
    ...overrides,
  };
}

function templateCatalogBody(catalogVersion = "notification-catalog-v1") {
  return {
    language_version: "bansoko-template-1",
    catalog_version: catalogVersion,
    trigger_kind: "download_submitted",
    variables: [
      { name: "anime.title", label: "Anime title", type: "string", nullable: false, source: "anime", sample_value: "示例动画", phase: "release", label_key: "facts.anime.title.label" },
      { name: "ep.display", label: "Episode", type: "string", nullable: true, source: "ep", sample_value: "07", phase: "release", label_key: "facts.ep.display.label" },
    ],
    functions: [],
    default_template: { mode: "custom", title_template: "默认标题", body_template: "默认正文" },
    sample: { values: {} },
  };
}

function stubFetch(
  options: {
    targets?: PluginTargetConfig[];
    patchError?: unknown;
    triggerKinds?: string[];
    triggerKindsError?: boolean;
  } = {},
) {
  fetchMock = vi.fn<FetchStub>(async (input, init) => {
    const url = String(input);
    if (options.triggerKindsError && url.includes("/config/notifications/trigger-kinds")) {
      return new Response(JSON.stringify({ code: "INTERNAL", message: "boom" }), {
        status: 500,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (url.includes("/config/notifications/trigger-kinds")) {
      return new Response(JSON.stringify({ trigger_kinds: options.triggerKinds ?? ALL_TRIGGER_KINDS }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (options.patchError && init?.method === "PATCH" && url.includes("/plugin-targets/")) {
      return new Response(JSON.stringify(options.patchError), {
        status: 422,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (init?.method === "POST" && url.endsWith("/plugin-targets")) {
      return new Response(JSON.stringify(telegramTarget({ id: "target-new", version: 1 })), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (init?.method === "PATCH" && url.includes("/plugin-targets/")) {
      return new Response(JSON.stringify(telegramTarget({ version: 4 })), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (init?.method === "POST" && url.includes("/test")) {
      return new Response(JSON.stringify({ accepted: true, delivery_id: "delivery-1" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (url.includes("/plugin-deliveries/")) {
      return new Response(
        JSON.stringify({
          delivery: {
            id: "delivery-1",
            state: "delivered",
            last_error: null,
          },
          attempts: [],
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    }
    if (url.includes("/template-catalogs/")) {
      const version = new URL(url, "http://localhost").searchParams.get("catalog_version") ?? "notification-catalog-v1";
      return new Response(JSON.stringify(templateCatalogBody(version)), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (url.includes("/plugin-targets")) {
      return new Response(JSON.stringify({ targets: options.targets ?? [] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    return new Response("not found", { status: 404 });
  });
  vi.stubGlobal("fetch", fetchMock);
}

function renderNotifications() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SettingsNotifications />
    </QueryClientProvider>,
  );
}

function postCalls() {
  return fetchMock.mock.calls.filter(
    ([input, init]) => String(input).endsWith("/plugin-targets") && init?.method === "POST",
  );
}

function patchCalls() {
  return fetchMock.mock.calls.filter(
    ([input, init]) => String(input).includes("/plugin-targets/") && init?.method === "PATCH",
  );
}

function postBody(call: unknown[]) {
  return JSON.parse(String((call[1] as RequestInit).body)) as Record<string, unknown>;
}

// The create-channel button enables only once the trigger-event registry query returns; wait until enabled before opening it.
async function openNewDialog() {
  const button = await screen.findByRole("button", { name: t("settings.notifications.new") });
  await waitFor(() => expect(button).toBeEnabled());
  fireEvent.click(button);
  await screen.findByRole("dialog");
}

describe("SettingsNotifications telegram 表单", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("新建 telegram：缺 chat_id 时保存禁用；补全后提交 config + set secret", async () => {
    stubFetch();
    renderNotifications();
    await openNewDialog();

    expect(screen.getByRole("dialog")).toHaveTextContent(t("settings.notifications.newTitle"));

    const save = screen.getByRole("button", { name: t("settings.notifications.save") });
    // Name and bot_token present with chat_id empty -> cannot save.
    fireEvent.change(screen.getByLabelText(t("settings.notifications.field.name")), { target: { value: "运维群" } });
    fireEvent.change(screen.getByLabelText(t("settings.notifications.field.botToken")), { target: { value: "123:abc" } });
    expect(save).toBeDisabled();

    fireEvent.change(screen.getByLabelText(t("settings.notifications.field.chatId")), { target: { value: "-1001234567890" } });
    expect(save).toBeEnabled();
    fireEvent.click(save);

    await waitFor(() => expect(postCalls()).toHaveLength(1));
    const body = postBody(postCalls()[0]!);
    expect(body).toMatchObject({
      name: "运维群",
      plugin_kind: "notification",
      adapter_kind: "telegram",
      config: { adapter_kind: "telegram", chat_id: "-1001234567890" },
      secret: { mode: "set", value: { adapter_kind: "telegram", bot_token: "123:abc" } },
    });
  });

  it("编辑 telegram：默认 keep 不携带 secret；chat_id 清空不可保存", async () => {
    stubFetch({ targets: [telegramTarget()] });
    renderNotifications();
    fireEvent.click(await screen.findByRole("button", { name: t("settings.notifications.edit") }));

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(t("settings.notifications.editTitle"));
    // The bot_token input's guidance copy in the keep state.
    expect(screen.getByPlaceholderText(t("settings.notifications.field.botTokenReplace"))).toBeInTheDocument();

    const save = screen.getByRole("button", { name: t("settings.notifications.save") });
    expect(save).toBeEnabled();
    // Clearing chat_id (a config field) -> cannot save.
    fireEvent.change(screen.getByLabelText(t("settings.notifications.field.chatId")), { target: { value: "  " } });
    expect(save).toBeDisabled();
    fireEvent.change(screen.getByLabelText(t("settings.notifications.field.chatId")), { target: { value: "-100999" } });
    expect(save).toBeEnabled();

    fireEvent.click(save);
    await waitFor(() => expect(patchCalls()).toHaveLength(1));
    const body = postBody(patchCalls()[0]!);
    expect(body).toMatchObject({
      expected_version: 3,
      adapter_kind: "telegram",
      config: { adapter_kind: "telegram", chat_id: "-100999" },
      secret: { mode: "keep" },
    });
  });

  it("保存时模板编译失败：列出事件、字段、位置和原因", async () => {
    stubFetch({
      targets: [telegramTarget()],
      patchError: {
        code: "TEMPLATE_INVALID",
        message: "The request failed typed validation.",
        details: [
          {
            field: "template_set.events.file_operation_completed.title_template",
            code: "invalid_expression",
            message: "未知变量 \"anime.nope\"",
            start: 7,
            end: 21,
          },
        ],
      },
    });
    renderNotifications();
    fireEvent.click(await screen.findByRole("button", { name: t("settings.notifications.edit") }));
    fireEvent.click(await screen.findByRole("button", { name: t("settings.notifications.save") }));
    const where = `${t("settings.notifications.trigger.file_operation_completed")} · ${t("settings.notifications.template.titleField")}`;
    expect(
      await screen.findByText(
        t("settings.notifications.template.errorLine", {
          where,
          position: t("settings.notifications.template.errorAt", { n: 7 }),
          message: "未知变量 \"anime.nope\"",
        }),
      ),
    ).toBeInTheDocument();
  });

  it("编辑 telegram：replace 态必须填新 token；提交 replace secret", async () => {
    stubFetch({ targets: [telegramTarget()] });
    renderNotifications();
    fireEvent.click(await screen.findByRole("button", { name: t("settings.notifications.edit") }));
    await screen.findByRole("dialog");

    fireEvent.click(screen.getByLabelText(t("secret.replace")));
    const save = screen.getByRole("button", { name: t("settings.notifications.save") });
    // Replace with an empty new token -> cannot save (an empty token must never overwrite valid credentials).
    expect(save).toBeDisabled();

    fireEvent.change(screen.getByLabelText(t("settings.notifications.field.botToken")), { target: { value: "456:def" } });
    expect(save).toBeEnabled();
    fireEvent.click(save);

    await waitFor(() => expect(patchCalls()).toHaveLength(1));
    expect(postBody(patchCalls()[0]!)).toMatchObject({
      secret: { mode: "replace", value: { adapter_kind: "telegram", bot_token: "456:def" } },
    });
  });

  it("telegram 目标测试投递：按目标 id 提交 trigger_kind 并轮询到 delivered", async () => {
    const { toast } = await import("sonner");
    stubFetch({ targets: [telegramTarget()] });
    renderNotifications();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.notifications.test") }));
    fireEvent.click(await screen.findByRole("button", { name: t("settings.notifications.trigger.download_submitted") }));

    await waitFor(() => {
      const testCall = fetchMock.mock.calls.find(
        ([input, init]) => String(input).includes("/plugin-targets/target-tg-1/test") && init?.method === "POST",
      );
      expect(testCall).toBeDefined();
      expect(postBody(testCall!)).toMatchObject({ expected_version: 3, trigger_kind: "download_submitted" });
    });
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.notifications.testDelivered")));
  });
});

describe("SettingsNotifications 模板变量", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("点击变量插入到最后编辑的输入框", async () => {
    stubFetch();
    renderNotifications();
    await openNewDialog();

    const event = t("settings.notifications.trigger.download_submitted");
    await userEvent.click(screen.getByRole("combobox", { name: t("settings.notifications.template.modeLabel", { event }) }));
    await userEvent.click(await screen.findByRole("option", { name: t("settings.notifications.template.modeCustom") }));

    const title = (await screen.findAllByLabelText(t("settings.notifications.template.titleField")))[0]!;
    const body = screen.getAllByLabelText(t("settings.notifications.template.bodyField"))[0]!;
    expect(title).toHaveValue("默认标题");

    const catalog = screen.getAllByText(t("settings.notifications.template.variables", { n: 2 }))[0]!.closest("details")!;
    expect(within(catalog as HTMLElement).getByText(t("templateFacts.roots.ep"))).toBeInTheDocument();

    fireEvent.focus(title);
    fireEvent.click(within(catalog as HTMLElement).getByRole("button", { name: /ep\.display/ }));
    expect(title).toHaveValue("默认标题{{ep.display}}");
    expect(body).toHaveValue("默认正文");
  });
});

function webhookTarget(overrides: Partial<PluginTargetConfig> = {}): PluginTargetConfig {
  return {
    ...telegramTarget(),
    id: "target-wh-1",
    name: "自动化",
    adapter_kind: "webhook",
    config: { adapter_kind: "webhook", auth_kind: "none", auth_header_name: null },
    has_signing_secret: false,
    ...overrides,
  };
}

describe("SettingsNotifications 工作流事件", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("新建渠道默认开启下载失败与导入未识别文件；勾选结果按固定顺序提交", async () => {
    stubFetch();
    renderNotifications();
    await openNewDialog();

    const box = (kind: string) => screen.getByRole("checkbox", { name: t(`settings.notifications.trigger.${kind}` as never) });
    expect(box("download_failed")).toBeChecked();
    expect(box("unknown_files_imported")).toBeChecked();
    expect(box("release_discovered")).not.toBeChecked();
    expect(box("subscription_changed")).not.toBeChecked();

    fireEvent.click(box("subscription_changed"));
    fireEvent.click(box("release_discovered"));
    fireEvent.click(box("download_submitted"));

    fireEvent.change(screen.getByLabelText(t("settings.notifications.field.name")), { target: { value: "运维群" } });
    fireEvent.change(screen.getByLabelText(t("settings.notifications.field.botToken")), { target: { value: "123:abc" } });
    fireEvent.change(screen.getByLabelText(t("settings.notifications.field.chatId")), { target: { value: "-100" } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.save") }));

    await waitFor(() => expect(postCalls()).toHaveLength(1));
    const body = postBody(postCalls()[0]!);
    // Checked results submit in the backend registry's (/config/notifications/trigger-kinds) order.
    expect(body.trigger_kinds).toEqual([
      "file_operation_completed",
      "release_discovered",
      "download_failed",
      "subscription_changed",
      "unknown_files_imported",
    ]);
    const set = body.template_set as { locale: string; events: Record<string, unknown> };
    expect(set.locale).toBe("zh-CN");
    // Every trigger event in the registry is written back explicitly; non-customized events use the system default templates.
    expect(Object.keys(set.events).sort()).toEqual([...ALL_TRIGGER_KINDS].sort());
  });

  it("注册表里的每个触发事件都有模板编辑器", async () => {
    stubFetch();
    renderNotifications();
    await openNewDialog();

    for (const kind of ALL_TRIGGER_KINDS) {
      const label = t(`settings.notifications.trigger.${kind}` as never);
      expect(
        screen.getByRole("combobox", { name: t("settings.notifications.template.modeLabel", { event: label }) }),
      ).toBeInTheDocument();
    }
    expect(screen.getByText(t("settings.notifications.template.defaultHint"))).toBeInTheDocument();
  });

  it("触发事件列表加载失败：显示错误并可重试，不静默渲染空列表", async () => {
    stubFetch({ triggerKindsError: true });
    renderNotifications();
    expect(await screen.findByText(t("settings.notifications.triggerKindsFailed"))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("settings.notifications.new") })).toBeDisabled();
  });

  it("编辑时原样保留通过 API 写入的工作流事件模板", async () => {
    const custom = { mode: "custom", title_template: "发现 {{release.title}}", body_template: "{{anime.title}}" };
    const base = templateSet();
    stubFetch({
      targets: [
        telegramTarget({
          template_set: { ...base, events: { ...base.events, release_discovered: custom } } as PluginTargetConfig["template_set"],
        }),
      ],
    });
    renderNotifications();
    fireEvent.click(await screen.findByRole("button", { name: t("settings.notifications.edit") }));
    fireEvent.click(await screen.findByRole("button", { name: t("settings.notifications.save") }));
    await waitFor(() => expect(patchCalls()).toHaveLength(1));
    const set = postBody(patchCalls()[0]!).template_set as { events: Record<string, unknown> };
    expect(set.events.release_discovered).toEqual(custom);
  });

  it("新建 webhook：签名密钥只写入请求；过短不可保存；未填时提示", async () => {
    stubFetch();
    renderNotifications();
    await openNewDialog();
    await userEvent.click(screen.getByRole("combobox", { name: t("settings.notifications.field.type") }));
    await userEvent.click(await screen.findByRole("option", { name: t("settings.notifications.adapter.webhook") }));

    fireEvent.change(screen.getByLabelText(t("settings.notifications.field.name")), { target: { value: "自动化" } });
    fireEvent.change(screen.getByLabelText(t("settings.notifications.field.endpointUrl")), { target: { value: "https://example.test/hook" } });
    expect(screen.getByText(t("settings.notifications.unsignedHint"))).toBeInTheDocument();

    const secretInput = screen.getByLabelText(t("settings.notifications.field.webhookSigningSecret"));
    expect(secretInput).toHaveAttribute("type", "password");
    const save = screen.getByRole("button", { name: t("settings.notifications.save") });
    fireEvent.change(secretInput, { target: { value: "short" } });
    expect(save).toBeDisabled();
    expect(screen.getByText(t("settings.notifications.field.webhookSigningSecretTooShort"))).toBeInTheDocument();

    fireEvent.change(secretInput, { target: { value: "whsec_0123456789abcdef" } });
    expect(save).toBeEnabled();
    expect(screen.queryByText(t("settings.notifications.unsignedHint"))).not.toBeInTheDocument();
    fireEvent.click(save);

    await waitFor(() => expect(postCalls()).toHaveLength(1));
    expect(postBody(postCalls()[0]!)).toMatchObject({
      adapter_kind: "webhook",
      secret: {
        mode: "set",
        value: { adapter_kind: "webhook", endpoint_url: "https://example.test/hook", signing_secret: "whsec_0123456789abcdef" },
      },
    });
  });

  it("编辑 webhook：签名密钥从不回显；保留时不提交密钥；未签名显示提示", async () => {
    stubFetch({ targets: [webhookTarget()] });
    renderNotifications();
    expect(await screen.findByText(t("settings.notifications.unsigned"))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.edit") }));
    await screen.findByRole("dialog");

    expect(screen.getByLabelText(t("settings.notifications.field.webhookSigningSecret"))).toHaveValue("");
    expect(screen.getByText(t("settings.notifications.unsignedHint"))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.save") }));
    await waitFor(() => expect(patchCalls()).toHaveLength(1));
    expect(postBody(patchCalls()[0]!).secret).toEqual({ mode: "keep" });
  });

  it("已签名的 webhook 不显示未签名提示", async () => {
    stubFetch({ targets: [webhookTarget({ has_signing_secret: true })] });
    renderNotifications();
    expect(await screen.findByText("自动化")).toBeInTheDocument();
    expect(screen.queryByText(t("settings.notifications.unsigned"))).not.toBeInTheDocument();
  });
});
