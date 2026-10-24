import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { BrowseCard } from "./BrowseCard";
import { setSessionTokenProvider } from "../../../lib/api/client";
import { resetAuthedImageCache } from "../../../lib/useAuthedImage";
import type { CatalogEntryView } from "../../../lib/providers/viewmodels";
import { t } from "../../../lib/i18n";

// The browse card contract (after the card redesign; shared by the catalog browse area and the
//   global search): a 2:3 poster, one line of title + original name + one caption line + top-left
//   corner badges (broadcast day / score / release state). Complex state (progress, RSS signals,
//   metadata matching) is never rendered. No full-width buttons: the whole-card click area opens
//   the config (a provider entry IS the draft); browse is a small icon button on the info strip;
//   hide and delete preset live in the top-right menu. A subscription adds only a "subscribed"
//   badge and never shows subscription state.
// The click area, browse and the menu are siblings; no card-level role=button.

const base: CatalogEntryView = {
  providerEntryRef: "mikan:e1",
  source: "mikan",
  title: "测试番剧",
  season: "2026-summer",
  broadcastDay: "sat",
  metadataMatch: { matched: false },
  airDate: "2026-07-04",
  lastUpdated: "2026-07-18",
};

const subscribedBase: CatalogEntryView = { ...base, subscribed: true };

function renderCard(entry: CatalogEntryView, overrides: Partial<Parameters<typeof BrowseCard>[0]> = {}) {
  return render(
    <BrowseCard
      entry={entry}
      onHide={() => {}}
      onOpenConfig={() => {}}
      {...overrides}
    />,
  );
}

describe("BrowseCard 信息预算", () => {
  it("显示标题、放送日角标与 caption（Mikan 更新文字）", () => {
    renderCard(base);
    expect(screen.getByText("测试番剧")).toBeInTheDocument();
    expect(screen.getByText("周六")).toBeInTheDocument();
    expect(screen.getByText(/更新 2026-07-18/)).toBeInTheDocument();
  });

  it("AniBT 标明的种子数进 caption；未知时不显示", () => {
    const anibt: CatalogEntryView = { ...base, source: "anibt", lastUpdated: undefined };
    const { unmount } = renderCard({ ...anibt, releaseState: "has_releases", releaseCount: 7 });
    expect(screen.getByText(t("grid.card.releaseCount", { n: 7 }))).toBeInTheDocument();
    unmount();
    renderCard(anibt);
    expect(screen.queryByText(t("grid.card.releaseCount", { n: 7 }))).not.toBeInTheDocument();
    expect(screen.queryByText(t("grid.card.noReleases"))).not.toBeInTheDocument();
  });
  it("评分角标在有 score 时显示", () => {
    renderCard({ ...base, score: 8.2 });
    expect(screen.getByText("8.2")).toBeInTheDocument();
  });

  it("浏览区不渲染复杂状态：无进度、无 RSS 信号、无 Bangumi / TMDB", () => {
    renderCard({
      ...subscribedBase,
      metadataHold: { held: false, missing: [] },
      subscribedGroups: ["SampleGroup", "SampleSecondary"],
      episodeProgress: { episodes: [1, 2, 3], total: 13 },
      rssHealth: "failed",
    });
    expect(screen.queryByText("Bangumi")).not.toBeInTheDocument();
    expect(screen.queryByText("TMDB")).not.toBeInTheDocument();
    expect(screen.queryByText(/已入库/)).not.toBeInTheDocument();
    expect(screen.queryByText(/RSS/)).not.toBeInTheDocument();
    expect(screen.queryByText("Mikan")).not.toBeInTheDocument();
    // The caption stays a site fact; it never becomes the subscribed group count.
    expect(screen.queryByText(/SampleGroup|字幕组/)).not.toBeInTheDocument();
    expect(screen.getByText(/更新 2026-07-18/)).toBeInTheDocument();
  });

  it("未订阅时不渲染状态胶囊", () => {
    const { container } = renderCard(base);
    expect(container.querySelector(".catalog-pw-chip")).not.toBeInTheDocument();
  });

  it("有订阅时左上只显示「已订阅」，没有订阅状态", () => {
    const { container } = renderCard(subscribedBase);
    expect(screen.getByText(t("grid.card.subscribed"))).toBeInTheDocument();
    expect(container.querySelectorAll(".catalog-pw-chip")).toHaveLength(1);
    for (const state of ["pending", "active", "stopped", "ended"] as const) {
      expect(screen.queryByText(t(`anime.state.${state}`))).not.toBeInTheDocument();
    }
  });
});

async function openMenu(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: t("grid.card.moreFor", { title: "测试番剧" }) }));
}

