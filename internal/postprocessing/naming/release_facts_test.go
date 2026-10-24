package naming

import (
	"testing"

	"bansoko/internal/namingvars"
	"bansoko/internal/templatefacts"
)

func TestWithFrozenReleaseFactsReadsTheVarsFactObject(t *testing.T) {
	season := int64(3)
	tmdbYear, bgmYear := int64(2020), int64(2019)
	input := namingvars.BuildInput(namingvars.ReleaseEvidence{
		AnimeEvidence: namingvars.AnimeEvidence{AnimeID: "a1", AnimeYear: &tmdbYear, AnimeTitle: "Show", AnimeTitleOverride: "Over", AnimeProviderTitle: "Provider", BgmID: "1", BgmTitle: "B", BgmYear: &bgmYear, TmdbID: "2", TmdbKind: "tv", TmdbSeason: &season, TmdbTitleCN: "CN", TmdbOriginalTitle: "O", TmdbYear: &tmdbYear},
		ReleaseTitle:  "Show - 01", Files: []namingvars.File{{Name: "a", Path: "a", Size: 7}}, HashV2: "h2",
	})
	ctx := NamingContext{}
	ctx.Anime.TMDB.MediaKind = "movie"
	ctx = WithFrozenReleaseFacts(ctx, input.Facts)
	facts := UnifiedFacts(ctx)
	// The frozen object's own titles are read back, so a render that only sees
	// the frozen input derives the same title the acceptance did.
	if facts.Anime.ID != "a1" || ctx.Anime.Title != "Show" || ctx.Anime.TitleOverride != "Over" || ctx.Anime.ProviderTitle != "Provider" {
		t.Fatalf("anime facts = %+v", facts.Anime)
	}
	if facts.Bgm.ID != "1" || facts.Bgm.Title != "B" || facts.Tmdb.ID != "2" || facts.Tmdb.TitleCN != "CN" || facts.Tmdb.OriginalTitle != "O" {
		t.Fatalf("facts = %+v %+v", facts.Bgm, facts.Tmdb)
	}
	if facts.Anime.Title != "Over" || facts.Anime.TitleSource != templatefacts.TitleSourceOverride {
		t.Fatalf("effective title = %q/%q", facts.Anime.Title, facts.Anime.TitleSource)
	}
	// anime.year is the frozen owner-edited year, read back as is.
	if facts.Anime.Year != "2020" {
		t.Fatalf("year = %q", facts.Anime.Year)
	}
	// The media kind stays the caller's frozen parser identity (here the movie
	// pin); the bound season comes from the frozen Vars input, exactly like the
	// artifact render reads it from the frozen snapshot.
	if facts.Tmdb.Kind != "movie" {
		t.Fatalf("media kind taken from the Vars facts: %q", facts.Tmdb.Kind)
	}
	if !ctx.Anime.TMDB.SeasonPresent || ctx.Anime.TMDB.Season != 3 {
		t.Fatalf("bound season = %d/%v, want 3/true", ctx.Anime.TMDB.Season, ctx.Anime.TMDB.SeasonPresent)
	}
	// The season-derived values resolve from the restored binding through the
	// same registry the artifact render uses.
	resolved := DefaultRegistry().ResolveAll(ResolverInput{Facts: ctx})
	if resolved.Context.Derived.SeasonFolder != "Season 03" {
		t.Fatalf("season folder = %q, want Season 03", resolved.Context.Derived.SeasonFolder)
	}
	if facts.Torrent.Size == nil || *facts.Torrent.Size != 7 || *facts.Torrent.FileCount != 1 || facts.Torrent.HashV2 != "h2" || facts.Torrent.HashV1 != "" {
		t.Fatalf("torrent = %+v", facts.Torrent)
	}
	if got := WithFrozenReleaseFacts(NamingContext{}, nil); got.Torrent.Size != nil || got.Anime.BGM.WorkID != "" {
		t.Fatal("nil facts produced values")
	}
}
