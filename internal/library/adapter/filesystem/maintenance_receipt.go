package filesystem

import (
	maintenance "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/domain"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// The invocation's start proof and final receipt live in the receipt store
// (PostgreSQL in production), never in the owner's library. The store's
// advisory lock replaces the filesystem lock inode: two executors of one
// invocation exclude each other while they share the database.
func maintenanceReceipts(store maintenance.ReceiptStore) (maintenance.ReceiptStore, error) {
	if store == nil {
		return nil, errors.New("maintenance receipt store is not wired")
	}
	return store, nil
}

// The start proof binds the source observation and both parent identities
// before any mutation; the final receipt is written exactly once and never
// updated. Both payloads keep the marker envelopes' JSON shape.
func writeMaintenanceStarted(ctx context.Context, store maintenance.ReceiptStore, i maintenance.MaintenanceInvocation) error {
	token, err := i.Token()
	if err != nil {
		return err
	}
	raw, err := captureMaintenanceStart(i)
	if err != nil {
		return err
	}
	return store.WriteStarted(ctx, token, raw)
}

func writeMaintenanceFinal(ctx context.Context, store maintenance.ReceiptStore, i maintenance.MaintenanceInvocation, o domain.FileObservation) error {
	token, err := i.Token()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(maintenance.MaintenanceReceipt{Invocation: i, ExecutorFinal: true, Applied: true, Observation: o})
	if err != nil {
		return err
	}
	return store.WriteFinal(ctx, token, raw)
}
func readMaintenanceFinal(ctx context.Context, store maintenance.ReceiptStore, i maintenance.MaintenanceInvocation) (maintenance.MaintenanceReceipt, error) {
	if err := validateMaintenanceNamespace(i); err != nil {
		return maintenance.MaintenanceReceipt{}, err
	}
	token, err := i.Token()
	if err != nil {
		return maintenance.MaintenanceReceipt{}, err
	}
	unlock, err := store.Lock(ctx, token)
	if err != nil {
		return maintenance.MaintenanceReceipt{}, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return maintenance.MaintenanceReceipt{}, err
	}
	started, err := store.ReadStarted(ctx, token)
	if errors.Is(err, maintenance.ErrMaintenanceNotStarted) {
		return maintenance.MaintenanceReceipt{}, err
	}
	if err != nil {
		return maintenance.MaintenanceReceipt{}, err
	}
	var original maintenance.MaintenanceInvocation
	if json.Unmarshal(started, &original) != nil {
		return maintenance.MaintenanceReceipt{}, errors.New("invalid maintenance start proof")
	}
	originalToken, err := original.Token()
	if err != nil || originalToken != token {
		return maintenance.MaintenanceReceipt{}, errors.New("maintenance start identity mismatch")
	}
	raw, err := store.ReadFinal(ctx, token)
	if errors.Is(err, maintenance.ErrMaintenanceFinalAbsent) {
		return adoptQuiescedMaintenance(ctx, store, i)
	}
	if err != nil {
		return maintenance.MaintenanceReceipt{}, err
	}
	var r maintenance.MaintenanceReceipt
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, err
	}
	return r, r.Validate(i)
}
func (e OSFileExecutor) ReadMaintenanceReceipt(ctx context.Context, i maintenance.MaintenanceInvocation) (maintenance.MaintenanceReceipt, error) {
	store, err := maintenanceReceipts(e.Receipts)
	if err != nil {
		return maintenance.MaintenanceReceipt{}, err
	}
	return readMaintenanceFinal(ctx, store, i)
}
func (e OSCleanupExecutor) ReadMaintenanceReceipt(ctx context.Context, i maintenance.MaintenanceInvocation) (maintenance.MaintenanceReceipt, error) {
	if err := ctx.Err(); err != nil {
		return maintenance.MaintenanceReceipt{}, err
	}
	if err := validateMaintenanceNamespace(i); err != nil {
		return maintenance.MaintenanceReceipt{}, err
	}
	if i.Kind == cleanupCaptureKind || i.Kind == fileManagerQuarantineKind {
		if !isQuarantineInvocation(i) {
			return maintenance.MaintenanceReceipt{}, errors.New("quarantine receipt requires its capturing owner")
		}
		if _, err := i.Token(); err != nil {
			return maintenance.MaintenanceReceipt{}, err
		}
		// The receipt no longer lives in the capture namespace, so a read
		// never creates it: an absent namespace with no start row is simply
		// an invocation that never started.
		if _, canonical, err := e.rootAndPath(i.Target); err != nil {
			return maintenance.MaintenanceReceipt{}, err
		} else if canonical != i.Target {
			return maintenance.MaintenanceReceipt{}, errors.New("quarantine receipt requires canonical path")
		}
	}
	store, err := maintenanceReceipts(e.Receipts)
	if err != nil {
		return maintenance.MaintenanceReceipt{}, err
	}
	return readMaintenanceFinal(ctx, store, i)
}
func (e OSFileExecutor) ExecuteMaintenance(ctx context.Context, op domain.FileOperation, i maintenance.MaintenanceInvocation) (domain.FileOperation, error) {
	store, err := maintenanceReceipts(e.Receipts)
	if err != nil {
		return op, err
	}
	token, err := i.Token()
	if err != nil {
		return op, err
	}
	unlock, err := store.Lock(ctx, token)
	if err != nil {
		return op, err
	}
	defer unlock()
	if op.SourcePath != i.Source || op.TargetPath != i.Target || op.SourceFingerprint != i.Fingerprint {
		return op, errors.New("maintenance operation identity mismatch")
	}
	if err := writeMaintenanceStarted(ctx, store, i); err != nil {
		return op, err
	}
	return e.executeMaintenanceStarted(ctx, store, op, i)
}

