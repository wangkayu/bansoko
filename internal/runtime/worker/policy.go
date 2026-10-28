package worker

import (
	"fmt"
	"math"
	"time"

	"bansoko/internal/workitems"
)

// Lane is the execution lane a kind is pinned to. Light lanes hold quick
// bounded work; heavy lanes hold external-effect work (qB submit, artifact
// inspection, file execution) with longer leases and lower concurrency.
type Lane string

const (
	LaneLight Lane = "light"
	LaneHeavy Lane = "heavy"
)

// LaneConcurrency is the per-lane dispatcher capacity ceiling (R3). Runtime
// temporarily gates heavy lanes containing unaccepted file-effect kinds to one
// slot; LifecycleStatus reports that actual capacity, not this ceiling.
const (
	LaneLightConcurrency = 4
	LaneHeavyConcurrency = 2
)

// KindPolicy is the mandatory per-kind runtime policy. A kind without a
// registered policy can never enter a composition root.
type KindPolicy struct {
	Kind      string
	Lane      Lane
	Lease     time.Duration // claim lease; also the initial heartbeat horizon
	Heartbeat time.Duration // renewal interval
	Deadline  time.Duration // handler execution deadline (0 = lease-bound)
	// AttemptTimeout is the business per-attempt base the accept path freezes
	// into the limits snapshot (0 = Deadline). Storage stages scale it with the
	// accepted bytes (ScaleStorageAttempt) and bound their I/O by the DB
	// attempt ticket, so their handler Deadline is only a hang ceiling.
	AttemptTimeout time.Duration
	MaxAttempts    int
	Backoff        func(attempt int) time.Duration
}

// BusinessAttemptTimeout is the per-attempt base frozen into accepted limits.
func (p KindPolicy) BusinessAttemptTimeout() time.Duration {
	if p.AttemptTimeout > 0 {
		return p.AttemptTimeout
	}
	return p.Deadline
}

// Storage-stage budgets (inspect, plan, file_execute) are sized from the bytes
// the stage must move, not a fixed wall clock: a slow NAS/SMB/ARM host that is
// still making progress must not turn into a permanent failure.
const (
	// StorageFloorBytesPerSecond is the slowest sustained throughput a stage
	// budget assumes.
	StorageFloorBytesPerSecond = 10 << 20
	// MaxStorageAttemptTimeout caps one scaled attempt; it stays below the
	// 86400s stage wall so a stalled attempt still leaves room for a retry.
	MaxStorageAttemptTimeout = 12 * time.Hour
	// StorageStageHandlerDeadline is the runtime hang ceiling of a storage
	// stage handler. It exceeds every scaled attempt; the attempt ticket, not
	// this timer, is the business deadline.
	StorageStageHandlerDeadline = MaxStorageAttemptTimeout + 30*time.Minute
)

// ScaleStorageAttempt returns base plus the time to move bytes×passes at the
// storage floor, capped at MaxStorageAttemptTimeout and rounded up to a second.
func ScaleStorageAttempt(base time.Duration, bytes int64, passes int) time.Duration {
	if bytes < 0 {
		bytes = 0
	}
	if passes < 1 {
		passes = 1
	}
	transfer := float64(bytes) * float64(passes) / float64(StorageFloorBytesPerSecond)
	total := base.Seconds() + transfer
	if total >= MaxStorageAttemptTimeout.Seconds() {
		return MaxStorageAttemptTimeout
	}
	return time.Duration(math.Ceil(total)) * time.Second
}

// defaultBackoff returns exponential backoff capped at 60s.
func defaultBackoff(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt-1)) * time.Second
	if d > 60*time.Second {
		return 60 * time.Second
	}
	return d
}

