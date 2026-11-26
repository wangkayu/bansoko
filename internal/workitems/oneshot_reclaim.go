package workitems

// Finite one-shot reclamation and fail-closed cleanup.
//
// The marker (one_shot_acquisitions) is the only reclamation authority:
// origin=oneshot and state=stopped are provenance/automation-intent facts and
// are NEVER selected by any query here, so persistent Agent skeletons and
// stopped Subscriptions are unreachable. Reclamation shares the marker-first
// lock order with the effect gate and every downstream insertion; it proceeds
// only when the admission ledger is empty, active_effect_count=0, no related
// WorkItem is available or leased, and the anchored owner graph validates by
// exact identity. A running root or live chain work retains the current
// running/pending marker without a write. Once the chain is terminal, anything
// unprovable leaves cleanup_blocked with a bounded reason; failed rechecks
// extend the deadline within the hard maximum.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	observabilityport "bansoko/internal/observability/port"
	persistence "bansoko/internal/workitems/adapter/persistence/generated"
)

// One-shot marker states (001_baseline.sql and 063_retention_renames.sql).
const (
	OneShotStateRunning        = "running"
	OneShotStateReclaimPending = "reclaim_pending"
	OneShotStateCleanupBlocked = "cleanup_blocked"
	OneShotStateReclaiming     = "reclaiming"
	OneShotStateCleaned        = "cleaned"
)

const (
	defaultOneShotBatchSize = 10
	// bounded reason fields mirror the marker column CHECKs.
	maxOneShotReasonCode = 128
	maxOneShotReason     = 1024
)

// OneShotReclaimResult summarizes one bounded RunPass pass.
type OneShotReclaimResult struct {
	Drained   int // running -> reclaim_pending
	Blocked   int // newly transitioned to cleanup_blocked
	Reclaimed int // reclaim_pending -> reclaiming -> cleaned
	Rechecked int // cleanup_blocked -> reclaim_pending
	Extended  int // cleanup_blocked deadlines advanced
	Revived   int // cleanup_blocked -> running
	Purged    int // cleaned markers physically deleted after retention
}

// Changed reports whether the pass committed any visible changes.
func (r OneShotReclaimResult) Changed() bool {
	return r.Drained > 0 || r.Blocked > 0 || r.Reclaimed > 0 || r.Rechecked > 0 || r.Extended > 0 || r.Revived > 0 || r.Purged > 0
}

// OneShotReclaimService processes finite one-shot markers in bounded
// database-time batches. It shares only scheduling shape with evidence
// record retention; its predicate and owner scope are one-shot-specific.
type OneShotReclaimService struct {
	DB            *sql.DB
	Logger        observabilityport.RuntimeLogger
	BatchSize     int
	RetentionDays int
	// Now is the evaluation instant of the root detail-expiry predicate the
	// reclaim checks. Nil means the database clock. It moves only the instant
	// the expiry is evaluated at; every written row records the database clock.
	Now func() time.Time
}

func (s *OneShotReclaimService) evaluationNow() sql.NullTime {
	if s.Now == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: s.Now(), Valid: true}
}

// NewOneShotReclaimService constructs the service. A non-positive batch size
// uses the default; retention outside [1,30] days falls back to the default.
func NewOneShotReclaimService(db *sql.DB, logger observabilityport.RuntimeLogger) (*OneShotReclaimService, error) {
	if db == nil {
		return nil, errors.New("one-shot reclaim service database is nil")
	}
	if logger == nil {
		logger = observabilityport.NopRuntimeLogger{}
	}
	batch := defaultOneShotBatchSize
	if batch <= 0 {
		batch = defaultOneShotBatchSize
	}
	return &OneShotReclaimService{DB: db, Logger: logger, BatchSize: batch, RetentionDays: DefaultRetentionDays}, nil
}

// RunPass executes one bounded pass: drain terminal chains out of running,
// reclaim due reclaim_pending markers, resume cleanup_blocked markers whose
// chain is still open and recheck due ones, and purge expired cleaned markers.
func (s *OneShotReclaimService) RunPass(ctx context.Context) (OneShotReclaimResult, error) {
	var result OneShotReclaimResult
	drained, blocked, err := s.drainCandidates(ctx)
	result.Drained = drained
	result.Blocked = blocked
	if err != nil {
		return result, err
	}

	reclaimed, blocked, err := s.reclaimDue(ctx)
	result.Reclaimed = reclaimed
	result.Blocked += blocked
	if err != nil {
		return result, err
	}

	result.Rechecked, result.Extended, result.Revived, err = s.recheckBlocked(ctx)
	if err != nil {
		return result, err
	}

	result.Purged, err = s.purgeCleaned(ctx)
	return result, err
}

func (s *OneShotReclaimService) logEvent(ctx context.Context, operation, reasonCode, message string) {
	if s == nil || s.Logger == nil {
		return
	}
	s.Logger.Info(ctx, observabilityport.RuntimeEvent{
		Message:    message,
		Component:  "workitems",
		Operation:  operation,
		Outcome:    "info",
		ReasonCode: reasonCode,
	})
}

