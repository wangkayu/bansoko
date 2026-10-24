import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { GlobalSectionView } from "../../lib/api/client";
import { SettingsPolicy } from "./SettingsPolicy";
import { SaveHarness } from "./saveHarness.test-utils";
import { t } from "../../lib/i18n";

// The subscription policy section, edited in place: both the subgroup cap and automatic file
//   alignment write to the draft only, submitted in order by the settings dialog footer's single
//   save (the subgroups section first, then the alignment policy).
//   Subgroup roster management / catalog auto-refresh / hidden anime have moved to each provider's
//   page (covered by SettingsProviderDetail.test.tsx).

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

const subgroupsSection: GlobalSectionView = {
  section_name: "subgroups",
  schema_version: "subgroups-v1",
  value: {
    max_rss: 42,
    entries: [],
    provider_defaults: {
      mikan: { mode: "blacklist", score: 50, groups: [] },
      anibt: { mode: "blacklist", score: 50, groups: [] },
    },
  },
  version: 3,
  saved: true,
};

const policyView = { scope_key: "global", enabled: true, alignment_enabled: false, safety: {}, version: 1 };

type Call = { method: string; url: string; body?: unknown };

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function stubFetch(calls: Call[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      calls.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined });
      if (url.includes("/config/global-sections/subgroups") && method === "PUT") {
        const body = calls[calls.length - 1].body as { value: Record<string, unknown> };
        return json({ ...subgroupsSection, value: body.value, version: 4 });
      }
      if (url.includes("/config/global-sections")) return json([subgroupsSection]);
      if (url.endsWith("/file-maintenance/policy") && method === "PUT") {
        const body = calls[calls.length - 1].body as { alignment_enabled: boolean };
        return json({ ...policyView, alignment_enabled: body.alignment_enabled, version: 2 });
      }
      if (url.endsWith("/file-maintenance/policy")) return json(policyView);
      return json([]);
    }),
  );
}

function renderPolicy() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SaveHarness>
        <SettingsPolicy />
      </SaveHarness>
    </QueryClientProvider>,
  );
}

const saveButton = () => screen.getByRole("button", { name: t("act.save") });

describe("SettingsPolicy 最多订阅字幕组数就地编辑", () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(() => vi.unstubAllGlobals());

  it("非整数与越界值提示且不写草稿，合法值写草稿并由 footer 保存", async () => {
    const calls: Call[] = [];
    stubFetch(calls);
    renderPolicy();

    await screen.findByText(/42 个/);
    expect(screen.queryByRole("button", { name: /保存策略/ })).not.toBeInTheDocument();
    expect(saveButton()).toBeDisabled();

    const input = screen.getByLabelText(t("settings.policy.maxRssField"));
    expect(input).toHaveValue("42");

    fireEvent.change(input, { target: { value: "abc" } });
    expect(screen.getByRole("alert")).toHaveTextContent(t("settings.policy.maxRssInvalid"));
    expect(screen.getByText(/42 个/)).toBeInTheDocument();
    expect(saveButton()).toBeDisabled();

    fireEvent.change(input, { target: { value: "101" } });
    expect(screen.getByRole("alert")).toHaveTextContent(t("settings.policy.maxRssInvalid"));
    expect(saveButton()).toBeDisabled();

    fireEvent.change(input, { target: { value: "50" } });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByText(/50 个/)).toBeInTheDocument();
    expect(await screen.findByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument();
    expect(calls.some((call) => call.method === "PUT")).toBe(false);

    fireEvent.click(saveButton());
    await waitFor(() => expect(calls.filter((call) => call.method === "PUT")).toHaveLength(1));
    const put = calls.find((call) => call.method === "PUT")!;
    expect(put.url).toContain("/config/global-sections/subgroups");
    expect((put.body as { value: { max_rss: number } }).value.max_rss).toBe(50);
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.policy.saved")));
    await waitFor(() => expect(saveButton()).toBeDisabled());
  });

  it("改回原值不计为未保存", async () => {
    stubFetch([]);
    renderPolicy();
    await screen.findByText(/42 个/);
    const input = screen.getByLabelText(t("settings.policy.maxRssField"));
    fireEvent.change(input, { target: { value: "50" } });
    expect(await screen.findByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument();
    fireEvent.change(input, { target: { value: "42" } });
    await waitFor(() => expect(saveButton()).toBeDisabled());
  });
});

describe("SettingsPolicy 瘦身后只含最多订阅字幕组数", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("页面不含目录自动刷新与已隐藏的番剧卡片", async () => {
    stubFetch([]);
    renderPolicy();

    await screen.findByText(/42 个/);
    expect(screen.getByText(t("settings.policy.maxRss"))).toBeInTheDocument();
    expect(screen.queryByText(t("settings.provider.refresh.title"))).not.toBeInTheDocument();
    expect(screen.queryByText(t("settings.provider.blacklist.title"))).not.toBeInTheDocument();
  });
});

describe("全局自动对齐确认", () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(() => vi.unstubAllGlobals());

  const alignmentSwitch = () => screen.findByRole("switch", { name: t("settings.policy.alignment.title") });

  it("确认只写草稿，footer 保存才提交开启", async () => {
    const calls: Call[] = [];
    stubFetch(calls);
    renderPolicy();
    fireEvent.click(await alignmentSwitch());
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(screen.getByText(t("settings.policy.alignment.confirmLine2"))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("settings.policy.alignment.confirm") }));

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(await alignmentSwitch()).toHaveAttribute("aria-checked", "true");
    expect(calls.some((call) => call.method === "PUT")).toBe(false);
    expect(await screen.findByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument();

    fireEvent.click(saveButton());
    await waitFor(() => expect(calls.filter((call) => call.method === "PUT")).toHaveLength(1));
    const put = calls.find((call) => call.method === "PUT")!;
    expect(put.url).toMatch(/\/file-maintenance\/policy$/);
    expect(put.body).toMatchObject({ alignment_enabled: true, expected_version: 1 });
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.policy.alignment.saved")));
  });

  it("取消确认不改变草稿", async () => {
    const calls: Call[] = [];
    stubFetch(calls);
    renderPolicy();
    fireEvent.click(await alignmentSwitch());
    fireEvent.click(await screen.findByRole("button", { name: t("settings.policy.alignment.cancel") }));
    expect(await alignmentSwitch()).toHaveAttribute("aria-checked", "false");
    expect(saveButton()).toBeDisabled();
    expect(calls.some((call) => call.method === "PUT")).toBe(false);
  });

  it("两处都改时先 PUT 订阅策略段，再保存自动对齐，提示取第一条", async () => {
    const calls: Call[] = [];
    stubFetch(calls);
    renderPolicy();
    await screen.findByText(/42 个/);
    fireEvent.change(screen.getByLabelText(t("settings.policy.maxRssField")), { target: { value: "60" } });
    fireEvent.click(await alignmentSwitch());
    fireEvent.click(await screen.findByRole("button", { name: t("settings.policy.alignment.confirm") }));
    expect(await screen.findByText(t("act.unsavedCount", { n: 2 }))).toBeInTheDocument();

    fireEvent.click(saveButton());
    await waitFor(() => expect(calls.filter((call) => call.method === "PUT")).toHaveLength(2));
    const puts = calls.filter((call) => call.method === "PUT");
    expect(puts[0].url).toContain("/config/global-sections/subgroups");
    expect(puts[1].url).toMatch(/\/file-maintenance\/policy$/);
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.policy.saved")));
    expect(toast.success).toHaveBeenCalledTimes(1);
  });
});
