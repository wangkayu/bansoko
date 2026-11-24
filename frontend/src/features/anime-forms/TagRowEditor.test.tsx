import { fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import type { TagFilterRow } from "../../lib/api/client";
import { tagFullLabel } from "../../lib/anibtTags";
import { t } from "../../lib/i18n";
import { ANIBT_ROW_VOCABULARY, titleRowVocabulary } from "../../lib/tagRowVocabulary";
import { TagRowEditor } from "./TagRowEditor";

function Harness({ initial, onRow, extraTags = [] }: { initial: TagFilterRow; onRow: (row: TagFilterRow) => void; extraTags?: readonly string[] }) {
  const [row, setRow] = useState(initial);
  return (
    <TagRowEditor
      vocabulary={ANIBT_ROW_VOCABULARY}
      value={row}
      extraTags={extraTags}
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
  it("空行显示不按标签过滤；展开添加面板后按「添加为」的角色添加，必须与排除分两行显示", () => {
    const onRow = vi.fn();
    render(<Harness initial={{ require: [], exclude: [] }} onRow={onRow} />);
    expect(screen.getByText(t("anibtTags.none"))).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: t("anibtTags.openEditor") }));
    fireEvent.click(screen.getByRole("button", { name: "CHS" }));
    fireEvent.click(screen.getByRole("radio", { name: t("anibtTags.role.exclude") }));
    fireEvent.click(screen.getByRole("button", { name: "HEVC" }));

    expect(onRow).toHaveBeenLastCalledWith({ require: ["language:CHS"], exclude: ["codec:HEVC"] });
    expect(within(screen.getByRole("list", { name: t("anibtTags.role.require") })).getByText("CHS")).toBeInTheDocument();
    expect(within(screen.getByRole("list", { name: t("anibtTags.role.exclude") })).getByText("HEVC")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "HEVC" })).not.toBeInTheDocument();
  });

  it("添加面板默认收起，收起时不残留任何输入框或选择框，可以展开再收起", () => {
    render(<Harness initial={{ require: [], exclude: [] }} onRow={vi.fn()} />);
    const expand = screen.getByRole("button", { name: t("anibtTags.openEditor") });
    expect(expand).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("button", { name: "CHS" })).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("radio")).not.toBeInTheDocument();

    fireEvent.click(expand);
    const collapse = screen.getByRole("button", { name: t("anibtTags.closeEditor") });
    expect(collapse).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("button", { name: "CHS" })).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();

    fireEvent.click(collapse);
    expect(screen.queryByRole("button", { name: "CHS" })).not.toBeInTheDocument();
  });

  it("已添加的标签可以移除后回到候选区，不渲染交换按钮", () => {
    const onRow = vi.fn();
    render(<Harness initial={{ require: ["language:CHS"], exclude: ["codec:HEVC"] }} onRow={onRow} />);
    // Only the two remove buttons plus the "+ Add tags" toggle button exist (no switch buttons).
    expect(screen.getAllByRole("button")).toHaveLength(3);

    fireEvent.click(screen.getByRole("button", { name: t("anibtTags.removeAria", { tag: tagFullLabel("codec:HEVC") }) }));
    expect(onRow).toHaveBeenLastCalledWith({ require: ["language:CHS"], exclude: [] });
    fireEvent.click(screen.getByRole("button", { name: t("anibtTags.openEditor") }));
    expect(screen.getByRole("button", { name: "HEVC" })).toBeInTheDocument();
  });

  it("仅从预设与已观测标签中选择，不渲染自定义输入框", () => {
    const onRow = vi.fn();
    render(<Harness initial={{ require: [], exclude: [] }} extraTags={["tag:DV"]} onRow={onRow} />);
    fireEvent.click(screen.getByRole("button", { name: t("anibtTags.openEditor") }));
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /DV/ }));
    expect(onRow).toHaveBeenLastCalledWith({ require: ["tag:DV"], exclude: [] });
  });

  it("只读时只显示已添加的标签，没有切换、移除和添加", () => {
    render(<TagRowEditor vocabulary={ANIBT_ROW_VOCABULARY} value={{ require: ["language:CHS"], exclude: ["codec:HEVC"] }} readOnly />);
    expect(entries()).toEqual([`require:${"CHS"}`, "exclude:HEVC"]);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.queryByRole("radio")).not.toBeInTheDocument();
  });

  it("标题解析行：固定词表直接通过候选点选添加，已添加的值不再出现在候选区", () => {
    const vocabulary = titleRowVocabulary({
      implementation: "reltitle@test",
      config_digest: "test",
      dimensions: [{ dimension: "resolution", values: ["1080p", "2160p"] }],
    });
    const onChange = vi.fn();
    render(<TagRowEditor vocabulary={vocabulary} value={{ require: ["resolution:1080p"], exclude: [] }} onChange={onChange} />);
    fireEvent.click(screen.getByRole("button", { name: t("anibtTags.openEditor") }));
    expect(screen.getByRole("button", { name: "2160p" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "1080p" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("radio", { name: t("anibtTags.role.exclude") }));
    fireEvent.click(screen.getByRole("button", { name: "2160p" }));
    expect(onChange).toHaveBeenLastCalledWith({ require: ["resolution:1080p"], exclude: ["resolution:2160p"] });
  });
});
