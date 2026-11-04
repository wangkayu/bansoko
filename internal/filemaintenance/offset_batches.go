package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"bansoko/internal/persistence/txscope"
)

// scheduleOffsetBatches finds complete active-subscription batches on every
// tick, including batches whose last change recorded before a stop or restart.
func (s *ScanScheduler) scheduleOffsetBatches(ctx context.Context) error {
	if s.offsetCandidates == nil {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT sub.id, sub.managed_anime_id
FROM subscriptions sub
WHERE sub.state = 'active'
  AND EXISTS (
      SELECT 1 FROM offset_alignment_changes ch
      JOIN rss_sources src ON src.id = ch.rss_source_id
      WHERE src.subscription_id = sub.id AND ch.state = 'recorded' AND ch.candidates_scheduled_at IS NULL)
  AND NOT EXISTS (
      SELECT 1 FROM offset_alignment_changes ch
      JOIN rss_sources src ON src.id = ch.rss_source_id
      WHERE src.subscription_id = sub.id AND ch.state IN ('comparing', 'aligning'))
ORDER BY sub.id
LIMIT 50`)
	if err != nil {
		return fmt.Errorf("read complete offset batches: %w", err)
	}
	type batch struct{ subscriptionID, animeID string }
	var batches []batch
	for rows.Next() {
		var item batch
		if err := rows.Scan(&item.subscriptionID, &item.animeID); err != nil {
			rows.Close()
			return err
		}
		batches = append(batches, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range batches {
		if err := s.scheduleOffsetBatch(ctx, item.subscriptionID, item.animeID); err != nil {
			return err
		}
	}
	return s.recoverOffsetRetries(ctx)
}

// recoverOffsetRetries covers qualifications persisted before a stop or a
// process restart. The batch marker alone cannot recover a completed candidate
// whose evaluation had already been dispatched when the subscription stopped.
func (s *ScanScheduler) recoverOffsetRetries(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT sub.id
FROM subscriptions sub
JOIN rss_sources src ON src.subscription_id = sub.id
JOIN releases r ON r.rss_source_id = src.id
WHERE sub.state = 'active' AND r.competition_retry_key LIKE 'offset:%'
  AND r.verdict <> 'awaiting_competition'
  AND NOT EXISTS (
      SELECT 1 FROM work_items wi
      WHERE wi.owner_type = 'release' AND wi.owner_id = r.id
        AND wi.kind = 'release_evaluate' AND wi.state IN ('available', 'leased'))
ORDER BY sub.id
LIMIT 50`)
	if err != nil {
		return fmt.Errorf("read offset retries to recover: %w", err)
	}
	var subscriptionIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		subscriptionIDs = append(subscriptionIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, subscriptionID := range subscriptionIDs {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		var state string
		err = tx.QueryRowContext(ctx, `SELECT state FROM subscriptions WHERE id = $1 FOR UPDATE`, subscriptionID).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			_ = tx.Rollback()
			continue
		}
		if err == nil && state == "active" {
			err = s.offsetCandidates.ScheduleOffsetRetryRecoveryTx(ctx, txscope.Wrap(tx), subscriptionID)
		}
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if err != nil {
			return fmt.Errorf("recover offset retries: %w", err)
		}
	}
	return nil
}

// scheduleOffsetBatch serializes with new offset edits through the
// subscription row. A change inserted before this lock joins the batch; one
// inserted after it necessarily starts the next batch.
func (s *ScanScheduler) scheduleOffsetBatch(ctx context.Context, subscriptionID, animeID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM subscriptions WHERE id = $1 FOR UPDATE`, subscriptionID).Scan(&state); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("lock offset batch subscription: %w", err)
	}
	if state != "active" {
		return nil
	}
	var unfinished bool
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM offset_alignment_changes ch
    JOIN rss_sources src ON src.id = ch.rss_source_id
    WHERE src.subscription_id = $1 AND ch.state IN ('comparing', 'aligning'))`, subscriptionID).Scan(&unfinished); err != nil {
		return err
	}
	if unfinished {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `
SELECT ch.id
FROM offset_alignment_changes ch
JOIN rss_sources src ON src.id = ch.rss_source_id
WHERE src.subscription_id = $1 AND ch.state = 'recorded' AND ch.candidates_scheduled_at IS NULL
ORDER BY ch.created_at, ch.id
FOR UPDATE OF ch`, subscriptionID)
	if err != nil {
		return fmt.Errorf("lock offset batch changes: %w", err)
	}
	var changeIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		changeIDs = append(changeIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(changeIDs) == 0 {
		return nil
	}
	if err := s.offsetCandidates.ScheduleOffsetReconciliationTx(ctx, txscope.Wrap(tx), animeID, subscriptionID, changeIDs); err != nil {
		return fmt.Errorf("schedule offset batch candidates: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE offset_alignment_changes ch
SET candidates_scheduled_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
FROM rss_sources src
WHERE src.id = ch.rss_source_id AND src.subscription_id = $1
  AND ch.state = 'recorded' AND ch.candidates_scheduled_at IS NULL`, subscriptionID)
	if err != nil {
		return fmt.Errorf("mark offset batch scheduled: %w", err)
	}
	marked, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if marked != int64(len(changeIDs)) {
		return fmt.Errorf("offset batch changed during dispatch: locked %d, marked %d", len(changeIDs), marked)
	}
	return tx.Commit()
}
