import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AcquisitionFilePage, AcquisitionRecordDetail, AcquisitionRecordPage } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { labelValue } from "../../lib/i18n/format";
import { formatBytes } from "../../lib/labels";
import { titleTagLabel } from "../../lib/titleTags";
import { ExecutionRecordsDialog } from "./ExecutionRecordsDialog";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() } }));

const runningSummary = {
  acquisition_id: "acq-running",
  entry_kind: "automatic",
  state: "running",
  record_detail: "full",
  accepted_at: "2026-10-11T08:00:00Z",
  source_title: { state: "visible", value: "[Group] Show - 02" },
  anime: { managed_anime_id: "anime-1", title: "测试番" },
  stage: "download_wait",
  stage_state: "running",
} as const;

const failedSummary = {
  acquisition_id: "acq-failed",
  entry_kind: "one_shot",
  state: "failed",
  record_detail: "full",
  accepted_at: "2026-10-10T08:00:00Z",
  ended_at: "2026-10-10T09:00:00Z",
  source_title: { state: "redacted" },
  stage: "file_execution",
  stage_state: "failed",
  reason_code: "SOME_UNREGISTERED_CODE",
  counts: { total: 2, required: 1, excluded: 1, succeeded: 0, failed: 1 },
} as const;

const failedDetail: AcquisitionRecordDetail = {
  ...failedSummary,
  execution_branch: "download",
  as_of: "2026-10-11T10:00:00Z",
  release: { release_id: "release-1", title: { state: "visible", value: "[Group] Show - 01" } },
  outcome: {
    outcome: "failed",
    requirements_status: "sealed",
    counts: { total: 2, required: 1, excluded: 1, succeeded: 0, failed: 1 },
    primary_failure: { stage: "file_execution", reason_code: "SOME_UNREGISTERED_CODE", file_ref: "file-a" },
    effects_complete: true,
    has_confirmed_effect: false,
    has_unknown_effect: false,
    settled_at: "2026-10-10T09:00:00Z",
  },
  stages: [
    { stage: "submit", availability: "present", state: "succeeded" },
    { stage: "download_wait", availability: "present", state: "succeeded" },
    { stage: "inspect", availability: "present", state: "succeeded" },
    { stage: "plan", availability: "present", state: "succeeded" },
    { stage: "file_execution", availability: "present", state: "failed", reason_code: "SOME_UNREGISTERED_CODE", done: 1, total: 1, attempts_started: 3 },
    { stage: "settlement", availability: "present", state: "failed" },
    { stage: "completion", availability: "not_applicable" },
  ],
  requirements: { status: "sealed", file_count: 2, required_count: 1, excluded_count: 1 },
  files_available: true,
  version: 4,
  import_retry_available: true,
};

// A succeeded record whose download-completion action was refused: no repair.
const succeededDetail: AcquisitionRecordDetail = {
  ...failedDetail,
  acquisition_id: "acq-done",
  state: "succeeded",
  stage: undefined,
  stage_state: undefined,
  reason_code: undefined,
  outcome: { ...failedDetail.outcome!, outcome: "succeeded", primary_failure: undefined, counts: { total: 2, required: 1, excluded: 1, succeeded: 1, failed: 0 } },
  stages: [
    ...failedDetail.stages.slice(0, 4),
    { stage: "file_execution", availability: "present", state: "succeeded", done: 1, total: 1 },
    { stage: "settlement", availability: "present", state: "succeeded" },
    { stage: "completion", availability: "present", state: "failed", reason_code: "placement_inside_content" },
  ],
  files_available: false,
  import_retry_available: false,
};

const compactDetail: AcquisitionRecordDetail = {
  ...failedDetail,
  acquisition_id: "acq-compact",
  record_detail: "compact",
  compacted_at: "2026-11-10T09:00:00Z",
  compact_reason: "detail_expired",
  stages: [],
  requirements: undefined,
  files_available: false,
  import_retry_available: false,
};

