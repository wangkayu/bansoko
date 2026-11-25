package filemaintenance

import (
	"testing"

	"bansoko/internal/postprocessing/naming"
)

// A draft replaces every value it carries and clears the ones it leaves empty,
// so a render never mixes the operation's copy with the stored row.
func TestApplyRecognitionAnimeDraftReplacesTheStoredFacts(t *testing.T) {
	storedYear, storedSeason := 2019, 2
	facts := naming.AnimeNamingFacts{
		Title: "Stored", TitleOverride: "Stored override", ProviderTitle: "Provider",
		Year: &storedYear, Season: &storedSeason,
		BGM:  naming.BGMNamingFacts{WorkID: "bgm-1", Title: "BGM"},
		TMDB: naming.TMDBNamingFacts{WorkID: "tmdb-1", MediaKind: "tv", Season: storedSeason, SeasonPresent: true},
	}
	year := 2024
	applyRecognitionAnimeDraft(&facts, &RecognitionAnimeDraft{
		Title: "Edited", MediaKind: "movie", Year: &year,
		BGM: true, BGMWorkID: "bgm-9",
		TMDB: true, TMDBWorkID: "99", TMDBMediaKind: "movie", TMDBSeason: 3,
	})
	if facts.Title != "Edited" || facts.TitleOverride != "Edited" {
		t.Fatalf("title = %q / override = %q", facts.Title, facts.TitleOverride)
	}
	if facts.Year == nil || *facts.Year != 2024 {
		t.Fatalf("year = %v", facts.Year)
	}
	if facts.BGM.WorkID != "bgm-9" || facts.TMDB.WorkID != "99" || facts.TMDB.MediaKind != "movie" {
		t.Fatalf("bindings = %+v", facts)
	}
	if !facts.TMDB.SeasonPresent || facts.TMDB.Season != 3 {
		t.Fatalf("tmdb season = %+v", facts.TMDB)
	}
	// The owner season is the operation's own field; the draft does not carry it.
	if facts.Season == nil || *facts.Season != 2 {
		t.Fatalf("owner season = %v", facts.Season)
	}
}

func TestApplyRecognitionAnimeDraftClearsWhatItLeavesEmpty(t *testing.T) {
	storedYear, storedSeason := 2019, 1
	facts := naming.AnimeNamingFacts{
		Title: "Stored", Year: &storedYear, Season: &storedSeason,
		BGM:  naming.BGMNamingFacts{WorkID: "bgm-1"},
		TMDB: naming.TMDBNamingFacts{WorkID: "tmdb-1", MediaKind: "tv", Season: storedSeason, SeasonPresent: true},
	}
	applyRecognitionAnimeDraft(&facts, &RecognitionAnimeDraft{Title: "Edited", MediaKind: "tv"})
	if facts.Year != nil || facts.BGM.WorkID != "" || facts.TMDB.WorkID != "" || facts.TMDB.MediaKind != "" || facts.TMDB.SeasonPresent {
		t.Fatalf("facts = %+v", facts)
	}
}
