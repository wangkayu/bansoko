package application

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"bansoko/internal/acquisition/terminal/settlement"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// installFinishFault makes every finish of a closed proof roll back inside
// its atomic body, as the notification proof mismatch fixed by 070 did: the
// notification admission is the finish body's own write.
func installFinishFault(t *testing.T) func() {
	t.Helper()
	owner := ownerTestDB(t)
	if _, err := owner.Exec(`CREATE FUNCTION test_finish_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='FILE_NOTIFICATION_PROOF_MISMATCH'; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(`CREATE TRIGGER test_finish_fault BEFORE INSERT ON notification_admissions FOR EACH ROW EXECUTE FUNCTION test_finish_fault()`); err != nil {
		t.Fatal(err)
	}
	removed := false
	remove := func() {
		if removed {
			return
		}
		removed = true
		if _, err := owner.Exec(`DROP TRIGGER IF EXISTS test_finish_fault ON notification_admissions; DROP FUNCTION IF EXISTS test_finish_fault()`); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(remove)
	return remove
}

type finishWorkState struct {
	live, recoveryBlocked bool
	outcome               string
	attempt, maxAttempts  int
}

func (x typedRuntimeFixture) finishWork(t *testing.T) finishWorkState {
	t.Helper()
	var st finishWorkState
	if err := x.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM work_items WHERE id=$1),
 COALESCE((SELECT recovery_blocked FROM work_items WHERE id=$1),false),
 COALESCE((SELECT state FROM work_item_outcomes WHERE id=$1),''),
 (SELECT attempt FROM work_item_history WHERE id=$1),(SELECT max_attempts FROM work_item_history WHERE id=$1)`, x.workID).
		Scan(&st.live, &st.recoveryBlocked, &st.outcome, &st.attempt, &st.maxAttempts); err != nil {
		t.Fatal(err)
	}
	return st
}

func (x typedRuntimeFixture) makeWorkDue(t *testing.T) {
	t.Helper()
	if _, err := ownerTestDB(t).Exec(`UPDATE work_items SET available_at=CURRENT_TIMESTAMP-INTERVAL '1 second', lease_until=CASE WHEN state='leased' THEN CURRENT_TIMESTAMP-INTERVAL '1 second' END WHERE id=$1`, x.workID); err != nil {
		t.Fatal(err)
	}
}

// observeRecoveryCommitted runs one recovery observation exactly as the runtime
// recovery lane does and requires the fixed finish to have committed.
func (x typedRuntimeFixture) observeRecoveryCommitted(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	c, err := x.store.ClaimRecovery(ctx, []string{workitems.KindFileExecute}, time.Minute)
	if err != nil {
		t.Fatalf("recovery claim: %v", err)
	}
	if c.ID != x.workID {
		t.Fatalf("recovery claimed %s, want %s", c.ID, x.workID)
	}
	out, err := x.s.ObserveRecovery(ctx, c)
	if err != nil || out.Kind != worker.OutcomeCommitted {
		t.Fatalf("recovery observation: %+v %v", out, err)
	}
}

func (x typedRuntimeFixture) requireCompletedAndSettled(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if got := x.state(t, x.opID); got.opState != ownerStateCompleted || got.workState != workitems.StateSucceeded {
		t.Fatalf("operation not completed: %+v", got)
	}
	if st := x.finishWork(t); st.live || st.outcome != workitems.StateSucceeded {
		t.Fatalf("finish work not succeeded: %+v", st)
	}
	var admissions int
	if err := x.db.QueryRow(`SELECT count(*) FROM notification_admissions WHERE source_owner_type='file_operation' AND source_owner_id=$1`, x.opID).Scan(&admissions); err != nil || admissions != 1 {
		t.Fatalf("notification admissions=%d %v", admissions, err)
	}
	root := acquisitionOf(t, x.db, x.opID)
	settler, err := settlement.NewSettlementService(x.db)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := settler.SettleRoot(ctx, root); err != nil || status != "settled" {
		t.Fatalf("settle root: status=%q err=%v", status, err)
	}
	var state string
	if err := x.db.QueryRow(`SELECT state FROM acquisitions WHERE id=$1`, root).Scan(&state); err != nil || state != "succeeded" {
		t.Fatalf("root state=%q %v, want succeeded", state, err)
	}
}

// A finish of a closed proof that keeps rolling back is retried without
// failing at the attempt budget: the exhausted claim is quarantined for the
// recovery lane, the operation stays pending with live work, and once the
// cause is gone the recovery observation completes it.
func TestTypedFinishRollbackQuarantinesAtBudget(t *testing.T) {
	x := newTypedRuntimeFixture(t, nil, false)
	removeFault := installFinishFault(t)
	for i := 0; ; i++ {
		if i > 100 {
			t.Fatal("finish retries never reached the attempt budget")
		}
		// The publishing run reports the rolled-back finish as a handler
		// error (its claim stays leased for reclaim); every re-claim
		// converges on the closed proof and retries the finish.
		if handled, err := x.runFileWorker(t); !handled || (err != nil && (i != 0 || !errors.Is(err, ErrFinishNotCommitted))) {
			t.Fatalf("run %d: handled=%v err=%v", i, handled, err)
		}
		st := x.finishWork(t)
		if !st.live {
			t.Fatalf("finish work left the live queue after run %d: %+v", i, st)
		}
		if st.recoveryBlocked {
			if st.attempt != st.maxAttempts {
				t.Fatalf("quarantined before the budget: %+v", st)
			}
			break
		}
		x.makeWorkDue(t)
	}
	if got := x.state(t, x.opID); got.opState != "pending" {
		t.Fatalf("operation must stay pending while quarantined: %+v", got)
	}
	if x.attemptsStarted(t) != 1 {
		t.Fatalf("a closed proof must never start another attempt: %d", x.attemptsStarted(t))
	}
	removeFault()
	x.makeWorkDue(t)
	x.observeRecoveryCommitted(t)
	x.requireCompletedAndSettled(t)
}