const files: AcquisitionFilePage = {
  acquisition_id: "acq-failed",
  as_of: "2026-10-11T10:00:00Z",
  items: [
    {
      ordinal: 0,
      file_ref: "file-a",
      relative_path: { state: "visible", value: "Show - 01.mkv" },
      size_bytes: 1024,
      media_kind: "video",
      requirement: "required",
      operation: { state: "failed", operation: "copy", reason_code: "SOME_UNREGISTERED_CODE" },
    },
    {
      ordinal: 1,
      file_ref: "file-b",
      relative_path: { state: "visible", value: "readme.txt" },
      size_bytes: 10,
      media_kind: "other",
      requirement: "excluded",
    },
  ],
};

let fetchMock: ReturnType<typeof vi.fn>;
// Pages may omit origin_counts; the stub answers subscription 1 / one-shot 1 unless a test sets them.
type PageFixture = Omit<AcquisitionRecordPage, "origin_counts"> & Partial<Pick<AcquisitionRecordPage, "origin_counts">>;
let listPages: PageFixture[];
let filesStatus: number;
let filesBody: AcquisitionFilePage;
let retryStatus: number;
let failedDetailBody: AcquisitionRecordDetail;

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

beforeEach(() => {
  listPages = [
    { as_of: "2026-10-11T10:00:00Z", items: [runningSummary], next_cursor: "cursor-2" },
    { as_of: "2026-10-11T10:00:00Z", items: [failedSummary] },
  ];
  filesStatus = 200;
  retryStatus = 202;
  filesBody = files;
  failedDetailBody = failedDetail;
  fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://localhost");
    if (url.pathname === "/api/v1/acquisitions") {
      const page = url.searchParams.get("cursor") ? listPages[1] : listPages[0];
      return json({ origin_counts: { subscription: 1, one_shot: 1 }, ...page });
    }
    if (url.pathname === "/api/v1/acquisitions/acq-failed") return json(failedDetailBody);
    if (url.pathname === "/api/v1/acquisitions/acq-compact") return json(compactDetail);
    if (url.pathname === "/api/v1/acquisitions/acq-done") return json(succeededDetail);
    if (url.pathname === "/api/v1/acquisitions/acq-failed/import/retry") {
      if (init?.method !== "POST") return json({ code: "METHOD", message: "method" }, 405);
      return retryStatus === 202
        ? json({ acquisition_id: "acq-failed", attempt: 2, retried_files: 1, reopened_placements: 0, version: 5 }, 202)
        : json({ code: "IMPORT_RETRY_IN_FLIGHT", message: "running" }, retryStatus);
    }
    if (url.pathname === "/api/v1/acquisitions/acq-failed/files") {
      return filesStatus === 200 ? json(filesBody) : json({ code: "ACQUISITION_FILES_NOT_READY", message: "not ready" }, filesStatus);
    }
    if (url.pathname === "/api/v1/acquisitions/acq-missing") return json({ code: "ACQUISITION_NOT_FOUND", message: "missing" }, 404);
    return json({}, 404);
  });
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

function renderDialog(props: Partial<Parameters<typeof ExecutionRecordsDialog>[0]> = {}) {
  const handlers = {
    onOpenRecord: vi.fn(),
    onShowList: vi.fn(),
    onClearAnime: vi.fn(),
    onOpenAnime: vi.fn(),
    onClose: vi.fn(),
  };
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <ExecutionRecordsDialog open record="list" {...handlers} {...props} />
    </QueryClientProvider>,
  );
  return handlers;
}

function requestedPaths(): string[] {
  return fetchMock.mock.calls.map(([input]) => String(input));
}

