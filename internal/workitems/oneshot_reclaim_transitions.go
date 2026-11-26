package workitems

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	persistence "bansoko/internal/workitems/adapter/persistence/generated"
)

func (s *OneShotReclaimService) drainOne(ctx context.Context, markerID string) (string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin one-shot drain: %w", err)
	}
	defer tx.Rollback()
	row, err := lockOneShotMarkerForUpdateTx(ctx, tx, markerID)
	if errors.Is(err, ErrOneShotMarkerMissing) {
		return "", nil // purged concurrently
	}
	if err != nil {
		return "", err
	}
	if row.State != OneShotStateRunning {
		return "", nil // raced into another state
	}
	graph, graphErr := s.loadAnchorsTx(ctx, tx, markerID)
	if graphErr != nil {
		// Anchor corruption is typed: block with a bounded actionable reason.
		if _, updateErr := s.blockMarkerTx(ctx, tx, markerID, "anchor_validation_failed", boundedText(graphErr.Error(), maxOneShotReason)); updateErr != nil {
			return "", updateErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return "", fmt.Errorf("commit one-shot cleanup_blocked: %w", commitErr)
		}
		s.logEvent(ctx, "oneshot_drain", "cleanup_blocked", "one-shot chain blocked: anchor validation failed")
		return "blocked", nil
	}
	outcome, outcomeErr := s.evaluateChainTx(ctx, tx, graph)
	if errors.Is(outcomeErr, errOneShotChainRunning) {
		return "", nil // the running root owns progress; retry next pass
	}
	if outcomeErr != nil {
		// Unprovable quiescence or unsettled owners: keep running when work
		// may still be live, otherwise block with the bounded reason.
		live, liveErr := s.chainLiveTx(ctx, tx, graph)
		if liveErr != nil {
			return "", liveErr
		}
		if live || len(row.Leases) > 0 {
			return "", nil // still running normally; retry next pass
		}
		if _, updateErr := s.blockMarkerTx(ctx, tx, markerID, "chain_unsettled", boundedText(outcomeErr.Error(), maxOneShotReason)); updateErr != nil {
			return "", updateErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return "", fmt.Errorf("commit one-shot cleanup_blocked: %w", commitErr)
		}
		s.logEvent(ctx, "oneshot_drain", "cleanup_blocked", "one-shot chain blocked: "+boundedText(outcomeErr.Error(), 160))
		return "blocked", nil
	}
	// Live chain work keeps the marker running whatever the ledger holds: an
	// admission lease of live work settles with that work. Only a ledger
	// that no live work can settle blocks the marker.
	if live, liveErr := s.chainLiveTx(ctx, tx, graph); liveErr != nil {
		return "", liveErr
	} else if live {
		return "", nil
	}
	if len(row.Leases) > 0 {
		if _, updateErr := s.blockMarkerWithOutcomeTx(ctx, tx, markerID, "admission_ledger_not_drained", "the admission ledger must be empty before reclamation", outcome.outcome); updateErr != nil {
			return "", updateErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return "", fmt.Errorf("commit one-shot cleanup_blocked: %w", commitErr)
		}
		s.logEvent(ctx, "oneshot_drain", "cleanup_blocked", "one-shot chain terminal but admission ledger is not drained")
		return "blocked", nil
	}
	if err := s.setTerminalPendingTx(ctx, tx, markerID, outcome); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit one-shot drain: %w", err)
	}
	s.logEvent(ctx, "oneshot_drain", "reclaim_pending", "one-shot chain drained to reclaim_pending (outcome="+outcome.outcome+")")
	return "drained", nil
}

type oneShotReclaimStatus string

const (
	reclaimCleaned         oneShotReclaimStatus = "cleaned"
	reclaimWaiting         oneShotReclaimStatus = "waiting"
	reclaimContended       oneShotReclaimStatus = "contended"
	reclaimBlocked         oneShotReclaimStatus = "blocked"
	reclaimCompactRetained oneShotReclaimStatus = "compact_retained"
)

