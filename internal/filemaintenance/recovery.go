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
// (reconciliation_files.facts.recovery_candidates[]). The scan wrote it only
// after the COMPLETE rendered unit qualified (target differs from every
// missing locator, frozen expected size, complete companions at their
// rendered paths with the registered sizes, no other owner); the confirm
// re-reads it as the user's decision token and re-verifies every fact inside
// one fail-closed transaction before the placement re-attaches.

type recoveryCandidateFact struct {
	PlacementID       string                  `json:"placement_id"`
	PlacementVersion  int64                   `json:"placement_version"`
	Generation        int64                   `json:"generation"`
	OldPhysicalPath   string                  `json:"old_physical_path"`
	TargetPath        string                  `json:"target_path"`
	TargetFingerprint string                  `json:"target_fingerprint"`
	SizeBytes         int64                   `json:"size_bytes"`
	Companions        []recoveryCompanionFact `json:"companions"`
}

type recoveryCompanionFact struct {
	ID          string `json:"id"`
	Version     int64  `json:"version"`
	TargetPath  string `json:"target_path"`
	Fingerprint string `json:"fingerprint"`
	SizeBytes   int64  `json:"size_bytes"`
}

// readRecoveryCandidateTx reads the candidate of the EXACT requested
// generation from that generation's immutable observation row. ok=false is
// the typed candidate_gone outcome: that generation carries no candidate for
// the placement. A later generation never answers for this read: the confirm
// gates on the latest calibrated generation separately.
func readRecoveryCandidateTx(ctx context.Context, tx *sql.Tx, animeID string, placementID string, generation int64) (recoveryCandidateFact, bool, error) {
	var factsRaw []byte
	err := tx.QueryRowContext(ctx, `
SELECT rf.facts
FROM reconciliation_files rf
WHERE rf.managed_anime_id = $1 AND rf.generation = $2
  AND rf.facts->>'placement_id' = $3
  AND rf.facts->'recovery_candidates' IS NOT NULL
  AND rf.facts->>'disposition' = 'missing'`, animeID, generation, placementID).
		Scan(&factsRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return recoveryCandidateFact{}, false, nil
	}
	if err != nil {
		return recoveryCandidateFact{}, false, fmt.Errorf("read recovery candidate: %w", err)
	}
	var document struct {
		RecoveryCandidates []recoveryCandidateFact `json:"recovery_candidates"`
	}
	if err := json.Unmarshal(factsRaw, &document); err != nil {
		return recoveryCandidateFact{}, false, fmt.Errorf("decode recovery candidate: %w", err)
	}
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
	AnimeID     string
	PlacementID string
	Generation  int64
	RecoveredAt time.Time
	// TargetPath is the path the placement owns after the recovery (the
	// candidate's rendered target; also the replay's answer).
	TargetPath string
	Replay     bool
	Companions int
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
	RecoveryReasonScanRunning       = "scan_running"
	RecoveryReasonPlacementChanged  = "placement_changed"
	RecoveryReasonTargetMissing     = "target_missing"
	RecoveryReasonTargetChanged     = "target_changed"
	RecoveryReasonTargetOccupied    = "target_occupied"
	RecoveryReasonCompanionOccupied = "companion_occupied"
	RecoveryReasonCompanionMissing  = "companion_missing"
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

// AnimeFileRecoverCommand is the frozen confirmation token the read model
// showed the user: every field must still match inside the confirm's
// transaction, so a stale dialog can never recover a different state.
type AnimeFileRecoverCommand struct {
	PlacementID      string
	Generation       int64
	TargetPath       string
	Fingerprint      string
	PlacementVersion int64
}

// RecoverAnimeFileApplication is the user's explicit recovery confirm. It
// re-attaches the user-adopted file at the candidate's rendered target to the
// placement that rendered it, TOGETHER with its complete companion unit: the
// only path a scan-alignment recovery candidate ever becomes an ownership
// change.
//
// Fail-closed order (steps 2-9 inside ONE transaction):
//  1. the anime admits manual commands (lifecycle + subscription state);
//  2. the latest calibrated generation equals the requested generation and no
//     scan/calibration is live (a newer or in-flight generation refuses as
//     scan_changed / scan_running);
//  3. the candidate fact is read from the EXACT generation's immutable row
//     and every request field matches it (candidate_gone otherwise);
//  4. the placement row is re-read FOR UPDATE: still active+verified, still
//     at the candidate's frozen old path, version exactly the frozen one, and
//     its identity is NOT already the recovery identity at this generation
//     (that branch is the replay below — never a silent fallthrough);
//  5. the target file is re-stat'ed no-follow: regular file, size equal, and
//     the metadata fingerprint matched with the shared size+inode semantics
//     (the device number is deliberately not compared: an NFS remount must
//     not read as a content change);
//  6. EVERY companion of the frozen unit is re-stat'ed: all must sit at
//     their frozen paths with frozen size and fingerprint — one missing,
//     changed or occupied companion refuses the whole confirm (companion
//     recovery is atomic: video + companions or nothing);
//  7. no other active placement/companion relation and no unfinished owner
//     may hold the target or any companion path (no second owner, ever);
//  8. the placement re-attaches (physical_path = target, target fingerprint
//     refreshed to the observed metadata facts through the typed envelope the
//     column contract holds, source_evidence_type = `recovery`,
//     snapshot_generation = the confirmed generation) under a CAS guard on
//     the frozen version, and every companion re-attaches at its frozen path
//     with its OWN version CAS — keeping its existing hex digest, its
//     existing identity is never rewritten (no content re-read, no forged
//     digest);
//  9. the business journal records the confirm once (dedupe key).
//
// Replay of the same committed confirm (step 4's identity branch) re-verifies
// steps 2/3/5/6/7 against the CURRENT files and the successor version, then
// returns the same result without bumping a version or writing a second
// journal entry.
//
// No byte is ever moved, copied, overwritten or deleted: the file the user
// adopted stays exactly where it is; only ownership rows re-point.
func (s *Service) RecoverAnimeFileApplication(ctx context.Context, animeID string, command AnimeFileRecoverCommand) (AnimeFilesRecoverResult, error) {
	result := AnimeFilesRecoverResult{AnimeID: animeID, PlacementID: command.PlacementID, TargetPath: command.TargetPath}
	if s == nil || s.placements == nil || s.placements.db == nil || s.recoveryObserver == nil {
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

	// (2) the read the user confirmed is still the newest decided generation
	// and nothing is in flight (a live scan could re-decide this row).
	latest, _, err := latestCalibratedGeneration(ctx, tx, animeID)
	if err != nil {
		return result, err
	}
	if latest != command.Generation {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonScanChanged}
	}
	if live, err := recoveryScanLiveTx(ctx, tx, animeID); err != nil {
		return result, err
	} else if live {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonScanRunning}
	}

	// (3) the frozen candidate of that exact generation; every request field
	// must match it (the candidate is immutable so this read is stable).
	candidate, ok, err := readRecoveryCandidateTx(ctx, tx, animeID, command.PlacementID, command.Generation)
	if err != nil {
		return result, err
	}
	if !ok {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonCandidateGone}
	}
	if err := matchRecoveryCommand(candidate, command); err != nil {
		return result, err
	}
	result.Generation = candidate.Generation
	result.TargetPath = candidate.TargetPath

	// (4b) the CURRENT naming rules must still render the whole unit to the
	// frozen paths: a rule change after the scan moves the target, and the
	// old path must never be re-attached (both confirm and replay).
	if err := verifyRecoveryRenderedTargets(ctx, s.recoveryTargetRenderer(), animeID, candidate); err != nil {
		var refusal *AnimeFilesRecoverRefusal
		if errors.As(err, &refusal) {
			return result, refusal
		}
		return result, err
	}

	placement, ok, err := readRecoveryPlacementTx(ctx, tx, animeID, command.PlacementID)
	if err != nil {
		return result, err
	}
	if !ok || placement.State != "active" || placement.VerificationState != "verified" {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonPlacementChanged}
	}
	if placement.SourceEvidenceType == "recovery" && placement.SnapshotGeneration == candidate.Generation {
		// Replay: the committed confirm owns this exact generation. Re-verify
		// the world is still the one the confirm created, then answer without
		// touching any row.
		return s.replayRecoveryConfirm(ctx, s.recoveryObserver, tx, candidate, placement, result)
	}
	if placement.Version != candidate.PlacementVersion {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonPlacementChanged}
	}
	if !sameCleanPath(placement.PhysicalPath, candidate.OldPhysicalPath) {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonPlacementChanged}
	}

	// (5) the target file must still be exactly what the scan stat'ed.
	observation, err := observeRecoveryTarget(ctx, s.recoveryObserver, candidate.TargetPath)
	if err != nil {
		return result, err
	}
	if observation.SizeBytes != candidate.SizeBytes || !domain.MetadataFingerprintsMatch(observation.Fingerprint, candidate.TargetFingerprint) {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonTargetChanged}
	}
	facts, ok := domain.ParseMetadataFingerprint(observation.Fingerprint)
	if !ok {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonTargetChanged}
	}

	// (6) the COMPLETE companion unit must still sit at its frozen paths with
	// the frozen identity; one gap refuses the whole confirm.
	if err := verifyRecoveryCompanionsTx(ctx, s.recoveryObserver, tx, candidate); err != nil {
		var refusal *AnimeFilesRecoverRefusal
		if errors.As(err, &refusal) {
			return result, refusal
		}
		return result, err
	}

	// (7) no second owner on the target or any companion path.
	paths := []string{candidate.TargetPath}
	for _, companion := range candidate.Companions {
		paths = append(paths, companion.TargetPath)
	}
	occupied, err := recoveryTargetsOccupiedTx(ctx, tx, paths, command.PlacementID)
	if err != nil {
		return result, err
	}
	if occupied {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonTargetOccupied}
	}

	// (8)+(9) re-attach ownership rows and record the journal inside the same
	// transaction; the commit makes the recovery durable as one boundary.
	recoveredAt := time.Now().UTC()
	if err := s.writeRecoveryOwnershipTx(ctx, tx, writeRecoveryOwnershipInput{
		animeID:         animeID,
		candidate:       candidate,
		facts:           facts,
		fingerprint:     observation.Fingerprint,
		companions:      candidate.Companions,
		recoveredAt:     recoveredAt,
		previousVersion: placement.Version,
	}); err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	result.RecoveredAt = recoveredAt
	result.Companions = len(candidate.Companions)
	return result, nil
}

