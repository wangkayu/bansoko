package application

import (
	providerdomain "bansoko/internal/provider/domain"
	selectiondomain "bansoko/internal/selection/domain"
	"context"
	"testing"
)

func TestAutomaticRankingKeepsManualExceptionsOutOfMaxRSS(t *testing.T) {
	db := openDispatcherTestDB(t)
	insertProviderSubscription(t, db)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO provider_group_selections(id,subscription_id,provider,group_ref,display_name,selection_mode,manual_selected,effective_selected,effective_score) VALUES ('old','sub-1','anibt','old-id','Old','auto',NULL,TRUE,90),('manual','sub-1','anibt','manual-id','Manual','manual',TRUE,TRUE,0)`); err != nil {
		t.Fatal(err)
	}
	ref, _ := providerdomain.NewGroupRef(providerdomain.ProviderAniBT, "new-id")
	result := providerdomain.GroupReadResult{Provider: providerdomain.ProviderAniBT, CatalogEntryRef: "123", Groups: []providerdomain.GroupSnapshotEntry{{Ref: ref, DisplayName: "New"}}}
	policy := discoveryPolicy{MaxRSS: 1, ProviderDefaults: map[providerdomain.Kind]selectiondomain.ProviderDefaultPolicy{providerdomain.ProviderAniBT: {Mode: selectiondomain.Ranked, Score: 50}}}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	decisions, err := rankDiscoveryScopeTx(ctx, tx, "sub-1", "anibt", result, policy, false)
	if err != nil {
		t.Fatal(err)
	}
	selected := map[string]bool{}
	for _, decision := range decisions {
		selected[decision.Group.Ref.ExternalID] = decision.Outcome == selectiondomain.GroupSelected
	}
	// Equal default scores break ties by stable provider ID: new-id precedes old-id.
	if len(selected) != 3 || !selected["manual-id"] || !selected["new-id"] || selected["old-id"] {
		t.Fatalf("manual exception must not consume max_rss: scope=%v", selected)
	}
}
