package application

import (
	"context"
	"database/sql"
	"fmt"
)

// readPlacementHandoffsApplication projects the durable post-completion
// placement obligations of one subscription's completed file operations. It is
// a plain bounded read: the public stage state (`admitted` becomes the
// user-facing `completed`), the stable reason of a failed or retrying stage,
// the attempt budget, and the due time of the live continuation. No WorkItem
// or other internal identity is exposed, and the read never mutates state.
func (s *Service) readPlacementHandoffsApplication(ctx context.Context, subscriptionID string) ([]PlacementHandoff, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT obligation.file_operation_id,
       CASE WHEN obligation.state = 'admitted' THEN 'completed' ELSE obligation.state END,
       obligation.reason_code, obligation.attempts, obligation.max_attempts,
       obligation.updated_at,
       CASE WHEN obligation.state = 'waiting_retry' THEN pending.available_at ELSE NULL END
FROM placement_obligations obligation
JOIN file_operations op ON op.id = obligation.file_operation_id
JOIN artifacts a ON a.id = op.artifact_id
JOIN download_tasks t ON t.id = a.download_task_id
JOIN releases r ON r.id = t.release_id
JOIN rss_sources src ON src.id = r.rss_source_id
LEFT JOIN LATERAL (
	SELECT item.available_at
	FROM work_items item
	WHERE item.owner_type = 'file_operation' AND item.owner_id = obligation.file_operation_id
	  AND item.kind = 'file_execute' AND item.state = 'available'
	ORDER BY item.available_at, item.id
	LIMIT 1
) pending ON TRUE
WHERE src.subscription_id = $1
ORDER BY obligation.created_at, obligation.file_operation_id`, subscriptionID)
	if err != nil {
		return nil, fmt.Errorf("read placement handoffs: %w", err)
	}
	defer rows.Close()
	result := make([]PlacementHandoff, 0)
	for rows.Next() {
		var handoff PlacementHandoff
		var reasonCode sql.NullString
		var nextRetryAt sql.NullTime
		if err := rows.Scan(&handoff.FileOperationID, &handoff.State, &reasonCode,
			&handoff.Attempts, &handoff.AttemptBudget, &handoff.UpdatedAt, &nextRetryAt); err != nil {
			return nil, fmt.Errorf("scan placement handoff: %w", err)
		}
		handoff.ReasonCode = nullableStringPointer(reasonCode)
		if nextRetryAt.Valid {
			value := nextRetryAt.Time
			handoff.NextRetryAt = &value
		}
		result = append(result, handoff)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate placement handoffs: %w", err)
	}
	return result, nil
}
