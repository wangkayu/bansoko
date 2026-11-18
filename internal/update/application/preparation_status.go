package application

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const preparationFailureFile = "preparation-failure.json"

// This receipt is only consumed by the status API; bootstrap still uses the
// pending marker and journal exclusively to decide how to start.
func (s *Service) persistPreparationFailure(status Status) error {
	if err := ensureOperationDir(s.updatesRoot, s.updatesRoot, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.updatesRoot, filepath.Join(s.updatesRoot, preparationFailureFile), data, 0o600)
}

func (s *Service) readPreparationFailure() (Status, bool, error) {
	data, err := readFileBounded(filepath.Join(s.updatesRoot, preparationFailureFile), 4096)
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, false, nil
	}
	if err != nil {
		return Status{}, false, fmt.Errorf("read update preparation receipt: %w", err)
	}
	var status Status
	if err := json.Unmarshal(data, &status); err != nil {
		return Status{}, false, fmt.Errorf("decode update preparation receipt: %w", err)
	}
	if !validMarkerID(status.OperationID) || !validMarkerID(status.TargetReleaseID) || status.Channel != Channel ||
		status.Step != "failed" || status.LastError == nil || status.UpdatedAt.Before(status.CreatedAt) || status.CreatedAt.IsZero() {
		return Status{}, false, errors.New("invalid update preparation receipt")
	}
	return status, true, nil
}
