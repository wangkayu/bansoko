package worker

import (
	"bansoko/internal/workitems"
	persistence "bansoko/internal/workitems/adapter/persistence/generated"
	"context"
	"testing"
	"time"
)

func TestRuntimeAdmissionNullableProjection(t *testing.T) {
	runtimeDB := openPGTestDB(t)
	store := pgStore(t, runtimeDB)
	createPGItem(t, runtimeDB, store, "nullable", "nullable", 5)
	ctx := context.Background()
	claim, err := store.ClaimDue(ctx, "nullable-worker", []string{workitems.KindRSSRefresh}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	queries := persistence.New(runtimeDB)
	params := persistence.AdmitManagedAnimeEffectParams{WorkItemID: claim.ID, FencingToken: claim.FencingToken}
	admitted, err := queries.AdmitManagedAnimeEffect(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.Valid {
		t.Fatalf("non-effect admission must remain NULL: %v", admitted)
	}
	if err := store.AdmitManagedAnimeEffect(ctx, claim); err != nil {
		t.Fatal(err)
	}
	params.EffectBearing = true
	admitted, err = queries.AdmitManagedAnimeEffect(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if !admitted.Valid || admitted.Time.IsZero() {
		t.Fatalf("effect admission must have a timestamp: %v", admitted)
	}
}
