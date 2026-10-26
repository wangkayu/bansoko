package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bansoko/internal/deployment"
	maintenance "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/library/domain"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

// trackingCleanupExecutor is the real OS executor that counts content reads
// and can fail a purge before it reaches the filesystem.
type trackingCleanupExecutor struct {
	filesystem.OSCleanupExecutor
	contentCalls int
	purgeErr     error
}

func (e *trackingCleanupExecutor) Observe(ctx context.Context, path string) (domain.FileObservation, error) {
	e.contentCalls++
	return e.OSCleanupExecutor.Observe(ctx, path)
}

func (e *trackingCleanupExecutor) PurgeMaintenance(ctx context.Context, path string, expected domain.FileObservation, invocation maintenance.MaintenanceInvocation) error {
	if e.purgeErr != nil {
		return e.purgeErr
	}
	return e.OSCleanupExecutor.PurgeMaintenance(ctx, path, expected, invocation)
}

// cleanupFixture is one real replaced file under a temporary library root.
type cleanupFixture struct {
	db          *sql.DB
	service     *CleanupService
	store       *workitems.Store
	executor    *trackingCleanupExecutor
	root        string
	source      string
	observation domain.FileObservation
}

func newCleanupFixture(t *testing.T) *cleanupFixture {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	t.Cleanup(func() { _ = db.Close() })
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "Old", "EP01.mkv")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("replaced episode"), 0o600); err != nil {
		t.Fatal(err)
	}
	executor := &trackingCleanupExecutor{OSCleanupExecutor: filesystem.NewOSCleanupExecutor(root)}
	observation, err := executor.ObserveMetadataFile(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	service := NewCleanupService(db)
	store := cleanupWorkItemStore(t, db)
	service.SetWorkItems(store)
	service.SetFileExecutor(executor)
	return &cleanupFixture{db: db, service: service, store: store, executor: executor, root: root, source: source, observation: observation}
}

func (f *cleanupFixture) input() CleanupInput {
	return CleanupInput{
		ID:                "cleanup-1",
		IdempotencyKey:    "cln:cleanup-1",
		PlacementID:       "plc-old",
		ManagedAnimeID:    "anime-1",
		Coverage:          []string{"tv:s1:e1"},
		ReplacementRef:    "dec-new-1",
		AdmissionMode:     "single_copy",
		SourcePath:        f.source,
		SourceFingerprint: domain.MetadataFingerprint(f.observation.SizeBytes, f.observation.Identity),
	}
}

func (f *cleanupFixture) admit(t *testing.T, input CleanupInput) *ReplacementDeletion {
	t.Helper()
	op, err := f.service.Admit(context.Background(), input)
	if err != nil {
		t.Fatalf("admit cleanup: %v", err)
	}
	return op
}

// runDelete claims the live replacement_delete item, runs the handler and
// finishes the claim the way the worker does.
func (f *cleanupFixture) runDelete(t *testing.T) worker.Outcome {
	t.Helper()
	return runCleanupDelete(t, f.service, f.store)
}

func runCleanupDelete(t *testing.T, service *CleanupService, store *workitems.Store) worker.Outcome {
	t.Helper()
	ctx := context.Background()
	claim, err := store.ClaimDue(ctx, "cleanup-test-worker", []string{workitems.KindReplacementDelete}, time.Minute)
	if err != nil {
		t.Fatalf("claim replacement_delete: %v", err)
	}
	outcome, err := service.DeleteHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatalf("handle replacement_delete: %v", err)
	}
	if outcome.Kind != worker.OutcomeFinish {
		t.Fatalf("replacement_delete outcome = %+v", outcome)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatalf("finish replacement_delete: %v", err)
	}
	return outcome
}

func (f *cleanupFixture) state(t *testing.T, id string) (CleanupState, string) {
	t.Helper()
	var state string
	var code sql.NullString
	if err := f.db.QueryRow(`SELECT state, failure_code FROM replacement_deletions WHERE id = $1`, id).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	return CleanupState(state), code.String
}

