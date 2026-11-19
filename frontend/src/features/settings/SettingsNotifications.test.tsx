import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { t } from "../../lib/i18n";
import type { PluginTargetConfig } from "../../lib/api/client";
import { SettingsNotifications } from "./SettingsNotifications";

// The notification settings regression suite: channel rows (badges, the ⋯ menu's test / history /
//   delete), the two-page edit dialog (channel page with the trigger grid, template page with the
//   per-event editor and live preview), and the secret three-state / If-Match contract.

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
  "replacement_deletion_failed",
  "replacement_applied",
  "file_displaced",
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

function templateCatalogBody(catalogVersion = "notification-catalog-v1", locale = "zh-CN") {
  const english = locale === "en";
  return {
    language_version: "bansoko-template-1",
    catalog_version: catalogVersion,
    trigger_kind: "download_submitted",
    variables: [
      { name: "anime.title", label: "Anime title", type: "string", nullable: false, source: "anime", sample_value: "示例动画", phase: "release", label_key: "facts.anime.title.label" },
      { name: "ep.display", label: "Episode", type: "string", nullable: true, source: "ep", sample_value: "07", phase: "release", label_key: "facts.ep.display.label" },
      { name: "image", label: "Image", type: "string", nullable: false, source: "system" },
      { name: "bgm.image", label: "Bangumi image", type: "string", nullable: true, source: "bgm" },
    ],
    functions: [],
    default_template: english
      ? { mode: "custom", title_template: "Default title", body_template: "Default body" }
      : { mode: "custom", title_template: "默认标题", body_template: "默认正文" },
    sample: { values: {} },
  };
}

function previewPresentation(overrides: Record<string, unknown> = {}) {
  return {
    telegram: { mode: "sendPhoto", text: "<b>预览标题</b>\n\n预览正文", title: "预览标题", body_parts: [{ text: "预览正文", link: null }], text_units: 20, image_omitted: false, truncated: false },
    feishu: {
      title: "预览标题",
      content: [[{ tag: "text", text: "预览正文" }]],
      image: null,
      image_omitted: false,
    },
    webhook: {
      message: {
        title: "预览标题",
        body: "预览正文",
        image: { source: "automatic", placeholder: true },
        links: [{ provider: "bgm", label: "Bangumi", url: "https://bgm.tv/subject/123" }],
      },
    },
    ...overrides,
  };
}

function previewBody(overrides: Record<string, unknown> = {}) {
  return {
    language_version: "bansoko-template-1",
    catalog_version: "notification-catalog-v1",
    title: "预览标题",
    body: "预览正文",
    body_parts: [{ text: "预览正文", link: null }],
    valid: true,
    attachments: {
      image: { source: "automatic" },
      links: [{ provider: "bgm", label: "Bangumi", url: "https://bgm.tv/subject/123" }],
    },
    presentation: previewPresentation(),
    errors: [],
    warnings: [],
    variables: [],
    ...overrides,
  };
}

