package application

import (
	"context"
	"database/sql"
	"testing"
	"time"

	artifactapplication "bansoko/internal/artifact/application"
	"bansoko/internal/persistence/txscope"
	runtimeworker "bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// Plan business-attempt budget through the real worker entry
// (Worker.RunOnce -> PostProcessingPlanHandler). The migration SQL guards are proven
// by the migration tests; these prove the wiring.

type a11PlanBudget struct {
	started  int
	states   string
	workAtt  int
	workSt   string
	planSt   string
	planCode string
}

func readA11PlanBudget(t *testing.T, db *sql.DB, planID string) a11PlanBudget {
	t.Helper()
	var b a11PlanBudget
	var code sql.NullString
	if err := db.QueryRow(`
SELECT p.plan_attempts_started,
       COALESCE((SELECT string_agg(e->>'state', ',' ORDER BY (e->>'ordinal')::int) FROM jsonb_array_elements(p.plan_attempt_index) e), ''),
       w.attempt, w.state, p.state, p.planning_diagnostic->>'reason_code'
  FROM postprocessing_plans p JOIN work_item_history w ON w.owner_type='postprocessing_plan' AND w.owner_id=p.id AND w.kind='postprocess_plan'
 WHERE p.id=$1`, planID).Scan(&b.started, &b.states, &b.workAtt, &b.workSt, &b.planSt, &code); err != nil {
		t.Fatal(err)
	}
	b.planCode = code.String
	return b
}

func seedA11Plan(t *testing.T, artifactID string, planMaxAttempts any) string {
	t.Helper()
	ctx := context.Background()
	tx, err := ownerTestDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	planID, err := seedCommittedInspectionPlanBudgetTx(ctx, txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: artifactID}, nil, planMaxAttempts)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return planID
}

func runA11PlanWorker(t *testing.T, db *sql.DB, service *Service) {
	t.Helper()
	w := runtimeworker.NewWorker(&workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}, "a11-plan-worker", time.Minute)
	if err := w.Register(workitems.KindPostProcessPlan, service.PostProcessingPlanHandler()); err != nil {
		t.Fatal(err)
	}
	if handled, err := w.RunOnce(context.Background()); err != nil || !handled {
		t.Fatalf("plan worker run: handled=%v err=%v", handled, err)
	}
}

func expireA11PlanLease(t *testing.T, planID string) {
	t.Helper()
	if _, err := ownerTestDB(t).Exec(`UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE owner_type='postprocessing_plan' AND owner_id=$1`, planID); err != nil {
		t.Fatal(err)
	}
}

// A crash before the gate costs only the claim; the next real run consumes
// exactly one business attempt and succeeds.
func TestA11PlanPreGateCrashThenSuccessThroughWorker(t *testing.T) {
	db, fixture := openFileOperationDB(t)
	service := newFixtureService(t, db, fixture)
	planID := seedA11Plan(t, fixture.artifactID, nil)
	claimPlanWorkItem(t, db, "a11-dead-plan-worker")
	expireA11PlanLease(t, planID)
	runA11PlanWorker(t, db, service)
	if got := readA11PlanBudget(t, db, planID); got.started != 1 || got.states != "issued" || got.workAtt != 2 || got.workSt != workitems.StateSucceeded || got.planSt != "succeeded" {
		t.Fatalf("pre-gate crash then success must cost one business attempt: %+v", got)
	}
}

// A crash after the gate costs the business attempt; once the budget is spent
// the next claim terminates the plan visibly instead of planning again.
func TestA11PlanGateCrashExhaustsVisiblyThroughWorker(t *testing.T) {
	db, fixture := openFileOperationDB(t)
	service := newFixtureService(t, db, fixture)
	planID := seedA11Plan(t, fixture.artifactID, 1)
	claim := claimPlanWorkItem(t, db, "a11-crash-plan-worker")
	// The gate commits, then the process dies before planning.
	if _, err := service.workItems.StartStageAttempt(context.Background(), claim, "a11-crashed-session"); err != nil {
		t.Fatal(err)
	}
	expireA11PlanLease(t, planID)
	runA11PlanWorker(t, db, service)
	got := readA11PlanBudget(t, db, planID)
	if got.started != 1 || got.workAtt != 2 || got.workSt != workitems.StateFailed || got.planSt != "failed" || got.planCode != planBudgetExhaustedCode {
		t.Fatalf("exhausted plan budget must be a typed terminal failure: %+v", got)
	}
	var operations int
	if err := db.QueryRow(`SELECT count(*) FROM file_operations WHERE artifact_id=$1`, fixture.artifactID).Scan(&operations); err != nil || operations != 0 {
		t.Fatalf("an exhausted plan must not plan operations: %d %v", operations, err)
	}
}