// matchRecoveryCommand compares the request token with the frozen candidate
// field by field; a mismatch refuses with the typed reason the mismatch
// means (the row token is placement_changed, everything at the target is
// target_changed, a gone candidate is candidate_gone).
func matchRecoveryCommand(candidate recoveryCandidateFact, command AnimeFileRecoverCommand) error {
	if command.PlacementVersion != candidate.PlacementVersion {
		return &AnimeFilesRecoverRefusal{Code: RecoveryReasonPlacementChanged}
	}
	if !sameCleanPath(command.TargetPath, candidate.TargetPath) {
		return &AnimeFilesRecoverRefusal{Code: RecoveryReasonTargetChanged}
	}
	if strings.TrimSpace(command.Fingerprint) != candidate.TargetFingerprint {
		return &AnimeFilesRecoverRefusal{Code: RecoveryReasonTargetChanged}
	}
	return nil
}

// replayRecoveryConfirm answers an already-committed confirm: the placement
// must still sit at the target with the successor version, the target and the
// complete companion unit must still match their frozen identities, and no
// other owner may hold any unit path. No version bump, no journal write.
func (s *Service) replayRecoveryConfirm(
	ctx context.Context,
	observer RecoveryFileObserver,
	tx *sql.Tx,
	candidate recoveryCandidateFact,
	placement recoveryPlacementRow,
	result AnimeFilesRecoverResult,
) (AnimeFilesRecoverResult, error) {
	if placement.Version != candidate.PlacementVersion+1 || !sameCleanPath(placement.PhysicalPath, candidate.TargetPath) {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonPlacementChanged}
	}
	observation, err := observeRecoveryTarget(ctx, observer, candidate.TargetPath)
	if err != nil {
		return result, err
	}
	if observation.SizeBytes != candidate.SizeBytes || !domain.MetadataFingerprintsMatch(observation.Fingerprint, candidate.TargetFingerprint) {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonTargetChanged}
	}
	if err := verifyRecoveryCompanionsTx(ctx, observer, tx, candidate); err != nil {
		var refusal *AnimeFilesRecoverRefusal
		if errors.As(err, &refusal) {
			return result, refusal
		}
		return result, err
	}
	paths := []string{candidate.TargetPath}
	for _, companion := range candidate.Companions {
		paths = append(paths, companion.TargetPath)
	}
	occupied, err := recoveryTargetsOccupiedTx(ctx, tx, paths, candidate.PlacementID)
	if err != nil {
		return result, err
	}
	if occupied {
		return result, &AnimeFilesRecoverRefusal{Code: RecoveryReasonTargetOccupied}
	}
	result.Replay = true
	result.RecoveredAt = placement.UpdatedAt
	result.Companions = len(candidate.Companions)
	return result, nil
}

