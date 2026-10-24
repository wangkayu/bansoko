import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DiagnosticEpisode, ObservabilityRecord } from "../lib/api/client";
import { LogDialog } from "./LogDialog";
import { useLogDialogStore } from "./logDialogStore";
import { t } from "../lib/i18n";

// Base UI Select needs a real pointer sequence (it commits on pointerup) and
// exposes items as role="option". user-event drives the full sequence.
async function pickOption(selectName: string, optionLabel: string) {
  const user = userEvent.setup();
  await user.click(screen.getByRole("combobox", { name: selectName }));
  await user.click(await screen.findByRole("option", { name: optionLabel }));
}

// A diagnostic's raw last_error lives under its details: expand the first still-collapsed details before asserting the raw text.
async function openDiagnosticDetails(text: string) {
  const toggles = await screen.findAllByRole("button", { name: t("errorDetails.toggle"), expanded: false });
  fireEvent.click(toggles[0]!);
  await screen.findByText(new RegExp(text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
}

// The LogDialog business mainline: events query only business/plugin (multi-value kind);
// business rows render the kind label and plugin row titles follow the trigger mapping; the
// small outcome/anime filters and cursor "load more" pagination; the diagnostics tab has only
// severity/component filters. The time range lives in one popover. Events are compact
// single-line rows; adjacent same-result events collapse.

type BusinessKind = Extract<ObservabilityRecord, { record_type: "business" }>["entry"]["kind"];

const businessRecord: ObservabilityRecord = {
  record_type: "business",
  entry: {
    id: "business-1",
    subscription_id: "subscription-1",
    managed_anime_id: "anime-1",
    kind: "subscription_created",
    outcome: "success",
    occurred_at: "2026-07-21T08:00:00Z",
    title: "沉默魔女的秘密",
    summary: "订阅已创建",
    release_id: "release-1",
    dedupe_key: "dedupe-1",
    retention_until: "2026-07-28T08:00:00Z",
  },
};

const pluginRecord: ObservabilityRecord = {
  record_type: "plugin",
  delivery: {
    id: "delivery-1",
    plugin_target_config_id: "target-1",
    plugin_target_config_version: 1,
    adapter_kind: "telegram",
    adapter_config_snapshot: { adapter_kind: "telegram", chat_id: "42" },
    config_status: "available",
    delivery_kind: "business",
    trigger_kind: "download_submitted",
    source_owner_type: "download_task",
    source_owner_id: "task-1",
    source_owner_version: 1,
    source_result_key: "result-1",
    managed_anime_id: "anime-1",
    state: "delivered",
    version: 1,
    idempotency_key: "idem-1",
    payload_schema_version: 1,
    payload: { schema_version: 1, trigger_kind: "download_submitted", facts: {} },
    payload_status: "available",
    payload_digest: "digest-1",
    template_language_version: "bansoko-template-1",
    template_catalog_version: "notification-catalog-v1",
    title_template_snapshot: "{title}",
    body_template_snapshot: "{body}",
    target_display: "测试频道",
    attempt_count: 1,
    created_at: "2026-07-21T08:00:00Z",
    updated_at: "2026-07-21T08:05:00Z",
    retention_until: "2026-07-28T08:00:00Z",
  },
};

// Routine records must not appear on the events tab (the query kind is narrowed; rendering has no branch for them either).
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

let fetchMock: ReturnType<typeof vi.fn>;
let recordsPages: Array<{ records: ObservabilityRecord[]; next_cursor: string | null }>;
let diagnosticsPages: Array<{ episodes: DiagnosticEpisode[]; next_cursor: string | null }>;

// The listAnime stub: the option source of the anime filter.
function animeListPayload(lifecycleState?: "active" | "retired") {
  return [
    {
      managed_anime: { id: "anime-1", title: "沉默魔女的秘密", title_override: null, version: 1 },
      subscription: {
        id: "subscription-1",
        managed_anime_id: "anime-1",
        origin: "mikan",
        origin_entry_ref: "mikan:1",
        instance_label: null,
        state: "active",
        ...(lifecycleState ? { lifecycle_state: lifecycleState, lifecycle_epoch: 1 } : {}),
        version: 1,
      },
      metadata_hold: { held: false, missing: [] },
    },
  ];
}

let animeItems = animeListPayload();

function stubFetch() {
  animeItems = animeListPayload();
  const pluginDelivery = (pluginRecord as Extract<ObservabilityRecord, { record_type: "plugin" }>).delivery;
  fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    let body: unknown = [];
    if (url.includes("/plugin-deliveries/")) {
      body = {
        delivery: {
          ...pluginDelivery,
          last_error: null,
        },
        attempts: [
          {
            plugin_delivery_id: "delivery-1",
            attempt_number: 1,
            started_at: "2026-07-21T08:04:00Z",
            response_status: 200,
            error: null,
          },
        ],
      };
    } else if (url.includes("/observability/records")) {
      const cursor = new URL(url, "http://localhost").searchParams.get("cursor");
      body = cursor ? (recordsPages[1] ?? { records: [], next_cursor: null }) : recordsPages[0];
    } else if (url.includes("/observability/diagnostics")) {
      const cursor = new URL(url, "http://localhost").searchParams.get("cursor");
      body = cursor ? (diagnosticsPages[1] ?? { episodes: [], next_cursor: null }) : diagnosticsPages[0];
    } else if (new URL(url, "http://localhost").pathname === "/api/v1/anime") {
      body = animeItems;
    }
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
}

function recordsRequestUrls(): string[] {
  return fetchMock.mock.calls.map(([input]) => String(input)).filter((url) => url.includes("/observability/records"));
}

function diagnosticsRequestUrls(): string[] {
  return fetchMock.mock.calls.map(([input]) => String(input)).filter((url) => url.includes("/observability/diagnostics"));
}

function renderLogDialog() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <LogDialog />
    </QueryClientProvider>,
  );
}