// DefaultPolicies is the canonical 7-kind policy set (R3).
func DefaultPolicies() []KindPolicy {
	return []KindPolicy{
		{Kind: workitems.KindRSSRefresh, Lane: LaneLight, Lease: 60 * time.Second, Heartbeat: 15 * time.Second, Deadline: 120 * time.Second, MaxAttempts: 5, Backoff: defaultBackoff},
		{Kind: workitems.KindReleaseEvaluate, Lane: LaneLight, Lease: 60 * time.Second, Heartbeat: 15 * time.Second, Deadline: 60 * time.Second, MaxAttempts: 5, Backoff: defaultBackoff},
		{Kind: workitems.KindQBSync, Lane: LaneLight, Lease: 60 * time.Second, Heartbeat: 15 * time.Second, Deadline: 60 * time.Second, MaxAttempts: 5, Backoff: defaultBackoff},
		{Kind: workitems.KindPluginDeliver, Lane: LaneLight, Lease: 60 * time.Second, Heartbeat: 15 * time.Second, Deadline: 60 * time.Second, MaxAttempts: 5, Backoff: defaultBackoff},
		{Kind: workitems.KindQBSubmit, Lane: LaneHeavy, Lease: 90 * time.Second, Heartbeat: 15 * time.Second, Deadline: 90 * time.Second, MaxAttempts: 5, Backoff: defaultBackoff},
		{Kind: workitems.KindArtifactInspect, Lane: LaneHeavy, Lease: 120 * time.Second, Heartbeat: 15 * time.Second, Deadline: StorageStageHandlerDeadline, AttemptTimeout: 300 * time.Second, MaxAttempts: 5, Backoff: defaultBackoff},
		{Kind: workitems.KindFileExecute, Lane: LaneHeavy, Lease: 120 * time.Second, Heartbeat: 15 * time.Second, Deadline: StorageStageHandlerDeadline, AttemptTimeout: 300 * time.Second, MaxAttempts: 5, Backoff: defaultBackoff},
		{Kind: workitems.KindPostProcessPlan, Lane: LaneLight, Lease: 60 * time.Second, Heartbeat: 15 * time.Second, Deadline: StorageStageHandlerDeadline, AttemptTimeout: 120 * time.Second, MaxAttempts: 5, Backoff: defaultBackoff},
		{Kind: workitems.KindReconciliationScan, Lane: LaneHeavy, Lease: 120 * time.Second, Heartbeat: 30 * time.Second, Deadline: 30 * time.Minute, MaxAttempts: 3, Backoff: defaultBackoff},
		{Kind: workitems.KindAlignmentExecute, Lane: LaneHeavy, Lease: 120 * time.Second, Heartbeat: 30 * time.Second, Deadline: 30 * time.Minute, MaxAttempts: 3, Backoff: defaultBackoff},
		{Kind: workitems.KindReplacementEvaluate, Lane: LaneHeavy, Lease: 120 * time.Second, Heartbeat: 30 * time.Second, Deadline: 30 * time.Minute, MaxAttempts: 3, Backoff: defaultBackoff},
		// The subscription competition decides and admits one instance's
		// waiting releases: target probes, naming and qB lookups for each
		// decided release run inside one claim, so the deadline covers a whole
		// backlog round; it has no file effect of its own.
		{Kind: workitems.KindSubscriptionCompetition, Lane: LaneLight, Lease: 120 * time.Second, Heartbeat: 30 * time.Second, Deadline: 15 * time.Minute, MaxAttempts: 3, Backoff: defaultBackoff},
		{Kind: workitems.KindReplacementDelete, Lane: LaneHeavy, Lease: 120 * time.Second, Heartbeat: 30 * time.Second, Deadline: 30 * time.Minute, MaxAttempts: 3, Backoff: defaultBackoff},
		// Calibration and the two generation consumers are DB-bound owner work
		// (no external effect): light lane, long-enough deadline for a large
		// library, retried independently from each other.
		{Kind: workitems.KindReconciliationCalibrate, Lane: LaneLight, Lease: 120 * time.Second, Heartbeat: 30 * time.Second, Deadline: 30 * time.Minute, MaxAttempts: 3, Backoff: defaultBackoff},
		{Kind: workitems.KindReconciliationConsumeAlignment, Lane: LaneLight, Lease: 60 * time.Second, Heartbeat: 15 * time.Second, Deadline: 300 * time.Second, MaxAttempts: 3, Backoff: defaultBackoff},
		{Kind: workitems.KindReconciliationConsumeReplacement, Lane: LaneLight, Lease: 60 * time.Second, Heartbeat: 15 * time.Second, Deadline: 300 * time.Second, MaxAttempts: 3, Backoff: defaultBackoff},
		// A user file operation (file manager) hashes and moves, copies or
		// deletes one file: heavy file-effect lane, serial with the others.
		{Kind: workitems.KindFileManagerExecute, Lane: LaneHeavy, Lease: 120 * time.Second, Heartbeat: 30 * time.Second, Deadline: 30 * time.Minute, MaxAttempts: 3, Backoff: defaultBackoff},
		// A download-completion action is a few qBittorrent calls and read-only
		// path checks: light lane, idempotent, so it retries a few times.
		{Kind: workitems.KindCompletionExecute, Lane: LaneLight, Lease: 90 * time.Second, Heartbeat: 15 * time.Second, Deadline: 90 * time.Second, MaxAttempts: 3, Backoff: defaultBackoff},
	}
}

