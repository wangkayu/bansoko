package application

import (
	animeapplication "bansoko/internal/anime/application"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// OneShotAcquisitionView is the transport-neutral read model for a finite
// one-shot acquisition. It contains durable owner facts and retained terminal
// evidence, but no WorkItem lease/fence data, secrets, raw payloads, or private paths.
type OneShotAcquisitionView struct {
	Artifact           *OneShotArtifact        `json:"artifact"`
	InputSnapshot      *OneShotInputSnapshot   `json:"input_snapshot"`
	CreatedAt          time.Time               `json:"created_at"`
	DownloadTask       *OneShotDownloadTask    `json:"download_task"`
	DownloadTaskID     *string                 `json:"download_task_id"`
	FileOperation      *OneShotFileOperation   `json:"file_operation"`
	ManagedAnimeID     string                  `json:"managed_anime_id"`
	OneShotID          string                  `json:"one_shot_id"`
	ReleaseID          string                  `json:"release_id"`
	RSSSourceID        string                  `json:"rss_source_id"`
	State              OneShotAcquisitionState `json:"state"`
	SubscriptionID     string                  `json:"subscription_id"`
	TerminalOutcome    *OneShotTerminalOutcome `json:"terminal_outcome"`
	TerminalReason     *string                 `json:"terminal_reason"`
	TerminalReasonCode *string                 `json:"terminal_reason_code"`
	UpdatedAt          time.Time               `json:"updated_at"`
	// CurrentAttemptID names the immutable execution attempt whose evidence
	// the terminal projection mirrors. FailedStage is the first failed stage of
	// the anchored chain (download/artifact/planning/file_execution).
	CurrentAttemptID *string `json:"current_attempt_id"`
	FailedStage      *string `json:"failed_stage"`
	// Version is the current marker version.
	Version int64 `json:"version"`
}

// oneShotFailedStage is the ordered first-failure derivation over the anchored
// owner chain: the file execution outcome, then the verified artifact fact,
// then the download task itself.
func oneShotFailedStage(view OneShotAcquisitionView, planFailed bool) *string {
	if view.FileOperation != nil && view.FileOperation.State == "failed" {
		stage := "file_execution"
		return &stage
	}
	if planFailed {
		stage := "planning"
		return &stage
	}
	if view.Artifact != nil && view.Artifact.State == "failed" {
		stage := "artifact"
		return &stage
	}
	if view.DownloadTask != nil && view.DownloadTask.State == "failed" {
		stage := "download"
		return &stage
	}
	return nil
}

func decodeOneShotInputSnapshot(raw json.RawMessage) *OneShotInputSnapshot {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var snapshot OneShotInputSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil
	}
	return &snapshot
}

// OneShotAcquisitionState is the marker state exposed by the one-shot read
// surface. Values are projected to the HTTP enum at the transport boundary.
type OneShotAcquisitionState string

const (
	OneShotAcquisitionStateRunning          OneShotAcquisitionState = "running"
	OneShotAcquisitionStateRetentionPending OneShotAcquisitionState = "retention_pending"
	OneShotAcquisitionStateCleanupBlocked   OneShotAcquisitionState = "cleanup_blocked"
	OneShotAcquisitionStateReclaiming       OneShotAcquisitionState = "reclaiming"
	OneShotAcquisitionStateCleaned          OneShotAcquisitionState = "cleaned"
)

// OneShotTerminalOutcome is the terminal result retained by a marker.
type OneShotTerminalOutcome string

const (
	OneShotTerminalOutcomeFailed    OneShotTerminalOutcome = "failed"
	OneShotTerminalOutcomeSucceeded OneShotTerminalOutcome = "succeeded"
)

type OneShotProviderCorrelation struct {
	ExternalID *string `json:"external_id,omitempty"`
	InfoHash   *string `json:"info_hash,omitempty"`
	StableTag  *string `json:"stable_tag,omitempty"`
}

