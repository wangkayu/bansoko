import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { GlobalSectionView } from "../../lib/api/client";
import { SettingsProviderDetail } from "./SettingsProviderDetail";
import { SaveHarness } from "./saveHarness.test-utils";
import { t } from "../../lib/i18n";

// One provider's configuration page (SettingsProviderDetail, shared by anibt/mikan): a subgroup
//   dictionary (an independently owned resource — discover/instant-add/delete save immediately;
//   inline pinned scores go into the page draft), subgroup scoring (mode/default score/roster,
//   edited in place) and name scoring both write the page draft, and catalog auto-refresh is a draft
//   too. The settings dialog footer's single save first submits subgroups with a whole-section CAS
//   (the draft starts from the complete section value — max_rss, entries, and the other provider's
//   fields pass through untouched, never wiped), then submits the refresh preference. Hidden-anime
//   statistics filter to the current provider.
//   Hard rule: the roster may reference only keys already in the dictionary, and online lookup
//   exists only in the dictionary card's discover.

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

type Candidate = {
  provider: string;
  group_ref: string;
  display_name: string;
  source: "collected" | "user";
  available: boolean;
};

function candidate(overrides: Partial<Candidate> & { group_ref: string }): Candidate {
  return { provider: "mikan", display_name: "", source: "user", available: true, ...overrides };
}

const LOLIHOUSE = candidate({ group_ref: "58", display_name: "SampleGroup" });
const NANRAWS = candidate({ group_ref: "99", display_name: "NanRaws" });

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
    name_keywords: {
      anibt: [{ keyword: "简繁", score: 10 }],
      mikan: [],
    },
  },
  version: 3,
  saved: true,
};

type FetchStub = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

type DictionaryPage = { candidates: Candidate[]; next_offset: number | null; total: number };

function dictionaryPage(candidates: Candidate[], nextOffset: number | null = null, total = candidates.length): DictionaryPage {
  return { candidates, next_offset: nextOffset, total };
}

type StubOptions = {
  putStatus?: number;
  section?: GlobalSectionView;
  /** The source=user read result. */
  user?: Candidate[];
  /** The lookup (POST) result. */
  retrieve?: Candidate[];
  /** The status and error code when the lookup (POST) fails. */
  retrieveStatus?: number;
  retrieveCode?: string;
  /** The dictionary query filtered by q (GET?q=). */
  search?: (q: string) => Candidate[];
  /** The whole-dictionary paged read without q or source (GET?offset=). */
  browse?: (offset: number, params: URLSearchParams) => DictionaryPage;
  /** The deletion's status and error code; default 204. */
  deleteStatus?: number;
  deleteCode?: string;
};