func (s *OneShotReclaimService) reclaimOne(ctx context.Context, markerID string) (oneShotReclaimStatus, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin one-shot reclamation: %w", err)
	}
	defer tx.Rollback()
	row, err := lockOneShotMarkerForUpdateTx(ctx, tx, markerID)
	if errors.Is(err, ErrOneShotMarkerMissing) {
		return reclaimContended, nil
	}
	if err != nil {
		return "", err
	}
	if row.State != OneShotStateReclaimPending {
		return reclaimContended, nil
	}
	due, err := persistence.New(tx).IsOneShotMarkerDue(ctx, markerID)
	if err != nil {
		return "", fmt.Errorf("read one-shot reclaim deadline: %w", err)
	}
	if !due {
		return reclaimWaiting, nil
	}
	// Marker expiration never grants deletion of independent acquisition details.
	// This read takes no management/root locks; the marker is the stable prefix.
	now := s.evaluationNow()
	var retained bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM acquisitions r WHERE r.id IN (`+oneShotRootsQuery+`) AND r.record_state='full' AND (r.state='running' OR r.detail_until>COALESCE($2::timestamptz,CURRENT_TIMESTAMP)))`, markerID, now).Scan(&retained); err != nil {
		return "", fmt.Errorf("observe retained acquisition details: %w", err)
	}
	if retained {
		return reclaimWaiting, nil
	}
	graph, graphErr := s.loadAnchorsTx(ctx, tx, markerID)
	if graphErr != nil {
		return s.commitReclaimBlock(ctx, tx, markerID, oneShotCleanupBlock{"anchor_validation_failed", graphErr.Error()})
	}
	// Keep marker -> owner -> child order. The owner row serializes with
	// effect entry before we prove eligibility and delete any child.
	if graph.animeID != "" {
		if _, err := tx.ExecContext(ctx, `SELECT 1 FROM managed_anime WHERE id = $1 FOR UPDATE`, graph.animeID); err != nil {
			return "", fmt.Errorf("lock temporary owner for reclamation: %w", err)
		}
	}
	live, blocked, err := s.proveOneShotCleanupTx(ctx, tx, graph, row)
	if err != nil {
		return "", err
	}
	if live {
		return reclaimWaiting, nil
	}
	if blocked != nil {
		return s.commitReclaimBlock(ctx, tx, markerID, *blocked)
	}
	// Fenced drain phase: raise the epoch so any late worker's gate rejects
	// with ONE_SHOT_RECLAIMING before it can start an external effect.
	result, err := persistence.New(tx).EnterOneShotReclaiming(ctx, markerID)
	if err != nil {
		return "", fmt.Errorf("enter one-shot reclaiming: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return "", fmt.Errorf("read one-shot reclaiming result: %w", err)
	} else if affected != 1 {
		return "", ErrOneShotMarkerMissing
	}
	// Compact with the same evaluation instant as the retained-root check.
	// A refusal rolls back the marker's new epoch and any preceding deletes.
	if compacted, err := s.compactOneShotRootsTx(ctx, tx, markerID, graph, now); err != nil {
		return "", err
	} else if !compacted {
		return reclaimCompactRetained, nil
	}
	if err := deleteTemporaryOwnerGraphTx(ctx, tx, graph); err != nil {
		return "", err
	}
	result, err = persistence.New(tx).MarkOneShotCleaned(ctx, markerID)
	if err != nil {
		return "", fmt.Errorf("mark one-shot cleaned: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return "", fmt.Errorf("read one-shot cleaned result: %w", err)
	} else if affected != 1 {
		return "", ErrOneShotMarkerMissing
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit one-shot reclamation: %w", err)
	}
	s.logEvent(ctx, "oneshot_reclaim", "cleaned", "one-shot temporary owner graph reclaimed leaf-first")
	return reclaimCleaned, nil
}

func (s *OneShotReclaimService) commitReclaimBlock(ctx context.Context, tx *sql.Tx, markerID string, block oneShotCleanupBlock) (oneShotReclaimStatus, error) {
	if _, err := s.blockMarkerTx(ctx, tx, markerID, block.code, block.reason); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit one-shot cleanup_blocked: %w", err)
	}
	s.logEvent(ctx, "oneshot_reclaim", "cleanup_blocked", "reclamation blocked: "+boundedText(block.reason, 160))
	return reclaimBlocked, nil
}

func (s *OneShotReclaimService) recheckOne(ctx context.Context, markerID string) (string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin one-shot recheck: %w", err)
	}
	defer tx.Rollback()
	row, err := lockOneShotMarkerForUpdateTx(ctx, tx, markerID)
	if errors.Is(err, ErrOneShotMarkerMissing) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if row.State != OneShotStateCleanupBlocked {
		return "", nil
	}
	graph, graphErr := s.loadAnchorsTx(ctx, tx, markerID)
	if graphErr == nil && graph.downloadTaskID != "" {
		// A drain that read a chain as terminal while its root still ran
		// blocked it with live work behind the gate. Such a chain resumes:
		// the marker returns to running and the attempts the gate refused are
		// given back, so the chain completes instead of exhausting its work.
		reply, err := resumeBlockedOneShotChainTx(ctx, tx, markerID, nil)
		if err != nil {
			return "", err
		}
		if reply.Resumed {
			if err := tx.Commit(); err != nil {
				return "", fmt.Errorf("commit one-shot resume: %w", err)
			}
			s.logEvent(ctx, "oneshot_recheck", "running", fmt.Sprintf("cleanup_blocked chain was still open; marker returned to running (plans=%d refunded_attempts=%d)", reply.Plans, reply.Refunded))
			return "revived", nil
		}
	}
	due, err := persistence.New(tx).IsOneShotMarkerDue(ctx, markerID)
	if err != nil {
		return "", fmt.Errorf("read one-shot recheck deadline: %w", err)
	}
	if !due {
		return "", nil // selected as open but cannot resume: recheck when due
	}
	if graphErr == nil {
		live, blocked, err := s.proveOneShotCleanupTx(ctx, tx, graph, row)
		if err != nil {
			return "", err
		}
		if !live && blocked == nil {
			deadline, err := s.reclaimDeadlineTx(ctx, tx)
			if err != nil {
				return "", err
			}
			result, err := persistence.New(tx).UnblockOneShotMarker(ctx, persistence.UnblockOneShotMarkerParams{
				RetentionUntil:   deadline,
				MaxRetentionDays: int32(MaxRetentionDays),
				MarkerID:         markerID,
			})
			if err != nil {
				return "", fmt.Errorf("unblock one-shot marker: %w", err)
			}
			if affected, err := result.RowsAffected(); err != nil {
				return "", fmt.Errorf("read unblocked one-shot marker result: %w", err)
			} else if affected != 1 {
				return "", ErrOneShotMarkerMissing
			}
			if err := tx.Commit(); err != nil {
				return "", fmt.Errorf("commit one-shot recheck: %w", err)
			}
			s.logEvent(ctx, "oneshot_recheck", "reclaim_pending", "cleanup_blocked recheck passed; marker returned to reclaim_pending")
			return "passed", nil
		}
	}
	// Failed recheck: extend the deadline within the hard maximum.
	result, err := persistence.New(tx).ExtendOneShotCleanupBlocked(ctx, persistence.ExtendOneShotCleanupBlockedParams{
		RetentionDays:    int32(s.RetentionDays),
		MaxRetentionDays: int32(MaxRetentionDays),
		MarkerID:         markerID,
	})
	if err != nil {
		return "", fmt.Errorf("extend one-shot cleanup_blocked deadline: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return "", fmt.Errorf("read one-shot deadline extension result: %w", err)
	} else if affected == 0 {
		return "", nil // deadline did not advance; leave the marker untouched
	} else if affected != 1 {
		return "", ErrOneShotMarkerMissing
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit one-shot recheck extension: %w", err)
	}
	s.logEvent(ctx, "oneshot_recheck", "deadline_extended", "cleanup_blocked recheck failed; deadline extended within the hard maximum")
	return "extended", nil
}

// oneShotResumeReply is the reply of one_shot_resume_blocked_chain.
type oneShotResumeReply struct {
	Resumed       bool   `json:"resumed"`
	Reason        string `json:"reason"`
	Plans         int    `json:"plans"`
	Refunded      int    `json:"refunded"`
	HeldSeconds   int64  `json:"held_seconds"`
	ClaimReturned bool   `json:"claim_returned"`
}

// resumeBlockedOneShotChainTx resumes a cleanup_blocked marker whose chain is
// still open (migration 104). The caller holds the marker lock and has
// validated the anchors. With a refused claim, that claim returns to available
// without using an attempt in the same transaction. A marker that cannot
// resume is left unchanged and the reply carries the reason.
func resumeBlockedOneShotChainTx(ctx context.Context, tx *sql.Tx, markerID string, refused *Claim) (oneShotResumeReply, error) {
	params := persistence.ResumeBlockedOneShotChainParams{MarkerID: markerID}
	if refused != nil {
		params.WorkItemID = sql.NullString{String: refused.ID, Valid: true}
		params.FencingToken = sql.NullInt64{Int64: refused.FencingToken, Valid: true}
	}
	raw, err := persistence.New(tx).ResumeBlockedOneShotChain(ctx, params)
	if err != nil {
		return oneShotResumeReply{}, fmt.Errorf("resume blocked one-shot chain: %w", err)
	}
	var reply oneShotResumeReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return oneShotResumeReply{}, fmt.Errorf("decode one-shot resume reply: %w", err)
	}
	return reply, nil
}

// setTerminalPendingTx writes the computed terminal evidence plus the
// reclaim deadline while moving running -> reclaim_pending. The ledger
// must already be empty (DB CHECK enforces the field matrix).
func (s *OneShotReclaimService) setTerminalPendingTx(ctx context.Context, tx *sql.Tx, markerID string, outcome oneShotOutcome) error {
	deadline, err := s.reclaimDeadlineTx(ctx, tx)
	if err != nil {
		return err
	}
	result, err := persistence.New(tx).DrainOneShotMarkerToReclaimPending(ctx, persistence.DrainOneShotMarkerToReclaimPendingParams{
		TerminalOutcome:    sql.NullString{String: outcome.outcome, Valid: true},
		TerminalReasonCode: sql.NullString{String: boundedText(outcome.reasonCode, maxOneShotReasonCode), Valid: true},
		TerminalReason:     sql.NullString{String: boundedText(outcome.reason, maxOneShotReason), Valid: true},
		RetentionUntil:     sql.NullTime{Time: deadline, Valid: true},
		MarkerID:           markerID,
	})
	if err != nil {
		return fmt.Errorf("drain one-shot marker to reclaim_pending: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("read drained one-shot marker result: %w", err)
	} else if affected != 1 {
		return ErrOneShotMarkerMissing
	}
	// The attempts model: the current attempt receives its one-way
	// terminal evidence in the same transaction, so the immutable history and
	// the marker projection cannot drift. The CAS (state='running') makes the
	// write idempotent for an already-terminal attempt and skips legacy rows
	// whose pointer predates the attempts backfill. The failed stage is derived
	// from the same anchored-chain order the read projection uses.
	if _, err := tx.ExecContext(ctx, `
