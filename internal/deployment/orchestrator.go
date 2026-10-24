package deployment

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Plan is the frozen identity of one recovery operation. It is recorded once
// and never mutated by orchestration.
type Plan struct {
	OperationID       string
	WindowID          string
	OldBundleIdentity string
	NewBundleIdentity string
	BackupIdentity    string
	SchemaIdentity    string
	BuildIdentity     string
	// OldBundleSmokePassed is asserted by the deployment operator (supervisor
	// or script) that verified the restored old bundle's auth/owner/readiness
	// smoke; the CLI records it but never performs the smoke itself.
	OldBundleSmokePassed bool
}

// Orchestrator drives one recovery run: load-or-create the durable
// checkpoint, call every enabled registration once (skipping those already
// settled as completed), reduce the checkpoint atomically after each call,
// and finally compute the fail-closed gate decision.
type Orchestrator struct {
	Registry *Registry
	Store    Store
	Now      func() time.Time
}

// NewOrchestrator wires the registry and checkpoint store.
func NewOrchestrator(registry *Registry, store Store) *Orchestrator {
	return &Orchestrator{Registry: registry, Store: store, Now: time.Now}
}

// Result is the full outcome of one recovery run.
type Result struct {
	Run    *Run
	Gate   GateDecision
	Failed bool // true when any handler errored/panicked and the run is failed
}

// Run executes (or resumes) the recovery operation. It is idempotent: calls
// already settled as completed are not re-invoked, and a missing checkpoint
// table fails closed.
func (o *Orchestrator) Run(ctx context.Context, plan Plan) (Result, error) {
	run, err := o.Store.Load(ctx, plan.OperationID)
	if err != nil {
		var missing *ErrCheckpointSchemaMissing
		if errors.As(err, &missing) {
			return Result{}, missing
		}
		if !errors.Is(err, ErrRunNotFound) {
			return Result{}, fmt.Errorf("load recovery run: %w", err)
		}
		run = &Run{
			ID:                plan.OperationID,
			WindowID:          plan.WindowID,
			OldBundleIdentity: plan.OldBundleIdentity,
			NewBundleIdentity: plan.NewBundleIdentity,
			BackupIdentity:    plan.BackupIdentity,
			SchemaIdentity:    plan.SchemaIdentity,
			BuildIdentity:     plan.BuildIdentity,
			RegistryInventory: o.Registry.Inventory(),
			Checkpoint:        NewCheckpoint(),
			State:             "running",
			Version:           0,
			StartedAt:         o.Now().UTC(),
			UpdatedAt:         o.Now().UTC(),
		}
		if err := o.Store.Create(ctx, run); err != nil {
			return Result{}, fmt.Errorf("create recovery run: %w", err)
		}
	}

	entries := o.Registry.EnabledEntries()
	for _, reg := range entries {
		if call, ok := run.Checkpoint.CallFor(reg.Key); ok && call.State == StateCompleted {
			continue // already settled as a safe terminal state; never re-invoke
		}
		if run.State == "failed" {
			break // a previous failed call already failed the run; do not keep going
		}
		result, callErr := o.invoke(ctx, reg, run.ID, plan.WindowID)
		retry := false
		state := result.State
		reason := result.Reason
		if callErr != nil {
			// A handler error (including panic) fails the run closed; it is
			// never converted into a pass.
			state = StateFailed
			reason = callErr.Error()
		}
		if state == StateRetryable {
			retry = true
		}
		var gate *GateDecision
		next := run.Checkpoint
		next.Settle(reg.Key, state, result, reason, retry)
		run.Checkpoint = next
		if state == StateFailed {
			run.State = "failed"
			decision := NewGateDecision(false, []string{fmt.Sprintf("handler %s failed: %s", reg.Key, reason)})
			gate = &decision
			run.Checkpoint.Gate = gate
		}
		if err := o.persist(ctx, run); err != nil {
			return Result{}, fmt.Errorf("persist recovery checkpoint: %w", err)
		}
		if run.State == "failed" {
			return Result{Run: run, Gate: *run.Checkpoint.Gate, Failed: true}, nil
		}
	}

	decision := o.decide(run, plan)
	run.State = "completed"
	run.Checkpoint.Gate = &decision
	if err := o.persist(ctx, run); err != nil {
		return Result{}, fmt.Errorf("persist final recovery checkpoint: %w", err)
	}
	return Result{Run: run, Gate: decision}, nil
}

func (o *Orchestrator) invoke(ctx context.Context, reg Registration, operationID, windowID string) (result ReconcileResult, err error) {
	req := ReconcileRequest{OperationID: operationID, WindowID: windowID, OwnerEffect: reg.Effect.Owner + "/" + reg.Effect.Effect}
	defer func() {
		if recovered := recover(); recovered != nil {
			result = ReconcileResult{State: StateFailed, Effect: EffectUnknown, Reason: fmt.Sprintf("handler panic: %v", recovered)}
			err = fmt.Errorf("handler %s panicked: %v", reg.Key, recovered)
		}
	}()
	return reg.Handler.ReconcileUnknownEffect(ctx, req)
}

func (o *Orchestrator) persist(ctx context.Context, run *Run) error {
	run.UpdatedAt = o.Now().UTC()
	version, err := o.Store.Update(ctx, run.ID, run.Version, func(current *Run) {
		current.Checkpoint = run.Checkpoint
		current.State = run.State
		current.UpdatedAt = run.UpdatedAt
	})
	if err != nil {
		return err
	}
	run.Version = version
	return nil
}

// decide computes the fail-closed gate. The gate passes only when every
// required/enabled registration was called with a safe terminal state, no
// unresolved/conflict/failure remains, the checkpoint is complete, and the
// operator asserted the old-bundle smoke.
func (o *Orchestrator) decide(run *Run, plan Plan) GateDecision {
	var failures []string
	entries := o.Registry.EnabledEntries()
	if len(entries) == 0 {
		failures = append(failures, "no enabled reconciler registrations")
	}
	for _, reg := range entries {
		call, ok := run.Checkpoint.CallFor(reg.Key)
		if !ok {
			failures = append(failures, fmt.Sprintf("registration %s was never called", reg.Key))
			continue
		}
		switch call.State {
		case StateCompleted:
		case StateRetryable:
			failures = append(failures, fmt.Sprintf("registration %s still retryable: %s", reg.Key, call.Reason))
		case StateBlocked:
			failures = append(failures, fmt.Sprintf("registration %s blocked: %s", reg.Key, call.Reason))
		case StateConflict:
			failures = append(failures, fmt.Sprintf("registration %s conflict: %s", reg.Key, call.Reason))
		case StateFailed:
			failures = append(failures, fmt.Sprintf("registration %s failed: %s", reg.Key, call.Reason))
		default:
			failures = append(failures, fmt.Sprintf("registration %s has unknown state %q", reg.Key, call.State))
		}
	}
	if !plan.OldBundleSmokePassed {
		failures = append(failures, "old bundle smoke was not asserted")
	}
	return NewGateDecision(len(failures) == 0, failures)
}
