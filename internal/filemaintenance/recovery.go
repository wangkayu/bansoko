package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/library/domain"
	"bansoko/internal/observability/businesslog"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
)

// ---- recovery candidate (immutable scan fact) ----------------------------
//
// recoveryCandidateFact mirrors the reconciliation scan's frozen candidate
// (reconciliation_files.facts.recovery_candidates[]). The scan wrote it by
// stat'ing the exact rendered target; the confirm re-reads it as the user's
// decision token and re-verifies every fact before the placement re-attaches.

type recoveryCandidateFact struct {
	PlacementID        string                  `json:"placement_id"`
	TargetPath         string                  `json:"target_path"`
	TargetFingerprint  string                  `json:"target_fingerprint"`
	SizeBytes          int64                   `json:"size_bytes"`
	Generation         int64                   `json:"generation"`
	Companions         []recoveryCompanionFact `json:"companions"`
	CompanionsComplete bool                    `json:"companions_complete"`
}

type recoveryCompanionFact struct {
	ID          string  `json:"id"`
	TargetPath  string  `json:"target_path"`
	State       string  `json:"state"` // present | missing
	Fingerprint *string `json:"fingerprint,omitempty"`
	SizeBytes   int64   `json:"size_bytes"`
}

// readRecoveryCandidateTx reads the newest recovery candidate of one placement
// across completed generations (the frozen fact the file dialog showed).
// ok=false is the typed candidate_gone outcome: the candidate is gone or a
// newer generation superseded it.
func readRecoveryCandidateTx(ctx context.Context, tx *sql.Tx, animeID, placementID string) (recoveryCandidateFact, bool, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT rf.facts
FROM reconciliation_files rf
WHERE rf.managed_anime_id = $1 AND rf.facts->>'placement_id' = $2
  AND rf.facts->'recovery_candidates' IS NOT NULL
  AND rf.facts->>'disposition' = 'missing'
ORDER BY rf.generation DESC
LIMIT 1`, animeID, placementID)
	if err != nil {
		return recoveryCandidateFact{}, false, fmt.Errorf("read recovery candidate: %w", err)
	}
	defer rows.Close()
	var factsRaw []byte
	if !rows.Next() {
		return recoveryCandidateFact{}, false, rows.Err()
	}
	if err := rows.Scan(&factsRaw); err != nil {
		return recoveryCandidateFact{}, false, err
	}
	if err := rows.Err(); err != nil {
		return recoveryCandidateFact{}, false, err
	}
	var document struct {
		RecoveryCandidates []recoveryCandidateFact `json:"recovery_candidates"`
	}
	if err := json.Unmarshal(factsRaw, &document); err != nil {
		return recoveryCandidateFact{}, false, fmt.Errorf("decode recovery candidate: %w", err)
	}
	// The newest candidate of this placement inside that row wins; the fact
	// was frozen by the scan so this read is deterministic.
	for i := len(document.RecoveryCandidates) - 1; i >= 0; i-- {
		if candidate := document.RecoveryCandidates[i]; candidate.PlacementID == placementID {
			return candidate, true, nil
		}
	}
	return recoveryCandidateFact{}, false, nil
}

// ---- confirm result and typed refusals -----------------------------------

// AnimeFilesRecoverResult is the outcome of a confirmed recovery.
type AnimeFilesRecoverResult struct {
	AnimeID           string
	PlacementID       string
	Generation        int64
	RecoveredAt       time.Time
	TargetPath        string
	Replay            bool
	Companions        int
	CompanionsMissing int
}

// AnimeFilesRecoverRefusal is a typed fail-closed outcome; Code is one of the
// RecoveryReason* constants and rides as the Conflict detail code.
type AnimeFilesRecoverRefusal struct{ Code string }

func (e *AnimeFilesRecoverRefusal) Error() string { return "ANIME_FILES_RECOVER_REFUSED: " + e.Code }

// Recovery reasons are the confirm-time refusals. The scan never recovers
// anything and the confirm re-verifies every frozen fact, so each refusal is
// the candidate being gone, changed, occupied or already owned.
const (
	RecoveryReasonOwnerRetired      = "owner_retired"
	RecoveryReasonNotStarted        = "not_started"
	RecoveryReasonCandidateGone     = "candidate_gone"
	RecoveryReasonScanChanged       = "scan_changed"
	RecoveryReasonPlacementChanged  = "placement_changed"
	RecoveryReasonTargetMissing     = "target_missing"
	RecoveryReasonTargetChanged     = "target_changed"
	RecoveryReasonTargetOccupied    = "target_occupied"
	RecoveryReasonCompanionOccupied = "companion_occupied"
)

// ---- recovery observer port ----------------------------------------------

// RecoveryFileObserver re-stats one explicit path without following symlinks
// and without reading content (domain.MetadataFingerprint: size + dev + ino).
// The library filesystem adapter implements it; the port keeps this owner
// testable without a real disk.
type RecoveryFileObserver interface {
	ObserveFile(ctx context.Context, path string) (domain.FileObservation, error)
}

// SetRecoveryObserver wires the confirm's re-stat port; nil fails closed.
func (s *Service) SetRecoveryObserver(observer RecoveryFileObserver) {
	if s != nil {
		s.recoveryObserver = observer
	}
}

// ---- confirm command -------------------------------------------------------

// RecoverAnimeFileApplication is the user's explicit recovery confirm. It
// re-attaches the user-adopted file at the candidate's rendered target to the
// placement that rendered it: the only path a scan-alignment recovery
// candidate ever becomes an ownership change.
//
// Fail-closed order (steps 2-8 inside ONE transaction):
//  1. the anime admits manual commands (lifecycle + subscription state);
//  2. the candidate fact is read from the newest generation (frozen target
//     path, fingerprint, size, generation);
//  3. the placement row is re-read FOR UPDATE and must still be active and
//     verified, and the newest observation row naming it must still be the
//     candidate's generation (a newer scan already re-decided the fate);
//  4. the target file is re-stat'ed: same size and same metadata fingerprint
//     (a file the user replaced after adopting it is a changed file, not a
//     recovery — the inode is the same only when the file is the same);
//  5. companions observed present must still stat at their frozen paths with
//     the frozen identity (an occupied companion path refuses);
//  6. no other active relation may already own the target path (no second
//     owner, ever);
//  7. the placement re-attaches (physical_path = target, target fingerprint
//     refreshed to the observed metadata facts, source_evidence_type =
//     `recovery`, snapshot_generation = the confirmed generation) and its
//     verified present companions re-attach at their frozen paths;
//  8. the business journal records the confirm idempotently (dedupe key).
//
// No byte is ever moved, copied, overwritten or deleted: the file the user
// adopted stays exactly where it is; only ownership rows re-point.
func (s *Service) RecoverAnimeFileApplication(ctx context.Context, animeID, placementID string, expectedGeneration int64) (AnimeFilesRecoverResult, error) {
	result := AnimeFilesRecoverResult{AnimeID: animeID, PlacementID: placementID}
	if s == nil || s.placements == nil || s.placements.db == nil {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonCandidateGone}
	}
	if s.recoveryObserver == nil {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonCandidateGone}
	}
	db := s.placements.db
	owner, found, err := loadAnimeFilesOwner(ctx, db, animeID)
	if err != nil {
		return result, err
	}
	if !found {
		return result, &apperrors.NotFound{Resource: "managed_anime", ID: animeID}
	}
	if reason := owner.manualActionReason(); reason != "" {
		return result, &AnimeFilesRecoverRefusal{Code: reason}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	// (2) the frozen candidate fact the dialog confirmed.
	candidate, ok, err := readRecoveryCandidateTx(ctx, tx, animeID, placementID)
	if err != nil {
		return result, err
	}
	if !ok {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonCandidateGone}
	}
	result.Generation = candidate.Generation
	result.TargetPath = candidate.TargetPath

	// (3) the placement under lock: still the row the candidate froze.
	placement, ok, err := readRecoveryPlacementTx(ctx, tx, animeID, placementID)
	if err != nil {
		return result, err
	}
	if !ok {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonCandidateGone}
	}
	if placement.State != "active" || placement.VerificationState != "verified" {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonPlacementChanged}
	}
	if placement.ObservedGeneration != candidate.Generation {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonScanChanged}
	}
	if expectedGeneration != 0 && expectedGeneration != candidate.Generation {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonScanChanged}
	}

	// Idempotent replay: this generation's confirm already committed (the
	// placement sits at the target with the recovery evidence and the tagged
	// fact), so the same confirm replays the same result.
	if placement.SourceEvidenceType == "recovery" && placement.SnapshotGeneration == candidate.Generation {
		companions, err := countActiveCompanionsTx(ctx, tx, placementID)
		if err != nil {
			return result, err
		}
		result.Replay = true
		result.RecoveredAt = placement.UpdatedAt
		result.Companions = companions
		return result, nil
	}

	// (4) the target file must still be exactly what the scan stat'ed.
	observation, err := s.recoveryObserver.ObserveFile(ctx, candidate.TargetPath)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonTargetMissing}
	}
	if observation.Fingerprint != candidate.TargetFingerprint || observation.SizeBytes != candidate.SizeBytes {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonTargetChanged}
	}
	facts, ok := domain.ParseMetadataFingerprint(observation.Fingerprint)
	if !ok {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonTargetChanged}
	}

	// (5) companions: each present fact must still stat at its frozen path
	// with the frozen identity; a path that another relation took, or that now
	// carries different content, refuses the confirm (stale candidate).
	present, missing, refused, err := verifyRecoveryCompanionsTx(ctx, s.recoveryObserver, tx, candidate)
	if err != nil {
		return result, err
	}
	if refused {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonCompanionOccupied}
	}

	// (6) no second owner: the exact target path must not belong to another
	// active placement or companion relation of any instance. A placement
	// sitting at its own rendered target is the candidate's own row, not a
	// conflict.
	targetPath := filepath.Clean(strings.TrimSpace(candidate.TargetPath))
	occupied, err := recoveryTargetOccupiedTx(ctx, tx, targetPath, placementID)
	if err != nil {
		return result, err
	}
	if occupied {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonTargetOccupied}
	}

	// (7)+(8) re-attach ownership rows and record the journal inside the same
	// transaction; the commit makes the recovery durable as one boundary.
	recoveredAt := time.Now().UTC()
	if err := s.writeRecoveryOwnershipTx(ctx, tx, writeRecoveryOwnershipInput{
		animeID:         animeID,
		placementID:     placementID,
		targetPath:      targetPath,
		facts:           facts,
		fingerprint:     observation.Fingerprint,
		generation:      candidate.Generation,
		companions:      present,
		recoveredAt:     recoveredAt,
		previousVersion: placement.Version,
	}); err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	result.RecoveredAt = recoveredAt
	result.Companions = len(present)
	result.CompanionsMissing = missing
	return result, nil
}

// ---- transaction helpers ---------------------------------------------------

type recoveryPlacementRow struct {
	State              string
	VerificationState  string
	SourceEvidenceType string
	SnapshotGeneration int64
	ObservedGeneration int64
	Version            int64
	UpdatedAt          time.Time
}

// readRecoveryPlacementTx locks the placement row (FOR UPDATE) and reads the
// identity facts the confirm re-verifies. observed_generation is the newest
// generation whose observation row names this placement with a recovery
// candidate: the candidate's frozen generation must equal it, so a newer scan
// that already re-decided the placement's fate wins over a stale confirm.
func readRecoveryPlacementTx(ctx context.Context, tx *sql.Tx, animeID, placementID string) (recoveryPlacementRow, bool, error) {
	var row recoveryPlacementRow
	var updatedAt sql.NullTime
	err := tx.QueryRowContext(ctx, `
SELECT p.state, p.verification_state, p.source_evidence_type,
	       COALESCE(p.snapshot_generation, 0),
	       (SELECT COALESCE(MAX(rf.generation), 0) FROM reconciliation_files rf
	        WHERE rf.managed_anime_id = p.managed_anime_id
	          AND rf.facts->>'placement_id' = p.id
	          AND rf.facts->'recovery_candidates' IS NOT NULL
	          AND rf.facts->>'disposition' = 'missing'),
	       p.version, p.updated_at
FROM placements p WHERE p.id = $1 AND p.managed_anime_id = $2
FOR UPDATE`, placementID, animeID).
		Scan(&row.State, &row.VerificationState, &row.SourceEvidenceType,
			&row.SnapshotGeneration, &row.ObservedGeneration, &row.Version, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return recoveryPlacementRow{}, false, nil
	}
	if err != nil {
		return recoveryPlacementRow{}, false, fmt.Errorf("read recovery placement: %w", err)
	}
	if updatedAt.Valid {
		row.UpdatedAt = updatedAt.Time
	}
	return row, true, nil
}

// verifyRecoveryCompanionsTx re-stats the candidate's companion facts. A
// companion that vanished is a reported gap (the video still recovers); a
// companion whose path now carries a different identity, or whose exact path
// another active relation took, refuses the confirm.
func verifyRecoveryCompanionsTx(ctx context.Context, observer RecoveryFileObserver, tx *sql.Tx, candidate recoveryCandidateFact) (present []recoveryCompanionFact, missing int, refused bool, err error) {
	present = []recoveryCompanionFact{}
	for _, companion := range candidate.Companions {
		if companion.State != "present" {
			missing++
			continue
		}
		observation, statErr := observer.ObserveFile(ctx, companion.TargetPath)
		if statErr != nil {
			if ctx.Err() != nil {
				return present, missing, false, ctx.Err()
			}
			missing++
			continue
		}
		if companion.Fingerprint == nil || observation.Fingerprint != *companion.Fingerprint || observation.SizeBytes != companion.SizeBytes {
			return present, missing, true, nil
		}
		taken, takeErr := recoveryTargetOccupiedTx(ctx, tx, companion.TargetPath, candidate.PlacementID)
		if takeErr != nil {
			return present, missing, false, takeErr
		}
		if taken {
			return present, missing, true, nil
		}
		present = append(present, companion)
	}
	return present, missing, false, nil
}

// recoveryTargetOccupiedTx reports whether the exact path is already owned by
// another active placement or companion (no second owner, ever).
func recoveryTargetOccupiedTx(ctx context.Context, tx *sql.Tx, path, exceptPlacementID string) (bool, error) {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" {
		return true, nil
	}
	var occupied bool
	err := tx.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1 FROM placements p
  WHERE p.physical_path COLLATE "C" = $1 AND p.state = 'active' AND p.id <> $2
) OR EXISTS(
  SELECT 1 FROM placement_companions c
  WHERE c.current_path COLLATE "C" = $1 AND c.state = 'active' AND c.placement_id <> $2
)`, cleaned, exceptPlacementID).Scan(&occupied)
	if err != nil {
		return false, fmt.Errorf("read recovery target occupancy: %w", err)
	}
	return occupied, nil
}

