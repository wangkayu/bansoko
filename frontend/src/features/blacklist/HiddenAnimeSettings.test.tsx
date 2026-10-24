import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { CatalogSuppression } from "../../lib/api/client";
import { HiddenAnimeSettings } from "./HiddenAnimeSettings";
import { t } from "../../lib/i18n";

// The only management surface for hidden anime: the "hidden anime" settings section. Covers
//   provider grouping, title filtering, restore (DELETE), entry by ID (PUT) and read-failure
//   degradation.

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

type FetchStub = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

let fetchMock: ReturnType<typeof vi.fn<FetchStub>>;

const suppressions: CatalogSuppression[] = [
  {
    id: "sup-1",
    provider: "mikan",
    provider_entry_ref: "1001",
    title_snapshot: "被隐藏的番 A",
    suppressed_at: "2026-07-01T10:00:00Z",
    version: 1,
  },
  {
    id: "sup-2",
    provider: "mikan",
    provider_entry_ref: "1002",
    title_snapshot: "另一个 Mikan 条目",
    suppressed_at: "2026-07-02T10:00:00Z",
    version: 1,
  },
  {
    id: "sup-3",
    provider: "anibt",
    provider_entry_ref: "2001",
    title_snapshot: "AniBT 屏蔽条目",
    suppressed_at: "2026-07-03T10:00:00Z",
    version: 1,
  },
];

