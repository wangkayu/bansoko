import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { InstanceCard } from "./InstanceCard";
import type { CatalogEntryView } from "../../../lib/providers/viewmodels";
import { RecordLinkContext } from "../../../shell/recordLinkContext";
import { t } from "../../../lib/i18n";

// The subscribed poster card contract (after the card redesign): a 2:3 poster fills the card and
//   the compact bottom strip holds only the title, the progress summary (imported n/total, by count
//   only) plus broadcast day, and the thin progress bar. The whole card has one open target (the
//   poster's click area) and no separate configure button. The top-left state pill appears only
//   when attention is needed (not yet started, stopped, an RSS warning straight to the RSS tab, a
//   metadata hold); the top-right menu: start subscription (only when not yet started) / stop,
//   start / execution records / files / unsubscribe. A subscribed instance offers no one-shot
//   browse entry. Bangumi / TMDB, the original name and external links moved to the config dialog's
//   title area. Hiding is not on the instance card.

const base: CatalogEntryView = {
  providerEntryRef: "mikan:e1",
  source: "mikan",
  title: "测试番剧",
  season: "2026-summer",
  broadcastDay: "sat",
  metadataMatch: { matched: false },
  airDate: "2026-07-04",
  lastUpdated: "2026-07-18",
  subscriptionState: "active",
  managedAnimeRef: "ma1",
  subscriptionVersion: 1,
  metadataHold: { held: false, missing: [] },
};

function renderCard(entry: CatalogEntryView, overrides: Partial<Parameters<typeof InstanceCard>[0]> = {}) {
  return render(<InstanceCard entry={entry} onOpenConfig={() => {}} {...overrides} />);
}

describe("InstanceCard 信息条", () => {
  it("显示标题与按数量的进度摘要，不带放送日（墙上的日分隔给出，B4）；订阅中不出状态胶囊", () => {
    renderCard({ ...base, episodeProgress: { episodes: [1, 2, 3], total: 12 } });
    expect(screen.getByRole("heading", { name: "测试番剧" })).toBeInTheDocument();
    expect(screen.getByText(t("grid.progress.summary", { stored: 3, total: 12 }))).toBeInTheDocument();
    expect(screen.getByText(t("grid.progress.summaryShort", { stored: 3, total: 12 }))).toBeInTheDocument();
    expect(screen.queryByText(t("grid.weekday.sat"))).not.toBeInTheDocument();
    expect(screen.queryByText(t("anime.state.active"))).not.toBeInTheDocument();
  });

  it("无 total 时只写已入库集数，不渲染进度条", () => {
    renderCard({ ...base, episodeProgress: { episodes: [1, 2, 3, 4, 5] } });
    expect(screen.getByText(t("grid.progress.summaryNoTotal", { stored: 5 }))).toBeInTheDocument();
    expect(screen.queryByRole("img", { name: /入库进度/ })).not.toBeInTheDocument();
  });

  it("首集入库前显示等待首集", () => {
    renderCard({ ...base, episodeProgress: { episodes: [], total: 12 } });
    expect(screen.getByText(t("grid.progress.waiting"))).toBeInTheDocument();
  });

  it("进度条是一条按数量填充的连续条，不标缺集", () => {
    const { container } = renderCard({ ...base, episodeProgress: { episodes: [1, 2, 4, 5], total: 16 } });
    const bar = screen.getByRole("img", {
      name: t("grid.card.progressAria", { label: t("grid.progress.summary", { stored: 4, total: 16 }) }),
    });
    expect(bar.querySelectorAll(".catalog-progress-fill")).toHaveLength(1);
    expect((bar.querySelector(".catalog-progress-fill") as HTMLElement).style.width).toBe("25%");
    expect(container.querySelector("[title]:not(.catalog-pw-open):not(.catalog-pw-progress-text):not(.catalog-pw-title-text)")).toBeNull();
  });

  it("订阅备注与标题不同时显示，用来区分同一番剧的多个订阅", () => {
    renderCard({ ...base, source: "custom", instanceLabel: "客厅版" });
    expect(screen.getByText("客厅版")).toBeInTheDocument();
  });

  it("Bangumi / TMDB、原名与外链不在卡面", () => {
    renderCard({
      ...base,
      originalTitle: "テスト",
      sourceUrl: "https://mikanani.me/Home/Bangumi/3701",
      metadataMatch: { matched: true, bgmId: "123", tmdbWorkId: "456" },
    });
    expect(screen.queryByText("Bangumi")).not.toBeInTheDocument();
    expect(screen.queryByText("TMDB")).not.toBeInTheDocument();
    expect(screen.queryByText("テスト")).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });
});

