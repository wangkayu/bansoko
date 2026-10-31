import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import type { AnimeDraft, TitleSuggestion } from "../../lib/api/client";
import { t } from "../../lib/i18n";
import type { DraftTabHost } from "./host";
import { boundTitleSuggestions, candidateTitleSuggestions } from "./candidateTitles";
import { MetadataTab } from "./MetadataTab";

// The draft title row: its candidates arrive from the draft's RSS read; a badge click writes the
// text as the draft's title, like typing it and pressing save. A work picked in the match dialog
// also offers the titles it carries right away.

const { matchedWork } = vi.hoisted(() => ({
  matchedWork: {
    provider: "tmdb" as const,
    provider_work_id: "265010",
    title: "星际邮差艾拉",
    original_title: "Stellar Postman Ella",
    media_kind: "tv" as const,
    details_status: "available" as const,
    seasons: [{ season_number: 2, name: "第二季", episode_count: 12, air_date: "2024-01-01", is_special: false }],
    suggested_season: 2,
    score: 1,
  },
}));

vi.mock("../../lib/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib/api/client")>()),
  searchMetadata: vi.fn(async () => ({ provider: "tmdb" as const, query: "", candidates: [matchedWork] })),
}));

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

describe("候选自带的标题", () => {
  it("TMDB 候选按 read model 的来源映射，手动 ID 与无标题候选不产生条目", () => {
    expect(
      candidateTitleSuggestions({
        provider: "tmdb",
        workId: "1",
        title: "中文名",
        originalTitle: "Original",
        detailsStatus: "available",
      }),
    ).toEqual([
      { source: "tmdb_cn", text: "中文名" },
      { source: "tmdb_original", text: "Original" },
    ]);
    expect(candidateTitleSuggestions({ provider: "bgm", workId: "2", title: "番名", detailsStatus: "available" })).toEqual([
      { source: "bgm_cn", text: "番名" },
    ]);
    expect(candidateTitleSuggestions({ provider: "tmdb", workId: "3", title: "手动 ID", detailsStatus: "not_applicable" })).toEqual([]);
    expect(
      candidateTitleSuggestions({ provider: "bgm", workId: "4", title: " ", originalTitle: " ", detailsStatus: "available" }),
    ).toEqual([]);
  });

  it("只列出仍然绑定着的作品的标题", () => {
    const matched = {
      "bgm:2": [{ source: "bgm_cn" as const, text: "番名" }],
      "tmdb:1": [{ source: "tmdb_cn" as const, text: "中文名" }],
    };
    expect(boundTitleSuggestions(matched, { bgm: null, tmdb: { work_id: "1", media_kind: "tv", season: 1 } })).toEqual([
      { source: "tmdb_cn", text: "中文名" },
    ]);
    expect(boundTitleSuggestions(matched, { bgm: { work_id: "2" }, tmdb: null })).toEqual([{ source: "bgm_cn", text: "番名" }]);
    expect(boundTitleSuggestions(matched, { bgm: null, tmdb: null })).toEqual([]);
  });

  it("选中 TMDB 候选后，其标题立即成为标题候选；清除绑定后消失", async () => {
    function Harness() {
      const [draft, setDraft] = useState<AnimeDraft>(draftFixture());
      const host: DraftTabHost = {
        mode: "draft",
        draft,
        onDraftChange: setDraft,
        previewable: true,
        feedSnapshots: [],
        onFeedSnapshotsChange: () => {},
        observedGroups: [],
        onDiscovered: () => {},
      };
      return <MetadataTab host={host} />;
    }
    render(<Harness />);

    fireEvent.click(screen.getByRole("button", { name: t("forms.matchProvider", { provider: t("forms.providerTmdb") }) }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.search") }));
    fireEvent.click(await within(dialog).findByRole("option", { name: /星际邮差艾拉/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: t("forms.confirmMatch") }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    const region = screen.getByRole("region", { name: t("tabs.metadata.title") });
    fireEvent.click(within(region).getByRole("button", { name: t("tabs.metadata.editTitleAria") }));
    const texts = within(within(region).getByRole("group", { name: t("tabs.metadata.suggestionsAria") }))
      .getAllByRole("button")
      .map((badge) => badge.textContent ?? "");
    expect(texts.some((text) => text.includes("星际邮差艾拉"))).toBe(true);
    expect(texts.some((text) => text.includes("Stellar Postman Ella"))).toBe(true);

    // Cancelling the edit, clearing the binding drops the batch again.
    fireEvent.click(within(region).getByRole("button", { name: t("act.cancel") }));
    fireEvent.click(screen.getByRole("button", { name: t("forms.clearAria", { label: t("forms.tmdb") }) }));
    fireEvent.click(within(region).getByRole("button", { name: t("tabs.metadata.editTitleAria") }));
    expect(within(region).queryByRole("group")).not.toBeInTheDocument();
  });
});
