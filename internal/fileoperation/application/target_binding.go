package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

type frozenTargetRecord struct {
	targetPath       string
	expectedSize     int64
	intent           string
	collisionPolicy  string
	renderSnapshot   []byte
	configSnapshot   []byte
	observationState string
}

// applyFrozenTargetTx makes the persisted Release TargetSet authoritative for
// post-Artifact preparation. It returns a planned target for subtitle binding,
// and reports skip=true when the target was already satisfied during the
// acquisition probe and therefore needs no FileOperation at all.
func (s *Service) applyFrozenTargetTx(ctx context.Context, tx *sql.Tx, artifact artifactContext, file artifactFileContext, plan *operationPlan, prep prepareContext) (*plannedVideoTarget, bool, error) {
	record, found, err := s.frozenTargetRecordTx(ctx, tx, artifact, file, prep)
	if err != nil || !found {
		return nil, false, err
	}
	if strings.TrimSpace(record.targetPath) == "" || record.expectedSize < 0 {
		return nil, false, fmt.Errorf("%w: frozen target is invalid", ErrInvalidTarget)
	}
	directory := filepath.Dir(record.targetPath)
	filename := filepath.Base(record.targetPath)
	planned := &plannedVideoTarget{directory: directory, filename: filename}
	if record.intent == "ensure_present" && record.observationState == "present_matched" {
		observation, observeErr := observeTarget(ctx, s.observer, record.targetPath, true)
		if observeErr == nil && observation.SizeBytes == record.expectedSize {
			return planned, true, nil
		}
	}
	plan.targetPath = filepath.Clean(record.targetPath)
	plan.expectedSizeBytes = record.expectedSize
	plan.intent = record.intent
	plan.collisionPolicy = record.collisionPolicy
	// The acquisition target's own snapshots carry only the target-level
	// contract (operation kind, collision policy, parser provenance). The file
	// operation additionally has to carry the frozen naming/effect
	// configuration and the metadata binding the target was rendered with,
	// because a maintenance re-render reads the operation's own snapshot as its
	// only evidence. Copying the target snapshot verbatim left
	// the decode without naming rules, so every placed file was excluded from
	// alignment as render_facts_unavailable — and the placement then kept a path
	// the file effect never rendered. Both contracts are preserved: the
	// operation snapshot wins on the keys it defines, the target keeps the rest.
	configSnapshot, err := marshalConfigSnapshot(prep.config, nil, prep.parserProvenance)
	if err != nil {
		return nil, false, fmt.Errorf("marshal frozen config snapshot: %w", err)
	}
	plan.renderSnapshot, err = mergeFrozenSnapshot(map[string]any{
		"renderer_version": rendererVersion,
		"directory":        directory,
		"filename":         filename,
		"target":           plan.targetPath,
		"binding_facts":    artifactBindingFacts(artifact),
	}, record.renderSnapshot)
	if err != nil {
		return nil, false, fmt.Errorf("marshal frozen render snapshot: %w", err)
	}
	plan.configSnapshot, err = mergeFrozenSnapshot(configSnapshot, record.configSnapshot)
	if err != nil {
		return nil, false, fmt.Errorf("marshal frozen config snapshot: %w", err)
	}
	var config struct {
		Operation string `json:"operation"`
	}
	if json.Unmarshal(plan.configSnapshot, &config) == nil {
		switch config.Operation {
		case "copy", "move", "hardlink":
			plan.operation = config.Operation
		}
	}
	if plan.targetPath == plan.sourcePath {
		return nil, false, fmt.Errorf("%w: frozen source and target are identical", ErrInvalidTarget)
	}
	if plan.collisionPolicy == "overwrite" {
		snapshot, snapshotErr := buildOverwriteExecutionSnapshot(*plan, s.parentAuthorizer)
		if snapshotErr != nil {
			return nil, false, snapshotErr
		}
		plan.overwriteSnapshot = snapshot
	}
	if err := s.persistOperationPlan(ctx, tx, *plan, artifact, file); err != nil {
		return nil, false, err
	}
	if err := s.ensureWorkItemTxAfterMarkerAdmissionTx(ctx, tx, operationRecord{
		id:              plan.id,
		artifactID:      plan.artifactID,
		artifactVersion: plan.artifactVersion,
		artifactFileID:  plan.artifactFileID,
		operation:       plan.operation,
		collisionPolicy: plan.collisionPolicy,
		state:           plan.state,
		idempotencyKey:  plan.idempotencyKey,
		version:         plan.operationVersion,
	}, prep.oneShot); err != nil {
		return nil, false, err
	}
	planned.operationID = plan.id
	return planned, false, nil
}

// frozenTargetRecordTx reads the Release TargetSet entry for one artifact file,
// from the prepared batch's captured targets when planning in a batch.
func (s *Service) frozenTargetRecordTx(ctx context.Context, tx *sql.Tx, artifact artifactContext, file artifactFileContext, prep prepareContext) (frozenTargetRecord, bool, error) {
	// The Release TargetSet belongs to admission, not an explicit new-input
	// plan. Do not let a present-matched old target skip the new generation.
	if prep.replanTargets {
		return frozenTargetRecord{}, false, nil
	}
	if s.preparedBatch != nil {
		record, found := s.preparedBatch.targets[file.id]
		return record, found, nil
	}
	return loadFrozenTargetTx(ctx, tx, artifact.releaseID, file.relativePath, file.sourcePath)
}

