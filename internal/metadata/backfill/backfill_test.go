package backfill

import (
	"testing"

	"bansoko/internal/metadata/matcher"
)

// The owner media kind is backfilled once, from the TMDB tuple this merge
// binds, and only while it is unset. A stored kind is kept whatever the matched
// tuple, and a merge that binds no tuple never writes the kind.
func TestMergeBackfillsTheOwnerMediaKindOnlyWhenBindingWhileUnset(t *testing.T) {
	str := func(v string) *string { return &v }
	num := func(v int) *int { return &v }
	movieMatch := &matcher.MatchResult{TmdbWorkID: str("t1"), TmdbKind: str("movie"), TmdbSeason: num(0)}

	plan := Merge(Stored{}, movieMatch)
	if !plan.NewTMDB || !plan.NewMediaKind || plan.Merged.MediaKind == nil || *plan.Merged.MediaKind != "movie" {
		t.Fatalf("binding with an unset kind = %+v, want the movie kind backfilled", plan)
	}
	if !plan.Changed() {
		t.Fatal("a backfilled kind is a change")
	}

	plan = Merge(Stored{MediaKind: str("tv")}, movieMatch)
	if !plan.NewTMDB || plan.NewMediaKind || plan.Merged.MediaKind == nil || *plan.Merged.MediaKind != "tv" {
		t.Fatalf("binding with a stored kind = %+v, want the stored tv kept", plan)
	}

	// A bound tuple is never replaced by a match, so the kind is not touched.
	bound := Stored{TMDBWorkID: str("t0"), TMDBKind: str("tv"), TMDBSeason: num(1), MediaKind: str("tv")}
	plan = Merge(bound, movieMatch)
	if plan.NewTMDB || plan.NewMediaKind || *plan.Merged.MediaKind != "tv" {
		t.Fatalf("re-match on a bound tuple = %+v, want the kind untouched", plan)
	}

	// A match without a complete TMDB tuple binds nothing and writes no kind.
	plan = Merge(Stored{}, &matcher.MatchResult{BgmID: str("b1")})
	if plan.NewTMDB || plan.NewMediaKind || plan.Merged.MediaKind != nil {
		t.Fatalf("BGM-only match = %+v, want the kind left unset", plan)
	}
}
