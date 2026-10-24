// Package deployment owns the deployment recovery reconcile service: a typed
// reconciler registry, the orchestration/checkpoint reducer, and the
// machine-readable automation resume gate. It never reads owner-private
// checkpoints, never observes qB/filesystem effects, and never mutates any
// owner row — every owner convergence happens inside owner-specific
// ReconcileUnknownEffect handlers in their own application contexts.
package deployment

import (
	"context"
	"time"
)

// ReconcileState is the typed outcome a handler settles after checking its
// own frozen attempt/checkpoint against external observation.
type ReconcileState string

const (
	// StateCompleted means the handler converged its owner within its own
	// application transaction (or proved there is no effect to settle).
	StateCompleted ReconcileState = "completed"
	// StateRetryable means the handler could not decide yet (observation
	// failed, provider unreachable); the orchestrator will retry.
	StateRetryable ReconcileState = "retryable"
	// StateBlocked means the effect cannot be settled safely and needs
	// operator attention (for example ambiguous qB correlation). The gate
	// must fail closed.
	StateBlocked ReconcileState = "blocked"
	// StateConflict means external evidence contradicts the frozen attempt
	// (fingerprint/correlation changed); fail closed.
	StateConflict ReconcileState = "conflict"
	// StateFailed means the handler errored terminally; fail closed.
	StateFailed ReconcileState = "failed"
)

// EffectPresence describes whether the handler found an external effect.
type EffectPresence string

const (
	EffectNone    EffectPresence = "none"
	EffectPresent EffectPresence = "present"
	EffectUnknown EffectPresence = "unknown"
)

// ReconcileRequest carries the stable identity of one deployment recovery
// operation. Handlers receive the same operation id on retries so their
// idempotency/fencing stays anchored to one deployment window.
type ReconcileRequest struct {
	OperationID string
	WindowID    string
	// OwnerEffect identifies the owner+effect scope, e.g. "download_task/qb".
	OwnerEffect string
}

// ReconcileResult is the typed handler verdict. EvidenceRef points at an
// owner-owned durable fact (operation/attempt id); it never exposes mutable
// owner internals to the orchestrator.
type ReconcileResult struct {
	State       ReconcileState
	Effect      EffectPresence
	Reason      string
	EvidenceRef string
}

// ReconcileHandler is the owner-specific typed port. Implementations live in
// their owner packages and are wired by the composition root; this package
// only calls the port.
type ReconcileHandler interface {
	ReconcileUnknownEffect(ctx context.Context, req ReconcileRequest) (ReconcileResult, error)
}

// EffectIdentity is the stable owner/effect identity of a registration.
type EffectIdentity struct {
	Owner  string `json:"owner"`
	Effect string `json:"effect"`
}

// Registration is the composition-time typed wiring. Each registration has a
// stable unique key, a supported version, an enabled predicate (production-
// enabled effect owner) and the handler implementation.
type Registration struct {
	Key     string
	Effect  EffectIdentity
	Version int
	Enabled func() bool
	Handler ReconcileHandler
}

// InventoryEntry is the versioned registry fact persisted into the run
// checkpoint and emitted in the machine-readable output.
type InventoryEntry struct {
	Key     string         `json:"key"`
	Owner   string         `json:"owner"`
	Effect  string         `json:"effect"`
	Version int            `json:"version"`
	Enabled bool           `json:"enabled"`
	State   ReconcileState `json:"state,omitempty"`
}

// GateDecision is the fail-closed automation resume verdict.
type GateDecision struct {
	Passed   bool     `json:"passed"`
	Failures []string `json:"failures,omitempty"`
	Time     string   `json:"generated_at"`
}

// NewGateDecision builds a decision with a stable timestamp layout.
func NewGateDecision(passed bool, failures []string) GateDecision {
	return GateDecision{Passed: passed, Failures: failures, Time: time.Now().UTC().Format(time.RFC3339)}
}