function stubSectionFetch(options: StubOptions = {}) {
  const fetchMock = vi.fn<FetchStub>(async (input, init) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    if (method === "PUT" && url.includes("/config/global-sections/subgroups")) {
      if (options.putStatus === 409) {
        return jsonResponse({ code: "VERSION_CONFLICT", message: "版本冲突", current_version: 8, request_id: "r-1" }, 409);
      }
      const requestBody = JSON.parse(String(init?.body)) as { value: unknown };
      return jsonResponse({ ...(options.section ?? subgroupsSection), value: requestBody.value, version: subgroupsSection.version + 1 });
    }
    if (method === "POST" && url.includes("/config/subgroups/groups")) {
      if (options.retrieveStatus !== undefined && options.retrieveStatus !== 200) {
        return jsonResponse({ code: options.retrieveCode, message: "lookup failed", request_id: "r-2" }, options.retrieveStatus);
      }
      return jsonResponse({ candidates: options.retrieve ?? [] });
    }
    if (method === "DELETE" && url.includes("/config/subgroups/groups/")) {
      if (options.deleteStatus !== undefined && options.deleteStatus !== 204) {
        return jsonResponse({ code: options.deleteCode, message: "request rejected", request_id: "r-9" }, options.deleteStatus);
      }
      return new Response(null, { status: 204 });
    }
    if (url.includes("/config/subgroups/groups?")) {
      const params = new URL(url, "http://localhost").searchParams;
      if (params.get("source") === "user") return jsonResponse(dictionaryPage(options.user ?? []));
      const q = params.get("q") ?? "";
      if (q === "") {
        // Browsing the whole dictionary (the dialog's filter empty): the user layer by default, with browse specifying the page.
        if (options.browse) return jsonResponse(options.browse(Number(params.get("offset") ?? "0"), params));
        return jsonResponse(dictionaryPage(options.user ?? []));
      }
      return jsonResponse(dictionaryPage((options.search ?? (() => []))(q)));
    }
    let body: unknown = [];
    if (url.includes("/config/global-sections")) body = [options.section ?? subgroupsSection];
    else if (url.includes("/catalog/suppressions")) body = [];
    else if (url.includes("/preference")) {
      const provider = url.includes("/providers/mikan/") ? "mikan" : "anibt";
      body = {
        season: "2026-summer",
        refresh_enabled: provider === "mikan",
        schedule: null,
        version: 2,
        updated_at: "2026-08-02T00:00:00Z",
      };
    }
    return jsonResponse(body);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function sectionPutCalls(fetchMock: ReturnType<typeof vi.fn<FetchStub>>) {
  return fetchMock.mock.calls.filter(
    ([input, init]) => String(input).includes("/config/global-sections/subgroups") && init?.method === "PUT",
  );
}

function putBody(fetchMock: ReturnType<typeof vi.fn<FetchStub>>) {
  return JSON.parse(String(sectionPutCalls(fetchMock)[0]![1]?.body)) as {
    value: Record<string, unknown> & {
      provider_defaults: Record<string, { groups: string[] }>;
      name_keywords: Record<string, { keyword: string; score: number }[]>;
    };
  };
}

function callsMatching(fetchMock: ReturnType<typeof vi.fn<FetchStub>>, predicate: (url: string, init?: RequestInit) => boolean) {
  return fetchMock.mock.calls.filter(([input, init]) => predicate(String(input), init));
}

function renderProvider(provider: "anibt" | "mikan" = "mikan") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <SaveHarness>
          <SettingsProviderDetail provider={provider} />
        </SaveHarness>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

async function waitForSettings(label: "Mikan" | "AniBT" = "Mikan") {
  await screen.findByText(t("settings.provider.title", { label }));
  await waitFor(() => expect(screen.queryByText(t("settings.provider.loading", { label }))).not.toBeInTheDocument());
}

const footerSave = () => screen.getByRole("button", { name: t("act.save") });

/** The subgroup roster card once loaded, edited in place. */
async function groupsCard(label: "Mikan" | "AniBT" = "Mikan") {
  await waitForSettings(label);
  const card = await screen.findByRole("group", { name: t("settings.provider.groups.dialogTitle", { label }) });
  return card;
}

/** Opens the dictionary dialog (the dictionary list: filtering, inline pinned scores, deletion). */
async function openDictionaryDialog(label: "Mikan" | "AniBT" = "Mikan") {
  await waitForSettings(label);
  fireEvent.click(screen.getByRole("button", { name: t("settings.provider.dictionary.manageAria", { label }) }));
  await screen.findByLabelText(t("settings.provider.dictionary.filterAria", { label }));
}

/** Opens the discover dialog (looks up and confirms a subgroup online, adding it to the dictionary). */
async function openDiscoverDialog(label: "Mikan" | "AniBT" = "Mikan") {
  await waitForSettings(label);
  fireEvent.click(screen.getByRole("button", { name: t("settings.provider.dictionary.discoverAria", { label }) }));
  await screen.findByLabelText(t("settings.provider.dictionary.searchAria", { label }));
}

const withMikanGroups = (groups: string[]): GlobalSectionView => ({
  ...subgroupsSection,
  value: {
    ...(subgroupsSection.value as Record<string, unknown>),
    provider_defaults: { mikan: { mode: "blacklist", score: 50, groups }, anibt: { mode: "blacklist", score: 50, groups: [] } },
  },
});

// The roster card, edited in place: search the dictionary -> click a result (lookup is the filter,
//   clicking is adding). A roster entry stores only its key with no free-text entry; changes go into
//   the page draft, saved by the footer.
describe("SettingsProviderDetail 字幕组名单（就地编辑，从字典挑）", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("卡片就地编辑：没有管理弹窗与页头保存按钮", async () => {
    stubSectionFetch();
    renderProvider("mikan");
    const card = await groupsCard();

    expect(within(card).getByRole("radiogroup", { name: t("settings.provider.groups.modeAria") })).toBeInTheDocument();
    expect(within(card).getByRole("radio", { name: t("settings.provider.groups.modeBlacklist") })).toHaveAttribute("aria-checked", "true");
    expect(within(card).getByLabelText(t("settings.provider.groups.searchAria", { label: "Mikan" }))).toBeInTheDocument();
    expect(within(card).getByText(t("settings.provider.groups.listEmpty"))).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    // The page has only the footer's single save, disabled until something changes.
    expect(screen.getAllByRole("button", { name: t("act.save") })).toHaveLength(1);
    expect(footerSave()).toBeDisabled();
  });

  it("从字典搜索点选加入名单，footer 保存随 PUT 上送", async () => {
    const fetchMock = stubSectionFetch({ search: (q) => (q.toLowerCase().includes("samplegroup") ? [LOLIHOUSE] : []) });
    renderProvider("mikan");
    const card = await groupsCard();

    fireEvent.change(within(card).getByLabelText(t("settings.provider.groups.searchAria", { label: "Mikan" })), { target: { value: "SampleGroup" } });
    fireEvent.click(await within(card).findByRole("button", { name: t("settings.provider.groups.resultAria", { name: "SampleGroup" }) }));
    // The roster is a set of wrap-around badges, not a row-per-entry table.
    const badges = within(card).getByRole("list", { name: t("settings.provider.groups.listTitle") });
    expect(within(badges).getAllByRole("listitem")).toHaveLength(1);
    expect(within(card).getByRole("button", { name: t("settings.provider.groups.remove", { name: "SampleGroup" }) })).toBeInTheDocument();
    expect(await screen.findByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument();
    expect(sectionPutCalls(fetchMock)).toHaveLength(0);

    fireEvent.click(footerSave());
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.provider.saved", { label: "Mikan" })));

    expect(sectionPutCalls(fetchMock)).toHaveLength(1);
    expect(putBody(fetchMock).value.provider_defaults.mikan.groups).toEqual(["58"]);
    await waitFor(() => expect(footerSave()).toBeDisabled());
  });

  it("模式与默认分写草稿并随保存上送；非法默认分提示且不写入", async () => {
    const fetchMock = stubSectionFetch();
    renderProvider("mikan");
    const card = await groupsCard();

    fireEvent.click(within(card).getByRole("radio", { name: t("settings.provider.groups.modeWhitelist") }));
    expect(within(card).getByText(t("settings.provider.groups.whitelistHelp"))).toBeInTheDocument();

    const score = within(card).getByLabelText(t("settings.site.defaultScoreRange"));
    fireEvent.change(score, { target: { value: "0" } });
    expect(within(card).getByRole("alert")).toHaveTextContent(t("settings.site.defaultScoreInvalid"));
    fireEvent.change(score, { target: { value: "70" } });
    expect(within(card).queryByRole("alert")).not.toBeInTheDocument();

    fireEvent.click(footerSave());
    await waitFor(() => expect(sectionPutCalls(fetchMock)).toHaveLength(1));
    expect(putBody(fetchMock).value.provider_defaults.mikan).toEqual({ mode: "whitelist", score: 70, groups: [] });
  });

  it("名单拒绝自由文本：搜索框回车不会把文本当作名单项", async () => {
    stubSectionFetch({ search: (q) => (q.toLowerCase().includes("samplegroup") ? [LOLIHOUSE] : []) });
    renderProvider("mikan");
    const card = await groupsCard();

    const searchBox = within(card).getByLabelText(t("settings.provider.groups.searchAria", { label: "Mikan" }));
    fireEvent.change(searchBox, { target: { value: "58" } });
    fireEvent.keyDown(searchBox, { key: "Enter" });

    // The roster stays empty: the search box filters the dictionary only, and adding happens by clicking a result (whose key came from the dictionary).
    await waitFor(() => expect(within(card).queryByRole("button", { name: t("settings.provider.groups.resultAria", { name: "58" }) })).not.toBeInTheDocument());
    expect(within(card).getByText(t("settings.provider.groups.listEmpty"))).toBeInTheDocument();
    expect(footerSave()).toBeDisabled();
  });

  it("搜不到想要的组时给出提示并可跳到字典弹窗", async () => {
    stubSectionFetch({ search: () => [] });
    renderProvider("mikan");
    const card = await groupsCard();

    fireEvent.change(within(card).getByLabelText(t("settings.provider.groups.searchAria", { label: "Mikan" })), { target: { value: "没人有的组" } });

    expect(await within(card).findByText(t("settings.provider.groups.searchEmpty"))).toBeInTheDocument();
    fireEvent.click(within(card).getByRole("button", { name: t("settings.provider.groups.goToDictionary") }));

    expect(await screen.findByLabelText(t("settings.provider.dictionary.filterAria", { label: "Mikan" }))).toBeInTheDocument();
  });

  it("名单里未知键只显示键，解析不到名字也不报错", async () => {
    stubSectionFetch({
      section: withMikanGroups(["58"]),
      // Looking up 58 in the dictionary: every returned entry's group_ref differs from 58, so no name resolves.
      search: () => [NANRAWS],
    });
    renderProvider("mikan");
    const card = await groupsCard();

    // Only the key node exists (no secondary name row).
    await waitFor(() => expect(within(card).getAllByTitle("58")).toHaveLength(1));
    expect(within(card).getByRole("button", { name: t("settings.provider.groups.remove", { name: "58" }) })).toBeInTheDocument();
    expect(screen.queryByText("NanRaws")).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("名单里未知键解析到名字后显示 名称 + 键", async () => {
    stubSectionFetch({ section: withMikanGroups(["58"]), search: () => [LOLIHOUSE] });
    renderProvider("mikan");
    const card = await groupsCard();

    // The primary label is the name; the secondary identifier is the saved key.
    expect(await within(card).findByText("SampleGroup")).toBeInTheDocument();
    expect(within(card).getByTitle("58")).toBeInTheDocument();
  });

  it("已在名单的字典项在结果里标记", async () => {
    stubSectionFetch({ section: withMikanGroups(["58"]), search: () => [LOLIHOUSE] });
    renderProvider("mikan");
    const card = await groupsCard();

    fireEvent.change(within(card).getByLabelText(t("settings.provider.groups.searchAria", { label: "Mikan" })), { target: { value: "SampleGroup" } });

    const result = await within(card).findByRole("button", { name: t("settings.provider.groups.resultAria", { name: "SampleGroup" }) });
    expect(result).toBeDisabled();
    expect(within(result).getByText(t("settings.provider.groups.inList"))).toBeInTheDocument();
  });

  it("移除名单项写草稿，保存后上送剩余名单", async () => {
    const fetchMock = stubSectionFetch({ section: withMikanGroups(["58", "99"]), search: (q) => (q === "58" ? [LOLIHOUSE] : q === "99" ? [NANRAWS] : []) });
    renderProvider("mikan");
    const card = await groupsCard();

    fireEvent.click(await within(card).findByRole("button", { name: t("settings.provider.groups.remove", { name: "SampleGroup" }) }));
    expect(await screen.findByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument();
    fireEvent.click(footerSave());
    await waitFor(() => expect(sectionPutCalls(fetchMock)).toHaveLength(1));
    expect(putBody(fetchMock).value.provider_defaults.mikan.groups).toEqual(["99"]);
  });
});

// The dictionary card (the pool): discover adds directly (formerly the lookup flow) and an empty
//   result is a normal outcome; only an online lookup that returns nothing is a retryable failure.
//   In the dictionary list user-added entries can be deleted (extra confirmation) while the
//   collected layer cannot, it follows version updates; each row accepts a pinned score (into the
//   page draft, clearing cancels).
describe("SettingsProviderDetail 字幕组字典卡片", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("摘要显示你添加的条数", async () => {
    stubSectionFetch({ user: [LOLIHOUSE, NANRAWS] });
    renderProvider("mikan");
    expect(await screen.findByText(t("settings.provider.dictionary.summary", { n: 2 }))).toBeInTheDocument();
  });

  it("发现按字幕组 id 写用户层（POST provider + query）并标记已加入字典", async () => {
    const fetchMock = stubSectionFetch({ user: [], retrieve: [LOLIHOUSE] });
    renderProvider("mikan");
    await openDiscoverDialog();

    fireEvent.change(screen.getByLabelText(t("settings.provider.dictionary.searchAria", { label: "Mikan" })), { target: { value: "58" } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.dictionary.search") }));

    const posts = await waitFor(() => {
      const found = callsMatching(fetchMock, (url, init) => url.includes("/config/subgroups/groups") && init?.method === "POST");
      expect(found).toHaveLength(1);
      return found;
    });
    expect(JSON.parse(String(posts[0]![1]?.body))).toEqual({ provider: "mikan", query: "58" });
    expect(await screen.findByText(t("settings.provider.dictionary.statusAdded"))).toBeInTheDocument();
  });

  it("发现未命中显示空状态而不是错误", async () => {
    stubSectionFetch({ retrieve: [] });
    renderProvider("mikan");
    await openDiscoverDialog();

    fireEvent.change(screen.getByLabelText(t("settings.provider.dictionary.searchAria", { label: "Mikan" })), { target: { value: "9999" } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.dictionary.search") }));

    expect(await screen.findByText(t("settings.provider.dictionary.searchEmpty"))).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("发现需要联网却拿不到结果时显示可重试的失败文案", async () => {
    stubSectionFetch({ retrieveStatus: 503, retrieveCode: "PROVIDER_GROUP_LOOKUP_FAILED" });
    renderProvider("mikan");
    await openDiscoverDialog();

    fireEvent.change(screen.getByLabelText(t("settings.provider.dictionary.searchAria", { label: "Mikan" })), { target: { value: "58" } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.dictionary.search") }));

    expect(await screen.findByText(t("error.PROVIDER_GROUP_LOOKUP_FAILED"))).toBeInTheDocument();
  });

  it("发现只接受字幕组键：输入名称得到明确的提示文案", async () => {
    stubSectionFetch({ retrieveStatus: 400, retrieveCode: "SUBGROUP_DISCOVER_KEY_REQUIRED" });
    renderProvider("mikan");
    await openDiscoverDialog();

    const input = screen.getByLabelText(t("settings.provider.dictionary.searchAria", { label: "Mikan" }));
    expect(input).toHaveAttribute("placeholder", t("settings.provider.dictionary.searchPlaceholderMikan"));
    fireEvent.change(input, { target: { value: "SampleGroup" } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.dictionary.search") }));

    expect(await screen.findByText(t("error.SUBGROUP_DISCOVER_KEY_REQUIRED"))).toBeInTheDocument();
  });

  it("删除用户条目前二次确认，确认后 DELETE 并刷新", async () => {
    const fetchMock = stubSectionFetch({ user: [LOLIHOUSE] });
    renderProvider("mikan");
    await openDictionaryDialog();

    expect(await screen.findByText("SampleGroup")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.dictionary.remove", { name: "SampleGroup" }) }));
    expect(await screen.findByText(t("settings.provider.dictionary.removeConfirmTitle"))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.dictionary.removeConfirm") }));

    const deletes = await waitFor(() => {
      const found = callsMatching(fetchMock, (url, init) => url.includes("/config/subgroups/groups/mikan/58") && init?.method === "DELETE");
      expect(found).toHaveLength(1);
      return found;
    });
    expect(String(deletes[0]![0])).toContain("/api/v1/config/subgroups/groups/mikan/58");
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.provider.dictionary.removed", { name: "SampleGroup" })));
  });

  it("删除已采集条目得到 409 的明确文案", async () => {
    stubSectionFetch({ user: [LOLIHOUSE], deleteStatus: 409, deleteCode: "GROUP_DICTIONARY_ENTRY_IMMUTABLE" });
    renderProvider("mikan");
    await openDictionaryDialog();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.provider.dictionary.remove", { name: "SampleGroup" }) }));
    fireEvent.click(await screen.findByRole("button", { name: t("settings.provider.dictionary.removeConfirm") }));

    expect(await screen.findByText(t("error.GROUP_DICTIONARY_ENTRY_IMMUTABLE"))).toBeInTheDocument();
  });

  it("删除不存在的条目得到 404 的明确文案", async () => {
    stubSectionFetch({ user: [LOLIHOUSE], deleteStatus: 404, deleteCode: "GROUP_DICTIONARY_ENTRY_NOT_FOUND" });
    renderProvider("mikan");
    await openDictionaryDialog();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.provider.dictionary.remove", { name: "SampleGroup" }) }));
    fireEvent.click(await screen.findByRole("button", { name: t("settings.provider.dictionary.removeConfirm") }));

    expect(await screen.findByText(t("error.GROUP_DICTIONARY_ENTRY_NOT_FOUND"))).toBeInTheDocument();
  });

  it("已采集层不可编辑：只有说明，没有删除入口", async () => {
    stubSectionFetch({ user: [] });
    renderProvider("mikan");
    await openDictionaryDialog();

    expect(screen.getByText(t("settings.provider.dictionary.collectedNote"))).toBeInTheDocument();
    expect(await screen.findByText(t("settings.provider.dictionary.listEmpty"))).toBeInTheDocument();
    expect(screen.queryAllByRole("listitem")).toHaveLength(0);
  });

  it("筛选框为空时列出整个字典：已采集条目可设固定分，只读本地、隐藏已失效", async () => {
    const collected = candidate({ group_ref: "370", display_name: "SampleSecondary", source: "collected" });
    const fetchMock = stubSectionFetch({ user: [LOLIHOUSE], browse: () => dictionaryPage([LOLIHOUSE, collected]) });
    renderProvider("mikan");
    await openDictionaryDialog();

    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText("SampleSecondary")).toBeInTheDocument();
    expect(within(dialog).getByText(t("settings.provider.dictionary.statusCollected"))).toBeInTheDocument();
    // An unpinned row has only the control, never a persistent input.
    expect(within(dialog).getByRole("button", { name: t("settings.provider.dictionary.pinSetAria", { name: "SampleSecondary" }) })).toBeInTheDocument();
    expect(within(dialog).queryByLabelText(t("settings.provider.dictionary.pinAria", { name: "SampleSecondary" }))).not.toBeInTheDocument();
    expect(within(dialog).getByText(t("settings.provider.dictionary.listTitle", { n: 2 }))).toBeInTheDocument();

    const browse = callsMatching(fetchMock, (url, init) => url.includes("/config/subgroups/groups?") && !url.includes("source=") && (init?.method ?? "GET") === "GET");
    expect(browse.length).toBeGreaterThan(0);
    const params = new URL(String(browse[0]![0]), "http://localhost").searchParams;
    expect(params.get("hide_retired")).toBe("true");
    expect(params.get("limit")).toBe("500");
    expect(params.has("q")).toBe(false);
    // Managing the dictionary is a purely local read: the discover network call is never made.
    expect(callsMatching(fetchMock, (url, init) => url.includes("/config/subgroups/groups") && init?.method === "POST")).toHaveLength(0);
  });

  it("「加载更多」按 next_offset 取下一页并追加到列表", async () => {
    const first = candidate({ group_ref: "1", display_name: "First Group", source: "collected" });
    const second = candidate({ group_ref: "2", display_name: "Second Group", source: "collected" });
    const fetchMock = stubSectionFetch({
      browse: (offset) => (offset === 0 ? dictionaryPage([first], 1, 2) : dictionaryPage([second], null, 2)),
    });
    renderProvider("mikan");
    await openDictionaryDialog();

    expect(await screen.findByText("First Group")).toBeInTheDocument();
    expect(screen.queryByText("Second Group")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.dictionary.loadMore") }));

    expect(await screen.findByText("Second Group")).toBeInTheDocument();
    expect(screen.getByText("First Group")).toBeInTheDocument();
    const offsets = callsMatching(fetchMock, (url) => url.includes("/config/subgroups/groups?") && url.includes("offset=1"));
    expect(offsets).toHaveLength(1);
    await waitFor(() => expect(screen.queryByRole("button", { name: t("settings.provider.dictionary.loadMore") })).not.toBeInTheDocument());
  });

  it("输入后按服务端 q 筛选，不隐藏已失效条目", async () => {
    const retired = candidate({ group_ref: "68", display_name: "Old Raws", source: "collected", available: false });
    const fetchMock = stubSectionFetch({ search: (q) => (q === "Old" ? [retired] : []) });
    renderProvider("mikan");
    await openDictionaryDialog();

    fireEvent.change(screen.getByLabelText(t("settings.provider.dictionary.filterAria", { label: "Mikan" })), { target: { value: "Old" } });
    expect(await screen.findByText("Old Raws")).toBeInTheDocument();
    expect(screen.getByText(t("settings.provider.dictionary.statusRetired"))).toBeInTheDocument();
    const filtered = callsMatching(fetchMock, (url) => url.includes("/config/subgroups/groups?") && url.includes("q=Old"));
    const params = new URL(String(filtered[0]![0]), "http://localhost").searchParams;
    expect(params.has("hide_retired")).toBe(false);
  });
});

// Pinned scores: an unpinned dictionary row shows only the "set pinned score" control, with the
//   popover's quick tiers or a custom score writing the page draft
//   (provider_defaults.<site>.pinned_scores) and the footer saving the section whole; a pinned row
//   row shows the compact control (the card's pinned subgroups form a badge row whose popover holds
//   the same editor), with unpinning available in the popover.
describe("SettingsProviderDetail 字典固定分", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  const withMikanPins = (pins: { group_ref: string; score: number }[]): GlobalSectionView => ({
    ...subgroupsSection,
    value: {
      ...(subgroupsSection.value as Record<string, unknown>),
      provider_defaults: {
        mikan: { mode: "blacklist", score: 50, groups: [], pinned_scores: pins },
        anibt: { mode: "blacklist", score: 50, groups: [] },
      },
    },
  });

  it("字典行从入口打开弹层填自定义分数，footer 保存随 PUT 上送", async () => {
    const fetchMock = stubSectionFetch({ user: [LOLIHOUSE] });
    renderProvider("mikan");
    await openDictionaryDialog();

    // Unpinned there is no persistent input, only the control.
    expect(screen.queryByLabelText(t("settings.provider.dictionary.pinAria", { name: "SampleGroup" }))).not.toBeInTheDocument();
    fireEvent.click(await screen.findByRole("button", { name: t("settings.provider.dictionary.pinSetAria", { name: "SampleGroup" }) }));
    const input = await screen.findByLabelText(t("settings.provider.dictionary.pinAria", { name: "SampleGroup" }));
    fireEvent.change(input, { target: { value: "88" } });
    expect(await screen.findByText(t("settings.provider.dictionary.pinEffectRanked", { score: 88 }))).toBeInTheDocument();
    expect(await screen.findByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument();
    expect(sectionPutCalls(fetchMock)).toHaveLength(0);

    // Clicking the control once more collapses the popover.
    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.dictionary.pinChipAria", { name: "SampleGroup", score: 88 }) }));
    await waitFor(() => expect(screen.queryByLabelText(t("settings.provider.dictionary.pinAria", { name: "SampleGroup" }))).not.toBeInTheDocument());
    fireEvent.click(screen.getAllByRole("button", { name: t("settings.edit.close") })[0]!);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    fireEvent.click(footerSave());
    await waitFor(() => expect(sectionPutCalls(fetchMock)).toHaveLength(1));
    expect(putBody(fetchMock).value.provider_defaults.mikan).toEqual({
      mode: "blacklist", score: 50, groups: [], pinned_scores: [{ group_ref: "58", score: 88 }],
    });
  });

  it("「总是选中」档写入 100，并说明不占订阅名额", async () => {
    const collected = candidate({ group_ref: "370", display_name: "SampleSecondary", source: "collected" });
    const fetchMock = stubSectionFetch({ user: [], browse: () => dictionaryPage([collected]) });
    renderProvider("mikan");
    await openDictionaryDialog();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.provider.dictionary.pinSetAria", { name: "SampleSecondary" }) }));
    expect(await screen.findByText(t("settings.provider.dictionary.pinPresetAlwaysHint"))).toBeInTheDocument();
    fireEvent.click(await screen.findByRole("button", { name: t("settings.provider.dictionary.pinPresetAlways") }));
    await waitFor(() =>
      expect(screen.queryByLabelText(t("settings.provider.dictionary.pinAria", { name: "SampleSecondary" }))).not.toBeInTheDocument(),
    );
    expect(await screen.findByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.dictionary.pinChipAria", { name: "SampleSecondary", score: 100 }) }));
    expect(await screen.findByText(t("settings.provider.dictionary.pinEffectAlways"))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.dictionary.pinClear") }));
    expect(await screen.findByRole("button", { name: t("settings.provider.dictionary.pinSetAria", { name: "SampleSecondary" }) })).toBeInTheDocument();

    // Set 100 once more and confirm the submitted value after saving.
    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.dictionary.pinSetAria", { name: "SampleSecondary" }) }));
    fireEvent.click(await screen.findByRole("button", { name: t("settings.provider.dictionary.pinPresetAlways") }));
    fireEvent.click(screen.getAllByRole("button", { name: t("settings.edit.close") })[0]!);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    fireEvent.click(footerSave());
    await waitFor(() => expect(sectionPutCalls(fetchMock)).toHaveLength(1));
    expect((putBody(fetchMock).value.provider_defaults.mikan as { pinned_scores?: unknown }).pinned_scores).toEqual([{ group_ref: "370", score: 100 }]);
  });

  it("「优先」档写入 80", async () => {
    const collected = candidate({ group_ref: "370", display_name: "SampleSecondary", source: "collected" });
    const fetchMock = stubSectionFetch({ user: [], browse: () => dictionaryPage([collected]) });
    renderProvider("mikan");
    await openDictionaryDialog();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.provider.dictionary.pinSetAria", { name: "SampleSecondary" }) }));
    fireEvent.click(await screen.findByRole("button", { name: t("settings.provider.dictionary.pinPresetPrefer") }));
    await waitFor(() =>
      expect(screen.queryByLabelText(t("settings.provider.dictionary.pinAria", { name: "SampleSecondary" }))).not.toBeInTheDocument(),
    );

    fireEvent.click(screen.getAllByRole("button", { name: t("settings.edit.close") })[0]!);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    fireEvent.click(footerSave());
    await waitFor(() => expect(sectionPutCalls(fetchMock)).toHaveLength(1));
    expect((putBody(fetchMock).value.provider_defaults.mikan as { pinned_scores?: unknown }).pinned_scores).toEqual([{ group_ref: "370", score: 80 }]);
  });

  it("筛选字典后可给已采集的字幕组设固定分", async () => {
    const collected = candidate({ group_ref: "370", display_name: "SampleSecondary", source: "collected" });
    const fetchMock = stubSectionFetch({ user: [], search: (q) => (q === "SampleSecondary" ? [collected] : []) });
    renderProvider("mikan");
    await openDictionaryDialog();

    fireEvent.change(screen.getByLabelText(t("settings.provider.dictionary.filterAria", { label: "Mikan" })), { target: { value: "SampleSecondary" } });
    // A collected entry has no delete control.
    expect(await screen.findByRole("button", { name: t("settings.provider.dictionary.pinSetAria", { name: "SampleSecondary" }) })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("settings.provider.dictionary.remove", { name: "SampleSecondary" }) })).not.toBeInTheDocument();
    fireEvent.click(await screen.findByRole("button", { name: t("settings.provider.dictionary.pinSetAria", { name: "SampleSecondary" }) }));
    fireEvent.change(await screen.findByLabelText(t("settings.provider.dictionary.pinAria", { name: "SampleSecondary" })), { target: { value: "60" } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.dictionary.pinChipAria", { name: "SampleSecondary", score: 60 }) }));
    await waitFor(() => expect(screen.queryByLabelText(t("settings.provider.dictionary.pinAria", { name: "SampleSecondary" }))).not.toBeInTheDocument());

    fireEvent.click(screen.getAllByRole("button", { name: t("settings.edit.close") })[0]!);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    fireEvent.click(footerSave());
    await waitFor(() => expect(sectionPutCalls(fetchMock)).toHaveLength(1));
    expect((putBody(fetchMock).value.provider_defaults.mikan as { pinned_scores?: unknown }).pinned_scores).toEqual([{ group_ref: "370", score: 60 }]);
  });

  it("弹层里清空自定义分数等同取消固定", async () => {
    const fetchMock = stubSectionFetch({ section: withMikanPins([{ group_ref: "58", score: 88 }]), user: [LOLIHOUSE] });
    renderProvider("mikan");
    await openDictionaryDialog();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.provider.dictionary.pinChipAria", { name: "SampleGroup", score: 88 }) }));
    const input = await screen.findByLabelText(t("settings.provider.dictionary.pinAria", { name: "SampleGroup" }));
    expect(input).toHaveValue("88");
    fireEvent.change(input, { target: { value: "" } });
    expect(await screen.findByText(t("settings.provider.dictionary.pinEffectUnset"))).toBeInTheDocument();

    fireEvent.click(screen.getAllByRole("button", { name: t("settings.edit.close") })[0]!);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    fireEvent.click(footerSave());
    await waitFor(() => expect(sectionPutCalls(fetchMock)).toHaveLength(1));
    expect(putBody(fetchMock).value.provider_defaults.mikan).toEqual({ mode: "blacklist", score: 50, groups: [] });
  });

  it("非法固定分就地提示且不写入草稿", async () => {
    stubSectionFetch({ user: [LOLIHOUSE] });
    renderProvider("mikan");
    await openDictionaryDialog();

    fireEvent.click(await screen.findByRole("button", { name: t("settings.provider.dictionary.pinSetAria", { name: "SampleGroup" }) }));
    const input = await screen.findByLabelText(t("settings.provider.dictionary.pinAria", { name: "SampleGroup" }));
    fireEvent.change(input, { target: { value: "101" } });
    expect(await screen.findByText(t("settings.provider.dictionary.pinInvalid"))).toBeInTheDocument();
    expect(input).toHaveAttribute("aria-invalid", "true");

    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.dictionary.pinSetAria", { name: "SampleGroup" }) }));
    fireEvent.click(screen.getAllByRole("button", { name: t("settings.edit.close") })[0]!);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(footerSave()).toBeDisabled();
  });

  it("卡片把已固定分的字幕组排成徽章行；弹层里取消固定并随保存上送", async () => {
    const fetchMock = stubSectionFetch({ section: withMikanPins([{ group_ref: "58", score: 90 }, { group_ref: "99", score: 30 }]), user: [LOLIHOUSE, NANRAWS] });
    renderProvider("mikan");
    await waitForSettings();

    const pinned = await screen.findByRole("list", { name: t("settings.provider.dictionary.pinnedTitle") });
    expect(await within(pinned).findByText("SampleGroup")).toBeInTheDocument();
    fireEvent.click(within(pinned).getByRole("button", { name: t("settings.provider.dictionary.pinChipAria", { name: "SampleGroup", score: 90 }) }));
    expect(await screen.findByText(t("settings.provider.dictionary.pinEffectRanked", { score: 90 }))).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.dictionary.pinClear") }));
    await waitFor(() => expect(within(pinned).queryByText("SampleGroup")).not.toBeInTheDocument());

    fireEvent.click(footerSave());
    await waitFor(() => expect(sectionPutCalls(fetchMock)).toHaveLength(1));
    expect((putBody(fetchMock).value.provider_defaults.mikan as { pinned_scores?: unknown }).pinned_scores).toEqual([{ group_ref: "99", score: 30 }]);
  });

  it("取消最后一个固定分后上送的值不带 pinned_scores", async () => {
    const fetchMock = stubSectionFetch({ section: withMikanPins([{ group_ref: "58", score: 90 }]), user: [LOLIHOUSE] });
    renderProvider("mikan");
    await waitForSettings();

    const pinned = await screen.findByRole("list", { name: t("settings.provider.dictionary.pinnedTitle") });
    fireEvent.click(within(pinned).getByRole("button", { name: t("settings.provider.dictionary.pinChipAria", { name: "SampleGroup", score: 90 }) }));
    fireEvent.click(await screen.findByRole("button", { name: t("settings.provider.dictionary.pinClear") }));
    expect(await screen.findByText(t("settings.provider.dictionary.pinnedEmpty"))).toBeInTheDocument();

    fireEvent.click(footerSave());
    await waitFor(() => expect(sectionPutCalls(fetchMock)).toHaveLength(1));
    expect(putBody(fetchMock).value.provider_defaults.mikan).toEqual({ mode: "blacklist", score: 50, groups: [] });
  });

  it("「发现」只写字典，不改页面草稿也不保存 subgroups", async () => {
    const fetchMock = stubSectionFetch({ user: [], retrieve: [LOLIHOUSE] });
    renderProvider("mikan");
    await openDiscoverDialog();

    fireEvent.change(screen.getByLabelText(t("settings.provider.dictionary.searchAria", { label: "Mikan" })), { target: { value: "58" } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.dictionary.search") }));
    expect(await screen.findByText(t("settings.provider.dictionary.statusAdded"))).toBeInTheDocument();
    expect(sectionPutCalls(fetchMock)).toHaveLength(0);
    fireEvent.click(screen.getAllByRole("button", { name: t("settings.edit.close") })[0]!);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(footerSave()).toBeDisabled();
  });
});

// The single-provider page has no left provider-group nav. A 409 save goes through the shared
//   ConflictBlock (with refresh-and-load-latest, refetching global-sections and dropping the stale
//   draft).
describe("SettingsProviderDetail 单 provider 页 + 409 冲突块", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("单 provider 页无左侧 provider 分组导航", async () => {
    stubSectionFetch({ search: () => [LOLIHOUSE] });
    renderProvider("mikan");
    await groupsCard();

    expect(screen.queryByRole("navigation")).not.toBeInTheDocument();
  });

  it("保存 409 显示 ConflictBlock 并报出失败的部分，刷新重拉 global-sections 并丢弃旧草稿", async () => {
    const fetchMock = stubSectionFetch({ putStatus: 409, search: () => [LOLIHOUSE] });
    renderProvider("mikan");
    const card = await groupsCard();

    fireEvent.change(within(card).getByLabelText(t("settings.provider.groups.searchAria", { label: "Mikan" })), { target: { value: "SampleGroup" } });
    fireEvent.click(await within(card).findByRole("button", { name: t("settings.provider.groups.resultAria", { name: "SampleGroup" }) }));

    fireEvent.click(footerSave());
    expect(await screen.findByText(t("common.versionConflict"))).toBeInTheDocument();
    expect(screen.getByText(t("act.partFailed", { part: t("settings.provider.title", { label: "Mikan" }) }))).toBeInTheDocument();
    const { toast } = await import("sonner");
    expect(toast.success).not.toHaveBeenCalled();

    const sectionGets = () =>
      fetchMock.mock.calls.filter(
        ([input, init]) => String(input).includes("/config/global-sections") && (!init?.method || init.method === "GET"),
      ).length;
    const getsBefore = sectionGets();
    fireEvent.click(screen.getByRole("button", { name: t("common.refreshLatest") }));

    await waitFor(() => expect(sectionGets()).toBeGreaterThan(getsBefore));
    await waitFor(() => expect(screen.queryByText(t("common.versionConflict"))).not.toBeInTheDocument());
    // After the refresh the server's latest values load and the local draft is dropped.
    await waitFor(() => expect(within(card).getByText(t("settings.provider.groups.listEmpty"))).toBeInTheDocument());
    expect(within(card).queryByRole("button", { name: t("settings.provider.groups.remove", { name: "SampleGroup" }) })).not.toBeInTheDocument();
    await waitFor(() => expect(footerSave()).toBeDisabled());
  });
});

// Name scoring (name_keywords): edits keywords plus adjustments inline; on save the whole section
//   goes up with subgroups, and max_rss / entries / the other provider's fields pass through
//   unwiped. The scope note follows the roster mode.
describe("SettingsProviderDetail 名称评分", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("Mikan 添加规则保存随 PUT 上送，AniBT 规则与 max_rss 透传", async () => {
    const fetchMock = stubSectionFetch();
    renderProvider("mikan");
    await waitForSettings();

    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.rules.addAria", { label: "Mikan" }) }));
    fireEvent.change(screen.getByLabelText(t("settings.provider.rules.keywordAria", { label: "Mikan", index: 1 })), { target: { value: "v2" } });
    fireEvent.change(screen.getByLabelText(t("settings.provider.rules.scoreAria", { label: "Mikan", index: 1 })), { target: { value: "-30" } });

    fireEvent.click(footerSave());
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.provider.saved", { label: "Mikan" })));

    const body = putBody(fetchMock);
    expect(body.value.name_keywords.mikan).toEqual([{ keyword: "v2", score: -30 }]);
    // The AniBT rules pass through as-is (not edited on this page, but a save must never wipe them).
    expect(body.value.name_keywords.anibt).toEqual([{ keyword: "简繁", score: 10 }]);
    // The subscription policy page's fields pass through as-is, never wiped.
    expect(body.value.max_rss).toBe(42);
    expect(body.value.entries).toEqual([]);
  });

  it("名称评分的长说明默认收起，点“?”后显示", async () => {
    stubSectionFetch();
    renderProvider("mikan");

    const help = await screen.findByRole("button", { name: t("settings.provider.rules.helpLabel") });
    expect(screen.queryByText(t("settings.provider.rules.help"))).not.toBeInTheDocument();
    fireEvent.click(help);
    expect(await screen.findByText(t("settings.provider.rules.help"))).toBeInTheDocument();
  });

  it("名称评分的作用域说明随名单模式变化", async () => {
    stubSectionFetch({
      section: {
        ...subgroupsSection,
        value: {
          ...(subgroupsSection.value as Record<string, unknown>),
          provider_defaults: { mikan: { mode: "whitelist", score: 50, groups: [] }, anibt: { mode: "blacklist", score: 50, groups: [] } },
        },
      },
    });
    renderProvider("mikan");
    await waitForSettings();

    // The scope note renders after the subgroups section arrives, which can trail the header.
    expect(await screen.findByText(t("settings.provider.rules.scopeWhitelist"))).toBeInTheDocument();
    expect(screen.queryByText(t("settings.provider.rules.scopeBlacklist"))).not.toBeInTheDocument();
  });

  it("AniBT 页回填已有规则并可编辑", async () => {
    const fetchMock = stubSectionFetch();
    renderProvider("anibt");
    await waitForSettings("AniBT");

    const keyword = screen.getByLabelText(t("settings.provider.rules.keywordAria", { label: "AniBT", index: 1 }));
    expect(keyword).toHaveValue("简繁");
    expect(screen.getByLabelText(t("settings.provider.rules.scoreAria", { label: "AniBT", index: 1 }))).toHaveValue(10);

    fireEvent.change(screen.getByLabelText(t("settings.provider.rules.scoreAria", { label: "AniBT", index: 1 })), { target: { value: "20" } });
    fireEvent.click(footerSave());
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.provider.saved", { label: "AniBT" })));

    expect(putBody(fetchMock).value.name_keywords.anibt).toEqual([{ keyword: "简繁", score: 20 }]);
  });

  it("非法行内联标错：空关键词 / 越界分数 / 大小写重复", async () => {
    stubSectionFetch();
    renderProvider("mikan");
    await waitForSettings();

    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.rules.addAria", { label: "Mikan" }) }));
    expect(screen.getByText(t("settings.provider.rules.keywordEmpty"))).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText(t("settings.provider.rules.keywordAria", { label: "Mikan", index: 1 })), { target: { value: "ABC" } });
    fireEvent.change(screen.getByLabelText(t("settings.provider.rules.scoreAria", { label: "Mikan", index: 1 })), { target: { value: "101" } });
    expect(screen.getByText(t("settings.site.scoreRange"))).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText(t("settings.provider.rules.scoreAria", { label: "Mikan", index: 1 })), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.rules.addAria", { label: "Mikan" }) }));
    fireEvent.change(screen.getByLabelText(t("settings.provider.rules.keywordAria", { label: "Mikan", index: 2 })), { target: { value: "abc" } });
    expect(screen.getByText(t("settings.provider.rules.keywordDuplicate"))).toBeInTheDocument();

    // The error disappears after deleting the conflicting row.
    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.rules.removeAria", { label: "Mikan", index: 2 }) }));
    expect(screen.queryByText(t("settings.provider.rules.keywordDuplicate"))).not.toBeInTheDocument();
  });
});

// The catalog auto-refresh preference: a single-provider row whose toggles are a draft (SwitchRow),
//   committed by the footer save after the subgroups section (PUT
//   /catalog/providers/:provider/seasons/:season/preference, If-Match CAS); the interval is set in
//   automation.
describe("SettingsProviderDetail 目录自动刷新偏好", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  const refreshSwitch = () => screen.findByRole("switch", { name: t("settings.provider.refresh.toggleAria", { label: "Mikan" }) });
  const preferencePuts = (fetchMock: ReturnType<typeof vi.fn<FetchStub>>) =>
    callsMatching(fetchMock, (url, init) => url.includes("/preference") && init?.method === "PUT");

  it("开关只改草稿，footer 保存才 PUT 并 toast", async () => {
    // Pin the fixtures' summer dates; replace Date only, never freezing React Query/DOM timers.
    vi.setSystemTime(new Date("2026-08-02T00:00:00Z"));
    const fetchMock = stubSectionFetch();
    renderProvider("mikan");

    const toggle = await refreshSwitch();
    expect(toggle).toHaveAttribute("aria-checked", "true");
    // The interval is decided only by automation: no interval input here, just a link to it.
    expect(screen.getByRole("link", { name: t("settings.provider.refresh.scheduleLink") })).toHaveAttribute("href", "/settings/automation");

    fireEvent.click(toggle);
    await waitFor(async () => expect(await refreshSwitch()).toHaveAttribute("aria-checked", "false"));
    expect(await screen.findByText(t("act.unsavedCount", { n: 1 }))).toBeInTheDocument();
    expect(preferencePuts(fetchMock)).toHaveLength(0);

    fireEvent.click(footerSave());
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.provider.refresh.saved", { label: "Mikan" })));

    const puts = preferencePuts(fetchMock);
    expect(puts).toHaveLength(1);
    expect(String(puts[0]![0])).toContain("/catalog/providers/mikan/seasons/2026-summer/preference");
    expect(JSON.parse(String(puts[0]![1]?.body))).toEqual({
      season: "2026-summer",
      refresh_enabled: false,
      schedule: null,
    });
    expect(new Headers(puts[0]![1]?.headers).get("If-Match")).toBe("2");
    expect(sectionPutCalls(fetchMock)).toHaveLength(0);
  });

  it("名单与刷新都改时先 PUT subgroups 段再 PUT 刷新偏好，提示取第一条", async () => {
    vi.setSystemTime(new Date("2026-08-02T00:00:00Z"));
    const fetchMock = stubSectionFetch();
    renderProvider("mikan");
    await waitForSettings();

    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.rules.addAria", { label: "Mikan" }) }));
    fireEvent.change(screen.getByLabelText(t("settings.provider.rules.keywordAria", { label: "Mikan", index: 1 })), { target: { value: "v2" } });
    fireEvent.click(await refreshSwitch());
    expect(await screen.findByText(t("act.unsavedCount", { n: 2 }))).toBeInTheDocument();

    fireEvent.click(footerSave());
    await waitFor(() => expect(preferencePuts(fetchMock)).toHaveLength(1));
    const order = fetchMock.mock.calls
      .filter(([, init]) => init?.method === "PUT")
      .map(([input]) => (String(input).includes("/preference") ? "preference" : "section"));
    expect(order).toEqual(["section", "preference"]);
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.provider.saved", { label: "Mikan" })));
    expect(toast.success).toHaveBeenCalledTimes(1);
  });

  it("subgroups 段保存失败时停在第一步，刷新偏好保持未保存", async () => {
    vi.setSystemTime(new Date("2026-08-02T00:00:00Z"));
    const fetchMock = stubSectionFetch({ putStatus: 409 });
    renderProvider("mikan");
    await waitForSettings();

    fireEvent.click(screen.getByRole("button", { name: t("settings.provider.rules.addAria", { label: "Mikan" }) }));
    fireEvent.change(screen.getByLabelText(t("settings.provider.rules.keywordAria", { label: "Mikan", index: 1 })), { target: { value: "v2" } });
    fireEvent.click(await refreshSwitch());
    expect(await screen.findByText(t("act.unsavedCount", { n: 2 }))).toBeInTheDocument();

    fireEvent.click(footerSave());
    expect(await screen.findByText(t("common.versionConflict"))).toBeInTheDocument();
    expect(sectionPutCalls(fetchMock)).toHaveLength(1);
    expect(preferencePuts(fetchMock)).toHaveLength(0);
    expect(await refreshSwitch()).toHaveAttribute("aria-checked", "false");
  });
});

// The hidden-anime card: statistics filter to the current provider; manage switches to settings' hidden-anime section.
describe("SettingsProviderDetail 已隐藏的番剧按 provider 过滤统计", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("只统计当前 provider 的条目数", async () => {
    const fetchMock = vi.fn<FetchStub>(async (input) => {
      const url = String(input);
      let body: unknown = [];
      if (url.includes("/catalog/suppressions")) {
        body = [
          { provider: "mikan", provider_entry_ref: "1", title_snapshot: "Mikan A", suppressed_at: "2026-08-02T00:00:00Z" },
          { provider: "mikan", provider_entry_ref: "2", title_snapshot: "Mikan B", suppressed_at: "2026-08-02T00:00:00Z" },
          { provider: "anibt", provider_entry_ref: "3", title_snapshot: "AniBT C", suppressed_at: "2026-08-02T00:00:00Z" },
        ];
      } else if (url.includes("/config/subgroups/groups?")) {
        body = { candidates: [] };
      } else if (url.includes("/config/global-sections")) {
        body = [subgroupsSection];
      } else if (url.includes("/preference")) {
        body = { season: "2026-summer", refresh_enabled: true, schedule: null, version: 2, updated_at: "2026-08-02T00:00:00Z" };
      }
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    });
    vi.stubGlobal("fetch", fetchMock);

    renderProvider("mikan");
    // The Mikan page counts 2 entries (not AniBT's 1).
    expect(await screen.findByText(t("settings.provider.blacklist.count", { label: "Mikan", n: 2 }))).toBeInTheDocument();

    // Switching to the AniBT page counts 1.
    const client2 = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <MemoryRouter>
        <QueryClientProvider client={client2}>
          <SettingsProviderDetail provider="anibt" />
        </QueryClientProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByText(t("settings.provider.blacklist.count", { label: "AniBT", n: 1 }))).toBeInTheDocument();
  });
});