// frozenSubtitleTargetBesideVideo reports whether an admission-frozen subtitle
// target sits in the bound video's directory under the video's basename
// ("<basename>[.<suffix>].<ext>"), which is what binding the subtitle to that
// video promises. The frozen video directory is absolute like every rendered
// target; a relative legacy value does not match.
func frozenSubtitleTargetBesideVideo(target string, video plannedVideoTarget, videoBasename string) bool {
	directory := filepath.FromSlash(video.directory)
	if !filepath.IsAbs(directory) {
		return false
	}
	target = filepath.Clean(target)
	return filepath.Dir(target) == filepath.Clean(directory) &&
		strings.HasPrefix(filepath.Base(target), videoBasename+".")
}

// lockSubtitleBindingTx freezes the bound video's target segments onto the
// subtitle's binding snapshot, so later drift is detectable as
// BOUND_TARGET_MISMATCH and the execution-time dependency gate can verify it.
func (s *Service) lockSubtitleBindingTx(ctx context.Context, tx *sql.Tx, file artifactFileContext, snapshot bindingSnapshot, locked bindingLockedTarget) error {
	snapshot.Locked = &locked
	if s.preparedBatch != nil {
		s.preparedBatch.bindings = append(s.preparedBatch.bindings, preparedBinding{file: file, locked: &snapshot})
		return nil
	}
	return lockBindingTargetTx(ctx, tx, file, snapshot)
}

func loadFrozenTargetTx(ctx context.Context, tx *sql.Tx, releaseID, relativePath, sourcePath string) (frozenTargetRecord, bool, error) {
	return loadFrozenTargetWithLocks(ctx, tx, releaseID, relativePath, sourcePath, true)
}
func loadFrozenTargetWithLocks(ctx context.Context, tx *sql.Tx, releaseID, relativePath, sourcePath string, lock bool) (frozenTargetRecord, bool, error) {
	lockSQL := ""
	if lock {
		lockSQL = " FOR SHARE OF t"
	}
	releaseID = strings.TrimSpace(releaseID)
	relativePath = strings.TrimSpace(strings.ReplaceAll(relativePath, "\\", "/"))
	sourcePath = strings.TrimSpace(strings.ReplaceAll(sourcePath, "\\", "/"))
	var record frozenTargetRecord
	var observedState sql.NullString
	err := tx.QueryRowContext(ctx, `
SELECT t.target_path, t.expected_size_bytes, t.intent, t.collision_policy,
       t.render_snapshot, t.config_snapshot, t.observation_state
FROM acquisition_targets t
WHERE t.release_id = $1 AND (t.source_path = $2 OR t.source_path = $3)
ORDER BY CASE WHEN t.source_path = $2 THEN 0 ELSE 1 END, t.target_ref
LIMIT 1
`+lockSQL, releaseID, relativePath, sourcePath).Scan(
		&record.targetPath, &record.expectedSize, &record.intent, &record.collisionPolicy,
		&record.renderSnapshot, &record.configSnapshot, &observedState)
	if errors.Is(err, sql.ErrNoRows) {
		return frozenTargetRecord{}, false, nil
	}
	if err != nil {
		return frozenTargetRecord{}, false, fmt.Errorf("read frozen acquisition target: %w", err)
	}
	record.observationState = observedState.String
	return record, true, nil
}

// mergeFrozenSnapshot overlays one frozen snapshot onto another. `primary` wins
// on every key it defines; keys only `secondary` carries (the target-level
// operation kind, the collision policy, the parser provenance) survive. Used
// where a file operation is created from a persisted acquisition target, whose
// own snapshot describes the target contract while the operation also needs the
// frozen configuration and render binding a maintenance re-render reads.
func mergeFrozenSnapshot(primary any, secondary []byte) (json.RawMessage, error) {
	merged := map[string]json.RawMessage{}
	if len(secondary) > 0 && string(secondary) != "null" {
		if err := json.Unmarshal(secondary, &merged); err != nil {
			return nil, fmt.Errorf("decode the frozen target snapshot: %w", err)
		}
	}
	raw, err := json.Marshal(primary)
	if err != nil {
		return nil, err
	}
	var own map[string]json.RawMessage
	if err := json.Unmarshal(raw, &own); err != nil {
		return nil, err
	}
	for key, value := range own {
		merged[key] = value
	}
	return json.Marshal(merged)
}

// artifactBindingFacts reads the metadata binding the artifact was prepared
// with, in the same shape renderBindingFacts records for a rendered target: the
// media-kind branch, the season folder, the two work ids and the two provider
// years. An effect that renders from a frozen acquisition target must still
// freeze the binding it used, or the maintenance re-render cannot reproduce the
// committed target.
func artifactBindingFacts(artifact artifactContext) map[string]any {
	facts := map[string]any{"media_kind": "", "season": 0, "tmdb_work_id": "", "bgm_work_id": "", "bgm_year": nil, "tmdb_year": nil}
	if artifact.tmdbMediaKind.Valid {
		facts["media_kind"] = artifact.tmdbMediaKind.String
	}
	if artifact.tmdbSeason.Valid {
		facts["season"] = int(artifact.tmdbSeason.Int64)
		facts["season_bound"] = true
	}
	if artifact.tmdbWorkID.Valid {
		facts["tmdb_work_id"] = artifact.tmdbWorkID.String
	}
	if artifact.metadataBGM.Valid {
		facts["bgm_work_id"] = artifact.metadataBGM.String
	}
	if year := bindingYear(artifact.bindingTitles.bgmYear); year != nil {
		facts["bgm_year"] = year
	}
	if year := bindingYear(artifact.bindingTitles.tmdbYear); year != nil {
		facts["tmdb_year"] = year
	}
	return facts
}
