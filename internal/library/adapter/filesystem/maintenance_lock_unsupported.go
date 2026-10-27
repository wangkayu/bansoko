//go:build !linux && !darwin

package filesystem

import (
	maintenance "bansoko/internal/filemaintenance/executor"
	"errors"
)

func lockMaintenanceInvocation(maintenance.MaintenanceInvocation) (func(), error) {
	return nil, errors.New("maintenance invocation locking unsupported")
}

func discardRefusedMaintenance(maintenance.MaintenanceInvocation) error {
	return errors.New("maintenance invocation discard unsupported")
}
