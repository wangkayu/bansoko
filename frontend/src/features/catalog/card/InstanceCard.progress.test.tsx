import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { InstanceCard } from "./InstanceCard";
import type { CatalogEntryView } from "../../../lib/providers/viewmodels";
import { t } from "../../../lib/i18n";

describe("card progress calculation", () => {
  it.each([false, true])("shows movie import progress with done=%s", (done) => {
    const entry: CatalogEntryView = {
      providerEntryRef: "mikan:movie", source: "mikan", title: "测试电影",
      subscriptionState: "active", managedAnimeRef: "movie-1",
      metadataMatch: { matched: false, mediaKind: "movie" },
      episodeProgress: { episodes: done ? [1] : [], total: 1, done },
    };
    const html = renderToStaticMarkup(<InstanceCard entry={entry} onOpenConfig={() => undefined} />);
    expect(html).toContain(done ? "1/1" : t("grid.progress.waitingForImport"));
    expect(html).toContain(done ? "width:100%" : "width:0%");
  });

  it.each([undefined, false])("measures episode scans with done=%s", (done) => {
    const episodes = Array.from({ length: 1000 }, (_, index) => index + 1);
    const iterate = vi.spyOn(episodes, Symbol.iterator);
    const entry: CatalogEntryView = {
      providerEntryRef: "mikan:sample", source: "mikan", title: "测试番剧",
      subscriptionState: "active", managedAnimeRef: "sample-1",
      metadataMatch: { matched: false },
      episodeProgress: { episodes, total: 1200, done },
    };
    const html = renderToStaticMarkup(<InstanceCard entry={entry} onOpenConfig={() => undefined} />);
    expect(iterate).toHaveBeenCalledTimes(1);
    expect(html).toContain("1000/1200");
    iterate.mockRestore();
  });
});
