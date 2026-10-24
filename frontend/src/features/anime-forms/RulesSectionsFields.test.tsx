import { fireEvent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import type { RulesSections } from "../../lib/api/client";
import { renderWithQuery } from "../../testing/renderWithQuery";
import { RulesSectionsFields } from "./RulesSectionsFields";
import { t } from "../../lib/i18n";

// Selects are Base UI listboxes: open the combobox, then pick the option.
async function chooseOption(section: ReturnType<typeof within>, label: string, option: string) {
  await userEvent.click(section.getByRole("combobox", { name: label }));
  await userEvent.click(await screen.findByRole("option", { name: option }));
}

// Every section is the anime's own complete value (one-time copy of the
// defaults); there is no inherit mode.
const initialSections: RulesSections = {
  parser: { mode: "replace", value: { episode_patterns: ["[Ss](\\d{2})"] } },
  release_filter: { mode: "replace", value: { must_contain: "", not_in: "剧场版" } },
  file_filter: { mode: "replace", value: { must_contain: "", not_in: "" } },
  naming: {
    mode: "replace",
    value: {
      language: "js-naming-1",
      unknown: { directory_template: "", video_filename_template: "" },
      tv: { directory_template: "", video_filename_template: "" },
      movie: { directory_template: "", video_filename_template: "" },
      subtitle_suffix_template: "",
    },
  },
  file_effect: { mode: "replace", value: { operation: "copy", collision_policy: "fail" } },
};

function renderFields(onChange = vi.fn()) {
  renderWithQuery(<RulesSectionsFields sections={initialSections} onChange={onChange} />);
  return onChange;
}

function sectionByTitle(title: string) {
  const heading = screen.getByText(title, { exact: true });
  const section = heading.closest("section");
  if (!section) throw new Error(`section not found: ${title}`);
  return within(section);
}

function ControlledFields({ onChange }: { onChange: (next: RulesSections) => void }) {
  const [sections, setSections] = useState(initialSections);
  return (
    <RulesSectionsFields
      sections={sections}
      onChange={(next) => {
        setSections(next);
        onChange(next);
      }}
    />
  );
}

/** Types into the tag input and submits with Enter. */
function addTag(section: ReturnType<typeof sectionByTitle>, label: string, tag: string) {
  const input = section.getByLabelText(label);
  fireEvent.change(input, { target: { value: tag } });
  fireEvent.keyDown(input, { key: "Enter" });
}

describe("RulesSectionsFields", () => {
  it("renders the five rule sections without a subtitle section", () => {
    renderFields();

    for (const title of [t("tabs.sectionName.parser"), t("field.release_filter"), t("field.file_filter"), t("tabs.sectionName.naming"), t("tabs.sectionName.file_effect")]) {
      expect(screen.getByText(title, { exact: true })).toBeInTheDocument();
    }
    expect(screen.queryByText(t("rss.mediaKind.subtitle"), { exact: true })).not.toBeInTheDocument();
  });

  it("「已修改」只出现在 modified 列出的段卡上，缺省不标记", () => {
    const { unmount } = renderWithQuery(<RulesSectionsFields sections={initialSections} onChange={vi.fn()} />);
    expect(screen.queryByText(t("config.modified"))).not.toBeInTheDocument();
    unmount();

    renderWithQuery(<RulesSectionsFields sections={initialSections} onChange={vi.fn()} modified={new Set(["naming", "tag_filters"] as const)} />);
    expect(screen.getAllByText(t("config.modified"))).toHaveLength(1);
    expect(sectionByTitle(t("tabs.sectionName.naming")).getByText(t("config.modified"))).toHaveAttribute("title", t("config.modifiedTitle"));
    expect(sectionByTitle(t("field.release_filter")).queryByText(t("config.modified"))).not.toBeInTheDocument();
  });

  it("每段直接显示并编辑自己的值，没有使用默认 / 自定义切换", () => {
    renderFields();

    expect(screen.queryByRole("radio", { name: t("config.useDefaultsConfirm") })).not.toBeInTheDocument();
    const parser = sectionByTitle(t("tabs.sectionName.parser"));
    expect(parser.getByText("[Ss](\\d{2})")).toBeInTheDocument();
    expect(parser.getByLabelText(t("settings.rules.episodePatterns"))).toBeInTheDocument();

    const releaseFilter = sectionByTitle(t("field.release_filter"));
    expect(releaseFilter.getByLabelText(t("settings.rules.titleNot"))).toHaveValue("剧场版");
    expect(releaseFilter.getByLabelText(t("settings.rules.titleNot"))).toBeEnabled();

    const fileEffect = sectionByTitle(t("tabs.sectionName.file_effect"));
    expect(fileEffect.getByRole("combobox", { name: t("file.operationTitle") })).toHaveTextContent(t("errorDetails.copy"));
    expect(fileEffect.getByLabelText(t("file.operationTitle"))).toBeEnabled();
    expect(fileEffect.queryByLabelText("媒体库根目录标识")).not.toBeInTheDocument();
  });

  it("还没载入默认设置的草稿：缺的段按空值显示，第一次编辑就成为完整值", () => {
    const onChange = vi.fn();
    renderWithQuery(<RulesSectionsFields sections={{}} onChange={onChange} />);
    fireEvent.change(sectionByTitle(t("field.release_filter")).getByLabelText(t("settings.rules.titleMust")), { target: { value: "1080p" } });
    expect(onChange).toHaveBeenLastCalledWith({ release_filter: { mode: "replace", value: { must_contain: "1080p", not_in: "" } } });
  });

  it.each([
    {
      key: "parser" as const,
      title: t("tabs.sectionName.parser"),
      edit: (section: ReturnType<typeof sectionByTitle>) => addTag(section, t("settings.rules.episodePatterns"), "custom-parser"),
      expected: { episode_patterns: ["[Ss](\\d{2})", "custom-parser"] },
    },
    {
      key: "release_filter" as const,
      title: t("field.release_filter"),
      edit: (section: ReturnType<typeof sectionByTitle>) =>
        fireEvent.change(section.getByLabelText(t("settings.rules.titleMust")), { target: { value: "仅此组" } }),
      expected: { must_contain: "仅此组", not_in: "剧场版" },
    },
    {
      key: "file_filter" as const,
      title: t("field.file_filter"),
      edit: (section: ReturnType<typeof sectionByTitle>) =>
        fireEvent.change(section.getByLabelText(t("settings.rules.fileNot")), { target: { value: "(?i)sample" } }),
      expected: { must_contain: "", not_in: "(?i)sample" },
    },
    {
      key: "naming" as const,
      title: t("tabs.sectionName.naming"),
      edit: (section: ReturnType<typeof sectionByTitle>) =>
        fireEvent.change(section.getByLabelText(t("naming.filename")), { target: { value: "{title} - {episode}" } }),
      expected: {
        language: "js-naming-1",
        unknown: { directory_template: "", video_filename_template: "" },
        tv: { directory_template: "", video_filename_template: "{title} - {episode}" },
        movie: { directory_template: "", video_filename_template: "" },
        subtitle_suffix_template: "",
      },
    },
    {
      key: "file_effect" as const,
      title: t("tabs.sectionName.file_effect"),
      edit: (section: ReturnType<typeof sectionByTitle>) => chooseOption(section, t("file.operationTitle"), t("file.operation.hardlink")),
      expected: { operation: "hardlink", collision_policy: "fail" },
    },
    {
      key: "file_effect" as const,
      title: t("tabs.sectionName.file_effect"),
      edit: (section: ReturnType<typeof sectionByTitle>) => chooseOption(section, t("file.collisionTitle"), t("file.collision.overwrite")),
      expected: { operation: "copy", collision_policy: "overwrite" },
    },
  ])("edits $key in place and returns the complete sections body", async ({ key, title, edit, expected }) => {
    const onChange = vi.fn();
    renderWithQuery(<ControlledFields onChange={onChange} />);

    await edit(sectionByTitle(title));

    expect(onChange).toHaveBeenLastCalledWith({
      ...initialSections,
      [key]: { mode: "replace", value: expected },
    });
  });

  it("同名文件处理选覆盖时提示会永久删除原有文件", async () => {
    renderWithQuery(<ControlledFields onChange={vi.fn()} />);
    const title = t("tabs.sectionName.file_effect");
    expect(sectionByTitle(title).queryByText(t("file.collisionOverwriteNote"))).toBeNull();
    await chooseOption(sectionByTitle(title), t("file.collisionTitle"), t("file.collision.overwrite"));
    expect(sectionByTitle(title).getByText(t("file.collisionOverwriteNote"))).toBeInTheDocument();
  });

  it("清空排除正则即不再排除", () => {
    const onChange = vi.fn();
    renderWithQuery(<ControlledFields onChange={onChange} />);

    fireEvent.change(sectionByTitle(t("field.release_filter")).getByLabelText(t("settings.rules.titleNot")), { target: { value: "" } });

    expect(onChange).toHaveBeenLastCalledWith({
      ...initialSections,
      release_filter: {
        mode: "replace",
        value: { must_contain: "", not_in: "" },
      },
    });
  });

});