func (e OSFileExecutor) executeMaintenanceStarted(ctx context.Context, store maintenance.ReceiptStore, op domain.FileOperation, i maintenance.MaintenanceInvocation) (domain.FileOperation, error) {
	result, err := e.Execute(ctx, op)
	if err != nil {
		return result, err
	}
	observed, err := observeFileAs(result.TargetPath, i.Fingerprint)
	if err != nil {
		return result, err
	}
	matched := fileObservationMatches(observed, i.Fingerprint, -1)
	if op.Operation == domain.OperationCopy {
		if expected, ok := domain.ParseMetadataFingerprint(i.Fingerprint); ok {
			// A copy creates a new object with a new inode: the frozen
			// path-level identity contributes only its size; the byte count
			// was verified in-stream by the copy itself.
			matched = observed.SizeBytes == expected.SizeBytes
		}
	}
	if !matched {
		return result, errors.New("maintenance final fingerprint mismatch")
	}
	if err := writeMaintenanceFinal(ctx, store, i, observed); err != nil {
		return result, fmt.Errorf("maintenance effect returned; final receipt failed: %w", err)
	}
	return result, nil
}

// ExecuteMaintenanceOverwrite runs a maintenance move under the overwrite
// policy with a durable checkpoint. A fresh call (no recovery record) writes
// the started receipt like ExecuteMaintenance. A resumed call (the persisted
// recovery record) keeps the started receipt of the interrupted invocation
// and lets the executor finish the exchange or restore the replaced file.
func (e OSFileExecutor) ExecuteMaintenanceOverwrite(ctx context.Context, op domain.FileOperation, i maintenance.MaintenanceInvocation, checkpoint domain.FileOperationCheckpoint) (domain.FileOperation, error) {
	if checkpoint == nil || op.CollisionPolicy != domain.CollisionOverwrite {
		return op, errors.New("maintenance overwrite requires the overwrite policy and a durable checkpoint")
	}
	store, err := maintenanceReceipts(e.Receipts)
	if err != nil {
		return op, err
	}
	token, err := i.Token()
	if err != nil {
		return op, err
	}
	unlock, err := store.Lock(ctx, token)
	if err != nil {
		return op, err
	}
	defer unlock()
	if op.SourcePath != i.Source || op.TargetPath != i.Target || op.SourceFingerprint != i.Fingerprint {
		return op, errors.New("maintenance operation identity mismatch")
	}
	e.Checkpoint = checkpoint
	if op.OverwriteRecovery == nil {
		if err := writeMaintenanceStarted(ctx, store, i); err != nil {
			return op, err
		}
	} else {
		op.SourceParentIdentity = op.OverwriteRecovery.SourceParentIdentity
		op.TargetParentIdentity = op.OverwriteRecovery.TargetParentIdentity
	}
	return e.executeMaintenanceStarted(ctx, store, op, i)
}