// The release before this fix failed the finish work at its budget with a
// writer-less ordinary retry, stranding the operation pending and its root
// running. The revival pass re-opens exactly that work under its own id for
// the recovery lane, which completes the operation; the root then settles.
func TestTypedStrandedFinishRevival(t *testing.T) {
	ctx := context.Background()
	x := newTypedRuntimeFixture(t, nil, false)
	removeFault := installFinishFault(t)
	for i := 0; ; i++ {
		if i > 100 {
			t.Fatal("finish retries never reached the attempt budget")
		}
		c, err := x.store.ClaimDue(ctx, "typed-old-worker", []string{workitems.KindFileExecute}, time.Minute)
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		out, err := x.s.Handle(ctx, c)
		if i == 0 && errors.Is(err, ErrFinishNotCommitted) {
			// The publishing run: the claim stays leased until it expires.
			x.makeWorkDue(t)
			continue
		}
		if err != nil || out.Kind != worker.OutcomeRetry || out.WriteOwner != nil {
			t.Fatalf("handle %d: %+v %v", i, out, err)
		}
		// The previous release applied this outcome as an ordinary budgeted
		// retry.
		res, err := x.store.Retry(ctx, c, out.ReasonCode, out.Reason, 0, nil)
		if err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
		if res.Terminal {
			break
		}
	}
	if st := x.finishWork(t); st.live || st.outcome != workitems.StateFailed {
		t.Fatalf("stranded state not reproduced: %+v", st)
	}
	if got := x.state(t, x.opID); got.opState != "pending" {
		t.Fatalf("stranded operation must be pending: %+v", got)
	}
	// The cause is gone (070 shipped). Revival only re-opens the work; the
	// recovery lane re-drives the finish.
	removeFault()
	result, err := x.s.ReviveStrandedWork(ctx)
	if err != nil || len(result.Revived) != 1 || result.Revived[0] != x.opID || len(result.Refused) != 0 {
		t.Fatalf("revival pass: %+v %v", result, err)
	}
	if st := x.finishWork(t); !st.live || !st.recoveryBlocked || st.outcome != "" {
		t.Fatalf("revived work: %+v", st)
	}
	// A second pass finds nothing: the operation has live work again.
	if again, err := x.s.ReviveStrandedWork(ctx); err != nil || len(again.Revived) != 0 || len(again.Refused) != 0 {
		t.Fatalf("repeated revival pass: %+v %v", again, err)
	}
	// The ordinary claim path never takes recovery-blocked work.
	if _, err := x.store.ClaimDue(ctx, "typed-worker", []string{workitems.KindFileExecute}, time.Minute); !errors.Is(err, workitems.ErrNoDueWork) {
		t.Fatalf("revived work reached the business claim: %v", err)
	}
	x.observeRecoveryCommitted(t)
	x.requireCompletedAndSettled(t)
	if x.attemptsStarted(t) != 1 {
		t.Fatalf("revival must never start another attempt: %d", x.attemptsStarted(t))
	}
	if after, err := x.s.ReviveStrandedWork(ctx); err != nil || len(after.Revived) != 0 || len(after.Refused) != 0 {
		t.Fatalf("revival after completion: %+v %v", after, err)
	}
}

// Work stranded before any publication (the previous release's writer-less
// file_attempt_deadline retry on the last attempt) has no evidence: the
// revived work converges without starting an effect and ends the operation
// visibly; the root settles failed instead of running forever.
func TestTypedStrandedWorkWithoutEvidenceEndsVisibly(t *testing.T) {
	ctx := context.Background()
	x := newTypedRuntimeFixture(t, nil, false)
	if _, err := ownerTestDB(t).Exec(`UPDATE work_items SET max_attempts=1 WHERE id=$1`, x.workID); err != nil {
		t.Fatal(err)
	}
	c, err := x.store.ClaimDue(ctx, "typed-old-worker", []string{workitems.KindFileExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := x.store.Retry(ctx, c, "file_attempt_deadline", "file preparation exceeded its attempt deadline before any publication", 0, nil); err != nil || !res.Terminal {
		t.Fatalf("budgeted retry: %+v %v", res, err)
	}
	if got := x.state(t, x.opID); got.opState != "pending" || got.workState != workitems.StateFailed {
		t.Fatalf("stranded state not reproduced: %+v", got)
	}
	result, err := x.s.ReviveStrandedWork(ctx)
	if err != nil || len(result.Revived) != 1 || result.Revived[0] != x.opID || len(result.Refused) != 0 {
		t.Fatalf("revival pass: %+v %v", result, err)
	}
	x.observeRecoveryCommitted(t)
	if got := x.state(t, x.opID); got.opState != ownerStateFailed || got.opCode != "FILE_EXECUTION_INTERRUPTED" || got.workState != workitems.StateFailed {
		t.Fatalf("revived work must end visibly: %+v", got)
	}
	if x.attemptsStarted(t) != 0 {
		t.Fatalf("recovery must never start an attempt: %d", x.attemptsStarted(t))
	}
	if _, err := os.Lstat(x.target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target touched without a publication: %v", err)
	}
	root := acquisitionOf(t, x.db, x.opID)
	settler, err := settlement.NewSettlementService(x.db)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := settler.SettleRoot(ctx, root); err != nil || status != "settled" {
		t.Fatalf("settle root: status=%q err=%v", status, err)
	}
	var state string
	if err := x.db.QueryRow(`SELECT state FROM acquisitions WHERE id=$1`, root).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("root state=%q %v, want failed", state, err)
	}
}
