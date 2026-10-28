package filemaintenance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"bansoko/internal/episodeidentity"
	"bansoko/internal/postprocessing/naming"
	facts "bansoko/internal/release/facts"
)

// PlanGeneration converts one admitted offset generation into durable
// per-placement operation owners. It only reads frozen facts and renders a
// target; it never performs a filesystem effect. Repeated calls are safe:
// operation identity is deterministic and existing rows are not rewritten.
func duplicateOffsetTargets(operations []plannedOffsetOperation) map[string]int {
	targetCounts := make(map[string]int)
	for _, operation := range operations {
		if operation.state == OffsetMigrationOperationReady && operation.newTargetPath != nil {
			targetCounts[*operation.newTargetPath]++
		}
	}
	return targetCounts
}

func (s *OffsetMigrationService) PlanGeneration(ctx context.Context, generationID string) (*OffsetMigrationGeneration, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("offset migration planner database is not wired")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin offset migration planning: %w", err)
	}
	defer tx.Rollback()

	generation, err := loadOffsetMigrationGenerationTx(ctx, tx, generationID)
	if err != nil {
		return nil, err
	}
	if generation.State.IsTerminal() {
		return generation, nil
	}
	if generation.State != OffsetMigrationGenerationPending && generation.State != OffsetMigrationGenerationPlanning {
		return nil, fmt.Errorf("offset migration generation %s is not plannable in state %s", generationID, generation.State)
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE offset_migration_generations
SET state = 'planning', version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state IN ('pending', 'planning')`, generationID); err != nil {
		return nil, fmt.Errorf("mark offset migration generation planning: %w", err)
	}
	if err := supersedeOlderOffsetGenerations(ctx, tx, *generation); err != nil {
		return nil, err
	}

	candidates, err := loadOffsetMigrationCandidates(ctx, tx, *generation)
	if err != nil {
		return nil, err
	}
	operations := make([]plannedOffsetOperation, len(candidates))
	for index, candidate := range candidates {
		operation, err := s.planOffsetMigrationCandidate(ctx, *generation, candidate)
		if err != nil {
			return nil, err
		}
		operations[index] = operation
	}
	targetCounts := duplicateOffsetTargets(operations)
	for index, operation := range operations {
		if operation.state == OffsetMigrationOperationReady && operation.newTargetPath != nil && targetCounts[*operation.newTargetPath] > 1 {
			operation = blockedOffsetOperation(operation, "target_conflict", "multiple operations render the same target path")
			operations[index] = operation
		}
		if err := insertOffsetMigrationOperationTx(ctx, tx, operation); err != nil {
			return nil, err
		}
		if operation.state == OffsetMigrationOperationReady {
			if err := s.admitOffsetMigrationWorkItemTx(ctx, tx, operation); err != nil {
				return nil, err
			}
		}
	}

	if err := settleOffsetMigrationGenerationTx(ctx, tx, generationID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit offset migration planning: %w", err)
	}
	return s.loadOffsetMigrationGeneration(ctx, generationID)
}

func (s *OffsetMigrationService) loadOffsetMigrationGeneration(ctx context.Context, generationID string) (*OffsetMigrationGeneration, error) {
	return loadOffsetMigrationGeneration(ctx, s.db, generationID)
}

func loadOffsetMigrationGeneration(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, generationID string) (*OffsetMigrationGeneration, error) {
	var (
		generation  OffsetMigrationGeneration
		completedAt sql.NullTime
	)
	err := q.QueryRowContext(ctx, `
SELECT id, managed_anime_id, scope_kind, scope_owner_id, policy_version,
       old_offset, new_offset, state, planned_count, blocked_count,
       applied_count, superseded_count, COALESCE(reason_code, ''),
       COALESCE(reason, ''), version, created_at, updated_at, completed_at
FROM offset_migration_generations
WHERE id = $1`, generationID).Scan(
		&generation.ID, &generation.ManagedAnimeID, &generation.ScopeKind,
		&generation.ScopeOwnerID, &generation.PolicyVersion, &generation.OldOffset,
		&generation.NewOffset, &generation.State, &generation.PlannedCount,
		&generation.BlockedCount, &generation.AppliedCount,
		&generation.SupersededCount, &generation.ReasonCode, &generation.Reason,
		&generation.Version, &generation.CreatedAt, &generation.UpdatedAt,
		&completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("offset migration generation %s not found", generationID)
	}
	if err != nil {
		return nil, fmt.Errorf("read offset migration generation %s: %w", generationID, err)
	}
	if completedAt.Valid {
		value := completedAt.Time
		generation.CompletedAt = &value
	}
	return &generation, nil
}

func loadOffsetMigrationGenerationTx(ctx context.Context, tx *sql.Tx, generationID string) (*OffsetMigrationGeneration, error) {
	return loadOffsetMigrationGeneration(ctx, tx, generationID)
}

func supersedeOlderOffsetGenerations(ctx context.Context, tx *sql.Tx, current OffsetMigrationGeneration) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE offset_migration_plans
SET state = 'superseded', reason_code = 'superseded_by_new_generation',
    failure_reason = 'a newer offset generation owns this placement',
    version = version + 1, updated_at = CURRENT_TIMESTAMP,
    completed_at = CURRENT_TIMESTAMP
WHERE scope_kind = $1 AND scope_owner_id = $2 AND generation_id <> $3
  AND state IN ('planned', 'ready', 'waiting_retry')`,
		current.ScopeKind, current.ScopeOwnerID, current.ID); err != nil {
		return fmt.Errorf("supersede older offset migration operations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE offset_migration_generations AS older
SET state = 'superseded', reason_code = 'superseded_by_new_generation',
    reason = 'a newer offset generation owns this scope',
    version = version + 1, updated_at = CURRENT_TIMESTAMP,
    completed_at = CURRENT_TIMESTAMP
WHERE older.scope_kind = $1 AND older.scope_owner_id = $2
  AND older.id <> $3 AND older.state IN ('pending', 'planning', 'ready')
  AND NOT EXISTS (
      SELECT 1 FROM offset_migration_plans p
      WHERE p.generation_id = older.id
        AND p.state IN ('running', 'effect_unknown')
  )`, current.ScopeKind, current.ScopeOwnerID, current.ID); err != nil {
		return fmt.Errorf("supersede older offset migration generations: %w", err)
	}
	return nil
}

type offsetMigrationCandidate struct {
	placementID       string
	placementVersion  int64
	physicalPath      string
	sourceFingerprint string
	targetFingerprint string
	sourceID          string
	sourceOffset      *int
	mediaKind         string
	// mediaKindOverride is the owner media-kind pin (tv | movie); it is the
	// legacy-identity branch when the frozen identity itself carries no kind.
	mediaKindOverride string
	snapshotID        string
	fileRef           string
	sizeBytes         int64
	effectiveFacts    []byte
	fileEvidence      []byte
}

func loadOffsetMigrationCandidates(ctx context.Context, tx *sql.Tx, generation OffsetMigrationGeneration) ([]offsetMigrationCandidate, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT p.id, p.version, p.physical_path, p.source_fingerprint,
       p.target_fingerprint, COALESCE(src.id, ''), src.episode_offset,
       COALESCE(meta.tmdb_media_kind, ''), COALESCE(meta.media_kind_override, ''),
       COALESCE(snap.id, ''), COALESCE(mf.file_ref, ''),
       COALESCE(mf.size_bytes, af.size_bytes, 0),
       COALESCE(mf.effective_facts, '{}'::jsonb),
       COALESCE(mf.file_evidence, '{}'::jsonb)
FROM placements p
LEFT JOIN file_operations fo
  ON fo.id = p.source_owner_ref AND p.source_evidence_type = 'file_operation'
LEFT JOIN artifact_files af ON af.id = fo.artifact_file_id
LEFT JOIN artifacts art ON art.id = fo.artifact_id
LEFT JOIN download_tasks dt ON dt.id = art.download_task_id
LEFT JOIN releases rel ON rel.id = dt.release_id
LEFT JOIN rss_sources src ON src.id = rel.rss_source_id
LEFT JOIN subscriptions sub ON sub.id = src.subscription_id
LEFT JOIN anime_metadata meta ON meta.managed_anime_id = sub.managed_anime_id
LEFT JOIN LATERAL (
    SELECT snap.id
    FROM media_fact_snapshots snap
    WHERE snap.release_id = rel.id
    ORDER BY snap.created_at DESC, snap.id DESC
    LIMIT 1
) snap ON true
LEFT JOIN media_fact_files mf
  ON mf.snapshot_id = snap.id AND mf.file_ref = af.file_ref
WHERE p.managed_anime_id = $1
  AND p.verification_state = 'verified'
  AND p.state = 'active'
  AND p.replacement_eligible
  AND $2 = 'rss_source' AND src.id = $3
ORDER BY p.created_at, p.id`, generation.ManagedAnimeID, generation.ScopeKind, generation.ScopeOwnerID)
	if err != nil {
		return nil, fmt.Errorf("load offset migration candidates: %w", err)
	}
	defer rows.Close()

	candidates := make([]offsetMigrationCandidate, 0, 16)
	for rows.Next() {
		var candidate offsetMigrationCandidate
		var sourceOffset sql.NullInt64
		if err := rows.Scan(
			&candidate.placementID, &candidate.placementVersion,
			&candidate.physicalPath, &candidate.sourceFingerprint,
			&candidate.targetFingerprint, &candidate.sourceID, &sourceOffset,
			&candidate.mediaKind, &candidate.mediaKindOverride, &candidate.snapshotID,
			&candidate.fileRef, &candidate.sizeBytes,
			&candidate.effectiveFacts, &candidate.fileEvidence); err != nil {
			return nil, fmt.Errorf("scan offset migration candidate: %w", err)
		}
		if sourceOffset.Valid {
			value := int(sourceOffset.Int64)
			candidate.sourceOffset = &value
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate offset migration candidates: %w", err)
	}
	return candidates, nil
}

type plannedOffsetOperation struct {
	id                string
	generationID      string
	managedAnimeID    string
	scopeKind         OffsetMigrationScopeKind
	scopeOwnerID      string
	placementID       string
	placementVersion  int64
	policyVersion     int64
	sourcePath        string
	sourceFingerprint string
	fingerprint       json.RawMessage
	snapshotID        string
	fileRef           string
	sourceID          string
	sourceOffset      *int
	mediaKind         string
	rawEpisode        *string
	newEpisode        *string
	oldTargetPath     string
	newTargetPath     *string
	oldIdentity       json.RawMessage
	newIdentity       json.RawMessage
	oldCoverageKey    *string
	newCoverageKey    *string
	targetContract    json.RawMessage
	state             OffsetMigrationOperationState
	reasonCode        string
	failureReason     string
}

func (s *OffsetMigrationService) planOffsetMigrationCandidate(ctx context.Context, generation OffsetMigrationGeneration, candidate offsetMigrationCandidate) (plannedOffsetOperation, error) {
	operation := plannedOffsetOperation{
		id:                offsetMigrationOperationID(generation.ID, candidate.placementID, candidate.targetFingerprint),
		generationID:      generation.ID,
		managedAnimeID:    generation.ManagedAnimeID,
		scopeKind:         generation.ScopeKind,
		scopeOwnerID:      generation.ScopeOwnerID,
		placementID:       candidate.placementID,
		placementVersion:  candidate.placementVersion,
		policyVersion:     generation.PolicyVersion,
		sourcePath:        candidate.physicalPath,
		sourceFingerprint: candidate.targetFingerprint,
		fingerprint:       json.RawMessage(nil),
		snapshotID:        candidate.snapshotID,
		fileRef:           candidate.fileRef,
		sourceID:          candidate.sourceID,
		sourceOffset:      candidate.sourceOffset,
		mediaKind:         candidate.mediaKind,
		oldTargetPath:     candidate.physicalPath,
		state:             OffsetMigrationOperationBlocked,
	}
	if raw, err := parseFingerprint(candidate.targetFingerprint); err == nil {
		operation.fingerprint = raw
	}

	if candidate.snapshotID == "" || candidate.fileRef == "" || len(candidate.effectiveFacts) == 0 {
		return blockedOffsetOperation(operation, "render_facts_unavailable", "frozen media fact snapshot/file identity is unavailable"), nil
	}
	var effective facts.EffectiveFileFacts
	if err := json.Unmarshal(candidate.effectiveFacts, &effective); err != nil {
		return blockedOffsetOperation(operation, "media_facts_invalid", fmt.Sprintf("decode frozen media facts: %v", err)), nil
	}
	oldIdentity := effective.Identity
	oldIdentityRaw, err := json.Marshal(oldIdentity)
	if err != nil {
		return plannedOffsetOperation{}, fmt.Errorf("encode old episode identity: %w", err)
	}
	operation.oldIdentity = oldIdentityRaw
	if oldIdentity.CoverageKey != nil {
		value := *oldIdentity.CoverageKey
		operation.oldCoverageKey = &value
	}
	if oldIdentity.State != episodeidentity.StateResolved || oldIdentity.RawEpisode == nil || oldIdentity.SelectedSource == nil {
		return blockedOffsetOperation(operation, "render_facts_unavailable", "frozen episode identity is not resolved"), nil
	}
	// A multi-episode file occupies several keys; moving its offset needs every
	// member re-identified and the coverage rewritten as a set, which the
	// single-key migration does not do. It stays where it is, visibly.
	if oldIdentity.MultiEpisode() {
		return blockedOffsetOperation(operation, "multi_episode_offset_unsupported", "a multi-episode file is not moved by an offset change"), nil
	}
	// The frozen identity kind is the media-kind authority: a metadata rebinding
	// after submission must not turn a frozen movie into a TV re-identification
	// (or the reverse). The current metadata kind is only read for a legacy
	// frozen identity that never carried a kind.
	mediaKind := frozenIdentityMediaKind(oldIdentity.Kind)
	if mediaKind == "" {
		mediaKind = resolveLegacyMediaKind(candidate.mediaKind, candidate.mediaKindOverride)
	}
	if mediaKind != "tv" && mediaKind != "movie" {
		return blockedOffsetOperation(operation, "media_kind_unavailable", "metadata media kind is unavailable"), nil
	}
	operation.mediaKind = mediaKind
	if len(operation.fingerprint) == 0 {
		return blockedOffsetOperation(operation, "fingerprint_invalid", "placement target fingerprint is not a JSON object"), nil
	}
	if _, err := offsetSourceFingerprintValue(candidate.targetFingerprint); err != nil {
		return blockedOffsetOperation(operation, "fingerprint_invalid", fmt.Sprintf("placement target fingerprint is unsupported: %v", err)), nil
	}

	raw := *oldIdentity.RawEpisode
	source := *oldIdentity.SelectedSource
	fileEvidence := episodeidentity.EpisodeEvidence{
		Source:     source,
		InputKind:  "frozen_identity",
		Status:     episodeidentity.StatusValue,
		Shape:      episodeidentity.ShapeSingle,
		RawCapture: string(raw),
		Episode:    &raw,
	}
	identity := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{
		MediaKind:    mediaKind,
		FileRef:      candidate.fileRef,
		FileEvidence: fileEvidence,
		Offset:       generation.NewOffset,
	})
	newIdentityRaw, err := json.Marshal(identity)
	if err != nil {
		return plannedOffsetOperation{}, fmt.Errorf("encode new episode identity: %w", err)
	}
	operation.newIdentity = newIdentityRaw
	if identity.RawEpisode != nil {
		value := string(*identity.RawEpisode)
		operation.rawEpisode = &value
	}
	if identity.Episode != nil {
		value := string(*identity.Episode)
		operation.newEpisode = &value
	}
	if identity.CoverageKey != nil {
		value := *identity.CoverageKey
		operation.newCoverageKey = &value
	}
	if identity.Kind == episodeidentity.KindMovie {
		return blockedOffsetOperation(operation, "movie_noop", "movie identity is not affected by episode offset"), nil
	}
	if identity.State != episodeidentity.StateResolved || identity.CoverageKey == nil || identity.Episode == nil {
		return blockedOffsetOperation(operation, "episode_identity_unresolved", "new episode identity cannot be resolved"), nil
	}
	if strings.TrimSpace(candidate.sourceID) == "" {
		return blockedOffsetOperation(operation, "source_provenance_unavailable", "RSS source provenance is unavailable"), nil
	}
	frozen := &naming.FrozenEpisodeNamingFacts{
		Raw:     string(raw),
		Number:  string(*identity.Episode),
		Offset:  identity.Offset,
		Display: string(*identity.Episode),
	}
	newTarget, err := s.renderer.renderTargetWithFrozenEpisode(ctx, generation.ManagedAnimeID, candidate.physicalPath, candidate.sizeBytes, candidate.placementID, frozen)
	if err != nil {
		reason := "target_render_failed"
		if errors.Is(err, naming.ErrRenderFactsUnavailable) {
			reason = "render_facts_unavailable"
		}
		return blockedOffsetOperation(operation, reason, safeReason(err)), nil
	}
	if newTarget == candidate.physicalPath {
		return blockedOffsetOperation(operation, "target_unchanged", "new canonical target equals the current placement path"), nil
	}
	operation.newTargetPath = &newTarget
	operation.state = OffsetMigrationOperationReady
	operation.targetContract, err = json.Marshal(map[string]any{
		"protocol_version":   "offset-migration-v1",
		"placement_id":       candidate.placementID,
		"placement_version":  candidate.placementVersion,
		"source_path":        candidate.physicalPath,
		"source_fingerprint": json.RawMessage(operation.fingerprint),
		"target_path":        newTarget,
		"coverage_key":       *identity.CoverageKey,
	})
	if err != nil {
		return plannedOffsetOperation{}, fmt.Errorf("encode offset migration target contract: %w", err)
	}
	return operation, nil
}

