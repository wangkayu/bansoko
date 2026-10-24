package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
)

// LifecycleState is the runtime lifecycle state exposed for readiness and
// diagnostics (P0.6 consumes this; no Task Center product surface).
type LifecycleState string

const (
	StateServing  LifecycleState = "serving"
	StateDraining LifecycleState = "draining"
	StateStopped  LifecycleState = "stopped"
)

// LifecycleStatus is the structured runtime snapshot.
type LifecycleStatus struct {
	State       LifecycleState       `json:"state"`
	Lanes       map[Lane]LaneStatus  `json:"lanes"`
	Active      int                  `json:"active_handlers"`
	LastSuccess map[string]time.Time `json:"last_success,omitempty"`
}

// LaneStatus reports per-lane lifecycle inputs.
type LaneStatus struct {
	Concurrency int        `json:"concurrency"`
	StartedAt   time.Time  `json:"started_at"`
	LastSuccess *time.Time `json:"last_success,omitempty"`
}

// RuntimeStore is the persistence port the Runtime needs (store + renew +
// cancel). The Store implements it.
type RuntimeStore interface {
	WorkItemStore
	RenewLease(ctx context.Context, claim workitems.Claim, extendTo time.Time) error
	RequestCancel(ctx context.Context, claim workitems.Claim, reason string) error
	CancelRequests(ctx context.Context, kinds []string, reason string) (int64, error)
}

var _ RuntimeStore = (*workitems.Store)(nil)

// OneShotAdmissionStore is the optional RuntimeStore extension performing the
// central pre-effect admission gate for one-shot claims. The Store
// implements it; fakes without it simply never see one-shot claims.
type OneShotAdmissionStore interface {
	AdmitOneShotEffect(ctx context.Context, claim workitems.Claim) (workitems.AdmissionLease, error)
}

// ManagedAnimeAdmissionStore fences every persistent-owner handler before it
// runs. Retirement serializes against this gate; the store records admission
// only for kinds that can perform an external effect.
type ManagedAnimeAdmissionStore interface {
	AdmitManagedAnimeEffect(ctx context.Context, claim workitems.Claim) error
}

// OneShotTransitionStore is the optional RuntimeStore extension routing
// one-shot finish/retry outcomes through the marker-first settlement protocol.
type OneShotTransitionStore interface {
	FinishOneShotWithResult(ctx context.Context, claim workitems.Claim, lease workitems.AdmissionLease, disposition workitems.EffectDisposition, terminalState string, ownerResult workitems.OwnerResult, writeOwner workitems.OwnerWriter) error
	RetryOneShot(ctx context.Context, claim workitems.Claim, lease workitems.AdmissionLease, disposition workitems.EffectDisposition, reasonCode, reason string, delay time.Duration, writeOwner workitems.OwnerWriter) (workitems.RetryOutcome, error)
}

// oneShotPostTransitionFinisher is the marker-first variant of the same hook.
// The one-shot finish path owns its own transaction shape (marker settlement
// before the WorkItem lock), so it cannot reuse the ordinary finisher.
type oneShotPostTransitionFinisher interface {
	FinishOneShotWithResultPostTransition(context.Context, workitems.Claim, workitems.AdmissionLease, workitems.EffectDisposition, string, workitems.OwnerResult, workitems.OwnerWriter, func(context.Context, txscope.Tx) error) error
}

var (
	_ OneShotAdmissionStore         = (*workitems.Store)(nil)
	_ ManagedAnimeAdmissionStore    = (*workitems.Store)(nil)
	_ OneShotTransitionStore        = (*workitems.Store)(nil)
	_ postTransitionFinisher        = (*workitems.Store)(nil)
	_ oneShotPostTransitionFinisher = (*workitems.Store)(nil)
)

type activeGeneration struct {
	claim  workitems.Claim
	cancel context.CancelFunc
}

// Runtime is the lane-based WorkItem execution runtime: per-lane claim loops,
// per-claim fenced heartbeat, handler deadline cancellation, active-handler
// tracking and graceful drain. It owns no product policy.
type Runtime struct {
	store    RuntimeStore
	registry *Registry
	handlers map[string]Handler
	logger   observabilityport.RuntimeLogger
	workerID string

	ctx    context.Context
	cancel context.CancelFunc // cancels handler contexts only on drain timeout

	claimCtx    context.Context
	claimCancel context.CancelFunc // stops claim loops at drain start

	state atomic.Value // LifecycleState

	mu          sync.Mutex
	active      map[string]activeGeneration // claim ID + fencing token
	lastSuccess map[string]time.Time
	lanes       map[Lane]*laneWorker
	startedAt   map[Lane]time.Time
	wg          sync.WaitGroup // active handlers
	loops       sync.WaitGroup // lane claim loops
	drainOnce   sync.Once
	drainDone   chan struct{} // loops exited, then every registered handler returned
}

