package eventactions

import (
	"context"
	"time"
)

// RecordReceipt describes only the script attached to the current record result.
// An omitted FileRef denotes the download submission stage.
type RecordReceipt struct {
	FileRef    *string   `json:"file_ref,omitempty"`
	Script     string    `json:"script"`
	StartedAt  time.Time `json:"started_at"`
	Summary    *string   `json:"summary,omitempty"`
	ExitCode   *int      `json:"exit_code,omitempty"`
	HTTPStatus *int      `json:"http_status,omitempty"`
}

func (s *Service) RecordReceipts(ctx context.Context, acquisitionID string) ([]RecordReceipt, error) {
	// Match the manifest's current delivery and the exact operation version:
	// import retry clears the delivery and can reuse the same operation ID.
	rows, err := s.db.QueryContext(ctx, `
WITH candidates AS (
    SELECT a.id, NULL::text AS file_ref, a.script, a.process_started_at, a.script_summary, a.exit_code, a.http_status, a.source_version, a.occurred_at
    FROM event_action_attempts a
    JOIN acquisitions r ON r.id=a.acquisition_id AND r.record_state='full'
    JOIN download_tasks d ON d.id=a.source_id AND d.acquisition_id=r.id
    WHERE a.acquisition_id=$1 AND a.kind='download_submitted'
    UNION ALL
    SELECT a.id, f.file_ref, a.script, a.process_started_at, a.script_summary, a.exit_code, a.http_status, a.source_version, a.occurred_at
    FROM event_action_attempts a
    JOIN acquisitions r ON r.id=a.acquisition_id AND r.record_state='full'
    JOIN file_operations o ON o.id=a.source_id AND o.acquisition_id=r.id AND o.version=a.source_version
    JOIN acquisition_files f ON f.acquisition_id=r.id AND f.file_ref=o.file_ref AND f.operation_id=o.id
    WHERE a.acquisition_id=$1
      AND ((a.kind='file_operation_completed' AND o.state='completed' AND f.outcome='succeeded')
        OR (a.kind='file_operation_failed' AND o.state='failed' AND f.outcome='failed'))
), latest AS (
    SELECT DISTINCT ON (file_ref) * FROM candidates
    ORDER BY file_ref, source_version DESC, occurred_at DESC, id DESC
)
SELECT file_ref, script, process_started_at, script_summary,
       CASE WHEN script_summary IS NOT NULL THEN exit_code END, http_status
FROM latest WHERE process_started_at IS NOT NULL`, acquisitionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RecordReceipt, 0)
	for rows.Next() {
		var item RecordReceipt
		if err := rows.Scan(&item.FileRef, &item.Script, &item.StartedAt, &item.Summary, &item.ExitCode, &item.HTTPStatus); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
