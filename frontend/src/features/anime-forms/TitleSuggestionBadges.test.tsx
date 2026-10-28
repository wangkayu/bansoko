import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { TitleSuggestion } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import { TitleSuggestionBadges } from "./TitleSuggestionBadges";

const all: TitleSuggestion[] = [
  { source: "tmdb_cn", text: "甲" },
  { source: "tmdb_original", text: "  " },
  { source: "bgm_cn", text: "甲" },
  { source: "bgm", text: "Kou" },
  { source: "provider", text: "现标题" },
];

describe("TitleSuggestionBadges", () => {
  it("去掉空值与现值，同文只留第一个来源，保持顺序", () => {
    render(<TitleSuggestionBadges suggestions={all} current="现标题" onPick={() => {}} />);
    const badges = screen.getAllByRole("button");
    expect(badges).toHaveLength(2);
    expect(badges[0]).toHaveTextContent(`${t("tabs.metadata.suggestionSource.tmdb_cn")}甲`);
    expect(badges[1]).toHaveTextContent(`${t("tabs.metadata.suggestionSource.bgm")}Kou`);
  });

  it("点击把文本交给调用方；没有可显示候选时不渲染", () => {
    const onPick = vi.fn();
    const { rerender } = render(<TitleSuggestionBadges suggestions={all} current="现标题" onPick={onPick} />);
    fireEvent.click(screen.getByRole("button", { name: new RegExp(`${t("tabs.metadata.suggestionSource.bgm")}\\s*Kou`) }));
    expect(onPick).toHaveBeenCalledWith("Kou");

    rerender(<TitleSuggestionBadges suggestions={[{ source: "provider", text: "现标题" }]} current="现标题" onPick={onPick} />);
    expect(screen.queryByRole("group")).not.toBeInTheDocument();
  });
});