func liveCleanupWorkItems(t *testing.T, db *sql.DB, id string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM work_items WHERE owner_type = $1 AND owner_id = $2 AND kind = $3`,
		workitems.OwnerReplacementDeletion, id, workitems.KindReplacementDelete).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertFilePresent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("%s must remain: %v", path, err)
	}
}

func assertFileAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s must be gone: %v", path, err)
	}
}

func TestCleanupAdmitIdempotentWithOneDeleteWorkItem(t *testing.T) {
	f := newCleanupFixture(t)
	first := f.admit(t, f.input())
	second := f.admit(t, f.input())
	if first.ID != second.ID || first.State != CleanupAdmitted {
		t.Fatalf("idempotent admit failed: %+v %+v", first, second)
	}
	if len(first.Coverage) != 1 || first.Coverage[0] != "tv:s1:e1" {
		t.Fatalf("coverage must freeze: %+v", first.Coverage)
	}
	if got := liveCleanupWorkItems(t, f.db, first.ID); got != 1 {
		t.Fatalf("admission must enqueue exactly one replacement_delete item, got %d", got)
	}
}

func TestCleanupAdmitWithoutRecordedSizeIsSkipped(t *testing.T) {
	f := newCleanupFixture(t)
	input := f.input()
	input.SourceFingerprint = ""
	op := f.admit(t, input)
	if op.State != CleanupSkipped || op.FailureCode != CleanupSkipLoserIdentityUnknown {
		t.Fatalf("unknown identity must be skipped at admission: %+v", op)
	}
	if got := liveCleanupWorkItems(t, f.db, op.ID); got != 0 {
		t.Fatalf("a skipped obligation must not enqueue work, got %d items", got)
	}
	assertFilePresent(t, f.source)
}

func TestCleanupDeleteRemovesVerifiedLoserWithoutContentReads(t *testing.T) {
	f := newCleanupFixture(t)
	op := f.admit(t, f.input())
	outcome := f.runDelete(t)
	if outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("delete outcome = %+v", outcome)
	}
	if state, _ := f.state(t, op.ID); state != CleanupDeleted {
		t.Fatalf("state = %s, want deleted", state)
	}
	assertFileAbsent(t, f.source)
	assertFileAbsent(t, filepath.Join(filepath.Dir(f.source), ".bansoko-cleanup-"+op.ID, "object"))
	if f.executor.contentCalls != 0 {
		t.Fatalf("deletion read file content %d times", f.executor.contentCalls)
	}
}

func TestCleanupDeleteSkipsWithZeroEffect(t *testing.T) {
	cases := []struct {
		name    string
		perturb func(t *testing.T, source string)
		code    string
	}{
		{name: "identity_changed", code: CleanupSkipLoserIdentityChanged, perturb: func(t *testing.T, source string) {
			if err := os.WriteFile(source, []byte("a different file of another size"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing", code: CleanupSkipLoserMissing, perturb: func(t *testing.T, source string) {
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "not_regular", code: CleanupSkipLoserNotRegular, perturb: func(t *testing.T, source string) {
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(source, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newCleanupFixture(t)
			op := f.admit(t, f.input())
			tc.perturb(t, f.source)
			before, _ := os.Lstat(f.source)
			outcome := f.runDelete(t)
			if outcome.TerminalState != workitems.StateSucceeded {
				t.Fatalf("skip outcome = %+v", outcome)
			}
			state, code := f.state(t, op.ID)
			if state != CleanupSkipped || code != tc.code {
				t.Fatalf("state=%s code=%s, want skipped/%s", state, code, tc.code)
			}
			if before != nil {
				after, err := os.Lstat(f.source)
				if err != nil || after.Size() != before.Size() || after.IsDir() != before.IsDir() {
					t.Fatalf("a skip touched the path: %v", err)
				}
			}
			var attempt []byte
			if err := f.db.QueryRow(`SELECT attempt FROM replacement_deletions WHERE id = $1`, op.ID).Scan(&attempt); err != nil {
				t.Fatal(err)
			}
			if len(attempt) != 0 {
				t.Fatalf("a skip must not checkpoint an attempt: %s", attempt)
			}
		})
	}
}

func TestCleanupDeleteBlocksWithoutExecutor(t *testing.T) {
	f := newCleanupFixture(t)
	op := f.admit(t, f.input())
	f.service.SetFileExecutor(nil)
	outcome := f.runDelete(t)
	if outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("outcome = %+v", outcome)
	}
	if state, code := f.state(t, op.ID); state != CleanupBlocked || code != "cleanup_executor_unavailable" {
		t.Fatalf("state=%s code=%s", state, code)
	}
	assertFilePresent(t, f.source)
}

func TestCleanupDeleteRejectsStaleClaimBeforeEffect(t *testing.T) {
	f := newCleanupFixture(t)
	op := f.admit(t, f.input())
	ctx := context.Background()
	claim, err := f.store.ClaimDue(ctx, "stale-worker", []string{workitems.KindReplacementDelete}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE replacement_deletions SET version = version + 1 WHERE id = $1`, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.DeleteHandler().Handle(ctx, claim); err == nil {
		t.Fatal("a stale owner version must stop the claim")
	}
	assertFilePresent(t, f.source)
}