// observeRecoveryTarget stats one candidate path no-follow; an error becomes
// the typed target_missing refusal (a cancelled context passes through).
func observeRecoveryTarget(ctx context.Context, observer RecoveryFileObserver, path string) (domain.FileObservation, error) {
	observation, err := observer.ObserveFile(ctx, path)
	if err != nil {
		if ctx.Err() != nil {
			return domain.FileObservation{}, ctx.Err()
		}
		return domain.FileObservation{}, &AnimeFilesRecoverRefusal{Code: RecoveryReasonTargetMissing}
	}
	return observation, nil
}

// ---- transaction helpers ---------------------------------------------------

type recoveryPlacementRow struct {
	State              string
	VerificationState  string
	SourceEvidenceType string
	SnapshotGeneration int64
	Version            int64
	PhysicalPath       string
	UpdatedAt          time.Time
}

// readRecoveryPlacementTx locks the placement row (FOR UPDATE) and reads the
// identity facts the confirm re-verifies.
func readRecoveryPlacementTx(ctx context.Context, tx *sql.Tx, animeID, placementID string) (recoveryPlacementRow, bool, error) {
	var row recoveryPlacementRow
	var updatedAt sql.NullTime
	err := tx.QueryRowContext(ctx, `
SELECT p.state, p.verification_state, p.source_evidence_type,
       COALESCE(p.snapshot_generation, 0), p.version, p.physical_path, p.updated_at
FROM placements p WHERE p.id = $1 AND p.managed_anime_id = $2
FOR UPDATE`, placementID, animeID).
		Scan(&row.State, &row.VerificationState, &row.SourceEvidenceType,
			&row.SnapshotGeneration, &row.Version, &row.PhysicalPath, &updatedAt)
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

// recoveryScanLiveTx reports whether a scan or its calibration is still in
// flight for the anime: the generation the user confirmed could re-decide the
// row under them.
func recoveryScanLiveTx(ctx context.Context, tx *sql.Tx, animeID string) (bool, error) {
	var live bool
	err := tx.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM reconciliation_scans WHERE managed_anime_id = $1 AND state = 'running')
    OR EXISTS(SELECT 1 FROM work_items
              WHERE owner_type = 'file_maintenance' AND owner_id = $1
                AND kind IN ('reconciliation_scan', 'reconciliation_calibrate')
                AND state IN ('available', 'leased'))`, animeID).Scan(&live)
	if err != nil {
		return false, fmt.Errorf("read recovery scan liveness: %w", err)
	}
	return live, nil
}

// verifyRecoveryCompanionsTx re-stats EVERY companion of the frozen unit. A
// vanished, changed or taken companion refuses the WHOLE confirm (the video
// is never recovered without its complete bound unit).
func verifyRecoveryCompanionsTx(ctx context.Context, observer RecoveryFileObserver, tx *sql.Tx, candidate recoveryCandidateFact) error {
	if err := verifyRecoveryCompanionStats(ctx, observer, candidate); err != nil {
		return err
	}
	for _, companion := range candidate.Companions {
		taken, takeErr := recoveryTargetOccupiedTx(ctx, tx, companion.TargetPath, candidate.PlacementID)
		if takeErr != nil {
			return takeErr
		}
		if taken {
			return &AnimeFilesRecoverRefusal{Code: RecoveryReasonCompanionOccupied}
		}
	}
	return nil
}

// verifyRecoveryCompanionStats re-stats every companion of the unit and
// compares it against the frozen fact: one vanished or changed companion
// refuses the whole confirm (no partial recovery).
func verifyRecoveryCompanionStats(ctx context.Context, observer RecoveryFileObserver, candidate recoveryCandidateFact) error {
	for _, companion := range candidate.Companions {
		observation, err := observer.ObserveFile(ctx, companion.TargetPath)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &AnimeFilesRecoverRefusal{Code: RecoveryReasonCompanionMissing}
		}
		// Same identity contract as the video: size plus metadata fingerprint
		// (path + size + inode), with the device number excluded so an NFS
		// remount is not a change. A size or inode change fails closed.
		if observation.SizeBytes != companion.SizeBytes || !domain.MetadataFingerprintsMatch(observation.Fingerprint, companion.Fingerprint) {
			return &AnimeFilesRecoverRefusal{Code: RecoveryReasonCompanionOccupied}
		}
	}
	return nil
}

// recoveryTargetOccupiedTx reports whether the exact path is already owned by
// another active placement or companion relation, or held by any unfinished
// owner. Confirm and replay both go through it, and it is the SAME read the
// scan probe performs (recoveryPathOccupancyQuery), so the offered candidate
// and the accepted confirm can never disagree about ownership.
func recoveryTargetOccupiedTx(ctx context.Context, tx *sql.Tx, path, exceptPlacementID string) (bool, error) {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" {
		return true, nil
	}
	return recoveryPathsOccupied(ctx, tx, []string{cleaned}, exceptPlacementID)
}

// recoveryTargetsOccupiedTx reports whether ANY of the unit paths is already
// taken. The whole unit is checked in one read so a partially-occupied unit
// refuses atomically.
func recoveryTargetsOccupiedTx(ctx context.Context, tx *sql.Tx, paths []string, exceptPlacementID string) (bool, error) {
	cleaned := make([]string, 0, len(paths))
	for _, path := range paths {
		trimmed := filepath.Clean(strings.TrimSpace(path))
		if trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	if len(cleaned) != len(paths) {
		// A unit path that cleans to nothing is a malformed unit: fail closed.
		return true, nil
	}
	return recoveryPathsOccupied(ctx, tx, cleaned, exceptPlacementID)
}

type writeRecoveryOwnershipInput struct {
	animeID         string
	candidate       recoveryCandidateFact
	facts           domain.MetadataFacts
	fingerprint     string
	companions      []recoveryCompanionFact
	recoveredAt     time.Time
	previousVersion int64
}

// writeRecoveryOwnershipTx re-points the ownership rows inside the confirm's
// transaction and records the business journal. The placement update is a CAS
// on the frozen scan version; the target fingerprint refreshes to the
// observed metadata facts through the typed envelope (the NOT NULL column
// contract holds, the identity is re-pointed, never forged); each companion
// re-attaches under a CAS on its own version and KEEPS its existing hex
// digest and size — a companion's content identity is never rewritten.
func (s *Service) writeRecoveryOwnershipTx(ctx context.Context, tx *sql.Tx, input writeRecoveryOwnershipInput) error {
	targetFingerprint, err := json.Marshal(map[string]any{
		"algorithm":  "metadata",
		"value":      input.fingerprint,
		"size_bytes": input.facts.SizeBytes,
	})
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
WHERE id = $1 AND state = 'active' AND verification_state = 'verified' AND version = $6`,
		input.candidate.PlacementID, filepath.Clean(input.candidate.TargetPath), string(targetFingerprint),
		input.candidate.Generation, input.recoveredAt, input.previousVersion)
	if err != nil {
		return fmt.Errorf("re-attach placement: %w", err)
	}
	if affected, err := updated.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return fmt.Errorf("recovery placement %s changed concurrently", input.candidate.PlacementID)
	}
	for _, companion := range input.companions {
		result, err := tx.ExecContext(ctx, `
UPDATE placement_companions SET
  current_path = $2,
  version = version + 1,
  updated_at = $3
WHERE id = $1 AND state = 'active' AND placement_id = $4 AND version = $5`,
			companion.ID, filepath.Clean(companion.TargetPath), input.recoveredAt,
			input.candidate.PlacementID, companion.Version)
		if err != nil {
			return fmt.Errorf("re-attach companion: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("recovery companion %s changed concurrently", companion.ID)
		}
	}
	if err := s.appendRecoveryActivityTx(ctx, tx, input); err != nil {
		return err
	}
	return nil
}

