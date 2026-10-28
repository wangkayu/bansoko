// Package worker provides the typed WorkItem execution loop.
// It coordinates claims and handler outcomes without owning product policy.
package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

// HandlerPanicError marks a handler panic wrapped by the runtime. Owner-specific
// recovery fallback handlers may treat this as an ambiguous external effect;
// ordinary handler errors must not inherit that recovery classification.
type HandlerPanicError struct {
	Err error
}

func (e HandlerPanicError) Error() string {
	if e.Err == nil {
		return "work item handler panicked"
	}
	return e.Err.Error()
}

func (e HandlerPanicError) Unwrap() error    { return e.Err }
func (HandlerPanicError) HandlerPanic() bool { return true }

var (
	ErrWorkerInvalid                 = errors.New("invalid work item worker")
	ErrNoHandlers                    = errors.New("work item worker has no registered handlers")
	ErrUnsupportedKind               = errors.New("unsupported work item kind")
	ErrDuplicateHandler              = errors.New("work item handler is already registered")
	ErrHandlerMissing                = errors.New("no handler is registered for work item kind")
	ErrInvalidOutcome                = errors.New("invalid work item handler outcome")
	ErrRetryUntilResolvedUnsupported = errors.New("work item store does not support retry until resolved")
)

const (
	OutcomeFinish    OutcomeKind = "finish"
	OutcomeRetry     OutcomeKind = "retry"
	OutcomeCommitted OutcomeKind = "committed"
	// OutcomeAwaitRecovery means the external effect of this claim is unknown
	// and only the ticketed recovery lane may settle it: the worker applies no
	// transition, the claim stays leased, and after lease expiry the recovery
	// claim (bounded by the owner's recovery budget) resolves it. It is a
	// typed, visible result, not an unknown outcome.
	OutcomeAwaitRecovery OutcomeKind = "await_recovery"
)

// AwaitRecovery returns the typed "effect unknown, recovery owns it" outcome.
func AwaitRecovery(reasonCode string) Outcome {
	return Outcome{Kind: OutcomeAwaitRecovery, ReasonCode: reasonCode, Disposition: workitems.DispositionAmbiguous}
}

// awaitsRecovery reports the explicit await-recovery decision. An outcome
// without a kind is invalid, whatever its disposition.
func (o Outcome) awaitsRecovery() bool {
	return o.Kind == OutcomeAwaitRecovery
}

// OutcomeKind identifies the only two transitions a handler may request.
// Periodic successor creation is intentionally not an outcome.
type OutcomeKind string

// Outcome is the typed decision returned by a handler. Owner writes are passed
// to the WorkItem store so they commit atomically with the current item.
type Outcome struct {
	Kind               OutcomeKind
	CommittedClaim     *CommittedClaim
	TerminalState      string
	OwnerResult        workitems.OwnerResult
	WriteOwner         workitems.OwnerWriter
	ReasonCode         string
	Reason             string
	RetryAfter         time.Duration
	RetryUntilResolved bool
	AfterCommit        func(context.Context)
	// PostTransition, when set, runs inside the WorkItem fencing transaction
	// AFTER the WorkItem terminal transition and BEFORE commit. Stores that
	// cannot run it must fail closed instead of silently dropping it. It lets
	// one completion commit a durable follow-up row (for example a placement
	// obligation and its typed continuation WorkItem) that would otherwise
	// violate the one-live-item invariant while the current item is leased.
	PostTransition func(context.Context, txscope.Tx) error
	// Disposition is the typed external-effect result for one-shot claims.
	// It decides settlement of the marker-owned admission lease: confirmed /
	// no_external_effect consume the lease exactly once; ambiguous keeps it so
	// reclamation stays blocked until lookup or recovery settles. An unset
	// disposition on a one-shot outcome is treated as ambiguous (fail closed).
	Disposition workitems.EffectDisposition
}

// CommittedClaim identifies the claim whose owner transaction already committed.
// Its handler must settle all durable work, including one-shot admission, before returning.
type CommittedClaim struct {
	WorkItemID string
	Owner      workitems.OwnerRef
	Kind       string
	Token      int64
}

func Committed(claim workitems.Claim, result workitems.OwnerResult) Outcome {
	return Outcome{Kind: OutcomeCommitted, OwnerResult: result, CommittedClaim: &CommittedClaim{
		WorkItemID: claim.ID, Owner: claim.Owner, Kind: claim.Kind, Token: claim.FencingToken,
	}}
}