function stubFetch(options: { suppressions?: CatalogSuppression[]; error?: boolean } = {}) {
  fetchMock = vi.fn<FetchStub>(async (input, init) => {
    const url = String(input);
    if (url.includes("/catalog/suppressions")) {
      if (options.error) {
        return new Response(JSON.stringify({ code: "DATABASE_UNAVAILABLE", message: "unavailable", request_id: "r-1" }), {
          status: 503,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (init?.method === "DELETE") {
        return new Response(null, { status: 204 });
      }
      if (init?.method === "PUT") {
        const parts = url.split("/catalog/suppressions/")[1]?.split("/") ?? [];
        const body = JSON.parse(String(init.body ?? "{}")) as { title_snapshot?: string };
        const created: CatalogSuppression = {
          id: `sup-${parts[1] ?? "x"}`,
          provider: (parts[0] ?? "mikan") as CatalogSuppression["provider"],
          provider_entry_ref: parts[1] ?? "",
          title_snapshot: body.title_snapshot ?? "",
          suppressed_at: "2026-07-31T12:00:00Z",
          version: 1,
        };
        return new Response(JSON.stringify(created), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response(JSON.stringify(options.suppressions ?? suppressions), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    return new Response(JSON.stringify([]), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
}

function renderSection() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <HiddenAnimeSettings />
    </QueryClientProvider>,
  );
}

describe("HiddenAnimeSettings", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("按 provider 分组展示条目标题与时间", async () => {
    stubFetch();
    renderSection();

    expect(await screen.findByText("被隐藏的番 A")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /Mikan/ })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /AniBT/ })).toBeInTheDocument();
    expect(screen.getByText("AniBT 屏蔽条目")).toBeInTheDocument();
    // Two entry points: card hide + entry by ID within the section
    expect(screen.getByLabelText(t("blacklist.idLabel"))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: new RegExp(t("blacklist.add")) })).toBeInTheDocument();
  });

  it("作为设置分区渲染标题与说明，不是弹窗", async () => {
    stubFetch();
    renderSection();
    await screen.findByText("被隐藏的番 A");
    expect(screen.getByRole("heading", { level: 2, name: t("blacklist.title") })).toBeInTheDocument();
    expect(screen.getByText(t("blacklist.description"))).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("按番剧 ID 录入调用 PUT 并清空输入，标题留空用 ID 占位", async () => {
    stubFetch();
    renderSection();

    fireEvent.change(await screen.findByLabelText(t("blacklist.idLabel")), { target: { value: "3701" } });
    fireEvent.click(screen.getByRole("button", { name: new RegExp(t("blacklist.add")) }));

    await waitFor(() => {
      const puts = fetchMock.mock.calls.filter(
        ([input, init]) => String(input).includes("/catalog/suppressions/mikan/3701") && init?.method === "PUT",
      );
      expect(puts).toHaveLength(1);
      expect(JSON.parse(String(puts[0][1]?.body))).toEqual({ title_snapshot: "3701" });
    });
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("blacklist.added", { title: "3701" })));
    expect(screen.getByLabelText(t("blacklist.idLabel"))).toHaveValue("");
  });

  it("按 ID 录入可指定 provider 与标题", async () => {
    stubFetch();
    renderSection();

    fireEvent.click(await screen.findByRole("radio", { name: "AniBT" }));
    fireEvent.change(screen.getByLabelText(t("blacklist.idLabel")), { target: { value: "b-9" } });
    fireEvent.change(screen.getByLabelText(t("blacklist.titleLabel")), { target: { value: "手动番" } });
    fireEvent.click(screen.getByRole("button", { name: new RegExp(t("blacklist.add")) }));

    await waitFor(() => {
      const puts = fetchMock.mock.calls.filter(
        ([input, init]) => String(input).includes("/catalog/suppressions/anibt/b-9") && init?.method === "PUT",
      );
      expect(puts).toHaveLength(1);
      expect(JSON.parse(String(puts[0][1]?.body))).toEqual({ title_snapshot: "手动番" });
    });
  });

  it("按 ID 录入空 ID 内联报错，不发请求", async () => {
    stubFetch();
    renderSection();

    fireEvent.click((await screen.findByRole("button", { name: new RegExp(t("blacklist.add")) })));

    expect(await screen.findByRole("alert")).toHaveTextContent(t("blacklist.idRequired"));
    expect(
      fetchMock.mock.calls.filter(([, init]) => init?.method === "PUT"),
    ).toHaveLength(0);
  });

  it("顶部筛选框按标题过滤，分组计数同步", async () => {
    stubFetch();
    renderSection();

    fireEvent.change(await screen.findByLabelText(t("blacklist.filterLabel")), { target: { value: "番 A" } });

    expect(screen.getByText("被隐藏的番 A")).toBeInTheDocument();
    expect(screen.queryByText("另一个 Mikan 条目")).not.toBeInTheDocument();
    expect(screen.queryByText("AniBT 屏蔽条目")).not.toBeInTheDocument();
  });

  it("没有条目时只有一个空状态，不显示筛选框和站点分组", async () => {
    stubFetch({ suppressions: [] });
    renderSection();

    expect(await screen.findByText(t("blacklist.empty"))).toBeInTheDocument();
    expect(screen.getAllByText(t("blacklist.empty"))).toHaveLength(1);
    expect(screen.queryByLabelText(t("blacklist.filterLabel"))).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: /Mikan|AniBT/ })).not.toBeInTheDocument();
    // Entry by ID still works
    expect(screen.getByLabelText(t("blacklist.idLabel"))).toBeInTheDocument();
  });

  it("筛选无匹配时只显示一个无匹配状态", async () => {
    stubFetch();
    renderSection();

    fireEvent.change(await screen.findByLabelText(t("blacklist.filterLabel")), { target: { value: "不存在" } });
    expect(screen.getByText(t("search.noMatch"))).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: /Mikan|AniBT/ })).not.toBeInTheDocument();
  });

  it("行内恢复调用 unsuppress DELETE 并提示", async () => {
    stubFetch();
    renderSection();

    fireEvent.click(await screen.findByRole("button", { name: t("blacklist.restoreAria", { title: "被隐藏的番 A" }) }));

    await waitFor(() => {
      const deletes = fetchMock.mock.calls.filter(
        ([input, init]) => String(input).includes("/catalog/suppressions/mikan/1001") && init?.method === "DELETE",
      );
      expect(deletes).toHaveLength(1);
    });
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("blacklist.restored", { title: "被隐藏的番 A" })));
  });

  it("读取失败时整体降级为错误区块，不渲染分组空壳", async () => {
    stubFetch({ error: true });
    renderSection();

    expect(await screen.findByText(t("error.DATABASE_UNAVAILABLE"))).toBeInTheDocument();
    expect(screen.queryByLabelText(t("blacklist.filterLabel"))).not.toBeInTheDocument();
    expect(screen.queryByText("Mikan")).not.toBeInTheDocument();
  });
});