describe("InstanceCard 状态胶囊", () => {
  it("已停止时显示停止胶囊", () => {
    renderCard({ ...base, subscriptionState: "stopped" });
    expect(screen.getByText(t("anime.state.stopped"))).toBeInTheDocument();
  });

  it("未开始时显示未开始胶囊", () => {
    renderCard({ ...base, subscriptionState: "pending" });
    expect(screen.getByText(t("anime.state.pending"))).toBeInTheDocument();
  });

  it("元数据挂起时显示短形阻塞 chip，订阅中悬停给长形", () => {
    const { container } = renderCard({ ...base, subscriptionState: "active", metadataHold: { held: true, missing: ["bgm"] } });
    const chip = container.querySelector(".catalog-pw-chip-hold");
    expect(chip).toHaveTextContent(t("anime.metadata.missing", { names: "Bangumi" }));
    expect(chip).toHaveAttribute("title", t("anime.metadata.paused", { names: "Bangumi" }));
  });

  it("RSS 源异常：短文案胶囊，可访问名是完整原因", () => {
    renderCard({ ...base, rssHealth: "failed", rssHealthReason: "HTTP 502 Bad Gateway" });
    const chip = screen.getByRole("button", { name: "RSS 源异常，等待自动重试：HTTP 502 Bad Gateway" });
    expect(chip).toHaveTextContent(t("grid.card.rssFailedShort"));
  });

  it("RSS 源不稳定使用降级文案", () => {
    renderCard({ ...base, rssHealth: "degraded" });
    expect(screen.getByRole("button", { name: t("grid.card.rssDegraded") })).toHaveTextContent(t("grid.card.rssDegradedShort"));
  });

  it("RSS 胶囊打开配置的 RSS 页签", () => {
    const onOpenConfig = vi.fn();
    renderCard({ ...base, rssHealth: "failed" }, { onOpenConfig });
    fireEvent.click(screen.getByRole("button", { name: t("grid.card.rssFailed") }));
    expect(onOpenConfig).toHaveBeenCalledWith(expect.objectContaining({ title: "测试番剧" }), "rss");
  });
});

describe("InstanceCard 打开目标", () => {
  it("整卡只有一个打开目标；没有独立配置按钮，标题是纯文本", () => {
    const onOpenConfig = vi.fn();
    renderCard(base, { onOpenConfig });
    fireEvent.click(screen.getByRole("button", { name: t("grid.card.openConfig", { title: "测试番剧" }) }));
    expect(onOpenConfig).toHaveBeenCalledWith(base);
    expect(screen.queryByRole("button", { name: t("grid.card.configure") })).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "测试番剧" }).closest("button")).toBeNull();
  });

  it("不渲染隐藏入口", () => {
    renderCard(base);
    expect(screen.queryByRole("button", { name: t("grid.card.hide") })).not.toBeInTheDocument();
  });
});

async function openMenu(user: ReturnType<typeof userEvent.setup>, title = "测试番剧") {
  await user.click(screen.getByRole("button", { name: t("grid.card.moreFor", { title }) }));
}

