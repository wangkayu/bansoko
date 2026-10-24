import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { GlobalSectionView } from "../../lib/api/client";
import { SettingsDialog } from "./SettingsDialog";
import { t } from "../../lib/i18n";

// Switching sections in the settings dialog no longer silently resets via key={active} — when the
//   current section has unsaved changes a ConfirmDialog asks first ("discard unsaved changes?");
//   confirming leaves, cancelling stays. In-place-edited sections (subscription policy, the two
//   provider pages, unrecognized episodes) register their endpoints as save parts with a single
//   save in the dialog footer, and the departing confirmation looks at the registered unsaved-parts
//   count. Card -> edit-dialog sections have no footer save.

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

// Buttons/links are located by accessible role, so Chinese copy split across several spans never breaks text-based queries.
function buttonByRole(name: string): HTMLButtonElement {
  return screen.getByRole("button", { name });
}

async function findButtonByRole(name: string): Promise<HTMLButtonElement> {
  return screen.findByRole("button", { name });
}

function linkByRole(name: string): HTMLAnchorElement {
  return screen.getByRole("link", { name });
}

function stubFetch() {
  // Stateful: the re-read after a PUT returns the just-saved value.
  let section = subgroupsSection;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      let body: unknown = [];
      if (init?.method === "PUT" && url.includes("/config/global-sections/subgroups")) {
        const request = JSON.parse(String(init.body)) as { value: Record<string, unknown> };
        section = { ...section, value: request.value, version: section.version + 1 };
        body = section;
      } else if (url.includes("/config/subgroups/groups?")) {
        body = { candidates: [{ provider: "mikan", group_ref: "58", display_name: "SampleGroup", source: "user", available: true }] };
      } else if (url.includes("/config/global-sections")) body = [section];
      else if (url.includes("/catalog/suppressions")) body = [];
      else if (url.includes("/config/connections")) body = [];
      else if (url.includes("/config/network-routes")) body = [];
      else if (url.endsWith("/file-maintenance/policy")) {
        body = { scope_key: "global", enabled: true, alignment_enabled: false, safety: {}, version: 1 };
      } else if (url.includes("/preference")) {
        body = { season: "2026-summer", refresh_enabled: true, schedule: null, version: 2, updated_at: "2026-08-02T00:00:00Z" };
      }
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
}

function renderDialog(initialPath = "/settings/policy", initialEntries?: Array<string | { pathname: string; state?: unknown }>) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createMemoryRouter(
    [
      { path: "/settings/:category", element: <SettingsDialog /> },
      { path: "/", element: <div>目录页</div> },
    ],
    { initialEntries: initialEntries ?? [initialPath] },
  );
  const view = render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { ...view, router };
}