type writeRecoveryOwnershipInput struct {
	animeID         string
	placementID     string
	targetPath      string
	facts           domain.MetadataFacts
	fingerprint     string
	generation      int64
	companions      []recoveryCompanionFact
	recoveredAt     time.Time
	previousVersion int64
}

// writeRecoveryOwnershipTx re-points the ownership rows inside the confirm's
// transaction and records the business journal. source_evidence_type flips to
// `recovery` (an evidence kind the placements table already admits), the
// target fingerprint refreshes to the observed metadata facts (the scan's
// NOT NULL column contract holds through the typed envelope), and
// snapshot_generation carries the generation the user confirmed.
func (s *Service) writeRecoveryOwnershipTx(ctx context.Context, tx *sql.Tx, input writeRecoveryOwnershipInput) error {
	targetFingerprint, err := json.Marshal(map[string]any{
		"algorithm":  "metadata",
		"value":      input.fingerprint,
		"size_bytes": input.facts.SizeBytes,
	})
	if err != nil {
		return err
	}
	tagged, err := tagRecoveryGenerationTx(ctx, tx, input.animeID, input.placementID, input.generation)
	if err != nil {
		return err
	}
	updated, err := tx.ExecContext(ctx, `
UPDATE placements SET
  physical_path = $2,
  target_fingerprint = $3,
  source_evidence_type = 'recovery',
  snapshot_generation = $4,
  version = version + 1,
  updated_at = $5
WHERE id = $1 AND state = 'active' AND verification_state = 'verified'`,
		input.placementID, filepath.Clean(input.targetPath), string(targetFingerprint),
		input.generation, input.recoveredAt)
	if err != nil {
		return fmt.Errorf("re-attach placement: %w", err)
	}
	if affected, err := updated.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return fmt.Errorf("recovery placement %s changed concurrently", input.placementID)
	}
	for _, companion := range input.companions {
		// The companion column holds a hex sha256 digest; the scan froze the
		// content-observed fingerprint at the rendered path, so re-attach
		// keeps exactly that identity.
		fingerprint := strings.TrimSpace(deref(companion.Fingerprint))
		if fingerprint == "" {
			return fmt.Errorf("recovery companion %s has no frozen fingerprint", companion.ID)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE placement_companions SET
  current_path = $2,
  fingerprint = $3,
  size_bytes = $4,
  version = version + 1,
  updated_at = $5
WHERE id = $1 AND state = 'active' AND placement_id = $6`,
			companion.ID, filepath.Clean(companion.TargetPath), fingerprint,
			companion.SizeBytes, input.recoveredAt, input.placementID); err != nil {
			return fmt.Errorf("re-attach companion: %w", err)
		}
	}
	if err := s.appendRecoveryActivityTx(ctx, tx, input, tagged); err != nil {
		return err
	}
	return nil
}

// appendRecoveryActivityTx records the confirm in the durable business
// journal. The dedupe key makes a replay of the same confirm (same placement,
// same generation) a no-op instead of a second entry.
func (s *Service) appendRecoveryActivityTx(ctx context.Context, tx *sql.Tx, input writeRecoveryOwnershipInput, tagged bool) error {
	if s.recoveryActivity == nil {
		return nil
	}
	subscriptionID, err := subscriptionIDForAnime(ctx, tx, input.animeID)
	if err != nil {
		return fmt.Errorf("read subscription for recovery activity: %w", err)
	}
	title, err := activityAnimeTitle(ctx, tx, input.animeID)
	if err != nil {
		return fmt.Errorf("read anime title for recovery activity: %w", err)
	}
	details, err := businesslog.MarshalDetails(map[string]any{
		"file_name":      businesslog.FileName(input.targetPath),
		"library_folder": businesslog.LibraryFolder(input.targetPath),
		"generation":     input.generation,
		"companions":     len(input.companions),
		"tagged":         tagged,
	})
	if err != nil {
		return fmt.Errorf("encode recovery activity details: %w", err)
	}
	return s.recoveryActivity.AppendTx(ctx, txscope.Wrap(tx), observabilityport.ActivityRecord{
		SubscriptionID: subscriptionID,
		ManagedAnimeID: input.animeID,
		Kind:           "placement_changed",
		Outcome:        "success",
		ReasonCode:     "file_recovered",
		SubjectType:    "placement",
		SubjectID:      input.placementID,
		SubjectVersion: input.previousVersion + 1,
		DedupeKey:      fmt.Sprintf("recovery:%s:%d", input.placementID, input.generation),
		Title:          title,
		Summary:        "文件已恢复对齐",
		Details:        details,
		CausationType:  "reconciliation_scan",
		CausationID:    fmt.Sprintf("recon:scan:%s:%d", input.animeID, input.generation),
	})
}

// SetRecoveryActivityWriter wires the durable business journal (the
// observability service implements the port); nil keeps the confirm silent
// like every other unwired activity owner.
func (s *Service) SetRecoveryActivityWriter(writer observabilityport.ActivityWriter) {
	if s != nil {
		s.recoveryActivity = writer
	}
}

// tagRecoveryGenerationTx marks this placement's newest candidate row with
// recovered_generation so a replay recognizes the confirm and the read model
// can stop offering the action. It returns whether the mark was new.
func tagRecoveryGenerationTx(ctx context.Context, tx *sql.Tx, animeID, placementID string, generation int64) (bool, error) {
	tag, err := json.Marshal(map[string]any{"recovered_generation": generation})
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE reconciliation_files SET facts = facts || $3::jsonb
WHERE managed_anime_id = $1 AND generation = $2
  AND facts->>'placement_id' = $4 AND facts->'recovery_candidates' IS NOT NULL
  AND NOT (facts ? 'recovered_generation')`,
		animeID, generation, string(tag), placementID)
	if err != nil {
		return false, fmt.Errorf("tag recovery generation: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func countActiveCompanionsTx(ctx context.Context, tx *sql.Tx, placementID string) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM placement_companions WHERE placement_id = $1 AND state = 'active'`, placementID).Scan(&count)
	return count, err
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
