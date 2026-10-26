package filemaintenance

import (
	"context"
	"errors"
	"testing"
	"time"

	"bansoko/internal/deployment"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

type metadataOnlyOffsetObserver struct {
	fingerprint   string
	contentCalls  int
	metadataCalls int
}

func (o *metadataOnlyOffsetObserver) Observe(context.Context, string) (string, error) {
	o.contentCalls++
	return "", errors.New("content observation is forbidden for a metadata fingerprint")
}

func (o *metadataOnlyOffsetObserver) ObserveMetadata(context.Context, string) (string, error) {
	o.metadataCalls++
	return o.fingerprint, nil
}

func TestOffsetMigrationExecutesRecoveryMetadataFingerprint(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	store := newOffsetMigrationWorkStore(t, db)
	const raw = "meta:size=1024;dev=2;ino=11"
	const envelope = `{"algorithm":"metadata","value":"meta:size=1024;dev=2;ino=11","size_bytes":1024}`
	seedOffsetMigrationReadyOperationWithFingerprint(t, db, store, envelope)

	claim, err := store.ClaimDue(ctx, "metadata-offset-worker", []string{workitems.KindOffsetMigrationExecute}, time.Minute)
	if err != nil {
		t.Fatalf("claim metadata offset operation: %v", err)
	}
	executor := &fakeOpsExecutor{}
	observer := &metadataOnlyOffsetObserver{fingerprint: raw}
	service := NewOffsetMigrationExecuteService(db)
	service.SetWorkItems(store)
	service.SetFilePorts(executor, observer)
	outcome, err := service.ExecuteHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatalf("execute metadata offset operation: %v", err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("metadata execution outcome = %+v, want finish/succeeded", outcome)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatalf("finish metadata execution: %v", err)
	}
	if executor.operation.SourceFingerprint != raw {
		t.Fatalf("executor source fingerprint = %q, want unwrapped %q", executor.operation.SourceFingerprint, raw)
	}
	if observer.contentCalls != 0 || observer.metadataCalls == 0 {
		t.Fatalf("observer calls: content=%d metadata=%d", observer.contentCalls, observer.metadataCalls)
	}
	var state, coverage string
	if err := db.QueryRow(`SELECT state FROM offset_migration_plans WHERE id = $1`, claim.Owner.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT observation_key FROM placement_coverage WHERE placement_id = 'plc-offmig'`).Scan(&coverage); err != nil {
		t.Fatal(err)
	}
	if state != string(OffsetMigrationOperationApplied) || coverage != "tv:s1:e2" {
		t.Fatalf("metadata migration state=%s coverage=%s, want applied/tv:s1:e2", state, coverage)
	}
}

func TestOffsetMigrationReconcileObservesMetadataEnvelope(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	store := newOffsetMigrationWorkStore(t, db)
	const raw = "meta:size=1024;dev=2;ino=11"
	const envelope = `{"algorithm":"metadata","value":"meta:size=1024;dev=2;ino=11","size_bytes":1024}`
	seedOffsetMigrationReadyOperationWithFingerprint(t, db, store, envelope)
	if _, err := db.Exec(`
UPDATE offset_migration_plans
SET state = 'running', attempt = '{"operation_id":"metadata-reconcile"}'::jsonb
WHERE placement_id = 'plc-offmig'`); err != nil {
		t.Fatalf("freeze metadata recovery attempt: %v", err)
	}
	observer := &metadataOnlyOffsetObserver{fingerprint: raw}
	service := NewOffsetMigrationExecuteService(db)
	service.SetFilePorts(&fakeOpsExecutor{}, observer)
	result, err := service.ReconcileUnknownEffect(ctx, deployment.ReconcileRequest{OperationID: "metadata-offset-reconcile"})
	if err != nil {
		t.Fatalf("reconcile metadata offset: %v", err)
	}
	if result.State != deployment.StateCompleted || result.Effect != deployment.EffectPresent {
		t.Fatalf("metadata reconcile result = %+v", result)
	}
	if observer.contentCalls != 0 || observer.metadataCalls != 1 {
		t.Fatalf("reconcile observer calls: content=%d metadata=%d", observer.contentCalls, observer.metadataCalls)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM offset_migration_plans WHERE placement_id = 'plc-offmig'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(OffsetMigrationOperationApplied) {
		t.Fatalf("reconciled metadata operation state = %s, want applied", state)
	}
}

func TestOffsetMigrationRejectsInvalidMetadataDuringPlanning(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	seedOffsetMigrationChain(t, db, "1")
	if _, err := db.Exec(`UPDATE placements SET target_fingerprint = $1 WHERE id = 'plc-offmig'`,
		`{"algorithm":"metadata","value":"meta:size=bad","size_bytes":1024}`); err != nil {
		t.Fatal(err)
	}
	admission := NewOffsetMigrationAdmissionService()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	generationID, _, err := admission.AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-offmig", "src-offmig", 1, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := newOffsetMigrationWorkStore(t, db)
	planner := NewOffsetMigrationService(db, NewNamingTargetRenderer(db, fakeRulesSource{}))
	planner.SetWorkItems(store)
	if _, err := planner.PlanGeneration(ctx, generationID); err != nil {
		t.Fatalf("plan invalid metadata: %v", err)
	}
	var state, reason string
	if err := db.QueryRow(`SELECT state, reason_code FROM offset_migration_plans WHERE generation_id = $1`, generationID).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != string(OffsetMigrationOperationBlocked) || reason != "fingerprint_invalid" {
		t.Fatalf("invalid metadata plan state=%s reason=%s", state, reason)
	}
	var executable int
	if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE kind = $1`, workitems.KindOffsetMigrationExecute).Scan(&executable); err != nil {
		t.Fatal(err)
	}
	if executable != 0 {
		t.Fatalf("invalid metadata produced %d executable work items", executable)
	}
}
