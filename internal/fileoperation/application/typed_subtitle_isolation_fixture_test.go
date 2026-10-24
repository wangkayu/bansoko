package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"testing"

	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// After the cutover a typed (prepared) operation routes to
// handleTypedExecution, and a claim whose input is
// missing or invalid reaches the visible terminal file_operation_input_invalid
// -- the correct fail-closed outcome. The isolation invariants that must
// still hold:
//
//	(a) no filesystem effect: executor/observer never called, no target
//	    created or mutated;
//	(b) the outcome is a typed terminal failure or a blocked result, never
//	    success and never a committed/post-transition result;
//	(c) an unfenced claim cannot write the owner: the deferred WriteOwner only
//	    runs inside the WorkItem fencing transaction, which refuses this claim
//	    (asserted directly below; the general stale/unfenced coverage lives in
//	    workitems TestStaleFencingTokenRejected and
//	    TestMutateClaimRollsBackOwnerAndRejectsStale).

// An actual prepared dependent subtitle has no initial work; nothing invents a claim
// or continuation for it, and a forged claim fails closed without effect.
func assertTypedSubtitleExecutionIsolation(t *testing.T) {
	t.Helper()
	db, s, c, _ := preparedSubtitleBatchFixture(t)
	if _, err := s.PostProcessingPlanHandler().Handle(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRow(`SELECT id FROM file_operations WHERE artifact_file_id='set-sub'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE owner_id=$1 AND kind='file_execute'`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("dependent subtitle received premature work")
	}
	assertTypedForgedClaimFailsClosed(t, db, s, id)
}

// assertTypedForgedClaimFailsClosed drives Service.Handle with an unfenced
// claim (no work item id, no token, no input snapshot) for the typed operation
// id and proves invariants (a)-(c) above.
func assertTypedForgedClaimFailsClosed(t *testing.T, db *sql.DB, s *Service, id string) {
	t.Helper()
	ctx := context.Background()
	if typed, err := s.operationExists(ctx, workitems.Claim{Owner: workitems.OwnerRef{Type: workitems.OwnerFileOperation, ID: id}}); err != nil || !typed {
		t.Fatalf("operation %s is not routed to the typed lane: typed=%v err=%v", id, typed, err)
	}
	ex := &testExecutor{}
	ob := &testObserver{}
	s.executor = ex
	s.observer = ob
	targetsBefore := typedTargetState(t, db)
	before := planGraphSnapshot(t, db)
	forged := workitems.Claim{Owner: workitems.OwnerRef{Type: workitems.OwnerFileOperation, ID: id}, Kind: workitems.KindFileExecute}
	out, err := s.Handle(ctx, forged)
	if err != nil {
		// B5: typed failures go through the fixed 184 command, whose gate
		// refuses an unfenced claim — an error with zero effect and zero write.
		if ex.calls != 0 || ob.calls != 0 || planGraphSnapshot(t, db) != before || typedTargetState(t, db) != targetsBefore {
			t.Fatalf("refused forged typed claim still had an effect: %v", err)
		}
		return
	}
	failedTerminal := out.Kind == worker.OutcomeFinish && out.TerminalState == "failed" && out.OwnerResult.State == "failed" && out.OwnerResult.ReasonCode == "file_operation_input_invalid"
	blocked := out.OwnerResult.State == "blocked" && out.WriteOwner == nil
	if !(failedTerminal || blocked) || out.Kind == worker.OutcomeCommitted || out.CommittedClaim != nil || out.PostTransition != nil {
		t.Fatalf("forged typed claim outcome=%+v; want typed terminal file_operation_input_invalid or blocked, never success", out)
	}
	if ex.calls != 0 || ob.calls != 0 {
		t.Fatalf("forged typed claim reached the effect ports: executor=%d observer=%d", ex.calls, ob.calls)
	}
	if planGraphSnapshot(t, db) != before {
		t.Fatal("forged typed claim changed the graph")
	}
	if got := typedTargetState(t, db); got != targetsBefore {
		t.Fatalf("forged typed claim touched a target:\nbefore %s\nafter  %s", targetsBefore, got)
	}
	if out.WriteOwner != nil {
		store, err := workitems.NewStore(db, 5, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.FinishWithResult(ctx, forged, out.TerminalState, out.OwnerResult, out.WriteOwner); err == nil {
			t.Fatal("an unfenced claim applied its owner write")
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatal("refused unfenced owner write changed the graph")
		}
	}
}

// typedTargetState fingerprints every planned target path (existence, size,
// mode, mtime) so any publication or target mutation shows up.
func typedTargetState(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT id, target_path FROM file_operations ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	state := ""
	for rows.Next() {
		var id, target string
		if err := rows.Scan(&id, &target); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(target)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			state += fmt.Sprintf("%s=absent;", id)
		case err != nil:
			t.Fatalf("stat %s: %v", target, err)
		default:
			state += fmt.Sprintf("%s=%d/%v/%d;", id, info.Size(), info.Mode(), info.ModTime().UnixNano())
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return state
}
