import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  ApiError,
  type NamingResolvedValue,
  type NamingTemplate,
  type NamingTemplateCatalog,
  type NamingTemplatePreview,
  type NamingTemplatePreviewInput,
} from "../../lib/api/client";
import { renderWithQuery } from "../../testing/renderWithQuery";
import { NamingTemplateFields, type NamingTemplateValue } from "./NamingTemplateFields";
import { t } from "../../lib/i18n";

const getNamingTemplateCatalog = vi.hoisted(() => vi.fn());
const previewNamingTemplate = vi.hoisted(() => vi.fn());

vi.mock("../../lib/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/api/client")>();
  return { ...actual, getNamingTemplateCatalog, previewNamingTemplate };
});

const catalog: NamingTemplateCatalog = {
  language: "js-naming-1",
  fields: [
    {
		path: "Anime.Title",
		label: "番组标题",
		type: "string",
		nullable: true,
		source: "managed_anime",
		sample_value: "示例动画",
    },
    {
      path: "Parsed.Episode.Display",
      label: "显示集数",
      type: "string",
      nullable: true,
      source: "parser",
      sample_value: "01",
    },
    {
      path: "Derived.EffectiveTitle",
      label: "生效标题",
      type: "string",
      nullable: false,
      source: "derived",
      sample_value: "示例动画",
    },
    { path: "File.Extension", label: "文件扩展名", type: "string", nullable: true, source: "file", sample_value: "mkv" },
  ],
  functions: [
    {
      name: "sanitize",
      signature: "sanitize value",
      description: "清理路径非法字符。",
      arguments: ["value"],
      example: "${sanitize(Derived.EffectiveTitle)}",
    },
    {
      name: "pad",
      signature: "pad value width",
      description: "左侧补零到指定宽度。",
      arguments: ["value", "width"],
      example: "${pad(Parsed.Episode.Display, 2)}",
    },
	],
	shorthands: [
		{ name: "s00e00", expansion: `"S" + pad(anime.season, 2) + "E" + pad(ep.number, 2)`, example: "S01E12" },
		{ name: "e00", expansion: "pad(ep.number, 2)", example: "12" },
	],
	samples: [
		{ ref: "sample-tv", label: "剧集样例" },
		{ ref: "sample-movie", label: "电影样例" },
	],
	vars_keys: ["anime"],
	vars_loaded_at: "2026-09-23T00:00:00Z",
};

function preview(overrides: Partial<NamingTemplatePreview> = {}): NamingTemplatePreview {
  return {
    valid: true,
    rendered: "示例动画",
    warnings: [],
    errors: [],
    ...overrides,
  };
}

const emptyValue: NamingTemplateValue = {
  language: "js-naming-1",
  unknown: { directory_template: "", video_filename_template: "" },
  tv: { directory_template: "", video_filename_template: "" },
  movie: { directory_template: "", video_filename_template: "" },
  subtitle_suffix_template: "",
};

function Controlled({
  disabled = false,
  initial = emptyValue,
  samplePicker = false,
  templates,
}: {
  disabled?: boolean;
  initial?: NamingTemplateValue;
  samplePicker?: boolean;
  templates?: NamingTemplate[];
}) {
  const [value, setValue] = useState<NamingTemplateValue>(initial);
  return <NamingTemplateFields value={value} onChange={setValue} disabled={disabled} samplePicker={samplePicker} templates={templates} />;
}

// The preview splits the text into several fragments: match paragraph elements by full text.
function paragraph(text: string) {
  return (_: string, element: Element | null) => element?.tagName === "P" && (element.textContent ?? "").includes(text);
}


// Selects are Base UI listboxes: open the combobox, then pick the option.
async function chooseOption(label: string, option: string | RegExp) {
  await userEvent.click(screen.getByRole("combobox", { name: label }));
  await userEvent.click(await screen.findByRole("option", { name: option }));
}

