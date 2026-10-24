import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { LocalDataCleanupDialog } from "./LocalDataCleanupDialog";
import type { CatalogEntryView } from "../../lib/providers/viewmodels";
import { t } from "../../lib/i18n";

// The standalone confirm dialog for deleting local records must be explicit: only local records go, no qB torrents/data and no library files.

const entry: CatalogEntryView = {
  providerEntryRef: "mikan:e1",
  source: "mikan",
  title: "退役番剧",
  metadataMatch: { matched: false },
  subscriptionState: "stopped",
  managedAnimeRef: "ma-retired",
  subscriptionRef: "sub-retired",
  subscriptionVersion: 4,
  lifecycleState: "retired",
  lifecycleEpoch: 2,
  metadataHold: { held: false, missing: [] },
};

describe("LocalDataCleanupDialog", () => {
  it("文案明确不删除 qB 数据与媒体库文件", () => {
    render(<LocalDataCleanupDialog entry={entry} onOpenChange={() => {}} onConfirm={() => {}} />);
    expect(screen.getByText(t("catalog.deleteLocalTitle", { title: "退役番剧" }))).toBeInTheDocument();
    expect(screen.getByText(t("grid.confirm.deleteLocalLine2"))).toBeInTheDocument();
    expect(screen.getByText(t("grid.confirm.deleteLocalLine1"))).toBeInTheDocument();
  });

  it("确认触发 onConfirm，取消触发 onOpenChange(false)", () => {
    const onConfirm = vi.fn();
    const onOpenChange = vi.fn();
    render(<LocalDataCleanupDialog entry={entry} onOpenChange={onOpenChange} onConfirm={onConfirm} />);
    screen.getByRole("button", { name: t("catalog.deleteLocal") }).click();
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });
});
