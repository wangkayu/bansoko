package application

import (
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
	"context"
	"errors"
	"testing"
	"time"
)

func TestPGScanRecoveryRejectsChangedGenerationAtomically(t *testing.T) {
	db, store := openReconTestDB(t)
	seedReconInstance(t, db, "anime-recovery", "sub-recovery", "active", nil)
	service, err := NewService(db, store, &fakeScanner{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := service.SubmitScan(ctx, ScanSubmitInput{ManagedAnimeID: "anime-recovery", TreeTop: stringPtr("/library")}); err != nil {
		t.Fatal(err)
	}
	c, err := store.ClaimDue(ctx, "before", []string{workitems.KindReconciliationScan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BlockRecovery(ctx, c, "effect_unknown"); err != nil {
		t.Fatal(err)
	}
	fixtureDB := disposabledb.Owner(t)
	if _, err := fixtureDB.Exec(`UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	c, err = store.ClaimRecovery(ctx, []string{c.Kind}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	observer, ok := service.ScanHandler().(worker.RecoveryObserver)
	if !ok {
		t.Fatal("factory not wired")
	}
	o, err := observer.ObserveRecovery(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE reconciliation_scans SET generation=generation+1,version=version+1 WHERE managed_anime_id=$1`, c.Owner.ID); err != nil {
		t.Fatal(err)
	}
	err = store.SettleRecovery(ctx, c, o.TerminalState, o.OwnerResult, o.Disposition, o.WriteOwner, o.PostTransition)
	if !errors.Is(err, workitems.ErrStaleClaim) {
		t.Fatalf("changed generation settled: %v", err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM work_item_history WHERE id=$1`, c.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != workitems.StateLeased {
		t.Fatal("stale settlement not rolled back")
	}
}

func TestScanRecoveryFactoryIsTyped(t *testing.T) {
	if _, ok := (&Service{}).ScanHandler().(worker.RecoveryObserver); !ok {
		t.Fatal("missing observer")
	}
}