describe("BrowseCard 操作", () => {
  it("没有整宽按钮：点卡片打开配置（未订阅即草稿），浏览是信息条上的小图标，隐藏在 ⋯", async () => {
    const user = userEvent.setup();
    const onOpenConfig = vi.fn();
    const onBrowseSeeds = vi.fn();
    const onHide = vi.fn();
    renderCard(base, { onOpenConfig, onBrowseSeeds, onHide });
    expect(screen.queryByRole("button", { name: t("anime.subscribe") })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("grid.card.configure") })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("grid.card.openConfig", { title: "测试番剧" }) }));
    expect(onOpenConfig).toHaveBeenCalledWith(base);
    const browse = screen.getByRole("button", { name: t("oneshot.actionFor", { title: "测试番剧" }) });
    // An icon button: no visible text; the name comes from the aria-label.
    expect(browse).toHaveTextContent("");
    expect(browse.querySelector("svg")).not.toBeNull();
    fireEvent.click(browse);
    expect(onBrowseSeeds).toHaveBeenCalledWith(base);
    expect(onOpenConfig).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("button", { name: t("grid.card.hide") })).not.toBeInTheDocument();
    await openMenu(user);
    await user.click(await screen.findByRole("menuitem", { name: t("grid.card.hide") }));
    expect(onHide).toHaveBeenCalledWith(base);
  });

  it("有订阅：点卡片与未订阅一样打开候选，浏览照旧在信息条上，隐藏照旧在 ⋯", async () => {
    const user = userEvent.setup();
    const onOpenConfig = vi.fn();
    const onBrowseSeeds = vi.fn();
    renderCard(subscribedBase, { onOpenConfig, onBrowseSeeds });
    expect(screen.queryByRole("button", { name: t("grid.card.configure") })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: t("grid.card.openConfig", { title: "测试番剧" }) }));
    expect(onOpenConfig).toHaveBeenCalledWith(subscribedBase);
    fireEvent.click(screen.getByRole("button", { name: t("oneshot.actionFor", { title: "测试番剧" }) }));
    expect(onBrowseSeeds).toHaveBeenCalledWith(subscribedBase);
    await openMenu(user);
    expect(screen.queryByRole("menuitem", { name: t("oneshot.action") })).not.toBeInTheDocument();
    // Hide uses the EyeOff icon, not a trash can.
    const block = await screen.findByRole("menuitem", { name: t("grid.card.hide") });
    expect(block.querySelector("svg.lucide-eye-off")).not.toBeNull();
    expect(block.querySelector("svg.lucide-trash-2")).toBeNull();
  });

  it("键盘：卡片、浏览、⋯ 是三个独立的 Tab 停靠点", async () => {
    const user = userEvent.setup();
    renderCard(base, { onBrowseSeeds: () => {} });
    await user.tab();
    expect(screen.getByRole("button", { name: t("grid.card.openConfig", { title: "测试番剧" }) })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("button", { name: t("grid.card.moreFor", { title: "测试番剧" }) })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("button", { name: t("oneshot.actionFor", { title: "测试番剧" }) })).toHaveFocus();
  });

  it("不传 onHide 且无其他菜单项时不渲染 ⋯", () => {
    renderCard(base, { onHide: undefined });
    expect(screen.queryByRole("button", { name: t("grid.card.moreFor", { title: "测试番剧" }) })).not.toBeInTheDocument();
  });
});

describe("BrowseCard 主点击区与可达性", () => {
  it("封面按钮承担打开配置，键盘原生可达；卡片本体无 role=button", () => {
    const onOpenConfig = vi.fn();
    const { container } = renderCard(base, { onOpenConfig });
    const cover = screen.getByRole("button", { name: t("grid.card.openConfig", { title: "测试番剧" }) });
    fireEvent.click(cover);
    expect(onOpenConfig).toHaveBeenCalledWith(base);
    expect(container.querySelector('[role="button"]')).toBeNull();
  });

  it("卡片不渲染日志入口", () => {
    renderCard(subscribedBase);
    expect(screen.queryByRole("button", { name: /日志/ })).not.toBeInTheDocument();
  });
});

describe("BrowseCard 自定义预设", () => {
  const customEntry: CatalogEntryView = {
    ...base,
    source: "custom",
    providerEntryRef: "custom:c1",
    candidateId: "cand-1",
    groupName: "SampleSecondary",
    rssUrl: "https://example.com/feed.xml",
    instanceLabel: "客厅版",
  };

  it("根元素是 article，主点击区为打开配置按钮", () => {
    const { container } = renderCard(customEntry);
    expect(container.querySelector("article")).not.toBeNull();
    expect(screen.getByRole("button", { name: t("grid.card.openConfig", { title: "测试番剧" }) })).toBeInTheDocument();
  });

  it("custom 卡 caption 显示 instance_label（客厅版）", () => {
    renderCard(customEntry);
    expect(screen.getByText(/客厅版/)).toBeInTheDocument();
  });

  it("有 originalTitle 时在标题下渲染原名", () => {
    renderCard({ ...customEntry, originalTitle: "テストバンギャ" });
    expect(screen.getByText("テストバンギャ", { exact: true })).toBeInTheDocument();
  });

  it("未订阅 custom 预设：⋯ 里是删除预设，没有隐藏", async () => {
    const user = userEvent.setup();
    const onDeleteCandidate = vi.fn();
    renderCard(customEntry, { onDeleteCandidate });
    await openMenu(user);
    expect(screen.queryByRole("menuitem", { name: t("grid.card.hide") })).not.toBeInTheDocument();
    await user.click(await screen.findByRole("menuitem", { name: t("grid.card.deletePreset") }));
    expect(onDeleteCandidate).toHaveBeenCalledWith(customEntry);
  });
});

