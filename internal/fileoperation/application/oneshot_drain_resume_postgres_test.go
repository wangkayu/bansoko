package application

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	settlement "bansoko/internal/acquisition/terminal/settlement"
	artifactapplication "bansoko/internal/artifact/application"
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// seedOneShotDrainFixture attaches the existing verified-artifact fixture to
// the exact owner anchors used by reclamation. The plan itself is then created
// through the normal fixture owner path, with its real one-shot WorkItem.
func seedOneShotDrainFixture(t *testing.T, db *sql.DB, markerID string) workitems.OneShotRef {
	t.Helper()
	ref := workitems.OneShotRef{MarkerID: markerID, CorrelationID: "corr-" + markerID, AdmissionEpoch: 1}
	if _, err := db.Exec(`
INSERT INTO one_shot_acquisitions(
    id, managed_anime_id, subscription_id, rss_source_id, release_id, download_task_id,
    correlation_id, request_idempotency_key, request_hash, result_snapshot, state)
VALUES ($1, 'anime-file-test', 'subscription-file-test', 'source-file-test',
        'release-file-test', 'download-file-test', $2, $3, $4, '{}'::jsonb, 'running')`,
		markerID, ref.CorrelationID, "req-key-"+markerID, "sha256:"+strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO one_shot_execution_attempts(id, acquisition_id, sequence, mode, input_snapshot, input_digest, state)
VALUES ('attempt-'||$1, $1, 1, 'initial', '{}'::jsonb, 'sha256:'||repeat('a',64), 'running')`, markerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE one_shot_acquisitions SET current_attempt_id='attempt-'||$1 WHERE id=$1`, markerID); err != nil {
		t.Fatal(err)
	}
	return ref
}

