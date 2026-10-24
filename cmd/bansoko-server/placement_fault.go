package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	fileoperationapplication "bansoko/internal/fileoperation/application"
)

// The placement-handoff fixture fault exists so a disposable browser session can
// observe the real placement failure and manual-retry transitions end to end.
// It is deliberately NOT a public API and NOT a product behavior switch:
//
//   - it can only be enabled by the BANSOKO_FIXTURE_PLACEMENT_FAULT process
//     environment variable at server boot;
//   - the same boot refuses to start when the disposable-database marker
//     (BANSOKO_TEST_DB_DISPOSABLE=1) is absent, so a production deployment can never
//     arm it;
//   - it changes only the placement sink's outcome. It never writes an owner
//     or WorkItem terminal state, never bypasses the durable obligation, and
//     never re-runs a file effect: the state machine still reaches `failed` by
//     exhausting its own retry budget.
const (
	placementFaultEnv         = "BANSOKO_FIXTURE_PLACEMENT_FAULT"
	placementFaultDelayEnv    = "BANSOKO_FIXTURE_PLACEMENT_RETRY_MS"
	placementFaultMaxSubjects = 32
)

// placementFaultSink wraps the real placement sink and fails admissions of the
// file operation under test so the stage exhausts its real retry budget. Two
// modes exist, both keyed to the file operation (never a global request
// counter):
//
//   - "exhaust": every admission fails until the stage has a terminally failed
//     attempt, i.e. until the automatic retry budget is spent. A manual retry
//     then re-opens the obligation and its next admission is allowed through, so
//     the browser test can release the fault without any control API or SQL.
//   - a positive integer N: the first N admissions fail (fixed budget).
type placementFaultSink struct {
	inner fileoperationapplication.PlacementSink
	db    *sql.DB
	limit int

	mu        sync.Mutex
	remaining map[string]int
}

// placementFaultExhaustMode is the semantic fault mode: automatic attempts fail
// until the placement stage exhausts, and the manual retry succeeds.
const placementFaultExhaustMode = "exhaust"

// newPlacementFaultSink returns the outer sink for one composition, plus the
// retry delay the fixture session should use so the bounded retry budget is
// spent inside the browser test. A disabled fault returns the inner sink
// unchanged with a zero delay.
func newPlacementFaultSink(inner fileoperationapplication.PlacementSink, db *sql.DB, lookup func(string) string) (fileoperationapplication.PlacementSink, time.Duration, error) {
	raw := strings.TrimSpace(lookup(placementFaultEnv))
	if raw == "" {
		return inner, 0, nil
	}
	if strings.TrimSpace(lookup("BANSOKO_TEST_DB_DISPOSABLE")) != "1" {
		return nil, 0, fmt.Errorf("%s is a disposable-fixture-only switch and requires BANSOKO_TEST_DB_DISPOSABLE=1", placementFaultEnv)
	}
	limit := 0
	if !strings.EqualFold(raw, placementFaultExhaustMode) {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			return nil, 0, fmt.Errorf("%s must be %q or a bounded positive attempt count", placementFaultEnv, placementFaultExhaustMode)
		}
		limit = parsed
	}
	if limit == 0 && db == nil {
		return nil, 0, fmt.Errorf("%s=%s requires the database handle", placementFaultEnv, placementFaultExhaustMode)
	}
	delay := time.Second
	if configured := strings.TrimSpace(lookup(placementFaultDelayEnv)); configured != "" {
		milliseconds, err := strconv.Atoi(configured)
		if err != nil || milliseconds < 1 || milliseconds > 60_000 {
			return nil, 0, fmt.Errorf("%s must be a bounded millisecond delay", placementFaultDelayEnv)
		}
		delay = time.Duration(milliseconds) * time.Millisecond
	}
	return &placementFaultSink{inner: inner, db: db, limit: limit, remaining: map[string]int{}}, delay, nil
}

func (s *placementFaultSink) AdmitFromFileOperation(ctx context.Context, fileOperationID string) error {
	if s == nil || s.inner == nil {
		return &fileoperationapplication.PlacementSinkError{Code: "placement_sink_unwired", Err: errors.New("placement sink is not fully wired")}
	}
	if s.take(ctx, fileOperationID) {
		return &fileoperationapplication.PlacementSinkError{
			Code: "placement_admission_failed",
			Err:  errors.New("fixture placement fault: admission rejected for this attempt"),
		}
	}
	return s.inner.AdmitFromFileOperation(ctx, fileOperationID)
}

// take reports whether this admission is one of the injected failures.
func (s *placementFaultSink) take(ctx context.Context, fileOperationID string) bool {
	if s.limit == 0 {
		// Exhaust mode: keep the stage inside its automatic retry budget, then
		// release the fault once that budget is spent (the terminal failed
		// continuation is the durable evidence that it is).
		return !s.hasExhaustedAttempt(ctx, fileOperationID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remaining == nil {
		s.remaining = map[string]int{}
	}
	spent, seen := s.remaining[fileOperationID]
	if !seen {
		// Bound the bookkeeping so a long-lived fixture session cannot grow it
		// without limit; the test release is always one of the first subjects.
		if len(s.remaining) >= placementFaultMaxSubjects {
			return false
		}
		s.remaining[fileOperationID] = 0
		spent = 0
	}
	if spent >= s.limit {
		return false
	}
	s.remaining[fileOperationID] = spent + 1
	return true
}

// hasExhaustedAttempt reports whether the placement stage of this file
// operation already has a terminally failed attempt, i.e. its automatic retry
// budget is spent. A read error keeps the fault active, so an unreadable state
// can never silently turn the injected failure into a passing admission.
func (s *placementFaultSink) hasExhaustedAttempt(ctx context.Context, fileOperationID string) bool {
	if s.db == nil {
		return false
	}
	var exhausted bool
	if err := s.db.QueryRowContext(ctx, `
SELECT EXISTS (
	SELECT 1 FROM work_item_outcomes item
	WHERE item.owner_type = 'file_operation' AND item.owner_id = $1
	  AND item.kind = 'file_execute' AND item.state = 'failed'
	  AND item.input_snapshot ? 'placement_retry'
)`, fileOperationID).Scan(&exhausted); err != nil {
		return false
	}
	return exhausted
}