func validateCommittedOutcome(claim workitems.Claim, outcome Outcome) error {
	receipt := outcome.CommittedClaim
	if receipt == nil || receipt.WorkItemID != claim.ID || receipt.Owner != claim.Owner || receipt.Kind != claim.Kind || receipt.Token != claim.FencingToken || outcome.WriteOwner != nil || outcome.PostTransition != nil {
		return fmt.Errorf("%w: committed receipt does not match claim or carries pending writes", ErrInvalidOutcome)
	}
	return nil
}

// WithAfterCommit attaches best-effort work that may run only after the owner
// and current WorkItem business transaction has committed successfully.
func WithAfterCommit(outcome Outcome, afterCommit func(context.Context)) Outcome {
	outcome.AfterCommit = afterCommit
	return outcome
}

// Finish returns a terminal outcome. TerminalState belongs to the WorkItem;
// OwnerResult.State remains owned by the concrete product owner.
func Finish(terminalState string, result workitems.OwnerResult, writeOwner workitems.OwnerWriter) Outcome {
	return Outcome{
		Kind:          OutcomeFinish,
		TerminalState: terminalState,
		OwnerResult:   result,
		WriteOwner:    writeOwner,
	}
}

// Retry returns a retryable outcome. The store decides whether the current
// attempt budget makes this retry terminal; the handler supplies owner reason
// data through the store's owner writer.
func Retry(reasonCode, reason string, delay time.Duration, writeOwner workitems.OwnerWriter) Outcome {
	return Outcome{
		Kind:       OutcomeRetry,
		ReasonCode: reasonCode,
		Reason:     reason,
		RetryAfter: delay,
		WriteOwner: writeOwner,
	}
}

// RetryUntilResolved marks a retry outcome that must remain non-terminal even
// when the current claim has reached max_attempts. The worker uses the store's
// optional retry-without-budget port for this outcome.
func RetryUntilResolved(reasonCode, reason string, delay time.Duration, writeOwner workitems.OwnerWriter) Outcome {
	return Outcome{
		Kind:               OutcomeRetry,
		ReasonCode:         reasonCode,
		Reason:             reason,
		RetryAfter:         delay,
		WriteOwner:         writeOwner,
		RetryUntilResolved: true,
	}
}

// Handler is implemented by an owner-specific application handler. It may read
// the immutable claim snapshot and returns an explicit terminal or retry
// outcome; the generic worker does not infer product policy from errors.
type Handler interface {
	Handle(context.Context, workitems.Claim) (Outcome, error)
}

// RecoveryFallbackHandler opts one owner handler into budgetless recovery when
// the handler itself panics or returns an error after an ambiguous effect.
// Ordinary handlers keep the normal attempt budget.
type RecoveryFallbackHandler interface {
	RecoveryFallback(context.Context, workitems.Claim, error) (Outcome, bool)
}

// HandlerFunc adapts a function to Handler for unit tests and small adapters.
type HandlerFunc func(context.Context, workitems.Claim) (Outcome, error)

func (f HandlerFunc) Handle(ctx context.Context, claim workitems.Claim) (Outcome, error) {
	if f == nil {
		return Outcome{}, fmt.Errorf("%w: handler is nil", ErrWorkerInvalid)
	}
	return f(ctx, claim)
}

// WorkItemStore is the narrow persistence port required by Worker. Store
// implements this interface; keeping the port small makes dispatch testable
// without a database.
type WorkItemStore interface {
	ClaimDue(context.Context, string, []string, time.Duration) (workitems.Claim, error)
	Finish(context.Context, workitems.Claim, string, workitems.OwnerWriter) error
	Retry(context.Context, workitems.Claim, string, string, time.Duration, workitems.OwnerWriter) (workitems.RetryOutcome, error)
}

var _ WorkItemStore = (*workitems.Store)(nil)

// typedResultFinisher is implemented by the Store. The optional extension
// preserves the existing Finish port while allowing terminal owner reason data
// to cross the worker/store boundary atomically.
type typedResultFinisher interface {
	FinishWithResult(context.Context, workitems.Claim, string, workitems.OwnerResult, workitems.OwnerWriter) error
}

// postTransitionFinisher is the optional store extension for outcomes that
// carry a PostTransition hook. Implementations must run the hook inside the
// fencing transaction after the WorkItem terminal transition and before
// commit; a store that lacks the extension must fail closed on such outcomes.
type postTransitionFinisher interface {
	FinishWithResultPostTransition(context.Context, workitems.Claim, string, workitems.OwnerResult, workitems.OwnerWriter, func(context.Context, txscope.Tx) error) error
}

// retryUntilResolvedStore is an optional WorkItemStore extension. Keeping it
// out of WorkItemStore preserves compatibility with existing fakes and stores
// that only implement the ordinary attempt-budgeted retry path.
type retryUntilResolvedStore interface {
	RetryWithoutAttemptBudget(context.Context, workitems.Claim, string, string, time.Duration, workitems.OwnerWriter) (workitems.RetryOutcome, error)
}

