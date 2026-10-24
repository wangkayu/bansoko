package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"bansoko/internal/filemaintenance/policy"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/worktree"
)

// ScanSubmitter is the P3.2 snapshot scan submission port (composition wires
// the reconciliation service). Submission is idempotent per input
// fingerprint: a scheduler tick passes a fresh request identity so every
// independent periodic trigger starts a new generation, while a replay of the
// exact same request identity reuses the completed generation.
//
// treeTop is the scope of the scan: the directory the work's own files live
// in. It is derived from placement evidence per submission, never configured.
type ScanSubmitter interface {
	SubmitScan(ctx context.Context, animeID, treeTop, requestID string) error
}

// AnimeLister enumerates managed anime ids for the global policy schedule.
type AnimeLister interface {
	ListAnimeIDs(ctx context.Context) ([]string, error)
}

// ScanScheduler drives the FileMaintenance periodic scan: it reads the
// effective policy (global + anime overrides), and for every anime whose
// policy enables scanning and whose interval is due, submits an idempotent
// snapshot scan. The schedule lives inside FileMaintenance — it never
// touches the three fixed automation schedules.
type ScanScheduler struct {
	db          *sql.DB
	policies    *policy.PolicyService
	submitter   ScanSubmitter
	animeLister AnimeLister
	logger      observabilityport.RuntimeLogger

	mu         sync.Mutex
	lastSubmit map[string]time.Time
}

// NewScanScheduler wires the scheduler.
func NewScanScheduler(db *sql.DB, policies *policy.PolicyService, submitter ScanSubmitter, animeLister AnimeLister, logger observabilityport.RuntimeLogger) *ScanScheduler {
	if logger == nil {
		logger = observabilityport.NopRuntimeLogger{}
	}
	return &ScanScheduler{
		db: db, policies: policies, submitter: submitter, animeLister: animeLister,
		logger: logger, lastSubmit: map[string]time.Time{},
	}
}