describe("ExecutionRecordsDialog 列表", () => {
  it("按页加载记录，显示状态、来源、所在阶段，并打开详情", async () => {
    const handlers = renderDialog();
    const list = await screen.findByRole("list", { name: t("records.listAria") });
    expect(list.textContent).toContain("[Group] Show - 02");
    expect(list.textContent).toContain(t("records.entry.automatic"));
    expect(list.textContent).toContain(t("records.atStage", { stage: t("records.stage.download_wait"), state: t("records.stageState.running") }));

    fireEvent.click(screen.getByRole("button", { name: t("records.loadMore") }));
    await waitFor(() => expect(list.textContent).toContain(t("records.redacted")));
    // An unregistered reason code shows the unknown reason, with the raw code in the tooltip.
    expect(within(list).getByTitle("SOME_UNREGISTERED_CODE").textContent).toBe(t("common.unknownReason"));
    expect(screen.queryByRole("button", { name: t("records.loadMore") })).not.toBeInTheDocument();

    fireEvent.click(within(list).getByText("[Group] Show - 02"));
    expect(handlers.onOpenRecord).toHaveBeenCalledWith("acq-running");
  });

  it("番剧筛选传给接口，并可清除", async () => {
    const handlers = renderDialog({ animeFilter: "anime-1" });
    expect(await screen.findByText(t("records.filter.anime", { title: "测试番" }))).toBeInTheDocument();
    expect(requestedPaths().some((url) => url.includes("managed_anime_id=anime-1"))).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: t("records.filter.clearAnime") }));
    expect(handlers.onClearAnime).toHaveBeenCalled();
  });

  it("订阅下载的名称与番剧标题相同时不重复显示番剧", async () => {
    listPages = [
      {
        as_of: "2026-10-11T10:00:00Z",
        items: [{ ...runningSummary, source_title: { state: "visible", value: "测试番" } }],
      },
    ];
    renderDialog();
    const list = await screen.findByRole("list", { name: t("records.listAria") });
    expect(within(list).getAllByText("测试番")).toHaveLength(1);
  });

  it("同一番剧的记录按集数或种子标题区分，下载中显示进度与速度", async () => {
    listPages = [
      {
        as_of: "2026-10-11T10:00:00Z",
        items: [
          {
            ...runningSummary,
            acquisition_id: "acq-ep2",
            source_title: { state: "visible", value: "测试番" },
            episodes: [2],
            transfer: { completed_bytes: 512, total_bytes: 1024, download_speed: 2048, observed_at: "2026-10-11T09:59:00Z" },
          },
          {
            ...runningSummary,
            acquisition_id: "acq-batch",
            source_title: { state: "visible", value: "测试番" },
            stage: "plan",
            stage_state: "pending",
            episodes: [1, 2, 3, 5],
          },
          {
            ...runningSummary,
            acquisition_id: "acq-movie",
            source_title: { state: "visible", value: "测试番" },
            stage: "plan",
            stage_state: "pending",
            release: { release_id: "release-9", title: { state: "visible", value: "[Group] 测试番 剧场版" } },
          },
        ],
      },
    ];
    renderDialog();
    const list = await screen.findByRole("list", { name: t("records.listAria") });
    expect(list.textContent).toContain(t("records.episode", { n: 2 }));
    expect(list.textContent).toContain(t("records.episodes", { range: "01-03, 05" }));
    expect(within(list).getByTitle("[Group] 测试番 剧场版")).toBeInTheDocument();
    expect(list.textContent).toContain(t("records.transfer.progress", { percent: 50 }));
    expect(list.textContent).toContain(t("records.transfer.speed", { speed: "2.0 KB" }));
  });

  it("没有记录时按分段显示空状态与说明", async () => {
    listPages = [{ as_of: "2026-10-11T10:00:00Z", items: [] }];
    renderDialog();
    expect(await screen.findByText(t("records.segmentEmpty.subscription"))).toBeInTheDocument();
    expect(screen.getByText(t("records.segmentEmpty.subscriptionHint"))).toBeInTheDocument();
  });
});