// frozenIdentityMediaKind maps a frozen EpisodeIdentity kind to the metadata
// media kind the resolver consumes. An identity frozen before the kind existed
// returns "" so the caller can fall back to the stored metadata evidence.
func frozenIdentityMediaKind(kind string) string {
	switch kind {
	case episodeidentity.KindMovie:
		return "movie"
	case episodeidentity.KindTVEpisode:
		return "tv"
	default:
		return ""
	}
}

func blockedOffsetOperation(operation plannedOffsetOperation, code, reason string) plannedOffsetOperation {
	operation.state = OffsetMigrationOperationBlocked
	operation.reasonCode = code
	operation.failureReason = boundedOffsetMigrationReason(reason)
	return operation
}

func boundedOffsetMigrationReason(reason string) string {
	if len(reason) > 1024 {
		return reason[:1024]
	}
	return reason
}

func insertOffsetMigrationOperationTx(ctx context.Context, tx *sql.Tx, operation plannedOffsetOperation) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO offset_migration_plans(
    id, generation_id, managed_anime_id, scope_kind, scope_owner_id,
    placement_id, placement_version, policy_version, source_path,
    source_fingerprint, fingerprint, media_fact_snapshot_id,
    media_fact_file_ref, rss_source_id, source_episode_offset, media_kind,
    bound_season, raw_episode, new_episode, old_target_path, new_target_path,
    old_identity, new_identity, old_coverage_key, new_coverage_key,
    target_contract, state, failure_reason, reason_code, version,
    created_at, updated_at, completed_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, '{}'::jsonb),
        NULLIF($12, ''), NULLIF($13, ''), NULLIF($14, ''), $15, NULLIF($16, ''),
        $17, $18, $19, $20, $21, NULLIF($22, '{}'::jsonb),
        NULLIF($23, '{}'::jsonb), $24, $25, NULLIF($26, '{}'::jsonb),
        $27, NULLIF($28, ''), NULLIF($29, ''), 1, CURRENT_TIMESTAMP,
        CURRENT_TIMESTAMP, CASE WHEN $27 IN ('blocked', 'failed', 'superseded') THEN CURRENT_TIMESTAMP ELSE NULL END)