// The subscription policy's page-level draft: editing the subgroup cap (max_rss) in place means unsaved changes.
async function makePolicyDirty() {
  const input = await screen.findByLabelText(t("settings.policy.maxRssField"));
  fireEvent.change(input, { target: { value: "50" } });
  expect(await screen.findByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument();
}

// The provider page's (mikan) page-level draft: picking subgroups from dictionary search into the roster card.
async function makeProviderDirty() {
  const input = await screen.findByLabelText(t("settings.provider.groups.searchAria", { label: "Mikan" }));
  fireEvent.change(input, { target: { value: "SampleGroup" } });
  // Roster entries store only dictionary keys: clicking a search result adds the group_ref to the draft.
  fireEvent.click(await screen.findByRole("button", { name: t("settings.provider.groups.resultAria", { name: "SampleGroup" }) }));
  expect(await screen.findByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument();
}

function groupsCardName(label: "Mikan" | "AniBT") {
  return t("settings.provider.groups.dialogTitle", { label });
}

describe("SettingsDialog 切分区 dirty 确认（批次 4）", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("订阅策略 dirty 时切分区先弹确认，确认后离开", async () => {
    stubFetch();
    renderDialog();

    await screen.findByText(t("settings.policy.maxRssSummary", { n: 42 }));
    await makePolicyDirty();

    fireEvent.click(linkByRole(t("settings.nav.connections")));
    expect(await screen.findByText(t("common.discardEditsTitle"))).toBeInTheDocument();

    fireEvent.click(buttonByRole(t("common.discardChanges")));
    expect(await findButtonByRole(t("settings.connections.new"))).toBeInTheDocument();
  }, 10_000);

  it("确认弹窗取消则留在原分区，草稿保留", async () => {
    stubFetch();
    renderDialog();

    await screen.findByText(t("settings.policy.maxRssSummary", { n: 42 }));
    await makePolicyDirty();

    fireEvent.click(linkByRole(t("settings.nav.connections")));
    expect(await screen.findByText(t("common.discardEditsTitle"))).toBeInTheDocument();

    fireEvent.click(buttonByRole(t("ui.cancel")));
    expect(screen.queryByText(t("common.discardEditsTitle"))).not.toBeInTheDocument();
    // Nothing switched: still on subscription policy, the max_rss draft kept (the card shows the new value 50) and the footer still reports unsaved.
    expect(screen.getByLabelText(t("settings.policy.maxRssField"))).toHaveValue("50");
    expect(screen.getByText(t("settings.policy.maxRssSummary", { n: 50 }))).toBeInTheDocument();
    expect(screen.getByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument();
    expect(buttonByRole(t("act.save"))).toBeEnabled();
  }, 10_000);

  it("Mikan 配置页 dirty 时切分区同样先弹确认", async () => {
    stubFetch();
    renderDialog("/settings/mikan");

    await screen.findByRole("group", { name: groupsCardName("Mikan") });
    await makeProviderDirty();

    fireEvent.click(linkByRole(t("settings.nav.connections")));
    expect(await screen.findByText(t("common.discardEditsTitle"))).toBeInTheDocument();

    fireEvent.click(buttonByRole(t("ui.cancel")));
    expect(screen.queryByText(t("common.discardEditsTitle"))).not.toBeInTheDocument();
    expect(buttonByRole(t("act.save"))).toBeEnabled();
    expect(buttonByRole(t("settings.provider.groups.remove", { name: "SampleGroup" }))).toBeInTheDocument();
  });

  it("history back dirty 时取消留在设置，确认后回到目录", async () => {
    stubFetch();
    const { router } = renderDialog("/settings/policy", ["/", "/settings/policy"]);

    await screen.findByText(t("settings.policy.maxRssSummary", { n: 42 }));
    await makePolicyDirty();

    await router.navigate(-1);
    expect(await screen.findByText(t("common.discardEditsTitle"))).toBeInTheDocument();
    fireEvent.click(buttonByRole(t("ui.cancel")));
    expect(screen.queryByText(t("common.discardEditsTitle"))).not.toBeInTheDocument();
    expect(screen.getByText(t("settings.policy.maxRssSummary", { n: 50 }))).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/settings/policy");

    await router.navigate(-1);
    expect(await screen.findByText(t("common.discardEditsTitle"))).toBeInTheDocument();
    fireEvent.click(buttonByRole(t("common.discardChanges")));
    await waitFor(() => expect(screen.getByText("目录页")).toBeInTheDocument());
    expect(router.state.location.pathname).toBe("/");
  }, 10_000);

  it("非 dirty 切分区不弹确认", async () => {
    stubFetch();
    renderDialog();

    await screen.findByText(t("settings.policy.maxRssSummary", { n: 42 }));
    fireEvent.click(linkByRole(t("settings.nav.connections")));

    expect(await findButtonByRole(t("settings.connections.new"))).toBeInTheDocument();
    expect(screen.queryByText(t("common.discardEditsTitle"))).not.toBeInTheDocument();
  });
});

// Single save: in-place-edited sections get the dialog footer's single save; card -> edit-dialog sections do not.
describe("SettingsDialog footer 唯一保存", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("订阅策略分区有 footer 保存，未改动时不可点，改动后可点", async () => {
    stubFetch();
    renderDialog();
    await screen.findByText(t("settings.policy.maxRssSummary", { n: 42 }));
    expect(screen.getAllByRole("button", { name: t("act.save") })).toHaveLength(1);
    expect(buttonByRole(t("act.save"))).toBeDisabled();
    await makePolicyDirty();
    expect(buttonByRole(t("act.save"))).toBeEnabled();
  });

  it("Mikan 配置页有 footer 保存", async () => {
    stubFetch();
    renderDialog("/settings/mikan");
    await screen.findByRole("group", { name: groupsCardName("Mikan") });
    expect(await findButtonByRole(t("act.save"))).toBeDisabled();
  });

  it("卡片 → 编辑弹窗的分区没有 footer 保存", async () => {
    stubFetch();
    renderDialog("/settings/connections");
    await findButtonByRole(t("settings.connections.new"));
    expect(screen.queryByRole("button", { name: t("act.save") })).not.toBeInTheDocument();
  });

  it("保存后未保存处数清零，切分区不再弹确认", async () => {
    stubFetch();
    renderDialog();
    await screen.findByText(t("settings.policy.maxRssSummary", { n: 42 }));
    await makePolicyDirty();
    fireEvent.click(buttonByRole(t("act.save")));
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.policy.saved")));
    await waitFor(() => expect(buttonByRole(t("act.save"))).toBeDisabled());
    expect(screen.getByLabelText(t("settings.policy.maxRssField"))).toHaveValue("50");

    fireEvent.click(linkByRole(t("settings.nav.connections")));
    expect(await findButtonByRole(t("settings.connections.new"))).toBeInTheDocument();
    expect(screen.queryByText(t("common.discardEditsTitle"))).not.toBeInTheDocument();
  });
});

// The group structure: services [connections, download] / new-subscription defaults
//   [rules, unclassified, policy] / sites [anibt, mikan, hidden] / system
//   [automation, notifications, updates, backup, ai]. anibt and mikan render the same
//   parameterized SettingsProviderDetail component.
describe("SettingsDialog 新分组结构与 provider 导航页", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  function groupLinks(groupKey: Parameters<typeof t>[0]) {
    const group = screen.getByRole("group", { name: t(groupKey) });
    return within(group).getAllByRole("link").map((link) => link.textContent);
  }

  it("导航按 服务 / 新订阅默认值 / 站点 / 系统 分组，组内顺序固定", async () => {
    stubFetch();
    renderDialog();

    // Wait for any section to finish loading.
    await screen.findByText(t("settings.policy.maxRssSummary", { n: 42 }));

    expect(groupLinks("settings.nav.groupService")).toEqual([t("settings.nav.connections"), t("settings.nav.download")]);
    expect(groupLinks("settings.nav.groupDefaults")).toEqual([
      t("settings.nav.rules"),
      t("settings.nav.unclassified"),
      t("settings.nav.policy"),
    ]);
    expect(groupLinks("settings.nav.groupSite")).toEqual([t("settings.nav.anibt"), t("settings.nav.mikan"), t("settings.nav.hidden")]);
    expect(groupLinks("settings.nav.groupSystem")).toEqual([
      t("settings.nav.account"),
      t("settings.nav.automation"),
      t("settings.nav.notifications"),
      t("settings.nav.updates"),
      t("settings.nav.backup"),
      t("settings.nav.token"),
    ]);
    expect(screen.queryByRole("link", { name: "文件维护" })).not.toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "文件维护" })).not.toBeInTheDocument();
  });

  // Naming variables is hidden this version (restore steps are in the SettingsDialog.tsx categories comment).
  it("命名变量分区隐藏：导航无入口，/settings/vars 深链回落到服务连接", async () => {
    stubFetch();
    renderDialog("/settings/vars");

    expect((await screen.findAllByRole("heading", { level: 2, name: t("settings.nav.connections") })).length).toBeGreaterThan(0);
    expect(screen.queryByRole("link", { name: t("settings.nav.vars") })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: t("varsEditor.title") })).not.toBeInTheDocument();
    const urls = vi.mocked(fetch).mock.calls.map(([input]) => String(input));
    expect(urls.some((url) => url.includes("/naming-vars"))).toBe(false);
  });

  it("anibt 导航渲染 AniBT 配置页", async () => {
    stubFetch();
    renderDialog("/settings/anibt");

    // The visible page title shares a name with the sr-only section title, so a button asserts the provider page rendered.
    expect(await screen.findByRole("group", { name: groupsCardName("AniBT") })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("settings.provider.rules.addAria", { label: "AniBT" }) })).toBeInTheDocument();
  });

  it("mikan 导航渲染 Mikan 配置页", async () => {
    stubFetch();
    renderDialog("/settings/mikan");

    expect(await screen.findByRole("group", { name: groupsCardName("Mikan") })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("settings.provider.rules.addAria", { label: "Mikan" }) })).toBeInTheDocument();
  });

  it("订阅策略页只含 max_rss，无目录刷新与隐藏列表", async () => {
    stubFetch();
    renderDialog();

    await screen.findByText(t("settings.policy.maxRssSummary", { n: 42 }));
    expect(screen.getByText(t("settings.policy.maxRss"))).toBeInTheDocument();
    // Catalog auto-refresh and the hidden-anime cards have moved to the provider pages (a similarly named nav section link does not count).
    const content = screen.getByTestId("settings-content");
    expect(within(content).queryByText(t("settings.provider.refresh.title"))).not.toBeInTheDocument();
    expect(within(content).queryByText(t("settings.provider.blacklist.title"))).not.toBeInTheDocument();
  });

  it("站点分组含「隐藏的番剧」分区，渲染隐藏列表管理", async () => {
    stubFetch();
    const { router } = renderDialog();

    await screen.findByText(t("settings.policy.maxRssSummary", { n: 42 }));
    fireEvent.click(screen.getByRole("link", { name: t("settings.nav.hidden") }));

    await waitFor(() => expect(router.state.location.pathname).toBe("/settings/hidden"));
    const content = screen.getByTestId("settings-content");
    expect(await within(content).findByText(t("blacklist.empty"))).toBeInTheDocument();
    expect(within(content).getByLabelText(t("blacklist.idLabel"))).toBeInTheDocument();
    expect(within(content).getByRole("button", { name: new RegExp(t("blacklist.add")) })).toBeInTheDocument();
  });

  it("provider 页隐藏卡片的「管理」切到「隐藏的番剧」分区", async () => {
    stubFetch();
    const { router } = renderDialog("/settings/mikan");

    await screen.findByRole("group", { name: groupsCardName("Mikan") });
    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.blacklist.manage") }));

    await waitFor(() => expect(router.state.location.pathname).toBe("/settings/hidden"));
    expect(await screen.findByLabelText(t("blacklist.idLabel"))).toBeInTheDocument();
  });
});

