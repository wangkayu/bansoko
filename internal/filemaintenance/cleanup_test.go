package filemaintenance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bansoko/internal/deployment"
	"bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

func cleanupInput() CleanupInput {
	return CleanupInput{
		ID:                "cleanup-1",
		IdempotencyKey:    "cln:cleanup-1",
		PlacementID:       "plc-old",
		ManagedAnimeID:    "anime-1",
		Coverage:          []string{"tv:s1:e1"},
		ReplacementRef:    "dec-new-1",
		PolicyVersion:     1,
		RetentionMode:     "single_copy",
		SourcePath:        "/library/Old/EP01.mkv",
		SourceFingerprint: "fp-old",
		QuarantinePath:    "/library/.quarantine/cleanup-1",
		Effect:            "quarantine",
	}
}

func TestCleanupAdmitIdempotent(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	service := NewCleanupService(db)
	ctx := context.Background()
	first, err := service.Admit(ctx, cleanupInput())
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Admit(ctx, cleanupInput())
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.State != CleanupAdmitted {
		t.Fatalf("idempotent admit failed: %+v %+v", first, second)
	}
	if len(first.Coverage) != 1 || first.Coverage[0] != "tv:s1:e1" {
		t.Fatalf("coverage must freeze: %+v", first.Coverage)
	}
}

func TestCleanupCheckpointThenTransition(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	service := NewCleanupService(db)
	ctx := context.Background()
	op, err := service.Admit(ctx, cleanupInput())
	if err != nil {
		t.Fatal(err)
	}
	attempt, _ := json.Marshal(map[string]any{"fencing_token": 1, "source": op.SourcePath, "fingerprint": op.SourceFingerprint})
	version, err := service.PersistAttempt(ctx, op.ID, op.Version, attempt)
	if err != nil || version != 2 {
		t.Fatalf("attempt checkpoint failed: %v", err)
	}
	version, err = service.Transition(ctx, op.ID, version, CleanupQuarantined, "", "")
	if err != nil || version != 3 {
		t.Fatalf("quarantine transition failed: %v", err)
	}
	// Stale CAS rejected.
	if _, err := service.Transition(ctx, op.ID, 1, CleanupCompleted, "", ""); err == nil {
		t.Fatal("stale CAS must conflict")
	}
}

