package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"bansoko/internal/workitems"
)

// QBSyncDispatchResult summarizes one explicit polling dispatch. It is a
// bounded target-enqueue result, not a product history or a periodic successor.
type QBSyncDispatchResult struct {
	Scanned int
	Queued  int
	Merged  int
	Skipped int
}

// DispatchQBSync scans provider-backed DownloadTasks and creates or merges one
// live qb_sync WorkItem per task in one transaction. It does not hold task row
// locks while creating WorkItems; the live owner/kind unique index resolves
// concurrent dispatch races. A caller may invoke this from a fixed automation
// dispatcher or a test; it does not run a loop and it never waits for qBittorrent.
//
// Finite one-shot tasks are excluded: there is no autonomous one-shot
// dispatcher, so their qb_sync continuation is created only inside the
// marker-locked typed submission settlement transaction.
func (s *Service) DispatchQBSync(ctx context.Context) (QBSyncDispatchResult, error) {
	if s == nil || s.db == nil || s.workItems == nil {
		return QBSyncDispatchResult{}, errors.New("qB sync dispatcher is not configured")
	}
	if ctx == nil {
		return QBSyncDispatchResult{}, errors.New("qB sync dispatcher context is nil")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return QBSyncDispatchResult{}, fmt.Errorf("begin qB sync dispatch: %w", err)
	}
	defer tx.Rollback()

	type syncTarget struct {
		taskID         string
		correlationRaw []byte
		version        int64
		effectKey      string
		pollOrdinal    int
	}
	rows, err := tx.QueryContext(ctx, `
SELECT task.id, task.provider_correlation, task.version, task.idempotency_key, task.next_poll_ordinal
FROM download_tasks task
JOIN releases release ON release.id = task.release_id
JOIN rss_sources source ON source.id = release.rss_source_id
JOIN subscriptions subscription ON subscription.id = source.subscription_id
JOIN managed_anime lifecycle
  ON lifecycle.id = subscription.managed_anime_id
 AND lifecycle.lifecycle_state = 'active'
WHERE task.state IN ('queued', 'submitting', 'downloading', 'waiting_retry')
  AND task.provider_correlation IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM one_shot_acquisitions m WHERE m.download_task_id = task.id
  )
ORDER BY task.updated_at, task.id`)
	if err != nil {
		return QBSyncDispatchResult{}, fmt.Errorf("scan qB sync targets: %w", err)
	}
	targets := make([]syncTarget, 0)
	for rows.Next() {
		var target syncTarget
		if err := rows.Scan(&target.taskID, &target.correlationRaw, &target.version, &target.effectKey, &target.pollOrdinal); err != nil {
			rows.Close()
			return QBSyncDispatchResult{}, fmt.Errorf("scan qB sync target: %w", err)
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return QBSyncDispatchResult{}, fmt.Errorf("iterate qB sync targets: %w", err)
	}
	if err := rows.Close(); err != nil {
		return QBSyncDispatchResult{}, fmt.Errorf("close qB sync targets: %w", err)
	}

	result := QBSyncDispatchResult{Scanned: len(targets)}
	for _, target := range targets {
		var correlation providerCorrelation
		if err := json.Unmarshal(target.correlationRaw, &correlation); err != nil || strings.TrimSpace(correlation.ExternalID) == "" {
			result.Skipped++
			continue
		}
		var live bool
		if err := tx.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM work_items
  WHERE owner_type = $1 AND owner_id = $2 AND kind = $3
    AND state IN ('available', 'leased')
)`, workitems.OwnerDownloadTask, target.taskID, workitems.KindQBSync).Scan(&live); err != nil {
			return QBSyncDispatchResult{}, fmt.Errorf("check qB sync target: %w", err)
		}
		if live {
			result.Merged++
			continue
		}
		created, err := createQBSyncWorkItem(ctx, tx, s.workItems, workitems.WorkItemSpec{
			Owner:                workitems.OwnerRef{Type: workitems.OwnerDownloadTask, ID: target.taskID},
			Kind:                 workitems.KindQBSync,
			IdempotencyKey:       target.effectKey,
			InputSnapshot:        json.RawMessage(fmt.Sprintf(`{"trigger":"qb_sync_dispatch","poll_ordinal":%d}`, target.pollOrdinal)),
			MaxAttempts:          defaultMaxAttempts,
			ExpectedOwnerVersion: target.version,
		})
		if errors.Is(err, workitems.ErrLiveItemConflict) {
			result.Merged++
			continue
		}
		if err != nil {
			return QBSyncDispatchResult{}, fmt.Errorf("create qB sync work item: %w", err)
		}
		if created {
			result.Queued++
		} else {
			result.Merged++
		}
	}
	if err := tx.Commit(); err != nil {
		return QBSyncDispatchResult{}, fmt.Errorf("commit qB sync dispatch: %w", err)
	}
	return result, nil
}

// createQBSyncWorkItem isolates the live-unique conflict for one target. A
// PostgreSQL unique violation aborts the current transaction until it is
// rolled back to a savepoint; without this boundary, one concurrent dispatch
// race would discard all other targets in the same dispatch transaction.
func createQBSyncWorkItem(ctx context.Context, tx *sql.Tx, store *workitems.Store, spec workitems.WorkItemSpec) (bool, error) {
	const savepoint = "qb_sync_dispatch_item"
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+savepoint); err != nil {
		return false, fmt.Errorf("create qB sync savepoint: %w", err)
	}

	_, createErr := store.Create(ctx, tx, spec)
	if createErr != nil {
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+savepoint); err != nil {
			return false, fmt.Errorf("rollback qB sync savepoint after create error: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+savepoint); err != nil {
			return false, fmt.Errorf("release qB sync savepoint after create error: %w", err)
		}
		if errors.Is(createErr, workitems.ErrLiveItemConflict) {
			return false, nil
		}
		return false, createErr
	}
	if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+savepoint); err != nil {
		return false, fmt.Errorf("release qB sync savepoint: %w", err)
	}
	return true, nil
}
