import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { BrowseCard } from "./BrowseCard";
import type { CatalogEntryView } from "../../../lib/providers/viewmodels";
import { t } from "../../../lib/i18n";

// A retired custom preset: the preset still carries a retired owner, and re-subscribing would
//   replay the already-consumed candidate, so the card shows only "retired" and has no open entry
//   (the explicit path is delete preset). Provider candidate cards carry no subscription fields at
//   all, so a retired subscription cannot affect them (see catalogProvider.test.ts).

const retiredAssociation: CatalogEntryView = {
  providerEntryRef: "custom:cand-1",
  source: "custom",
  candidateId: "cand-1",
  title: "重新订阅番剧",
  season: "2026-summer",
  broadcastDay: "sat",
  metadataMatch: { matched: false },
  subscriptionState: "stopped",
  managedAnimeRef: "ma-retired",
  subscriptionRef: "sub-retired",
  subscriptionVersion: 3,
  lifecycleState: "retired",
  lifecycleEpoch: 2,
  subscribedGroups: ["SampleGroup"],
};

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

describe("BrowseCard 已结束的自定义预设", () => {
  it("显示已结束标记，没有打开入口", () => {
    renderCard(retiredAssociation);
    expect(screen.getByText(t("anime.state.ended"))).toBeInTheDocument();
    expect(screen.queryByText(t("grid.card.subscribed"))).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t("grid.card.openConfig", { title: "重新订阅番剧" }) })).not.toBeInTheDocument();
  });
});