func TestCleanupReconcileReviewsFrozenAttempts(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	service := NewCleanupService(db)
	ctx := context.Background()
	op, err := service.Admit(ctx, cleanupInput())
	if err != nil {
		t.Fatal(err)
	}
	attempt, _ := json.Marshal(map[string]any{"fencing_token": 1})
	if _, err := service.PersistAttempt(ctx, op.ID, op.Version, attempt); err != nil {
		t.Fatal(err)
	}
	result, err := service.ReconcileUnknownEffect(ctx, deployment.ReconcileRequest{OperationID: "recovery-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != deployment.StateBlocked {
		t.Fatalf("incomplete frozen evidence must block recovery: %+v", result)
	}
}

func TestCleanupReconcileConvergesQuarantineEffectIdempotently(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mkv")
	quarantine := filepath.Join(dir, ".quarantine", "cleanup-reconcile")
	content := []byte("crash-window-quarantine")
	if err := os.MkdirAll(filepath.Dir(quarantine), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, content, 0o644); err != nil {
		t.Fatal(err)
	}
	// A hard link preserves the frozen inode identity while the source name is
	// removed, which models a quarantine effect whose owner commit was lost.
	if err := os.Link(source, quarantine); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	fingerprint := hex.EncodeToString(digest[:])
	executor := filesystem.NewOSCleanupExecutor(dir)
	frozen, err := executor.Observe(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}

	store := cleanupWorkItemStore(t, db)
	service := NewCleanupService(db)
	service.SetWorkItems(store)
	service.SetFileExecutor(executor)
	input := cleanupInput()
	input.ID = "cleanup-reconcile"
	input.IdempotencyKey = "cln:cleanup-reconcile"
	input.SourcePath = source
	input.SourceFingerprint = fingerprint
	input.QuarantinePath = quarantine
	op, err := service.Admit(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := json.Marshal(map[string]any{
		"cleanup_id":         op.ID,
		"effect":             "quarantine",
		"stage":              "quarantine",
		"source_path":        source,
		"source_fingerprint": fingerprint,
		"quarantine_path":    quarantine,
		"source_observation": frozen,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PersistAttempt(ctx, op.ID, op.Version, attempt); err != nil {
		t.Fatal(err)
	}

	claim, err := store.ClaimDue(ctx, "cleanup-recovery-lock-test", []string{workitems.KindCleanupQuarantine}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ownerLocked := make(chan struct{})
	release := make(chan struct{})
	handlerDone := make(chan error, 1)
	go func() {
		_, handlerErr := service.mutateClaim(ctx, claim, func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
			tx := scope.SQLTx()
			if tx == nil {
				return 0, errors.New("test owner transaction is nil")
			}
			var heldOwnerVersion int64
			if err := tx.QueryRowContext(ctx, `SELECT version FROM cleanup_operations WHERE id = $1 FOR UPDATE`, op.ID).Scan(&heldOwnerVersion); err != nil {
				return 0, err
			}
			close(ownerLocked)
			<-release
			return heldOwnerVersion, nil
		})
		handlerDone <- handlerErr
	}()
	<-ownerLocked

	recoveryDone := make(chan struct {
		result deployment.ReconcileResult
		err    error
	}, 1)
	go func() {
		reconcileCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		reconcileResult, reconcileErr := service.ReconcileUnknownEffect(reconcileCtx, deployment.ReconcileRequest{OperationID: "recovery-reconcile-concurrent"})
		recoveryDone <- struct {
			result deployment.ReconcileResult
			err    error
		}{result: reconcileResult, err: reconcileErr}
	}()
	// Give recovery a chance to reach the WorkItem lock while the handler holds
	// both rows. The release below then exercises the same WorkItem -> owner
	// order without leaving either transaction waiting on the other.
	time.Sleep(100 * time.Millisecond)
	close(release)
	if handlerErr := <-handlerDone; handlerErr != nil {
		t.Fatal(handlerErr)
	}
	recoveryCall := <-recoveryDone
	if recoveryCall.err != nil {
		t.Fatalf("concurrent handler/recovery must not deadlock or fail: %v", recoveryCall.err)
	}
	if recoveryCall.result.State != deployment.StateCompleted || recoveryCall.result.Effect != deployment.EffectPresent || recoveryCall.result.EvidenceRef != op.ID {
		t.Fatalf("quarantine effect must converge after handler lock release: %+v", recoveryCall.result)
	}
	var state, ownerQuarantine string
	var ownerVersion int64
	if err := db.QueryRow(`SELECT state, quarantine_path, version FROM cleanup_operations WHERE id = $1`, op.ID).
		Scan(&state, &ownerQuarantine, &ownerVersion); err != nil {
		t.Fatal(err)
	}
	if state != string(CleanupQuarantined) || ownerQuarantine != quarantine || ownerVersion != 3 {
		t.Fatalf("cleanup owner did not converge: state=%s quarantine=%s version=%d", state, ownerQuarantine, ownerVersion)
	}
	var quarantineState, quarantineOwnerType, quarantineOwnerID, quarantineKind string
	var purgeCount int
	var expectedOwnerVersion int64
	if err := db.QueryRow(`SELECT state, owner_type, owner_id, kind, expected_owner_version FROM work_item_history WHERE owner_id = $1 AND kind = $2`, op.ID, workitems.KindCleanupQuarantine).
		Scan(&quarantineState, &quarantineOwnerType, &quarantineOwnerID, &quarantineKind, &expectedOwnerVersion); err != nil {
		t.Fatal(err)
	}
	if quarantineOwnerType != workitems.OwnerCleanup || quarantineOwnerID != op.ID || quarantineKind != workitems.KindCleanupQuarantine {
		t.Fatalf("quarantine WorkItem identity changed: owner_type=%s owner_id=%s kind=%s", quarantineOwnerType, quarantineOwnerID, quarantineKind)
	}
	if quarantineState != workitems.StateSucceeded || expectedOwnerVersion != ownerVersion {
		t.Fatalf("quarantine WorkItem did not converge: state=%s expected_owner_version=%d owner_version=%d", quarantineState, expectedOwnerVersion, ownerVersion)
	}
	if err := db.QueryRow(`SELECT count(*) FROM work_items WHERE owner_id = $1 AND kind = $2 AND state IN ('available', 'leased')`, op.ID, workitems.KindCleanupPurge).
		Scan(&purgeCount); err != nil {
		t.Fatal(err)
	}
	if purgeCount != 1 {
		t.Fatalf("expected one live purge WorkItem, got %d", purgeCount)
	}
	// Model a purge enqueue transaction lost after quarantine convergence. The
	// next recovery pass must repair the missing live stage before completing.
	if _, err := workItemFixtureDB(t).Exec(`DELETE FROM work_items WHERE owner_id = $1 AND kind = $2 AND state IN ('available', 'leased')`, op.ID, workitems.KindCleanupPurge); err != nil {
		t.Fatal(err)
	}
	service.workItems = nil
	result, err := service.ReconcileUnknownEffect(ctx, deployment.ReconcileRequest{OperationID: "recovery-reconcile-enqueue-failed"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != deployment.StateRetryable || result.Effect != deployment.EffectPresent {
		t.Fatalf("failed purge enqueue must remain retryable: %+v", result)
	}
	service.SetWorkItems(store)

	result, err = service.ReconcileUnknownEffect(ctx, deployment.ReconcileRequest{OperationID: "recovery-reconcile-replay"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != deployment.StateCompleted || result.Effect != deployment.EffectPresent {
		t.Fatalf("missing purge WorkItem must be repaired idempotently: %+v", result)
	}
	var replayVersion int64
	if err := db.QueryRow(`SELECT version FROM cleanup_operations WHERE id = $1`, op.ID).Scan(&replayVersion); err != nil {
		t.Fatal(err)
	}
	if replayVersion != ownerVersion {
		t.Fatalf("replay advanced owner version: first=%d replay=%d", ownerVersion, replayVersion)
	}
	if err := db.QueryRow(`SELECT count(*) FROM work_items WHERE owner_id = $1 AND kind = $2 AND state IN ('available', 'leased')`, op.ID, workitems.KindCleanupPurge).Scan(&purgeCount); err != nil {
		t.Fatal(err)
	}
	if purgeCount != 1 {
		t.Fatalf("recovery must recreate one live purge WorkItem, got %d", purgeCount)
	}
	// A terminal failed purge is not a live success. Recovery may recreate a
	// fresh live item under the existing manual-retry/owner policy.
	if _, err := workItemFixtureDB(t).Exec(`
	SELECT public.work_item_close(id,'failed') FROM work_items
WHERE owner_id = $1 AND kind = $2 AND state IN ('available', 'leased')`, op.ID, workitems.KindCleanupPurge); err != nil {
		t.Fatal(err)
	}
	result, err = service.ReconcileUnknownEffect(ctx, deployment.ReconcileRequest{OperationID: "recovery-reconcile-terminal-failed"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != deployment.StateCompleted || result.Effect != deployment.EffectPresent {
		t.Fatalf("terminal failed purge must be replayable: %+v", result)
	}
	if err := db.QueryRow(`SELECT count(*) FROM work_items WHERE owner_id = $1 AND kind = $2 AND state IN ('available', 'leased')`, op.ID, workitems.KindCleanupPurge).Scan(&purgeCount); err != nil {
		t.Fatal(err)
	}
	if purgeCount != 1 {
		t.Fatalf("terminal failed purge must be replaced by one live WorkItem, got %d", purgeCount)
	}
}

func TestCleanupGraceDeadlineFreezes(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	service := NewCleanupService(db)
	input := cleanupInput()
	// PostgreSQL timestamp has microsecond precision: truncate so the
	// round-trip through the DB compares equal.
	deadline := time.Now().Truncate(time.Microsecond).Add(7 * 24 * time.Hour)
	input.GraceDeadline = &deadline
	op, err := service.Admit(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if op.GraceDeadline == nil || !op.GraceDeadline.Equal(deadline) {
		t.Fatalf("grace deadline must freeze: %+v", op.GraceDeadline)
	}
}

func seedCleanupOperation(t *testing.T, db *sql.DB, id, animeID, sourcePath, fingerprint string, effect string, quarantinePath string) string {
	t.Helper()
	key := "cln:" + id
	if _, err := db.Exec(`
INSERT INTO cleanup_operations
(id, idempotency_key, placement_id, managed_anime_id, coverage_keys, replacement_ref,
 policy_version, retention_mode, source_path, source_fingerprint, quarantine_path,
 effect, grace_deadline, state, version, created_at, updated_at)
VALUES ($1, $2, 'plc-1', $3, '["tv:s1:e1"]', 'dec-1', 1, 'single_copy', $4, $5, $6, $7, NULL, 'admitted', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT (idempotency_key) DO NOTHING`,
		id, key, animeID, sourcePath, fingerprint, nullableText(quarantinePath), effect); err != nil {
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
		Owner:                workitems.OwnerRef{Type: workitems.OwnerCleanup, ID: id},
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

func cleanupOwnerAndWorkItemVersions(t *testing.T, db *sql.DB, id, workItemID string) (int64, int64) {
	t.Helper()
	var ownerVersion, workItemVersion int64
	if err := db.QueryRow(`SELECT version FROM cleanup_operations WHERE id = $1`, id).Scan(&ownerVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT expected_owner_version FROM work_item_history WHERE id = $1`, workItemID).Scan(&workItemVersion); err != nil {
		t.Fatal(err)
	}
	return ownerVersion, workItemVersion
}

func TestQuarantineHandlerBlocksUnsupportedMutation(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	dir := t.TempDir()
	source := filepath.Join(dir, "EP01.mkv")
	content := []byte("quarantine-me")
	if err := os.WriteFile(source, content, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	service := NewCleanupService(db)
	store := cleanupWorkItemStore(t, db)
	service.SetWorkItems(store)
	quarantine := filepath.Join(dir, ".quarantine", "cln-1")
	if err := os.MkdirAll(filepath.Dir(quarantine), 0o755); err != nil {
		t.Fatal(err)
	}
	seedCleanupOperation(t, db, "cln-1", "anime-1", source, hex.EncodeToString(digest[:]), "quarantine", quarantine)
	service.SetFileExecutor(filesystem.NewOSCleanupExecutor(dir))
	outcome, err := service.QuarantineHandler().Handle(context.Background(), cleanupClaim(t, store, "cln-1", workitems.KindCleanupQuarantine, 1))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("unsupported cleanup mutation must fail closed, got %+v", outcome)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source must remain untouched: %v", err)
	}
	if _, err := os.Stat(quarantine); !os.IsNotExist(err) {
		t.Fatal("quarantine target must not be created")
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM cleanup_operations WHERE id = 'cln-1'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "blocked" {
		t.Fatalf("state must be blocked, got %s", state)
	}
}

func TestPurgeHandlerBlocksUnsupportedMutation(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	dir := t.TempDir()
	quarantine := filepath.Join(dir, ".quarantine", "cln-2")
	content := []byte("purge-me")
	if err := os.MkdirAll(filepath.Dir(quarantine), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(quarantine, content, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	now := time.Now()
	service := NewCleanupService(db)
	store := cleanupWorkItemStore(t, db)
	service.SetWorkItems(store)
	service.now = func() time.Time { return now }
	attempt, _ := json.Marshal(map[string]any{
		"source_observation": map[string]any{"Fingerprint": hex.EncodeToString(digest[:]), "SizeBytes": int64(len(content)), "Identity": map[string]any{"Device": 1, "Inode": 1}},
	})
	if _, err := db.Exec(`
INSERT INTO cleanup_operations
(id, idempotency_key, placement_id, managed_anime_id, coverage_keys, replacement_ref,
 policy_version, retention_mode, source_path, source_fingerprint, quarantine_path,
 effect, grace_deadline, state, version, attempt, created_at, updated_at)
VALUES ('cln-2', 'cln:cln-2', 'plc-2', 'anime-1', '["tv:s1:e1"]', 'dec-2', 1, 'single_copy',
 'src', $1, $2, 'quarantine', $3, 'quarantined', 2, $4, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		hex.EncodeToString(digest[:]), quarantine, now.Add(-time.Hour), attempt); err != nil {
		t.Fatal(err)
	}
	service.SetFileExecutor(filesystem.NewOSCleanupExecutor(dir))
	claim := cleanupClaim(t, store, "cln-2", workitems.KindCleanupPurge, 2)
	outcome, err := service.PurgeHandler().Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("unsupported purge mutation must fail closed, got %+v", outcome)
	}
	if _, err := os.Stat(quarantine); err != nil {
		t.Fatalf("quarantine file must remain untouched: %v", err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM cleanup_operations WHERE id = 'cln-2'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "blocked" {
		t.Fatalf("state must be blocked, got %s", state)
	}
	ownerVersion, expectedVersion := cleanupOwnerAndWorkItemVersions(t, db, "cln-2", claim.ID)
	if ownerVersion != expectedVersion || ownerVersion != 3 {
		t.Fatalf("purge checkpoint/block versions diverged: owner=%d work_item=%d", ownerVersion, expectedVersion)
	}
}

func TestQuarantineHandlerFailsClosedWithoutExecutor(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	service := NewCleanupService(db)
	store := cleanupWorkItemStore(t, db)
	service.SetWorkItems(store)
	seedCleanupOperation(t, db, "cln-3", "anime-1", "/tmp/nonexistent.mkv", "fp", "quarantine", "/tmp/.quarantine/cln-3")
	outcome, err := service.QuarantineHandler().Handle(context.Background(), cleanupClaim(t, store, "cln-3", workitems.KindCleanupQuarantine, 1))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("without executor the handler must fail closed, got %+v", outcome)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM cleanup_operations WHERE id = 'cln-3'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "blocked" {
		t.Fatalf("state must be blocked, got %s", state)
	}
}

func TestQuarantineHandlerRejectsStaleClaimBeforeEffect(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	dir := t.TempDir()
	source := filepath.Join(dir, "stale.mkv")
	content := []byte("stale-claim")
	if err := os.WriteFile(source, content, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	service := NewCleanupService(db)
	store := cleanupWorkItemStore(t, db)
	service.SetWorkItems(store)
	service.SetFileExecutor(filesystem.NewOSCleanupExecutor(dir))
	quarantine := filepath.Join(dir, ".quarantine", "cln-stale")
	if err := os.MkdirAll(filepath.Dir(quarantine), 0o755); err != nil {
		t.Fatal(err)
	}
	seedCleanupOperation(t, db, "cln-stale", "anime-1", source, hex.EncodeToString(digest[:]), "quarantine", quarantine)
	claim := cleanupClaim(t, store, "cln-stale", workitems.KindCleanupQuarantine, 1)
	if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET lease_until = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, claim.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.QuarantineHandler().Handle(context.Background(), claim); err == nil {
		t.Fatal("stale claim must be rejected before filesystem effect")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source changed after stale claim: %v", err)
	}
	if _, err := os.Stat(quarantine); !os.IsNotExist(err) {
		t.Fatal("stale claim must not create quarantine target")
	}
	var version int64
	if err := db.QueryRow(`SELECT version FROM cleanup_operations WHERE id = 'cln-stale'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("stale claim changed owner version to %d", version)
	}
}

func TestQuarantineCheckpointSynchronizesOwnerAndWorkItemVersion(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	dir := t.TempDir()
	source := filepath.Join(dir, "checkpoint.mkv")
	content := []byte("checkpoint")
	if err := os.WriteFile(source, content, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	service := NewCleanupService(db)
	store := cleanupWorkItemStore(t, db)
	service.SetWorkItems(store)
	service.SetFileExecutor(filesystem.NewOSCleanupExecutor(dir))
	quarantine := filepath.Join(dir, ".quarantine", "cln-checkpoint")
	if err := os.MkdirAll(filepath.Dir(quarantine), 0o755); err != nil {
		t.Fatal(err)
	}
	seedCleanupOperation(t, db, "cln-checkpoint", "anime-1", source, hex.EncodeToString(digest[:]), "quarantine", quarantine)
	claim := cleanupClaim(t, store, "cln-checkpoint", workitems.KindCleanupQuarantine, 1)
	outcome, err := service.QuarantineHandler().Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("unsupported cleanup effect must finish blocked, got %+v", outcome)
	}
	var ownerVersion, expectedVersion int64
	if err := db.QueryRow(`SELECT version FROM cleanup_operations WHERE id = 'cln-checkpoint'`).Scan(&ownerVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT expected_owner_version FROM work_item_history WHERE id = $1`, claim.ID).Scan(&expectedVersion); err != nil {
		t.Fatal(err)
	}
	if ownerVersion != expectedVersion {
		t.Fatalf("owner/work item versions diverged: owner=%d work_item=%d", ownerVersion, expectedVersion)
	}
	if ownerVersion != 3 {
		t.Fatalf("checkpoint + blocked transition should advance owner twice, got %d", ownerVersion)
	}
}
func TestCleanupAdmitEnqueuesQuarantineWorkItem(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	store := cleanupWorkItemStore(t, db)
	service := NewCleanupService(db)
	service.SetWorkItems(store)
	input := cleanupInput()
	if _, err := service.Admit(context.Background(), input); err != nil {
		t.Fatalf("admit: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE kind = 'cleanup_quarantine' AND owner_id = 'cleanup-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one quarantine work item, got %d", count)
	}
	// Idempotent: admit again does not duplicate.
	if _, err := service.Admit(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE kind = 'cleanup_quarantine' AND owner_id = 'cleanup-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("quarantine work item must be idempotent, got %d", count)
	}
}

func TestQuarantineBlocksWithoutEnqueuingPurge(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	store := cleanupWorkItemStore(t, db)
	dir := t.TempDir()
	source := filepath.Join(dir, "EP02.mkv")
	content := []byte("quarantine-again")
	if err := os.WriteFile(source, content, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	service := NewCleanupService(db)
	service.SetWorkItems(store)
	service.SetFileExecutor(filesystem.NewOSCleanupExecutor(dir))
	quarantine := filepath.Join(dir, ".quarantine", "cln-q")
	if err := os.MkdirAll(filepath.Dir(quarantine), 0o755); err != nil {
		t.Fatal(err)
	}
	seedCleanupOperation(t, db, "cln-q", "anime-1", source, hex.EncodeToString(digest[:]), "quarantine", quarantine)
	outcome, err := service.QuarantineHandler().Handle(context.Background(), cleanupClaim(t, store, "cln-q", workitems.KindCleanupQuarantine, 1))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("unsupported quarantine mutation must be blocked, got %+v", outcome)
	}
	var purgeCount int
	if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE kind = 'cleanup_purge' AND owner_id = 'cln-q'`).Scan(&purgeCount); err != nil {
		t.Fatal(err)
	}
	if purgeCount != 0 {
		t.Fatalf("blocked quarantine must not enqueue purge, got %d", purgeCount)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source must remain untouched: %v", err)
	}
}

func TestPurgeHandlerIdempotentReplayWhenAlreadyPurged(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	service := NewCleanupService(db)
	store := cleanupWorkItemStore(t, db)
	service.SetWorkItems(store)
	service.SetFileExecutor(nil)
	if _, err := db.Exec(`
INSERT INTO cleanup_operations
(id, idempotency_key, placement_id, managed_anime_id, coverage_keys, replacement_ref,
 policy_version, retention_mode, source_path, source_fingerprint, quarantine_path,
 effect, grace_deadline, state, version, created_at, updated_at)
VALUES ('cln-purged', 'cln:cln-purged', 'plc-3', 'anime-1', '["tv:s1:e1"]', 'dec-3', 1, 'single_copy',
 'src', 'fp', '/tmp/.quarantine/cln-purged', 'quarantine', NULL, 'purged', 3, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	// A crashed purge terminal transition replays with the already-committed
	// owner state; it must converge to success, never to failed.
	outcome, err := service.PurgeHandler().Handle(context.Background(), cleanupClaim(t, store, "cln-purged", workitems.KindCleanupPurge, 3))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("purged replay must succeed idempotently, got %+v", outcome)
	}
	if outcome.OwnerResult.ReasonCode != "cleanup_purged" {
		t.Fatalf("purged replay reason code = %s", outcome.OwnerResult.ReasonCode)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM cleanup_operations WHERE id = 'cln-purged'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "purged" {
		t.Fatalf("state must stay purged, got %s", state)
	}
}

func TestQuarantineHandlerIdempotentReplayWhenAlreadyQuarantined(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	store := cleanupWorkItemStore(t, db)
	service := NewCleanupService(db)
	service.SetWorkItems(store)
	// Source is already moved: quarantine_path is committed and the source no
	// longer exists. The handler must not re-run the move.
	dir := t.TempDir()
	quarantine := filepath.Join(dir, ".quarantine", "cln-q-replay")
	if _, err := db.Exec(`
INSERT INTO cleanup_operations
(id, idempotency_key, placement_id, managed_anime_id, coverage_keys, replacement_ref,
 policy_version, retention_mode, source_path, source_fingerprint, quarantine_path,
 effect, grace_deadline, state, version, created_at, updated_at)
VALUES ('cln-q-replay', 'cln:cln-q-replay', 'plc-4', 'anime-1', '["tv:s1:e1"]', 'dec-4', 1, 'single_copy',
 $1, 'fp', $2, 'quarantine', NULL, 'quarantined', 2, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		filepath.Join(dir, "missing-source.mkv"), quarantine); err != nil {
		t.Fatal(err)
	}
	outcome, err := service.QuarantineHandler().Handle(context.Background(), cleanupClaim(t, store, "cln-q-replay", workitems.KindCleanupQuarantine, 2))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("quarantined replay must succeed idempotently, got %+v", outcome)
	}
	var purgeCount int
	if err := db.QueryRow(`SELECT count(*) FROM work_item_history WHERE kind = 'cleanup_purge' AND owner_id = 'cln-q-replay'`).Scan(&purgeCount); err != nil {
		t.Fatal(err)
	}
	if purgeCount != 1 {
		t.Fatalf("quarantined replay must schedule the purge stage, got %d", purgeCount)
	}
}

func TestCleanupRestoreBlocksUnsupportedMutation(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	dir := t.TempDir()
	source := filepath.Join(dir, "EP01.mkv")
	quarantine := filepath.Join(dir, ".quarantine", "cln-restore")
	content := []byte("restore-me")
	if err := os.MkdirAll(filepath.Dir(quarantine), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(quarantine, content, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	service := NewCleanupService(db)
	service.SetFileExecutor(filesystem.NewOSCleanupExecutor(dir))
	if _, err := db.Exec(`
INSERT INTO cleanup_operations
(id, idempotency_key, placement_id, managed_anime_id, coverage_keys, replacement_ref,
 policy_version, retention_mode, source_path, source_fingerprint, quarantine_path,
 effect, grace_deadline, state, version, created_at, updated_at)
VALUES ('cln-restore', 'cln:cln-restore', 'plc-r', 'anime-1', '["tv:s1:e1"]', 'dec-r', 1, 'single_copy',
 $1, $2, $3, 'quarantine', NULL, 'quarantined', 2, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		source, hex.EncodeToString(digest[:]), quarantine); err != nil {
		t.Fatal(err)
	}

	if err := service.Restore(context.Background(), "cln-restore", 2); err == nil {
		t.Fatal("restore without complete frozen evidence must fail closed")
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatal("source must remain absent")
	}
	if _, err := os.Stat(quarantine); err != nil {
		t.Fatalf("quarantine file must stay untouched: %v", err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM cleanup_operations WHERE id = 'cln-restore'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "blocked" {
		t.Fatalf("restore state = %s, want blocked", state)
	}
}

func TestCleanupRestoreFailsClosedOnFingerprintMismatch(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	dir := t.TempDir()
	source := filepath.Join(dir, "EP01.mkv")
	quarantine := filepath.Join(dir, ".quarantine", "cln-restore-fp")
	if err := os.MkdirAll(filepath.Dir(quarantine), 0o755); err != nil {
		t.Fatal(err)
	}
	// The quarantined file does not match the frozen fingerprint: restore must
	// abort and keep the file untouched.
	if err := os.WriteFile(quarantine, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := NewCleanupService(db)
	service.SetFileExecutor(filesystem.NewOSCleanupExecutor(dir))
	if _, err := db.Exec(`
INSERT INTO cleanup_operations
(id, idempotency_key, placement_id, managed_anime_id, coverage_keys, replacement_ref,
 policy_version, retention_mode, source_path, source_fingerprint, quarantine_path,
 effect, grace_deadline, state, version, created_at, updated_at)
VALUES ('cln-restore-fp', 'cln:cln-restore-fp', 'plc-f', 'anime-1', '["tv:s1:e1"]', 'dec-f', 1, 'single_copy',
 $1, 'expected-fingerprint', $2, 'quarantine', NULL, 'quarantined', 2, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		source, quarantine); err != nil {
		t.Fatal(err)
	}

	if err := service.Restore(context.Background(), "cln-restore-fp", 2); err == nil {
		t.Fatal("fingerprint mismatch must fail closed")
	}
	if _, err := os.Stat(quarantine); err != nil {
		t.Fatalf("quarantined file must stay untouched: %v", err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM cleanup_operations WHERE id = 'cln-restore-fp'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "blocked" {
		t.Fatalf("mismatch restore state = %s, want blocked", state)
	}
}

func TestPurgeHandlerBlocksWithoutExecutor(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	store := cleanupWorkItemStore(t, db)
	service := NewCleanupService(db)
	service.SetWorkItems(store)
	if _, err := db.Exec(`
INSERT INTO cleanup_operations
(id, idempotency_key, placement_id, managed_anime_id, coverage_keys, replacement_ref,
 policy_version, retention_mode, source_path, source_fingerprint, quarantine_path,
 effect, grace_deadline, state, version, created_at, updated_at)
VALUES ('cln-purge-no-executor', 'cln:cln-purge-no-executor', 'plc-no-executor', 'anime-1', '[]', 'dec-no-executor', 1, 'single_copy',
 'src', 'fp', '/missing/.quarantine/cln-purge-no-executor', 'quarantine', NULL, 'quarantined', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

	outcome, err := service.PurgeHandler().Handle(context.Background(), cleanupClaim(t, store, "cln-purge-no-executor", workitems.KindCleanupPurge, 1))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateFailed || outcome.OwnerResult.State != "blocked" {
		t.Fatalf("unwired purge executor must block, got %+v", outcome)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM cleanup_operations WHERE id = 'cln-purge-no-executor'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(CleanupBlocked) {
		t.Fatalf("unwired purge executor state = %s, want blocked", state)
	}
}

func TestPurgeHandlerRetriesWhenQuarantineParentMissing(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	dir := t.TempDir()
	quarantine := filepath.Join(dir, ".quarantine", "missing-parent", "cln-purge-missing-parent")
	contentDigest := sha256.Sum256([]byte("frozen"))
	fingerprint := hex.EncodeToString(contentDigest[:])
	attempt, err := json.Marshal(map[string]any{
		"source_observation": map[string]any{
			"Fingerprint": fingerprint,
			"SizeBytes":   int64(len("frozen")),
			"Identity":    map[string]any{"Device": uint64(1), "Inode": uint64(1)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO cleanup_operations
(id, idempotency_key, placement_id, managed_anime_id, coverage_keys, replacement_ref,
 policy_version, retention_mode, source_path, source_fingerprint, quarantine_path,
 effect, grace_deadline, state, version, attempt, created_at, updated_at)
VALUES ('cln-purge-missing-parent', 'cln:cln-purge-missing-parent', 'plc-missing-parent', 'anime-1', '[]', 'dec-missing-parent', 1, 'single_copy',
 'src', $1, $2, 'quarantine', NULL, 'quarantined', 2, $3, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, fingerprint, quarantine, attempt); err != nil {
		t.Fatal(err)
	}

	service := NewCleanupService(db)
	store := cleanupWorkItemStore(t, db)
	service.SetWorkItems(store)
	service.now = func() time.Time { return time.Now().Add(time.Hour) }
	service.SetFileExecutor(filesystem.NewOSCleanupExecutor(dir))
	outcome, err := service.PurgeHandler().Handle(context.Background(), cleanupClaim(t, store, "cln-purge-missing-parent", workitems.KindCleanupPurge, 2))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeRetry {
		t.Fatalf("missing quarantine parent must remain retryable, got %+v", outcome)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM cleanup_operations WHERE id = 'cln-purge-missing-parent'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state == string(CleanupPurged) {
		t.Fatal("missing quarantine parent must never be marked purged")
	}
	if state != string(CleanupQuarantined) {
		t.Fatalf("missing quarantine parent changed state to %s, want quarantined", state)
	}
}

func TestPurgeHandlerBlocksInvalidEvidence(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	store := cleanupWorkItemStore(t, db)
	service := NewCleanupService(db)
	service.SetWorkItems(store)
	service.SetFileExecutor(filesystem.NewOSCleanupExecutor(t.TempDir()))
	if _, err := db.Exec(`
INSERT INTO cleanup_operations
(id, idempotency_key, placement_id, managed_anime_id, coverage_keys, replacement_ref,
 policy_version, retention_mode, source_path, source_fingerprint, quarantine_path,
 effect, grace_deadline, state, version, attempt, created_at, updated_at)
VALUES ('cln-purge-invalid-evidence', 'cln:cln-purge-invalid-evidence', 'plc-invalid-evidence', 'anime-1', '[]', 'dec-invalid-evidence', 1, 'single_copy',
 'src', 'fp', '/missing/.quarantine/cln-purge-invalid-evidence', 'quarantine', NULL, 'quarantined', 1, '{"source_observation":{}}', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

	outcome, err := service.PurgeHandler().Handle(context.Background(), cleanupClaim(t, store, "cln-purge-invalid-evidence", workitems.KindCleanupPurge, 1))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateFailed || outcome.OwnerResult.State != "blocked" {
		t.Fatalf("invalid purge evidence must block, got %+v", outcome)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM cleanup_operations WHERE id = 'cln-purge-invalid-evidence'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(CleanupBlocked) {
		t.Fatalf("invalid purge evidence state = %s, want blocked", state)
	}
}
