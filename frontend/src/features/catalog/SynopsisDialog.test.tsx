import { describe, expect, it, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { SynopsisDialog } from "./SynopsisDialog";
import { splitSynopsis } from "./synopsisText";
import type { CatalogEntryView } from "../../lib/providers/viewmodels";
import { t } from "../../lib/i18n";

const api = vi.hoisted(() => ({ read: vi.fn() }));

vi.mock("../../lib/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../lib/api/client")>()),
  readProviderEntrySynopsis: api.read,
}));

const entry: CatalogEntryView = {
  providerEntryRef: "301",
  source: "mikan",
  title: "测试番剧",
  metadataMatch: { matched: false },
};

function renderDialog(value: Partial<Parameters<typeof SynopsisDialog>[0]> = {}, data: unknown = {}) {
  api.read.mockResolvedValue(data);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SynopsisDialog
        entry={entry}
        provider="mikan"
        onRequestClose={() => {}}
        {...value}
      />
    </QueryClientProvider>,
  );
}

const mikanData = {
  provider: "mikan",
  provider_entry_ref: "301",
  source_url: "https://mikan.example/Home/Bangumi/301",
  synopsis: "一段简介",
  episodes: 13,
  weekday: "wed",
  season: null,
  tags: [],
  official_site: "https://official.example/",
  bgm_url: "https://bgm.tv/subject/84873",
};

afterEach(() => {
  api.read.mockReset();
});

describe("splitSynopsis", () => {
  it("只在标记两侧都有内容时分页", () => {
    expect(splitSynopsis("前文\n[简介原文]\n后文")).toEqual({ summary: "前文", original: "后文" });
    expect(splitSynopsis("前文【简介原文】后文")).toEqual({ summary: "前文", original: "后文" });
    expect(splitSynopsis("[简介原文]后文")).toBeNull();
    expect(splitSynopsis("前文[简介原文]")).toBeNull();
    expect(splitSynopsis("没有标记的整段")).toBeNull();
    expect(splitSynopsis(null)).toBeNull();
  });
});

describe("SynopsisDialog Mikan", () => {
  it("按来源页事实渲染星期与集数，不显示标签、评分或季", async () => {
    renderDialog({}, mikanData);
    expect(await screen.findByText("一段简介")).toBeInTheDocument();
    expect(screen.getByText("周三")).toBeInTheDocument();
    expect(screen.getByText(t("grid.card.synopsis.episodes", { n: 13 }))).toBeInTheDocument();
    expect(screen.queryByText(t("grid.card.synopsis.score", { score: "8.0" }))).not.toBeInTheDocument();
    expect(screen.queryByText(t("grid.card.synopsis.formatTv"))).not.toBeInTheDocument();
  });

  it("没有简介时显示正常空状态", async () => {
    renderDialog({}, { ...mikanData, synopsis: null, episodes: null, weekday: null, official_site: null, bgm_url: null });
    expect(await screen.findByText(t("grid.card.synopsis.empty"))).toBeInTheDocument();
    expect(screen.getByText(t("grid.card.synopsis.emptyHint"))).toBeInTheDocument();
  });

  it("读取失败显示原因并可重试", async () => {
    const user = userEvent.setup();
    api.read.mockReset();
    api.read.mockRejectedValueOnce(new Error("boom")).mockResolvedValueOnce(mikanData);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <SynopsisDialog entry={entry} provider="mikan" onRequestClose={() => {}} />
      </QueryClientProvider>,
    );
    expect(await screen.findByText(t("grid.card.synopsis.failed"))).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: t("grid.card.synopsis.retry") }));
    expect(await screen.findByText("一段简介")).toBeInTheDocument();
    await waitFor(() => expect(api.read).toHaveBeenCalledTimes(2));
  });
});

describe("SynopsisDialog 分页", () => {
  it("含标记时默认显示简介，可切到简介原文", async () => {
    const user = userEvent.setup();
    renderDialog({}, { ...mikanData, synopsis: "中文简介\n[简介原文]\n日文原文" });
    expect(await screen.findByText("中文简介")).toBeInTheDocument();
    expect(screen.queryByText("日文原文")).not.toBeInTheDocument();
    await user.click(screen.getByRole("tab", { name: t("grid.card.synopsis.original") }));
    expect(screen.getByText("日文原文")).toBeInTheDocument();
  });

  it("标记一侧为空时整段显示，不出现页签", async () => {
    renderDialog({}, { ...mikanData, synopsis: "整段[简介原文]" });
    expect(await screen.findByText("整段[简介原文]")).toBeInTheDocument();
    expect(screen.queryByRole("tablist")).not.toBeInTheDocument();
  });
});

describe("SynopsisDialog AniBT", () => {
  it("显示原始标题、标签、形式、季、集数、评分与外部链接", async () => {
    renderDialog(
      {
        entry: { ...entry, source: "anibt", providerEntryRef: "505550", originalTitle: "DARK MACHINE", releaseFormat: "tv", score: 8 },
        provider: "anibt",
      },
      {
        ...mikanData,
        provider: "anibt",
        provider_entry_ref: "505550",
        season: "2026-fall",
        episodes: 14,
        tags: ["原创", "机战"],
        bgm_url: "https://bgm.tv/subject/505550",
      },
    );
    expect(await screen.findByText("一段简介")).toBeInTheDocument();
    expect(screen.getByText("DARK MACHINE")).toBeInTheDocument();
    expect(screen.getByText(t("grid.card.synopsis.formatTv"))).toBeInTheDocument();
    expect(screen.getByText("2026 秋")).toBeInTheDocument();
    expect(screen.getByText(t("grid.card.synopsis.episodes", { n: 14 }))).toBeInTheDocument();
    expect(screen.getByText(t("grid.card.synopsis.score", { score: "8.0" }))).toBeInTheDocument();
    expect(screen.getByText("原创")).toBeInTheDocument();
    expect(screen.getByText("机战")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: new RegExp(t("grid.card.synopsis.officialSite")) })).toHaveAttribute("href", "https://official.example/");
    expect(screen.getByRole("link", { name: new RegExp(t("grid.card.synopsis.bangumi")) })).toHaveAttribute("href", "https://bgm.tv/subject/505550");
  });
});
