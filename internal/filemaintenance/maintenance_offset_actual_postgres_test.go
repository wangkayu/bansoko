package filemaintenance

import (
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestPGOffsetActualHandlerReceiptCrashBeforeSettlementRecovery(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := newOffsetMigrationWorkStore(t, db)
	seedOffsetMigrationRealChain(t, db, root, "1")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, created, err := NewOffsetMigrationAdmissionService().AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-real", "src-real", 1, 0, 1)
	if err != nil || !created {
		_ = tx.Rollback()
		t.Fatalf("admit %v %v", created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	planner := NewOffsetMigrationService(db, NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: offsetRealChainRules(root)}))
	planner.SetWorkItems(store)
	g, err := planner.PlanGeneration(ctx, id)
	if err != nil || g.State != OffsetMigrationGenerationReady {
		t.Fatalf("plan %+v %v", g, err)
	}
	if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET available_at=CURRENT_TIMESTAMP WHERE kind='offset_migration_execute'`); err != nil {
		t.Fatal(err)
	}
	c, err := store.ClaimDue(ctx, "actual-offset", []string{workitems.KindOffsetMigrationExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s := NewOffsetMigrationExecuteService(db)
	s.SetWorkItems(store)
	s.SetFilePorts(filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	o, err := s.ExecuteHandler().Handle(ctx, c)
	if err != nil || o.TerminalState != workitems.StateSucceeded {
		t.Fatalf("handler %+v %v", o, err)
	}
	if err := db.QueryRow(`SELECT expected_owner_version FROM work_item_history WHERE id=$1`, c.ID).Scan(&c.ExpectedOwnerVersion); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.ExecuteHandler().(worker.RecoveryObserver).ObserveRecovery(ctx, c)
	if err != nil || recovered.Disposition != workitems.DispositionConfirmed {
		t.Fatalf("recovery %+v %v", recovered, err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recovered.WriteOwner(ctx, txscope.Wrap(tx), recovered.OwnerResult); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM offset_migration_plans WHERE id=$1`, c.Owner.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "applied" {
		t.Fatal(state)
	}
}