describe("ExecutionRecordsDialog 分段", () => {
  function segmentRadio(segment: "subscription" | "one_shot") {
    return screen.getByRole("radio", { name: new RegExp(`^${t(`records.segment.${segment}`)}`) });
  }
  function listQueries(): URLSearchParams[] {
    return requestedPaths()
      .map((input) => new URL(input, "http://localhost"))
      .filter((url) => url.pathname === "/api/v1/acquisitions")
      .map((url) => url.searchParams);
  }

  it("默认打开订阅分段，由服务端按来源筛选，分段显示计数", async () => {
    listPages = [{ as_of: "2026-10-11T10:00:00Z", items: [runningSummary], origin_counts: { subscription: 12, one_shot: 3 } }];
    renderDialog();
    await screen.findByRole("list", { name: t("records.listAria") });
    expect(listQueries().at(-1)?.get("origin")).toBe("subscription");
    expect(segmentRadio("subscription")).toBeChecked();
    expect(segmentRadio("subscription")).toHaveAccessibleName(`${t("records.segment.subscription")} 12`);
    expect(segmentRadio("one_shot")).toHaveAccessibleName(`${t("records.segment.one_shot")} 3`);
    // The subscription segment can be subdivided further by entry.
    expect(screen.getByText(t("records.filter.entry"))).toBeInTheDocument();
  });

  it("切换到一次性：请求 origin=one_shot，隐藏入口细分，记住选择并写回链接", async () => {
    const onSegmentChange = vi.fn();
    renderDialog({ onSegmentChange });
    await screen.findByRole("list", { name: t("records.listAria") });
    fireEvent.click(segmentRadio("one_shot"));
    await waitFor(() => expect(listQueries().at(-1)?.get("origin")).toBe("one_shot"));
    expect(listQueries().at(-1)?.get("entry_kind")).toBeNull();
    expect(onSegmentChange).toHaveBeenCalledWith("one_shot");
    expect(localStorage.getItem("bansoko.records.segment")).toBe("one_shot");
    expect(screen.queryByText(t("records.filter.entry"))).not.toBeInTheDocument();
  });

  it("链接里的分段优先于记住的选择", async () => {
    localStorage.setItem("bansoko.records.segment", "subscription");
    renderDialog({ segment: "one_shot" });
    await screen.findByRole("list", { name: t("records.listAria") });
    expect(segmentRadio("one_shot")).toBeChecked();
    expect(listQueries().every((query) => query.get("origin") === "one_shot")).toBe(true);
  });

  it("没有链接参数时用本浏览器上次的选择", async () => {
    localStorage.setItem("bansoko.records.segment", "one_shot");
    renderDialog();
    await screen.findByRole("list", { name: t("records.listAria") });
    expect(segmentRadio("one_shot")).toBeChecked();
  });
});