describe("NamingTemplateFields", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.useRealTimers();
    getNamingTemplateCatalog.mockResolvedValue(catalog);
    previewNamingTemplate.mockResolvedValue(preview());
  });

  it("变量面板「常用」区展示简写与展开结果，点击插入 ${name}", async () => {
    renderWithQuery(<Controlled />);

    // The palette starts collapsed; expanded, the "common" section lists the catalog's shorthands and their expansions.
    fireEvent.click(await screen.findByRole("button", { name: new RegExp(t("naming.expand")) }));
    expect(await screen.findByText(t("naming.shorthandsLabel"))).toBeInTheDocument();
    const s00e00 = screen.getByRole("button", { name: t("naming.shorthandPair", { token: "${s00e00}", example: "S01E12" }) });
    expect(screen.getByRole("button", { name: t("naming.shorthandPair", { token: "${e00}", example: "12" }) })).toBeInTheDocument();
    // The two non-obvious rules must be visible at the point of use: a season shorthand is empty without a season number, and the full expression stays legal.
    expect(screen.getByText(t("naming.shorthandsHint"))).toBeInTheDocument();

    // Clicking inserts into the currently focused template (the TV directory template by default).
    fireEvent.focus(screen.getByLabelText(t("naming.directory")));
    fireEvent.click(s00e00);
    expect(screen.getByLabelText(t("naming.directory"))).toHaveValue("${s00e00}");
  });

  it("渲染三 tab；默认剧集 tab 显示目录与文件名两个模板输入", () => {
    renderWithQuery(<Controlled />);

    expect(screen.getByRole("radio", { name: t("naming.tabs.tv") })).toBeChecked();
    expect(screen.getByRole("radio", { name: t("forms.movie") })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: t("naming.tabs.subtitle") })).toBeInTheDocument();
    expect(screen.getByLabelText(t("naming.directory"))).toBeInTheDocument();
    expect(screen.getByLabelText(t("naming.filename"))).toBeInTheDocument();
  });

  it("三个 tab 合计五个模板编辑框，字幕后缀 tab 附锁定段说明", () => {
    renderWithQuery(<Controlled />);

    const directory = screen.getByLabelText(t("naming.directory"));
    const filename = screen.getByLabelText(t("naming.filename"));
    fireEvent.change(directory, { target: { value: "tv-dir" } });
    fireEvent.change(filename, { target: { value: "tv-file" } });

    fireEvent.click(screen.getByRole("radio", { name: t("forms.movie") }));
    fireEvent.change(screen.getByLabelText(t("naming.directory")), { target: { value: "movie-dir" } });
    fireEvent.change(screen.getByLabelText(t("naming.filename")), { target: { value: "movie-file" } });

    fireEvent.click(screen.getByRole("radio", { name: t("naming.tabs.subtitle") }));
    expect(screen.queryByLabelText(t("naming.directory"))).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(t("settings.rules.namingSubtitle")), { target: { value: "sub-suffix" } });
    expect(screen.getByText(t("naming.subtitleHint"))).toBeInTheDocument();

    // Switching back confirms all five values are kept independently.
    fireEvent.click(screen.getByRole("radio", { name: t("naming.tabs.tv") }));
    expect(screen.getByLabelText(t("naming.directory"))).toHaveValue("tv-dir");
    expect(screen.getByLabelText(t("naming.filename"))).toHaveValue("tv-file");
    fireEvent.click(screen.getByRole("radio", { name: t("forms.movie") }));
    expect(screen.getByLabelText(t("naming.directory"))).toHaveValue("movie-dir");
    expect(screen.getByLabelText(t("naming.filename"))).toHaveValue("movie-file");
    fireEvent.click(screen.getByRole("radio", { name: t("naming.tabs.subtitle") }));
    expect(screen.getByLabelText(t("settings.rules.namingSubtitle"))).toHaveValue("sub-suffix");
  });

  it("占位符提示输入模板", () => {
    renderWithQuery(<Controlled />);

		expect(screen.getByLabelText(t("naming.directory"))).toHaveAttribute("placeholder", t("naming.placeholder"));
  });

  it("展开后按 path 顶层对象分组展示变量与函数白名单", async () => {
    renderWithQuery(<Controlled />);

		const toggle = await screen.findByRole("button", { name: /可用变量 4 个 · 函数 2 个/ });
    fireEvent.click(toggle);

		expect(screen.getByText("Anime")).toBeInTheDocument();
		expect(screen.getByText("Parsed")).toBeInTheDocument();
		expect(screen.getByText("Derived")).toBeInTheDocument();
		expect(screen.getByText("File")).toBeInTheDocument();
		expect(screen.getByText("Anime.Title")).toBeInTheDocument();
    expect(screen.getByText("sanitize value")).toBeInTheDocument();
  });

  it("值命名空间按说明文字分组，变量路径保持原样", async () => {
    getNamingTemplateCatalog.mockResolvedValue({
      ...catalog,
      fields: [
        ...catalog.fields,
        { path: "rt.title", type: "string", nullable: true, source: "source_facts.title_tags", sample_value: "Silent Witch" },
        { path: "anibt.language", type: "string", nullable: true, source: "source_facts.anibt", sample_value: "CHS+CHT" },
      ],
    });
    renderWithQuery(<Controlled />);

    fireEvent.click(await screen.findByRole("button", { name: /可用变量 6 个/ }));

    expect(screen.getByText(t("templateFacts.roots.rt"))).toBeInTheDocument();
    expect(screen.getByText(t("templateFacts.roots.anibt"))).toBeInTheDocument();
    expect(screen.getAllByText("rt.title").length).toBeGreaterThan(0);
    expect(screen.getAllByText("anibt.language").length).toBeGreaterThan(0);
  });

  it("点击变量插入带前导点的对象路径到当前聚焦的模板输入", async () => {
    renderWithQuery(<Controlled />);

    const toggle = await screen.findByRole("button", { name: /可用变量/ });
    fireEvent.click(toggle);

    const filename = screen.getByLabelText(t("naming.filename"));
    fireEvent.focus(filename);
    fireEvent.click(screen.getByText("显示集数"));

    await waitFor(() => expect(filename).toHaveValue("${Parsed.Episode.Display}"));
    expect(screen.getByLabelText(t("naming.directory"))).toHaveValue("");
  });

  it("点击函数插入示例到当前聚焦的模板输入", async () => {
    renderWithQuery(<Controlled />);

    const toggle = await screen.findByRole("button", { name: /可用变量/ });
    fireEvent.click(toggle);

    const directory = screen.getByLabelText(t("naming.directory"));
    fireEvent.focus(directory);
		fireEvent.click(screen.getByRole("button", { name: /pad value width/ }));

    await waitFor(() => expect(directory).toHaveValue("${pad(Parsed.Episode.Display, 2)}"));
  });

  it("模板非空时调用预览并展示渲染结果与合成路径", async () => {
    previewNamingTemplate.mockResolvedValue(preview({ composed_target: "示例动画/Season 01/示例动画 - E01.mkv" }));
    renderWithQuery(<Controlled />);

    fireEvent.change(screen.getByLabelText(t("naming.directory")), {
      target: { value: "${Derived.EffectiveTitle}" },
    });

    await waitFor(() => expect(previewNamingTemplate).toHaveBeenCalled());
    const input = previewNamingTemplate.mock.calls[0][0] as NamingTemplatePreviewInput;
    expect(input.template_kind).toBe("directory");
    expect(input.sample_ref).toBe("sample-tv");
    expect(await screen.findByText("示例动画")).toBeInTheDocument();
    expect(screen.getByText(paragraph(t("naming.composedLabel") + "示例动画/Season 01"))).toBeInTheDocument();
  });

  it("正文形态的模板原样显示并送去预览", async () => {
    const body = "/library/${anime.title}/${vars.anime.ep}";
    renderWithQuery(<Controlled initial={{ ...emptyValue, tv: { directory_template: body, video_filename_template: "" } }} />);

    expect(screen.getByLabelText(t("naming.directory"))).toHaveValue(body);
    await waitFor(() => expect(previewNamingTemplate).toHaveBeenCalled());
    const input = previewNamingTemplate.mock.calls[0][0] as NamingTemplatePreviewInput;
    expect(input.template_source).toBe(body);
  });

  it("电影 tab 预览带 sample-movie，文件名模板使用 video_filename kind", async () => {
    renderWithQuery(<Controlled />);
    fireEvent.click(screen.getByRole("radio", { name: t("forms.movie") }));

    fireEvent.change(screen.getByLabelText(t("naming.filename")), {
      target: { value: "${Derived.EffectiveTitle}.${File.Extension}" },
    });

    await waitFor(() => expect(previewNamingTemplate).toHaveBeenCalled());
    const input = previewNamingTemplate.mock.calls[0][0] as NamingTemplatePreviewInput;
    expect(input.template_kind).toBe("video_filename");
    expect(input.sample_ref).toBe("sample-movie");
  });

  it("字幕后缀预览回显锁定目录/文件名/扩展名", async () => {
    previewNamingTemplate.mockResolvedValue(
      preview({
        rendered: ".chs",
        locked_directory: "示例动画/Season 01",
        locked_basename: "示例动画 - E01",
        locked_extension: "mkv",
      }),
    );
    renderWithQuery(<Controlled />);
    fireEvent.click(screen.getByRole("radio", { name: t("naming.tabs.subtitle") }));

    fireEvent.change(screen.getByLabelText(t("settings.rules.namingSubtitle")), {
      target: { value: "${defaultValue(\"und\", Parsed.Language.Code)}" },
    });

    await waitFor(() => expect(previewNamingTemplate).toHaveBeenCalled());
    const input = previewNamingTemplate.mock.calls[0][0] as NamingTemplatePreviewInput;
    expect(input.template_kind).toBe("subtitle_suffix");
    expect(await screen.findByText(/目录（锁定）：示例动画\/Season 01/)).toBeInTheDocument();
    expect(screen.getByText(paragraph(t("naming.lockedBaseLabel") + "示例动画 - E01"))).toBeInTheDocument();
    expect(screen.getByText(/扩展名（锁定）：mkv/)).toBeInTheDocument();
  });

  it("预览报错时展示错误且不阻塞继续编辑", async () => {
    previewNamingTemplate.mockResolvedValue(
      preview({
        valid: false,
        rendered: "",
        errors: [{ code: "parse_error", message: "模板语法错误。", start: 0, end: 10 }],
      }),
    );
    renderWithQuery(<Controlled />);

    const directory = screen.getByLabelText(t("naming.directory"));
    fireEvent.change(directory, { target: { value: "${bad" } });

    expect(await screen.findByRole("alert")).toHaveTextContent("模板语法错误。");
    expect(screen.getByText(t("naming.invalid"))).toBeInTheDocument();

		fireEvent.change(directory, { target: { value: "${Anime.Title}" } });
		expect(directory).toHaveValue("${Anime.Title}");
  });

  it("展示空值说明，空值导致路径无效时展示本地化原因", async () => {
    previewNamingTemplate.mockResolvedValueOnce(
      preview({ rendered: "/media/示例动画", empty_facts: ["anime.year", "anime.season_folder"] }),
    );
    renderWithQuery(<Controlled />);
    const directory = screen.getByLabelText(t("naming.directory"));
    fireEvent.change(directory, { target: { value: "/media/${rt.title} (${anime.year})" } });
    expect(await screen.findByText("空值：年份、季目录")).toBeInTheDocument();

    previewNamingTemplate.mockResolvedValue(
      preview({
        valid: false,
        rendered: "",
        empty_facts: ["anime.year"],
        errors: [{ code: "NAMING_FACT_MISSING", message: "raw server text", start: 0, end: 0 }],
      }),
    );
    fireEvent.change(directory, { target: { value: "/media/${anime.year}" } });
    expect(await screen.findByRole("alert")).toHaveTextContent(t("error.NAMING_FACT_MISSING"));
  });

  it("展示 null 变量告警", async () => {
    previewNamingTemplate.mockResolvedValue(
      preview({
        rendered: "",
        warnings: [
          {
            code: "null_variable",
            message: "变量在样例中为 null，已渲染为空值：Parsed.Episode.Title",
            variable: "Parsed.Episode.Title",
          },
        ],
      }),
    );
    renderWithQuery(<Controlled />);

    fireEvent.change(screen.getByLabelText(t("naming.directory")), {
      target: { value: "${Parsed.Episode.Title}" },
    });

    expect(await screen.findByText(/变量在样例中为 null/)).toBeInTheDocument();
  });

  it("继承态不展示变量目录与预览，也不请求预览接口", async () => {
    renderWithQuery(<Controlled disabled />);

    expect(screen.getByLabelText(t("naming.directory"))).toBeDisabled();
    expect(screen.queryByRole("button", { name: /可用变量/ })).not.toBeInTheDocument();
    await waitFor(() => expect(previewNamingTemplate).not.toHaveBeenCalled());
  });

  it("目录加载失败时给出可读错误而非静默空面板", async () => {
    getNamingTemplateCatalog.mockRejectedValue(
      new ApiError({ status: 503, message: "catalog unavailable", code: "DATABASE_UNAVAILABLE" }),
    );
    renderWithQuery(<Controlled />);

    expect(await screen.findByRole("alert")).toHaveTextContent(t("error.DATABASE_UNAVAILABLE"));
    // An unavailable catalog must not also hide the template inputs.
    expect(screen.getByLabelText(t("naming.directory"))).toBeInTheDocument();
  });

  it("预览样本选择器从 catalog 动态渲染，切换后预览带所选 sample_ref", async () => {
    renderWithQuery(<Controlled samplePicker />);

    // Defaults to auto-matching the sample for the tab keyword (TV -> sample-tv).
    const selector = await screen.findByRole("combobox", { name: t("naming.sample") });
    expect(selector).toHaveTextContent("剧集样例");

    await chooseOption(t("naming.sample"), /电影样例/);
    fireEvent.change(screen.getByLabelText(t("naming.directory")), {
      target: { value: "${Derived.EffectiveTitle}" },
    });

    await waitFor(() => expect(previewNamingTemplate).toHaveBeenCalled());
    const input = previewNamingTemplate.mock.calls[0][0] as NamingTemplatePreviewInput;
    expect(input.sample_ref).toBe("sample-movie");
  });

  it("字幕后缀 tab 自动匹配目录中的字幕样本 ref（不硬编码）", async () => {
    getNamingTemplateCatalog.mockResolvedValue({
      ...catalog,
      samples: [...catalog.samples, { ref: "sample.tv-subtitle", label: "剧集字幕样例" }],
    });
    renderWithQuery(<Controlled />);
    fireEvent.click(screen.getByRole("radio", { name: t("naming.tabs.subtitle") }));

    fireEvent.change(screen.getByLabelText(t("settings.rules.namingSubtitle")), {
      target: { value: "${defaultValue(\"und\", Parsed.Language.Code)}" },
    });

    await waitFor(() => expect(previewNamingTemplate).toHaveBeenCalled());
    const input = previewNamingTemplate.mock.calls[0][0] as NamingTemplatePreviewInput;
    expect(input.template_kind).toBe("subtitle_suffix");
    expect(input.sample_ref).toBe("sample.tv-subtitle");
  });

  it("锁定段与合成路径缺失时优雅降级，不渲染对应区块", async () => {
    previewNamingTemplate.mockResolvedValue(preview({ rendered: ".chs" }));
    renderWithQuery(<Controlled />);
    fireEvent.click(screen.getByRole("radio", { name: t("naming.tabs.subtitle") }));

    fireEvent.change(screen.getByLabelText(t("settings.rules.namingSubtitle")), {
      target: { value: "${defaultValue(\"und\", Parsed.Language.Code)}" },
    });

    expect(await screen.findByText(".chs")).toBeInTheDocument();
    expect(screen.queryByText(/（锁定）/)).not.toBeInTheDocument();
    expect(screen.queryByText(/合成路径/)).not.toBeInTheDocument();
  });

  it("字幕预览同时展示后缀渲染、锁定段与合成路径", async () => {
    previewNamingTemplate.mockResolvedValue(
      preview({
        rendered: ".chs",
        composed_target: "示例动画/Season 01/示例动画 - E01.chs.ass",
        locked_directory: "示例动画/Season 01",
        locked_basename: "示例动画 - E01",
        locked_extension: "ass",
      }),
    );
    renderWithQuery(<Controlled />);
    fireEvent.click(screen.getByRole("radio", { name: t("naming.tabs.subtitle") }));

    fireEvent.change(screen.getByLabelText(t("settings.rules.namingSubtitle")), {
      target: { value: "${defaultValue(\"und\", Parsed.Language.Code)}" },
    });

    expect(await screen.findByText(".chs")).toBeInTheDocument();
    expect(screen.getByText(paragraph(t("naming.composedLabel") + "示例动画/Season 01/示例动画 - E01.chs.ass"))).toBeInTheDocument();
    expect(screen.getByText(t("naming.lockedExt", { value: "ass" }))).toBeInTheDocument();
  });

  it("变量面板展开后显示可空徽标与示例值", async () => {
    renderWithQuery(<Controlled />);

    const toggle = await screen.findByRole("button", { name: /可用变量/ });
    fireEvent.click(toggle);

    // Parsed.Episode.Display nullable=true、sample_value="01"。
    const variableButton = screen.getByText("显示集数").closest("button");
    expect(variableButton).toHaveTextContent(t("templateFacts.nullable"));
    expect(variableButton).toHaveTextContent(t("templateFacts.example", { value: "01" }));
  });

  it("语法与变量帮助弹窗展示字段树、可空性、示例值与函数签名", async () => {
    renderWithQuery(<Controlled />);

    fireEvent.click(await screen.findByRole("button", { name: t("naming.help") }));

    const dialog = await screen.findByRole("dialog");
    // Custom variables are hidden in the current version: the help covers syntax only, with no vars examples and no vars_keys listed (the catalog does carry "anime").
    expect(dialog).toHaveTextContent(t("naming.helpSyntax"));
    expect(dialog).not.toHaveTextContent("vars.");
    expect(dialog).not.toHaveTextContent(t("naming.helpBody", { keys: "anime" }));
    // The field tree: grouping + the full template spelling + type + a nullable badge + example values.
		expect(dialog).toHaveTextContent("Anime");
		expect(dialog).toHaveTextContent("${Anime.Title}");
    expect(dialog).toHaveTextContent("${Parsed.Episode.Display}");
    expect(dialog).toHaveTextContent(t("naming.sampleValue", { value: "01" }));
    // Function signatures, parameters and examples.
    expect(dialog).toHaveTextContent("sanitize value");
    expect(dialog).toHaveTextContent(t("naming.exampleTitle", { value: "${sanitize(Derived.EffectiveTitle)}" }));
    // The fixed redacted sample list.
    expect(dialog).toHaveTextContent("sample-tv");
  });

  it("预览携带 resolved_values 时可折叠展示三态徽标、value 摘要与审计证据", async () => {
    previewNamingTemplate.mockResolvedValue(
      preview({
        resolved_values: [
          {
            key: "parsed.episode",
            state: "value",
            value: "01",
            source: "Release.OriginalTitle",
            evidence: "rule bracket-episode, span [12..14]",
          },
          { key: "parsed.language", state: null },
          {
            key: "derived.effective_title",
            state: "conflict",
            value: "示例动画",
			source: "Anime.Title",
          },
        ],
      }),
    );
    renderWithQuery(<Controlled />);

    fireEvent.change(screen.getByLabelText(t("naming.directory")), {
      target: { value: "${Derived.EffectiveTitle}" },
    });

    // Collapsed by default: only the count summary shows, no itemized details.
    const toggle = await screen.findByRole("button", { name: new RegExp(t("naming.resolved", { n: 3 })) });
    expect(screen.queryByText("parsed.episode")).not.toBeInTheDocument();

    fireEvent.click(toggle);

    expect(screen.getByText("parsed.episode")).toBeInTheDocument();
    expect(screen.getByText(t("naming.resolvedState.value"))).toBeInTheDocument();
    expect(screen.getByText(t("naming.resolvedState.empty"))).toBeInTheDocument();
    expect(screen.getByText(t("naming.resolvedState.conflict"))).toBeInTheDocument();
    expect(screen.getByText("01")).toBeInTheDocument();
    expect(screen.getByText(new RegExp(t("naming.source", { value: "Release.OriginalTitle" }).replace(".", "\\.")))).toBeInTheDocument();
    expect(screen.getByText(/rule bracket-episode/)).toBeInTheDocument();
  });

  it("未知 resolved-value state 安全降级为 muted/unknown 展示", async () => {
    const unknownState = {
      key: "derived.future",
      state: "future_state",
      value: "未知值",
    } as unknown as NamingResolvedValue;
    previewNamingTemplate.mockResolvedValue(preview({ resolved_values: [unknownState] }));
    renderWithQuery(<Controlled />);

    fireEvent.change(screen.getByLabelText(t("naming.directory")), {
      target: { value: "${Derived.EffectiveTitle}" },
    });

    const toggle = await screen.findByRole("button", { name: new RegExp(t("naming.resolved", { n: 1 })) });
    fireEvent.click(toggle);

    const label = screen.getByText(t("naming.resolvedState.unknown"));
    expect(label).toHaveAttribute("data-tone", "muted");
    expect(screen.getByText("derived.future")).toBeInTheDocument();
    expect(screen.getByText("未知值")).toBeInTheDocument();
  });

  it("resolved_values 缺失时不渲染解析值区块", async () => {
    previewNamingTemplate.mockResolvedValue(preview());
    renderWithQuery(<Controlled />);

    fireEvent.change(screen.getByLabelText(t("naming.directory")), {
      target: { value: "${Derived.EffectiveTitle}" },
    });

    expect(await screen.findByText("示例动画")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /解析值/ })).not.toBeInTheDocument();
  });
});