describe("InstanceCard ⋯ 菜单", () => {
  it("active 显示停止项，点击触发 onToggleState", async () => {
    const user = userEvent.setup();
    const onToggleState = vi.fn();
    renderCard(base, { onToggleState });
    await openMenu(user);
    await user.click(await screen.findByRole("menuitem", { name: t("grid.card.pause") }));
    expect(onToggleState).toHaveBeenCalledWith(base);
  });

  it("stopped 切换为启动项", async () => {
    const user = userEvent.setup();
    const onToggleState = vi.fn();
    const stoppedEntry = { ...base, subscriptionState: "stopped" as const };
    renderCard(stoppedEntry, { onToggleState });
    await openMenu(user);
    await user.click(await screen.findByRole("menuitem", { name: t("grid.card.start") }));
    expect(onToggleState).toHaveBeenCalledWith(stoppedEntry);
  });

  it("busy 时停止/结束订阅项禁用", async () => {
    const user = userEvent.setup();
    renderCard(base, { busy: true, onToggleState: () => {}, onUnsubscribe: () => {} });
    await openMenu(user);
    expect(await screen.findByRole("menuitem", { name: t("grid.card.pause") })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("menuitem", { name: t("catalog.endConfirm") })).toHaveAttribute("aria-disabled", "true");
  });

  it("点结束订阅触发 onUnsubscribe", async () => {
    const user = userEvent.setup();
    const onUnsubscribe = vi.fn();
    renderCard(base, { onUnsubscribe });
    await openMenu(user);
    await user.click(await screen.findByRole("menuitem", { name: t("catalog.endConfirm") }));
    expect(onUnsubscribe).toHaveBeenCalledWith(base);
  });

  it("pending 显示开始订阅项，点击触发 onToggleState", async () => {
    const user = userEvent.setup();
    const onToggleState = vi.fn();
    const pendingEntry = { ...base, subscriptionState: "pending" as const };
    renderCard(pendingEntry, { onToggleState });
    await openMenu(user);
    await user.click(await screen.findByRole("menuitem", { name: t("grid.card.begin") }));
    expect(onToggleState).toHaveBeenCalledWith(pendingEntry);
    expect(screen.queryByRole("menuitem", { name: t("grid.card.start") })).not.toBeInTheDocument();
  });

  it("已订阅实例的菜单没有浏览种子", async () => {
    const user = userEvent.setup();
    renderCard(base, { onToggleState: () => {} });
    await openMenu(user);
    await screen.findByRole("menuitem", { name: t("grid.card.pause") });
    expect(screen.queryByRole("menuitem", { name: t("oneshot.action") })).not.toBeInTheDocument();
  });

  it("复制项触发 onDuplicate；busy 时禁用", async () => {
    const user = userEvent.setup();
    const onDuplicate = vi.fn();
    const { unmount } = renderCard(base, { onDuplicate });
    await openMenu(user);
    await user.click(await screen.findByRole("menuitem", { name: t("grid.card.duplicate") }));
    expect(onDuplicate).toHaveBeenCalledWith(base);
    unmount();
    renderCard(base, { onDuplicate, busy: true });
    await openMenu(user);
    expect(await screen.findByRole("menuitem", { name: t("grid.card.duplicate") })).toHaveAttribute("aria-disabled", "true");
  });

  it("文件项打开单番文件弹窗；不传 onOpenFiles 不渲染", async () => {
    const user = userEvent.setup();
    const onOpenFiles = vi.fn();
    const { unmount } = renderCard(base, { onOpenFiles });
    await openMenu(user);
    await user.click(await screen.findByRole("menuitem", { name: t("grid.card.files") }));
    expect(onOpenFiles).toHaveBeenCalledWith(base);
    unmount();
    renderCard(base, { onToggleState: vi.fn() });
    await openMenu(user);
    await screen.findByRole("menuitem", { name: t("grid.card.pause") });
    expect(screen.queryByRole("menuitem", { name: t("grid.card.files") })).not.toBeInTheDocument();
  });

  it("执行记录按该番剧打开全局执行记录", async () => {
    const user = userEvent.setup();
    const openRecord = vi.fn();
    render(
      <RecordLinkContext.Provider value={openRecord}>
        <InstanceCard entry={base} onOpenConfig={() => {}} />
      </RecordLinkContext.Provider>,
    );
    await openMenu(user);
    await user.click(await screen.findByRole("menuitem", { name: t("records.title") }));
    expect(openRecord).toHaveBeenCalledWith({ managedAnimeID: "ma1" });
  });
});
