package application

import (
	providerdomain "bansoko/internal/provider/domain"
	selectiondomain "bansoko/internal/selection/domain"
	"context"
	"testing"
)

func TestAuthoritativeAvailabilityIsAtomicAndManualIndependent(t *testing.T) {
	db := openDispatcherTestDB(t)
	insertProviderSubscription(t, db)
	ctx := context.Background()
	d := Dispatcher{DB: db}
	ref, _ := providerdomain.NewGroupRef(providerdomain.ProviderAniBT, "123")
	active := true
	result := providerdomain.GroupReadResult{Provider: providerdomain.ProviderAniBT, CatalogEntryRef: "123", Complete: true, CompletenessEvidence: "test-complete", Groups: []providerdomain.GroupSnapshotEntry{{Ref: ref, DisplayName: "Name", Active: &active}}}
	decisions := []selectiondomain.GroupSelectionDecision{{Group: selectiondomain.EffectiveProviderGroup{Ref: ref, Mode: selectiondomain.Ranked, Score: 80, CountInLimit: true}, Outcome: selectiondomain.GroupSelected}}
	if err := d.applyDiscoveryScope(ctx, "sub-1", "anibt", result, decisions, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE provider_group_selections SET selection_mode='manual',manual_selected=TRUE,effective_selected=TRUE WHERE subscription_id='sub-1'`); err != nil {
		t.Fatal(err)
	}
	result.Groups = nil
	result.Complete = false
	if err := d.applyDiscoveryScope(ctx, "sub-1", "anibt", result, nil, true); err == nil {
		t.Fatal("partial accepted")
	}
	assert := func(want bool, generation int64) {
		t.Helper()
		var available, manual bool
		var got int64
		if err := db.QueryRow(`SELECT available,manual_selected,availability_generation FROM provider_group_selections WHERE subscription_id='sub-1' AND group_ref='123'`).Scan(&available, &manual, &got); err != nil {
			t.Fatal(err)
		}
		if available != want || !manual || got != generation {
			t.Fatalf("availability/manual/generation=%t/%t/%d want=%t/true/%d", available, manual, got, want, generation)
		}
	}
	assert(true, 1)
	result.Complete = true
	if err := d.applyDiscoveryScope(ctx, "sub-1", "anibt", result, nil, false); err != nil {
		t.Fatal(err)
	}
	assert(true, 1) // automatic empty never retires the retained selection
	if err := d.applyDiscoveryScope(ctx, "sub-1", "anibt", result, nil, true); err != nil {
		t.Fatal(err)
	}
	assert(false, 3)
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM provider_group_selections WHERE subscription_id='sub-1'`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("history deleted: %d %v", rows, err)
	}
}
