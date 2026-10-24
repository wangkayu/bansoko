package filesystem

import (
	maintenance "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/domain"
	"context"
	"encoding/json"
	"errors"
	"os"
)

// Called only while the executor's exclusive lock is held by recovery. A
// started marker identifies this attempt, but cannot prove irreversible purge.
func adoptQuiescedMaintenance(ctx context.Context, i maintenance.MaintenanceInvocation) (maintenance.MaintenanceReceipt, error) {
	if isPurgeInvocation(i) {
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
	if _, err := observeFileAs(i.Source, i.Fingerprint); !errors.Is(err, os.ErrNotExist) {
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
