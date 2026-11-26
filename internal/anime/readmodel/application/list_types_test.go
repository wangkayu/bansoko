package application

import (
	"database/sql"
	"testing"
)

// TestEpisodeProgressDoneIsCountBased pins the rule: the
// progress compares the number of imported episodes with the total and never
// maps episode numbers to slots.
func TestEpisodeProgressDoneIsCountBased(t *testing.T) {
	twelve := 12
	three := 3
	cases := []struct {
		name     string
		episodes string
		total    *int
		catalog  *int
		wantDone bool
		wantNil  bool
	}{
		{name: "high episode number alone is not done", episodes: "12", total: &twelve, wantDone: false},
		{name: "gapped but enough episodes is done", episodes: "1,2,5", total: &three, wantDone: true},
		{name: "fewer episodes than total", episodes: "1,2", total: &three, wantDone: false},
		{name: "duplicates count once", episodes: "1,1,2", total: &three, wantDone: false},
		{name: "catalog total is the fallback", episodes: "1,2,3", catalog: &three, wantDone: true},
		{name: "no total is never done", episodes: "1,2,3", wantDone: false},
		{name: "no episodes is null", episodes: "", total: &three, wantNil: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildApplicationEpisodeProgress(sql.NullString{String: tc.episodes, Valid: tc.episodes != ""}, nil, tc.total, tc.catalog)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("progress = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("progress is nil")
			}
			if got.Done != tc.wantDone {
				t.Fatalf("done = %v, want %v", got.Done, tc.wantDone)
			}
		})
	}
}

func TestMovieEpisodeProgressUsesFixedTotal(t *testing.T) {
	movie := "movie"
	storedTotal, catalogTotal := 12, 24
	for _, tc := range []struct {
		name     string
		coverage sql.NullString
		imported int
	}{
		{name: "waiting"},
		{name: "imported", coverage: sql.NullString{String: "1", Valid: true}, imported: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := buildApplicationEpisodeProgress(tc.coverage, &movie, &storedTotal, &catalogTotal)
			if got == nil || got.Total == nil || *got.Total != 1 || got.Episodes == nil || len(got.Episodes) != tc.imported || got.Done != (tc.imported == 1) {
				t.Fatalf("progress = %+v, want %d/1 with a non-nil episodes array", got, tc.imported)
			}
			if tc.imported == 1 && got.Episodes[0] != 1 {
				t.Fatalf("episodes = %v, want [1]", got.Episodes)
			}
		})
	}
}
