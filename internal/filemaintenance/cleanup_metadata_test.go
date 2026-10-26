package filemaintenance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bansoko/internal/deployment"
	executorpkg "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/library/domain"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

type trackingMetadataCleanupExecutor struct {
	inner         filesystem.OSCleanupExecutor
	contentCalls  int
	metadataCalls int
}

func (e *trackingMetadataCleanupExecutor) Observe(ctx context.Context, path string) (domain.FileObservation, error) {
	e.contentCalls++
	return e.inner.Observe(ctx, path)
}

func (e *trackingMetadataCleanupExecutor) ObserveMetadataFile(ctx context.Context, path string) (domain.FileObservation, error) {
	e.metadataCalls++
	return e.inner.ObserveMetadataFile(ctx, path)
}

func (e *trackingMetadataCleanupExecutor) MoveToQuarantine(ctx context.Context, source, target string, expected domain.FileObservation) (string, error) {
	return e.inner.MoveToQuarantine(ctx, source, target, expected)
}

func (e *trackingMetadataCleanupExecutor) RemoveQuarantined(ctx context.Context, path string, expected domain.FileObservation) error {
	return e.inner.RemoveQuarantined(ctx, path, expected)
}

func (e *trackingMetadataCleanupExecutor) RestoreFromQuarantine(ctx context.Context, source, target string, expected domain.FileObservation) error {
	return e.inner.RestoreFromQuarantine(ctx, source, target, expected)
}

func (e *trackingMetadataCleanupExecutor) MoveMaintenance(ctx context.Context, source, target string, expected domain.FileObservation, invocation executorpkg.MaintenanceInvocation) (string, error) {
	return e.inner.MoveMaintenance(ctx, source, target, expected, invocation)
}

func (e *trackingMetadataCleanupExecutor) PurgeMaintenance(ctx context.Context, path string, expected domain.FileObservation, invocation executorpkg.MaintenanceInvocation) error {
	return e.inner.PurgeMaintenance(ctx, path, expected, invocation)
}

func (e *trackingMetadataCleanupExecutor) ReadMaintenanceReceipt(ctx context.Context, invocation executorpkg.MaintenanceInvocation) (executorpkg.MaintenanceReceipt, error) {
	return e.inner.ReadMaintenanceReceipt(ctx, invocation)
}

func metadataCleanupEnvelope(t *testing.T, observation domain.FileObservation) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"algorithm": "metadata", "value": observation.Fingerprint, "size_bytes": observation.SizeBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func admitMetadataCleanup(t *testing.T, service *CleanupService, source, target string, observation domain.FileObservation) *CleanupOperation {
	t.Helper()
	input := cleanupInput()
	input.ID = "cleanup-metadata"
	input.IdempotencyKey = "cln:cleanup-metadata"
	input.SourcePath = source
	input.QuarantinePath = target
	input.SourceFingerprint = metadataCleanupEnvelope(t, observation)
	op, err := service.Admit(context.Background(), input)
	if err != nil {
		t.Fatalf("admit metadata cleanup: %v", err)
	}
	return op
}