// NewRuntime wires the registry, handlers and lifecycle. The registry must be
// complete (every policy kind has a handler and vice versa); incomplete wiring
// fails closed at construction.
func NewRuntime(store RuntimeStore, registry *Registry, handlers map[string]Handler, workerID string, logger observabilityport.RuntimeLogger) (*Runtime, error) {
	if store == nil || registry == nil {
		return nil, errors.New("worker runtime requires a store and registry")
	}
	if err := registry.Complete(handlers); err != nil {
		return nil, err
	}
	if workerID == "" {
		return nil, errors.New("worker runtime requires a worker id")
	}
	if logger == nil {
		logger = observabilityport.NopRuntimeLogger{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	claimCtx, claimCancel := context.WithCancel(context.Background())
	r := &Runtime{
		store:       store,
		registry:    registry,
		handlers:    handlers,
		logger:      logger,
		workerID:    workerID,
		ctx:         ctx,
		cancel:      cancel,
		claimCtx:    claimCtx,
		claimCancel: claimCancel,
		active:      make(map[string]activeGeneration),
		lastSuccess: make(map[string]time.Time),
		lanes:       make(map[Lane]*laneWorker),
		startedAt:   make(map[Lane]time.Time),
		drainDone:   make(chan struct{}),
	}
	r.state.Store(StateServing)
	return r, nil
}

// Start launches both lane loops. It is idempotent per runtime instance.
func (r *Runtime) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.Load().(LifecycleState) != StateServing {
		return
	}
	for _, lane := range []Lane{LaneLight, LaneHeavy} {
		if _, ok := r.lanes[lane]; ok {
			continue
		}
		concurrency := LaneLightConcurrency
		if lane == LaneHeavy {
			concurrency = LaneHeavyConcurrency
		}
		if lane == LaneHeavy {
			// File/subtitle dependency and cross-kind target exclusion have not
			// passed concurrent real-effect acceptance. Keep their shared heavy
			// lane serial until those owner-level gates are proven; the bounded
			// dispatcher can use two slots for lanes without file effects.
			for _, kind := range r.registry.KindsForLane(lane) {
				if kind == workitems.KindFileExecute || kind == workitems.KindAlignmentExecute || kind == workitems.KindReplacementEvaluate || kind == workitems.KindCleanupQuarantine || kind == workitems.KindCleanupPurge || kind == workitems.KindOffsetMigrationExecute || kind == workitems.KindFileManagerExecute {
					concurrency = 1
					break
				}
			}
		}
		lw := &laneWorker{runtime: r, lane: lane, concurrency: concurrency, slots: make(chan struct{}, concurrency)}
		r.lanes[lane] = lw
		r.startedAt[lane] = time.Now().UTC()
		r.loops.Add(1)
		go func() {
			defer r.loops.Done()
			lw.loop(r.claimCtx)
		}()
	}
}

// Drain uses one budget for dispatchers and handlers. Cancellation is not proof
// of quiescence: timeout returns an error and leaves the runtime draining. A
// later Drain can observe actual completion without starting another waiter.
func (r *Runtime) Drain(timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	r.drainOnce.Do(func() {
		r.mu.Lock()
		r.state.Store(StateDraining)
		r.claimCancel()
		r.mu.Unlock()
		go func() {
			// All dispatcher registrations precede loop exit. Start and handler
			// registration use mu and reject draining, so Add cannot race Wait.
			r.loops.Wait()
			r.wg.Wait()
			close(r.drainDone)
		}()
	})
	select {
	case <-r.drainDone:
		r.cancel()
		r.state.Store(StateStopped)
		return nil
	case <-timer.C:
		// Local shutdown is not a durable business cancel request. In
		// particular, do not leave cancel_requested poisoning future reclaim.
		r.cancel()
		return errors.New("worker drain deadline exceeded; runtime is not quiescent")
	}
}

// Stop is a drain with a short grace period (used by composition roots).
func (r *Runtime) Stop() {
	_ = r.Drain(15 * time.Second)
}

// Status returns the structured lifecycle snapshot.
func (r *Runtime) Status() LifecycleStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := LifecycleStatus{
		State:       r.state.Load().(LifecycleState),
		Lanes:       make(map[Lane]LaneStatus, len(r.lanes)),
		Active:      len(r.active),
		LastSuccess: make(map[string]time.Time, len(r.lastSuccess)),
	}
	for lane, lw := range r.lanes {
		ls := LaneStatus{Concurrency: lw.concurrency, StartedAt: r.startedAt[lane]}
		if t, ok := r.lastSuccess[string(lane)]; ok {
			ls.LastSuccess = &t
		}
		status.Lanes[lane] = ls
	}
	for kind, t := range r.lastSuccess {
		status.LastSuccess[kind] = t
	}
	return status
}

