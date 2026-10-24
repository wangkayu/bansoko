package application

import (
	"testing"

	"bansoko/internal/metadata/matcher"
)

// The server match is the form's TMDB match with an automatic confirm: it fills
// the owner year from the matched TMDB work only while the year is empty.
func TestMergeMetadataFillsTheOwnerYearOnlyWhenEmpty(t *testing.T) {
	workID, kind, season := "265010", "tv", 1
	year := 2024
	match := &matcher.MatchResult{TmdbWorkID: &workID, TmdbKind: &kind, TmdbSeason: &season, TmdbYear: &year}

	if got := mergeMetadata(MetadataMatchMetadata{}, match).Year; got == nil || *got != 2024 {
		t.Fatalf("empty year = %v, want the matched 2024", got)
	}

	stored := 2019
	if got := mergeMetadata(MetadataMatchMetadata{Year: &stored}, match).Year; got == nil || *got != 2019 {
		t.Fatalf("stored year = %v, want it kept at 2019", got)
	}

	noYear := &matcher.MatchResult{TmdbWorkID: &workID, TmdbKind: &kind, TmdbSeason: &season}
	if got := mergeMetadata(MetadataMatchMetadata{}, noYear).Year; got != nil {
		t.Fatalf("no candidate year = %v, want NULL", *got)
	}
}

// The season follows the same fill-if-empty rule: the match writes the owner
// season from the TMDB tuple it newly binds, only while the season is unset and
// only for TV. A stored season is never overwritten, and a re-match against an
// already-bound tuple never touches the season.
func TestMergeMetadataFillsTheOwnerSeasonOnlyWhenEmpty(t *testing.T) {
	workID, tv, movie, season := "265010", "tv", "movie", 2
	match := &matcher.MatchResult{TmdbWorkID: &workID, TmdbKind: &tv, TmdbSeason: &season}

	if got := mergeMetadata(MetadataMatchMetadata{}, match).Season; got == nil || *got != 2 {
		t.Fatalf("unset season = %v, want the matched 2", got)
	}

	stored := 5
	if got := mergeMetadata(MetadataMatchMetadata{Season: &stored}, match).Season; got == nil || *got != 5 {
		t.Fatalf("stored season = %v, want it kept at 5", got)
	}

	movieSeason := 0
	movieMatch := &matcher.MatchResult{TmdbWorkID: &workID, TmdbKind: &movie, TmdbSeason: &movieSeason}
	if got := mergeMetadata(MetadataMatchMetadata{}, movieMatch).Season; got != nil {
		t.Fatalf("movie match season = %v, want NULL", *got)
	}

	bound := MetadataMatchMetadata{TMDB: &MetadataMatchTMDB{WorkID: "1", MediaKind: "tv", Season: 3}}
	if got := mergeMetadata(bound, match).Season; got != nil {
		t.Fatalf("re-match on a bound tuple season = %v, want it left unset", *got)
	}
}