// appendRecoveryActivityTx records the confirm in the durable business
// journal. The dedupe key makes a replay of the same confirm (same placement,
// same generation) a no-op instead of a second entry.
func (s *Service) appendRecoveryActivityTx(ctx context.Context, tx *sql.Tx, input writeRecoveryOwnershipInput) error {
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
		"file_name":      businesslog.FileName(input.candidate.TargetPath),
		"library_folder": businesslog.LibraryFolder(input.candidate.TargetPath),
		"generation":     input.candidate.Generation,
		"companions":     len(input.companions),
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
		SubjectID:      input.candidate.PlacementID,
		SubjectVersion: input.previousVersion + 1,
		DedupeKey:      fmt.Sprintf("recovery:%s:%d", input.candidate.PlacementID, input.candidate.Generation),
		Title:          title,
		Summary:        "文件已恢复对齐",
		Details:        details,
		CausationType:  "reconciliation_scan",
		CausationID:    fmt.Sprintf("recon:scan:%s:%d", input.animeID, input.candidate.Generation),
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

// activeRecoveryUnitCompanions reads the placement's CURRENT active companion
// unit (ids only, ordered) so the render check compares against what the unit
// is now, not just what the scan froze: a companion registered or retired
// since the scan makes the frozen unit stale.
func activeRecoveryUnitCompanions(ctx context.Context, db *sql.DB, placementID string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
SELECT c.id FROM placement_companions c
WHERE c.placement_id = $1 AND c.state = 'active'
ORDER BY c.id`, placementID)
	if err != nil {
		return nil, fmt.Errorf("read recovery unit companions: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// verifyRecoveryRenderedTargets re-renders, under the anime's CURRENT naming
// rules, the placement's target and the target of every CURRENTLY active
// companion, and requires the result to be EXACTLY the frozen unit:
//
//   - the renderer is required (nil fails closed): a rule change after the
//     scan moves the target, and the old scanned path must never be
//     re-attached;
//   - the active companion id set must equal the frozen one (an added or
//     retired companion makes the frozen unit stale);
//   - every rendered path must equal the frozen path byte for byte.
//
// A missing render is target_changed for the video and companion_missing for
// a companion (the reason the operator can act on); an I/O failure is
// returned as the underlying error so the caller fails closed without
// pretending the unit changed.
func verifyRecoveryRenderedTargets(ctx context.Context, renderer *NamingTargetRenderer, animeID string, candidate recoveryCandidateFact) error {
	if renderer == nil {
		return &AnimeFilesRecoverRefusal{Code: RecoveryReasonTargetChanged}
	}
	activeIDs, err := activeRecoveryUnitCompanions(ctx, renderer.db, candidate.PlacementID)
	if err != nil {
		return err
	}
	if len(activeIDs) != len(candidate.Companions) {
		return &AnimeFilesRecoverRefusal{Code: RecoveryReasonCompanionMissing}
	}
	frozen := make(map[string]recoveryCompanionFact, len(candidate.Companions))
	for _, companion := range candidate.Companions {
		frozen[companion.ID] = companion
	}
	for _, id := range activeIDs {
		if _, ok := frozen[id]; !ok {
			// A companion registered after the scan: the frozen unit is
			// not the current unit.
			return &AnimeFilesRecoverRefusal{Code: RecoveryReasonCompanionMissing}
		}
	}
	targets, err := renderer.RenderPlacementTargets(ctx, animeID, []string{candidate.PlacementID})
	if err != nil {
		return fmt.Errorf("render recovery unit targets: %w", err)
	}
	video, ok := targets[candidate.PlacementID]
	if !ok || !sameCleanPath(video, candidate.TargetPath) {
		return &AnimeFilesRecoverRefusal{Code: RecoveryReasonTargetChanged}
	}
	for _, companion := range candidate.Companions {
		path, ok := targets[companion.ID]
		if !ok || !sameCleanPath(path, companion.TargetPath) {
			return &AnimeFilesRecoverRefusal{Code: RecoveryReasonCompanionMissing}
		}
	}
	return nil
}

// recoveryTargetRenderer is the renderer the confirm re-renders through: the
// SAME instance the files read model previews with (s.pathDiff.renderer), so
// the target a user sees and the target the confirm demands can never be
// produced by two different rule sets. nil stays nil and fails closed in
// verifyRecoveryRenderedTargets.
func (s *Service) recoveryTargetRenderer() *NamingTargetRenderer {
	if s == nil || s.pathDiff == nil {
		return nil
	}
	return s.pathDiff.renderer
}

// sameCleanPath compares two paths after cleaning (the scan's own path
// identity for explicit stat targets).
func sameCleanPath(left, right string) bool {
	return filepath.Clean(strings.TrimSpace(left)) == filepath.Clean(strings.TrimSpace(right))
}
