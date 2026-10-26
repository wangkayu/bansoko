package application

import (
	providerdomain "bansoko/internal/provider/domain"
	selectiondomain "bansoko/internal/selection/domain"
	"context"
	"testing"
)

func TestDerivedNeverCanBeWithdrawnWithoutRestoreAuto(t *testing.T) {
	db := openDispatcherTestDB(t)
	insertProviderSubscription(t, db)
	ctx := context.Background()
	ref, _ := providerdomain.NewGroupRef(providerdomain.ProviderAniBT, "12345")
	decision := selectiondomain.GroupSelectionDecision{Group: selectiondomain.EffectiveProviderGroup{Ref: ref, Observed: true, Mode: selectiondomain.Never}, Outcome: selectiondomain.GroupNotSelected}
	if _, err := upsertProviderGroupSelection(ctx, db, "sub-1", "anibt", "12345", "Same Display", decision); err != nil {
		t.Fatal(err)
	}
	decision.Group.Mode = selectiondomain.Ranked
	decision.Group.Score = 80
	decision.Outcome = selectiondomain.GroupSelected
	if _, err := upsertProviderGroupSelection(ctx, db, "sub-1", "anibt", "12345", "Renamed Display", decision); err != nil {
		t.Fatal(err)
	}
	var mode, display string
	var selected bool
	var score int
	var manual any
	if err := db.QueryRow(`SELECT selection_mode,manual_selected,effective_selected,effective_score,display_name FROM provider_group_selections WHERE subscription_id='sub-1' AND provider='anibt' AND group_ref='12345'`).Scan(&mode, &manual, &selected, &score, &display); err != nil {
		t.Fatal(err)
	}
	if mode != "auto" || manual != nil || !selected || score != 80 || display != "Renamed Display" {
		t.Fatalf("derived policy remained sticky: %s %v %t %d %s", mode, manual, selected, score, display)
	}
	if _, err := db.Exec(`UPDATE provider_group_selections SET selection_mode='manual',manual_selected=FALSE,effective_selected=FALSE WHERE subscription_id='sub-1' AND provider='anibt' AND group_ref='12345'`); err != nil {
		t.Fatal(err)
	}
	if selected, err := upsertProviderGroupSelection(ctx, db, "sub-1", "anibt", "12345", "Same Display", decision); err != nil || selected {
		t.Fatalf("user exclusion lost: %t %v", selected, err)
	}
}
