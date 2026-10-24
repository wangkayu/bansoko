package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

func (s *Service) readFileOperationsApplication(ctx context.Context, subscriptionID string) ([]FileOperation, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT op.id, op.artifact_id, af.file_ref, op.operation, op.source_path, op.target_path,
       op.source_fingerprint, op.target_fingerprint, op.expected_size_bytes,
       op.observed_target_size_bytes, op.intent, op.collision_policy,
       op.frozen_render_snapshot, op.frozen_config_snapshot, op.state, op.observed_at,
       op.failure_code, op.failure_reason, op.idempotency_key, op.version, fw.id
FROM file_operations op
JOIN artifact_files af ON af.id = op.artifact_file_id AND af.artifact_id = op.artifact_id
JOIN artifacts a ON a.id = op.artifact_id
JOIN download_tasks t ON t.id = a.download_task_id
JOIN releases r ON r.id = t.release_id
JOIN rss_sources src ON src.id = r.rss_source_id
LEFT JOIN LATERAL (
  SELECT w.id FROM work_item_outcomes w
  WHERE w.owner_type = 'file_operation' AND w.owner_id = op.id AND w.state = 'failed'
  ORDER BY w.terminal_at DESC, w.id DESC
  LIMIT 1
) fw ON TRUE
WHERE src.subscription_id = $1
ORDER BY op.created_at, op.id`, subscriptionID)
	if err != nil {
		return nil, fmt.Errorf("read file operations: %w", err)
	}
	defer rows.Close()
	result := make([]FileOperation, 0)
	for rows.Next() {
		var operation FileOperation
		var sourceFingerprint, targetFingerprint, renderSnapshot, configSnapshot []byte
		var observedAt sql.NullTime
		var expectedSize, observedTargetSize sql.NullInt64
		var failureCode, failureReason, failedWorkItemID sql.NullString
		var intent, collisionPolicy string
		if err := rows.Scan(&operation.ID, &operation.ArtifactID, &operation.ArtifactFileRef, &operation.Operation, &operation.SourcePath, &operation.TargetPath,
			&sourceFingerprint, &targetFingerprint, &expectedSize, &observedTargetSize, &intent, &collisionPolicy,
			&renderSnapshot, &configSnapshot, &operation.State, &observedAt, &failureCode, &failureReason, &operation.IdempotencyKey, &operation.Version, &failedWorkItemID); err != nil {
			return nil, fmt.Errorf("scan file operation: %w", err)
		}
		operation.CollisionPolicy = workspaceCollisionPolicy(collisionPolicy)
		operation.Intent = cloneStringPointer(&intent)
		if expectedSize.Valid {
			value := expectedSize.Int64
			operation.ExpectedSizeBytes = &value
		}
		if observedTargetSize.Valid {
			value := observedTargetSize.Int64
			operation.ObservedTargetSizeBytes = &value
		}
		if err := json.Unmarshal(sourceFingerprint, &operation.SourceFingerprint); err != nil {
			return nil, fmt.Errorf("decode source fingerprint: %w", err)
		}
		if targetFingerprint != nil {
			operation.TargetFingerprint = decodeTargetFingerprint(targetFingerprint, expectedSize)
		}
		if err := json.Unmarshal(renderSnapshot, &operation.FrozenRenderSnapshot); err != nil {
			return nil, fmt.Errorf("decode frozen render snapshot: %w", err)
		}
		if err := json.Unmarshal(configSnapshot, &operation.FrozenConfigSnapshot); err != nil {
			return nil, fmt.Errorf("decode frozen config snapshot: %w", err)
		}
		if observedAt.Valid {
			value := observedAt.Time
			operation.ObservedAt = &value
		}
		operation.FailureCode = cloneNullableString(failureCode)
		operation.FailureReason = cloneNullableString(failureReason)
		operation.FailedWorkItemID = cloneNullableString(failedWorkItemID)
		result = append(result, operation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate file operations: %w", err)
	}
	return result, nil
}

// decodeTargetFingerprint projects a stored target fingerprint. Typed
// completions before migration 186 stored no size_bytes; the published bytes
// equal the frozen expected size, which fills the gap.
func decodeTargetFingerprint(raw []byte, expectedSize sql.NullInt64) *Fingerprint {
	var value Fingerprint
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	if !fingerprintHasSize(raw) && expectedSize.Valid {
		value.SizeBytes = expectedSize.Int64
	}
	return &value
}

// fingerprintHasSize reports whether a stored fingerprint carries size_bytes.
func fingerprintHasSize(raw []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	_, ok := fields["size_bytes"]
	return ok
}
