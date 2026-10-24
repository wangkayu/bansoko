package workitems

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPGCancelledClaimCannotAdmitNewEffect(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	source := createSourceOwner(t, db, "cancel-admission")
	createRefreshItem(t, store, source, "cancel-admission", 3)
	claim, err := store.ClaimDue(context.Background(), "before-cancel", []string{KindRSSRefresh}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RequestCancel(context.Background(), claim, "test durable cancellation"); err != nil {
		t.Fatal(err)
	}
	if err := store.AdmitManagedAnimeEffect(context.Background(), claim); err == nil {
		t.Error("cancelled claim passed pre-effect admission")
	}
	if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, claim.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimDue(context.Background(), "after-cancel", []string{KindRSSRefresh}, time.Minute); !errors.Is(err, ErrNoDueWork) {
		t.Errorf("cancel restarted business claim: %v", err)
	}
	var cancelled bool
	if err := db.QueryRow(`SELECT cancel_requested FROM work_item_history WHERE id=$1`, claim.ID).Scan(&cancelled); err != nil {
		t.Fatal(err)
	}
	if !cancelled {
		t.Error("cancel evidence cleared")
	}
}

func TestPGExpiredFinalAttemptCannotRestartBusiness(t *testing.T) {
	db := openTestDB(t)
	store := newTestStore(t, db)
	source := createSourceOwner(t, db, "final-expiry")
	createRefreshItem(t, store, source, "final-expiry", 1)
	claim, err := store.ClaimDue(context.Background(), "final", []string{KindRSSRefresh}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, claim.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := store.ClaimDue(context.Background(), "reclaim", []string{KindRSSRefresh}, time.Minute); !errors.Is(err, ErrNoDueWork) {
			t.Errorf("final attempt reclaimed into business again: %v", err)
		}
	}
}

func TestPGOneShotUnknownFinalAttemptRetainsLedger(t *testing.T) {
	f := newOneShotSettlementFixture(t, "unknown-final")
	before := readOneShotSettlementMarker(t, f.db, f.markerID)
	f.claim.Attempt = f.claim.MaxAttempts
	if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET attempt=max_attempts WHERE id=$1`, f.itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RetryOneShot(context.Background(), f.claim, f.lease, DispositionAmbiguous, "EFFECT_UNCERTAIN", "unknown effect", time.Second, nil); err != nil {
		t.Fatal(err)
	}
	after := readOneShotSettlementMarker(t, f.db, f.markerID)
	if after.count != before.count || after.count != 1 {
		t.Fatalf("unknown ledger consumed: before=%+v after=%+v", before, after)
	}
	var state, reason string
	if err := f.db.QueryRow(`SELECT state,recovery_reason FROM work_item_history WHERE id=$1`, f.itemID).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != StateLeased || reason != "effect_unknown" {
		t.Fatalf("unknown incorrectly terminal: %s %s", state, reason)
	}
}