type OneShotDownloadTask struct {
	FailedWorkItemID    *string                     `json:"failed_work_item_id,omitempty"`
	FailureCode         *string                     `json:"failure_code,omitempty"`
	FailureReason       *string                     `json:"failure_reason,omitempty"`
	FrozenInput         OneShotFrozenDownloadInput  `json:"frozen_input"`
	FrozenPolicy        OneShotFrozenPolicySnapshot `json:"frozen_policy"`
	ID                  string                      `json:"id"`
	IdempotencyKey      string                      `json:"idempotency_key"`
	ProviderCorrelation *OneShotProviderCorrelation `json:"provider_correlation,omitempty"`
	ReleaseID           string                      `json:"release_id"`
	State               string                      `json:"state"`
	Version             int64                       `json:"version"`
}

type OneShotArtifact struct {
	DownloadTaskID   string                `json:"download_task_id"`
	FailedWorkItemID *string               `json:"failed_work_item_id,omitempty"`
	FailureCode      *string               `json:"failure_code,omitempty"`
	FailureReason    *string               `json:"failure_reason,omitempty"`
	Files            []OneShotArtifactFile `json:"files"`
	Fingerprints     []OneShotFingerprint  `json:"fingerprints"`
	ID               string                `json:"id"`
	State            string                `json:"state"`
	Version          int64                 `json:"version"`
}

type OneShotArtifactFile struct {
	Fingerprint     OneShotFingerprint      `json:"fingerprint"`
	MediaKind       string                  `json:"media_kind"`
	Path            string                  `json:"path"`
	Ref             string                  `json:"ref"`
	SubtitleBinding *OneShotSubtitleBinding `json:"subtitle_binding,omitempty"`
}

type OneShotSubtitleBinding struct {
	BoundVideoFileRef *string                   `json:"bound_video_file_ref,omitempty"`
	Confidence        *float32                  `json:"confidence,omitempty"`
	Diagnostics       []OneShotNamingDiagnostic `json:"diagnostics"`
	Method            *string                   `json:"method,omitempty"`
	State             string                    `json:"state"`
}

type OneShotNamingDiagnostic struct {
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Path    *string `json:"path,omitempty"`
}

type OneShotFingerprint struct {
	Algorithm string `json:"algorithm"`
	SizeBytes int64  `json:"size_bytes"`
	Value     string `json:"value"`
}

type OneShotFrozenDownloadInput = animeapplication.FrozenDownloadInput

type OneShotFrozenPolicySnapshot = animeapplication.FrozenPolicySnapshot

type OneShotFrozenFileConfigSnapshot struct {
	RulesVersion  int64  `json:"rules_version"`
	SafetyVersion string `json:"safety_version"`
}

type OneShotFrozenRenderSnapshot struct {
	Directory       string `json:"directory"`
	Filename        string `json:"filename"`
	RendererVersion string `json:"renderer_version"`
}

type OneShotFileOperation struct {
	ArtifactFileRef         string                          `json:"artifact_file_ref"`
	ArtifactID              string                          `json:"artifact_id"`
	CollisionPolicy         string                          `json:"collision_policy"`
	ExpectedSizeBytes       *int64                          `json:"expected_size_bytes"`
	FailedWorkItemID        *string                         `json:"failed_work_item_id,omitempty"`
	FailureCode             *string                         `json:"failure_code,omitempty"`
	FailureReason           *string                         `json:"failure_reason,omitempty"`
	FrozenConfigSnapshot    OneShotFrozenFileConfigSnapshot `json:"frozen_config_snapshot"`
	FrozenRenderSnapshot    OneShotFrozenRenderSnapshot     `json:"frozen_render_snapshot"`
	ID                      string                          `json:"id"`
	IdempotencyKey          string                          `json:"idempotency_key"`
	Intent                  *string                         `json:"intent,omitempty"`
	ObservedAt              *time.Time                      `json:"observed_at,omitempty"`
	ObservedTargetSizeBytes *int64                          `json:"observed_target_size_bytes"`
	Operation               string                          `json:"operation"`
	SourceFingerprint       OneShotFingerprint              `json:"source_fingerprint"`
	SourcePath              string                          `json:"source_path"`
	State                   string                          `json:"state"`
	TargetFingerprint       *OneShotFingerprint             `json:"target_fingerprint,omitempty"`
	TargetPath              string                          `json:"target_path"`
	Version                 int64                           `json:"version"`
}

