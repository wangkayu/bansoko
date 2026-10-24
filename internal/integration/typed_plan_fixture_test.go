package integration

import (
	fileoperationapplication "bansoko/internal/fileoperation/application"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"github.com/google/uuid"
	"testing"
	"time"
)

// Arrange the committed planning output as fixture owner, then invoke the actual prepare stage.
func prepareRealEffectTypedPlan(t *testing.T, db *sql.DB, s *fileoperationapplication.Service, artifactID string) {
	t.Helper()
	ctx := context.Background()
	owner := disposabledb.Owner(t)
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	id := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO postprocessing_plans(id,artifact_id,acquisition_id,artifact_version,input_digest,state) SELECT $1,a.id,a.acquisition_id,a.version,i.admission_digest,'queued' FROM artifacts a JOIN acquisitions i ON i.id=a.acquisition_id WHERE a.id=$2 AND a.state='verified'`, id, artifactID); err != nil {
		t.Fatal(err)
	}
	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	if _, err := store.Create(ctx, tx, workitems.WorkItemSpec{Owner: workitems.OwnerRef{Type: workitems.OwnerPostProcessingPlan, ID: id}, Kind: workitems.KindPostProcessPlan, IdempotencyKey: "plan:" + id, InputSnapshot: []byte(`{}`), MaxAttempts: 5, ExpectedOwnerVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(ctx, "typed-real-effect-planner", []string{workitems.KindPostProcessPlan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.PostProcessingPlanHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != worker.OutcomeCommitted || out.WriteOwner != nil || out.PostTransition != nil {
		t.Fatalf("planning receipt=%+v", out)
	}
}