// Registry is the enforced kind policy registry. Missing or duplicate
// registration fails closed; there is no generic default fallback.
type Registry struct {
	byKind map[string]KindPolicy
	order  []string
}

// NewRegistry validates and stores the policy set. Duplicate kinds, empty
// kinds, non-positive leases/heartbeats/deadlines or max_attempts outside
// [1,100] are rejected.
func NewRegistry(policies ...KindPolicy) (*Registry, error) {
	r := &Registry{byKind: make(map[string]KindPolicy, len(policies))}
	for _, p := range policies {
		if p.Kind == "" {
			return nil, fmt.Errorf("kind policy requires a kind")
		}
		if _, exists := r.byKind[p.Kind]; exists {
			return nil, fmt.Errorf("duplicate kind policy %q", p.Kind)
		}
		if p.Lane != LaneLight && p.Lane != LaneHeavy {
			return nil, fmt.Errorf("kind %q has invalid lane %q", p.Kind, p.Lane)
		}
		if p.Lease <= 0 || p.Lease > time.Hour {
			return nil, fmt.Errorf("kind %q lease outside bounds: %v", p.Kind, p.Lease)
		}
		if p.Heartbeat <= 0 || p.Heartbeat >= p.Lease {
			return nil, fmt.Errorf("kind %q heartbeat must be positive and shorter than lease: %v", p.Kind, p.Heartbeat)
		}
		if p.Deadline <= 0 {
			return nil, fmt.Errorf("kind %q deadline must be positive", p.Kind)
		}
		if p.MaxAttempts < 1 || p.MaxAttempts > 100 {
			return nil, fmt.Errorf("kind %q max_attempts outside [1,100]: %d", p.Kind, p.MaxAttempts)
		}
		if p.Backoff == nil {
			p.Backoff = defaultBackoff
		}
		r.byKind[p.Kind] = p
		r.order = append(r.order, p.Kind)
	}
	return r, nil
}

// Get returns the policy for kind, failing closed when unregistered.
func (r *Registry) Get(kind string) (KindPolicy, error) {
	if r == nil {
		return KindPolicy{}, fmt.Errorf("kind registry is nil")
	}
	p, ok := r.byKind[kind]
	if !ok {
		return KindPolicy{}, fmt.Errorf("no kind policy registered for %q", kind)
	}
	return p, nil
}

// MustGet panics on an unregistered kind (composition-root startup assertion).
func (r *Registry) MustGet(kind string) KindPolicy {
	p, err := r.Get(kind)
	if err != nil {
		panic(err)
	}
	return p
}

// Kinds returns the registered kinds in registration order.
func (r *Registry) Kinds() []string {
	return append([]string(nil), r.order...)
}

// KindsForLane returns the kinds pinned to one lane, in registration order.
func (r *Registry) KindsForLane(lane Lane) []string {
	var kinds []string
	for _, kind := range r.order {
		if r.byKind[kind].Lane == lane {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

// Complete reports whether every registered policy is backed by the given
// handler set (composition-root wiring completeness check).
func (r *Registry) Complete(handlers map[string]Handler) error {
	for _, kind := range r.order {
		if _, ok := handlers[kind]; !ok {
			return fmt.Errorf("kind %q has a policy but no handler wired", kind)
		}
	}
	for kind := range handlers {
		if _, ok := r.byKind[kind]; !ok {
			return fmt.Errorf("handler for kind %q is wired without a kind policy", kind)
		}
	}
	return nil
}