// ResumeOverwrite finishes or restores an interrupted maintenance exchange
// from its persisted recovery record without a work-item invocation.
func (e OSFileExecutor) ResumeOverwrite(ctx context.Context, op domain.FileOperation, checkpoint domain.FileOperationCheckpoint) (domain.FileOperation, error) {
	if checkpoint == nil || op.CollisionPolicy != domain.CollisionOverwrite || op.OverwriteRecovery == nil {
		return op, errors.New("overwrite resume requires the persisted recovery record and a durable checkpoint")
	}
	e.Checkpoint = checkpoint
	op.SourceParentIdentity = op.OverwriteRecovery.SourceParentIdentity
	op.TargetParentIdentity = op.OverwriteRecovery.TargetParentIdentity
	return e.Execute(ctx, op)
}
func (e OSCleanupExecutor) MoveMaintenance(ctx context.Context, source, target string, expected domain.FileObservation, i maintenance.MaintenanceInvocation) (string, error) {
	if err := validateMaintenanceNamespace(i); err != nil {
		return "", err
	}
	root, canonical, err := e.rootAndPath(target)
	if err != nil {
		return "", err
	}
	if canonical != target {
		return "", errors.New("quarantine receipt requires canonical path")
	}
	private, err := cleanupCaptureDirectory(root, target, true)
	if err != nil {
		return "", err
	}
	if err := private.Close(); err != nil {
		return "", err
	}
	store, err := maintenanceReceipts(e.Receipts)
	if err != nil {
		return "", err
	}
	token, err := i.Token()
	if err != nil {
		return "", err
	}
	unlock, err := store.Lock(ctx, token)
	if err != nil {
		return "", err
	}
	defer unlock()
	if source != i.Source || target != i.Target || expected.Fingerprint != i.Fingerprint {
		return "", errors.New("maintenance quarantine identity mismatch")
	}
	if err := writeMaintenanceStarted(ctx, store, i); err != nil {
		return "", err
	}
	result, err := e.CaptureForDelete(ctx, source, target, expected)
	if err != nil {
		return result, err
	}
	var observed domain.FileObservation
	if domain.IsMetadataFingerprint(expected.Fingerprint) {
		observed, err = e.ObserveMetadataFile(context.WithoutCancel(ctx), result)
	} else {
		observed, err = e.Observe(context.WithoutCancel(ctx), result)
	}
	if err != nil || !sameMutationFileObservation(observed, expected) {
		return result, errors.New("maintenance quarantine final identity mismatch")
	}
	return result, writeMaintenanceFinal(ctx, store, i, observed)
}

// DiscardRefusedMaintenance removes the start proof of a deletion stage whose
// rename or unlink its caller saw refused and proved unapplied; the caller
// retries under a new invocation. A final receipt is never removed.
func (e OSCleanupExecutor) DiscardRefusedMaintenance(ctx context.Context, i maintenance.MaintenanceInvocation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateMaintenanceNamespace(i); err != nil {
		return err
	}
	if i.Kind != cleanupCaptureKind && i.Kind != cleanupPurgeKind {
		return errors.New("only a deletion stage is discarded")
	}
	store, err := maintenanceReceipts(e.Receipts)
	if err != nil {
		return err
	}
	token, err := i.Token()
	if err != nil {
		return err
	}
	unlock, err := store.Lock(ctx, token)
	if err != nil {
		return err
	}
	defer unlock()
	return store.DiscardStarted(ctx, token)
}

func (e OSCleanupExecutor) PurgeMaintenance(ctx context.Context, path string, expected domain.FileObservation, i maintenance.MaintenanceInvocation) error {
	if err := validateMaintenanceNamespace(i); err != nil {
		return err
	}
	store, err := maintenanceReceipts(e.Receipts)
	if err != nil {
		return err
	}
	token, err := i.Token()
	if err != nil {
		return err
	}
	unlock, err := store.Lock(ctx, token)
	if err != nil {
		return err
	}
	defer unlock()
	if path != i.Source || path != i.Target || expected.Fingerprint != i.Fingerprint {
		return errors.New("maintenance purge identity mismatch")
	}
	// The receipt records this exact-object PurgeCaptured invocation's nil
	// return, NOT a later absence observation. Crash before receipt stays unknown.
	if err := writeMaintenanceStarted(ctx, store, i); err != nil {
		return err
	}
	if err := e.PurgeCaptured(ctx, path, expected); err != nil {
		return err
	}
	if err := writeMaintenanceFinal(ctx, store, i, expected); err != nil {
		return err
	}
	// The object is gone and its purge receipt final: the namespace directory
	// now holds nothing and no later stage revisits it.
	if root, canonical, err := e.rootAndPath(path); err == nil && canonical == path {
		removeEmptyCleanupNamespace(root, path)
	}
	return nil
}
