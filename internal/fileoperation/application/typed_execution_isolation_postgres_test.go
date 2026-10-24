package application

import (
	"bansoko/internal/deployment"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"testing"
)

// TestPGTypedExecutionIsolation pins the lane boundaries: a planned operation
// dispatches to the fixed typed lane, recovery fallback keeps the claim for the
// recovery lane, and deployment reconcile stays read-only. The typed execution itself is proven positive by the chain
// evidence (TestRuntimeTypedFinishEntry) — this test pins the lane split.
func TestPGTypedExecutionIsolation(t *testing.T) {
	s, c := preparedPlanPGFixture(t)
	ctx := context.Background()
	if _, err := s.PostProcessingPlanHandler().Handle(ctx, c); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := s.db.QueryRow(`SELECT id FROM file_operations WHERE postprocessing_plan_id=$1 LIMIT 1`, c.Owner.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	// Handle dispatches typed claims to the fixed lane. With no
	// publication evidence yet the typed chain would run — but this fixture
	// has no filesystem source, so the frozen preparation fails closed with a
	// bounded outcome instead of reaching any legacy executor. The essential
	// boundary: no legacy executor call, no owner write, no terminal result.
	file := workitems.Claim{Owner: workitems.OwnerRef{Type: workitems.OwnerFileOperation, ID: id}, Kind: workitems.KindFileExecute}
	before := planGraphSnapshot(t, s.db)
	ex := &testExecutor{}
	ob := &testObserver{}
	s.executor = ex
	s.observer = ob
	// The fixture claim carries no frozen input snapshot, so the typed lane
	// fails closed at the first re-verification (file_operation_input_invalid)
	// — still a bounded terminal outcome through the typed writer; the legacy
	// adapter/observer stay unreached.
	// B5: the typed failure goes through the fixed 184 command, whose gate
	// refuses this unfenced claim: an error, no legacy port call, no write.
	if _, tauErr := s.Handle(ctx, file); tauErr == nil {
		t.Fatal("an unfenced typed claim must be refused by the fixed failure command")
	}
	if ex.calls != 0 || ob.calls != 0 {
		t.Fatalf("typed claim reached legacy ports: executor=%d observer=%d", ex.calls, ob.calls)
	}
	// Recovery observation: typed claims observe read-only (never the legacy
	// exact-target finish).
	// B5: typed recovery converges through the fixed commands, whose gates
	// refuse an unfenced claim — an error, no legacy port call, no write.
	if out, err := s.ObserveRecovery(ctx, file); err == nil {
		t.Fatalf("typed recovery accepted an unfenced claim: %+v", out)
	}
	if ex.calls != 0 || ob.calls != 0 {
		t.Fatalf("typed recovery reached legacy ports: executor=%d observer=%d", ex.calls, ob.calls)
	}
	// Recovery fallback keeps the claim leased for the recovery lane with the
	// explicit await-recovery outcome.
	fbOut, fbHandled := s.RecoveryFallback(ctx, file, nil)
	if !fbHandled {
		t.Fatal("typed claim fell through to the legacy fallback")
	}
	if fbOut.Kind != worker.OutcomeAwaitRecovery || fbOut.WriteOwner != nil || fbOut.PostTransition != nil || fbOut.OwnerResult.ReasonCode != "FILE_CANCEL_FINALIZATION_PENDING" {
		t.Fatalf("typed fallback shape=%+v", fbOut)
	}
	result, err := s.ReconcileUnknownEffect(ctx, deployment.ReconcileRequest{})
	if err != nil || result.State != deployment.StateBlocked || result.Effect != deployment.EffectUnknown {
		t.Fatalf("deployment legacy settle reachable: %+v %v", result, err)
	}
	if after := planGraphSnapshot(t, s.db); after != before {
		t.Fatal("isolation changed graph")
	}
}
