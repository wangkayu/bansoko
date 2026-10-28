package filemaintenance

import (
	"context"
	"testing"
)

func TestSubscriptionScoresCannotChangeProductionReplacementQuality(t *testing.T) {
	for _, tc := range []struct {
		name, resolution string
		score            int
		verdict          string
	}{
		{"high_score_lower_quality", "720p", 100, "keep"},
		{"high_score_equal_quality", "1080p", 100, "keep"},
		{"low_score_better_quality", "2160p", 1, "replace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openFrozenTestDB(t)
			defer db.Close()
			if _, err := db.Exec(`UPDATE anime_business_configs SET admission_mode='single_copy',quality_profile='{"version":"profile-v1","items":[{"key":"release.resolution","ranks":[["2160p"],["1080p"],["720p"]]}]}',replacement_version=replacement_version+1 WHERE managed_anime_id='anime-1'`); err != nil {
				t.Fatal(err)
			}
			old := seedFilePlacement(t, db, "anime-1", "sub-1", "quality-old", []string{"tv:e1"}, Facts{Resolution: "1080p", Group: "Incumbent"}, "2026-07-01T00:00:00Z")
			candidate := seedFilePlacement(t, db, "anime-1", "sub-1", "quality-new", []string{"tv:e1"}, Facts{Resolution: tc.resolution, Group: "Candidate"}, "2026-08-01T00:00:00Z")
			for _, entry := range []struct {
				id, group string
				score     int
			}{{"quality-old", "incumbent", 50}, {"quality-new", "candidate", tc.score}} {
				if _, err := db.Exec(`UPDATE rss_sources SET source_kind='provider_group',provider='anibt',provider_entry_ref='123',provider_group_ref=$2,discovery_snapshot_version='v1' WHERE id=$1`, "src-"+entry.id, entry.group); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO provider_group_selections(id,subscription_id,provider,group_ref,display_name,selection_mode,effective_selected,effective_score,available) VALUES($1,'sub-1','anibt',$2,$2,'auto',TRUE,$3,TRUE)`, entry.id, entry.group, entry.score); err != nil {
					t.Fatal(err)
				}
			}
			view, err := evaluateSingle(NewReplacementDecisionService(db).EvaluateServer(context.Background(), EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "quality-new"}))
			if err != nil {
				t.Fatal(err)
			}
			if string(view.Verdict) != tc.verdict {
				t.Fatalf("score=%d resolution=%s verdict=%s want=%s", tc.score, tc.resolution, view.Verdict, tc.verdict)
			}
			if view.Pools == nil || len(*view.Pools) != 1 {
				t.Fatalf("missing pool: %+v", view)
			}
			pool := (*view.Pools)[0]
			want := old
			if tc.verdict == "replace" {
				want = candidate
			}
			if pool.Winner == nil || pool.Winner.PlacementId != want {
				t.Fatalf("winner=%+v want=%s", pool.Winner, want)
			}
			if tc.verdict == "keep" && pool.Losers != nil && len(*pool.Losers) != 0 {
				t.Fatalf("inferior/tied candidate created losers: %+v", pool.Losers)
			}
		})
	}
}
