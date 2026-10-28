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

// Called only while the executor's exclusive lock is held by recovery. A
// started marker identifies this attempt, but cannot prove irreversible purge.
func adoptQuiescedMaintenance(ctx context.Context, i maintenance.MaintenanceInvocation) (maintenance.MaintenanceReceipt, error) {
	if isPurgeInvocation(i) {
		if err := purgeProvablyNotApplied(ctx, i); err != nil {
			return maintenance.MaintenanceReceipt{}, err
		}
		return maintenance.MaintenanceReceipt{}, errors.New("purge crash gap has no exact deletion receipt")
	}
	if err := ctx.Err(); err != nil {
		return maintenance.MaintenanceReceipt{}, err
	}
	token, err := i.Token()
	if err != nil {
		return maintenance.MaintenanceReceipt{}, err
	}
	raw, _, err := ReadPublicationMarker(i.Target, token+"-started")
	if err != nil {
		return maintenance.MaintenanceReceipt{}, err
	}
	var original maintenanceStartProof
	if json.Unmarshal(raw, &original) != nil {
		return maintenance.MaintenanceReceipt{}, errors.New("invalid start identity")
	}
	other, err := original.Token()
	if err != nil || other != token {
		return maintenance.MaintenanceReceipt{}, errors.New("foreign start identity")
	}
	if source, err := observeFileAs(i.Source, i.Fingerprint); !errors.Is(err, os.ErrNotExist) {
		if err == nil && captureProvablyNotApplied(i, original, source) {
			return maintenance.MaintenanceReceipt{}, fmt.Errorf("%w: the source is still the started object and nothing was captured", maintenance.ErrMaintenanceNotApplied)
		}
		return maintenance.MaintenanceReceipt{}, errors.New("source still present/conflicting; adopt rejected")
	}
	if err := validateMaintenanceStartAnchors(original); err != nil {
		return maintenance.MaintenanceReceipt{}, err
	}
	observed, err := observeFileAs(i.Target, i.Fingerprint)
	if err != nil || !sameMutationFileObservation(observed, original.SourceObservation) {
		return maintenance.MaintenanceReceipt{}, errors.New("target facts do not prove applied move")
	}
	if isQuarantineInvocation(i) {
		var attempt struct {
			SourceObservation domain.FileObservation `json:"source_observation"`
		}
		if json.Unmarshal(i.Attempt, &attempt) != nil || !sameMutationFileObservation(observed, attempt.SourceObservation) {
			return maintenance.MaintenanceReceipt{}, errors.New("captured quarantine identity mismatch")
		}
	}
	if err := writeMaintenanceFinal(i, observed); err != nil {
		return maintenance.MaintenanceReceipt{}, err
	}
	return maintenance.MaintenanceReceipt{Invocation: i, ExecutorFinal: true, Applied: true, Observation: observed}, nil
}

// captureProvablyNotApplied is the not-applied proof of a replacement
// deletion's capture whose start marker has no final receipt: the source at
// its public path is still the object the start marker froze, both parents
// are the frozen ones, and nothing sits at the capture path. A rename is
// atomic, so the capture did not happen and the stage may run again. Only the
// replacement deletion owner reads this proof; every other invocation keeps
// the adopt-or-reject rule.
func captureProvablyNotApplied(i maintenance.MaintenanceInvocation, original maintenanceStartProof, source domain.FileObservation) bool {
	if i.OwnerType != cleanupOwnerType || i.Kind != cleanupCaptureKind {
		return false
	}
	if !sameMutationFileObservation(source, original.SourceObservation) {
		return false
	}
	if validateMaintenanceStartAnchors(original) != nil {
		return false
	}
	_, err := observeFileAs(i.Target, i.Fingerprint)
	return errors.Is(err, os.ErrNotExist)
}

// purgeProvablyNotApplied is the not-applied proof of a replacement
// deletion's purge whose start marker has no final receipt: the captured
// object is still present at the capture path and identical to the frozen
// capture observation of the attempt the invocation binds. An unlink is
// atomic, so the purge did not happen. A missing object proves nothing and
// stays an unknown effect.
func purgeProvablyNotApplied(ctx context.Context, i maintenance.MaintenanceInvocation) error {
	if i.OwnerType != cleanupOwnerType || i.Kind != cleanupPurgeKind || i.Source != i.Target {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var attempt struct {
		CaptureObservation domain.FileObservation `json:"capture_observation"`
	}
	if json.Unmarshal(i.Attempt, &attempt) != nil || attempt.CaptureObservation.Fingerprint != i.Fingerprint {
		return nil
	}
	observed, err := observeFileAs(i.Target, i.Fingerprint)
	if err != nil || !sameMutationFileObservation(observed, attempt.CaptureObservation) {
		return nil
	}
	return fmt.Errorf("%w: the captured object is still present", maintenance.ErrMaintenanceNotApplied)
}
