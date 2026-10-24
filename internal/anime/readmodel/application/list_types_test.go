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
			got := buildApplicationEpisodeProgress(sql.NullString{String: tc.episodes, Valid: tc.episodes != ""}, tc.total, tc.catalog)
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
