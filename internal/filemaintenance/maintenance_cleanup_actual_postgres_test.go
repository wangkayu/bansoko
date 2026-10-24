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
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPGCleanupActualHandlersFinalReceiptCrashBeforeOwnerCommitRecovery(t *testing.T) {
	for _, kind := range []string{workitems.KindCleanupQuarantine, workitems.KindCleanupPurge} {
		t.Run(kind, func(t *testing.T) {
			db := disposabledb.ResetDisposableSchema(t)
			ctx := context.Background()
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(root, "source")
			target := filepath.Join(root, ".bansoko-cleanup-actual-cleanup", "object")
			if err := os.WriteFile(source, []byte("actual cleanup"), 0600); err != nil {
				t.Fatal(err)
			}
			e := filesystem.NewOSCleanupExecutor(root)
			observation, err := e.Observe(ctx, source)
			if err != nil {
				t.Fatal(err)
			}
			id := seedCleanupOperation(t, db, "actual-cleanup", "anime-cleanup", source, observation.Fingerprint, "quarantine", "")
			s := NewCleanupService(db)
			store := cleanupWorkItemStore(t, db)
			s.SetWorkItems(store)
			s.SetFileExecutor(e)
			c := cleanupClaim(t, store, id, workitems.KindCleanupQuarantine, 1)
			if kind == workitems.KindCleanupPurge {
				o, err := s.QuarantineHandler().Handle(ctx, c)
				if err != nil || o.TerminalState != workitems.StateSucceeded {
					t.Fatalf("setup quarantine %+v %v", o, err)
				}
				c, err = store.ClaimDue(ctx, "actual-purge", []string{kind}, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
			}
			// Inject a DB failure after real effect+final marker but before owner commit.
			owner := disposabledb.Owner(t)
			if _, err := owner.Exec(`CREATE FUNCTION test_maintenance_crash_owner() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state<>OLD.state AND NEW.state IN ('quarantined','grace_waiting','purged') THEN RAISE EXCEPTION 'test crash after actual receipt'; END IF; RETURN NEW; END $$; CREATE TRIGGER test_maintenance_crash BEFORE UPDATE ON cleanup_operations FOR EACH ROW EXECUTE FUNCTION test_maintenance_crash_owner()`); err != nil {
				t.Fatal(err)
			}
			var handler worker.Handler = s.QuarantineHandler()
			if kind == workitems.KindCleanupPurge {
				handler = s.PurgeHandler()
			}
			if _, err := handler.Handle(ctx, c); err == nil {
				t.Fatal("fault did not stop owner commit")
			}
			if _, err := owner.Exec(`DROP TRIGGER test_maintenance_crash ON cleanup_operations; DROP FUNCTION test_maintenance_crash_owner()`); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT expected_owner_version FROM work_item_history WHERE id=$1`, c.ID).Scan(&c.ExpectedOwnerVersion); err != nil {
				t.Fatal(err)
			}
			recovered, err := handler.(worker.RecoveryObserver).ObserveRecovery(ctx, c)
			if err != nil || recovered.Disposition != workitems.DispositionConfirmed {
				t.Fatalf("receipt recovery %+v %v", recovered, err)
			}
			tx, err := db.BeginTx(ctx, nil)
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
			var state string
			if err := db.QueryRow(`SELECT state FROM cleanup_operations WHERE id=$1`, id).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if kind == workitems.KindCleanupPurge {
				if state != "purged" {
					t.Fatal(state)
				}
				if _, err := os.Stat(target); !os.IsNotExist(err) {
					t.Fatalf("purge object remains %v", err)
				}
			} else {
				if state != "quarantined" {
					t.Fatal(state)
				}
				if _, err := os.Stat(target); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// panicBeforeMoveCleanupExecutor crashes after the quarantine attempt is
// checkpointed but before the executor writes its start marker.
type panicBeforeMoveCleanupExecutor struct {
	filesystem.OSCleanupExecutor
}

func (panicBeforeMoveCleanupExecutor) MoveMaintenance(context.Context, string, string, domain.FileObservation, maintenance.MaintenanceInvocation) (string, error) {
	panic("injected crash before quarantine start marker")
}

func TestPGCleanupQuarantineReclaimedPreStartAttempt(t *testing.T) {
	// No start marker, source intact at its frozen observation and nothing at
	// the quarantine target: the attempt provably never ran and must converge.
	// Any other disk state stays unresolved and untouched.
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
			db := disposabledb.ResetDisposableSchema(t)
			ctx := context.Background()
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(root, "source")
			if err := os.WriteFile(source, []byte("pre-start cleanup"), 0600); err != nil {
				t.Fatal(err)
			}
			e := filesystem.NewOSCleanupExecutor(root)
			observation, err := e.Observe(ctx, source)
			if err != nil {
				t.Fatal(err)
			}
			id := seedCleanupOperation(t, db, "prestart-cleanup", "anime-cleanup", source, observation.Fingerprint, "quarantine", "")
			target := filepath.Join(root, ".bansoko-cleanup-"+id, "object")
			s := NewCleanupService(db)
			store := cleanupWorkItemStore(t, db)
			s.SetWorkItems(store)
			s.SetFileExecutor(panicBeforeMoveCleanupExecutor{e})
			c := cleanupClaim(t, store, id, workitems.KindCleanupQuarantine, 1)
			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("expected injected crash before quarantine")
					}
				}()
				_, _ = s.QuarantineHandler().Handle(ctx, c)
			}()
			var state string
			var attempt []byte
			if err := db.QueryRow(`SELECT state, attempt FROM cleanup_operations WHERE id=$1`, id).Scan(&state, &attempt); err != nil {
				t.Fatal(err)
			}
			if state != "admitted" || len(attempt) == 0 {
				t.Fatalf("crash checkpoint state=%s attempt=%s", state, attempt)
			}
			if tc.perturb != nil {
				tc.perturb(t, source, target)
			}
			sourceBefore, targetBefore := readFileOrNil(t, source), readFileOrNil(t, target)
			s.SetFileExecutor(e)
			if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, c.ID); err != nil {
				t.Fatal(err)
			}
			reclaimed, err := store.ClaimDue(ctx, "reclaim-worker", []string{workitems.KindCleanupQuarantine}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := s.QuarantineHandler().Handle(ctx, reclaimed)
			if !tc.converges {
				if !errors.Is(err, workitems.ErrPublicationUnresolved) {
					t.Fatalf("unproven attempt must remain unresolved: %+v %v", outcome, err)
				}
				if !bytes.Equal(readFileOrNil(t, source), sourceBefore) || !bytes.Equal(readFileOrNil(t, target), targetBefore) {
					t.Fatal("unresolved attempt mutated the disk")
				}
				var after []byte
				if err := db.QueryRow(`SELECT state, attempt FROM cleanup_operations WHERE id=$1`, id).Scan(&state, &after); err != nil {
					t.Fatal(err)
				}
				if state != "admitted" || !bytes.Equal(after, attempt) {
					t.Fatalf("unresolved attempt changed owner state=%s", state)
				}
				return
			}
			if err != nil || outcome.TerminalState != workitems.StateSucceeded {
				t.Fatalf("resumed quarantine %+v %v", outcome, err)
			}
			var quarantinePath string
			if err := db.QueryRow(`SELECT state, quarantine_path FROM cleanup_operations WHERE id=$1`, id).Scan(&state, &quarantinePath); err != nil {
				t.Fatal(err)
			}
			if state != "quarantined" || quarantinePath != target {
				t.Fatalf("resumed quarantine state=%s path=%s", state, quarantinePath)
			}
			if _, err := os.Stat(source); !os.IsNotExist(err) {
				t.Fatalf("source remains after resumed quarantine: %v", err)
			}
			if !bytes.Equal(readFileOrNil(t, target), sourceBefore) {
				t.Fatal("quarantine object does not carry the frozen source bytes")
			}
		})
	}
}