ON CONFLICT (policy_version, placement_id, source_fingerprint) DO NOTHING`,
		operation.id, operation.generationID, operation.managedAnimeID,
		operation.scopeKind, operation.scopeOwnerID, operation.placementID,
		operation.placementVersion, operation.policyVersion, operation.sourcePath,
		operation.sourceFingerprint, jsonOrEmpty(operation.fingerprint),
		operation.snapshotID, operation.fileRef, operation.sourceID,
		nullableInt(operation.sourceOffset), operation.mediaKind,
		// An episode identity binds no season, so a plan records none.
		nil, nullableString(operation.rawEpisode),
		nullableString(operation.newEpisode), operation.oldTargetPath,
		nullableString(operation.newTargetPath), jsonOrEmpty(operation.oldIdentity),
		jsonOrEmpty(operation.newIdentity), nullableString(operation.oldCoverageKey),
		nullableString(operation.newCoverageKey), jsonOrEmpty(operation.targetContract),
		string(operation.state), operation.failureReason, operation.reasonCode)
	if err != nil {
		return fmt.Errorf("insert offset migration operation %s: %w", operation.id, err)
	}
	return nil
}

func settleOffsetMigrationGenerationTx(ctx context.Context, tx *sql.Tx, generationID string) error {
	var planned, blocked, applied, superseded, ready, active int
	if err := tx.QueryRowContext(ctx, `
