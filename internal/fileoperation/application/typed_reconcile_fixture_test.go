package application

import (
	"bansoko/internal/deployment"
	"context"
	"os"
	"testing"
)

// Plan arrangement and actual prepare commit, never a bare legacy Operation insert.
func assertTypedReconcileIsolation(t *testing.T, scenario string) {
	t.Helper()
	s, c := preparedPlanPGFixture(t)
	ctx := context.Background()
	if _, err := s.PostProcessingPlanHandler().Handle(ctx, c); err != nil {
		t.Fatal(err)
	}
	observer := &testObserver{fingerprint: "same"}
	if scenario == "absent" {
		observer.err = os.ErrNotExist
	}
	if scenario == "conflicting" {
		observer.fingerprint = "different"
	}
	executor := &testExecutor{}
	s.observer = observer
	s.executor = executor
	before := planGraphSnapshot(t, s.db)
	useRuntimePlanObserver(t, s)
	out, err := s.ReconcileUnknownEffect(ctx, deployment.ReconcileRequest{})
	if err != nil || out.State != deployment.StateBlocked || out.Effect != deployment.EffectUnknown || observer.calls != 0 || executor.calls != 0 {
		t.Fatalf("typed deployment isolation=%+v err=%v observer=%d executor=%d", out, err, observer.calls, executor.calls)
	}
	if planGraphSnapshot(t, ownerTestDB(t)) != before {
		t.Fatal("deployment isolated graph changed")
	}
}
