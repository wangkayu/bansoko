package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// CheckpointProtocolVersion guards the JSONB checkpoint shape.
const CheckpointProtocolVersion = 1

// CallRecord is one orchestration fact: which registration was called, in
// which sequence, with which typed result. It never copies owner-private
// checkpoint data or external effect observations.
type CallRecord struct {
	Key        string          `json:"key"`
	Sequence   int             `json:"sequence"`
	State      ReconcileState  `json:"state"`
	Result     ReconcileResult `json:"result,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	CalledAt   string          `json:"called_at"`
	RetryCount int             `json:"retry_count,omitempty"`
}

// Checkpoint is the only mutable orchestration fact of a recovery run.
type Checkpoint struct {
	ProtocolVersion int           `json:"protocol_version"`
	Calls           []CallRecord  `json:"calls"`
	Retries         int           `json:"retries"`
	Gate            *GateDecision `json:"gate,omitempty"`
}

// NewCheckpoint starts an empty checkpoint for the current protocol version.
func NewCheckpoint() Checkpoint {
	return Checkpoint{ProtocolVersion: CheckpointProtocolVersion}
}

// CallFor returns the latest call record for a registration key, if any.
func (c Checkpoint) CallFor(key string) (CallRecord, bool) {
	for i := len(c.Calls) - 1; i >= 0; i-- {
		if c.Calls[i].Key == key {
			return c.Calls[i], true
		}
	}
	return CallRecord{}, false
}

// Settle records the outcome of one registration call. A call whose latest
// state is a safe terminal state (completed) is never overwritten by a
// replayed run — the orchestrator skips it before calling.
func (c *Checkpoint) Settle(key string, state ReconcileState, result ReconcileResult, reason string, retry bool) {
	if retry {
		c.Retries++
	}
	sequence := len(c.Calls) + 1
	c.Calls = append(c.Calls, CallRecord{
		Key:        key,
		Sequence:   sequence,
		State:      state,
		Result:     result,
		Reason:     reason,
		CalledAt:   time.Now().UTC().Format(time.RFC3339),
		RetryCount: 0,
	})
}

// MarshalJSON serializes the checkpoint for the durable store.
func (c Checkpoint) MarshalJSON() ([]byte, error) {
	type alias Checkpoint
	return json.Marshal(alias(c))
}

// UnmarshalCheckpoint decodes a stored checkpoint and rejects unknown
// protocol versions (fail closed on incompatible shapes).
func UnmarshalCheckpoint(raw []byte) (Checkpoint, error) {
	var c Checkpoint
	if err := json.Unmarshal(raw, &c); err != nil {
		return Checkpoint{}, fmt.Errorf("decode deployment checkpoint: %w", err)
	}
	if c.ProtocolVersion != CheckpointProtocolVersion {
		return Checkpoint{}, fmt.Errorf("unsupported deployment checkpoint protocol version %d", c.ProtocolVersion)
	}
	sort.SliceStable(c.Calls, func(i, j int) bool { return c.Calls[i].Sequence < c.Calls[j].Sequence })
	return c, nil
}

// Run is the durable deployment recovery run record.
type Run struct {
	ID                string
	WindowID          string
	OldBundleIdentity string
	NewBundleIdentity string
	BackupIdentity    string
	SchemaIdentity    string
	BuildIdentity     string
	RegistryInventory []InventoryEntry
	Checkpoint        Checkpoint
	State             string // running|completed|failed
	Version           int64
	StartedAt         time.Time
	UpdatedAt         time.Time
}

// Store persists recovery runs. Implementations must be durable and atomic;
// repeated CLI invocations resume from the stored checkpoint.
type Store interface {
	Load(ctx context.Context, id string) (*Run, error)
	Create(ctx context.Context, run *Run) error
	// Update applies the mutator to the current row and persists it with
	// optimistic concurrency. It returns the new version.
	Update(ctx context.Context, id string, expectedVersion int64, mutate func(*Run)) (int64, error)
	// Latest returns the most recently updated run, or nil when no run exists.
	Latest(ctx context.Context) (*Run, error)
}

// ErrCheckpointSchemaMissing is returned when the deployment checkpoint
// table does not exist (for example a pre-030 PostgreSQL backup). The
// recovery CLI fails closed on it.
type ErrCheckpointSchemaMissing struct{}

func (e *ErrCheckpointSchemaMissing) Error() string {
	return "deployment checkpoint schema missing (deployment_recovery_runs not found)"
}