describe("ExecutionRecordsDialog 详情", () => {
  it("显示固定阶段、结果与文件列表；唯一的重试在入库阶段上", async () => {
    const handlers = renderDialog({ record: "acq-failed" });
    const stages = await screen.findByRole("list", { name: t("records.detail.stagesAria") });
    for (const stage of ["submit", "download_wait", "inspect", "plan", "file_execution", "settlement", "completion"] as const) {
      expect(stages.textContent).toContain(t(`records.stage.${stage}`));
    }
    expect(stages.textContent).toContain(t("records.detail.attempts", { n: 3 }));
    expect(stages.textContent).toContain(t("records.detail.progress", { done: 1, total: 1 }));
    expect(screen.getByText(t("records.detail.counts", { total: 2, required: 1, succeeded: 0, failed: 1 }))).toBeInTheDocument();
    expect(screen.getByText("[Group] Show - 01")).toBeInTheDocument();

    const fileList = await screen.findByRole("list", { name: t("records.file.aria") });
    expect(fileList.textContent).toContain("Show - 01.mkv");
    expect(fileList.textContent).toContain(t("records.file.operation.failed"));
    expect(fileList.textContent).toContain(t("records.file.excluded"));

    const retries = screen.getAllByRole("button", { name: /重试|重新/ });
    expect(retries).toHaveLength(1);
    expect(within(stages).getByText(t("records.stage.file_execution")).closest("li")).toContainElement(retries[0]);
    fireEvent.click(screen.getByRole("button", { name: t("records.allRecords") }));
    expect(handlers.onShowList).toHaveBeenCalled();
  });

  it("下载阶段进行中时显示进度；入库成功的文件显示入库位置", async () => {
    const downloading: AcquisitionRecordDetail = {
      ...failedDetail,
      state: "running",
      stage: "download_wait",
      stage_state: "running",
      reason_code: undefined,
      outcome: undefined,
      episodes: [1],
      transfer: { completed_bytes: 300, total_bytes: 1000, observed_at: "2026-10-11T09:59:00Z" },
      stages: [
        { stage: "submit", availability: "present", state: "succeeded" },
        { stage: "download_wait", availability: "present", state: "running" },
        ...failedDetail.stages.slice(2),
      ],
    };
    filesBody = {
      ...files,
      items: [
        {
          ...files.items[0],
          operation: { state: "completed", operation: "copy" },
          delivery: { outcome: "succeeded", completion_mode: "executed", effect_class: "confirmed", source_cleanup: "not_required" },
          placed_path: { state: "visible", value: "测试番/Season 01/测试番 - S01E01.mkv" },
          collision_outcome: "versioned",
        },
        files.items[1],
      ],
    };
    fetchMock.mockImplementationOnce(async () => json(downloading));
    renderDialog({ record: "acq-failed" });
    const stages = await screen.findByRole("list", { name: t("records.detail.stagesAria") });
    const download = within(stages).getByText(t("records.stage.download_wait")).closest("li");
    expect(download?.textContent).toContain(t("records.transfer.progress", { percent: 30 }));
    expect(download?.textContent).not.toContain("/s");
    expect(screen.getByText(t("records.episode", { n: 1 }))).toBeInTheDocument();

    const fileList = await screen.findByRole("list", { name: t("records.file.aria") });
    const placed = within(fileList).getByTitle("测试番/Season 01/测试番 - S01E01.mkv");
    expect(placed.textContent).toBe(t("records.file.placedAt", { path: "测试番/Season 01/测试番 - S01E01.mkv" }));
    expect(within(fileList).getAllByText(/Season 01/)).toHaveLength(1);
    expect(
      within(fileList).getByText(labelValue(t("records.file.collision.label"), t("records.file.collision.versioned"))),
    ).toBeInTheDocument();
  });

  it("入库阶段失败时提供重试，确认后带版本和幂等键提交，并重新读取记录", async () => {
    renderDialog({ record: "acq-failed" });
    const stages = await screen.findByRole("list", { name: t("records.detail.stagesAria") });
    const buttons = within(stages).getAllByRole("button", { name: t("records.importRetry.action") });
    expect(buttons).toHaveLength(1);
    expect(buttons[0].closest("li")?.textContent).toContain(t("records.stage.file_execution"));
    fireEvent.click(buttons[0]);

    expect(await screen.findByText(t("records.importRetry.line1"))).toBeInTheDocument();
    expect(screen.getByText(t("records.importRetry.line2"))).toBeInTheDocument();
    fireEvent.click(screen.getByTestId("import-retry-confirm"));
    expect(await screen.findByText(t("records.importRetry.accepted"))).toBeInTheDocument();

    const call = fetchMock.mock.calls.find(([input]) => String(input).endsWith("/acquisitions/acq-failed/import/retry"));
    expect(call).toBeDefined();
    const init = call?.[1] as RequestInit;
    expect(init.method).toBe("POST");
    expect(init.body).toBeUndefined();
    const headers = new Headers(init.headers);
    expect(headers.get("If-Match")).toBe("4");
    expect(headers.get("Idempotency-Key")).toMatch(/^import-retry-/);

    fireEvent.click(screen.getByRole("button", { name: t("records.importRetry.ok") }));
    // The record is read again after the retry was accepted.
    await waitFor(() => expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith("/acquisitions/acq-failed")).length).toBeGreaterThan(1));
  });

  it("入库重试被拒绝时在确认框里显示错误", async () => {
    retryStatus = 409;
    renderDialog({ record: "acq-failed" });
    const stages = await screen.findByRole("list", { name: t("records.detail.stagesAria") });
    fireEvent.click(within(stages).getByRole("button", { name: t("records.importRetry.action") }));
    fireEvent.click(await screen.findByTestId("import-retry-confirm"));
    expect(await screen.findByText(t("error.IMPORT_RETRY_IN_FLIGHT"))).toBeInTheDocument();
    expect(screen.queryByText(t("records.importRetry.accepted"))).not.toBeInTheDocument();
  });

  it("服务端不提供入库重试时没有重试按钮；其他阶段失败也没有", async () => {
    failedDetailBody = { ...failedDetail, import_retry_available: false };
    renderDialog({ record: "acq-failed" });
    let stages = await screen.findByRole("list", { name: t("records.detail.stagesAria") });
    expect(within(stages).queryByRole("button", { name: t("records.importRetry.action") })).not.toBeInTheDocument();
    cleanup();

    renderDialog({ record: "acq-done" });
    stages = await screen.findByRole("list", { name: t("records.detail.stagesAria") });
    expect(stages.textContent).toContain(t("reason.placement_inside_content"));
    expect(within(stages).queryByRole("button")).not.toBeInTheDocument();
  });

  it("已入库文件的结局：替换版本、被其他番剧覆盖、重新下载", async () => {
    const done = { state: "completed" as const, operation: "copy" };
    filesBody = {
      ...files,
      items: [
        { ...files.items[0], file_ref: "f-1", operation: done, placement_end: { state: "superseded" } },
        { ...files.items[0], file_ref: "f-2", ordinal: 2, operation: done, placement_end: { state: "displaced", cause: "overwrite", successor: { managed_anime_id: "anime-2", title: "Other Show" } } },
        { ...files.items[0], file_ref: "f-3", ordinal: 3, operation: done, placement_end: { state: "displaced", cause: "redownload", successor: { managed_anime_id: "anime-1", title: "Show" } } },
      ],
    };
    renderDialog({ record: "acq-failed" });
    const fileList = await screen.findByRole("list", { name: t("records.file.aria") });
    expect(within(fileList).getByText(t("records.file.end.superseded"))).toBeInTheDocument();
    expect(within(fileList).getByText(t("records.file.end.displacedBy", { title: "Other Show" }))).toBeInTheDocument();
    expect(within(fileList).getByText(t("records.file.end.redownloaded"))).toBeInTheDocument();
  });

  it("入库后续处理失败的文件只显示状态和原因，文件行没有重试按钮", async () => {
    filesBody = {
      ...files,
      items: [{ ...files.items[0], operation: { state: "completed", operation: "copy" }, placement_failure: { attempts: 3, reason_code: "placement_admission_failed" } }, files.items[1]],
    };
    renderDialog({ record: "acq-failed" });
    const fileList = await screen.findByRole("list", { name: t("records.file.aria") });
    expect(within(fileList).getByText(t("records.file.handoff.failed"))).toBeInTheDocument();
    expect(within(fileList).getByText(t("reason.placement_admission_failed"))).toBeInTheDocument();
    expect(within(fileList).queryByRole("button")).not.toBeInTheDocument();
  });

  it("入库后续处理失败的未登记原因显示未知原因", async () => {
    filesBody = {
      ...files,
      items: [{ ...files.items[0], operation: { state: "completed", operation: "copy" }, placement_failure: { attempts: 1, reason_code: "placement_brand_new_code" } }],
    };
    renderDialog({ record: "acq-failed" });
    const fileList = await screen.findByRole("list", { name: t("records.file.aria") });
    expect(within(fileList).getByTitle("placement_brand_new_code").textContent).toBe(t("common.unknownReason"));
  });

  it("不入库的文件说明没有选中的原因，选中的原因不显示", async () => {
    filesBody = {
      ...files,
      items: [
        { ...files.items[0], operation: { state: "completed", operation: "copy" }, selection_reason_code: "file_rules_selected" },
        { ...files.items[1], media_kind: "video", relative_path: { state: "visible", value: "Show - NCOP.mkv" }, selection_reason_code: "unknown_skipped" },
      ],
    };
    renderDialog({ record: "acq-failed" });
    const fileList = await screen.findByRole("list", { name: t("records.file.aria") });
    const rows = within(fileList).getAllByRole("listitem");
    expect(rows[1].textContent).toContain(t("records.file.excluded"));
    expect(rows[1].textContent).toContain(t("reason.unknown_skipped"));
    // The selected reason code has no registered copy and must not display as the "unknown reason".
    expect(fileList.textContent).not.toContain(t("common.unknownReason"));
  });

  it("文件列表尚未确定时显示说明而不是错误", async () => {
    filesStatus = 409;
    renderDialog({ record: "acq-failed" });
    expect(await screen.findByText(t("records.file.notReady"))).toBeInTheDocument();
  });

  it("列表行显示字幕组、关键标签、版本、大小、种子标题和洗版标记；缺失的值不显示", async () => {
    listPages = [
      {
        as_of: "2026-10-11T10:00:00Z",
        items: [
          {
            ...runningSummary,
            acquisition_id: "acq-seed",
            episodes: [2],
            replaces_existing: true,
            release: { release_id: "release-2", title: { state: "visible", value: "[NEST] Show - 02 [v2]" } },
            seed: {
              group_name: "NEST",
              provider: "mikan",
              size_bytes: 1024,
              release_version: "v2",
              title_tags: ["resolution:1080p", "sub_lang:SC", "codec:HEVC"],
            },
          },
          { ...runningSummary, acquisition_id: "acq-bare", episodes: [3] },
        ],
      },
    ];
    renderDialog();
    const list = await screen.findByRole("list", { name: t("records.listAria") });
    const [seeded, bare] = within(list).getAllByRole("listitem");
    expect(seeded.textContent).toContain(["NEST", titleTagLabel("resolution:1080p"), titleTagLabel("sub_lang:SC"), "v2", formatBytes(1024)].join(" · "));
    expect(seeded.textContent).not.toContain(titleTagLabel("codec:HEVC"));
    expect(within(seeded).getByTitle("[NEST] Show - 02 [v2]")).toBeInTheDocument();
    expect(seeded.textContent).toContain(t("records.replacement"));
    expect(bare.textContent).not.toContain(t("records.replacement"));
    expect(bare.textContent).not.toContain(" · v");
  });

  it("详情显示字幕组与来源站点、发布时间、大小、信息哈希和全部标签，且可复制哈希", async () => {
    const writeText = vi.fn(async () => undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    failedDetailBody = {
      ...failedDetail,
      seed: {
        group_name: "NEST",
        provider: "anibt",
        size_bytes: 2048,
        published_at: "2026-10-01T02:03:04Z",
        info_hash: "abcdef0123456789",
        title_tags: ["resolution:1080p", "codec:HEVC"],
        anibt_tags: ["audio:flac"],
      },
    };
    renderDialog({ record: "acq-failed" });
    expect(await screen.findByText(t("records.detail.infoHash"))).toBeInTheDocument();
    expect(screen.getByText(t("records.detail.groupWithSite", { group: "NEST", site: "AniBT" }))).toBeInTheDocument();
    expect(screen.getByText(t("records.detail.published"))).toBeInTheDocument();
    expect(screen.getByText(formatBytes(2048))).toBeInTheDocument();
    expect(screen.getByText(titleTagLabel("codec:HEVC"))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("records.detail.copyHash") }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("abcdef0123456789"));
  });

  it("详情没有来源标识时不显示这些行", async () => {
    renderDialog({ record: "acq-failed" });
    expect(await screen.findByText(t("records.detail.accepted"))).toBeInTheDocument();
    expect(screen.queryByText(t("records.detail.infoHash"))).not.toBeInTheDocument();
    expect(screen.queryByText(t("records.detail.group"))).not.toBeInTheDocument();
  });

  it("精简记录只保留名称与结果，不请求文件列表", async () => {
    renderDialog({ record: "acq-compact" });
    expect(await screen.findByText(t("records.detail.compact"))).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: t("records.detail.stagesAria") })).not.toBeInTheDocument();
    expect(screen.getByText(t("records.detail.outcomeTitle"))).toBeInTheDocument();
    expect(requestedPaths().some((url) => url.includes("/files"))).toBe(false);
  });

  it("找不到记录时说明并保留返回列表", async () => {
    renderDialog({ record: "acq-missing" });
    expect(await screen.findByText(t("records.detail.notFound"))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("records.allRecords") })).toBeInTheDocument();
  });

  it("番剧链接打开番剧", async () => {
    fetchMock.mockImplementationOnce(async () => json({ ...failedDetail, anime: { managed_anime_id: "anime-1", title: "测试番" } }));
    const handlers = renderDialog({ record: "acq-failed" });
    fireEvent.click(await screen.findByRole("button", { name: "测试番" }));
    expect(handlers.onOpenAnime).toHaveBeenCalledWith("anime-1");
  });
});