// The F-tier shell: Esc closes back to the catalog; on a nested edit dialog Esc closes only the top
//   layer; the phone's two-level nav (jsdom evaluates no media queries, so the phone: variant
//   classes are asserted rather than visibility).
describe("SettingsDialog F 档外壳", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("Esc 关闭设置并回到目录", async () => {
    stubFetch();
    const { router } = renderDialog("/settings/connections");
    await findButtonByRole(t("settings.connections.new"));

    await userEvent.keyboard("{Escape}");

    await waitFor(() => expect(screen.getByText("目录页")).toBeInTheDocument());
    expect(router.state.location.pathname).toBe("/");
  });

  it("关闭设置：由底座打开时回退到底座历史项，不新增历史", async () => {
    stubFetch();
    const { router } = renderDialog("/settings/connections", [
      "/?source=mikan&season=2026-spring",
      { pathname: "/settings/connections", state: { routeDialog: true } },
    ]);
    // Starts on the last item (settings).
    await findButtonByRole(t("settings.connections.new"));

    await userEvent.keyboard("{Escape}");

    await waitFor(() => expect(screen.getByText("目录页")).toBeInTheDocument());
    expect(router.state.location.pathname).toBe("/");
    expect(router.state.location.search).toBe("?source=mikan&season=2026-spring");
    expect(router.state.historyAction).toBe("POP");
  });

  it("关闭设置：深链进入时替换为底座地址，不留下设置历史项", async () => {
    stubFetch();
    const { router } = renderDialog("/settings/connections");
    await findButtonByRole(t("settings.connections.new"));

    await userEvent.keyboard("{Escape}");

    await waitFor(() => expect(screen.getByText("目录页")).toBeInTheDocument());
    expect(router.state.historyAction).toBe("REPLACE");
    expect(router.state.location.pathname).toBe("/");
  });

  it("嵌套编辑弹窗上 Esc 只关最上层，设置保持打开", async () => {
    stubFetch();
    const { router } = renderDialog("/settings/mikan");
    await screen.findByRole("group", { name: groupsCardName("Mikan") });

    fireEvent.click(await findButtonByRole(t("settings.provider.dictionary.manageAria", { label: "Mikan" })));
    await screen.findByLabelText(t("settings.provider.dictionary.filterAria", { label: "Mikan" }));

    await userEvent.keyboard("{Escape}");

    await waitFor(() => expect(screen.queryByLabelText(t("settings.provider.dictionary.filterAria", { label: "Mikan" }))).not.toBeInTheDocument());
    expect(router.state.location.pathname).toBe("/settings/mikan");
    expect(screen.getByRole("group", { name: groupsCardName("Mikan") })).toBeInTheDocument();
  });

  it("手机：从顶栏入口先看到分区列表，选分区进内容，返回回到列表", async () => {
    stubFetch();
    renderDialog("/settings/connections", [{ pathname: "/settings/connections", state: { pane: "nav" } }]);
    await findButtonByRole(t("settings.connections.new"));

    const nav = screen.getByRole("navigation", { name: t("settings.nav.label") });
    const content = screen.getByTestId("settings-content");
    // The list layer: content hidden on phones.
    expect(content.className).toContain("tw:phone:hidden");
    // The nav's scroll column (the nav scrolls itself) hides/shows whole on phones.
    expect(nav.parentElement!.className).not.toContain("tw:phone:hidden");

    fireEvent.click(linkByRole(t("settings.nav.automation")));
    await waitFor(() => expect(screen.getByTestId("settings-content").className).not.toContain("tw:phone:hidden"));
    expect(screen.getByRole("navigation", { name: t("settings.nav.label") }).parentElement!.className).toContain("tw:phone:hidden");

    fireEvent.click(screen.getByRole("button", { name: t("settings.nav.backToList") }));
    expect(screen.getByRole("navigation", { name: t("settings.nav.label") }).parentElement!.className).not.toContain("tw:phone:hidden");
    expect(screen.getByTestId("settings-content").className).toContain("tw:phone:hidden");
  });

  it("手机：深链直接进入分区内容", async () => {
    stubFetch();
    renderDialog("/settings/notifications");
    expect(screen.getByTestId("settings-content").className).not.toContain("tw:phone:hidden");
    expect(screen.getByRole("navigation", { name: t("settings.nav.label") }).parentElement!.className).toContain("tw:phone:hidden");
  });
});