function stubFetch(
  options: {
    targets?: PluginTargetConfig[];
    patchError?: unknown;
    triggerKinds?: string[];
    triggerKindsError?: boolean;
    preview?: () => Record<string, unknown>;
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
    if (init?.method === "DELETE" && url.includes("/plugin-targets/")) {
      return new Response(null, { status: 204 });
    }
    if (init?.method === "POST" && url.includes("/template-previews/notification")) {
      return new Response(JSON.stringify(options.preview ? options.preview() : previewBody()), {
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
      const params = new URL(url, "http://localhost").searchParams;
      const version = params.get("catalog_version") ?? "notification-catalog-v1";
      const locale = params.get("locale") ?? "zh-CN";
      return new Response(JSON.stringify(templateCatalogBody(version, locale)), {
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

function deleteCalls() {
  return fetchMock.mock.calls.filter(
    ([input, init]) => String(input).includes("/plugin-targets/") && init?.method === "DELETE",
  );
}

function previewCalls() {
  return fetchMock.mock.calls.filter(
    ([input, init]) => String(input).includes("/template-previews/notification") && init?.method === "POST",
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

/** From an open channel-page dialog onto the template page. */
async function openTemplatePage() {
  fireEvent.click(await screen.findByRole("button", { name: t("settings.notifications.template.editEntry") }));
  const dialog = await screen.findByRole("dialog");
  await waitFor(() => expect(dialog).toHaveTextContent(t("settings.notifications.template.pageTitle")));
  return dialog;
}

describe("SettingsNotifications 渠道列表", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("列表行：类型、启用、密钥徽章与第二行的已开通事件；仅已签名的 webhook 显示签名徽章", async () => {
    stubFetch({
      targets: [
        telegramTarget(),
        webhookTarget({ has_signing_secret: true }),
        webhookTarget({ id: "target-wh-2", name: "未签名渠道", has_signing_secret: false }),
      ],
    });
    renderNotifications();

    const signedRow = (await screen.findByText("自动化")).closest("li")!;
    expect(within(signedRow as HTMLElement).getByText(t("settings.notifications.signed"))).toBeInTheDocument();
    const unsignedRow = screen.getByText("未签名渠道").closest("li")!;
    expect(within(unsignedRow as HTMLElement).queryByText(t("settings.notifications.signed"))).not.toBeInTheDocument();

    const tgRow = screen.getByText("运维群").closest("li")!;
    expect(within(tgRow as HTMLElement).getByText(t("settings.notifications.enabledEvents"))).toBeInTheDocument();
    expect(within(tgRow as HTMLElement).getByText(t("settings.notifications.trigger.download_submitted"))).toBeInTheDocument();
    expect(within(tgRow as HTMLElement).queryByText(t("settings.notifications.trigger.download_failed"))).not.toBeInTheDocument();
  });

  it("⋯ 菜单的测试子菜单只列已开通事件；测试按目标 id 提交并轮询到 delivered", async () => {
    const { toast } = await import("sonner");
    stubFetch({ targets: [telegramTarget()] });
    renderNotifications();
    const user = userEvent.setup();

    const row = (await screen.findByText("运维群")).closest("li")!;
    await user.click(within(row as HTMLElement).getByRole("button", { name: t("settings.notifications.moreActions") }));
    await user.click(await screen.findByRole("menuitem", { name: t("settings.notifications.test") }));
    // Only the target's own trigger_kinds are offered.
    const menu = await screen.findByRole("menuitem", { name: t("settings.notifications.trigger.download_submitted") });
    expect(screen.queryByRole("menuitem", { name: t("settings.notifications.trigger.download_failed") })).not.toBeInTheDocument();
    // The submenu popup keeps pointer-events: none while its opening transition runs; jsdom never
    //   finishes transitions, so the item is clicked with fireEvent (as overlays.test.tsx does).
    fireEvent.click(menu);

    await waitFor(() => {
      const testCall = fetchMock.mock.calls.find(
        ([input, init]) => String(input).includes("/plugin-targets/target-tg-1/test") && init?.method === "POST",
      );
      expect(testCall).toBeDefined();
      expect(postBody(testCall!)).toMatchObject({ expected_version: 3, trigger_kind: "download_submitted" });
    });
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.notifications.testDelivered")));
  });

  it("⋯ 菜单删除：确认对话框确认后才发出 DELETE", async () => {
    stubFetch({ targets: [telegramTarget()] });
    renderNotifications();
    const user = userEvent.setup();

    const row = (await screen.findByText("运维群")).closest("li")!;
    await user.click(within(row as HTMLElement).getByRole("button", { name: t("settings.notifications.moreActions") }));
    fireEvent.click(await screen.findByRole("menuitem", { name: t("settings.notifications.delete") }));

    expect(deleteCalls()).toHaveLength(0);
    const confirmDialog = await screen.findByRole("alertdialog");
    expect(confirmDialog).toHaveTextContent(t("settings.notifications.deleteTitle", { name: "运维群" }));
    await user.click(within(confirmDialog).getByRole("button", { name: t("settings.notifications.delete") }));
    await waitFor(() => expect(deleteCalls()).toHaveLength(1));
    expect(String(deleteCalls()[0]![0])).toContain("/plugin-targets/target-tg-1");
  });
});

describe("SettingsNotifications 渠道页", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("新建 telegram：缺 chat_id 时保存禁用；补全后提交 config + set secret", async () => {
    stubFetch();
    renderNotifications();
    await openNewDialog();

    expect(screen.getByRole("dialog")).toHaveTextContent(t("settings.notifications.newTitle"));
    // The enable switch lives in the footer now.
    expect(screen.getByRole("checkbox", { name: t("settings.notifications.enableChannel") })).toBeChecked();

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

  it("保存时模板编译失败：列出事件、字段、位置和原因，点击跳到对应事件的模板编辑器", async () => {
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
    const errorButton = await screen.findByRole("button", {
      name: t("settings.notifications.template.errorLine", {
        where,
        position: t("settings.notifications.template.errorAt", { n: 7 }),
        message: "未知变量 \"anime.nope\"",
      }),
    });
    fireEvent.click(errorButton);
    // Lands on the template page with that event's editor selected.
    const dialog = await screen.findByRole("dialog");
    await waitFor(() => expect(dialog).toHaveTextContent(t("settings.notifications.template.pageTitle")));
    expect(dialog).toHaveTextContent(t("settings.notifications.trigger.file_operation_completed"));
    expect(screen.getByRole("radio", { name: t("settings.notifications.template.modeDefault") })).toBeInTheDocument();
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
    fireEvent.click(await screen.findByRole("button", { name: t("settings.notifications.edit") }));
    await screen.findByRole("dialog");

    expect(screen.getByLabelText(t("settings.notifications.field.webhookSigningSecret"))).toHaveValue("");
    expect(screen.getByText(t("settings.notifications.unsignedHint"))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.save") }));
    await waitFor(() => expect(patchCalls()).toHaveLength(1));
    expect(postBody(patchCalls()[0]!).secret).toEqual({ mode: "keep" });
  });
});

describe("SettingsNotifications 模板页", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("事件列表按启用状态分组并列出注册表的全部事件，标注自定义/默认", async () => {
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
    const dialog = await openTemplatePage();

    const list = dialog.querySelector(".settings-notifications-tpl-list")!;
    expect(within(list as HTMLElement).getByText(new RegExp(t("settings.notifications.template.enabledGroup")))).toBeInTheDocument();
    expect(within(list as HTMLElement).getByText(new RegExp(t("settings.notifications.template.disabledGroup")))).toBeInTheDocument();
    for (const kind of ALL_TRIGGER_KINDS) {
      expect(within(list as HTMLElement).getByText(t(`settings.notifications.trigger.${kind}` as never))).toBeInTheDocument();
    }
    // The release_discovered event keeps its custom badge, the rest default.
    expect(within(list as HTMLElement).getAllByText(t("settings.notifications.template.badgeCustom"))).toHaveLength(1);
  });

  it("渠道页触发事件区显示模板统计并带编辑模板入口；自定义事件在复选框上有圆点", async () => {
    const custom = { mode: "custom", title_template: "x", body_template: "y" };
    const base = templateSet();
    stubFetch({
      targets: [
        telegramTarget({
          template_set: { ...base, events: { ...base.events, download_submitted: custom } } as PluginTargetConfig["template_set"],
        }),
      ],
    });
    renderNotifications();
    fireEvent.click(await screen.findByRole("button", { name: t("settings.notifications.edit") }));
    await screen.findByRole("dialog");

    expect(screen.getByText(t("settings.notifications.customTemplateCount", { n: 1 }))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("settings.notifications.template.editEntry") })).toBeInTheDocument();
  });

  it("切换自定义模板从默认文案开始；点击变量插入到最后编辑的输入框", async () => {
    stubFetch();
    renderNotifications();
    await openNewDialog();
    await openTemplatePage();

    // The pane opens on the registry's first event (download_submitted).
    await userEvent.click(await screen.findByRole("radio", { name: t("settings.notifications.template.modeCustom") }));

    const title = await screen.findByLabelText(t("settings.notifications.template.titleField"));
    const body = screen.getByLabelText(t("settings.notifications.template.bodyField"));
    await waitFor(() => expect(title).toHaveValue("默认标题"));

    const catalog = screen.getByText(t("settings.notifications.template.variables", { n: 4 })).closest("details")!;
    expect(within(catalog as HTMLElement).getByText(t("templateFacts.roots.ep"))).toBeInTheDocument();

    fireEvent.focus(title);
    fireEvent.click(within(catalog as HTMLElement).getByRole("button", { name: /ep\.display/ }));
    expect(title).toHaveValue("默认标题{{ep.display}}");
    expect(body).toHaveValue("默认正文");
  });

  it("编辑时实时按渠道渲染预览：请求携带当前模板文本，结果按 Telegram 形态绘制", async () => {
    stubFetch();
    renderNotifications();
    await openNewDialog();
    await openTemplatePage();

    await userEvent.click(await screen.findByRole("radio", { name: t("settings.notifications.template.modeCustom") }));
    const title = await screen.findByLabelText(t("settings.notifications.template.titleField"));
    await waitFor(() => expect(title).toHaveValue("默认标题"));
    fireEvent.change(title, { target: { value: "标题 {{anime.title}}" } });

    await screen.findByText("预览正文");
    await waitFor(() => {
      const call = previewCalls().at(-1);
      expect(call).toBeDefined();
      expect(postBody(call!)).toMatchObject({
        trigger_kind: "download_submitted",
        title_template: "标题 {{anime.title}}",
        body_template: "默认正文",
      });
    });
    expect(screen.getByText(t("settings.notifications.template.previewAs", { adapter: t("settings.notifications.adapter.telegram") }))).toBeInTheDocument();
  });

  it("恢复默认把事件切回系统模板", async () => {
    stubFetch();
    renderNotifications();
    await openNewDialog();
    await openTemplatePage();

    await userEvent.click(await screen.findByRole("radio", { name: t("settings.notifications.template.modeCustom") }));
    await screen.findByLabelText(t("settings.notifications.template.titleField"));
    await userEvent.click(screen.getByRole("button", { name: t("settings.notifications.template.resetDefault") }));

    expect(screen.queryByLabelText(t("settings.notifications.template.titleField"))).not.toBeInTheDocument();
    await screen.findByText(t("settings.notifications.template.followDefault"));
  });

  it("编辑英文渠道：模板目录按渠道语言读取，自定义模板以英文默认开始，保存保留该语言", async () => {
    const base = templateSet();
    stubFetch({
      targets: [
        telegramTarget({
          template_set: { ...base, locale: "en" } as PluginTargetConfig["template_set"],
        }),
      ],
    });
    renderNotifications();
    fireEvent.click(await screen.findByRole("button", { name: t("settings.notifications.edit") }));
    await openTemplatePage();

    const catalogCall = fetchMock.mock.calls.find(([input]) =>
      String(input).includes("/template-catalogs/notification/"),
    );
    expect(String(catalogCall?.[0] ?? "")).toContain("locale=en");

    await userEvent.click(await screen.findByRole("radio", { name: t("settings.notifications.template.modeCustom") }));
    const title = await screen.findByLabelText(t("settings.notifications.template.titleField"));
    await waitFor(() => expect(title).toHaveValue("Default title"));

    fireEvent.click(screen.getByRole("button", { name: t("settings.notifications.save") }));
    await waitFor(() => expect(patchCalls()).toHaveLength(1));
    const set = postBody(patchCalls()[0]!).template_set as { locale: string };
    expect(set.locale).toBe("en");
  });

  it("点击图片变量把 image(...) 插入正文并独立成行", async () => {
    stubFetch();
    renderNotifications();
    await openNewDialog();
    await openTemplatePage();
    await userEvent.click(await screen.findByRole("radio", { name: t("settings.notifications.template.modeCustom") }));

    const title = await screen.findByLabelText(t("settings.notifications.template.titleField"));
    const body = screen.getByLabelText(t("settings.notifications.template.bodyField")) as HTMLTextAreaElement;
    const catalog = screen.getByText(t("settings.notifications.template.variables", { n: 4 })).closest("details")!;

    // An image is a body attachment even when the title was the last focused field.
    fireEvent.focus(title);
    fireEvent.click(within(catalog as HTMLElement).getByRole("button", { name: /bgm\.image/ }));

    expect(title).toHaveValue("默认标题");
    expect(body.value.split("\n")).toContain("{{image(bgm.image)}}");
  });

  it("Telegram 预览：说明文字超过 1024 时标为普通消息且不带图片", async () => {
    const longBody = "x".repeat(1100);
    stubFetch({
      preview: () =>
        previewBody({
          body: longBody,
          body_parts: [{ text: longBody, link: null }],
          attachments: { image: { source: "automatic" }, links: [] },
          presentation: previewPresentation({
            telegram: { mode: "sendMessage", text: longBody, title: "", body_parts: [{ text: longBody, link: null }], text_units: 1100, image_omitted: true, truncated: false },
          }),
        }),
    });
    renderNotifications();
    await openNewDialog();
    await openTemplatePage();

    await screen.findByText(t("settings.notifications.template.telegramImageOmitted"));
    expect(screen.queryByText(t("settings.notifications.template.imageNote"))).not.toBeInTheDocument();
  });

  it("Telegram 预览：无图片且说明文字超过 4096 时提示截断", async () => {
    const longBody = "x".repeat(5000);
    const fittedBody = `${"x".repeat(4095)}…`;
    stubFetch({
      preview: () =>
        previewBody({
          body: longBody,
          body_parts: [{ text: longBody, link: null }],
          attachments: { image: null, links: [] },
          presentation: previewPresentation({
            telegram: { mode: "sendMessage", text: fittedBody, title: "", body_parts: [{ text: fittedBody, link: null }], text_units: 4096, image_omitted: false, truncated: true },
          }),
        }),
    });
    renderNotifications();
    await openNewDialog();
    await openTemplatePage();

    await screen.findByText(t("settings.notifications.template.telegramTruncated"));
    expect(screen.getByText(fittedBody)).toBeInTheDocument();
    expect(screen.queryByText(longBody)).not.toBeInTheDocument();
  });

  it("飞书预览：没有图片地址时不显示图片", async () => {
    stubFetch({
      preview: () =>
        previewBody({
          attachments: { image: { source: "automatic" }, links: [] },
          presentation: previewPresentation({
            feishu: {
              title: "预览标题",
              content: [[{ tag: "text", text: "预览正文" }]],
              image: null,
              image_omitted: true,
            },
          }),
        }),
    });
    renderNotifications();
    await openNewDialog();
    await userEvent.click(screen.getByRole("combobox", { name: t("settings.notifications.field.type") }));
    await userEvent.click(await screen.findByRole("option", { name: t("settings.notifications.adapter.feishu") }));
    await openTemplatePage();

    await screen.findByText(t("settings.notifications.template.feishuImageNoUrl"));
  });

  it("飞书预览：有图片地址时显示为带来源名称的链接", async () => {
    stubFetch({
      preview: () =>
        previewBody({
          attachments: {
            image: { source: "custom", url: "https://example.com/a.jpg" },
            links: [],
          },
          presentation: previewPresentation({
            feishu: {
              title: "预览标题",
              content: [
                [{ tag: "text", text: "预览正文" }],
                [{ tag: "a", text: "Image", href: "https://example.com/a.jpg" }],
              ],
              image: { label: "Image", url: "https://example.com/a.jpg" },
              image_omitted: false,
            },
          }),
        }),
    });
    renderNotifications();
    await openNewDialog();
    await userEvent.click(screen.getByRole("combobox", { name: t("settings.notifications.field.type") }));
    await userEvent.click(await screen.findByRole("option", { name: t("settings.notifications.adapter.feishu") }));
    await openTemplatePage();

    // The image link is Feishu's own final post paragraph, rendered by the backend presentation.
    await screen.findByText("Image");
  });

  it("Webhook 预览：只展示 message 部分并说明完整 envelope 的其它字段", async () => {
    stubFetch();
    renderNotifications();
    await openNewDialog();
    await userEvent.click(screen.getByRole("combobox", { name: t("settings.notifications.field.type") }));
    await userEvent.click(await screen.findByRole("option", { name: t("settings.notifications.adapter.webhook") }));
    await openTemplatePage();

    await screen.findByText(t("settings.notifications.template.webhookEnvelopeNote"));
    const pre = document.querySelector(".settings-notifications-preview-wh");
    expect(pre?.textContent).toContain('"event": "download_submitted"');
    expect(pre?.textContent).toContain('"links"');
  });
});

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
});
