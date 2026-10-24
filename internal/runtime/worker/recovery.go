package worker

import (
	"context"
	"errors"
	"time"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

// RecoveryObserver must only observe the frozen effect identity. It must never
// submit a new effect or call the normal Handle path. Ambiguous is retryable
// observation, not permission to erase evidence or fail a product owner.
type RecoveryObserver interface {
	ObserveRecovery(context.Context, workitems.Claim) (Outcome, error)
}

type recoverySettler interface {
	SettleRecovery(context.Context, workitems.Claim, string, workitems.OwnerResult, workitems.EffectDisposition, workitems.OwnerWriter, func(context.Context, txscope.Tx) error) error
}

// The concrete settlement port keeps the existing transaction hook type.
type recoveryClaimer interface {
	ClaimRecovery(context.Context, []string, time.Duration) (workitems.Claim, error)
	DeferRecovery(context.Context, workitems.Claim, string, time.Duration) error
}

func (r *Runtime) observeRecovery(parent context.Context, claim workitems.Claim, policy KindPolicy) {
	ctx, cancel := context.WithTimeout(parent, policy.Deadline)
	defer cancel()
	defer func() {
		if recover() != nil {
			r.deferRecovery(claim, "recovery_observer_panic")
		}
	}()
	observer, ok := r.handlers[claim.Kind].(RecoveryObserver)
	if !ok {
		r.deferRecovery(claim, "recovery_observer_unavailable")
		return
	}
	outcome, err := observer.ObserveRecovery(ctx, claim)
	if err != nil || ctx.Err() != nil {
		r.deferRecovery(claim, "recovery_observation_failed")
		return
	}
	if outcome.Kind == OutcomeCommitted {
		if err := validateCommittedOutcome(claim, outcome); err != nil {
			r.deferRecovery(claim, "invalid_committed_receipt")
			return
		}
		if outcome.AfterCommit != nil {
			outcome.AfterCommit(ctx)
		}
		return
	}
	if outcome.Kind != OutcomeFinish || outcome.WriteOwner == nil || (outcome.Disposition != workitems.DispositionConfirmed && outcome.Disposition != workitems.DispositionNoExternalEffect) {
		r.deferRecovery(claim, "recovery_effect_unknown")
		return
	}
	if outcome.Disposition == workitems.DispositionNoExternalEffect && outcome.TerminalState != workitems.StateFailed {
		r.deferRecovery(claim, "recovery_outcome_invalid")
		return
	}
	store, ok := r.store.(recoverySettler)
	if !ok {
		r.deferRecovery(claim, "recovery_settlement_unavailable")
		return
	}
	if err := store.SettleRecovery(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.Disposition, outcome.WriteOwner, outcome.PostTransition); err != nil {
		if !errors.Is(err, workitems.ErrStaleClaim) {
			r.deferRecovery(claim, "recovery_settlement_failed")
		}
		return
	}
	if outcome.AfterCommit != nil {
		outcome.AfterCommit(ctx)
	}
}

func (r *Runtime) deferRecovery(claim workitems.Claim, reason string) {
	if store, ok := r.store.(recoveryClaimer); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = store.DeferRecovery(ctx, claim, reason, 30*time.Second)
	}
	r.logEvent(context.Background(), runtimeWarn, workerEvent(claim, "worker recovery remains blocked; observation will retry", "recovery", reason, "recovery_observation", true))
}
