package filesystem

import (
	maintenance "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/domain"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Separate maintenance token namespace; the existing placement marker envelope
// and exact permit proof are unchanged. Final marker is immutable and synced.
func writeMaintenanceStarted(i maintenance.MaintenanceInvocation) error {
	token, err := i.Token()
	if err != nil {
		return err
	}
	raw, err := captureMaintenanceStart(i)
	if err != nil {
		return err
	}
	_, err = WritePublicationMarker(i.Target, token+"-started", raw)
	return err
}

func writeMaintenanceFinal(i maintenance.MaintenanceInvocation, o domain.FileObservation) error {
	token, err := i.Token()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(maintenance.MaintenanceReceipt{Invocation: i, ExecutorFinal: true, Applied: true, Observation: o})
	if err != nil {
		return err
	}
	_, err = WritePublicationMarker(i.Target, token+"-final", raw)
	return err
}
func readMaintenanceFinal(ctx context.Context, i maintenance.MaintenanceInvocation) (maintenance.MaintenanceReceipt, error) {
	if err := validateMaintenanceNamespace(i); err != nil {
		return maintenance.MaintenanceReceipt{}, err
	}
	unlock, lockErr := lockMaintenanceInvocation(i)
	if lockErr != nil {
		return maintenance.MaintenanceReceipt{}, lockErr
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return maintenance.MaintenanceReceipt{}, err
	}
	token, err := i.Token()
	if err != nil {
		return maintenance.MaintenanceReceipt{}, err
	}
	started, _, err := ReadPublicationMarker(i.Target, token+"-started")
	if errors.Is(err, os.ErrNotExist) {
		return maintenance.MaintenanceReceipt{}, fmt.Errorf("%w: %w", maintenance.ErrMaintenanceNotStarted, err)
	}
	if err != nil {
		return maintenance.MaintenanceReceipt{}, err
	}
	var original maintenance.MaintenanceInvocation
	if json.Unmarshal(started, &original) != nil {
		return maintenance.MaintenanceReceipt{}, errors.New("invalid maintenance start marker")
	}
	originalToken, err := original.Token()
	if err != nil || originalToken != token {
		return maintenance.MaintenanceReceipt{}, errors.New("maintenance start identity mismatch")
	}
	raw, _, err := ReadPublicationMarker(i.Target, token+"-final")
	if errors.Is(err, os.ErrNotExist) {
		return adoptQuiescedMaintenance(ctx, i)
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
func (OSFileExecutor) ReadMaintenanceReceipt(ctx context.Context, i maintenance.MaintenanceInvocation) (maintenance.MaintenanceReceipt, error) {
	return readMaintenanceFinal(ctx, i)
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
		// A checkpoint can precede creation of the capture directory. Create
		// only the rooted, owner-bound 0700 namespace (never the object), so
		// the reader and executor use the same durable invocation lock even
		// in that window. A missing parent alone is not no-start proof.
		root, canonical, err := e.rootAndPath(i.Target)
		if err != nil {
			return maintenance.MaintenanceReceipt{}, err
		}
		if canonical != i.Target {
			return maintenance.MaintenanceReceipt{}, errors.New("quarantine receipt requires canonical path")
		}
		private, err := cleanupCaptureDirectory(root, i.Target, true)
		if err != nil {
			// The namespace holds every start marker; one that does not exist
			// and cannot be created holds none, so the stage never started.
			if namespaceAbsentAfterRefusal(i.Target, err) {
				return maintenance.MaintenanceReceipt{}, fmt.Errorf("%w: the capture namespace does not exist and cannot be created: %w", maintenance.ErrMaintenanceNotStarted, err)
			}
			return maintenance.MaintenanceReceipt{}, err
		}
		if err := private.Close(); err != nil {
			return maintenance.MaintenanceReceipt{}, err
		}
	}
	return readMaintenanceFinal(ctx, i)
}
func (e OSFileExecutor) ExecuteMaintenance(ctx context.Context, op domain.FileOperation, i maintenance.MaintenanceInvocation) (domain.FileOperation, error) {
	unlock, lockErr := lockMaintenanceInvocation(i)
	if lockErr != nil {
		return op, lockErr
	}
	defer unlock()
	if op.SourcePath != i.Source || op.TargetPath != i.Target || op.SourceFingerprint != i.Fingerprint {
		return op, errors.New("maintenance operation identity mismatch")
	}
	if _, err := i.Token(); err != nil {
		return op, err
	}
	if err := writeMaintenanceStarted(i); err != nil {
		return op, err
	}
	return e.executeMaintenanceStarted(ctx, op, i)
}

func (e OSFileExecutor) executeMaintenanceStarted(ctx context.Context, op domain.FileOperation, i maintenance.MaintenanceInvocation) (domain.FileOperation, error) {
	result, err := e.Execute(ctx, op)
	if err != nil {
		return result, err
	}
	observed, err := observeFileAs(result.TargetPath, i.Fingerprint)
	if err != nil {
		return result, err
	}
	if !fileObservationMatches(observed, i.Fingerprint, -1) {
		return result, errors.New("maintenance final fingerprint mismatch")
	}
	if err := writeMaintenanceFinal(i, observed); err != nil {
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
	unlock, lockErr := lockMaintenanceInvocation(i)
	if lockErr != nil {
		return op, lockErr
	}
	defer unlock()
	if op.SourcePath != i.Source || op.TargetPath != i.Target || op.SourceFingerprint != i.Fingerprint {
		return op, errors.New("maintenance operation identity mismatch")
	}
	if _, err := i.Token(); err != nil {
		return op, err
	}
	e.Checkpoint = checkpoint
	if op.OverwriteRecovery == nil {
		if err := writeMaintenanceStarted(i); err != nil {
			return op, err
		}
	} else {
		op.SourceParentIdentity = op.OverwriteRecovery.SourceParentIdentity
		op.TargetParentIdentity = op.OverwriteRecovery.TargetParentIdentity
	}
	return e.executeMaintenanceStarted(ctx, op, i)
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
	unlock, lockErr := lockMaintenanceInvocation(i)
	if lockErr != nil {
		return "", lockErr
	}
	defer unlock()
	if source != i.Source || target != i.Target || expected.Fingerprint != i.Fingerprint {
		return "", errors.New("maintenance quarantine identity mismatch")
	}
	if _, err := i.Token(); err != nil {
		return "", err
	}
	if err := writeMaintenanceStarted(i); err != nil {
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
	return result, writeMaintenanceFinal(i, observed)
}

// DiscardRefusedMaintenance removes the start marker and lock of a deletion
// stage whose rename or unlink its caller saw refused and proved unapplied;
// the caller retries under a new invocation. A final receipt is never
// removed.
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
	return discardRefusedMaintenance(i)
}

func (e OSCleanupExecutor) PurgeMaintenance(ctx context.Context, path string, expected domain.FileObservation, i maintenance.MaintenanceInvocation) error {
	if err := validateMaintenanceNamespace(i); err != nil {
		return err
	}
	unlock, lockErr := lockMaintenanceInvocation(i)
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	if path != i.Source || path != i.Target || expected.Fingerprint != i.Fingerprint {
		return errors.New("maintenance purge identity mismatch")
	}
	if _, err := i.Token(); err != nil {
		return err
	}
	// The receipt records this exact-object PurgeCaptured invocation's nil
	// return, NOT a later absence observation. Crash before receipt stays unknown.
	if err := writeMaintenanceStarted(i); err != nil {
		return err
	}
	if err := e.PurgeCaptured(ctx, path, expected); err != nil {
		return err
	}
	return writeMaintenanceFinal(i, expected)
}