func (s *OneShotReclaimService) logError(ctx context.Context, operation, reasonCode, message string) {
	if s == nil || s.Logger == nil {
		return
	}
	s.Logger.Error(ctx, observabilityport.RuntimeEvent{
		Message:    message,
		Component:  "workitems",
		Operation:  operation,
		Outcome:    "failure",
		ReasonCode: reasonCode,
		ErrorClass: "oneshot_reclaim",
	})
}

// oneShotDueIDs selects one bounded batch of marker ids in the given states
// using database time. The batch read is advisory only: each marker is
// re-locked FOR UPDATE inside its own processing transaction, which is where
// concurrent server instances actually serialize.
func (s *OneShotReclaimService) oneShotDueIDs(ctx context.Context, states []string, due bool) ([]string, error) {
	queries := persistence.New(s.DB)
	batchSize := int32(s.BatchSize)
	switch {
	case !due && len(states) == 1 && states[0] == OneShotStateRunning:
		return queries.ListRunningOneShotMarkers(ctx, batchSize)
	case due && len(states) == 1 && states[0] == OneShotStateReclaimPending:
		return queries.ListDueReclaimPendingOneShotMarkers(ctx, batchSize)
	case due && len(states) == 1 && states[0] == OneShotStateCleanupBlocked:
		return queries.ListDueCleanupBlockedOneShotMarkers(ctx, batchSize)
	default:
		return nil, fmt.Errorf("unsupported one-shot marker selection states=%v due=%t", states, due)
	}
}

// drainCandidates moves terminal, fully-drained running markers to
// reclaim_pending with their computed terminal outcome. Chains that are not
// terminal yet are skipped; terminal chains whose reclamation proof fails
// become cleanup_blocked.
func (s *OneShotReclaimService) drainCandidates(ctx context.Context) (drained, blocked int, err error) {
	ids, err := s.oneShotDueIDs(ctx, []string{OneShotStateRunning}, false)
	if err != nil {
		return 0, 0, err
	}
	for _, id := range ids {
		processed, blockErr := s.drainOne(ctx, id)
		if blockErr != nil {
			return drained, blocked, blockErr
		}
		switch processed {
		case "drained":
			drained++
		case "blocked":
			blocked++
		}
	}
	return drained, blocked, nil
}

// reclaimDue processes reclaim_pending markers whose reclaim deadline has
// passed: revalidate, then raise the epoch into reclaiming, delete ONLY the
// anchored temporary database owner graph leaf-first, and mark cleaned — all
// in one transaction. A crash rolls back to reclaim_pending.
func (s *OneShotReclaimService) reclaimDue(ctx context.Context) (reclaimed, blocked int, err error) {
	ids, err := s.oneShotDueIDs(ctx, []string{OneShotStateReclaimPending}, true)
	if err != nil {
		return 0, 0, err
	}
	for _, id := range ids {
		status, err := s.reclaimOne(ctx, id)
		if err != nil {
			return reclaimed, blocked, err
		}
		switch status {
		case reclaimCleaned:
			reclaimed++
		case reclaimBlocked:
			blocked++
		}
	}
	return reclaimed, blocked, nil
}

// recheckBlocked processes selected cleanup_blocked markers. A marker whose
// chain is still open (blocked while its work was live) returns to running;
// for a due marker, a passing proof returns it to reclaim_pending with a fresh
// deadline and a failing proof extends the deadline within the hard maximum
// from terminal_at.
func (s *OneShotReclaimService) recheckBlocked(ctx context.Context) (rechecked, extended, revived int, err error) {
	ids, err := s.oneShotDueIDs(ctx, []string{OneShotStateCleanupBlocked}, true)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, id := range ids {
		status, blockErr := s.recheckOne(ctx, id)
		if blockErr != nil {
			return rechecked, extended, revived, blockErr
		}
		switch status {
		case "revived":
			revived++
		case "passed":
			rechecked++
		case "extended":
			extended++
		}
	}
	return rechecked, extended, revived, nil
}

// purgeCleaned physically deletes cleaned markers whose reclaim window has passed.
// After purge the replay record is gone: the request key may be reused for a
// new admission by design.
func (s *OneShotReclaimService) purgeCleaned(ctx context.Context) (int, error) {
	result, err := persistence.New(s.DB).PurgeExpiredCleanedOneShotMarkers(ctx, int32(s.BatchSize))
	if err != nil {
		return 0, fmt.Errorf("purge cleaned one-shot markers: %w", err)
	}
	purged, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read one-shot purge result: %w", err)
	}
	if purged > 0 {
		s.logEvent(ctx, "oneshot_purge", "purge_completed", fmt.Sprintf("expired cleaned one-shot markers purged (count=%d)", purged))
	}
	return int(purged), nil
}