const libraryTemplates: NamingTemplate[] = [
  {
    key: "default",
    name: "Default",
    rules: {
      language: "js-naming-1",
      unknown: { directory_template: "U", video_filename_template: "V" },
      tv: { directory_template: "A", video_filename_template: "B" },
      movie: { directory_template: "C", video_filename_template: "D" },
      subtitle_suffix_template: "E",
    },
  },
];

describe("NamingTemplateFields templates", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    getNamingTemplateCatalog.mockResolvedValue(catalog);
    previewNamingTemplate.mockResolvedValue(preview());
  });

  it("选择模板把模板内容复制进五个字段，未匹配时显示自定义", async () => {
    renderWithQuery(<Controlled templates={libraryTemplates} />);

    const select = await screen.findByRole("combobox", { name: t("naming.templateField") });
    expect(select).toHaveTextContent(t("naming.custom"));
    await chooseOption(t("naming.templateField"), "Default");
    expect(screen.getByLabelText(t("naming.directory"))).toHaveValue("A");
    expect(screen.getByLabelText(t("naming.filename"))).toHaveValue("B");
    fireEvent.click(screen.getByRole("radio", { name: t("forms.movie") }));
    expect(screen.getByLabelText(t("naming.directory"))).toHaveValue("C");
    fireEvent.click(screen.getByRole("radio", { name: t("naming.tabs.subtitle") }));
    expect(screen.getByLabelText(t("settings.rules.namingSubtitle"))).toHaveValue("E");
    expect(select).toHaveTextContent("Default");
  });

  it("复制后字段仍可编辑，改动后显示自定义", async () => {
    renderWithQuery(<Controlled templates={libraryTemplates} />);

    const select = await screen.findByRole("combobox", { name: t("naming.templateField") });
    await chooseOption(t("naming.templateField"), "Default");
    fireEvent.change(screen.getByLabelText(t("naming.directory")), { target: { value: "手动改过的目录" } });
    expect(screen.getByLabelText(t("naming.directory"))).toHaveValue("手动改过的目录");
    expect(select).toHaveTextContent(t("naming.custom"));
  });

  it("没有模板库或继承态时不显示选择模板；不传 samplePicker 不显示样本选择", async () => {
    const { unmount } = renderWithQuery(<Controlled />);
    expect(screen.queryByRole("combobox", { name: t("naming.templateField") })).not.toBeInTheDocument();
    await screen.findByRole("button", { name: t("naming.help") });
    expect(screen.queryByRole("combobox", { name: t("naming.sample") })).not.toBeInTheDocument();
    unmount();

    renderWithQuery(<Controlled disabled templates={libraryTemplates} />);
    expect(screen.queryByRole("combobox", { name: t("naming.templateField") })).not.toBeInTheDocument();
  });
});