// Tick evaluates every policy scope once and submits due scans.
//
// An anime's effective Enabled is its override's when it has one, else the
// global policy's. So with the global policy absent or disabled only anime
// with an enabled override can be due, and the managed anime list is not read;
// with nothing enabled at all the tick ends after one small query.
func (s *ScanScheduler) Tick(ctx context.Context) error {
	now := time.Now()
	// Enumerate explicit scopes (including disabled overrides) plus global
	// candidates; use the policy owner's resolver for every decision.
	rows, err := s.db.QueryContext(ctx, `SELECT scope_key, enabled FROM filemaintenance_policies WHERE scope_key = $1 OR scope_key LIKE 'anime:%'`, policy.GlobalScope)
	if err != nil {
		return err
	}
	var animeIDs, enabledOverrides []string
	seenIDs := map[string]bool{}
	globalEnabled := false
	for rows.Next() {
		var scope string
		var enabled bool
		if err := rows.Scan(&scope, &enabled); err != nil {
			rows.Close()
			return err
		}
		if scope == policy.GlobalScope {
			globalEnabled = enabled
			continue
		}
		id := scope[len("anime:"):]
		if !seenIDs[id] {
			animeIDs = append(animeIDs, id)
			seenIDs[id] = true
			if enabled {
				enabledOverrides = append(enabledOverrides, id)
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if !globalEnabled {
		// Only an enabled override can make an anime due.
		animeIDs = enabledOverrides
	} else if s.animeLister != nil {
		all, err := s.animeLister.ListAnimeIDs(ctx)
		if err != nil {
			return err
		}
		for _, id := range all {
			if !seenIDs[id] {
				animeIDs = append(animeIDs, id)
				seenIDs[id] = true
			}
		}
	}
	for _, animeID := range animeIDs {
		effective, err := s.policies.GetEffective(ctx, animeID)
		if err != nil {
			return err
		}
		interval := effective.ScanIntervalSeconds
		if !effective.Enabled || interval == nil {
			continue
		}
		// Periodic scheduling is active-only. A stopped instance keeps its
		// files and owner truth but is not periodically scanned; the owner may
		// still submit an explicit manual read-only scan/preview. A deleted
		// instance is also skipped here (defense in depth: unsubscribe now
		// removes its policy row too).
		if !s.instanceActive(ctx, animeID) {
			continue
		}
		s.mu.Lock()
		last, seen := s.lastSubmit[animeID]
		s.mu.Unlock()
		if seen && now.Sub(last) < time.Duration(*interval)*time.Second {
			continue
		}
		if s.submitter == nil {
			return errors.New("scan scheduler has no submitter")
		}
		treeTop, ok, err := s.treeTop(ctx, animeID)
		if err != nil {
			return err
		}
		if !ok {
			// The scope of a scan is the work's own tree top, derived from the
			// files its placements put on disk. A work with no placement
			// evidence has no tree top: the tick skips it with a diagnostic
			// rather than scanning a directory the work does not live in. The
			// state is normal (a work that has placed nothing yet), so it is a
			// Debug record: a Warn or Info line per work per tick would drown
			// the runtime log in a condition nothing is wrong with.
			s.logger.Debug(ctx, observabilityport.RuntimeEvent{
				Message:    "file maintenance scan skipped: the work has no tree top",
				Component:  "filemaintenance",
				Operation:  "scan_submit",
				Outcome:    "skipped",
				ReasonCode: "scan_scope_unavailable",
				OwnerType:  "managed_anime",
				OwnerID:    animeID,
				ErrorClass: "scan_scheduler",
			})
			continue
		}
		// Every due tick is an independent periodic trigger: a fresh request
		// identity makes the input fingerprint differ, so the reconciliation
		// owner starts a strictly newer generation instead of reusing the old
		// completed one forever. The active instance gate above remains mandatory.
		requestID := fmt.Sprintf("periodic:%d:%s", now.UnixNano(), animeID)
		if err := s.submitter.SubmitScan(ctx, animeID, treeTop, requestID); err != nil {
			// A running scan is the idempotent conflict; any other error is
			// logged and retried on the next tick — the scheduler never
			// blocks the core chain.
			s.logger.Warn(ctx, observabilityport.RuntimeEvent{
				Message:    "file maintenance scan submission failed",
				Component:  "filemaintenance",
				Operation:  "scan_submit",
				Outcome:    "failure",
				ReasonCode: "scan_submit_failed",
				OwnerType:  "managed_anime",
				OwnerID:    animeID,
				ErrorClass: "scan_scheduler",
			})
			continue
		}
		s.mu.Lock()
		s.lastSubmit[animeID] = now
		s.mu.Unlock()
	}
	return nil
}

// treeTop returns the work's tree top: the deepest directory holding the files
// its own active placements put on disk. ok=false means the work has no
// placement evidence, so it has no scope to scan.
func (s *ScanScheduler) treeTop(ctx context.Context, animeID string) (string, bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT physical_path FROM placements
WHERE managed_anime_id = $1 AND state = 'active' ORDER BY physical_path LIMIT 1000`, animeID)
	if err != nil {
		return "", false, fmt.Errorf("read scan scope placements: %w", err)
	}
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			rows.Close()
			return "", false, fmt.Errorf("scan scope placement: %w", err)
		}
		paths = append(paths, path)
	}
	if err := rows.Close(); err != nil {
		return "", false, fmt.Errorf("read scan scope placements: %w", err)
	}
	if err := rows.Err(); err != nil {
		return "", false, fmt.Errorf("read scan scope placements: %w", err)
	}
	top, ok := worktree.Top(paths)
	return top, ok, nil
}

// instanceActive reports whether the anime still exists with an active
// subscription. Stopped and deleted instances are excluded from the periodic
// schedule; the reconciliation SubmitScan gate is the second enforcement layer.
func (s *ScanScheduler) instanceActive(ctx context.Context, animeID string) bool {
	var state string
	err := s.db.QueryRowContext(ctx, `
SELECT sub.state
FROM managed_anime a
JOIN subscriptions sub ON sub.managed_anime_id = a.id
WHERE a.id = $1`, animeID).Scan(&state)
	if err != nil {
		return false
	}
	return state == "active"
}

// Run is the blocking loop for the composition root goroutine.
func (s *ScanScheduler) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Tick(ctx); err != nil {
				s.logger.Error(ctx, observabilityport.RuntimeEvent{
					Message:    "file maintenance scan tick failed",
					Component:  "filemaintenance",
					Operation:  "scan_tick",
					Outcome:    "failure",
					ReasonCode: "scan_tick_failed",
					ErrorClass: "scan_scheduler",
				})
			}
		}
	}
}