func (r *Runtime) cancelActive(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, generation := range r.active {
		generation.cancel()
		_ = r.store.RequestCancel(context.Background(), generation.claim, reason)
	}
}

type runtimeSeverity uint8

const (
	runtimeDebug runtimeSeverity = iota
	runtimeInfo
	runtimeWarn
	runtimeError
)

func (r *Runtime) logEvent(ctx context.Context, severity runtimeSeverity, event observabilityport.RuntimeEvent) {
	if r.logger == nil {
		return
	}
	if event.Component == "" {
		event.Component = "worker"
	}
	if event.Operation == "" {
		event.Operation = "runtime"
	}
	if event.WorkItemID == "" {
		event.WorkItemID = event.OwnerID
	}
	switch severity {
	case runtimeDebug:
		r.logger.Debug(ctx, event)
	case runtimeInfo:
		r.logger.Info(ctx, event)
	case runtimeWarn:
		r.logger.Warn(ctx, event)
	case runtimeError:
		r.logger.Error(ctx, event)
	}
}

func retryableAttempt(claim workitems.Claim) bool {
	return claim.Attempt < claim.MaxAttempts
}

func severityForAttempt(claim workitems.Claim) runtimeSeverity {
	if retryableAttempt(claim) {
		return runtimeWarn
	}
	return runtimeError
}

func workerEvent(claim workitems.Claim, message, operation, reasonCode, errorClass string, retryable bool) observabilityport.RuntimeEvent {
	return observabilityport.RuntimeEvent{
		Message:    message,
		Component:  "worker",
		Operation:  operation,
		Outcome:    "failure",
		ReasonCode: reasonCode,
		OwnerType:  claim.Owner.Type,
		OwnerID:    claim.Owner.ID,
		WorkItemID: claim.ID,
		Attempt:    claim.Attempt,
		Retryable:  &retryable,
		ErrorClass: errorClass,
	}
}

func (r *Runtime) logClaimFailed(ctx context.Context) {
	r.logEvent(ctx, runtimeError, observabilityport.RuntimeEvent{
		Message:    "worker claim failed",
		Component:  "worker",
		Operation:  "claim",
		Outcome:    "failure",
		ReasonCode: "claim_failed",
		Retryable:  boolPtr(true),
		ErrorClass: "claim_error",
	})
}

func boolPtr(value bool) *bool {
	return &value
}

// effectiveDisposition resolves the typed disposition of a one-shot outcome.
// An unset disposition is ambiguous (fail closed): the admission lease stays
// unconsumed and reclamation remains blocked until a later attempt settles
// the external result definitively.
func effectiveDisposition(outcome Outcome) workitems.EffectDisposition {
	switch outcome.Disposition {
	case workitems.DispositionConfirmed, workitems.DispositionNoExternalEffect, workitems.DispositionAmbiguous:
		return outcome.Disposition
	default:
		return workitems.DispositionAmbiguous
	}
}

// laneWorker claims and dispatches one lane's kinds.
type laneWorker struct {
	runtime     *Runtime
	lane        Lane
	concurrency int
	slots       chan struct{}
	cursor      int // round-robin kind cursor for fairness
}

// claimTick is the idle poll interval of a lane: while nothing is claimable a
// lane makes one claim attempt (one kind) per tick.
const claimTick = 250 * time.Millisecond

