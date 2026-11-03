import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowseCard } from "./BrowseCard";
import type { CatalogEntryView } from "../../../lib/providers/viewmodels";
import { t } from "../../../lib/i18n";

const api = vi.hoisted(() => ({ read: vi.fn() }));

vi.mock("../../../lib/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../lib/api/client")>()),
  readProviderEntrySynopsis: api.read,
}));

const base: CatalogEntryView = {
  providerEntryRef: "301",
  source: "mikan",
  title: "测试番剧",
  metadataMatch: { matched: false },
  lastUpdated: "2026-07-18",
};

function renderCard(entry: CatalogEntryView, onOpenConfig = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <BrowseCard entry={entry} onHide={() => {}} onOpenConfig={onOpenConfig} />
    </QueryClientProvider>,
  );
  return onOpenConfig;
}

afterEach(() => {
  api.read.mockReset();
});

describe("BrowseCard 简介入口", () => {
  it("来源条目显示位于 ⋯ 左侧的简介按钮", () => {
    renderCard(base);
    expect(screen.getByRole("button", { name: t("grid.card.viewSynopsisFor", { title: base.title }) })).toHaveClass("browse-card-synopsis");
  });

  it("AniBT 条目同样显示简介入口", () => {
    renderCard({ ...base, source: "anibt", providerEntryRef: "505550" });
    expect(screen.getByRole("button", { name: t("grid.card.viewSynopsisFor", { title: base.title }) })).toBeInTheDocument();
  });

  it("自定义预设没有可读的来源页，不显示入口", () => {
    renderCard({ ...base, source: "custom", providerEntryRef: "custom:1" });
    expect(screen.queryByRole("button", { name: t("grid.card.viewSynopsisFor", { title: base.title }) })).not.toBeInTheDocument();
  });

  it("点简介只打开阅读器，不触发打开配置", async () => {
    const user = userEvent.setup();
    api.read.mockResolvedValue({
      provider: "mikan",
      provider_entry_ref: "301",
      source_url: "https://mikan.example/Home/Bangumi/301",
      synopsis: "一段简介",
      episodes: 13,
      weekday: "wed",
      season: null,
      tags: [],
      official_site: null,
      bgm_url: null,
    });
    const onOpenConfig = renderCard(base);
    expect(api.read).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: t("grid.card.viewSynopsisFor", { title: base.title }) }));
    expect(await screen.findByText("一段简介")).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(onOpenConfig).not.toHaveBeenCalled();
  });
});