// A4 upgrade regression: artifact verification completed and the plan was
// admitted, but the old drain saw no file operations and mistook the chain for
// terminal. Its cleanup_blocked gate then consumed every plan claim and left
// attempt_exhausted recovery quarantine. Maintenance must repair that persisted
// shape; the ordinary workers must then plan, import the file and settle root.
func TestOneShotFalseDrainBlockedExhaustedPlanResumesThroughFileImport(t *testing.T) {
	db, fixture := openFileOperationDB(t)
	service := newFixtureServiceWithPorts(t, db, fixture, filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	ctx := context.Background()
	const markerID = "m-a4-exhausted-resume"
	ref := seedOneShotDrainFixture(t, db, markerID)

	tx, err := ownerTestDB(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	planID, err := seedCommittedInspectionPlanTx(ctx, txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: fixture.artifactID}, &ref)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	first, err := store.ClaimDue(ctx, "a4-first-plan-claim", []string{workitems.KindPostProcessPlan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitOneShotEffect(ctx, first); err != nil {
		t.Fatalf("admit live plan before false drain: %v", err)
	}
	// This is the exact old drain output: terminal success and an undrained
	// admission ledger while the plan and acquisition root are still live.
	if _, err := db.Exec(`
UPDATE one_shot_acquisitions
SET state='cleanup_blocked', terminal_outcome='succeeded',
    terminal_reason_code='admission_ledger_not_drained',
    terminal_reason='the admission ledger must be empty before reclamation',
    terminal_at=CURRENT_TIMESTAMP, retention_until=CURRENT_TIMESTAMP+INTERVAL '1 day'
WHERE id=$1`, markerID); err != nil {
		t.Fatal(err)
	}

	last := first
	for attempt := 2; attempt <= first.MaxAttempts; attempt++ {
		if _, err := ownerTestDB(t).Exec(`UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, first.ID); err != nil {
			t.Fatal(err)
		}
		last, err = store.ClaimDue(ctx, "a4-refused-plan-claim", []string{workitems.KindPostProcessPlan}, time.Minute)
		if err != nil {
			t.Fatalf("refused claim %d: %v", attempt, err)
		}
		if _, err := store.AdmitOneShotEffect(ctx, last); !errors.Is(err, workitems.ErrOneShotReclaiming) {
			t.Fatalf("blocked gate claim %d error = %v, want ErrOneShotReclaiming", attempt, err)
		}
	}
	if err := store.BlockRecovery(ctx, last, "attempt_exhausted"); err != nil {
		t.Fatalf("quarantine exhausted refused plan: %v", err)
	}
	var attempt int
	var recoveryBlocked bool
	var planState string
	if err := db.QueryRow(`SELECT w.attempt,w.recovery_blocked,p.state FROM work_items w JOIN postprocessing_plans p ON p.id=w.owner_id WHERE w.id=$1`, first.ID).Scan(&attempt, &recoveryBlocked, &planState); err != nil {
		t.Fatal(err)
	}
	if attempt != first.MaxAttempts || !recoveryBlocked || planState != "queued" {
		t.Fatalf("A4 precondition = attempt %d recovery %t plan %q", attempt, recoveryBlocked, planState)
	}
	var operations int
	if err := db.QueryRow(`SELECT count(*) FROM file_operations WHERE artifact_id=$1`, fixture.artifactID).Scan(&operations); err != nil || operations != 0 {
		t.Fatalf("A4 must start without file operations: count=%d err=%v", operations, err)
	}

	reclaimer, err := workitems.NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := reclaimer.RunPass(ctx)
	if err != nil {
		t.Fatalf("maintenance resume: %v", err)
	}
	if result.Revived != 1 {
		t.Fatalf("maintenance result = %+v, want one revived marker", result)
	}
	var markerState string
	var terminalOutcome sql.NullString
	if err := db.QueryRow(`SELECT state,terminal_outcome FROM one_shot_acquisitions WHERE id=$1`, markerID).Scan(&markerState, &terminalOutcome); err != nil {
		t.Fatal(err)
	}
	if markerState != workitems.OneShotStateRunning || terminalOutcome.Valid {
		t.Fatalf("resumed marker = %s outcome=%v", markerState, terminalOutcome)
	}
	if err := db.QueryRow(`SELECT attempt,recovery_blocked,state FROM work_items WHERE id=$1`, first.ID).Scan(&attempt, &recoveryBlocked, &markerState); err != nil {
		t.Fatal(err)
	}
	if attempt != 0 || recoveryBlocked || markerState != workitems.StateAvailable {
		t.Fatalf("repaired plan work = attempt %d recovery=%t state=%s", attempt, recoveryBlocked, markerState)
	}

	runA11PlanWorker(t, db, service)
	if err := db.QueryRow(`SELECT state FROM postprocessing_plans WHERE id=$1`, planID).Scan(&planState); err != nil || planState != "succeeded" {
		t.Fatalf("resumed plan state=%q err=%v", planState, err)
	}
	var operationID, target string
	if err := db.QueryRow(`SELECT id,target_path FROM file_operations WHERE artifact_id=$1 AND file_ref='primary'`, fixture.artifactID).Scan(&operationID, &target); err != nil {
		t.Fatalf("resumed plan created no import operation: %v", err)
	}
	fileWorker := worker.NewWorker(store, "a4-file-worker", time.Minute)
	if err := fileWorker.Register(workitems.KindFileExecute, service); err != nil {
		t.Fatal(err)
	}
	if handled, err := fileWorker.RunOnce(ctx); err != nil || !handled {
		t.Fatalf("resumed file import: handled=%t err=%v", handled, err)
	}
	if raw, err := os.ReadFile(target); err != nil || string(raw) != "source-content" {
		t.Fatalf("imported target content=%q err=%v", raw, err)
	}

	settler, err := settlement.NewSettlementService(db)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := settler.SettleRoot(ctx, "file-fixture-root"); err != nil || status != "settled" {
		t.Fatalf("settle imported root: status=%q err=%v", status, err)
	}
	var rootState string
	if err := db.QueryRow(`SELECT state FROM acquisitions WHERE id='file-fixture-root'`).Scan(&rootState); err != nil || rootState != "succeeded" {
		t.Fatalf("root after import = %q err=%v", rootState, err)
	}
	result, err = reclaimer.RunPass(ctx)
	if err != nil {
		t.Fatalf("drain completed chain: %v", err)
	}
	if result.Drained != 1 {
		t.Fatalf("completed-chain maintenance = %+v, want drained=1", result)
	}
	if err := db.QueryRow(`SELECT state,terminal_outcome FROM one_shot_acquisitions WHERE id=$1`, markerID).Scan(&markerState, &terminalOutcome); err != nil {
		t.Fatal(err)
	}
	if markerState != workitems.OneShotStateReclaimPending || !terminalOutcome.Valid || terminalOutcome.String != "succeeded" {
		t.Fatalf("final marker = %s outcome=%v", markerState, terminalOutcome)
	}
}

func blockOneShotDrainMarker(t *testing.T, db *sql.DB, markerID string) {
	t.Helper()
	if _, err := db.Exec(`
UPDATE one_shot_acquisitions
SET state='cleanup_blocked', terminal_outcome='succeeded',
    terminal_reason_code='admission_ledger_not_drained',
    terminal_reason='the admission ledger must be empty before reclamation',
    terminal_at=CURRENT_TIMESTAMP, retention_until=CURRENT_TIMESTAMP+INTERVAL '1 day'
WHERE id=$1`, markerID); err != nil {
		t.Fatal(err)
	}
}

func seedOneShotDrainPlan(t *testing.T, db *sql.DB, artifactID string, ref workitems.OneShotRef) string {
	t.Helper()
	tx, err := ownerTestDB(t).BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	planID, err := seedCommittedInspectionPlanTx(context.Background(), txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: artifactID}, &ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return planID
}

func TestOneShotDrainLeavesAdmittedQueuedPlanRunning(t *testing.T) {
	db, fixture := openFileOperationDB(t)
	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	ctx := context.Background()
	const markerID = "m-a4-live-plan-drain"
	ref := seedOneShotDrainFixture(t, db, markerID)
	seedOneShotDrainPlan(t, db, fixture.artifactID, ref)
	claim, err := store.ClaimDue(ctx, "a4-live-plan", []string{workitems.KindPostProcessPlan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitOneShotEffect(ctx, claim); err != nil {
		t.Fatal(err)
	}
	reclaimer, err := workitems.NewOneShotReclaimService(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := reclaimer.RunPass(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Drained != 0 || result.Blocked != 0 {
		t.Fatalf("live plan drain = %+v, want no mutation", result)
	}
	var state string
	var terminalOutcome sql.NullString
	if err := db.QueryRow(`SELECT state,terminal_outcome FROM one_shot_acquisitions WHERE id=$1`, markerID).Scan(&state, &terminalOutcome); err != nil {
		t.Fatal(err)
	}
	if state != workitems.OneShotStateRunning || terminalOutcome.Valid {
		t.Fatalf("live plan marker = %s outcome=%v", state, terminalOutcome)
	}
}

func TestOneShotFalseDrainGateRefusalReturnsClaimWithoutAttempt(t *testing.T) {
	db, fixture := openFileOperationDB(t)
	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	ctx := context.Background()
	const markerID = "m-a4-gate-resume"
	ref := seedOneShotDrainFixture(t, db, markerID)
	seedOneShotDrainPlan(t, db, fixture.artifactID, ref)
	first, err := store.ClaimDue(ctx, "a4-gate-first", []string{workitems.KindPostProcessPlan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitOneShotEffect(ctx, first); err != nil {
		t.Fatal(err)
	}
	blockOneShotDrainMarker(t, db, markerID)
	if _, err := ownerTestDB(t).Exec(`UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	refused, err := store.ClaimDue(ctx, "a4-gate-refused", []string{workitems.KindPostProcessPlan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitOneShotEffect(ctx, refused); !errors.Is(err, workitems.ErrOneShotReclaiming) {
		t.Fatalf("gate error = %v, want ErrOneShotReclaiming", err)
	}
	resumed, err := store.ResumeRefusedOneShotClaim(ctx, refused)
	if err != nil || !resumed {
		t.Fatalf("resume refused claim: resumed=%t err=%v", resumed, err)
	}
	var state, markerState string
	var attempt, started, leases int
	if err := db.QueryRow(`SELECT w.state,w.attempt,p.plan_attempts_started,m.state,m.active_effect_count
FROM work_items w JOIN postprocessing_plans p ON p.id=w.owner_id
JOIN one_shot_acquisitions m ON m.id=$2 WHERE w.id=$1`, first.ID, markerID).Scan(&state, &attempt, &started, &markerState, &leases); err != nil {
		t.Fatal(err)
	}
	if state != workitems.StateAvailable || attempt != 0 || started != 0 || markerState != workitems.OneShotStateRunning || leases != 1 {
		t.Fatalf("resumed claim state=%s attempt=%d started=%d marker=%s leases=%d", state, attempt, started, markerState, leases)
	}
	next, err := store.ClaimDue(ctx, "a4-gate-next", []string{workitems.KindPostProcessPlan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitOneShotEffect(ctx, next); err != nil {
		t.Fatalf("next claim was not admitted with the preserved lease: %v", err)
	}
}

func TestOneShotFalseDrainResumeKeepsRealRecoveryQuarantine(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		start  bool
		cancel bool
	}{
		{name: "effect_unknown", reason: "effect_unknown"},
		{name: "cancel_requested", reason: "cancel_requested", cancel: true},
		{name: "plan_attempt_started", reason: "attempt_exhausted", start: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, fixture := openFileOperationDB(t)
			store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
			ctx := context.Background()
			markerID := "m-a4-quarantine-" + tc.name
			ref := seedOneShotDrainFixture(t, db, markerID)
			planID := seedOneShotDrainPlan(t, db, fixture.artifactID, ref)
			claim, err := store.ClaimDue(ctx, "a4-quarantine-claim", []string{workitems.KindPostProcessPlan}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AdmitOneShotEffect(ctx, claim); err != nil {
				t.Fatal(err)
			}
			if tc.start {
				if _, err := store.StartStageAttempt(ctx, claim, "a4-real-plan-attempt"); err != nil {
					t.Fatal(err)
				}
			}
			blockOneShotDrainMarker(t, db, markerID)
			if tc.cancel {
				if _, err := ownerTestDB(t).Exec(`UPDATE work_items SET cancel_requested=true WHERE id=$1`, claim.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.BlockRecovery(ctx, claim, tc.reason); err != nil {
				t.Fatal(err)
			}
			reclaimer, err := workitems.NewOneShotReclaimService(db, nil)
			if err != nil {
				t.Fatal(err)
			}
			result, err := reclaimer.RunPass(ctx)
			if err != nil || result.Revived != 1 {
				t.Fatalf("maintenance result=%+v err=%v", result, err)
			}
			var blocked bool
			var reason, markerState string
			var started int
			if err := db.QueryRow(`SELECT w.recovery_blocked,w.recovery_reason,p.plan_attempts_started,m.state
FROM work_items w JOIN postprocessing_plans p ON p.id=w.owner_id
JOIN one_shot_acquisitions m ON m.id=$2 WHERE w.id=$1 AND p.id=$3`, claim.ID, markerID, planID).Scan(&blocked, &reason, &started, &markerState); err != nil {
				t.Fatal(err)
			}
			wantStarted := 0
			if tc.start {
				wantStarted = 1
			}
			if !blocked || reason != tc.reason || started != wantStarted || markerState != workitems.OneShotStateRunning {
				t.Fatalf("quarantine blocked=%t reason=%q started=%d marker=%s", blocked, reason, started, markerState)
			}
		})
	}
}
