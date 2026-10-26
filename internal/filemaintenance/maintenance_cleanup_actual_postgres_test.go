package filemaintenance

import (
	maintenance "bansoko/internal/filemaintenance/executor"
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/library/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A crash after the purge receipt but before the owner commit converges from
// the exact receipt alone.
func TestPGCleanupDeleteFinalReceiptCrashBeforeOwnerCommitRecovery(t *testing.T) {
	f := newCleanupFixture(t)
	ctx := context.Background()
	op := f.admit(t, f.input())
	owner := disposabledb.Owner(t)
	if _, err := owner.Exec(`CREATE FUNCTION test_maintenance_crash_owner() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state<>OLD.state AND NEW.state = 'deleted' THEN RAISE EXCEPTION 'test crash after actual receipt'; END IF; RETURN NEW; END $$; CREATE TRIGGER test_maintenance_crash BEFORE UPDATE ON replacement_deletions FOR EACH ROW EXECUTE FUNCTION test_maintenance_crash_owner()`); err != nil {
		t.Fatal(err)
	}
	claim, err := f.store.ClaimDue(ctx, "actual-delete", []string{workitems.KindReplacementDelete}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	handler := f.service.DeleteHandler()
	outcome, err := handler.Handle(ctx, claim)
	if err != nil || outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("delete effect %+v %v", outcome, err)
	}
	if err := f.store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err == nil {
		t.Fatal("fault did not stop owner commit")
	}
	if _, err := owner.Exec(`DROP TRIGGER test_maintenance_crash ON replacement_deletions; DROP FUNCTION test_maintenance_crash_owner()`); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT expected_owner_version FROM work_item_history WHERE id=$1`, claim.ID).Scan(&claim.ExpectedOwnerVersion); err != nil {
		t.Fatal(err)
	}
	recovered, err := handler.(worker.RecoveryObserver).ObserveRecovery(ctx, claim)
	if err != nil || recovered.Disposition != workitems.DispositionConfirmed {
		t.Fatalf("receipt recovery %+v %v", recovered, err)
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recovered.WriteOwner(ctx, txscope.Wrap(tx), recovered.OwnerResult); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if state, _ := f.state(t, op.ID); state != CleanupDeleted {
		t.Fatalf("state = %s, want deleted", state)
	}
	assertFileAbsent(t, f.source)
	assertFileAbsent(t, filepath.Join(filepath.Dir(f.source), ".bansoko-cleanup-"+op.ID, "object"))
}

// panicBeforeMoveCleanupExecutor crashes after the capture attempt is
// checkpointed but before the executor writes its start marker.
type panicBeforeMoveCleanupExecutor struct {
	filesystem.OSCleanupExecutor
}

func (panicBeforeMoveCleanupExecutor) MoveMaintenance(context.Context, string, string, domain.FileObservation, maintenance.MaintenanceInvocation) (string, error) {
	panic("injected crash before capture start marker")
}

func TestPGCleanupDeleteReclaimedPreStartAttempt(t *testing.T) {
	// No start marker, source intact at its frozen observation and nothing at
	// the capture path: the attempt provably never ran and the deletion runs
	// again. Any other disk state blocks and stays untouched.
	cases := []struct {
		name      string
		perturb   func(t *testing.T, source, target string)
		converges bool
	}{
		{name: "not_started", converges: true},
		// MoveMaintenance creates the private capture directory before its
		// start marker, so a crash can leave it behind without an object.
		{name: "capture_directory_only", converges: true, perturb: func(t *testing.T, _, target string) {
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "source_drifted", perturb: func(t *testing.T, source, _ string) {
			if err := os.WriteFile(source, []byte("drifted after checkpoint"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "target_occupied", perturb: func(t *testing.T, _, target string) {
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("occupied after checkpoint"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newCleanupFixture(t)
			ctx := context.Background()
			op := f.admit(t, f.input())
			target := filepath.Join(filepath.Dir(f.source), ".bansoko-cleanup-"+op.ID, "object")
			f.service.SetFileExecutor(panicBeforeMoveCleanupExecutor{f.executor.OSCleanupExecutor})
			claim, err := f.store.ClaimDue(ctx, "crash-worker", []string{workitems.KindReplacementDelete}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("expected injected crash before capture")
					}
				}()
				_, _ = f.service.DeleteHandler().Handle(ctx, claim)
			}()
			var attempt []byte
			if err := f.db.QueryRow(`SELECT attempt FROM replacement_deletions WHERE id=$1`, op.ID).Scan(&attempt); err != nil {
				t.Fatal(err)
			}
			if state, _ := f.state(t, op.ID); state != CleanupAdmitted || len(attempt) == 0 {
				t.Fatalf("crash checkpoint state=%s attempt=%s", state, attempt)
			}
			if tc.perturb != nil {
				tc.perturb(t, f.source, target)
			}
			sourceBefore, targetBefore := readFileOrNil(t, f.source), readFileOrNil(t, target)
			f.service.SetFileExecutor(f.executor)
			if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, claim.ID); err != nil {
				t.Fatal(err)
			}
			reclaimed, err := f.store.ClaimDue(ctx, "reclaim-worker", []string{workitems.KindReplacementDelete}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := f.service.DeleteHandler().Handle(ctx, reclaimed)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.store.FinishWithResult(ctx, reclaimed, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
				t.Fatal(err)
			}
			state, _ := f.state(t, op.ID)
			if !tc.converges {
				if state != CleanupBlocked {
					t.Fatalf("unproven attempt must block, state=%s", state)
				}
				if !bytes.Equal(readFileOrNil(t, f.source), sourceBefore) || !bytes.Equal(readFileOrNil(t, target), targetBefore) {
					t.Fatal("unresolved attempt mutated the disk")
				}
				return
			}
			if state != CleanupDeleted {
				t.Fatalf("resumed deletion state=%s", state)
			}
			assertFileAbsent(t, f.source)
			assertFileAbsent(t, target)
		})
	}
}