// loop is the lane's single claim loop; claims stay serialized per lane.
//
// While idle it makes one claim attempt per tick, rotating the kind cursor, so
// an idle lane costs one attempt per tick. As soon as an attempt dispatches a
// claim the lane bursts: it keeps attempting, one kind per attempt in the same
// strict rotation, without waiting for the next tick, and returns to ticking
// once a full rotation over its kinds dispatched nothing. A burst attempt
// first waits for a free slot (no claim is ever taken without a slot), so the
// lane's capacity, not the tick, bounds a backlog's throughput.
//
// Fairness: every attempt advances the cursor by exactly one kind, so between
// two attempts for any kind each other kind of the lane gets exactly one
// attempt; a kind with due work is claimed within len(kinds) attempts while the
// lane bursts, and within len(kinds) ticks while it is idle. No kind can be
// starved by another kind's backlog.
func (lw *laneWorker) loop(ctx context.Context) {
	kinds := lw.runtime.registry.KindsForLane(lw.lane)
	if len(kinds) == 0 {
		return
	}
	ticker := time.NewTicker(claimTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		bursting := false
		for misses := 0; misses < len(kinds); {
			if !lw.acquireSlot(ctx) {
				return
			}
			kind := kinds[lw.cursor%len(kinds)]
			lw.cursor++
			if lw.claimAndExecute(ctx, kind) {
				bursting, misses = true, 0
				continue
			}
			if !bursting {
				break
			}
			misses++
		}
	}
}

// acquireSlot waits for a free dispatch slot; false means the claim loop must
// stop (drain started).
func (lw *laneWorker) acquireSlot(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case lw.slots <- struct{}{}:
	}
	if ctx.Err() != nil {
		<-lw.slots
		return false
	}
	return true
}

// claimAndExecute makes one claim attempt for kind with an already acquired
// slot and reports whether a claim was dispatched to a handler (the handler
// then owns the slot). Every other outcome releases the slot.
func (lw *laneWorker) claimAndExecute(ctx context.Context, kind string) (dispatched bool) {
	defer func() {
		if !dispatched {
			<-lw.slots
		}
		if recover() != nil {
			dispatched = false
			lw.runtime.logClaimFailed(ctx)
		}
	}()
	policy := lw.runtime.registry.MustGet(kind)
	var claim workitems.Claim
	var err error
	recovery := false
	// Alternate observation and business preference to avoid starvation.
	if store, ok := lw.runtime.store.(recoveryClaimer); ok && ((lw.cursor-1)/len(lw.runtime.registry.KindsForLane(lw.lane)))%2 == 0 {
		claim, err = store.ClaimRecovery(ctx, []string{kind}, policy.Lease)
		recovery = err == nil
	} else {
		err = workitems.ErrNoDueWork
	}
	if errors.Is(err, workitems.ErrNoDueWork) {
		claim, err = lw.runtime.store.ClaimDue(ctx, lw.runtime.workerID, []string{kind}, policy.Lease)
		if errors.Is(err, workitems.ErrNoDueWork) {
			if store, ok := lw.runtime.store.(recoveryClaimer); ok {
				claim, err = store.ClaimRecovery(ctx, []string{kind}, policy.Lease)
				recovery = err == nil
			}
		}
	}
	if errors.Is(err, workitems.ErrNoDueWork) {
		return
	}
	if err != nil {
		lw.runtime.logClaimFailed(ctx)
		return
	}
	handlerCtx, cancel, ok := lw.runtime.register(lw.runtime.ctx, claim)
	if !ok {
		// A claim racing drain is abandoned without effects; fencing and lease
		// reclaim remain store-owned. Never start a handler after the stop gate.
		return
	}
	go func() {
		defer func() { <-lw.slots }()
		if recovery {
			defer func() {
				cancel()
				lw.runtime.mu.Lock()
				delete(lw.runtime.active, generationKey(claim))
				lw.runtime.mu.Unlock()
				lw.runtime.wg.Done()
			}()
			lw.runtime.observeRecovery(handlerCtx, claim, policy)
		} else {
			lw.runtime.executeRegistered(handlerCtx, cancel, claim, policy)
		}
	}()
	return true
}

func generationKey(claim workitems.Claim) string {
	return fmt.Sprintf("%s:%d", claim.ID, claim.FencingToken)
}

// register linearizes handler admission against Drain and registers before launch.
type recoveryBlocker interface {
	BlockRecovery(context.Context, workitems.Claim, string) error
}

func (r *Runtime) blockRecovery(claim workitems.Claim, reason string) {
	store, ok := r.store.(recoveryBlocker)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.BlockRecovery(ctx, claim, reason); err != nil && !errors.Is(err, workitems.ErrStaleClaim) {
		r.logEvent(ctx, runtimeError, workerEvent(claim, "worker recovery quarantine failed", "recovery", "recovery_block_failed", "recovery_block", false))
	}
}