func TestCleanupBlockedPurgeReconcilesAndResumes(t *testing.T) {
	f := newCleanupFixture(t)
	op := f.admit(t, f.input())
	f.executor.purgeErr = errors.New("injected purge failure before the effect")
	outcome := f.runDelete(t)
	if outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("outcome = %+v", outcome)
	}
	if state, code := f.state(t, op.ID); state != CleanupBlocked || code != "cleanup_purge_failed" {
		t.Fatalf("state=%s code=%s", state, code)
	}
	captured := filepath.Join(filepath.Dir(f.source), ".bansoko-cleanup-"+op.ID, "object")
	assertFileAbsent(t, f.source)
	assertFilePresent(t, captured)

	f.executor.purgeErr = nil
	result, err := f.service.ReconcileUnknownEffect(context.Background(), deployment.ReconcileRequest{OperationID: "cleanup-reconcile"})
	if err != nil || result.State != deployment.StateCompleted {
		t.Fatalf("reconcile = %+v %v", result, err)
	}
	if state, _ := f.state(t, op.ID); state != CleanupAdmitted {
		t.Fatalf("a provably unstarted purge must be re-admitted, state=%s", state)
	}
	if got := liveCleanupWorkItems(t, f.db, op.ID); got != 1 {
		t.Fatalf("re-admission must enqueue one item, got %d", got)
	}
	if outcome := f.runDelete(t); outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("resumed outcome = %+v", outcome)
	}
	if state, _ := f.state(t, op.ID); state != CleanupDeleted {
		t.Fatalf("state = %s, want deleted", state)
	}
	assertFileAbsent(t, captured)
}

func TestCleanupBlockedWithoutReceiptStaysBlocked(t *testing.T) {
	f := newCleanupFixture(t)
	op := f.admit(t, f.input())
	f.executor.purgeErr = errors.New("injected purge failure before the effect")
	f.runDelete(t)
	captured := filepath.Join(filepath.Dir(f.source), ".bansoko-cleanup-"+op.ID, "object")
	// The captured object vanished without a purge receipt: absence is never
	// a receipt, so the deletion stays an unknown effect.
	if err := os.Remove(captured); err != nil {
		t.Fatal(err)
	}
	result, err := f.service.ReconcileUnknownEffect(context.Background(), deployment.ReconcileRequest{OperationID: "cleanup-reconcile"})
	if err != nil || result.State != deployment.StateBlocked || result.EvidenceRef != op.ID {
		t.Fatalf("reconcile = %+v %v", result, err)
	}
	if state, _ := f.state(t, op.ID); state != CleanupBlocked {
		t.Fatalf("state = %s, want blocked", state)
	}
}

func TestCleanupReconcileEnqueuesAdmittedWithoutWorkItem(t *testing.T) {
	f := newCleanupFixture(t)
	op := f.admit(t, f.input())
	if _, err := workItemFixtureDB(t).Exec(`DELETE FROM work_items WHERE owner_type = $1 AND owner_id = $2`, workitems.OwnerReplacementDeletion, op.ID); err != nil {
		t.Fatal(err)
	}
	result, err := f.service.ReconcileUnknownEffect(context.Background(), deployment.ReconcileRequest{OperationID: "cleanup-reconcile"})
	if err != nil || result.State != deployment.StateCompleted {
		t.Fatalf("reconcile = %+v %v", result, err)
	}
	if got := liveCleanupWorkItems(t, f.db, op.ID); got != 1 {
		t.Fatalf("reconcile must re-enqueue the admitted obligation, got %d", got)
	}
}

func seedReplacementDeletion(t *testing.T, db *sql.DB, id, animeID, sourcePath, fingerprint string) string {
	t.Helper()
	key := "cln:" + id
	if _, err := db.Exec(`
INSERT INTO replacement_deletions
(id, idempotency_key, placement_id, managed_anime_id, coverage_keys, replacement_ref,
 admission_mode, source_path, source_fingerprint, state, version, created_at, updated_at)
VALUES ($1, $2, 'plc-1', $3, '["tv:s1:e1"]', 'dec-1', 'single_copy', $4, $5, 'admitted', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT (idempotency_key) DO NOTHING`,
		id, key, animeID, sourcePath, fingerprint); err != nil {
		t.Fatal(err)
	}
	return id
}

func cleanupWorkItemStore(t *testing.T, db *sql.DB) *workitems.Store {
	t.Helper()
	store, err := workitems.NewStore(db, workitems.DefaultRetentionDays, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func cleanupClaim(t *testing.T, store *workitems.Store, id, kind string, expectedVersion int64) workitems.Claim {
	t.Helper()
	ctx := context.Background()
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerReplacementDeletion, ID: id},
		Kind:                 kind,
		IdempotencyKey:       "test:" + id + ":" + kind,
		MaxAttempts:          3,
		ExpectedOwnerVersion: expectedVersion,
	}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(ctx, "cleanup-test-worker", []string{kind}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}
