package executor

import (
	"bansoko/internal/library/domain"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// MaintenanceInvocation binds a receipt to the durable owner checkpoint, not
// merely a path. Attempt is immutable for this invocation; each new checkpoint
// generates a different token. The receipt proves the synchronous executor
// returned, never that an absent path implies an effect.
type MaintenanceInvocation struct {
	OwnerType    string          `json:"owner_type"`
	OwnerID      string          `json:"owner_id"`
	Kind         string          `json:"kind"`
	WorkItemID   string          `json:"work_item_id"`
	OwnerVersion int64           `json:"owner_version"`
	Attempt      json.RawMessage `json:"attempt"`
	Source       string          `json:"source"`
	Target       string          `json:"target"`
	Fingerprint  string          `json:"fingerprint"`
}

func (i MaintenanceInvocation) Token() (string, error) {
	if i.OwnerType == "" || i.OwnerID == "" || i.Kind == "" || i.WorkItemID == "" || i.OwnerVersion < 1 || !json.Valid(i.Attempt) || i.Source == "" || i.Target == "" {
		return "", errors.New("invalid maintenance invocation")
	}
	raw, err := json.Marshal(i)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "maintenance-" + hex.EncodeToString(sum[:]), nil
}

// ErrMaintenanceNotStarted is returned by ReadMaintenanceReceipt when the exact
// invocation, read under its own lock, has no start marker. The executor syncs
// that marker before any mutation, so it proves the invocation never reached
// its effect; it proves nothing about the paths themselves.
var ErrMaintenanceNotStarted = errors.New("maintenance invocation has no start marker")

type MaintenanceReceipt struct {
	Invocation    MaintenanceInvocation  `json:"invocation"`
	ExecutorFinal bool                   `json:"executor_final"`
	Applied       bool                   `json:"applied"`
	Observation   domain.FileObservation `json:"observation"`
}

func (r MaintenanceReceipt) Validate(i MaintenanceInvocation) error {
	a, err := i.Token()
	if err != nil {
		return err
	}
	b, err := r.Invocation.Token()
	if err != nil || a != b || !r.ExecutorFinal || !r.Applied {
		return fmt.Errorf("maintenance receipt is not exact applied final proof")
	}
	return nil
}

type MaintenanceFileExecutor interface {
	ExecuteMaintenance(context.Context, domain.FileOperation, MaintenanceInvocation) (domain.FileOperation, error)
	ReadMaintenanceReceipt(context.Context, MaintenanceInvocation) (MaintenanceReceipt, error)
}

// MaintenanceOverwriteExecutor runs a maintenance move under the overwrite
// collision policy. Every exchange stage is handed to checkpoint, which must
// persist it durably before the executor continues. With
// op.OverwriteRecovery set it resumes from that persisted record: it finishes
// the exchange or restores the replaced file, never guessing.
// ResumeOverwrite does the same outside a work-item invocation (deployment
// reconcile) and writes no receipt.
type MaintenanceOverwriteExecutor interface {
	ExecuteMaintenanceOverwrite(context.Context, domain.FileOperation, MaintenanceInvocation, domain.FileOperationCheckpoint) (domain.FileOperation, error)
	ResumeOverwrite(context.Context, domain.FileOperation, domain.FileOperationCheckpoint) (domain.FileOperation, error)
}
type MaintenanceCleanupExecutor interface {
	MoveMaintenance(context.Context, string, string, domain.FileObservation, MaintenanceInvocation) (string, error)
	PurgeMaintenance(context.Context, string, domain.FileObservation, MaintenanceInvocation) error
	ReadMaintenanceReceipt(context.Context, MaintenanceInvocation) (MaintenanceReceipt, error)
}