SELECT
  COUNT(*) FILTER (WHERE state IN ('planned', 'ready', 'running', 'waiting_retry', 'effect_unknown')),
  COUNT(*) FILTER (WHERE state = 'blocked'),
  COUNT(*) FILTER (WHERE state = 'applied'),
  COUNT(*) FILTER (WHERE state = 'superseded'),
  COUNT(*) FILTER (WHERE state = 'ready'),
  COUNT(*) FILTER (WHERE state IN ('running', 'effect_unknown'))
FROM offset_migration_plans WHERE generation_id = $1`, generationID).Scan(
		&planned, &blocked, &applied, &superseded, &ready, &active); err != nil {
		return fmt.Errorf("count offset migration operations: %w", err)
	}
	state := OffsetMigrationGenerationCompleted
	if ready > 0 || active > 0 {
		state = OffsetMigrationGenerationReady
	} else if blocked > 0 {
		state = OffsetMigrationGenerationCompletedWithBlocks
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE offset_migration_generations
SET state = $2, planned_count = $3, blocked_count = $4,
    applied_count = $5, superseded_count = $6,
    completed_at = CASE WHEN $2 IN ('completed', 'completed_with_blocks') THEN CURRENT_TIMESTAMP ELSE NULL END,
    version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1`, generationID, state, planned, blocked, applied, superseded); err != nil {
		return fmt.Errorf("settle offset migration generation: %w", err)
	}
	return nil
}

func offsetMigrationOperationID(generationID, placementID, sourceFingerprint string) string {
	return "offop:" + hexDigest(generationID+"\x00"+placementID+"\x00"+sourceFingerprint)
}

func hexDigest(value string) string {
	// Kept in one helper so operation/admission identifiers use the same
	// bounded, opaque format without exposing user-controlled paths.
	return fmt.Sprintf("%x", sha256Bytes(value))
}

func sha256Bytes(value string) [32]byte {
	return sha256.Sum256([]byte(value))
}

func parseFingerprint(value string) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || !json.Valid([]byte(trimmed)) || !strings.HasPrefix(trimmed, "{") {
		return nil, errors.New("fingerprint is not a JSON object")
	}
	return json.RawMessage(trimmed), nil
}

func jsonOrEmpty(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
