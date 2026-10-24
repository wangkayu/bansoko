import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { tagFullLabel } from "../../lib/anibtTags";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { GlobalSectionView, NamingTemplateLibraryView, ParserCatalogEntry, ParserPolicyDefaultView } from "../../lib/api/client";
import { SettingsRules } from "./SettingsRules";
import { t } from "../../lib/i18n";

// Session 4: the two filter cards hold a single must_contain/not_in regex each (subtitle_rules
//   retired); the episode-regex textarea becomes a TagInput with the regex validation hook; badges
//   unify on built-in default / custom default.

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

type FetchStub = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

const fileRulesSection: GlobalSectionView = {
  section_name: "file_rules",
  schema_version: "go-re2-v1",
  value: { must_contain: "", not_in: "" },
  version: 1,
  saved: false,
};

const parserSection: GlobalSectionView = {
  section_name: "parser",
  schema_version: "parser-section-v1",
  value: { episode_patterns: ["第 (\\d+) 话"] },
  version: 3,
  saved: true,
};

const namingLibrary: NamingTemplateLibraryView = {
  default_key: "default",
  version: 1,
  templates: [{
    key: "default",
    name: "Default",
    rules: {
      language: "js-naming-1",
      unknown: { directory_template: "u-dir", video_filename_template: "u-file" },
      tv: { directory_template: "tv-dir", video_filename_template: "tv-file" },
      movie: { directory_template: "movie-dir", video_filename_template: "movie-file" },
      subtitle_suffix_template: "",
    },
  }],
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

// The default episode parser card's two endpoints: the global creation template plus the parser catalog.
const seededParserDefault: ParserPolicyDefaultView = {
  parser_kind: "regex",
  version: 1,
  updated_at: "2026-10-17T00:00:00Z",
};

const regexCatalogEntry: ParserCatalogEntry = {
  id: "regex",
  display_name: "Regex rules",
  capabilities: ["release_title"],
  implementation_version: "parser-v1",
  output_contract_version: "episode-evidence-v1",
  config_version: "parser-config-v1",
};

const reltitleCatalogEntry: ParserCatalogEntry = {
  ...regexCatalogEntry,
  id: "reltitle",
  display_name: "reltitle episode parser",
};

type ParserDefaultStub = {
  parserDefault?: ParserPolicyDefaultView;
  catalog?: ParserCatalogEntry[];
  parserDefaultSave?: (body: { expected_version: number; parser_kind: string }) => Response;
};

function stubFetch(sections: GlobalSectionView[], stubs: ParserDefaultStub = {}) {
  const parserDefault = stubs.parserDefault ?? seededParserDefault;
  const catalog = stubs.catalog ?? [regexCatalogEntry, reltitleCatalogEntry];
  const fetchMock = vi.fn<FetchStub>(async (input, init) => {
    const url = String(input);
    if (url.includes("/file-maintenance/parser-policy/default")) {
      const body = JSON.parse(String(init?.body ?? "{}")) as { expected_version: number; parser_kind: string };
      if (init?.method === "PUT") {
        if (stubs.parserDefaultSave) return stubs.parserDefaultSave(body);
        return jsonResponse({ ...body, version: body.expected_version + 1, updated_at: "2026-10-17T01:00:00Z" });
      }
      return jsonResponse(parserDefault);
    }
    if (url.includes("/parser-catalog")) return jsonResponse(catalog);
    if (url.includes("/config/naming-templates")) return jsonResponse(namingLibrary);
    if (init?.method === "PUT" && url.includes("/config/global-sections/")) {
      const requestBody = JSON.parse(String(init.body)) as { schema_version: string; value: unknown };
      const name = url.slice(url.lastIndexOf("/") + 1) as GlobalSectionView["section_name"];
      const saved: GlobalSectionView = {
        section_name: name,
        schema_version: requestBody.schema_version,
        value: requestBody.value as Record<string, unknown>,
        version: 9,
        saved: true,
      };
      return jsonResponse(saved);
    }
    const body: unknown = url.includes("/config/global-sections") ? sections : [];
    return jsonResponse(body);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderRules() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SettingsRules />
    </QueryClientProvider>,
  );
}

// Each card carries its own edit; queries pin to the card's title to avoid mixing with the default episode parser card's edit.
async function editButtonFor(title: string) {
  const card = (await screen.findByText(title)).closest("li")!;
  return within(card).findByRole("button", { name: t("settings.rules.edit") });
}

describe("SettingsRules 批次 4", () => {
  it("命名规则卡显示模板库的默认模板", async () => {
    stubFetch([]);
    renderRules();
    expect(await screen.findByText(t("settings.rules.namingRules"))).toBeInTheDocument();
    expect(await screen.findAllByText("Default")).not.toHaveLength(0);
    expect(screen.getByText("tv-dir")).toBeInTheDocument();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("文件筛选卡：版本 1 显示内置默认徽章，编辑保存 must_contain/not_in 正则", async () => {
    const fetchMock = stubFetch([fileRulesSection]);
    renderRules();

    expect(await screen.findByText(t("settings.rules.fileRules"))).toBeInTheDocument();
    expect(screen.getByText(t("settings.badge.builtin"))).toBeInTheDocument();
    expect(screen.queryByText("字幕规则")).not.toBeInTheDocument();

    fireEvent.click(await editButtonFor(t("settings.rules.fileRules")));
    expect(screen.getByText(t("settings.rules.editTitle", { title: t("settings.rules.fileRules") }))).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText(t("settings.rules.fileNot")), { target: { value: "(?i)sample" } });
    fireEvent.click(screen.getByRole("button", { name: t("settings.rules.save") }));
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.rules.saved", { title: t("settings.rules.fileRules") })));

    const puts = fetchMock.mock.calls.filter(
      ([input, init]) => String(input).includes("/config/global-sections/file_rules") && init?.method === "PUT",
    );
    expect(puts).toHaveLength(1);
    const [, init] = puts[0]!;
    expect((init?.headers as Headers).get("If-Match")).toBe("1");
    expect(JSON.parse(String(init?.body))).toEqual({
      schema_version: "go-re2-v1",
      value: { must_contain: "", not_in: "(?i)sample" },
    });
  });

  it("AniBT 标签默认值：摘要显示当前规则，编辑后保存 AniBT 行并保留标题解析行", async () => {
    const fetchMock = stubFetch([{
      section_name: "tag_filters",
      schema_version: "tag-filters-v2",
      value: { anibt: { require: [], exclude: [] }, title: { require: [], exclude: ["sub_lang:TC"] } },
      version: 1,
      saved: false,
    }]);
    renderRules();

    expect(await screen.findByText(t("anibtTags.title"))).toBeInTheDocument();
    expect(screen.getByText(t("anibtTags.none"))).toBeInTheDocument();
    fireEvent.click(await editButtonFor(t("tagFilters.title")));
    const anibtRow = screen.getByRole("region", { name: t("anibtTags.title") });
    fireEvent.click(within(anibtRow).getByRole("button", { name: "CHS" }));
    // An added tag leaves the candidates; typing a duplicate by hand shows a hint.
    expect(within(anibtRow).queryByRole("button", { name: "CHS" })).not.toBeInTheDocument();
    fireEvent.change(within(anibtRow).getByLabelText(t("anibtTags.addValue")), { target: { value: "DV" } });
    fireEvent.click(within(anibtRow).getByRole("button", { name: t("anibtTags.add") }));
    fireEvent.change(within(anibtRow).getByLabelText(t("anibtTags.addValue")), { target: { value: "dv" } });
    fireEvent.click(within(anibtRow).getByRole("button", { name: t("anibtTags.add") }));
    expect(within(anibtRow).getByRole("alert")).toHaveTextContent(t("anibtTags.duplicate"));
    // Each added tag can be switched to exclude or removed; a removed tag returns to the candidates.
    fireEvent.click(within(anibtRow).getByRole("button", { name: t("anibtTags.switchTo.exclude", { tag: tagFullLabel("tag:DV") }) }));
    fireEvent.click(within(anibtRow).getByRole("button", { name: t("anibtTags.removeAria", { tag: tagFullLabel("language:CHS") }) }));
    expect(within(anibtRow).getByRole("button", { name: "CHS" })).toBeInTheDocument();
    fireEvent.click(within(anibtRow).getByRole("button", { name: "CHT" }));
    expect(within(anibtRow).getAllByTestId("tag-row-entry").map((entry) => entry.dataset.role)).toEqual(["require", "exclude"]);
    fireEvent.click(screen.getByRole("button", { name: t("settings.rules.save") }));

    await waitFor(() => expect(fetchMock.mock.calls.some(([input, init]) => String(input).includes("/config/global-sections/tag_filters") && init?.method === "PUT")).toBe(true));
    const [, init] = fetchMock.mock.calls.find(([input, init]) => String(input).includes("/config/global-sections/tag_filters") && init?.method === "PUT")!;
    expect(JSON.parse(String(init?.body))).toEqual({
      schema_version: "tag-filters-v2",
      value: {
        anibt: { require: ["language:CHT"], exclude: ["tag:DV"] },
        title: { require: [], exclude: ["sub_lang:TC"] },
      },
    });
  });

  it("下载完成后不在默认规则里（只在下载与入库编辑）", async () => {
    stubFetch([fileRulesSection, {
      section_name: "download_completion",
      schema_version: "download-completion-v2",
      value: { action: "pause" },
      version: 1,
      saved: true,
    }]);
    renderRules();

    expect(await screen.findByText(t("settings.rules.fileRules"))).toBeInTheDocument();
    expect(screen.queryByText(t("downloadCompletion.title"))).toBeNull();
  });

  it("解析规则：集数正则为 tag input，非法正则逐条拦截", async () => {
    stubFetch([parserSection]);
    renderRules();

    await screen.findByText(t("settings.rules.parser"));
    fireEvent.click(await editButtonFor(t("settings.rules.parser")));
    // Existing values render as badges (the card summary repeats the same copy after the dialog, so anchor on the chip's delete button).
    expect(screen.getByLabelText("删除 第 (\\d+) 话")).toBeInTheDocument();

    const input = screen.getByLabelText(t("settings.rules.episodePatterns"));
    fireEvent.change(input, { target: { value: "[" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(screen.getByRole("alert")).toHaveTextContent(t("settings.rules.invalidRegex"));
    // The illegal entry never became a badge.
    expect(screen.queryByLabelText("删除 [")).not.toBeInTheDocument();

    fireEvent.change(input, { target: { value: "EP(\\d+)" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByLabelText("删除 EP(\\d+)")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: t("settings.rules.save") }));
    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.rules.saved", { title: t("settings.rules.parser") })));
  });
});

// The default episode parser (the global creation template) merges into the episode-rules card:
//   parser and recognition rules are edited together and saved once (parser first, then rules), no
//   longer a neighboring second card.
describe("SettingsRules 解析规则卡：解析器与集数识别规则", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  const kindLabel = () => t("settings.rules.defaultParser.kindLabel");
  const parserTitle = () => t("settings.rules.parser");

  async function openEditor() {
    fireEvent.click(await editButtonFor(parserTitle()));
    const dialog = await screen.findByRole("dialog", { name: t("settings.rules.editTitle", { title: parserTitle() }) });
    await within(dialog).findByRole("radiogroup", { name: kindLabel() });
    return dialog;
  }

  const parserDefaultPuts = (fetchMock: ReturnType<typeof stubFetch>) =>
    fetchMock.mock.calls.filter(([input, init]) => String(input).includes("/file-maintenance/parser-policy/default") && init?.method === "PUT");
  const parserSectionPuts = (fetchMock: ReturnType<typeof stubFetch>) =>
    fetchMock.mock.calls.filter(([input, init]) => String(input).includes("/config/global-sections/parser") && init?.method === "PUT");

  it("只有一张解析规则卡：摘要先写解析器再写识别规则；编辑弹窗里正则 / reltitle 二选一，各带一行说明", async () => {
    stubFetch([parserSection, fileRulesSection]);
    renderRules();

    const card = (await screen.findByText(parserTitle())).closest("li")!;
    expect(await within(card).findByText(t("policy.parser.kindRegex"))).toBeInTheDocument();
    expect(within(card).getByText("第 (\\d+) 话")).toBeInTheDocument();
    expect(within(card).queryByRole("radiogroup")).not.toBeInTheDocument();
    // Episode rules and file filters, plus the naming templates card that is always present.
    expect(screen.getAllByRole("button", { name: t("settings.rules.edit") })).toHaveLength(3);

    const dialog = await openEditor();
    expect(within(dialog).getByRole("radio", { name: t("policy.parser.kindRegex") })).toHaveAttribute("aria-checked", "true");
    expect(within(dialog).getByRole("radio", { name: t("policy.parser.kindReltitle") })).toHaveAccessibleDescription(
      t("policy.parser.kindReltitleDescription"),
    );
    expect(within(dialog).getByLabelText(t("settings.rules.episodePatterns"))).toBeInTheDocument();
  });

  it("选 reltitle：识别规则隐藏；保存只按当前版本写默认解析器，识别规则原样不写", async () => {
    const fetchMock = stubFetch([parserSection]);
    renderRules();

    const dialog = await openEditor();
    await userEvent.click(within(dialog).getByRole("radio", { name: t("policy.parser.kindReltitle") }));
    expect(within(dialog).queryByLabelText(t("settings.rules.episodePatterns"))).not.toBeInTheDocument();
    expect(within(dialog).getByText(t("forms.rules.episodePatternsUnused"))).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: t("settings.rules.save") }));

    const { toast } = await import("sonner");
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(t("settings.rules.saved", { title: parserTitle() })));
    const puts = parserDefaultPuts(fetchMock);
    expect(puts).toHaveLength(1);
    expect(JSON.parse(String(puts[0]![1]?.body))).toEqual({ expected_version: 1, parser_kind: "reltitle" });
    expect(parserSectionPuts(fetchMock)).toHaveLength(0);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    const card = screen.getByText(parserTitle()).closest("li")!;
    expect(within(card).getByText(t("policy.parser.kindReltitle"))).toBeInTheDocument();
  });

  it("只改识别规则：不写默认解析器", async () => {
    const fetchMock = stubFetch([parserSection]);
    renderRules();

    const dialog = await openEditor();
    const input = within(dialog).getByLabelText(t("settings.rules.episodePatterns"));
    fireEvent.change(input, { target: { value: "EP(\\d+)" } });
    fireEvent.keyDown(input, { key: "Enter" });
    fireEvent.click(within(dialog).getByRole("button", { name: t("settings.rules.save") }));

    await waitFor(() => expect(parserSectionPuts(fetchMock)).toHaveLength(1));
    expect(parserDefaultPuts(fetchMock)).toHaveLength(0);
  });

  it("冲突：409 提示刷新并重新拉取最新默认解析器，不静默覆盖，识别规则不写", async () => {
    const fetchMock = stubFetch([parserSection], {
      parserDefaultSave: () =>
        jsonResponse(
          { code: "OWNER_VERSION_CONFLICT", current_version: 4, message: "conflict" },
          409,
        ),
    });
    renderRules();

    const dialog = await openEditor();
    await userEvent.click(within(dialog).getByRole("radio", { name: t("policy.parser.kindReltitle") }));
    fireEvent.click(within(dialog).getByRole("button", { name: t("settings.rules.save") }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent(t("common.versionConflict"));
    expect(parserSectionPuts(fetchMock)).toHaveLength(0);
    const reads = () =>
      fetchMock.mock.calls.filter(
        ([input, init]) => String(input).includes("/file-maintenance/parser-policy/default") && init?.method !== "PUT",
      ).length;
    const before = reads();
    await userEvent.click(within(dialog).getByRole("button", { name: t("common.refreshLatest") }));
    await waitFor(() => expect(reads()).toBeGreaterThan(before));
  });

  it("选项过滤：只列出具备 release_title 能力的解析器", async () => {
    stubFetch([parserSection], {
      catalog: [
        regexCatalogEntry,
        reltitleCatalogEntry,
        { ...regexCatalogEntry, id: "file-only", display_name: "File name only", capabilities: ["file_name"] },
      ],
    });
    renderRules();

    const dialog = await openEditor();
    expect(within(dialog).getAllByRole("radio")).toHaveLength(2);
    expect(screen.queryByText("File name only")).not.toBeInTheDocument();
  });

  it("当前值不在目录中时仍显示原值并选中，不静默换成别的解析器", async () => {
    stubFetch([parserSection], {
      parserDefault: { parser_kind: "legacy-parser", version: 7, updated_at: "2026-10-17T00:00:00Z" },
    });
    renderRules();

    const card = (await screen.findByText(parserTitle())).closest("li")!;
    expect(await within(card).findByText("legacy-parser")).toBeInTheDocument();
    const dialog = await openEditor();
    expect(within(dialog).getByRole("radio", { name: "legacy-parser" })).toHaveAttribute("aria-checked", "true");
  });
});

// The regex list summary: one per line, monospace; beyond three the count shows first, with everything listed once expanded.
describe("SettingsRules 正则列表摘要", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("不超过 3 条时逐行列出", async () => {
    stubFetch([{ ...parserSection, value: { episode_patterns: ["EP(\\d+)", "第(\\d+)集"] } }]);
    renderRules();
    const card = (await screen.findByText(t("settings.rules.parser"))).closest("li")!;
    const items = within(card).getAllByRole("listitem").map((item) => item.textContent);
    expect(items).toEqual(["EP(\\d+)", "第(\\d+)集"]);
    expect(within(card).queryByRole("button", { name: t("settings.rules.patternExpand") })).not.toBeInTheDocument();
  });

  it("超过 3 条时折叠为条数，可展开与收起", async () => {
    const patterns = ["a(\\d+)", "b(\\d+)", "c(\\d+)", "d(\\d+)"];
    stubFetch([{ ...parserSection, value: { episode_patterns: patterns } }]);
    renderRules();
    const card = (await screen.findByText(t("settings.rules.parser"))).closest("li")!;
    expect(within(card).getByText(t("settings.rules.patternCount", { n: 4 }))).toBeInTheDocument();
    expect(within(card).queryByText("a(\\d+)")).not.toBeInTheDocument();

    fireEvent.click(within(card).getByRole("button", { name: t("settings.rules.patternExpand") }));
    expect(within(card).getAllByRole("listitem").map((item) => item.textContent)).toEqual(patterns);
    expect(within(card).queryByText(t("settings.rules.patternCount", { n: 4 }))).not.toBeInTheDocument();

    fireEvent.click(within(card).getByRole("button", { name: t("settings.rules.patternCollapse") }));
    expect(within(card).queryByText("a(\\d+)")).not.toBeInTheDocument();
  });

  it("单条正则字符串按一行显示，空值显示不限", async () => {
    stubFetch([{ ...fileRulesSection, value: { must_contain: "(?i)1080p", not_in: "" } }]);
    renderRules();
    const card = (await screen.findByText(t("settings.rules.fileRules"))).closest("li")!;
    expect(within(card).getByText("(?i)1080p")).toBeInTheDocument();
    expect(within(card).getByText(t("settings.rules.unlimited"))).toBeInTheDocument();
  });
});