describe("ExecutionRecordsDialog 一次性下载失败", () => {
  it("失败的一次性下载只提示从浏览种子重新下载：没有重试 / 重新规划，也不读一次性下载标记", async () => {
    renderDialog({ record: "acq-failed" });
    expect(await screen.findByText(t("records.detail.oneShotFailed"))).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "重新规划" })).not.toBeInTheDocument();
    expect(requestedPaths().some((url) => url.includes("/one-shot-acquisitions/"))).toBe(false);
  });

  it("成功的一次性下载没有这条提示", async () => {
    renderDialog({ record: "acq-done" });
    await screen.findByText(t("records.detail.outcomeTitle"));
    expect(screen.queryByText(t("records.detail.oneShotFailed"))).not.toBeInTheDocument();
  });

  it("在下载器中删除的一次性下载显示为已取消，原因是已在下载器中删除，不显示失败", async () => {
    const base = fetchMock.getMockImplementation()!;
    const removed: AcquisitionRecordDetail = {
      ...failedDetail,
      acquisition_id: "acq-removed",
      state: "cancelled",
      stage: "download_wait",
      stage_state: "cancelled",
      reason_code: "DOWNLOADER_TASK_REMOVED",
      outcome: {
        ...failedDetail.outcome!,
        outcome: "cancelled",
        requirements_status: "incomplete",
        counts: undefined,
        primary_failure: { stage: "download_wait", reason_code: "DOWNLOADER_TASK_REMOVED" },
        effects_complete: false,
      },
      stages: [
        { stage: "submit", availability: "present", state: "succeeded" },
        { stage: "download_wait", availability: "present", state: "cancelled", reason_code: "DOWNLOADER_TASK_REMOVED" },
        { stage: "inspect", availability: "not_reached" },
        { stage: "plan", availability: "not_reached" },
        { stage: "file_execution", availability: "not_reached" },
        { stage: "settlement", availability: "present", state: "cancelled" },
        { stage: "completion", availability: "not_applicable" },
      ],
      requirements: { status: "incomplete" },
      files_available: false,
    };
    fetchMock.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost");
      if (url.pathname === "/api/v1/acquisitions/acq-removed") return json(removed);
      return base(input, init);
    });
    renderDialog({ record: "acq-removed" });
    expect(await screen.findByText(t("records.detail.oneShotCancelled"))).toBeInTheDocument();
    expect(screen.queryByText(t("records.detail.oneShotFailed"))).not.toBeInTheDocument();
    expect(screen.getAllByText(t("reason.downloader_task_removed")).length).toBeGreaterThan(0);
  });

  it("失败的订阅下载没有这条提示", async () => {
    const base = fetchMock.getMockImplementation()!;
    fetchMock.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost");
      if (url.pathname === "/api/v1/acquisitions/acq-auto") return json({ ...failedDetail, acquisition_id: "acq-auto", entry_kind: "automatic", files_available: false });
      return base(input, init);
    });
    renderDialog({ record: "acq-auto" });
    await screen.findByText(t("records.detail.outcomeTitle"));
    expect(screen.queryByText(t("records.detail.oneShotFailed"))).not.toBeInTheDocument();
  });
});
