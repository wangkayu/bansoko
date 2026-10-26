package filemaintenance

import (
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
	"context"
	"errors"
	"testing"
	"time"
)

func TestMandatoryMaintenanceRecoveryActualFactoriesAreTyped(t *testing.T) {
	for _, h := range []worker.Handler{(&AlignmentService{}).ExecuteHandler(), (&OffsetMigrationExecuteService{}).ExecuteHandler(), (&ReplacementDecisionService{}).EvaluateHandler(), (&CleanupService{}).DeleteHandler()} {
		if _, ok := h.(worker.RecoveryObserver); !ok {
			t.Fatalf("actual factory %T lacks observer", h)
		}
	}
}
func TestPGAlignmentRecoveryNoEffectCompletedUnknownAndFence(t *testing.T) {
	f := openOpsTestDB(t)
	ctx := context.Background()
	op, err := f.svc.CreateOperation(ctx, testOperation())
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.svc.MarkReady(ctx, op.ID, op.Version)
	if err != nil {
		t.Fatal(err)
	}
	_, c := alignmentWorkItemClaim(t, ctx, f.db, op.ID, v)
	observer := f.svc.ExecuteHandler().(worker.RecoveryObserver)
	o, err := observer.ObserveRecovery(ctx, c)
	if err != nil || o.Disposition != workitems.DispositionNoExternalEffect {
		t.Fatalf("no effect: %+v %v", o, err)
	}
	for _, state := range []string{"running", "completed"} {
		if _, err := f.db.Exec(`UPDATE alignment_operations SET state=$2,attempt='{}' WHERE id=$1`, op.ID, state); err != nil {
			t.Fatal(err)
		}
		o, err = observer.ObserveRecovery(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		want := workitems.DispositionAmbiguous
		if state == "completed" {
			want = workitems.DispositionConfirmed
		}
		if o.Disposition != want {
			t.Fatalf("%s: %+v", state, o)
		}
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE alignment_operations SET version=version+1 WHERE id=$1`, op.ID); err != nil {
		t.Fatal(err)
	}
	_, err = o.WriteOwner(ctx, txscope.Wrap(tx), o.OwnerResult)
	_ = tx.Rollback()
	if !errors.Is(err, workitems.ErrStaleClaim) {
		t.Fatalf("writer fence=%v", err)
	}
}
func TestPGOffsetRecoveryNoEffectCompletedUnknownAndFence(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	ctx := context.Background()
	store := newOffsetMigrationWorkStore(t, db)
	generationID := seedOffsetRecoveryReadyFixture(t, db, store)
	if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET available_at=CURRENT_TIMESTAMP WHERE kind='offset_migration_execute' AND owner_id IN (SELECT id FROM offset_migration_plans WHERE generation_id=$1)`, generationID); err != nil {
		t.Fatal(err)
	}
	c, err := store.ClaimDue(ctx, "offset-recovery", []string{workitems.KindOffsetMigrationExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	id := c.Owner.ID
	s := NewOffsetMigrationExecuteService(db)
	observer := s.ExecuteHandler().(worker.RecoveryObserver)
	o, err := observer.ObserveRecovery(ctx, c)
	if err != nil || o.Disposition != workitems.DispositionNoExternalEffect {
		t.Fatalf("no effect: %+v %v", o, err)
	}
	for _, state := range []string{"running", "applied"} {
		if _, err := db.Exec(`UPDATE offset_migration_plans SET state=$2,attempt='{}' WHERE id=$1`, id, state); err != nil {
			t.Fatal(err)
		}
		o, err = observer.ObserveRecovery(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		want := workitems.DispositionAmbiguous
		if state == "applied" {
			want = workitems.DispositionConfirmed
		}
		if o.Disposition != want {
			t.Fatalf("%s: %+v", state, o)
		}
	}
	c.ExpectedOwnerVersion++
	if _, err := observer.ObserveRecovery(ctx, c); !errors.Is(err, workitems.ErrStaleClaim) {
		t.Fatalf("fence=%v", err)
	}
}
func TestPGCleanupRecoveryNoEffectCompletedUnknownAndFence(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	ctx := context.Background()
	id := seedReplacementDeletion(t, db, "recovery-cleanup", "anime-cleanup", "/source/file", "meta:size=4")
	s := NewCleanupService(db)
	store := cleanupWorkItemStore(t, db)
	s.SetWorkItems(store)
	c := cleanupClaim(t, store, id, workitems.KindReplacementDelete, 1)
	observer := s.DeleteHandler().(worker.RecoveryObserver)
	o, err := observer.ObserveRecovery(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if o.Disposition != workitems.DispositionNoExternalEffect {
		t.Fatalf("initial=%+v", o)
	}
	if _, err := db.Exec(`UPDATE replacement_deletions SET attempt='{"stage":"purge"}' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	o, err = observer.ObserveRecovery(ctx, c)
	if err != nil || o.Disposition != workitems.DispositionAmbiguous {
		t.Fatalf("unknown=%+v %v", o, err)
	}
	if _, err := db.Exec(`UPDATE replacement_deletions SET state='deleted' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	o, err = observer.ObserveRecovery(ctx, c)
	if err != nil || o.Disposition != workitems.DispositionConfirmed {
		t.Fatalf("completed=%+v %v", o, err)
	}
	c.ExpectedOwnerVersion++
	if _, err := observer.ObserveRecovery(ctx, c); !errors.Is(err, workitems.ErrStaleClaim) {
		t.Fatalf("fence=%v", err)
	}
}
