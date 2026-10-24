package application

import (
	"context"
	"testing"
	"time"

	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// manualTrigger builds a manual submit input with a stable request identity.
func manualTrigger(animeID, requestID string) ScanSubmitInput {
	trigger := ScanSubmitTriggerManual
	root := "/library"
	return ScanSubmitInput{
		ManagedAnimeID: animeID,
		TreeTop:        &root,
		Trigger:        &trigger,
		RequestID:      &requestID,
	}
}

func periodicTrigger(animeID, requestID string) ScanSubmitInput {
	trigger := ScanSubmitTriggerPeriodic
	root := "/library"
	return ScanSubmitInput{
		ManagedAnimeID: animeID,
		TreeTop:        &root,
		Trigger:        &trigger,
		RequestID:      &requestID,
	}
}

// completeScan drives one submitted scan through the handler to completion.
func completeScan(t *testing.T, service *Service, store *workitems.Store, input ScanSubmitInput) int64 {
	t.Helper()
	ctx := context.Background()
	if _, err := service.SubmitScan(ctx, input); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(ctx, "gen-worker", []string{workitems.KindReconciliationScan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.ScanHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("scan must complete: %+v", outcome)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	view, err := service.GetScan(ctx, input.ManagedAnimeID)
	if err != nil {
		t.Fatal(err)
	}
	return int64(view.Generation)
}

// TestGenerationIdentityReplayReusesAndIndependentTriggersIncrement proves the
// generation idempotency contract: replaying the exact same request (same
// trigger identity + same input) returns the same completed generation, while
// a new manual trigger and a new periodic trigger each start a strictly newer
// generation. The old completed generation is never reused forever.
func TestGenerationIdentityReplayReusesAndIndependentTriggersIncrement(t *testing.T) {
	db, store := openReconTestDB(t)
	defer db.Close()
	files := testFiles(3)
	seedReconInstance(t, db, "anime-gen-1", "sub-gen-1", "active", files)
	service, err := NewService(db, store, &fakeScanner{files: files})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	first := manualTrigger("anime-gen-1", "req-aaa")
	gen1 := completeScan(t, service, store, first)

	// Exact replay of the same request reuses the completed generation and
	// creates no new scan work item.
	replay, err := service.SubmitScan(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if replay.State != "completed" || int64(replay.Generation) != gen1 {
		t.Fatalf("identical replay must reuse generation %d: %+v", gen1, replay)
	}

	// A new manual request (new request identity) is an independent trigger:
	// strictly newer generation.
	gen2 := completeScan(t, service, store, manualTrigger("anime-gen-1", "req-bbb"))
	if gen2 != gen1+1 {
		t.Fatalf("new manual trigger must increment the generation: gen1=%d gen2=%d", gen1, gen2)
	}

	// A periodic scheduler tick is an independent trigger too.
	gen3 := completeScan(t, service, store, periodicTrigger("anime-gen-1", "tick-1"))
	if gen3 != gen2+1 {
		t.Fatalf("periodic trigger must increment the generation: gen2=%d gen3=%d", gen2, gen3)
	}

	// Every completed generation is usable/immutable; the running owner row
	// always reports the newest generation.
	view, err := service.GetScan(ctx, "anime-gen-1")
	if err != nil {
		t.Fatal(err)
	}
	if int64(view.Generation) != gen3 {
		t.Fatalf("owner must report the newest generation: %d", view.Generation)
	}
}

// TestScanWorkItemsForActiveAndStoppedInstances proves the lifecycle gate: the
// periodic trigger admits only active instances; a stopped instance may still
// run an explicit manual read-only scan and both form immutable observations.
func TestScanWorkItemsForActiveAndStoppedInstances(t *testing.T) {
	db, store := openReconTestDB(t)
	defer db.Close()
	files := testFiles(2)
	service, err := NewService(db, store, &fakeScanner{files: files})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Active instance: periodic trigger submits and completes.
	seedReconInstance(t, db, "anime-active", "sub-active", "active", files)
	gen := completeScan(t, service, store, periodicTrigger("anime-active", "tick-active"))
	if gen != 1 {
		t.Fatalf("active instance must scan into generation 1: %d", gen)
	}
	var items, observations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_item_history WHERE kind = 'reconciliation_scan' AND owner_id = 'anime-active'`).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM reconciliation_files WHERE managed_anime_id = 'anime-active' AND generation = 1`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if items != 1 || observations != 2 {
		t.Fatalf("active: scan items=%d observations=%d", items, observations)
	}
	usable, err := service.GenerationUsable(ctx, "anime-active", 1)
	if err != nil || !usable {
		t.Fatalf("active generation must be completed/immutable: %v %v", usable, err)
	}

	// Stopped instance: periodic is rejected by the gate; an explicit manual
	// read-only scan completes into observations with no effect authorization.
	seedReconInstance(t, db, "anime-stopped", "sub-stopped", "stopped", files)
	if _, err := service.SubmitScan(ctx, periodicTrigger("anime-stopped", "tick-stopped")); err == nil {
		t.Fatal("periodic scan of a stopped instance must be rejected")
	}
	gen = completeScan(t, service, store, manualTrigger("anime-stopped", "manual-1"))
	if gen != 1 {
		t.Fatalf("stopped manual scan must complete generation 1: %d", gen)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM reconciliation_files WHERE managed_anime_id = 'anime-stopped' AND generation = 1`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if observations != 2 {
		t.Fatalf("stopped manual scan observations=%d", observations)
	}
	if usable, err := service.GenerationUsable(ctx, "anime-stopped", 1); err != nil || !usable {
		t.Fatalf("stopped manual generation must be completed/immutable: %v %v", usable, err)
	}
}
