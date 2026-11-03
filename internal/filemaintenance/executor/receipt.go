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

// ErrMaintenanceNotApplied is returned by ReadMaintenanceReceipt for a
// deletion-owner capture or purge whose start marker exists without a final
// receipt, when the executor, under the invocation lock, observes that the
// effect did not happen: a capture's source still sits at its public path as
// the started object with nothing at the capture path, or a purge's captured
// object is still present and identical. The syscall refused (a busy file),
// so the stage may run again. Absence is never this proof: a missing object
// stays an unknown effect.
var ErrMaintenanceNotApplied = errors.New("maintenance invocation started but its effect provably did not apply")

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

// ErrMaintenanceFinalAbsent is returned by a ReceiptStore read of the final
// receipt when the invocation started but no final row exists yet. The reader
// then proves the effect from the filesystem (the adopt/not-applied path).
var ErrMaintenanceFinalAbsent = errors.New("maintenance invocation has no final receipt")

// ErrMaintenanceLocked is returned by ReceiptStore.Lock when another executor
// holds the invocation. Like the file lock it replaces, acquisition never
// waits: the caller retries the stage later.
var ErrMaintenanceLocked = errors.New("maintenance invocation is already locked")

// ReceiptStore is the durable home of one invocation's start proof and final
// receipt. It replaces the marker files the executor used to leave next to
// the operation's target: the rows, not the library, carry the proof, and the
// lock rows nothing at all — Lock is the database's advisory lock, so two
// executors of one invocation exclude each other only while they share this
// database.
//
// WriteStarted refuses an existing token (a retried stage runs under a new
// invocation). WriteFinal completes the started row exactly once.
// DiscardStarted removes the start proof of an invocation whose effect
// provably never applied, and never a row with a final receipt.
type ReceiptStore interface {
	Lock(ctx context.Context, token string) (unlock func(), err error)
	WriteStarted(ctx context.Context, token string, proof []byte) error
	ReadStarted(ctx context.Context, token string) ([]byte, error)
	WriteFinal(ctx context.Context, token string, receipt []byte) error
	ReadFinal(ctx context.Context, token string) ([]byte, error)
	DiscardStarted(ctx context.Context, token string) error
}
