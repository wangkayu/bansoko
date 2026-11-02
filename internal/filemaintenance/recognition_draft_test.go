package filemaintenance

import (
	"testing"

	"bansoko/internal/namingvars"
)

// A draft replaces every value it carries and clears the ones it leaves empty,
// so a render never mixes the operation's copy with the stored row.
func TestApplyRecognitionAnimeDraftReplacesTheStoredFacts(t *testing.T) {
	storedYear, storedSeason := int64(2019), int64(2)
	evidence := namingvars.AnimeEvidence{
		AnimeTitle: "Stored", AnimeTitleOverride: "Stored override", AnimeProviderTitle: "Provider",
		AnimeYear: &storedYear, AnimeSeason: &storedSeason,
		BgmID: "bgm-1", BgmTitle: "BGM", TmdbID: "tmdb-1", TmdbKind: "tv", TmdbSeason: &storedSeason,
	}
	year := 2024
	applyRecognitionAnimeDraft(&evidence, &RecognitionAnimeDraft{
		Title: "Edited", MediaKind: "movie", Year: &year,
		BGM: true, BGMWorkID: "bgm-9",
		TMDB: true, TMDBWorkID: "99", TMDBMediaKind: "movie", TMDBSeason: 3,
	})
	if evidence.AnimeTitle != "Edited" || evidence.AnimeTitleOverride != "Edited" {
		t.Fatalf("title = %q / override = %q", evidence.AnimeTitle, evidence.AnimeTitleOverride)
	}
	if evidence.AnimeYear == nil || *evidence.AnimeYear != 2024 {
		t.Fatalf("year = %v", evidence.AnimeYear)
	}
	if evidence.BgmID != "bgm-9" || evidence.TmdbID != "99" || evidence.TmdbKind != "movie" {
		t.Fatalf("bindings = %+v", evidence)
	}
	if evidence.TmdbSeason == nil || *evidence.TmdbSeason != 3 {
		t.Fatalf("tmdb season = %v", evidence.TmdbSeason)
	}
	// The owner season is the operation's own field; the draft does not carry it.
	if evidence.AnimeSeason == nil || *evidence.AnimeSeason != 2 {
		t.Fatalf("owner season = %v", evidence.AnimeSeason)
	}
}

func TestApplyRecognitionAnimeDraftClearsWhatItLeavesEmpty(t *testing.T) {
	storedYear, storedSeason := int64(2019), int64(1)
	evidence := namingvars.AnimeEvidence{
		AnimeTitle: "Stored", AnimeYear: &storedYear, AnimeSeason: &storedSeason,
		BgmID: "bgm-1", TmdbID: "tmdb-1", TmdbKind: "tv", TmdbSeason: &storedSeason,
	}
	applyRecognitionAnimeDraft(&evidence, &RecognitionAnimeDraft{Title: "Edited", MediaKind: "tv"})
	if evidence.AnimeYear != nil || evidence.BgmID != "" || evidence.TmdbID != "" || evidence.TmdbKind != "" || evidence.TmdbSeason != nil {
		t.Fatalf("evidence = %+v", evidence)
	}
}
