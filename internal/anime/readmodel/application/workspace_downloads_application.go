package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/lib/pq"
)

type workspaceProviderCorrelationJSON struct {
	ExternalID *string `json:"external_id"`
	InfoHash   *string `json:"info_hash"`
	StableTag  *string `json:"stable_tag"`
}

func (s *Service) readDownloadTasksApplication(ctx context.Context, subscriptionID string) ([]WorkspaceDownloadTask, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT t.id, t.release_id, t.provider_correlation, t.frozen_input, t.frozen_policy,
       t.state, t.idempotency_key, t.failure_code, t.failure_reason, t.version, fw.id
FROM download_tasks t
JOIN releases r ON r.id = t.release_id
JOIN rss_sources src ON src.id = r.rss_source_id
LEFT JOIN LATERAL (
  SELECT w.id FROM work_item_outcomes w
  WHERE w.owner_type = 'download_task' AND w.owner_id = t.id AND w.state = 'failed'
  ORDER BY w.terminal_at DESC, w.id DESC
  LIMIT 1
) fw ON TRUE
WHERE src.subscription_id = $1
ORDER BY t.created_at, t.id`, subscriptionID)
	if err != nil {
		return nil, fmt.Errorf("read download tasks: %w", err)
	}
	defer rows.Close()
	result := make([]WorkspaceDownloadTask, 0)
	for rows.Next() {
		var task WorkspaceDownloadTask
		var correlation, frozenInput, frozenPolicy []byte
		var failureCode, failureReason, failedWorkItemID sql.NullString
		if err := rows.Scan(&task.ID, &task.ReleaseID, &correlation, &frozenInput, &frozenPolicy, &task.State, &task.IdempotencyKey, &failureCode, &failureReason, &task.Version, &failedWorkItemID); err != nil {
			return nil, fmt.Errorf("scan download task: %w", err)
		}
		if correlation != nil {
			var value workspaceProviderCorrelationJSON
			if err := json.Unmarshal(correlation, &value); err == nil {
				task.ProviderCorrelation = &WorkspaceProviderCorrelation{ExternalID: cloneStringPointer(value.ExternalID), InfoHash: cloneStringPointer(value.InfoHash), StableTag: cloneStringPointer(value.StableTag)}
			}
		}
		if err := json.Unmarshal(frozenInput, &task.FrozenInput); err != nil {
			return nil, fmt.Errorf("decode frozen input: %w", err)
		}
		if err := json.Unmarshal(frozenPolicy, &task.FrozenPolicy); err != nil {
			return nil, fmt.Errorf("decode frozen policy: %w", err)
		}
		task.FailureCode = cloneNullableString(failureCode)
		task.FailureReason = cloneNullableString(failureReason)
		task.FailedWorkItemID = cloneNullableString(failedWorkItemID)
		result = append(result, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate download tasks: %w", err)
	}
	return result, nil
}

func (s *Service) readArtifactsApplication(ctx context.Context, subscriptionID string) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT a.id, a.download_task_id, a.fingerprint_snapshot, a.state, a.failure_code, a.failure_reason, a.version, fw.id
FROM artifacts a
JOIN download_tasks t ON t.id = a.download_task_id
JOIN releases r ON r.id = t.release_id
JOIN rss_sources src ON src.id = r.rss_source_id
LEFT JOIN LATERAL (
  SELECT w.id FROM work_item_outcomes w
  WHERE w.owner_type = 'artifact' AND w.owner_id = a.id AND w.state = 'failed'
  ORDER BY w.terminal_at DESC, w.id DESC
  LIMIT 1
) fw ON TRUE
WHERE src.subscription_id = $1
ORDER BY a.created_at, a.id`, subscriptionID)
	if err != nil {
		return nil, fmt.Errorf("read artifacts: %w", err)
	}
	defer rows.Close()
	result := make([]Artifact, 0)
	for rows.Next() {
		var artifact Artifact
		var fingerprints []byte
		var failureCode, failureReason, failedWorkItemID sql.NullString
		if err := rows.Scan(&artifact.ID, &artifact.DownloadTaskID, &fingerprints, &artifact.State, &failureCode, &failureReason, &artifact.Version, &failedWorkItemID); err != nil {
			return nil, fmt.Errorf("scan artifact: %w", err)
		}
		artifact.Files = make([]ArtifactFile, 0)
		artifact.Fingerprints = make([]Fingerprint, 0)
		var wrapper struct {
			Fingerprints []Fingerprint `json:"fingerprints"`
		}
		if err := json.Unmarshal(fingerprints, &wrapper); err == nil && wrapper.Fingerprints != nil {
			artifact.Fingerprints = cloneFingerprints(wrapper.Fingerprints)
		}
		artifact.FailureCode = cloneNullableString(failureCode)
		artifact.FailureReason = cloneNullableString(failureReason)
		artifact.FailedWorkItemID = cloneNullableString(failedWorkItemID)
		result = append(result, artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifacts: %w", err)
	}
	rows.Close()
	if len(result) == 0 {
		return result, nil
	}
	ids := make([]string, len(result))
	for i := range result {
		ids[i] = result[i].ID
	}
	files, err := s.readArtifactFilesApplication(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range result {
		if list, ok := files[result[i].ID]; ok {
			result[i].Files = list
		}
	}
	return result, nil
}

// readArtifactFilesApplication reads the files of several artifacts with one
// query, grouped by artifact and ordered by file_ref.
func (s *Service) readArtifactFilesApplication(ctx context.Context, artifactIDs []string) (map[string][]ArtifactFile, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT artifact_id, file_ref, relative_path, media_kind, fingerprint,
       subtitle_binding_state, bound_video_file_ref, subtitle_binding_snapshot
FROM artifact_files
WHERE artifact_id = ANY($1::text[])
ORDER BY artifact_id, file_ref`, pq.Array(artifactIDs))
	if err != nil {
		return nil, fmt.Errorf("read artifact files: %w", err)
	}
	defer rows.Close()
	result := make(map[string][]ArtifactFile, len(artifactIDs))
	for rows.Next() {
		var artifactID string
		var file ArtifactFile
		var fingerprint, bindingSnapshot []byte
		var bindingState string
		var boundVideoFileRef sql.NullString
		if err := rows.Scan(&artifactID, &file.Ref, &file.Path, &file.MediaKind, &fingerprint, &bindingState, &boundVideoFileRef, &bindingSnapshot); err != nil {
			return nil, fmt.Errorf("scan artifact file: %w", err)
		}
		if err := json.Unmarshal(fingerprint, &file.Fingerprint); err != nil {
			return nil, fmt.Errorf("decode artifact file fingerprint: %w", err)
		}
		binding, err := decodeArtifactFileBindingApplication(bindingState, boundVideoFileRef, bindingSnapshot)
		if err != nil {
			return nil, fmt.Errorf("decode artifact file subtitle binding: %w", err)
		}
		file.SubtitleBinding = binding
		result[artifactID] = append(result[artifactID], file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifact files: %w", err)
	}
	return result, nil
}

func cloneFingerprints(value []Fingerprint) []Fingerprint {
	result := make([]Fingerprint, len(value))
	copy(result, value)
	return result
}

func decodeArtifactFileBindingApplication(state string, boundRef sql.NullString, rawSnapshot []byte) (*ArtifactFileSubtitleBinding, error) {
	binding := &ArtifactFileSubtitleBinding{State: state, Diagnostics: make([]NamingDiagnostic, 0)}
	if boundRef.Valid {
		binding.BoundVideoFileRef = cloneStringPointer(&boundRef.String)
	}
	if len(rawSnapshot) > 0 {
		var snapshot struct {
			Method      string   `json:"method"`
			Confidence  *float64 `json:"confidence"`
			Diagnostics []struct {
				Code    string  `json:"code"`
				Message string  `json:"message"`
				Path    *string `json:"path"`
			} `json:"diagnostics"`
		}
		if err := json.Unmarshal(rawSnapshot, &snapshot); err != nil {
			return nil, err
		}
		if snapshot.Method != "" {
			binding.Method = cloneStringPointer(&snapshot.Method)
		}
		if snapshot.Confidence != nil {
			value := float32(*snapshot.Confidence)
			binding.Confidence = &value
		}
		for _, diagnostic := range snapshot.Diagnostics {
			binding.Diagnostics = append(binding.Diagnostics, NamingDiagnostic{Code: diagnostic.Code, Message: diagnostic.Message, Path: cloneStringPointer(diagnostic.Path)})
		}
	}
	return binding, nil
}