const unifiedCatalog: NamingTemplateCatalog = {
  ...catalog,
  language: "js-naming-1",
  fields: [
    { path: "anime.title", label: "标题", label_key: "facts.anime.title.label", description_key: "facts.anime.title.description", type: "string", nullable: false, source: "anime", sample_value: "示例动画", phase: "release" },
    { path: "ep.display", label: "显示集数", label_key: "facts.ep.display.label", type: "string", nullable: true, source: "ep", sample_value: "07", phase: "release" },
    { path: "media.resolution", label: "分辨率", label_key: "facts.media.resolution.label", type: "string", nullable: true, source: "file", sample_value: "1080p", phase: "execution" },
  ],
};

describe("NamingTemplateFields unified catalog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.useRealTimers();
    getNamingTemplateCatalog.mockResolvedValue(unifiedCatalog);
    previewNamingTemplate.mockResolvedValue(preview());
  });

  it("按根分组，名称来自文案，执行时变量带标记", async () => {
    renderWithQuery(<Controlled />);
    fireEvent.click(await screen.findByRole("button", { name: /可用变量 3 个/ }));

    expect(getNamingTemplateCatalog).toHaveBeenCalledWith("js-naming-1");
    expect(screen.getByText(t("templateFacts.roots.anime"))).toBeInTheDocument();
    expect(screen.getByText(t("templateFacts.roots.media"))).toBeInTheDocument();
    const resolution = screen.getByText(t("facts.media.resolution.label")).closest("button");
    expect(resolution).toHaveTextContent(t("templateFacts.phaseExecution"));
    expect(resolution).not.toHaveTextContent(t("templateFacts.example", { value: "1080p" }));
  });

  it("搜索按名称或路径过滤，点击插入 ${路径}", async () => {
    renderWithQuery(<Controlled />);
    fireEvent.click(await screen.findByRole("button", { name: /可用变量/ }));

    fireEvent.change(screen.getByLabelText(t("templateFacts.searchLabel")), { target: { value: "ep.dis" } });
    expect(screen.queryByText(t("facts.anime.title.label"))).not.toBeInTheDocument();
    const filename = screen.getByLabelText(t("naming.filename"));
    fireEvent.focus(filename);
    fireEvent.click(screen.getByText(t("facts.ep.display.label")));
    await waitFor(() => expect(filename).toHaveValue("${ep.display}"));

    fireEvent.change(screen.getByLabelText(t("templateFacts.searchLabel")), { target: { value: "没有这个" } });
    expect(screen.getByText(t("templateFacts.noMatch"))).toBeInTheDocument();
  });

  it("预览中下载后才有的值显示为〈名称〉占位", async () => {
    previewNamingTemplate.mockResolvedValue(preview({ rendered: "示例动画 [⟨media.resolution⟩].mkv" }));
    renderWithQuery(<Controlled />);
    fireEvent.change(screen.getByLabelText(t("naming.filename")), { target: { value: "x" } });

    const placeholder = await screen.findByText(`〈${t("facts.media.resolution.label")}〉`);
    expect(placeholder.tagName).toBe("MARK");
    expect(placeholder).toHaveAttribute("title", t("templateFacts.placeholderHint", { path: "media.resolution" }));
    expect(previewNamingTemplate.mock.calls[0][0]).toMatchObject({ language: "js-naming-1" });
  });
});