func (r *Runtime) register(parent context.Context, claim workitems.Claim) (context.Context, context.CancelFunc, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.Load().(LifecycleState) != StateServing {
		return nil, nil, false
	}
	handlerCtx, cancel := context.WithCancel(parent)
	r.active[generationKey(claim)] = activeGeneration{claim: claim, cancel: cancel}
	r.wg.Add(1)
	return handlerCtx, cancel, true
}

// execute is the synchronous entry used by direct unit/recovery callers.
func (r *Runtime) execute(parent context.Context, claim workitems.Claim, policy KindPolicy) {
	handlerCtx, cancel, ok := r.register(parent, claim)
	if !ok {
		return
	}
	r.executeRegistered(handlerCtx, cancel, claim, policy)
}

func (r *Runtime) executeRegistered(handlerCtx context.Context, cancel context.CancelFunc, claim workitems.Claim, policy KindPolicy) {
	key := generationKey(claim)
	deadlineTimer := time.NewTimer(policy.Deadline)
	// Fenced heartbeat: renew the lease; any failure tears down the handler.
	heartbeatStop := make(chan struct{})
	var auxiliary sync.WaitGroup
	defer func() {
		// Cleanup is deliberately the final execute boundary: handler and
		// recovery-fallback failures must not strand the heartbeat, active claim,
		// or runtime WaitGroup.
		cancel()
		close(heartbeatStop)
		deadlineTimer.Stop()
		// A cancelled heartbeat may still be in the store call. Do not claim
		// quiescence until both owned helper goroutines have actually returned.
		auxiliary.Wait()
		r.mu.Lock()
		delete(r.active, key)
		r.mu.Unlock()
		r.wg.Done()
		if recover() != nil {
			// Admission/store panics cannot prove an effect disposition. Cleanup
			// admission bookkeeping locally, but leave persistent leases for reclaim.
			r.logEvent(handlerCtx, runtimeError, workerEvent(claim, "worker execution boundary panicked; lease left for reclaim", "handler", "execution_panic", "execution_panic", false))
		}
	}()

	auxiliary.Add(1)
	go func() {
		defer auxiliary.Done()
		defer func() {
			if recover() != nil {
				cancel()
			}
		}()
		ticker := time.NewTicker(policy.Heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatStop:
				return
			case <-handlerCtx.Done():
				return
			case <-ticker.C:
				extend := time.Now().UTC().Add(policy.Lease)
				if err := r.store.RenewLease(handlerCtx, claim, extend); err != nil {
					r.logEvent(handlerCtx, severityForAttempt(claim), workerEvent(claim, "worker lease heartbeat failed", "lease_heartbeat", "lease_heartbeat_failed", "lease_heartbeat_error", retryableAttempt(claim)))
					cancel()
					if claim.Attempt >= claim.MaxAttempts {
						r.blockRecovery(claim, "attempt_exhausted")
					}
					return
				}
			}
		}
	}()

	// Deadline: cancel the handler when its budget is exhausted.
	auxiliary.Add(1)
	go func() {
		defer auxiliary.Done()
		defer func() {
			if recover() != nil {
				cancel()
			}
		}()
		select {
		case <-deadlineTimer.C:
			r.logEvent(handlerCtx, severityForAttempt(claim), workerEvent(claim, "worker handler deadline exceeded", "deadline", "deadline_exceeded", "handler_deadline", retryableAttempt(claim)))
			cancel()
			if claim.Attempt >= claim.MaxAttempts {
				r.blockRecovery(claim, "attempt_exhausted")
			}
		case <-handlerCtx.Done():
		}
	}()

	if claim.CancelRequested {
		r.blockRecovery(claim, "cancel_requested")
		return
	}
	if handlerCtx.Err() != nil {
		return
	}
	if claim.OneShot != nil {
		// Central one-shot admission gate. The claim transaction already
		// committed and released its WorkItem lock (the single-statement claim
		// is atomic, so statement commit equals claim-tx commit). The gate
		// locks the marker, requires state='running' and the claimed epoch,
		// appends exactly one admission lease per (work_item_id, epoch), and
		// commits BEFORE any handler runs — so qb_submit, qb_sync,
		// artifact_inspect, file_execute and every recovery fallback share the
		// same gate and none can bypass it.
		admissionStore, ok := r.store.(OneShotAdmissionStore)
		if !ok {
			r.logEvent(handlerCtx, runtimeError, workerEvent(claim, "one-shot claim cannot be admitted; store lacks the admission gate", "oneshot_gate", "oneshot_gate_unsupported", "oneshot_gate_error", false))
			return
		}
		lease, gateErr := admissionStore.AdmitOneShotEffect(handlerCtx, claim)
		if gateErr != nil {
			if errors.Is(gateErr, workitems.ErrOneShotReclaiming) {
				// ONE_SHOT_RECLAIMING: non-mutating abort. The worker abandons
				// the claim — no Retry (ordinary Retry mutates the WorkItem
				// row), no owner write, no external effect. The item stays
				// leased for normal lease expiry/reclaim.
				r.logEvent(handlerCtx, runtimeWarn, workerEvent(claim, "one-shot acquisition is reclaiming; effect aborted without mutation", "oneshot_gate", "one_shot_reclaiming", "one_shot_reclaiming", false))
				return
			}
			// Any other gate failure is fail-closed: no external effect, no
			// owner/WorkItem mutation; the lease expires and the item becomes
			// reclaimable.
			r.logEvent(handlerCtx, runtimeError, workerEvent(claim, "one-shot admission gate failed; claim abandoned before any effect", "oneshot_gate", "oneshot_gate_failed", "oneshot_gate_error", false))
			return
		}
		r.handleWithRecoveryGated(handlerCtx, claim, lease)
		return
	}
	if claim.ManagedAnimeID != "" {
		admissionStore, ok := r.store.(ManagedAnimeAdmissionStore)
		if !ok {
			r.logEvent(handlerCtx, runtimeError, workerEvent(claim, "managed anime claim cannot be admitted; store lacks the lifecycle gate", "retirement_gate", "retirement_gate_unsupported", "retirement_gate_error", false))
			return
		}
		if gateErr := admissionStore.AdmitManagedAnimeEffect(handlerCtx, claim); gateErr != nil {
			reason := "retirement_gate_failed"
			if errors.Is(gateErr, workitems.ErrOwnerRetired) {
				reason = "managed_anime_retired"
			}
			r.logEvent(handlerCtx, runtimeWarn, workerEvent(claim, "managed anime lifecycle fence rejected the claim before its effect", "retirement_gate", reason, reason, false))
			return
		}
	}
	r.handleWithRecovery(handlerCtx, claim, nil)
}

