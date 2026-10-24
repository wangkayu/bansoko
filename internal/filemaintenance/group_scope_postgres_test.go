package filemaintenance

import (
	"bansoko/internal/apperrors"
	"context"
	"errors"
	"testing"
)

// Expected scope is fixed independently of the policy resolver. Missing
// candidate ownership is placement-missing; present provider sources fail closed.
func TestRetentionProviderScopeGateFailsClosed(t *testing.T) {
	for _, state := range []string{"missing-selection", "unavailable", "excluded", "selected"} {
		t.Run(state, func(t *testing.T) {
			db := openFrozenTestDB(t)
			defer db.Close()
			seedFilePlacement(t, db, "anime-1", "sub-1", "scope-candidate", []string{"tv:s1:e1"}, Facts{Resolution: "1080p", Group: "GroupA"}, "2026-08-01T00:00:00Z")
			if _, err := db.Exec(`UPDATE rss_sources SET source_kind='provider_group',provider='anibt',provider_entry_ref='123',provider_group_ref='alpha',discovery_snapshot_version='v1' WHERE id='src-scope-candidate'`); err != nil {
				t.Fatal(err)
			}
			if state != "missing-selection" {
				if _, err := db.Exec(`INSERT INTO provider_group_selections(id,subscription_id,provider,group_ref,display_name,selection_mode,effective_selected,effective_score,count_in_limit,available) VALUES('scope','sub-1','anibt','alpha','Alpha','auto',$1,50,TRUE,$2)`, state != "excluded", state != "unavailable"); err != nil {
					t.Fatal(err)
				}
			}
			_, err := evaluateSingle(NewReplacementDecisionService(db).EvaluateServer(context.Background(), EvaluateRequest{AnimeID: "anime-1", CandidateRelease: "scope-candidate"}))
			if state == "selected" {
				if err != nil {
					t.Fatalf("selected provider rejected: %v", err)
				}
				return
			}
			var business *apperrors.Business
			if !errors.As(err, &business) || business.Code != "GROUP_SCOPE_NOT_SELECTED" {
				t.Fatalf("%s: expected GROUP_SCOPE_NOT_SELECTED, got %v", state, err)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM replacement_decisions`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("out-of-scope candidate created %d decisions", count)
			}
		})
	}
}
