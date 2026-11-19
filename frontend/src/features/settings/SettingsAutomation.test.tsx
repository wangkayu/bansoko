import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { afterEach, describe, expect, it, vi } from "vitest";
import { t } from "../../lib/i18n";
import type { AutomationSchedule, Connection } from "../../lib/api/client";
import { SettingsAutomation } from "./SettingsAutomation";

// The settings page's automation tasks: four fixed schedule cards
//   (enable/interval/run-now/last/next summaries), with no long execution-record list.

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

let fetchMock: ReturnType<typeof vi.fn>;

function stubFetch(options: { schedules?: AutomationSchedule[]; connections?: Connection[]; unconfirmedRun?: boolean } = {}) {
  fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    let body: unknown = [];
    if (options.unconfirmedRun && url.endsWith("/automation/rss_refresh/run")) {
      body = { accepted: true, run_id: "manual-test", state: "queued" };
    } else if (options.unconfirmedRun && url.endsWith("/automation/runs/manual-test")) {
      body = { state: "unconfirmed" };
    } else if (url.includes("/automation")) {
      body = options.schedules ?? [];
    } else if (url.includes("/config/connections")) {
      body = options.connections ?? [];
    }
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
      <SettingsAutomation />
    </QueryClientProvider>,
  );
}

// The metadata_match schedule card shows a "config missing" read-only hint when the TMDB connection lacks an API Key.
describe("SettingsAutomation 调度卡配置缺失指示（G6）", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  const metadataSchedule: AutomationSchedule = {
    kind: "metadata_match",
    enabled: true,
    interval_value: 6,
    interval_unit: "hour",
    version: 1,
  };

  const rssSchedule: AutomationSchedule = {
    kind: "rss_refresh",
    enabled: true,
    interval_value: 30,
    interval_unit: "minute",
    version: 1,
  };

  function tmdbConnection(credential_configured: boolean): Connection {
    return {
      id: "conn-tmdb",
      kind: "tmdb",
      name: "TMDB",
      enabled: true,
      base_url: "https://api.themoviedb.org/3",
      credential_configured,
      version: 1,
    };
  }

  it("TMDB 连接缺 API Key 时，仅元数据自动匹配卡显示配置缺失", async () => {
    stubFetch({
      schedules: [metadataSchedule, rssSchedule],
      connections: [tmdbConnection(false)],
    });
    renderSettings();

    expect(await screen.findByText(t("settings.automation.missingTag"))).toBeInTheDocument();
    expect(screen.getAllByText(t("settings.automation.missingTag"))).toHaveLength(1);
    expect(screen.getByText(t("settings.automation.missingTmdbKey"))).toBeInTheDocument();
    expect(screen.getByText(t("nav.automationKind.rss_refresh"))).toBeInTheDocument();
  });

  it("目录刷新卡显示中文名称", async () => {
    stubFetch({
      schedules: [{ kind: "catalog_refresh", enabled: true, interval_value: 12, interval_unit: "hour", version: 1 }],
      connections: [tmdbConnection(true)],
    });
    renderSettings();

    expect(await screen.findByText(t("nav.automationKind.catalog_refresh"))).toBeInTheDocument();
    expect(screen.queryByText("catalog_refresh")).not.toBeInTheDocument();
  });

  it("没有 TMDB 连接时同样显示配置缺失", async () => {
    stubFetch({ schedules: [metadataSchedule], connections: [] });
    renderSettings();

    expect(await screen.findByText(t("settings.automation.missingTag"))).toBeInTheDocument();
  });

  it("TMDB 连接已配置 API Key 时不显示配置缺失", async () => {
    stubFetch({
      schedules: [metadataSchedule],
      connections: [tmdbConnection(true)],
    });
    renderSettings();

    expect(await screen.findByText(t("nav.automationKind.metadata_match"))).toBeInTheDocument();
    expect(screen.queryByText(t("settings.automation.missingTag"))).not.toBeInTheDocument();
  });
});

// Schedule cards show the last dispatch outcome (last_run_outcome) in both terminal and in-flight states.
describe("SettingsAutomation 上次结果展示", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  const baseSchedule: AutomationSchedule = {
    kind: "rss_refresh",
    enabled: true,
    interval_value: 30,
    interval_unit: "minute",
    version: 1,
  };

  it("completed 显示「上次成功」", async () => {
    stubFetch({
      schedules: [
        {
          ...baseSchedule,
          last_run_outcome: { state: "completed", finished_at: "2026-08-12T10:00:00Z" },
        },
      ],
    });
    renderSettings();
    expect(await screen.findByText(t("settings.automation.lastOutcome.completed"))).toBeInTheDocument();
  });

  it("failed 显示「上次失败」，partial 显示「上次部分完成」", async () => {
    stubFetch({
      schedules: [
        { ...baseSchedule, last_run_outcome: { state: "failed" } },
        { ...baseSchedule, kind: "group_discovery", last_run_outcome: { state: "partial" } },
      ],
    });
    renderSettings();
    expect(await screen.findByText(t("settings.automation.lastOutcome.failed"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.automation.lastOutcome.partial"))).toBeInTheDocument();
  });

  it("unconfirmed 在上次结果中显示警示状态", async () => {
    stubFetch({ schedules: [{ ...baseSchedule, last_run_outcome: { state: "unconfirmed" } }] });
    renderSettings();
    expect(await screen.findByText(t("settings.automation.lastOutcome.unconfirmed"))).toHaveAttribute("data-tone", "warning");
  });

  it("手动运行结果未确认时不提示成功或失败", async () => {
    vi.clearAllMocks();
    stubFetch({ schedules: [baseSchedule], unconfirmedRun: true });
    renderSettings();
    fireEvent.click(await screen.findByRole("button", { name: t("settings.automation.runNow") }));
    await waitFor(() => {
      expect(toast.warning).toHaveBeenCalledWith(t("settings.automation.runUnconfirmed", {
        name: t("nav.automationKind.rss_refresh"),
      }));
    });
    expect(toast.success).not.toHaveBeenCalled();
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("没有运行记录时不显示上次结果", async () => {
    stubFetch({ schedules: [baseSchedule] });
    renderSettings();
    expect(await screen.findByText(t("nav.automationKind.rss_refresh"))).toBeInTheDocument();
    expect(screen.queryByText(new RegExp([t("settings.automation.lastOutcome.completed"), t("settings.automation.lastOutcome.failed"), t("settings.automation.lastOutcome.partial"), t("settings.automation.lastOutcome.running"), t("settings.automation.lastOutcome.queued")].join("|")))).not.toBeInTheDocument();
  });
});