// handleWithRecovery runs the handler and applies the outcome, converting
// panics into a structured failure that follows the kind retry policy. A
// non-nil lease marks a gated one-shot claim whose outcome must settle the
// marker-owned admission lease through the marker-first transition methods.
func (r *Runtime) handleWithRecovery(ctx context.Context, claim workitems.Claim, lease *workitems.AdmissionLease) {
	handler, ok := r.handlers[claim.Kind]
	if !ok {
		r.logEvent(ctx, runtimeError, workerEvent(claim, "worker handler is missing", "handler", "missing_handler", "handler_missing", false))
		return
	}
	fallback := func(err error, reasonCode string) {
		if ctx.Err() != nil {
			r.logEvent(ctx, runtimeWarn, workerEvent(claim, "worker handler cancelled before recovery fallback", "handler", "cancelled", "handler_cancelled", retryableAttempt(claim)))
			return
		}
		if recovery, ok := handler.(RecoveryFallbackHandler); ok {
			var outcome Outcome
			var handled bool
			var fallbackPanic any
			func() {
				defer func() { fallbackPanic = recover() }()
				outcome, handled = recovery.RecoveryFallback(ctx, claim, err)
			}()
			if fallbackPanic != nil {
				// A fallback panic cannot establish whether the external effect
				// happened. Do not transition the item; lease reclaim is safer.
				r.logEvent(ctx, runtimeError, workerEvent(claim, "worker recovery fallback panicked; lease left for reclaim", "handler", "recovery_fallback_panic", "recovery_fallback_panic", false))
				return
			}
			if handled {
				r.logEvent(ctx, runtimeWarn, workerEvent(claim, "worker handler recovery fallback", "handler", reasonCode, reasonCode, true))
				if ctx.Err() != nil {
					r.logEvent(ctx, runtimeWarn, workerEvent(claim, "worker handler cancelled after recovery fallback", "handler", "cancelled", "handler_cancelled", retryableAttempt(claim)))
					return
				}
				r.applyOutcome(ctx, claim, outcome, lease)
				return
			}
		}
		if claim.Kind == workitems.KindFileExecute && claim.Owner.Type == workitems.OwnerFileOperation {
			r.logEvent(ctx, runtimeError, workerEvent(claim, "file recovery mode could not be confirmed", "handler", "recovery_mode_unknown", "recovery_mode_unknown", false))
			return
		}
		retryable := retryableAttempt(claim)
		r.logEvent(ctx, severityForAttempt(claim), workerEvent(claim, "worker handler recovery fallback used ordinary retry", "handler", reasonCode, reasonCode, retryable))
		r.applyOutcome(ctx, claim, Retry(reasonCode, "handler recovery fallback", time.Second, nil), lease)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			fallback(HandlerPanicError{Err: fmt.Errorf("handler panic: %v", recovered)}, "handler_panic")
		}
	}()

	outcome, err := handler.Handle(ctx, claim)
	if err != nil {
		if ctx.Err() != nil {
			r.logEvent(ctx, runtimeWarn, workerEvent(claim, "worker handler cancelled before recovery fallback", "handler", "cancelled", "handler_cancelled", retryableAttempt(claim)))
			return
		}
		fallback(err, "handler_error")
		return
	}
	if ctx.Err() != nil {
		// Cancelled (lost lease / deadline / drain): do not write terminal or
		// retry state; the item becomes reclaimable through the normal path.
		r.logEvent(ctx, runtimeWarn, workerEvent(claim, "worker handler cancelled before transition", "handler", "cancelled", "handler_cancelled", retryableAttempt(claim)))
		return
	}
	r.applyOutcome(ctx, claim, outcome, lease)
}

