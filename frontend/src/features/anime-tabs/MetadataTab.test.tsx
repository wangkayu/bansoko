import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { AnimeDraft, TitleSuggestion } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import type { DraftTabHost } from "./host";
import { MetadataTab } from "./MetadataTab";

// The draft title row: its candidates arrive from the draft's RSS read; a badge click writes the
// text as the draft's title, like typing it and pressing save.

function draftFixture(): AnimeDraft {
  return {
    origin: "anibt",
    origin_entry_ref: "entry-1",
    title: "测试番",
    title_override: null,
    instance_label: null,
    metadata: { bgm: null, tmdb: null, total_episodes: null },
    subgroups: { max_rss: 3, entries: [] },
    rules: { sections: {} },
    rss_sources: [],
  } as unknown as AnimeDraft;
}

function renderDraftTab(suggestions: TitleSuggestion[] | undefined) {
  const onDraftChange = vi.fn();
  const host: DraftTabHost = {
    mode: "draft",
    draft: draftFixture(),
    onDraftChange,
    previewable: true,
    feedSnapshots: [],
    onFeedSnapshotsChange: () => {},
    observedGroups: [],
    onDiscovered: () => {},
  };
  render(<MetadataTab host={host} draftTitleSuggestions={suggestions} />);
  return onDraftChange;
}

describe("MetadataTab 草稿标题候选", () => {
  it("编辑时显示候选（隐藏现值），点击写入草稿标题并退出编辑", () => {
    const onDraftChange = renderDraftTab([
      { source: "tmdb_cn", text: "候选甲" },
      { source: "provider", text: "测试番" },
    ]);
    const region = screen.getByRole("region", { name: t("tabs.metadata.title") });
    expect(within(region).queryByRole("group")).not.toBeInTheDocument();
    fireEvent.click(within(region).getByRole("button", { name: t("tabs.metadata.editTitleAria") }));
    const badges = within(within(region).getByRole("group", { name: t("tabs.metadata.suggestionsAria") })).getAllByRole("button");
    expect(badges).toHaveLength(1);

    fireEvent.click(badges[0]!);
    expect(onDraftChange).toHaveBeenCalledTimes(1);
    expect(onDraftChange.mock.calls[0]![0]).toMatchObject({ title: "候选甲", title_override: null });
    expect(within(region).queryByRole("textbox")).not.toBeInTheDocument();
  });

  it("没有候选（草稿尚未读取）时编辑行不显示徽章", () => {
    renderDraftTab(undefined);
    const region = screen.getByRole("region", { name: t("tabs.metadata.title") });
    fireEvent.click(within(region).getByRole("button", { name: t("tabs.metadata.editTitleAria") }));
    expect(within(region).queryByRole("group")).not.toBeInTheDocument();
  });
});
