package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	animedomain "bansoko/internal/anime/domain"
	artifactapplication "bansoko/internal/artifact/application"
	"bansoko/internal/namingvars"
	"bansoko/internal/persistence/txscope"
	runtimeworker "bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"os"
)

// The dedicated post-processing plan stage. Artifact verification
// no longer couples its success transaction to naming outcomes: it persists a
// queued plan plus its claimable WorkItem, and the shared prepare engine runs
// in the plan stage's own transaction. A deterministic planning failure turns
// the plan terminal without rewriting the verified artifact fact; transient
// failures stay queued and retry inside the WorkItem budget.

func claimPlanWorkItem(t *testing.T, db *sql.DB, workerID string) workitems.Claim {
	t.Helper()
	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	claim, err := store.ClaimDue(context.Background(), workerID, []string{workitems.KindPostProcessPlan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func TestPostProcessingPlanExecutesNamingForVerifiedArtifact(t *testing.T) {
	db, fixture := openFileOperationDB(t)
	service := newFixtureService(t, db, fixture)
	ctx := context.Background()

	// The artifact-success transaction persists the plan and its WorkItem.
	tx, err := ownerTestDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	planID, err := seedCommittedInspectionPlanTx(ctx, txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: fixture.artifactID}, nil)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if planID == "" {
		t.Fatal("no plan was created")
	}

	var state string
	if err := db.QueryRowContext(ctx, `SELECT state FROM postprocessing_plans WHERE id = $1`, planID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "queued" {
		t.Fatalf("plan state = %q, want queued", state)
	}

	// A verified-replay under the same artifact reuses the live plan without a
	// second WorkItem.
	tx, err = ownerTestDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	replayID, err := seedCommittedInspectionPlanTx(ctx, txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: fixture.artifactID}, nil)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if replayID != planID {
		t.Fatalf("replay created plan %q, want the existing %q", replayID, planID)
	}

	// Executing the plan claim runs the shared prepare engine: the FileOperation
	// appears and the plan owner reaches succeeded.
	handler := service.PostProcessingPlanHandler()
	claim := claimPlanWorkItem(t, db, "plan-worker-1")
	if claim.Owner.ID != planID || claim.Owner.Type != workitems.OwnerPostProcessingPlan {
		t.Fatalf("claimed %s/%s, want the plan owner", claim.Owner.Type, claim.Owner.ID)
	}
	outcome, err := handler.Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != runtimeworker.OutcomeCommitted || outcome.CommittedClaim == nil || outcome.OwnerResult.State != workitems.StateSucceeded || outcome.WriteOwner != nil || outcome.PostTransition != nil {
		t.Fatalf("outcome = %+v, want a committed success without callbacks", outcome)
	}
	var workState string
	if err := db.QueryRowContext(ctx, `SELECT state FROM work_item_history WHERE id=$1`, claim.ID).Scan(&workState); err != nil {
		t.Fatal(err)
	}
	if workState != workitems.StateSucceeded {
		t.Fatalf("planning work=%s", workState)
	}

	if err := db.QueryRowContext(ctx, `SELECT state FROM postprocessing_plans WHERE id = $1`, planID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" {
		t.Fatalf("plan state = %q, want succeeded", state)
	}
	var operations int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file_operations WHERE artifact_id = $1`, fixture.artifactID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	var snapshot []byte
	if err := db.QueryRowContext(ctx, `SELECT frozen_config_snapshot FROM file_operations WHERE artifact_id=$1 LIMIT 1`, fixture.artifactID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Vars namingvars.FrozenEvaluation `json:"vars"`
	}
	if err := json.Unmarshal(snapshot, &stored); err != nil {
		t.Fatal(err)
	}
	if err := stored.Vars.Output.Validate(); err != nil {
		t.Fatalf("persisted disabled vars: %v", err)
	}
	if operations == 0 {
		var items []byte
		if err := db.QueryRowContext(ctx, `SELECT canonical_plan FROM postprocessing_plans WHERE id=$1`, planID).Scan(&items); err != nil {
			t.Fatal(err)
		}
		t.Logf("committed declarations: %s", items)
		t.Fatal("plan execution created no file operations")
	}
	// The verified artifact fact is untouched by the stage transition.
	var artifactState string
	if err := db.QueryRowContext(ctx, `SELECT state FROM artifacts WHERE id = $1`, fixture.artifactID).Scan(&artifactState); err != nil {
		t.Fatal(err)
	}
	if artifactState != "verified" {
		t.Fatalf("artifact state = %q, want verified", artifactState)
	}
}

// A deterministic planning failure turns the plan terminal (failed) on the
// plan owner only; the verified artifact stays verified and no fake
// FileOperation is fabricated.
func TestPostProcessingPlanDeterministicFailureIsTerminalOnPlanOwner(t *testing.T) {
	db, fixture := openFileOperationDB(t)
	service := newFixtureService(t, db, fixture)
	ctx := context.Background()

	tx, err := ownerTestDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	planID, err := seedCommittedInspectionPlanTx(ctx, txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: fixture.artifactID}, nil)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Exercise the deterministic prepare-error boundary with an intact plan graph.
	claim := claimPlanWorkItem(t, db, "plan-worker-2")
	outcome, err := service.typedPlanError(ctx, claim, errors.New("fixture deterministic planning failure"), true)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != runtimeworker.OutcomeCommitted || outcome.CommittedClaim == nil || outcome.OwnerResult.State != workitems.StateFailed || outcome.WriteOwner != nil || outcome.PostTransition != nil {
		t.Fatalf("outcome = %+v, want typed failed commit", outcome)
	}

	var state, outcomeCode string
	if err := db.QueryRowContext(ctx, `SELECT state, outcome_code FROM postprocessing_plans WHERE id = $1`, planID).Scan(&state, &outcomeCode); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || outcomeCode == "" {
		t.Fatalf("plan = %s/%s, want failed with a typed outcome code", state, outcomeCode)
	}
	var artifactState string
	if err := db.QueryRowContext(ctx, `SELECT state FROM artifacts WHERE id = $1`, fixture.artifactID).Scan(&artifactState); err != nil {
		t.Fatal(err)
	}
	if artifactState != "verified" {
		t.Fatalf("artifact state = %q, want verified", artifactState)
	}
	var operations int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file_operations WHERE artifact_id = $1`, fixture.artifactID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if operations != 0 {
		t.Fatal("a failed plan fabricated file operations")
	}
}

// A transient planning failure (exhaustion not reached) keeps the plan queued
// and returns a retry outcome.
func TestPostProcessingPlanTransientFailureStaysQueued(t *testing.T) {
	db, fixture := openFileOperationDB(t)
	service := newFixtureService(t, db, fixture)
	ctx := context.Background()

	tx, err := ownerTestDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	planID, err := seedCommittedInspectionPlanTx(ctx, txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: fixture.artifactID}, nil)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	claim := claimPlanWorkItem(t, db, "plan-worker-3")
	outcome, err := service.typedPlanError(ctx, claim, errors.New("fixture transient planning failure"), false)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != runtimeworker.OutcomeCommitted || outcome.CommittedClaim == nil || outcome.WriteOwner != nil || outcome.PostTransition != nil {
		t.Fatalf("outcome=%+v, want typed retry commit", outcome)
	}
	var workState string
	var attempt int
	if err := db.QueryRow(`SELECT state,attempt FROM work_item_history WHERE id=$1`, claim.ID).Scan(&workState, &attempt); err != nil {
		t.Fatal(err)
	}
	if workState != "available" || attempt != claim.Attempt {
		t.Fatalf("retry reset budget/state: %s/%d", workState, attempt)
	}
	var state string
	if err := db.QueryRowContext(ctx, `SELECT state FROM postprocessing_plans WHERE id = $1`, planID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "queued" {
		t.Fatalf("plan state = %q, want queued across retries", state)
	}
}

var _ = errors.New

// A replan plan's captured config snapshot is the configuration authority for
// that plan: the DownloadTask frozen policy still carries the fixture default,
// but the plan's frozen file effect decides the operation.
func TestPostProcessingPlanUsesCapturedConfigSnapshot(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, func(policy map[string]any) {
		policy["effective_sections"].(map[string]any)[animedomain.SectionFileEffect] = map[string]any{"operation": "move", "collision_policy": "versioned"}
	})
	service := newFixtureService(t, db, fixture)
	ctx := context.Background()

	tx, err := ownerTestDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedCommittedInspectionPlanTx(ctx, txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: fixture.artifactID}, nil); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claim := claimPlanWorkItem(t, db, "captured-policy-worker")
	outcome, err := service.PostProcessingPlanHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != runtimeworker.OutcomeCommitted || outcome.WriteOwner != nil || outcome.PostTransition != nil {
		t.Fatalf("typed outcome=%+v", outcome)
	}
	var operation, collisionPolicy string
	if err := db.QueryRowContext(ctx, `
SELECT operation, collision_policy FROM file_operations WHERE artifact_id = $1`, fixture.artifactID).Scan(&operation, &collisionPolicy); err != nil {
		t.Fatalf("read planned operation: %v", err)
	}
	if operation != "move" || collisionPolicy != "version" {
		t.Fatalf("planned operation = %s/%s, want the captured snapshot's move/versioned", operation, collisionPolicy)
	}
	var id string
	if err := db.QueryRow(`SELECT id FROM file_operations WHERE artifact_id=$1`, fixture.artifactID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	// The captured plan's operation is routed to the typed lane,
	// never the legacy executor, and a forged claim fails closed without any
	// filesystem effect or owner write.
	assertTypedForgedClaimFailsClosed(t, db, service, id)
}

// Frozen file regex excludes both video and subtitle, yielding exactly zero effects.
func TestPostProcessingPlanExcludesVideoAndSubtitleWithZeroOperations(t *testing.T) {
	db, fixture := openFileOperationDBWithPolicy(t, func(policy map[string]any) {
		policy["effective_sections"].(map[string]any)[animedomain.SectionFileRules] = map[string]any{"must_contain": "", "not_in": ".*"}
		policy["fixture_manifest"] = []map[string]any{{"file_ref": "primary", "ordinal": 0, "relative_path": "source.mkv", "exact_size": 14, "media_kind": "video", "identity_evidence": map[string]any{}}, {"file_ref": "excluded-subtitle", "ordinal": 1, "relative_path": "source.mkv.ass", "exact_size": 1, "media_kind": "subtitle", "identity_evidence": map[string]any{}}}
	})
	service := newFixtureService(t, db, fixture)
	ctx := context.Background()
	source := fixture.sourcePath + ".ass"
	if err := os.WriteFile(source, []byte("s"), 0600); err != nil {
		t.Fatal(err)
	}
	addArtifactFile(t, db, fixture, "excluded-subtitle", "excluded-subtitle", "source.mkv.ass", source, "subtitle", 1)
	tx, err := ownerTestDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedCommittedInspectionPlanTx(ctx, txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: fixture.artifactID}, nil); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	handler := service.PostProcessingPlanHandler()
	claim := claimPlanWorkItem(t, db, "plan-recovery-selection-worker")
	outcome, err := handler.Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != runtimeworker.OutcomeCommitted || outcome.OwnerResult.State != workitems.StateFailed || outcome.OwnerResult.ReasonCode != "NO_REQUIRED_FILES" || outcome.WriteOwner != nil || outcome.PostTransition != nil {
		t.Fatalf("outcome=%+v, want typed NO_REQUIRED_FILES commit", outcome)
	}
	var excluded int
	if err := db.QueryRow(`SELECT count(*) FROM acquisition_files WHERE acquisition_id='file-fixture-root' AND requirement='excluded'`).Scan(&excluded); err != nil {
		t.Fatal(err)
	}
	if excluded != 2 {
		t.Fatalf("sealed excluded files=%d, want 2", excluded)
	}
	var operations int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM file_operations WHERE artifact_id = $1`, fixture.artifactID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if operations != 0 {
		t.Fatalf("file operations = %d, want exactly 0 for excluded video and subtitle", operations)
	}
}