// "Browse seeds" is a read-only entry alongside subscribing: it opens the one-shot dialog, writes no
//   subscription and never enters the config.
describe("BrowseCard 浏览种子", () => {
  it("点浏览种子触发 onBrowseSeeds，与打开配置互不干扰", () => {
    const onBrowseSeeds = vi.fn();
    const onOpenConfig = vi.fn();
    renderCard(base, { onBrowseSeeds, onOpenConfig });

    fireEvent.click(screen.getByRole("button", { name: t("oneshot.actionFor", { title: "测试番剧" }) }));
    expect(onBrowseSeeds).toHaveBeenCalledWith(base);
    expect(onOpenConfig).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: t("grid.card.openConfig", { title: "测试番剧" }) }));
    expect(onOpenConfig).toHaveBeenCalledWith(base);
  });

  it("不传 onBrowseSeeds 时不渲染该入口", () => {
    renderCard(base);
    expect(screen.queryByRole("button", { name: t("oneshot.actionFor", { title: "测试番剧" }) })).not.toBeInTheDocument();
  });

});

// Without a poster or after a poster load failure: a brand-tinted gradient with the title's first
//   character as placeholder, not a plain grey block.
describe("BrowseCard 海报占位", () => {
  it("无海报时显示标题首字符占位", () => {
    const { container } = renderCard({ ...base, posterUrl: undefined });
    expect(container.querySelector("img")).not.toBeInTheDocument();
    expect(screen.getByText("测")).toBeInTheDocument();
  });

  it("第三方 URL 不会被当成海报加载，直接显示首字符占位", () => {
    const { container } = renderCard({ ...base, posterUrl: "https://example.test/poster.jpg" });
    expect(container.querySelector("img")).not.toBeInTheDocument();
    expect(screen.getByText("测")).toBeInTheDocument();
  });
});

// The own /api/v1 poster proxy needs a Bearer session, and a native <img> src receives no bytes at
//   all beyond the 401: the image must be fetched as a blob through the session, then rendered via
//   objectURL. (Testing the shared Poster atom here once is enough; InstanceCard reuses the same
//   implementation.)
describe("BrowseCard 自家 API 海报", () => {
  beforeEach(() => {
    let seq = 0;
    URL.createObjectURL = vi.fn(() => `blob:poster-${++seq}`);
    URL.revokeObjectURL = vi.fn();
    setSessionTokenProvider(() => "t-1");
  });

  afterEach(() => {
    resetAuthedImageCache();
    vi.unstubAllGlobals();
    setSessionTokenProvider(() => "");
  });

  it("经会话 fetch 携带 token，成功后以 objectURL 渲染", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response("jpeg-bytes", { status: 200, headers: { "Content-Type": "image/jpeg" } }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { container } = renderCard({ ...base, posterUrl: "/api/v1/images/poster/583729" });
    await waitFor(() => {
      expect(container.querySelector("img")?.getAttribute("src")).toBe("blob:poster-1");
    });
    const [calledUrl, calledInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(calledUrl).toBe("/api/v1/images/poster/583729");
    expect(new Headers(calledInit.headers).get("Authorization")).toBe("Bearer t-1");
  });

  it("会话 fetch 失败时回退到首字符占位", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(null, { status: 404 })));
    const { container } = renderCard({ ...base, posterUrl: "/api/v1/images/poster/583729" });
    await waitFor(() => {
      expect(screen.getByText("测")).toBeInTheDocument();
    });
    expect(container.querySelector("img")).not.toBeInTheDocument();
  });
});

describe("BrowseCard 暂无种子遮罩", () => {
  it("no_releases 加遮罩与短标签，不显示 0 个种子，仍可打开配置与浏览", async () => {
    const user = userEvent.setup();
    const onOpenConfig = vi.fn();
    const onBrowseSeeds = vi.fn();
    const { container } = renderCard({ ...base, source: "anibt", releaseState: "no_releases", releaseCount: 0 }, { onOpenConfig, onBrowseSeeds });
    expect(container.firstElementChild).toHaveClass("browse-card-no-releases");
    expect(screen.getByText(t("grid.card.noReleases"))).toBeInTheDocument();
    expect(screen.queryByText(t("grid.card.releaseCount", { n: 0 }))).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: t("grid.card.openConfig", { title: "测试番剧" }) }));
    expect(onOpenConfig).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole("button", { name: t("oneshot.actionFor", { title: "测试番剧" }) }));
    expect(onBrowseSeeds).toHaveBeenCalledTimes(1);
  });

  it("已有种子或未知时不加遮罩", () => {
    const { container, unmount } = renderCard({ ...base, releaseState: "has_releases" });
    expect(container.firstElementChild).not.toHaveClass("browse-card-no-releases");
    unmount();
    const unknown = renderCard(base);
    expect(unknown.container.firstElementChild).not.toHaveClass("browse-card-no-releases");
  });
});
