import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { InstanceCard } from "./InstanceCard";
import type { CatalogEntryView } from "../../../lib/providers/viewmodels";
import { t } from "../../../lib/i18n";

// The retired (lifecycleState=retired) poster wall card contract: the card stays in the
//   "subscribed" list and clearly shows "retired"; the poster's click area is the view entry,
//   opening the same workspace (which the dialog puts into read-only mode per its lifecycle
//   capability). The card itself renders no mutation entries — stop/start and unsubscribe all
//   disappear; the menu keeps only execution records and the explicit "delete local records"
//   dangerous action. Read-only viewing is not repeated in the menu.
// Subscription.state is not widened to retired, and the card does not rewrite it either.

const retiredBase: CatalogEntryView = {
  providerEntryRef: "mikan:e1",
  source: "mikan",
  title: "退役番剧",
  season: "2026-summer",
  broadcastDay: "sat",
  metadataMatch: { matched: false },
  subscriptionState: "stopped",
  managedAnimeRef: "ma-retired",
  subscriptionRef: "sub-retired",
  subscriptionVersion: 4,
  lifecycleState: "retired",
  lifecycleEpoch: 2,
  metadataHold: { held: false, missing: [] },
};

function renderCard(entry: CatalogEntryView, overrides: Partial<Parameters<typeof InstanceCard>[0]> = {}) {
  return render(
    <InstanceCard
      entry={entry}
      onOpenConfig={() => {}}
      onToggleState={() => {}}
      onUnsubscribe={() => {}}
      {...overrides}
    />,
  );
}

describe("InstanceCard 已退役只读", () => {
  it("显示已退役标记而不是订阅状态", () => {
    renderCard(retiredBase);
    expect(screen.getByText(t("anime.state.ended"))).toBeInTheDocument();
    expect(screen.queryByText(t("anime.state.stopped"))).not.toBeInTheDocument();
    expect(screen.queryByText(t("anime.state.active"))).not.toBeInTheDocument();
  });

  it("历史事实仍可查看（标题、进度）", () => {
    renderCard(retiredBase);
    expect(screen.getByText("退役番剧")).toBeInTheDocument();
    expect(screen.getByText(t("grid.progress.waiting"))).toBeInTheDocument();
  });

  it("不渲染停止/启动、结束订阅两处 mutation 入口", () => {
    renderCard(retiredBase);
    expect(screen.queryByRole("button", { name: /停止/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /启动/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: new RegExp(t("catalog.endConfirm")) })).not.toBeInTheDocument();
    // The only open target is read-only viewing; "open config" does not appear.
    expect(screen.queryByRole("button", { name: t("grid.card.openConfig", { title: "退役番剧" }) })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: t("grid.card.viewFor", { title: "退役番剧" }) })).toBeInTheDocument();
  });

  it("⋯ 菜单不重复只读查看入口（海报点击已打开同一个 workspace）", async () => {
    const user = userEvent.setup();
    renderCard(retiredBase, { onCleanupLocalData: vi.fn() });
    await user.click(screen.getByRole("button", { name: t("grid.card.moreFor", { title: "退役番剧" }) }));
    await screen.findByRole("menuitem", { name: t("catalog.deleteLocal") });
    expect(screen.getAllByRole("menuitem")).toHaveLength(1);
  });

  it("海报点击区是只读查看入口（标题是纯文本）", () => {
    const onOpenConfig = vi.fn();
    renderCard(retiredBase, { onOpenConfig });
    screen.getByRole("button", { name: t("grid.card.viewFor", { title: "退役番剧" }) }).click();
    expect(onOpenConfig).toHaveBeenCalledWith(retiredBase);
  });

  it("active 生命周期仍保留停止/启动与结束订阅（对照，收在 ⋯ 菜单）", async () => {
    const user = userEvent.setup();
    renderCard({ ...retiredBase, lifecycleState: "active" });
    await user.click(screen.getByRole("button", { name: t("grid.card.moreFor", { title: "退役番剧" }) }));
    expect(await screen.findByRole("menuitem", { name: t("grid.card.start") })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: t("catalog.endConfirm") })).toBeInTheDocument();
  });

  it("提供 onCleanupLocalData 时菜单里渲染「删除本地记录」危险动作并回调", async () => {
    const user = userEvent.setup();
    const onCleanupLocalData = vi.fn();
    renderCard(retiredBase, { onCleanupLocalData });
    await user.click(screen.getByRole("button", { name: t("grid.card.moreFor", { title: "退役番剧" }) }));
    await user.click(await screen.findByRole("menuitem", { name: t("catalog.deleteLocal") }));
    expect(onCleanupLocalData).toHaveBeenCalledWith(retiredBase);
  });

  it("已结束的订阅仍有「文件」入口（弹窗内只读）", async () => {
    const user = userEvent.setup();
    const onOpenFiles = vi.fn();
    renderCard(retiredBase, { onOpenFiles, onCleanupLocalData: vi.fn() });
    await user.click(screen.getByRole("button", { name: t("grid.card.moreFor", { title: "退役番剧" }) }));
    await user.click(await screen.findByRole("menuitem", { name: t("grid.card.files") }));
    expect(onOpenFiles).toHaveBeenCalledWith(retiredBase);
  });

  it("已结束的订阅没有复制入口", async () => {
    const user = userEvent.setup();
    renderCard(retiredBase, { onDuplicate: vi.fn(), onCleanupLocalData: vi.fn() });
    await user.click(screen.getByRole("button", { name: t("grid.card.moreFor", { title: "退役番剧" }) }));
    await screen.findByRole("menuitem", { name: t("catalog.deleteLocal") });
    expect(screen.queryByRole("menuitem", { name: t("grid.card.duplicate") })).not.toBeInTheDocument();
  });

  it("未提供 onCleanupLocalData 时不渲染清理入口", () => {
    renderCard(retiredBase);
    expect(screen.queryByRole("button", { name: new RegExp(t("catalog.deleteLocal")) })).not.toBeInTheDocument();
  });

  it("active 实例不渲染清理入口", async () => {
    const user = userEvent.setup();
    const onCleanupLocalData = vi.fn();
    renderCard({ ...retiredBase, lifecycleState: "active" }, { onCleanupLocalData });
    await user.click(screen.getByRole("button", { name: t("grid.card.moreFor", { title: "退役番剧" }) }));
    await screen.findByRole("menuitem", { name: t("grid.card.start") });
    expect(screen.queryByRole("menuitem", { name: new RegExp(t("catalog.deleteLocal")) })).not.toBeInTheDocument();
  });
});