// Worker claims at most one item per RunOnce call and routes it to the handler
// registered for its exact typed kind.
type Worker struct {
	store    WorkItemStore
	workerID string
	lease    time.Duration
	handlers map[string]Handler
}

// NewWorker constructs a worker. A non-positive lease uses the runtime default.
func NewWorker(store WorkItemStore, workerID string, lease time.Duration) *Worker {
	if lease <= 0 {
		lease = 30 * time.Second
	}
	return &Worker{
		store:    store,
		workerID: workerID,
		lease:    lease,
		handlers: make(map[string]Handler),
	}
}

// AllowedKinds returns the complete kind allowlist in stable order.
func AllowedKinds() []string {
	return append([]string(nil), allowedKinds[:]...)
}

// Register adds one handler to the explicit kind-to-handler registry.
func (w *Worker) Register(kind string, handler Handler) error {
	if w == nil {
		return ErrWorkerInvalid
	}
	if !isAllowedKind(kind) {
		return fmt.Errorf("%w: %q", ErrUnsupportedKind, kind)
	}
	if handler == nil {
		return fmt.Errorf("%w: handler for %q is nil", ErrWorkerInvalid, kind)
	}
	if _, exists := w.handlers[kind]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateHandler, kind)
	}
	w.handlers[kind] = handler
	return nil
}

// RunOnce claims and executes at most one WorkItem. An idle store is not an
// error. Cancellation before or after handler execution prevents any terminal
// or retry write and leaves lease recovery to the normal claim/reclaim path.
func (w *Worker) RunOnce(ctx context.Context) (handled bool, err error) {
	if w == nil || w.store == nil || w.workerID == "" {
		return false, ErrWorkerInvalid
	}
	if ctx == nil {
		return false, fmt.Errorf("%w: context is nil", ErrWorkerInvalid)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if len(w.handlers) == 0 {
		return false, ErrNoHandlers
	}

	claim, err := w.store.ClaimDue(ctx, w.workerID, w.claimKinds(), w.lease)
	if errors.Is(err, workitems.ErrNoDueWork) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}

	if claim.CancelRequested {
		if blocker, ok := w.store.(recoveryBlocker); ok {
			return true, blocker.BlockRecovery(ctx, claim, "cancel_requested")
		}
		return true, errors.New("cancelled work item cannot execute")
	}

	if !isAllowedKind(claim.Kind) {
		return true, fmt.Errorf("%w: claimed kind %q is outside the allowlist", ErrUnsupportedKind, claim.Kind)
	}
	handler, ok := w.handlers[claim.Kind]
	if !ok {
		return true, fmt.Errorf("%w: %q", ErrHandlerMissing, claim.Kind)
	}
	// One-shot claims pass the same central admission gate as the production
	// Runtime: one admission lease per (work item, epoch) is committed before
	// any handler effect, and the outcome settles marker-first with it. Without
	// the gate a typed one-shot finish finds no lease to consume and refuses.
	var lease *workitems.AdmissionLease
	if claim.OneShot != nil {
		gate, ok := w.store.(OneShotAdmissionStore)
		if !ok {
			return true, errors.New("one-shot claim cannot be admitted: store lacks the admission gate")
		}
		admitted, gateErr := gate.AdmitOneShotEffect(ctx, claim)
		if errors.Is(gateErr, workitems.ErrOneShotReclaiming) {
			// Non-mutating abort, as in the Runtime: the item stays leased
			// for normal lease expiry/reclaim.
			return true, nil
		}
		if gateErr != nil {
			return true, gateErr
		}
		lease = &admitted
	}
	outcome, handlerErr := handler.Handle(ctx, claim)
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if handlerErr != nil {
		return true, handlerErr
	}

	if outcome.awaitsRecovery() {
		// The claim stays leased; the recovery lane settles it after expiry.
		return true, nil
	}
	var transitionErr error
	switch outcome.Kind {
	case OutcomeCommitted:
		transitionErr = validateCommittedOutcome(claim, outcome)
	case OutcomeFinish:
		if outcome.TerminalState != workitems.StateSucceeded && outcome.TerminalState != workitems.StateFailed {
			return true, fmt.Errorf("%w: terminal state must be succeeded or failed", ErrInvalidOutcome)
		}
		if lease != nil {
			if outcome.PostTransition != nil {
				postStore, ok := w.store.(oneShotPostTransitionFinisher)
				if !ok {
					return true, fmt.Errorf("%w: work item store cannot run the one-shot post-transition hook", ErrInvalidOutcome)
				}
				transitionErr = postStore.FinishOneShotWithResultPostTransition(ctx, claim, *lease, effectiveDisposition(outcome), outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner, outcome.PostTransition)
			} else if transitionStore, ok := w.store.(OneShotTransitionStore); ok {
				transitionErr = transitionStore.FinishOneShotWithResult(ctx, claim, *lease, effectiveDisposition(outcome), outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner)
			} else {
				return true, errors.New("one-shot finish is unsupported: store lacks marker-first transitions")
			}
		} else if outcome.PostTransition != nil {
			postFinisher, ok := w.store.(postTransitionFinisher)
			if !ok {
				return true, fmt.Errorf("%w: work item store cannot run the outcome post-transition hook", ErrInvalidOutcome)
			}
			transitionErr = postFinisher.FinishWithResultPostTransition(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner, outcome.PostTransition)
		} else if finisher, ok := w.store.(typedResultFinisher); ok {
			transitionErr = finisher.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner)
		} else {
			transitionErr = w.store.Finish(ctx, claim, outcome.TerminalState, outcome.WriteOwner)
		}
	case OutcomeRetry:
		retryStore, ok := w.store.(retryUntilResolvedStore)
		if lease != nil {
			transitionStore, ok := w.store.(OneShotTransitionStore)
			if !ok {
				return true, errors.New("one-shot retry is unsupported: store lacks marker-first transitions")
			}
			_, transitionErr = transitionStore.RetryOneShot(ctx, claim, *lease, effectiveDisposition(outcome), outcome.ReasonCode, outcome.Reason, outcome.RetryAfter, outcome.WriteOwner)
		} else if outcome.RetryUntilResolved {
			if !ok {
				return true, ErrRetryUntilResolvedUnsupported
			}
			_, transitionErr = retryStore.RetryWithoutAttemptBudget(ctx, claim, outcome.ReasonCode, outcome.Reason, outcome.RetryAfter, outcome.WriteOwner)
		} else {
			_, transitionErr = w.store.Retry(ctx, claim, outcome.ReasonCode, outcome.Reason, outcome.RetryAfter, outcome.WriteOwner)
		}
	default:
		return true, fmt.Errorf("%w: unknown outcome kind %q", ErrInvalidOutcome, outcome.Kind)
	}
	if errors.Is(transitionErr, workitems.ErrStaleClaim) && claim.OneShot != nil {
		// Marker-only settlement may have committed; the fenced half was
		// lost to a newer claim, exactly as the Runtime tolerates.
		return true, nil
	}
	if transitionErr != nil {
		return true, transitionErr
	}
	if outcome.AfterCommit != nil {
		outcome.AfterCommit(ctx)
	}
	return true, nil
}

