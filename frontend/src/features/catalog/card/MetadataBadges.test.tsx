import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { CatalogEntryView } from "../../../lib/providers/viewmodels";
import { MetadataBadges } from "./MetadataBadges";

function entry(metadataMatch: CatalogEntryView["metadataMatch"]): CatalogEntryView {
  return { providerEntryRef: "mikan:4012", source: "mikan", title: "测试番剧", metadataMatch };
}

// The TMDB value names the season for a series and the media kind for a movie, linking to the matching TMDB page.
describe("MetadataBadges", () => {
  it("shows the season of a TV binding", () => {
    render(<MetadataBadges entry={entry({ matched: true, bgmId: "1", tmdbWorkId: "100", tmdbMediaKind: "tv", tmdbSeason: 2 })} />);
    expect(screen.getByRole("link", { name: "100 · S2" })).toHaveAttribute("href", "https://www.themoviedb.org/tv/100");
  });

  it("shows Movie instead of a season for a movie binding", () => {
    render(<MetadataBadges entry={entry({ matched: true, bgmId: "1", tmdbWorkId: "200", tmdbMediaKind: "movie", tmdbSeason: 0 })} />);
    expect(screen.getByRole("link", { name: "200 · Movie" })).toHaveAttribute("href", "https://www.themoviedb.org/movie/200");
  });
});
