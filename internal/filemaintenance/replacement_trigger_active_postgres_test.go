package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// triggerScanDuplicate seeds, in single_copy, a downloaded release placement
// and an unclaimed scan-managed duplicate of the same episode backed by a real
// file. It returns the duplicate's path and the cleanup service wired to it.
func triggerScanDuplicate(t *testing.T, db *sql.DB, store *workitems.Store) (string, *CleanupService) {
	t.Helper()
	setAdmissionMode(t, db, ModeSingleCopy)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scanPath := filepath.Join(dir, "Scanned", "EP01.mkv")
	if err := os.MkdirAll(filepath.Dir(scanPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scanPath, []byte("scanned-duplicate"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor := filesystem.NewOSCleanupExecutor(dir)
	observed, err := executor.ObserveMetadataFile(context.Background(), scanPath)
	if err != nil {
		t.Fatal(err)
	}
	seedScanManagedPlacement(t, db, "plc-scan-dup", "anime-1", scanPath, observed.Fingerprint, "", []string{"tv:s1:e1"}, "2026-07-01T00:00:00Z")
	seedFilePlacement(t, db, "anime-1", "sub-1", "trigger-placed", []string{"tv:s1:e1"},
		Facts{Resolution: "1080p", Group: "G"}, "2026-08-01T00:00:00Z")
	cleanup := NewCleanupService(db)
	cleanup.SetFileExecutor(executor)
	cleanup.SetWorkItems(store)
	return scanPath, cleanup
}

// runLiveEvaluate claims and runs the anime's live replacement_evaluate item
// through the real handler; false when there is none.
func runLiveEvaluate(t *testing.T, db *sql.DB, store *workitems.Store, service *ReplacementDecisionService) bool {
	t.Helper()
	ctx := context.Background()
	if countLiveEvaluateItems(t, db, "anime-1") == 0 {
		return false
	}
	claim, err := store.ClaimDue(ctx, "trigger-evaluate", []string{workitems.KindReplacementEvaluate}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.EvaluateHandler().Handle(ctx, claim)
	if err != nil || outcome.Kind != worker.OutcomeFinish {
		t.Fatalf("evaluate outcome=%+v err=%v", outcome, err)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	return true
}

// Every trigger that can enqueue replacement_evaluate is inert on a stopped
// subscription: no item, no decision, no supersede, no deletion — even with
// an unclaimed scan duplicate a single_copy comparison would delete.
func TestReplacementTriggersAreInertOnStoppedSubscription(t *testing.T) {
	triggers := map[string]func(ctx context.Context, db *sql.DB, trigger *ReplacementTriggerService) error{
		TriggerGenerationCompleted: func(ctx context.Context, _ *sql.DB, trigger *ReplacementTriggerService) error {
			return trigger.TriggerGenerationCompleted(ctx, "anime-1")
		},
		TriggerSubscriptionResumed: func(ctx context.Context, _ *sql.DB, trigger *ReplacementTriggerService) error {
			return trigger.TriggerSubscriptionResumed(ctx, "anime-1")
		},
		TriggerPlacementCommitted: func(ctx context.Context, _ *sql.DB, trigger *ReplacementTriggerService) error {
			return trigger.TriggerPlacementCommitted(ctx, "anime-1", "trigger-placed")
		},
		TriggerPolicySaved: func(ctx context.Context, _ *sql.DB, trigger *ReplacementTriggerService) error {
			return trigger.TriggerPolicySaved(ctx, "anime-1")
		},
		"placement_recovery": func(ctx context.Context, db *sql.DB, trigger *ReplacementTriggerService) error {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if err := trigger.TriggerPlacementRecoveryTx(ctx, txscope.Wrap(tx), "anime-1", "trigger-placed"); err != nil {
				return err
			}
			return tx.Commit()
		},
	}
	for name, fire := range triggers {
		t.Run(name, func(t *testing.T) {
			db, store, trigger := newTriggerTestDB(t)
			defer db.Close()
			ctx := context.Background()
			scanPath, cleanup := triggerScanDuplicate(t, db, store)
			if _, err := db.Exec(`UPDATE subscriptions SET state = 'stopped', version = version + 1 WHERE managed_anime_id = 'anime-1'`); err != nil {
				t.Fatal(err)
			}
			if err := fire(ctx, db, trigger); err != nil {
				t.Fatal(err)
			}
			service := NewReplacementDecisionService(db)
			service.SetCleanupHandoff(cleanup)
			if runLiveEvaluate(t, db, store, service) {
				t.Fatalf("%s must not enqueue an evaluation on a stopped subscription", name)
			}
			var decisions int
			if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_decisions WHERE managed_anime_id = 'anime-1'`).Scan(&decisions); err != nil {
				t.Fatal(err)
			}
			_, deletions, superseded := decisionCounts(t, db, "anime-1")
			if decisions != 0 || deletions != 0 || superseded != 0 {
				t.Fatalf("stopped subscription: decisions=%d deletions=%d superseded=%d", decisions, deletions, superseded)
			}
			if _, err := os.Stat(scanPath); err != nil {
				t.Fatalf("the scan duplicate must stay: %v", err)
			}
		})
	}
}

// An evaluate item enqueued while active and run after a stop decides nothing.
func TestReplacementEvaluateQueuedBeforeStopDecidesNothing(t *testing.T) {
	db, store, trigger := newTriggerTestDB(t)
	defer db.Close()
	ctx := context.Background()
	scanPath, cleanup := triggerScanDuplicate(t, db, store)
	if err := trigger.TriggerGenerationCompleted(ctx, "anime-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE subscriptions SET state = 'stopped', version = version + 1 WHERE managed_anime_id = 'anime-1'`); err != nil {
		t.Fatal(err)
	}
	service := NewReplacementDecisionService(db)
	service.SetCleanupHandoff(cleanup)
	if !runLiveEvaluate(t, db, store, service) {
		t.Fatal("the item enqueued while active must exist")
	}
	_, deletions, superseded := decisionCounts(t, db, "anime-1")
	var decisions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM replacement_decisions WHERE managed_anime_id = 'anime-1'`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if decisions != 0 || deletions != 0 || superseded != 0 {
		t.Fatalf("stopped before run: decisions=%d deletions=%d superseded=%d", decisions, deletions, superseded)
	}
	if _, err := os.Stat(scanPath); err != nil {
		t.Fatalf("the scan duplicate must stay: %v", err)
	}
}

// On an active subscription in single_copy, a completed scan and a resume
// compare every managed file of the episode: the unclaimed scan duplicate
// loses and is deleted.
func TestReplacementTriggerDeletesUnclaimedScanDuplicateWhenActive(t *testing.T) {
	for _, name := range []string{TriggerGenerationCompleted, TriggerSubscriptionResumed} {
		t.Run(name, func(t *testing.T) {
			db, store, trigger := newTriggerTestDB(t)
			defer db.Close()
			ctx := context.Background()
			scanPath, cleanup := triggerScanDuplicate(t, db, store)
			var err error
			if name == TriggerGenerationCompleted {
				err = trigger.TriggerGenerationCompleted(ctx, "anime-1")
			} else {
				err = trigger.TriggerSubscriptionResumed(ctx, "anime-1")
			}
			if err != nil {
				t.Fatal(err)
			}
			service := NewReplacementDecisionService(db)
			service.SetCleanupHandoff(cleanup)
			if !runLiveEvaluate(t, db, store, service) {
				t.Fatalf("%s must enqueue an evaluation on an active subscription", name)
			}
			if state := placementState(t, db, "plc-scan-dup"); state != "superseded" {
				t.Fatalf("the unclaimed scan duplicate must lose: %s", state)
			}
			claim, err := store.ClaimDue(ctx, "trigger-delete", []string{workitems.KindReplacementDelete}, time.Minute)
			if err != nil || claim.ID == "" {
				t.Fatalf("claim delete: %+v %v", claim, err)
			}
			outcome, err := cleanup.DeleteHandler().Handle(ctx, claim)
			if err != nil || outcome.TerminalState != workitems.StateSucceeded {
				t.Fatalf("delete outcome=%+v err=%v", outcome, err)
			}
			if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(scanPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the scan duplicate must be deleted: %v", err)
			}
		})
	}
}
