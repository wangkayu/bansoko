package deployment

import (
	"context"
	"errors"
	"testing"
	"time"

	"bansoko/internal/testutil/disposabledb"
)

func testRun(id string) *Run {
	return &Run{
		ID:                id,
		WindowID:          "win-" + id,
		OldBundleIdentity: "old",
		NewBundleIdentity: "new",
		BackupIdentity:    "backup",
		SchemaIdentity:    "schema",
		BuildIdentity:     "build",
		RegistryInventory: []InventoryEntry{{Key: "qb_download_task", Owner: "download_task", Effect: "qb_unknown_effect", Version: 1, Enabled: true}},
		Checkpoint:        NewCheckpoint(),
		State:             "running",
		Version:           1,
		StartedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
	}
}

func TestPGStoreCreateLoadUpdateCAS(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	store := NewPGStore(db)

	run := testRun("op-cas")
	if err := store.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx, "op-cas")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != "running" || loaded.Version != 1 || len(loaded.RegistryInventory) != 1 {
		t.Fatalf("unexpected loaded run: %+v", loaded)
	}
	loaded.Checkpoint.Settle("qb_download_task", StateCompleted, ReconcileResult{State: StateCompleted, Effect: EffectNone}, "", false)
	version, err := store.Update(ctx, "op-cas", loaded.Version, func(current *Run) {
		current.Checkpoint = loaded.Checkpoint
		current.State = "completed"
		current.UpdatedAt = time.Now().UTC()
	})
	if err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("expected version 2, got %d", version)
	}
	// Stale expected version must conflict.
	if _, err := store.Update(ctx, "op-cas", 1, func(current *Run) {}); err == nil {
		t.Fatal("stale CAS update must conflict")
	}
	// Latest reflects the updated run.
	latest, err := store.Latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || latest.State != "completed" || latest.Version != 2 {
		t.Fatalf("unexpected latest run: %+v", latest)
	}
}

func TestPGStoreMissingSchemaFailsClosed(t *testing.T) {
	db := disposabledb.ResetDisposableSchemaAsOwner(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DROP TABLE deployment_recovery_runs`); err != nil {
		t.Fatal(err)
	}
	store := NewPGStore(db)
	if _, err := store.Load(ctx, "op-any"); err == nil {
		t.Fatal("load must fail closed on missing checkpoint table")
	} else {
		var missing *ErrCheckpointSchemaMissing
		if !errors.As(err, &missing) {
			t.Fatalf("expected ErrCheckpointSchemaMissing, got %v", err)
		}
	}
	if err := store.Create(ctx, testRun("op-any")); err == nil {
		t.Fatal("create must fail closed on missing checkpoint table")
	}
}

func TestPGStoreReplaySkipsCompletedHandlers(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	store := NewPGStore(db)

	var calls int
	counting := handlerFunc(func(ctx context.Context, req ReconcileRequest) (ReconcileResult, error) {
		calls++
		return ReconcileResult{State: StateCompleted, Effect: EffectNone}, nil
	})
	registry, err := NewRegistry(DefaultRequiredKeys,
		fakeRegistration("qb_download_task", counting),
		fakeRegistration("artifact", counting),
		fakeRegistration("file_operation", counting))
	if err != nil {
		t.Fatal(err)
	}
	orchestrator := NewOrchestrator(registry, store)

	first, err := orchestrator.Run(ctx, testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if !first.Gate.Passed || calls != 3 {
		t.Fatalf("first run must pass with 3 calls: passed=%v calls=%d", first.Gate.Passed, calls)
	}
	// Simulate an orchestrator restart: a fresh orchestrator over the same
	// durable store must resume without re-invoking completed handlers.
	replay, err := NewOrchestrator(registry, store).Run(ctx, testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Gate.Passed || calls != 3 {
		t.Fatalf("restart replay must skip completed handlers: passed=%v calls=%d", replay.Gate.Passed, calls)
	}
}

func TestPGStoreResumesRetryableAfterRestart(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	store := NewPGStore(db)

	var calls int
	flaky := handlerFunc(func(ctx context.Context, req ReconcileRequest) (ReconcileResult, error) {
		calls++
		if calls == 1 {
			return ReconcileResult{State: StateRetryable, Effect: EffectUnknown, Reason: "transient"}, nil
		}
		return ReconcileResult{State: StateCompleted, Effect: EffectNone}, nil
	})
	registry, err := NewRegistry(DefaultRequiredKeys,
		fakeRegistration("qb_download_task", flaky),
		fakeRegistration("artifact", handlerReturning(StateCompleted)),
		fakeRegistration("file_operation", handlerReturning(StateCompleted)))
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewOrchestrator(registry, store).Run(ctx, testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if first.Gate.Passed {
		t.Fatalf("first run must fail while retryable")
	}
	second, err := NewOrchestrator(registry, store).Run(ctx, testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if !second.Gate.Passed || calls != 2 {
		t.Fatalf("restart must retry only the retryable handler: passed=%v calls=%d", second.Gate.Passed, calls)
	}
}