UPDATE one_shot_execution_attempts
SET state = $2,
    failed_stage = NULLIF($5, ''),
    outcome_code = $3,
    outcome_reason = $4,
    terminal_at = CURRENT_TIMESTAMP,
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = (SELECT current_attempt_id FROM one_shot_acquisitions WHERE id = $1)
  AND state = 'running'`,
		markerID, outcome.outcome,
		boundedText(outcome.reasonCode, maxOneShotReasonCode),
		boundedText(outcome.reason, maxOneShotReason),
		boundedText(outcome.failedStage, maxOneShotReasonCode)); err != nil {
		return fmt.Errorf("terminalize current one-shot execution attempt: %w", err)
	}
	return nil
}

// blockMarkerTx moves a failing marker into cleanup_blocked with a bounded
// actionable reason and a fresh recheck deadline.
func (s *OneShotReclaimService) blockMarkerTx(ctx context.Context, tx *sql.Tx, markerID, reasonCode, reason string) (int64, error) {
	return s.blockMarkerWithOutcomeTx(ctx, tx, markerID, reasonCode, reason, "")
}

// blockMarkerWithOutcomeTx moves a marker into cleanup_blocked while retaining
// the marker's terminal outcome when one is already known. A caller that has
// just completed the terminal-chain proof may provide that outcome so a
// drained-but-unsettled marker is not mislabeled as failed. The new deadline
// is capped at terminal_at + MaxRetentionDays, matching the hard-max recheck
// policy even when a stale reclaim_pending marker is blocked during
// reclamation.
func (s *OneShotReclaimService) blockMarkerWithOutcomeTx(ctx context.Context, tx *sql.Tx, markerID, reasonCode, reason, terminalOutcome string) (int64, error) {
	if terminalOutcome == "" {
		terminalOutcome = StateFailed
	}
	if terminalOutcome != StateSucceeded && terminalOutcome != StateFailed {
		return 0, fmt.Errorf("%w: invalid one-shot terminal outcome %q", ErrInvalidSpec, terminalOutcome)
	}
	deadline, err := persistence.New(tx).ComputeOneShotCleanupDeadline(ctx, persistence.ComputeOneShotCleanupDeadlineParams{
		RetentionDays:    int32(s.RetentionDays),
		MaxRetentionDays: int32(MaxRetentionDays),
		MarkerID:         markerID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrOneShotMarkerMissing
	}
	if err != nil {
		return 0, fmt.Errorf("compute one-shot cleanup deadline: %w", err)
	}
	result, err := persistence.New(tx).BlockOneShotMarker(ctx, persistence.BlockOneShotMarkerParams{
		TerminalOutcome:    sql.NullString{String: terminalOutcome, Valid: true},
		TerminalReasonCode: sql.NullString{String: boundedText(reasonCode, maxOneShotReasonCode), Valid: true},
		TerminalReason:     sql.NullString{String: boundedText(reason, maxOneShotReason), Valid: true},
		RetentionUntil:     sql.NullTime{Time: deadline, Valid: true},
		MarkerID:           markerID,
	})
	if err != nil {
		return 0, fmt.Errorf("block one-shot marker: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read blocked one-shot marker result: %w", err)
	}
	if affected != 1 {
		return 0, ErrOneShotMarkerMissing
	}
	return affected, nil
}

// reclaimDeadlineTx computes the reclamation deadline from PostgreSQL database
// time so batches stay consistent across server instances.
func (s *OneShotReclaimService) reclaimDeadlineTx(ctx context.Context, tx *sql.Tx) (time.Time, error) {
	deadline, err := persistence.New(tx).ComputeOneShotReclaimDeadline(ctx, int32(s.RetentionDays))
	if err != nil {
		return time.Time{}, fmt.Errorf("compute one-shot reclaim deadline: %w", err)
	}
	return deadline, nil
}

func boundedText(value string, max int) string {
	value = trimSafe(value)
	if len(value) > max {
		return value[:max]
	}
	return value
}

// trimSafe strips control characters and bounds whitespace so persisted
// reasons stay single-line, bounded, and redacted-safe.
func trimSafe(value string) string {
	out := make([]rune, 0, len(value))
	for _, r := range value {
		if r == '\n' || r == '\r' || r == '\x00' {
			out = append(out, ' ')
			continue
		}
		out = append(out, r)
	}
	start, end := 0, len(out)
	for start < end && out[start] == ' ' {
		start++
	}
	for end > start && out[end-1] == ' ' {
		end--
	}
	return string(out[start:end])
}