func runMetadataQuarantine(t *testing.T, service *CleanupService, store *workitems.Store, op *CleanupOperation) {
	t.Helper()
	claim, err := store.ClaimDue(context.Background(), "metadata-quarantine", []string{workitems.KindCleanupQuarantine}, time.Minute)
	if err != nil {
		t.Fatalf("claim metadata quarantine: %v", err)
	}
	outcome, err := service.QuarantineHandler().Handle(context.Background(), claim)
	if err != nil {
		t.Fatalf("handle metadata quarantine: %v", err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("metadata quarantine outcome = %+v", outcome)
	}
	if err := store.FinishWithResult(context.Background(), claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatalf("finish metadata quarantine: %v", err)
	}
	var state string
	if err := service.db.QueryRow(`SELECT state FROM cleanup_operations WHERE id = $1`, op.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(CleanupQuarantined) {
		t.Fatalf("metadata quarantine state = %s", state)
	}
}

func TestCleanupMetadataFingerprintQuarantineAndPurgeWithoutContentReads(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "episode.mkv")
	target := filepath.Join(root, ".bansoko-cleanup-cleanup-metadata", "object")
	if err := os.WriteFile(source, []byte("metadata cleanup content"), 0o600); err != nil {
		t.Fatal(err)
	}
	observation, err := (filesystem.OSFileObserver{}).ObserveMetadataFile(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	executor := &trackingMetadataCleanupExecutor{inner: filesystem.NewOSCleanupExecutor(root)}
	service := NewCleanupService(db)
	store := cleanupWorkItemStore(t, db)
	service.SetWorkItems(store)
	service.SetFileExecutor(executor)
	op := admitMetadataCleanup(t, service, source, target, observation)
	runMetadataQuarantine(t, service, store, op)
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quarantine source still exists: %v", err)
	}

	claim, err := store.ClaimDue(ctx, "metadata-purge", []string{workitems.KindCleanupPurge}, time.Minute)
	if err != nil {
		t.Fatalf("claim metadata purge: %v", err)
	}
	outcome, err := service.PurgeHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatalf("handle metadata purge: %v", err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("metadata purge outcome = %+v", outcome)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metadata purge left quarantine object: %v", err)
	}
	if executor.contentCalls != 0 || executor.metadataCalls < 5 {
		t.Fatalf("cleanup observations: content=%d metadata=%d", executor.contentCalls, executor.metadataCalls)
	}
	var frozen, state string
	if err := db.QueryRow(`SELECT source_fingerprint, state FROM cleanup_operations WHERE id = $1`, op.ID).Scan(&frozen, &state); err != nil {
		t.Fatal(err)
	}
	if state != string(CleanupPurged) || frozen != op.SourceFingerprint {
		t.Fatalf("metadata cleanup final state=%s fingerprint=%q", state, frozen)
	}
}

func TestCleanupMetadataFingerprintResumeAndRestore(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "episode.mkv")
	target := filepath.Join(root, ".bansoko-cleanup-cleanup-metadata", "object")
	if err := os.WriteFile(source, []byte("metadata resume content"), 0o600); err != nil {
		t.Fatal(err)
	}
	observation, err := (filesystem.OSFileObserver{}).ObserveMetadataFile(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	executor := &trackingMetadataCleanupExecutor{inner: filesystem.NewOSCleanupExecutor(root)}
	service := NewCleanupService(db)
	store := cleanupWorkItemStore(t, db)
	service.SetWorkItems(store)
	service.SetFileExecutor(executor)
	op := admitMetadataCleanup(t, service, source, target, observation)
	attempt, err := json.Marshal(map[string]any{
		"cleanup_id": op.ID, "effect": op.Effect, "stage": "quarantine", "source_path": source,
		"source_fingerprint": op.SourceFingerprint, "quarantine_path": target,
		"source_observation": observation, "fencing_token": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	version, err := service.PersistAttempt(ctx, op.ID, op.Version, attempt)
	if err != nil {
		t.Fatalf("persist resumable metadata attempt: %v", err)
	}
	op.Version = version
	tag, err := workItemFixtureDB(t).Exec(`
UPDATE work_items SET expected_owner_version = $2
WHERE owner_type = $3 AND owner_id = $1 AND kind = 'cleanup_quarantine'`, op.ID, version, workitems.OwnerCleanup)
	if err != nil {
		t.Fatalf("align resumable work-item fencing version: %v", err)
	}
	if affected, err := tag.RowsAffected(); err != nil || affected != 1 {
		t.Fatalf("aligned %d resumable work items (err=%v), want exactly one", affected, err)
	}
	// No invocation receipt exists, the source matches, and the target is absent:
	// the real recovery handler must prove not-started with metadata and resume.
	runMetadataQuarantine(t, service, store, op)
	if executor.contentCalls != 0 {
		t.Fatalf("resume performed %d content observations", executor.contentCalls)
	}

	var restoreVersion int64
	if err := db.QueryRow(`SELECT version FROM cleanup_operations WHERE id = $1`, op.ID).Scan(&restoreVersion); err != nil {
		t.Fatal(err)
	}
	err = service.Restore(ctx, op.ID, restoreVersion)
	var operationError *filesystem.OperationError
	if !errors.As(err, &operationError) || operationError.Code != filesystem.CodeCleanupIdentityMutationUnsupported {
		t.Fatalf("metadata restore did not pass identity verification to the platform boundary: %v", err)
	}
	if _, statErr := os.Lstat(target); statErr != nil {
		t.Fatalf("restore failure moved the quarantine object: %v", statErr)
	}
	if executor.contentCalls != 0 {
		t.Fatalf("restore path performed %d service content observations", executor.contentCalls)
	}
}

func TestCleanupMetadataFingerprintReconcileUsesMetadataEvidence(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "episode.mkv")
	target := filepath.Join(root, ".bansoko-cleanup-cleanup-metadata", "object")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("metadata reconcile content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, target); err != nil {
		t.Fatal(err)
	}
	observation, err := (filesystem.OSFileObserver{}).ObserveMetadataFile(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	executor := &trackingMetadataCleanupExecutor{inner: filesystem.NewOSCleanupExecutor(root)}
	service := NewCleanupService(db)
	store := cleanupWorkItemStore(t, db)
	service.SetWorkItems(store)
	service.SetFileExecutor(executor)
	op := admitMetadataCleanup(t, service, source, target, observation)
	attempt, err := json.Marshal(map[string]any{
		"cleanup_id": op.ID, "effect": op.Effect, "stage": "quarantine", "source_path": source,
		"source_fingerprint": op.SourceFingerprint, "quarantine_path": target,
		"source_observation": observation, "fencing_token": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PersistAttempt(ctx, op.ID, op.Version, attempt); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	result, err := service.ReconcileUnknownEffect(ctx, deployment.ReconcileRequest{OperationID: "metadata-cleanup-reconcile"})
	if err != nil {
		t.Fatalf("reconcile metadata cleanup: %v", err)
	}
	if result.State != deployment.StateCompleted || result.Effect != deployment.EffectPresent {
		t.Fatalf("metadata reconcile result = %+v", result)
	}
	if executor.contentCalls != 0 || executor.metadataCalls < 2 {
		t.Fatalf("reconcile observations: content=%d metadata=%d", executor.contentCalls, executor.metadataCalls)
	}
}

func TestCleanupInvalidMetadataEnvelopeNeverFallsBackToContent(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "episode.mkv")
	if err := os.WriteFile(source, []byte("invalid metadata must not be hashed"), 0o600); err != nil {
		t.Fatal(err)
	}
	executor := &trackingMetadataCleanupExecutor{inner: filesystem.NewOSCleanupExecutor(root)}
	const frozen = `{"algorithm":"metadata","value":"meta:size=bad","size_bytes":34}`
	observed, err := observeCleanupFile(context.Background(), executor, source, frozen)
	if err != nil {
		t.Fatalf("observe invalid metadata envelope: %v", err)
	}
	if executor.contentCalls != 0 || executor.metadataCalls != 1 {
		t.Fatalf("invalid metadata observations: content=%d metadata=%d", executor.contentCalls, executor.metadataCalls)
	}
	if cleanupObservationMatchesFingerprint(observed, frozen) {
		t.Fatal("invalid metadata envelope matched a real file")
	}
}

func TestCleanupMetadataFingerprintComparisonRules(t *testing.T) {
	expected := domain.FileObservation{
		Fingerprint: "meta:size=64;dev=2;ino=12", SizeBytes: 64,
		Identity: domain.FileIdentity{Device: 2, Inode: 12},
	}
	envelope := `{"algorithm":"metadata","value":"meta:size=64;dev=2;ino=12","size_bytes":64}`
	remounted := expected
	remounted.Fingerprint = "meta:size=64;dev=9;ino=12"
	remounted.Identity.Device = 9
	if !cleanupObservationMatchesFingerprint(remounted, envelope) || !sameFrozenCleanupObservation(remounted, expected) {
		t.Fatal("device-only metadata change must retain path identity")
	}
	changedInode := remounted
	changedInode.Fingerprint = "meta:size=64;dev=9;ino=13"
	changedInode.Identity.Inode = 13
	if cleanupObservationMatchesFingerprint(changedInode, envelope) || sameFrozenCleanupObservation(changedInode, expected) {
		t.Fatal("inode change must fail closed")
	}
	changedSize := remounted
	changedSize.SizeBytes = 65
	changedSize.Fingerprint = "meta:size=65;dev=9;ino=12"
	if cleanupObservationMatchesFingerprint(changedSize, envelope) || sameFrozenCleanupObservation(changedSize, expected) {
		t.Fatal("size change must fail closed")
	}
	shaExpected := domain.FileObservation{
		Fingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SizeBytes: 64,
		Identity: domain.FileIdentity{Device: 2, Inode: 12},
	}
	shaSame := shaExpected
	if !sameFrozenCleanupObservation(shaSame, shaExpected) {
		t.Fatal("unchanged SHA-256 observation must still match")
	}
	shaSame.Fingerprint = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if sameFrozenCleanupObservation(shaSame, shaExpected) {
		t.Fatal("SHA-256 comparison must remain exact")
	}
}
