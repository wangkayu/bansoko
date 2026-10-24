package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	artifact "bansoko/internal/artifact/application"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
	"github.com/google/uuid"
)

// Arrange the already committed plan graph for downstream prepare tests.
// This helper is test-only; production Plan creation belongs to the planning stage.
func seedCommittedInspectionPlanTx(ctx context.Context, scope txscope.Tx, request artifact.VerifiedArtifactRequest, oneShot *workitems.OneShotRef) (string, error) {
	return seedCommittedInspectionPlanBudgetTx(ctx, scope, request, oneShot, nil)
}

// seedCommittedInspectionPlanBudgetTx optionally freezes an explicit plan
// max_attempts at INSERT (nil lets the migration default apply).
func seedCommittedInspectionPlanBudgetTx(ctx context.Context, scope txscope.Tx, request artifact.VerifiedArtifactRequest, oneShot *workitems.OneShotRef, planMaxAttempts any) (string, error) {
	tx := scope.SQLTx()
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT id FROM postprocessing_plans WHERE artifact_id=$1`, request.ArtifactID).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO postprocessing_plans(id,artifact_id,acquisition_id,artifact_version,one_shot_id,input_digest,state,plan_max_attempts) SELECT $1,a.id,a.acquisition_id,a.version,$3,i.admission_digest,'queued',$4::integer FROM artifacts a JOIN acquisitions i ON i.id=a.acquisition_id WHERE a.id=$2 AND a.state='verified'`, id, request.ArtifactID, nullablePlanText(oneShot), planMaxAttempts); err != nil {
		return "", err
	}
	store := &workitems.Store{RetentionDays: workitems.DefaultRetentionDays}
	if _, err := store.Create(ctx, tx, workitems.WorkItemSpec{Owner: workitems.OwnerRef{Type: workitems.OwnerPostProcessingPlan, ID: id}, Kind: workitems.KindPostProcessPlan, IdempotencyKey: "plan:" + id, InputSnapshot: []byte(`{}`), MaxAttempts: defaultMaxAttempts, ExpectedOwnerVersion: 1, OneShot: oneShot}); err != nil {
		return "", fmt.Errorf("seed committed inspection plan work: %w", err)
	}
	return id, nil
}
