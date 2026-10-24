import { fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import type { TagFilterRow } from "../../lib/api/client";
import { tagFullLabel } from "../../lib/anibtTags";
import { t } from "../../lib/i18n";
import { ANIBT_ROW_VOCABULARY, titleRowVocabulary } from "../../lib/tagRowVocabulary";
import { TagRowEditor } from "./TagRowEditor";

function Harness({ initial, onRow }: { initial: TagFilterRow; onRow: (row: TagFilterRow) => void }) {
  const [row, setRow] = useState(initial);
  return (
    <TagRowEditor
      vocabulary={ANIBT_ROW_VOCABULARY}
      value={row}
      onChange={(next) => {
        setRow(next);
        onRow(next);
      }}
    />
  );
}

function entries() {
  return screen.queryAllByTestId("tag-row-entry").map((entry) => `${entry.dataset.role}:${entry.textContent}`);
}

describe("TagRowEditor（添加型）", () => {
  it("空行显示不按标签过滤；候选按「添加为」的角色添加，必须与排除分两行显示", () => {
    const onRow = vi.fn();
    render(<Harness initial={{ require: [], exclude: [] }} onRow={onRow} />);
    expect(screen.getByText(t("anibtTags.none"))).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "CHS" }));
    fireEvent.click(screen.getByRole("radio", { name: t("anibtTags.role.exclude") }));
    fireEvent.click(screen.getByRole("button", { name: "HEVC" }));

    expect(onRow).toHaveBeenLastCalledWith({ require: ["language:CHS"], exclude: ["codec:HEVC"] });
    expect(within(screen.getByRole("list", { name: t("anibtTags.role.require") })).getByText("CHS")).toBeInTheDocument();
    expect(within(screen.getByRole("list", { name: t("anibtTags.role.exclude") })).getByText("HEVC")).toBeInTheDocument();
    // An added tag no longer appears among the candidates.
    expect(screen.queryByRole("button", { name: "HEVC" })).not.toBeInTheDocument();
  });

  it("已添加的标签可以切换角色和移除，同一标签不会同时在两个列表里", () => {
    const onRow = vi.fn();
    render(<Harness initial={{ require: ["language:CHS"], exclude: ["codec:HEVC"] }} onRow={onRow} />);

    fireEvent.click(screen.getByRole("button", { name: t("anibtTags.switchTo.exclude", { tag: tagFullLabel("language:CHS") }) }));
    expect(onRow).toHaveBeenLastCalledWith({ require: [], exclude: ["codec:HEVC", "language:CHS"] });
    fireEvent.click(screen.getByRole("button", { name: t("anibtTags.switchTo.require", { tag: tagFullLabel("codec:HEVC") }) }));
    expect(onRow).toHaveBeenLastCalledWith({ require: ["codec:HEVC"], exclude: ["language:CHS"] });
    fireEvent.click(screen.getByRole("button", { name: t("anibtTags.removeAria", { tag: tagFullLabel("codec:HEVC") }) }));
    expect(onRow).toHaveBeenLastCalledWith({ require: [], exclude: ["language:CHS"] });
    expect(screen.getByRole("button", { name: "HEVC" })).toBeInTheDocument();
  });

  it("手动输入：回车添加为当前角色，重复（忽略大小写，两个列表都查）时提示且不添加", () => {
    const onRow = vi.fn();
    render(<Harness initial={{ require: ["tag:BDRip"], exclude: [] }} onRow={onRow} />);
    const input = screen.getByLabelText(t("anibtTags.addValue"));

    fireEvent.click(screen.getByRole("radio", { name: t("anibtTags.role.exclude") }));
    fireEvent.change(input, { target: { value: " DV " } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onRow).toHaveBeenLastCalledWith({ require: ["tag:BDRip"], exclude: ["tag:DV"] });
    expect(input).toHaveValue("");

    fireEvent.change(input, { target: { value: "bdrip" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(screen.getByRole("alert")).toHaveTextContent(t("anibtTags.duplicate"));
    expect(onRow).toHaveBeenCalledTimes(1);
  });

  it("只读时只显示已添加的标签，没有切换、移除和添加", () => {
    render(<TagRowEditor vocabulary={ANIBT_ROW_VOCABULARY} value={{ require: ["language:CHS"], exclude: ["codec:HEVC"] }} readOnly />);
    expect(entries()).toEqual([`require:${"CHS"}`, "exclude:HEVC"]);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.queryByRole("radio")).not.toBeInTheDocument();
    expect(screen.queryByLabelText(t("anibtTags.addValue"))).not.toBeInTheDocument();
  });

  it("标题解析行：添加的值只能从词表里选，已添加的值不在可选项里", () => {
    const vocabulary = titleRowVocabulary({
      implementation: "reltitle@test",
      config_digest: "test",
      dimensions: [{ dimension: "resolution", values: ["1080p", "2160p"] }],
    });
    const onChange = vi.fn();
    render(<TagRowEditor vocabulary={vocabulary} value={{ require: ["resolution:1080p"], exclude: [] }} onChange={onChange} />);
    expect(screen.queryByRole("textbox", { name: t("anibtTags.addValue") })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("radio", { name: t("anibtTags.role.exclude") }));
    fireEvent.click(screen.getByRole("button", { name: t("anibtTags.add") }));
    expect(onChange).toHaveBeenLastCalledWith({ require: ["resolution:1080p"], exclude: ["resolution:2160p"] });
  });
});