func (s *Service) readOneShotDownloadTasks(ctx context.Context, subscriptionID string) ([]OneShotDownloadTask, error) {
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
	result := []OneShotDownloadTask{}
	for rows.Next() {
		var task OneShotDownloadTask
		var correlation, frozenInput, frozenPolicy []byte
		var failureCode, failureReason, failedWorkItemID sql.NullString
		if err := rows.Scan(&task.ID, &task.ReleaseID, &correlation, &frozenInput, &frozenPolicy,
			&task.State, &task.IdempotencyKey, &failureCode, &failureReason, &task.Version, &failedWorkItemID); err != nil {
			return nil, fmt.Errorf("scan download task: %w", err)
		}
		if correlation != nil {
			var value OneShotProviderCorrelation
			if err := json.Unmarshal(correlation, &value); err == nil {
				task.ProviderCorrelation = &value
			}
		}
		if err := json.Unmarshal(frozenInput, &task.FrozenInput); err != nil {
			return nil, fmt.Errorf("decode frozen input: %w", err)
		}
		if err := json.Unmarshal(frozenPolicy, &task.FrozenPolicy); err != nil {
			return nil, fmt.Errorf("decode frozen policy: %w", err)
		}
		if failureCode.Valid {
			task.FailureCode = &failureCode.String
		}
		if failureReason.Valid {
			task.FailureReason = &failureReason.String
		}
		if failedWorkItemID.Valid {
			task.FailedWorkItemID = &failedWorkItemID.String
		}
		result = append(result, task)
	}
	return result, rows.Err()
}

