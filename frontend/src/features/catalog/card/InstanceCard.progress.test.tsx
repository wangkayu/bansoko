import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { InstanceCard } from "./InstanceCard";
import type { CatalogEntryView } from "../../../lib/providers/viewmodels";

describe("card progress calculation", () => {
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