// handleWithRecoveryGated dispatches an already-admitted one-shot claim. The
// admission lease flows into every outcome transition so settlement consumes
// it exactly once under the marker-first lock order.
func (r *Runtime) handleWithRecoveryGated(ctx context.Context, claim workitems.Claim, lease workitems.AdmissionLease) {
	r.handleWithRecovery(ctx, claim, &lease)
}

func (r *Runtime) applyOutcome(ctx context.Context, claim workitems.Claim, outcome Outcome, lease *workitems.AdmissionLease) {
	if ctx == nil || ctx.Err() != nil {
		r.logEvent(ctx, runtimeWarn, workerEvent(claim, "worker outcome discarded after cancellation", "outcome", "cancelled", "handler_cancelled", retryableAttempt(claim)))
		return
	}
	if outcome.awaitsRecovery() {
		reason := outcome.ReasonCode
		if reason == "" {
			reason = "effect_unknown"
		}
		r.logEvent(ctx, runtimeWarn, workerEvent(claim, "external effect outcome unknown; the claim is left for ticketed recovery", "outcome", reason, "await_recovery", true))
		return
	}
	switch outcome.Kind {
	case OutcomeCommitted:
		if err := validateCommittedOutcome(claim, outcome); err != nil {
			r.logEvent(ctx, runtimeError, workerEvent(claim, "worker committed receipt is invalid", "outcome", "invalid_committed_receipt", "invalid_outcome", false))
			return
		}
		if outcome.AfterCommit != nil {
			outcome.AfterCommit(ctx)
		}
	case OutcomeFinish:
		if outcome.TerminalState != workitems.StateSucceeded && outcome.TerminalState != workitems.StateFailed {
			r.logEvent(ctx, runtimeError, workerEvent(claim, "worker outcome has an invalid terminal state", "finish", "invalid_terminal", "invalid_terminal_state", false))
			return
		}
		var finishErr error
		if claim.OneShot != nil && lease != nil {
			transitionStore, ok := r.store.(OneShotTransitionStore)
			if !ok {
				r.logEvent(ctx, runtimeError, workerEvent(claim, "one-shot finish is unsupported; store lacks marker-first transitions", "finish", "oneshot_transition_unsupported", "oneshot_transition_error", false))
				return
			}
			if outcome.PostTransition != nil {
				// The hook must commit with the settlement. A store that cannot
				// run it fails closed here: the claim stays leased and becomes
				// reclaimable instead of silently dropping the durable follow-up.
				postStore, ok := r.store.(oneShotPostTransitionFinisher)
				if !ok {
					r.logEvent(ctx, runtimeError, workerEvent(claim, "one-shot finish cannot run the post-transition hook; claim left for reclaim", "finish", "oneshot_post_transition_unsupported", "post_transition_error", false))
					return
				}
				finishErr = postStore.FinishOneShotWithResultPostTransition(ctx, claim, *lease, effectiveDisposition(outcome), outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner, outcome.PostTransition)
			} else {
				finishErr = transitionStore.FinishOneShotWithResult(ctx, claim, *lease, effectiveDisposition(outcome), outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner)
			}
		} else if outcome.PostTransition != nil {
			// The hook commits a durable follow-up row with the terminal
			// transition; dropping it would lose that row for good, so a store
			// without the extension fails closed instead of degrading to the
			// plain finish.
			postFinisher, ok := r.store.(postTransitionFinisher)
			if !ok {
				r.logEvent(ctx, runtimeError, workerEvent(claim, "worker finish cannot run the post-transition hook; claim left for reclaim", "finish", "post_transition_unsupported", "post_transition_error", false))
				return
			}
			finishErr = postFinisher.FinishWithResultPostTransition(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner, outcome.PostTransition)
		} else if finisher, ok := r.store.(typedResultFinisher); ok {
			finishErr = finisher.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner)
		} else {
			finishErr = r.store.Finish(ctx, claim, outcome.TerminalState, outcome.WriteOwner)
		}
		if errors.Is(finishErr, workitems.ErrStaleClaim) && claim.OneShot != nil {
			// Marker-only settlement may have committed; the fenced half was
			// lost to a newer claim. Nothing further to do.
			r.logEvent(ctx, runtimeWarn, workerEvent(claim, "one-shot finish lost the fencing race; disposition settled marker-only", "finish", "stale_claim", "stale_claim", false))
			return
		}
		if finishErr != nil {
			r.logEvent(ctx, runtimeError, workerEvent(claim, "worker finish transition failed", "finish", "finish_failed", "finish_error", false))
			return
		}
		// Best-effort after-commit work (diagnostics, sinks, event
		// invalidation) runs only after the owner + item transaction
		// committed successfully.
		if outcome.TerminalState == workitems.StateSucceeded {
			r.mu.Lock()
			r.lastSuccess[claim.Kind] = time.Now().UTC()
			r.mu.Unlock()
		}
		if outcome.AfterCommit != nil {
			outcome.AfterCommit(ctx)
		}
	case OutcomeRetry:
		policy := r.registry.MustGet(claim.Kind)
		delay := outcome.RetryAfter
		if delay <= 0 {
			delay = policy.Backoff(claim.Attempt + 1)
		}
		var retryOutcome workitems.RetryOutcome
		var err error
		if claim.OneShot != nil && lease != nil {
			transitionStore, ok := r.store.(OneShotTransitionStore)
			if !ok {
				r.logEvent(ctx, runtimeError, workerEvent(claim, "one-shot retry is unsupported; store lacks marker-first transitions", "retry", "oneshot_transition_unsupported", "oneshot_transition_error", false))
				return
			}
			retryOutcome, err = transitionStore.RetryOneShot(ctx, claim, *lease, effectiveDisposition(outcome), outcome.ReasonCode, outcome.Reason, delay, outcome.WriteOwner)
		} else if outcome.RetryUntilResolved {
			retryStore, ok := r.store.(retryUntilResolvedStore)
			if !ok {
				r.logEvent(ctx, runtimeError, workerEvent(claim, "worker recovery retry is unsupported", "retry", "retry_until_resolved_unsupported", "retry_persistence_error", false))
				return
			}
			retryOutcome, err = retryStore.RetryWithoutAttemptBudget(ctx, claim, outcome.ReasonCode, outcome.Reason, delay, outcome.WriteOwner)
		} else {
			retryOutcome, err = r.store.Retry(ctx, claim, outcome.ReasonCode, outcome.Reason, delay, outcome.WriteOwner)
		}
		if errors.Is(err, workitems.ErrStaleClaim) && claim.OneShot != nil {
			r.logEvent(ctx, runtimeWarn, workerEvent(claim, "one-shot retry lost the fencing race; disposition settled marker-only", "retry", "stale_claim", "stale_claim", false))
			return
		}
		if err != nil {
			r.logEvent(ctx, runtimeError, workerEvent(claim, "worker retry persistence failed", "retry", "retry_persist_failed", "retry_persistence_error", true))
			return
		}
		reasonCode := "retry_scheduled"
		message := "worker retry scheduled"
		retryable := true
		severity := runtimeWarn
		if retryOutcome.Terminal {
			reasonCode = "retry_exhausted"
			message = "worker retry budget exhausted"
			retryable = false
			severity = runtimeError
		}
		r.logEvent(ctx, severity, workerEvent(claim, message, "retry", reasonCode, "retry_outcome", retryable))
	default:
		r.logEvent(ctx, runtimeError, workerEvent(claim, "worker produced an unknown outcome", "outcome", "unknown_outcome", "unknown_outcome", false))
	}
}