func (s *Service) readOneShotArtifacts(ctx context.Context, subscriptionID string) ([]OneShotArtifact, error) {
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
	result := []OneShotArtifact{}
	for rows.Next() {
		var artifact OneShotArtifact
		var fingerprints []byte
		var failureCode, failureReason, failedWorkItemID sql.NullString
		if err := rows.Scan(&artifact.ID, &artifact.DownloadTaskID, &fingerprints, &artifact.State, &failureCode, &failureReason, &artifact.Version, &failedWorkItemID); err != nil {
			return nil, fmt.Errorf("scan artifact: %w", err)
		}
		artifact.Files = []OneShotArtifactFile{}
		artifact.Fingerprints = []OneShotFingerprint{}
		var wrapper struct {
			Fingerprints []OneShotFingerprint `json:"fingerprints"`
		}
		if err := json.Unmarshal(fingerprints, &wrapper); err == nil && wrapper.Fingerprints != nil {
			artifact.Fingerprints = wrapper.Fingerprints
		}
		if failureCode.Valid {
			artifact.FailureCode = &failureCode.String
		}
		if failureReason.Valid {
			artifact.FailureReason = &failureReason.String
		}
		if failedWorkItemID.Valid {
			artifact.FailedWorkItemID = &failedWorkItemID.String
		}
		result = append(result, artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range result {
		files, err := s.readOneShotArtifactFiles(ctx, result[index].ID)
		if err != nil {
			return nil, err
		}
		result[index].Files = files
	}
	return result, nil
}

func (s *Service) readOneShotArtifactFiles(ctx context.Context, artifactID string) ([]OneShotArtifactFile, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT file_ref, relative_path, media_kind, fingerprint,
       subtitle_binding_state, bound_video_file_ref, subtitle_binding_snapshot
FROM artifact_files
WHERE artifact_id = $1
ORDER BY file_ref`, artifactID)
	if err != nil {
		return nil, fmt.Errorf("read artifact files: %w", err)
	}
	defer rows.Close()
	result := []OneShotArtifactFile{}
	for rows.Next() {
		var file OneShotArtifactFile
		var fingerprint []byte
		var bindingState string
		var boundVideoFileRef sql.NullString
		var bindingSnapshot []byte
		if err := rows.Scan(&file.Ref, &file.Path, &file.MediaKind, &fingerprint, &bindingState, &boundVideoFileRef, &bindingSnapshot); err != nil {
			return nil, fmt.Errorf("scan artifact file: %w", err)
		}
		if err := json.Unmarshal(fingerprint, &file.Fingerprint); err != nil {
			return nil, fmt.Errorf("decode artifact file fingerprint: %w", err)
		}
		binding, err := decodeOneShotArtifactFileBinding(bindingState, boundVideoFileRef, bindingSnapshot)
		if err != nil {
			return nil, fmt.Errorf("decode artifact file subtitle binding: %w", err)
		}
		file.SubtitleBinding = binding
		result = append(result, file)
	}
	return result, rows.Err()
}

func decodeOneShotArtifactFileBinding(state string, boundRef sql.NullString, rawSnapshot []byte) (*OneShotSubtitleBinding, error) {
	binding := OneShotSubtitleBinding{State: state, Diagnostics: []OneShotNamingDiagnostic{}}
	if boundRef.Valid {
		binding.BoundVideoFileRef = &boundRef.String
	}
	if len(rawSnapshot) == 0 {
		return &binding, nil
	}
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
		binding.Method = &snapshot.Method
	}
	if snapshot.Confidence != nil {
		confidence := float32(*snapshot.Confidence)
		binding.Confidence = &confidence
	}
	for _, diagnostic := range snapshot.Diagnostics {
		binding.Diagnostics = append(binding.Diagnostics, OneShotNamingDiagnostic{Code: diagnostic.Code, Message: diagnostic.Message, Path: diagnostic.Path})
	}
	return &binding, nil
}

func (s *Service) readOneShotFileOperations(ctx context.Context, subscriptionID string) ([]OneShotFileOperation, error) {
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
	result := []OneShotFileOperation{}
	for rows.Next() {
		var operation OneShotFileOperation
		var sourceFingerprint, targetFingerprint, renderSnapshot, configSnapshot []byte
		var observedAt sql.NullTime
		var expectedSize, observedTargetSize sql.NullInt64
		var failureCode, failureReason, failedWorkItemID sql.NullString
		var intent, collisionPolicy string
		if err := rows.Scan(&operation.ID, &operation.ArtifactID, &operation.ArtifactFileRef, &operation.Operation, &operation.SourcePath, &operation.TargetPath, &sourceFingerprint, &targetFingerprint, &expectedSize, &observedTargetSize, &intent, &collisionPolicy, &renderSnapshot, &configSnapshot, &operation.State, &observedAt, &failureCode, &failureReason, &operation.IdempotencyKey, &operation.Version, &failedWorkItemID); err != nil {
			return nil, fmt.Errorf("scan file operation: %w", err)
		}
		operation.CollisionPolicy = animeapplication.WorkspaceCollisionPolicy(collisionPolicy)
		operation.Intent = &intent
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
			var value OneShotFingerprint
			if err := json.Unmarshal(targetFingerprint, &value); err == nil {
				// Typed completions before migration 186 stored no size_bytes;
				// the published bytes equal the frozen expected size.
				if !fingerprintHasSize(targetFingerprint) && expectedSize.Valid {
					value.SizeBytes = expectedSize.Int64
				}
				operation.TargetFingerprint = &value
			}
		}
		if err := json.Unmarshal(renderSnapshot, &operation.FrozenRenderSnapshot); err != nil {
			return nil, fmt.Errorf("decode frozen render snapshot: %w", err)
		}
		if err := json.Unmarshal(configSnapshot, &operation.FrozenConfigSnapshot); err != nil {
			return nil, fmt.Errorf("decode frozen config snapshot: %w", err)
		}
		if observedAt.Valid {
			operation.ObservedAt = &observedAt.Time
		}
		if failureCode.Valid {
			operation.FailureCode = &failureCode.String
		}
		if failureReason.Valid {
			operation.FailureReason = &failureReason.String
		}
		if failedWorkItemID.Valid {
			operation.FailedWorkItemID = &failedWorkItemID.String
		}
		result = append(result, operation)
	}
	return result, rows.Err()
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