// The time range lives in a popover: open it before changing the inputs.
async function openTimeRange() {
  fireEvent.click(screen.getByRole("button", { name: new RegExp(t("log.timeRange")) }));
  await screen.findByLabelText(t("log.startTime"));
}

describe("LogDialog 事件（业务主线）", () => {
  beforeEach(() => {
    recordsPages = [{ records: [businessRecord, pluginRecord, routineRecord], next_cursor: null }];
    stubFetch();
    useLogDialogStore.getState().openDialog();
  });

  afterEach(() => {
    useLogDialogStore.getState().closeDialog();
    vi.unstubAllGlobals();
  });

  it("空记录显示空态", async () => {
    recordsPages = [{ records: [], next_cursor: null }];
    renderLogDialog();
    expect(await screen.findByText(t("log.emptyRecords"))).toBeInTheDocument();
  });

  it("日志用 M 档固定高度，筛选或加载时弹窗不跳动", async () => {
    renderLogDialog();
    await screen.findByText("订阅已创建");
    const dialog = screen.getByRole("dialog", { name: t("log.title") });
    expect(dialog.className).toContain("tw:h-[min(640px,calc(100dvh-2rem))]");
    expect(dialog.className).not.toContain("tw:max-h-[min(640px");
  });

  it("日志恰好保留事件和诊断两个顶层 Tab", async () => {
    renderLogDialog();
    await screen.findByText("订阅已创建");

    const tabs = screen.getAllByRole("tab");
    expect(tabs).toHaveLength(2);
    expect(tabs.map((tab) => tab.textContent)).toEqual([t("log.tabRecords"), t("log.tabDiagnostics")]);
    expect(t("log.tabRecords")).toBe("事件");
  });

  it("打开时焦点落在标题上，不落在关闭按钮（UI 评审 A3）", async () => {
    renderLogDialog();
    await screen.findByText("订阅已创建");
    const dialog = screen.getByRole("dialog", { name: t("log.title") });
    await waitFor(() => expect(within(dialog).getByRole("heading", { name: t("log.title") })).toHaveFocus());
    expect(within(dialog).getByRole("button", { name: t("ui.close") })).not.toHaveFocus();
  });

  it("筛选压成一行：状态、番剧和一个时间范围按钮，不再平铺两个时间输入", async () => {
    renderLogDialog();
    await screen.findByText("订阅已创建");
    expect(screen.getByRole("combobox", { name: t("log.status") })).toHaveTextContent(t("log.allStatus"));
    expect(screen.getByRole("combobox", { name: t("log.anime") })).toBeInTheDocument();
    expect(screen.queryByLabelText(t("log.startTime"))).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: new RegExp(t("log.timeRange")) })).toHaveTextContent(t("log.timeAny"));
  });

  it("运行记录只按 kind=business/plugin 多值查询", async () => {
    renderLogDialog();
    await screen.findByText("订阅已创建");

    const url = recordsRequestUrls()[0]!;
    const params = new URL(url, "http://localhost").searchParams;
    expect(params.getAll("kind")).toEqual(["business", "plugin"]);
  });

  it("business 行渲染 kind 中文徽标，plugin 行标题走 trigger 映射，routine 行不出现", async () => {
    renderLogDialog();
    await screen.findByText("订阅已创建");

    // business: the kind badge mapping plus the backend's summary kept as written; internal lineage never enters the UI.
    expect(screen.getByText("创建订阅")).toBeInTheDocument();
    expect(screen.queryByText("release-1")).not.toBeInTheDocument();
    expect(screen.queryByText("corr-1")).not.toBeInTheDocument();
    // plugin: the row title is the trigger label mapping, not a bare snake_case.
    expect(screen.getByText("下载已提交")).toBeInTheDocument();
    expect(screen.queryByText("download_submitted")).not.toBeInTheDocument();
    // plugin: the target description renders the redacted target_display, not a bare adapter_kind.
    expect(screen.getByText("测试频道")).toBeInTheDocument();
    expect(screen.queryByText("telegram")).not.toBeInTheDocument();
    // Routine execution evidence never appears.
    expect(screen.queryByText("目录刷新")).not.toBeInTheDocument();
    expect(screen.queryByText("常规活动")).not.toBeInTheDocument();
  });

  it("原因只在失败时显示，成功或提示的原因码不重复摘要", async () => {
    recordsPages = [
      {
        records: [
          { record_type: "business", entry: { ...businessRecord.entry, release_id: null, id: "ok", kind: "acquisition_settled", summary: "获取完成", reason_code: "acquisition_succeeded" } },
          { record_type: "business", entry: { ...businessRecord.entry, release_id: null, id: "info", kind: "policy_changed", outcome: "info", summary: "规则策略已更新", reason_code: "rules_policy_changed" } },
          { record_type: "business", entry: { ...businessRecord.entry, release_id: null, id: "fail", kind: "file_alignment_failed", outcome: "failure", summary: "入库失败", reason_code: "placement_sink_failed" } },
          { record_type: "business", entry: { ...businessRecord.entry, release_id: null, id: "fail-unknown", kind: "file_alignment_failed", outcome: "failure", summary: "入库失败：未知", reason_code: "SOME_NEW_CODE" } },
        ],
        next_cursor: null,
      },
    ];
    renderLogDialog();
    const list = await screen.findByRole("list", { name: t("log.recordsAria") });
    const rows = within(list).getAllByRole("listitem");
    expect(rows[0].textContent).not.toContain(t("log.reason", { reason: "" }).trim());
    expect(rows[1].textContent).toContain(t("status.logKind.policy_changed"));
    expect(rows[1].textContent).not.toContain(t("common.unknownReason"));
    expect(rows[2].textContent).toContain(t("log.reason", { reason: t("reason.placement_sink_failed") }));
    expect(within(rows[3]).getByTitle("SOME_NEW_CODE").textContent).toBe(t("log.reason", { reason: t("common.unknownReason") }));
  });

  it("事件不再显示解析器标签", async () => {
    const recorder = businessRecord as Extract<ObservabilityRecord, { record_type: "business" }>;
    recordsPages = [
      {
        records: [
          {
            ...recorder,
            entry: {
              ...recorder.entry,
              details: {
                verdict: "accepted_pending_download",
                episode_state: "resolved",
                episode_shape: "single",
                parser: { kind: "regex", offset: 0, parse_status: "value", parse_shape: "single" },
              },
            },
          },
        ],
        next_cursor: null,
      },
    ];
    renderLogDialog();
    await screen.findByText("订阅已创建");

    expect(screen.queryByText(/regex/)).not.toBeInTheDocument();
    expect(screen.queryByText(/sha256:/)).not.toBeInTheDocument();
  });

  it("元数据阻塞的种子评估只写长形原因，缺什么读 details.metadata_missing", async () => {
    const recorder = businessRecord as Extract<ObservabilityRecord, { record_type: "business" }>;
    const held = (id: string, missing?: ("bgm" | "tmdb")[]) => ({
      ...recorder,
      entry: {
        ...recorder.entry,
        id,
        kind: "release_processed" as const,
        outcome: "info" as const,
        summary: "评估结果：等待关联元数据",
        reason_code: "metadata_incomplete",
        details: { verdict: "waiting_metadata", ...(missing ? { metadata_missing: missing } : {}) },
      },
    });
    recordsPages = [{ records: [held("held-1", ["tmdb"]), held("held-legacy")], next_cursor: null }];
    renderLogDialog();

    expect(await screen.findByText(t("anime.metadata.paused", { names: "TMDB" }))).toBeInTheDocument();
    // Older records have no metadata_missing: nothing is named.
    expect(screen.getByText(t("anime.metadata.paused", { names: t("anime.metadata.unknownNames") }))).toBeInTheDocument();
    expect(screen.queryByText(/评估结果/)).not.toBeInTheDocument();
  });

  it("相邻且结果相同的事件折叠成一行带计数，可展开", async () => {
    const recorder = businessRecord as Extract<ObservabilityRecord, { record_type: "business" }>;
    const held = (id: string, title: string) => ({
      ...recorder,
      entry: {
        ...recorder.entry,
        id,
        title,
        kind: "release_processed" as const,
        outcome: "info" as const,
        summary: "评估结果：等待关联元数据",
        reason_code: "metadata_incomplete",
        details: { verdict: "waiting_metadata", metadata_missing: ["tmdb" as const] },
      },
    });
    recordsPages = [
      { records: [held("h1", "种子 A"), held("h2", "种子 B"), held("h3", "种子 C"), businessRecord, held("h4", "种子 D")], next_cursor: null },
    ];
    renderLogDialog();

    // Unfiltered, a merged row writes the anime name + the count.
    const toggle = await screen.findByRole("button", { name: `沉默魔女的秘密 · ${t("log.groupReleases", { n: 3 })}` });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    // Collapsed, the group's inner titles are not listed; single events outside the group show as usual and are never folded across it.
    expect(screen.queryByText("种子 A")).not.toBeInTheDocument();
    expect(screen.getByText("种子 D")).toBeInTheDocument();
    expect(screen.getAllByText(t("anime.metadata.paused", { names: "TMDB" }))).toHaveLength(2);

    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    for (const title of ["种子 A", "种子 B", "种子 C"]) expect(screen.getByText(title)).toBeInTheDocument();
  });

  it("一次下载合成一行「字幕组 · 第 N 集 · 结果」，展开按时间列出各步；筛选单番时不重复番剧名", async () => {
    const recorder = businessRecord as Extract<ObservabilityRecord, { record_type: "business" }>;
    const context = { group_name: "NEST", episodes: [2], release_title: "[NEST] 沉默魔女的秘密 - 02.mkv" };
    const step = (id: string, kind: BusinessKind, second: number, summary: string, details: Record<string, unknown> = {}) => ({
      ...recorder,
      entry: {
        ...recorder.entry,
        id,
        kind,
        outcome: "success" as const,
        occurred_at: `2026-07-21T08:40:${String(second).padStart(2, "0")}Z`,
        summary,
        release_id: "release-2",
        details: { ...context, ...details },
      },
    });
    recordsPages = [
      {
        records: [
          step("settled", "acquisition_settled", 50, "获取完成", { item_count: 1 }),
          step("file", "file_alignment_completed", 20, "文件已入库：S01E02.mkv", { file_name: "S01E02.mkv", library_folder: "沉默魔女的秘密/Season 1" }),
          step("download", "download_changed", 0, "下载完成"),
        ],
        next_cursor: null,
      },
    ];
    renderLogDialog();
    const episode = t("records.episode", { n: 2 });
    const toggle = await screen.findByRole("button", { name: `沉默魔女的秘密 · NEST · ${episode}` });
    const list = screen.getByRole("list", { name: t("log.recordsAria") });
    expect(within(list).getAllByRole("listitem")).toHaveLength(1);
    expect(list.textContent).toContain(t("log.run.result.imported"));
    expect(screen.queryByText("文件已入库：S01E02.mkv")).not.toBeInTheDocument();

    fireEvent.click(toggle);
    const steps = screen.getByRole("list", { name: t("log.run.stepsAria") });
    expect(steps.textContent).toContain(`${t("log.step.imported", { name: "S01E02.mkv" })} · 沉默魔女的秘密/Season 1`);
    const order = [t("status.logKind.download_changed"), t("status.logKind.file_alignment_completed"), t("status.logKind.acquisition_settled")];
    const text = steps.textContent ?? "";
    expect(order.map((label) => text.indexOf(label))).toEqual([...order.map((label) => text.indexOf(label))].sort((a, b) => a - b));
  });

  it("筛选到一部番剧时运行行不写番剧名", async () => {
    const recorder = businessRecord as Extract<ObservabilityRecord, { record_type: "business" }>;
    recordsPages = [
      {
        records: [
          {
            ...recorder,
            entry: {
              ...recorder.entry,
              id: "only",
              kind: "download_changed",
              outcome: "info",
              summary: "已提交下载",
              reason_code: "submitted",
              release_id: "release-3",
              details: { group_name: "NEST", episodes: [3] },
            },
          },
        ],
        next_cursor: null,
      },
    ];
    useLogDialogStore.getState().openDialog("anime-1");
    renderLogDialog();
    expect(await screen.findByRole("button", { name: `NEST · ${t("records.episode", { n: 3 })}` })).toBeInTheDocument();
    expect(screen.queryByText(/沉默魔女的秘密 · NEST/)).not.toBeInTheDocument();
  });

  it("元数据阻塞的事件带修复动作：打开该番剧配置的元数据页签", async () => {
    const recorder = businessRecord as Extract<ObservabilityRecord, { record_type: "business" }>;
    recordsPages = [
      {
        records: [
          {
            ...recorder,
            entry: {
              ...recorder.entry,
              id: "held",
              kind: "release_processed",
              outcome: "info",
              summary: "评估结果：等待关联元数据",
              reason_code: "metadata_incomplete",
              details: { verdict: "waiting_metadata", metadata_missing: ["tmdb"] },
            },
          },
        ],
        next_cursor: null,
      },
    ];
    const onOpenAnimeMetadata = vi.fn();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <LogDialog onOpenAnimeMetadata={onOpenAnimeMetadata} />
      </QueryClientProvider>,
    );
    fireEvent.click(await screen.findByRole("button", { name: t("anime.metadata.link", { name: "TMDB" }) }));
    expect(onOpenAnimeMetadata).toHaveBeenCalledWith("anime-1");
  });

  it("outcome 过滤传契约 outcome 参数", async () => {
    renderLogDialog();
    await screen.findByText("订阅已创建");
    await pickOption(t("log.status"), t("log.failure"));
    await waitFor(() => {
      const urls = recordsRequestUrls();
      expect(new URL(urls[urls.length - 1]!, "http://localhost").searchParams.get("outcome")).toBe("failure");
    });
  });

  it("卡片入口预选 anime 过滤并传 managed_anime_id 参数", async () => {
    useLogDialogStore.getState().openDialog("anime-1");
    renderLogDialog();
    await screen.findByText("订阅已创建");

    // An effect pours the preselection in after opening, so after the first request it queries again with managed_anime_id.
    await waitFor(() => {
      const urls = recordsRequestUrls();
      const params = new URL(urls[urls.length - 1]!, "http://localhost").searchParams;
      expect(params.get("managed_anime_id")).toBe("anime-1");
    });
    // The query-parameter assertion already proves the preselection took effect; the Select's display text does not expose the raw value.
  });

  it("time 过滤把 datetime-local 转 ISO 传 from/to 参数", async () => {
    renderLogDialog();
    await screen.findByText("订阅已创建");

    await openTimeRange();
    fireEvent.change(screen.getByLabelText(t("log.startTime")), { target: { value: "2026-07-20T10:30" } });
    fireEvent.change(screen.getByLabelText(t("log.endTime")), { target: { value: "2026-07-21T23:59" } });

    await waitFor(() => {
      const urls = recordsRequestUrls();
      const params = new URL(urls[urls.length - 1]!, "http://localhost").searchParams;
      expect(params.get("from")).toBe(new Date("2026-07-20T10:30").toISOString());
      expect(params.get("to")).toBe(new Date("2026-07-21T23:59").toISOString());
    });
  });

  it("插件记录打开并关闭详情时不关闭父日志", async () => {
    renderLogDialog();
    await screen.findByText("订阅已创建");

    fireEvent.click(screen.getByRole("button", { name: /打开发送详情/ }));
    expect(await screen.findByText("发送详情")).toBeInTheDocument();
    expect(await screen.findByText(new RegExp(t("settings.delivery.detail.attemptNo", { n: 1 })))).toBeInTheDocument();

    // Both dialogs are Base UI now; the child is found by its own title.
    const child = screen.getByRole("dialog", { name: "发送详情" });
    fireEvent.click(within(child).getByRole("button", { name: "关闭" }));
    await waitFor(() => expect(screen.queryByText(t("settings.delivery.detail.attemptNo", { n: 1 }))).not.toBeInTheDocument());
    expect(screen.getByText("订阅已创建")).toBeInTheDocument();
  });

  it("投递详情叠在日志上时 Esc 只关闭最上层", async () => {
    renderLogDialog();
    await screen.findByText("订阅已创建");

    fireEvent.click(screen.getByRole("button", { name: /打开发送详情/ }));
    await screen.findByText(new RegExp(t("settings.delivery.detail.attemptNo", { n: 1 })));

    await userEvent.keyboard("{Escape}");

    await waitFor(() => expect(screen.queryByText(t("settings.delivery.detail.attemptNo", { n: 1 }))).not.toBeInTheDocument());
    expect(screen.getByText("订阅已创建")).toBeInTheDocument();
  });

  it("事件不再提供处理过程入口：执行记录从 RSS 行与顶栏进入", async () => {
    renderLogDialog();
    await screen.findByText("订阅已创建");
    expect(screen.queryByRole("button", { name: t("records.openFromRelease") })).not.toBeInTheDocument();
  });

  it("「加载更多」按 next_cursor 翻页并累积记录", async () => {
    const secondPageRecord: ObservabilityRecord = {
      record_type: "business",
      entry: { ...businessRecord.entry, id: "business-2", summary: "第二页记录", kind: "rss_new_content" },
    };
    recordsPages = [
      { records: [businessRecord], next_cursor: "cursor-2" },
      { records: [secondPageRecord], next_cursor: null },
    ];
    renderLogDialog();
    await screen.findByText("订阅已创建");

    fireEvent.click(screen.getByRole("button", { name: t("log.loadMore") }));
    await screen.findByText("第二页记录");

    const urls = recordsRequestUrls();
    expect(new URL(urls[urls.length - 1]!, "http://localhost").searchParams.get("cursor")).toBe("cursor-2");
    // The first page's records are still there (accumulated, not replaced).
    expect(screen.getByText("订阅已创建")).toBeInTheDocument();
    expect(screen.getByText("第二页记录")).toBeInTheDocument();
    // The second page has no next_cursor, so the button disappears.
    expect(screen.queryByRole("button", { name: t("log.loadMore") })).not.toBeInTheDocument();
  });
});

