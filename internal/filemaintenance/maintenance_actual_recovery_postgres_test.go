package filemaintenance

import (
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPGAlignmentActualHandlerReceiptCrashBeforeSettlementRecovery(t *testing.T) {
	f := openOpsTestDB(t)
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, target := filepath.Join(root, "source.mkv"), filepath.Join(root, "target.mkv")
	if err := os.WriteFile(source, []byte("actual alignment"), 0600); err != nil {
		t.Fatal(err)
	}
	observed, err := (filesystem.OSFileObserver{}).ObserveMetadata(ctx, source) // a scan-derived source is path-level
	if err != nil {
		t.Fatal(err)
	}
	input := testOperation()
	input.SourcePath = source
	input.TargetPath = target
	input.SourceFingerprint = observed
	seedAlignmentPlacement(t, f.db, input.ManagedAnimeID, source, observed)
	op, err := f.svc.CreateOperation(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.svc.MarkReady(ctx, op.ID, op.Version)
	if err != nil {
		t.Fatal(err)
	}
	store, c := alignmentWorkItemClaim(t, ctx, f.db, op.ID, v)
	f.svc.SetWorkItems(store)
	f.svc.SetFilePorts(filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	outcome, err := f.svc.ExecuteHandler().Handle(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("actual handler=%+v", outcome)
	}
	// Drop the returned writer: process crashed after receipt but before Finish.
	if err := f.db.QueryRow(`SELECT expected_owner_version FROM work_item_history WHERE id=$1`, c.ID).Scan(&c.ExpectedOwnerVersion); err != nil {
		t.Fatal(err)
	}
	recovered, err := f.svc.ExecuteHandler().(worker.RecoveryObserver).ObserveRecovery(ctx, c)
	if err != nil || recovered.Disposition != workitems.DispositionConfirmed {
		t.Fatalf("recovery=%+v %v", recovered, err)
	}
	tx, err := f.db.BeginTx(ctx, nil)
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
	var state, path string
	if err := f.db.QueryRow(`SELECT state FROM alignment_operations WHERE id=$1`, op.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT physical_path FROM placements WHERE managed_anime_id=$1 AND state='active' LIMIT 1`, input.ManagedAnimeID).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || path != target {
		t.Fatalf("owner=%s path=%s", state, path)
	}
}