func (w *Worker) claimKinds() []string {
	kinds := make([]string, 0, len(allowedKinds))
	for _, kind := range allowedKinds {
		if _, ok := w.handlers[kind]; ok {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

var allowedKinds = [...]string{
	workitems.KindRSSRefresh,
	workitems.KindReleaseEvaluate,
	workitems.KindQBSubmit,
	workitems.KindQBSync,
	workitems.KindArtifactInspect,
	workitems.KindFileExecute,
	workitems.KindPluginDeliver,
	// P3 FileMaintenance kinds: registered in the runtime policy registry
	// (runtime/worker/policy.go) and composed with real handlers; they must
	// be claimable or the worker fails closed on any scheduled scan/cleanup.
	workitems.KindReconciliationScan,
	workitems.KindAlignmentExecute,
	workitems.KindReplacementEvaluate,
	workitems.KindSubscriptionCompetition,
	workitems.KindReplacementDelete,
	// P3.8 reconciliation closure kinds: calibration and the two generation
	// consumers (DB-bound, light lane). They must be claimable or the worker
	// fails closed on any scheduled calibrate/consume item, which breaks the
	// deployment acceptance (exit maintenance → background runtime start).
	workitems.KindReconciliationCalibrate,
	workitems.KindReconciliationConsumeAlignment,
	workitems.KindReconciliationConsumeReplacement,
	// EXEC-001 post-processing plan stage: one frozen plan per verified
	// artifact executes naming/target planning independently of the artifact
	// success transaction.
	workitems.KindPostProcessPlan,
	// B13 P1 user file operations from the file manager.
	workitems.KindFileManagerExecute,
	// MF3 download completion action on the qBittorrent task.
	workitems.KindCompletionExecute,
}

func isAllowedKind(kind string) bool {
	for _, candidate := range allowedKinds {
		if candidate == kind {
			return true
		}
	}
	return false
}