describe("LogDialog 诊断", () => {
  beforeEach(() => {
    recordsPages = [{ records: [], next_cursor: null }];
    diagnosticsPages = [{ episodes: [], next_cursor: null }];
    stubFetch();
    useLogDialogStore.getState().openDialog();
  });

  afterEach(() => {
    useLogDialogStore.getState().closeDialog();
    vi.unstubAllGlobals();
  });

  it("severity/component 过滤传参", async () => {
    renderLogDialog();
    fireEvent.click(await screen.findByRole("tab", { name: t("log.tabDiagnostics") }));
    await screen.findByText(t("log.emptyDiagnostics"));

    await pickOption(t("log.status"), t("log.error"));
    await pickOption(t("log.type"), t("log.component.rss"));

    await waitFor(() => {
      const urls = diagnosticsRequestUrls();
      const params = new URL(urls[urls.length - 1]!, "http://localhost").searchParams;
      expect(params.get("severity")).toBe("error");
      expect(params.get("component")).toBe("rss");
    });
  });

  it("HTTP 接口组件过滤传 httpapi", async () => {
    renderLogDialog();
    fireEvent.click(await screen.findByRole("tab", { name: t("log.tabDiagnostics") }));
    await screen.findByText(t("log.emptyDiagnostics"));

    await pickOption(t("log.type"), t("log.component.httpapi"));

    await waitFor(() => {
      const urls = diagnosticsRequestUrls();
      const params = new URL(urls[urls.length - 1]!, "http://localhost").searchParams;
      expect(params.get("component")).toBe("httpapi");
    });
  });

  it("来源组件过滤传 provider", async () => {
    renderLogDialog();
    fireEvent.click(await screen.findByRole("tab", { name: t("log.tabDiagnostics") }));
    await screen.findByText(t("log.emptyDiagnostics"));

    await pickOption(t("log.type"), t("log.component.provider"));

    await waitFor(() => {
      const urls = diagnosticsRequestUrls();
      const params = new URL(urls[urls.length - 1]!, "http://localhost").searchParams;
      expect(params.get("component")).toBe("provider");
    });
  });

  it("替换组件过滤传 replacement", async () => {
    renderLogDialog();
    fireEvent.click(await screen.findByRole("tab", { name: t("log.tabDiagnostics") }));
    await screen.findByText(t("log.emptyDiagnostics"));

    await pickOption(t("log.type"), t("log.component.replacement"));

    await waitFor(() => {
      const urls = diagnosticsRequestUrls();
      const params = new URL(urls[urls.length - 1]!, "http://localhost").searchParams;
      expect(params.get("component")).toBe("replacement");
    });
  });

  it("清理保留数据组件过滤传 retention", async () => {
    renderLogDialog();
    fireEvent.click(await screen.findByRole("tab", { name: t("log.tabDiagnostics") }));
    await screen.findByText(t("log.emptyDiagnostics"));

    await pickOption(t("log.type"), t("log.component.retention"));

    await waitFor(() => {
      const urls = diagnosticsRequestUrls();
      const params = new URL(urls[urls.length - 1]!, "http://localhost").searchParams;
      expect(params.get("component")).toBe("retention");
    });
  });

  it("time 过滤传 from/to 参数", async () => {
    renderLogDialog();
    fireEvent.click(await screen.findByRole("tab", { name: t("log.tabDiagnostics") }));
    await screen.findByText(t("log.emptyDiagnostics"));

    // Base UI Tabs unmounts the inactive panel, so only the diagnostics tab's input exists.
    await openTimeRange();
    fireEvent.change(screen.getByLabelText(t("log.startTime")), { target: { value: "2026-07-20T00:00" } });

    await waitFor(() => {
      const urls = diagnosticsRequestUrls();
      const params = new URL(urls[urls.length - 1]!, "http://localhost").searchParams;
      expect(params.get("from")).toBe(new Date("2026-07-20T00:00").toISOString());
    });
  });

  it("诊断消费 next_cursor 并累积加载更多", async () => {
    const first: DiagnosticEpisode = {
      id: "diagnostic-1",
      severity: "warning",
      component: "rss",
      operation: "refresh",
      error_code: "rss_refresh_failed",
      scope_kind: "worker",
      scope_id: "worker-1",
      managed_anime_id: null,
      first_failed_at: "2026-07-20T00:00:00Z",
      last_failed_at: "2026-07-20T00:00:00Z",
      occurrence_count: 1,
      last_error: "第一页诊断",
      details: null,
      details_status: "unavailable",
      succeeded_after_at: null,
      retention_until: "2026-08-19T00:00:00Z",
    };
    const second: DiagnosticEpisode = { ...first, id: "diagnostic-2", last_error: "第二页诊断" };
    diagnosticsPages = [
      { episodes: [first], next_cursor: "diagnostic-cursor-2" },
      { episodes: [second], next_cursor: null },
    ];

    renderLogDialog();
    fireEvent.click(await screen.findByRole("tab", { name: t("log.tabDiagnostics") }));
    await openDiagnosticDetails("第一页诊断");
    fireEvent.click(screen.getByRole("button", { name: t("log.loadMore") }));
    await openDiagnosticDetails("第二页诊断");

    const urls = diagnosticsRequestUrls();
    expect(new URL(urls[urls.length - 1]!, "http://localhost").searchParams.get("cursor")).toBe("diagnostic-cursor-2");
    expect(screen.getByText(/第一页诊断/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("log.loadMore") })).not.toBeInTheDocument();
  });

  it("诊断详情解码失败时显示有界状态而不展示原始内容", async () => {
    diagnosticsPages = [
      {
        episodes: [
          {
            id: "diagnostic-invalid",
            severity: "error",
            component: "download",
            operation: "submit",
            error_code: "qb_submit_failed",
            scope_kind: "worker",
            scope_id: "worker-1",
            managed_anime_id: null,
            first_failed_at: "2026-07-20T00:00:00Z",
            last_failed_at: "2026-07-20T00:00:00Z",
            occurrence_count: 1,
            last_error: "提交失败",
            details: null,
            details_status: "invalid",
            succeeded_after_at: null,
            retention_until: "2026-08-19T00:00:00Z",
          },
        ],
        next_cursor: null,
      },
    ];

    renderLogDialog();
    fireEvent.click(await screen.findByRole("tab", { name: t("log.tabDiagnostics") }));
    await openDiagnosticDetails("提交失败");
    expect(screen.getByText(t("log.detailsInvalid"))).toBeInTheDocument();
  });

  it("诊断只展示服务端已经脱敏的文本，不自行重组原始内容", async () => {
    // Records left by older writers may carry keys/URLs/private paths; the read
    // boundary collapses them into <redacted>. The UI renders last_error
    // verbatim and never reassembles or restores any fragment itself.
    diagnosticsPages = [
      {
        episodes: [
          {
            id: "diagnostic-redacted",
            severity: "error",
            component: "rss",
            operation: "refresh",
            error_code: "rss_refresh_failed",
            scope_kind: "subscription",
            scope_id: "subscription-1",
            managed_anime_id: null,
            first_failed_at: "2026-07-20T00:00:00Z",
            last_failed_at: "2026-07-20T00:00:00Z",
            occurrence_count: 1,
            last_error: "<redacted>",
            details: null,
            details_status: "unavailable",
            succeeded_after_at: null,
            retention_until: "2026-08-19T00:00:00Z",
          },
        ],
        next_cursor: null,
      },
    ];

    renderLogDialog();
    fireEvent.click(await screen.findByRole("tab", { name: t("log.tabDiagnostics") }));
    await openDiagnosticDetails("<redacted>");
    expect(screen.queryByText(/api_key|https?:\/\/|\/home\//)).not.toBeInTheDocument();
  });

  it("诊断条目显示中文类型、操作和原因，原始错误放进详情", async () => {
    diagnosticsPages = [
      {
        episodes: [
          {
            id: "diagnostic-http",
            severity: "error",
            component: "httpapi",
            operation: "request",
            error_code: "http_503",
            scope_kind: "global",
            scope_id: "global",
            managed_anime_id: null,
            first_failed_at: "2026-07-20T00:00:00Z",
            last_failed_at: "2026-07-20T00:00:00Z",
            occurrence_count: 3,
            last_error: "upstream exploded",
            details: null,
            details_status: "unavailable",
            succeeded_after_at: null,
            retention_until: "2026-08-19T00:00:00Z",
          },
        ],
        next_cursor: null,
      },
    ];
    renderLogDialog();
    fireEvent.click(await screen.findByRole("tab", { name: t("log.tabDiagnostics") }));
    expect(await screen.findByText(`${t("log.component.httpapi")} · ${t("log.operation.request")}`)).toBeInTheDocument();
    expect(screen.getByText(/请求失败（HTTP 503）/)).toBeInTheDocument();
    expect(screen.queryByText(/http_503|httpapi|upstream exploded/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("errorDetails.toggle") }));
    expect(screen.getByText(/upstream exploded/)).toBeInTheDocument();
  });
});
