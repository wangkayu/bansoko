package filemaintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
	"os"
)

// seedOffsetMigrationReadyOperation builds the full fixture and plans one
// ready operation with a wired work-item store.
func seedOffsetMigrationReadyOperation(t *testing.T, db *sql.DB, workStore *workitems.Store) string {
	t.Helper()
	return seedOffsetMigrationReadyOperationWithFingerprint(t, db, workStore, "")
}

func seedOffsetMigrationReadyOperationWithFingerprint(t *testing.T, db *sql.DB, workStore *workitems.Store, fingerprint string) string {
	t.Helper()
	ctx := context.Background()
	seedOffsetMigrationChain(t, db, "1")
	if fingerprint != "" {
		if _, err := db.Exec(`UPDATE placements SET target_fingerprint = $1 WHERE id = 'plc-offmig'`, fingerprint); err != nil {
			t.Fatalf("set placement fingerprint: %v", err)
		}
	}
	admission := NewOffsetMigrationAdmissionService()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	generationID, created, err := admission.AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-offmig", "src-offmig", 1, 0, 1)
	if err != nil || !created {
		t.Fatalf("admit generation id=%q created=%v err=%v", generationID, created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	planner := NewOffsetMigrationService(db, NewNamingTargetRenderer(db, fakeRulesSource{}))
	planner.SetWorkItems(workStore)
	generation, err := planner.PlanGeneration(ctx, generationID)
	if err != nil || generation.State != OffsetMigrationGenerationReady {
		t.Fatalf("fixture must plan ready: %+v %v", generation, err)
	}
	if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET available_at=CURRENT_TIMESTAMP WHERE kind='offset_migration_execute'`); err != nil {
		t.Fatal(err)
	}
	return generationID
}

func newOffsetMigrationWorkStore(t *testing.T, db *sql.DB) *workitems.Store {
	t.Helper()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestOffsetMigrationExecuteCommitsPlacement drives the full durable chain:
// planning admits one work item, the handler moves the file through the fake
// executor, the observer verifies the target fingerprint, and the atomic
// placement/coverage/applied switch lands in the same transaction as the
// work-item finish.
func TestOffsetMigrationExecuteCommitsPlacement(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	workStore := newOffsetMigrationWorkStore(t, db)
	seedOffsetMigrationReadyOperation(t, db, workStore)

	var workItemID, ownerID, itemState string
	if err := db.QueryRowContext(ctx, `
SELECT id, owner_id, state FROM work_item_history WHERE kind = $1`, workitems.KindOffsetMigrationExecute).
		Scan(&workItemID, &ownerID, &itemState); err != nil {
		t.Fatalf("read admitted work item: %v", err)
	}
	if itemState != "available" || ownerID == "" || len(ownerID) < 8 {
		t.Fatalf("work item id=%s owner=%s state=%s, want available", workItemID, ownerID, itemState)
	}

	claim, err := workStore.ClaimDue(ctx, "test-worker", []string{workitems.KindOffsetMigrationExecute}, 30*time.Second)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	execute := NewOffsetMigrationExecuteService(db)
	execute.SetWorkItems(workStore)
	execute.SetFilePorts(&fakeOpsExecutor{}, &fakeOpsObserver{fingerprint: `{"algorithm":"sha256","value":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`})
	outcome, err := execute.ExecuteHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("outcome kind=%s terminal=%s, want finish/succeeded", outcome.Kind, outcome.TerminalState)
	}
	if err := workStore.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatalf("finish: %v", err)
	}

	var (
		newPath, coverage string
		opState           string
		itemStateAfter    string
	)
	if err := db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE id = 'plc-offmig'`).Scan(&newPath); err != nil {
		t.Fatal(err)
	}
	if newPath == "/library/Offset Anime/Season 01/Offset Anime - 01 [Group].mkv" {
		t.Fatalf("placement path unchanged: %q", newPath)
	}
	if err := db.QueryRowContext(ctx, `SELECT observation_key FROM placement_coverage WHERE placement_id = 'plc-offmig'`).Scan(&coverage); err != nil {
		t.Fatal(err)
	}
	if coverage != "tv:s1:e2" {
		t.Fatalf("coverage=%s, want tv:s1:e2", coverage)
	}
	if err := db.QueryRowContext(ctx, `SELECT state FROM offset_migration_plans WHERE id = $1`, ownerID).Scan(&opState); err != nil {
		t.Fatal(err)
	}
	if opState != string(OffsetMigrationOperationApplied) {
		t.Fatalf("operation state=%s, want applied", opState)
	}
	if err := db.QueryRowContext(ctx, `SELECT state FROM work_item_history WHERE id = $1`, workItemID).Scan(&itemStateAfter); err != nil {
		t.Fatal(err)
	}
	if itemStateAfter != workitems.StateSucceeded {
		t.Fatalf("work item state=%s, want succeeded", itemStateAfter)
	}
	var ownerVersion, itemVersion int64
	if err := db.QueryRowContext(ctx, `SELECT operation.version, item.expected_owner_version FROM offset_migration_plans operation JOIN work_item_history item ON item.owner_type='offset_migration_operation' AND item.owner_id=operation.id WHERE operation.id=$1`, ownerID).Scan(&ownerVersion, &itemVersion); err != nil {
		t.Fatal(err)
	}
	if ownerVersion != itemVersion {
		t.Fatalf("owner version=%d work item expected version=%d", ownerVersion, itemVersion)
	}
}

func TestOffsetMigrationOccupiedTargetBlocksWithoutPlacementChange(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	store := newOffsetMigrationWorkStore(t, db)
	seedOffsetMigrationReadyOperation(t, db, store)
	claim, err := store.ClaimDue(ctx, "occupied-worker", []string{workitems.KindOffsetMigrationExecute}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	execute := NewOffsetMigrationExecuteService(db)
	execute.SetWorkItems(store)
	execute.SetFilePorts(&fakeOpsExecutor{err: &filesystem.OperationError{Code: filesystem.CodeTargetOccupied, Err: errors.New("occupied")}}, &fakeOpsObserver{})
	outcome, err := execute.ExecuteHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.OwnerResult.State != "blocked" || outcome.OwnerResult.ReasonCode != "target_occupied" {
		t.Fatalf("outcome=%+v", outcome)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	var state, path string
	if err := db.QueryRowContext(ctx, `SELECT state FROM offset_migration_plans WHERE id=$1`, claim.Owner.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	var ownerVersion, itemVersion int64
	if err := db.QueryRowContext(ctx, `SELECT operation.version, item.expected_owner_version FROM offset_migration_plans operation JOIN work_item_history item ON item.owner_type='offset_migration_operation' AND item.owner_id=operation.id WHERE operation.id=$1`, claim.Owner.ID).Scan(&ownerVersion, &itemVersion); err != nil {
		t.Fatal(err)
	}
	if ownerVersion != itemVersion {
		t.Fatalf("owner version=%d work item expected version=%d", ownerVersion, itemVersion)
	}
	if err := db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE id='plc-offmig'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if state != "blocked" || path != "/library/Offset Anime/Season 01/Offset Anime - 01 [Group].mkv" {
		t.Fatalf("state=%s path=%s", state, path)
	}
}

// An unresolved admitted attempt retains its obligation on ordinary retry;
// retry budget and path observations cannot authorize a second effect.
func TestOffsetMigrationExecuteRetryPreservesUnresolvedInvocation(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	store := newOffsetMigrationWorkStore(t, db)
	seedOffsetMigrationReadyOperation(t, db, store)
	e := &offsetPendingReceiptExecutor{fakeOpsExecutor: fakeOpsExecutor{err: errors.New("move outcome unresolved")}}
	s := NewOffsetMigrationExecuteService(db)
	s.SetWorkItems(store)
	s.SetFilePorts(e, &fakeOpsObserver{fingerprint: `{"algorithm":"sha256","value":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`})
	c, err := store.ClaimDue(ctx, "first", []string{workitems.KindOffsetMigrationExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	o, err := s.ExecuteHandler().Handle(ctx, c)
	if err != nil || o.Kind != worker.OutcomeRetry {
		t.Fatalf("first %+v %v", o, err)
	}
	if _, err := store.Retry(ctx, c, o.ReasonCode, o.Reason, o.RetryAfter, o.WriteOwner); err != nil {
		t.Fatal(err)
	}
	var frozen string
	if err := db.QueryRow(`SELECT attempt::text FROM offset_migration_plans WHERE id=$1`, c.Owner.ID).Scan(&frozen); err != nil {
		t.Fatal(err)
	}
	if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET available_at=CURRENT_TIMESTAMP WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	c, err = store.ClaimDue(ctx, "retry", []string{workitems.KindOffsetMigrationExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecuteHandler().Handle(ctx, c); !errors.Is(err, workitems.ErrPublicationUnresolved) {
		t.Fatalf("retry must retain unresolved obligation: %v", err)
	}
	var state, after string
	if err := db.QueryRow(`SELECT state,attempt::text FROM offset_migration_plans WHERE id=$1`, c.Owner.ID).Scan(&state, &after); err != nil {
		t.Fatal(err)
	}
	if e.calls != 1 || e.reads != 1 || frozen != after || state != "waiting_retry" {
		t.Fatalf("calls=%d reads=%d state=%s attempt changed=%v", e.calls, e.reads, state, frozen != after)
	}
}

// TestOffsetMigrationExecuteEffectUnknownAndRecovery proves the uncertain
// effect path: an unmatched target observation parks the owner as
// effect_unknown (never guessed failed), and the typed recovery reconcile
// converges the verified target into an applied placement.
func TestOffsetMigrationExecuteEffectUnknownAndRecovery(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	workStore := newOffsetMigrationWorkStore(t, db)
	seedOffsetMigrationReadyOperation(t, db, workStore)

	claim, err := workStore.ClaimDue(ctx, "test-worker", []string{workitems.KindOffsetMigrationExecute}, 30*time.Second)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	execute := NewOffsetMigrationExecuteService(db)
	execute.SetWorkItems(workStore)
	execute.SetFilePorts(&fakeOpsExecutor{}, &fakeOpsObserver{fingerprint: `{"algorithm":"sha256","value":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`})
	outcome, err := execute.ExecuteHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("outcome kind=%s terminal=%s, want finish/failed", outcome.Kind, outcome.TerminalState)
	}
	if outcome.OwnerResult.State != string(OffsetMigrationOperationEffectUnknown) {
		t.Fatalf("owner result state=%s, want effect_unknown", outcome.OwnerResult.State)
	}
	if err := workStore.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatalf("finish: %v", err)
	}
	var opState string
	if err := db.QueryRowContext(ctx, `SELECT state FROM offset_migration_plans WHERE id = $1`, claim.Owner.ID).Scan(&opState); err != nil {
		t.Fatal(err)
	}
	if opState != string(OffsetMigrationOperationEffectUnknown) {
		t.Fatal(opState)
	}
	// A matching path hash without an invocation-bound receipt stays unknown.
	e := &offsetPendingReceiptExecutor{}
	execute.SetFilePorts(e, &fakeOpsObserver{fingerprint: `{"algorithm":"sha256","value":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`})
	if err := db.QueryRow(`SELECT expected_owner_version FROM work_item_history WHERE id=$1`, claim.ID).Scan(&claim.ExpectedOwnerVersion); err != nil {
		t.Fatal(err)
	}
	o, err := execute.ExecuteHandler().(worker.RecoveryObserver).ObserveRecovery(ctx, claim)
	if err != nil || o.Disposition != workitems.DispositionAmbiguous {
		t.Fatalf("unproven path recovered: %+v %v", o, err)
	}
	if e.calls != 0 || e.reads != 1 {
		t.Fatalf("calls=%d reads=%d", e.calls, e.reads)
	}
	var path string
	if err := db.QueryRow(`SELECT physical_path FROM placements WHERE id='plc-offmig'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if path != "/library/Offset Anime/Season 01/Offset Anime - 01 [Group].mkv" {
		t.Fatal("unknown effect changed placement")
	}
}

func TestOffsetMigrationReclaimedAttemptConverges(t *testing.T) {
	type reclaimCase struct {
		name      string
		moved     bool
		perturb   func(t *testing.T, source, target string)
		converges bool
	}
	// A pre-effect crash leaves no start marker. With the source intact at its
	// frozen fingerprint and no target, the attempt provably never ran and must
	// converge; any other disk state must stay unresolved and untouched.
	cases := []reclaimCase{
		{name: "not_started", converges: true},
		{name: "moved", moved: true, converges: true},
		{name: "not_started_source_drifted", perturb: func(t *testing.T, source, _ string) {
			if err := os.WriteFile(source, []byte("drifted after checkpoint"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "not_started_target_occupied", perturb: func(t *testing.T, _, target string) {
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("occupied after checkpoint"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		moved := tc.moved
		t.Run(tc.name, func(t *testing.T) {
			db := disposabledb.ResetDisposableSchema(t)
			defer db.Close()
			ctx := context.Background()
			root := t.TempDir()
			store := newOffsetMigrationWorkStore(t, db)
			sourcePath, fingerprint := seedOffsetMigrationRealChain(t, db, root, "1")
			admission := NewOffsetMigrationAdmissionService()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			generationID, _, err := admission.AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-real", "src-real", 1, 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			planner := NewOffsetMigrationService(db, NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: offsetRealChainRules(root)}))
			planner.SetWorkItems(store)
			if _, err := planner.PlanGeneration(ctx, generationID); err != nil {
				t.Fatal(err)
			}
			if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET available_at=CURRENT_TIMESTAMP WHERE kind='offset_migration_execute'`); err != nil {
				t.Fatal(err)
			}
			claim, err := store.ClaimDue(ctx, "crash-worker", []string{workitems.KindOffsetMigrationExecute}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			execute := NewOffsetMigrationExecuteService(db)
			execute.SetWorkItems(store)
			fake := panicAfterOffsetExecutor{inner: filesystem.OSFileExecutor{}, before: !moved}
			execute.SetFilePorts(fake, filesystem.OSFileObserver{})
			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("expected injected effect crash")
					}
				}()
				_, _ = execute.ExecuteHandler().Handle(ctx, claim)
			}()
			var target string
			if err := db.QueryRowContext(ctx, `SELECT new_target_path FROM offset_migration_plans WHERE id=$1`, claim.Owner.ID).Scan(&target); err != nil {
				t.Fatal(err)
			}
			resolved := resolveOffsetTargetPath(target)
			if moved {
				if _, err := os.Stat(sourcePath); !os.IsNotExist(err) {
					t.Fatalf("source remains after effect: %v", err)
				}
				if _, err := os.Stat(resolved); err != nil {
					t.Fatalf("target missing after effect: %v", err)
				}
			} else {
				if _, err := os.Stat(sourcePath); err != nil {
					t.Fatalf("source should remain before effect: %v", err)
				}
			}
			execute.SetFilePorts(filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
			var checkpointState string
			var checkpointVersion, checkpointExpected int64
			if err := db.QueryRowContext(ctx, `SELECT operation.state, operation.version, item.expected_owner_version FROM offset_migration_plans operation JOIN work_item_history item ON item.owner_type='offset_migration_operation' AND item.owner_id=operation.id WHERE item.id=$1`, claim.ID).Scan(&checkpointState, &checkpointVersion, &checkpointExpected); err != nil {
				t.Fatal(err)
			}
			if checkpointState != string(OffsetMigrationOperationRunning) || checkpointVersion != 2 || checkpointExpected != 2 {
				t.Fatalf("crash checkpoint owner state=%s version=%d workitem expected=%d", checkpointState, checkpointVersion, checkpointExpected)
			}
			if tc.perturb != nil {
				tc.perturb(t, sourcePath, resolved)
			}
			sourceBefore, targetBefore := readFileOrNil(t, sourcePath), readFileOrNil(t, resolved)
			if _, err := workItemFixtureDB(t).ExecContext(ctx, `UPDATE work_items SET lease_until=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE id=$1`, claim.ID); err != nil {
				t.Fatal(err)
			}
			reclaimed, err := store.ClaimDue(ctx, "reclaim-worker", []string{workitems.KindOffsetMigrationExecute}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := execute.ExecuteHandler().Handle(ctx, reclaimed)
			if !tc.converges {
				if !errors.Is(err, workitems.ErrPublicationUnresolved) {
					t.Fatalf("unproven attempt must remain unresolved: %v", err)
				}
				if !bytes.Equal(readFileOrNil(t, sourcePath), sourceBefore) || !bytes.Equal(readFileOrNil(t, resolved), targetBefore) {
					t.Fatal("unresolved attempt mutated the disk")
				}
				var state string
				if err := db.QueryRow(`SELECT state FROM offset_migration_plans WHERE id=$1`, claim.Owner.ID).Scan(&state); err != nil {
					t.Fatal(err)
				}
				if state != "running" {
					t.Fatal(state)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := store.FinishWithResult(ctx, reclaimed, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
				t.Fatal(err)
			}
			var ownerVersion, itemVersion int64
			if err := db.QueryRowContext(ctx, `SELECT operation.version, item.expected_owner_version FROM offset_migration_plans operation JOIN work_item_history item ON item.owner_type='offset_migration_operation' AND item.owner_id=operation.id WHERE item.id=$1`, claim.ID).Scan(&ownerVersion, &itemVersion); err != nil {
				t.Fatal(err)
			}
			if ownerVersion != itemVersion {
				t.Fatalf("owner version=%d work item expected version=%d", ownerVersion, itemVersion)
			}
			var state, itemState string
			if err := db.QueryRowContext(ctx, `SELECT state FROM offset_migration_plans WHERE id=$1`, claim.Owner.ID).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRowContext(ctx, `SELECT state FROM work_item_history WHERE id=$1`, claim.ID).Scan(&itemState); err != nil {
				t.Fatal(err)
			}
			if state != string(OffsetMigrationOperationApplied) || itemState != workitems.StateSucceeded {
				t.Fatalf("owner=%s item=%s result=%+v", state, itemState, outcome)
			}
			if _, err := os.Stat(sourcePath); !os.IsNotExist(err) {
				t.Fatalf("source not removed after recovery: %v", err)
			}
			var committedPath string
			if err := db.QueryRowContext(ctx, `SELECT physical_path FROM placements WHERE id='plc-real'`).Scan(&committedPath); err != nil {
				t.Fatal(err)
			}
			if committedPath != resolved {
				t.Fatalf("placement path after recovery=%q, want resolved target %q", committedPath, resolved)
			}
			if _, err := os.Stat(committedPath); err != nil {
				t.Fatalf("committed placement target missing: %v", err)
			}
			content, err := os.ReadFile(committedPath)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(content)
			if got := hex.EncodeToString(sum[:]); got != fingerprint {
				t.Fatalf("recovered target fingerprint=%s, want %s", got, fingerprint)
			}
		})
	}
}

// TestOffsetMigrationPlannerReplayKeepsSingleWorkItem proves planner replay
// after a crash (generation forced back to planning) does not duplicate work
// items for the same operation.
func TestOffsetMigrationPlannerReplayKeepsSingleWorkItem(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	workStore := newOffsetMigrationWorkStore(t, db)
	generationID := seedOffsetMigrationReadyOperation(t, db, workStore)

	if _, err := db.ExecContext(ctx, `
UPDATE offset_migration_generations SET state = 'planning', completed_at = NULL, version = version + 1 WHERE id = $1`, generationID); err != nil {
		t.Fatal(err)
	}
	planner := NewOffsetMigrationService(db, NewNamingTargetRenderer(db, fakeRulesSource{}))
	planner.SetWorkItems(workStore)
	if _, err := planner.PlanGeneration(ctx, generationID); err != nil {
		t.Fatalf("replay plan generation: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM work_item_history WHERE kind = $1`, workitems.KindOffsetMigrationExecute).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("work item count=%d after replay, want 1", count)
	}
}
