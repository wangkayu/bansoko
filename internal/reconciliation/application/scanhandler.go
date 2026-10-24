package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"bansoko/internal/library/domain"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// batchSize bounds one reconciliation work item so long hash I/O never blocks
// a lane indefinitely.
const batchSize = 200

// ScanTreeTop is the decoded scan scope of one generation: the tree top the
// scan was scoped to.
type ScanTreeTop struct {
	TreeTop string `json:"tree_top"`
}

// mustJSON encodes a value that is statically encodable (map of primitives).
func mustJSON(value map[string]any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

// ScanHandler processes one reconciliation_scan work item: it scans and
// hashes files in batches, persists immutable observations, and advances the
// owner checkpoint inside the claim's fencing. A cancelled context (lease
// loss, worker shutdown) stops enumeration/hash and leaves the owner row
// running so a reclaimed claim resumes from the last committed batch.
func (s *Service) ScanHandler() worker.Handler {
	return scanRecoveryHandler{service: s}
}

func (s *Service) handleScan(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	if claim.Kind != workitems.KindReconciliationScan {
		return worker.Outcome{}, fmt.Errorf("reconciliation handler received kind %q", claim.Kind)
	}
	owner, err := loadScanOwner(ctx, s.db, claim.Owner.ID)
	if err != nil {
		return worker.Outcome{}, err
	}
	if owner == nil {
		return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "scan_owner_invalid", Reason: "scan owner is not running"}, nil), nil
	}
	if owner.State == ScanCompleted {
		// Crash replay after the completion transaction committed: the
		// generation is immutable and the calibrate item exists (same
		// transaction). Repair a legacy completed generation that predates the
		// calibrate contract, then report the already-completed truth.
		if exists, err := s.calibrateItemExists(ctx, claim.Owner.ID); err != nil {
			return worker.Outcome{}, err
		} else if !exists {
			var scope ScanTreeTop
			if err := json.Unmarshal(owner.ScanScope, &scope); err != nil || scope.TreeTop == "" {
				return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "scan_scope_invalid", Reason: "scan scope is missing tree_top"}, nil), nil
			}
			if err := s.ensureCalibrateItem(ctx, owner, scope); err != nil {
				return worker.Outcome{}, err
			}
		}
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "completed", ReasonCode: "scan_completed", Reason: "generation already completed and calibrated"}, nil), nil
	}
	if owner.State != ScanRunning {
		return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "scan_owner_invalid", Reason: "scan owner is not running"}, nil), nil
	}
	if owner.Version != claim.ExpectedOwnerVersion {
		// Stale fencing: the owner advanced past this claim's frozen version.
		return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "scan_fencing_stale", Reason: "stale fencing token cannot commit scan progress"}, nil), nil
	}

	var scope ScanTreeTop
	if err := json.Unmarshal(owner.ScanScope, &scope); err != nil || scope.TreeTop == "" {
		return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "scan_scope_invalid", Reason: "scan scope is missing tree_top"}, nil), nil
	}

	var progress struct {
		LastCommittedBatch int   `json:"last_committed_batch"`
		ScannedFiles       int64 `json:"scanned_files"`
		HashedFiles        int64 `json:"hashed_files"`
	}
	_ = json.Unmarshal(owner.Progress, &progress)

	// Placement-owned scan: the observation set is the instance's verified
	// active Placements (current paths plus bounded provenance fallbacks), NOT
	// a full-library walk. Sibling-anime, unowned and retained-source files can
	// never enter this generation because the scanner only observes the
	// explicit locator paths.
	placements, err := s.loadOwnedPlacements(ctx, claim.Owner.ID)
	if err != nil {
		return s.failScan(ctx, claim, owner, "scan_placements_failed", err.Error())
	}
	paths := make([]string, 0, len(placements))
	for _, placement := range placements {
		paths = append(paths, placement.LocatorPaths...)
		for _, companion := range placement.Companions {
			paths = append(paths, companion.Path)
		}
	}
	result, err := s.scanner.Scan(ctx, domain.ScanRequest{
		TreeTop: scope.TreeTop,
		Scope:   domain.ScanScope{Kind: domain.ScanScopePlacementOwned},
		Paths:   paths,
	})
	if err != nil {
		if ctx.Err() != nil {
			// Cancelled (lease loss/shutdown): keep the owner running so a
			// reclaimed claim resumes from the checkpoint.
			return worker.Outcome{}, ctx.Err()
		}
		return s.failScan(ctx, claim, owner, "scan_failed", err.Error())
	}
	found := make(map[string]domain.ScanFile, len(result.Files))
	for _, file := range result.Files {
		found[filepath.Clean(file.Path)] = file
	}
	observations := make([]placementObservation, 0, len(placements))
	for _, placement := range placements {
		observation := placementObservation{
			placementID:  placement.ID,
			physicalPath: placement.PhysicalPath,
			locatedVia:   "current_path",
			resolution:   placement.Resolution,
			group:        placement.Group,
			language:     placement.Language,
		}
		for _, locator := range placement.LocatorPaths {
			if file, ok := found[filepath.Clean(locator)]; ok {
				file := file
				observation.file = &file
				if locator != placement.PhysicalPath {
					observation.locatedVia = "source_provenance"
				}
				break
			}
		}
		observation.companions = observeCompanions(placement.Companions, found)
		observations = append(observations, observation)
	}

	start := (progress.LastCommittedBatch + 1) * batchSize
	if start > len(observations) {
		start = len(observations)
	}
	var scanned, hashed, bytes int64
	for batchIndex := progress.LastCommittedBatch + 1; start < len(observations); batchIndex++ {
		if err := ctx.Err(); err != nil {
			return worker.Outcome{}, err // stop before the next batch
		}
		end := start + batchSize
		if end > len(observations) {
			end = len(observations)
		}
		batch := observations[start:end]
		if err := s.commitBatch(ctx, claim, owner, batchIndex, batch); err != nil {
			if ctx.Err() != nil {
				return worker.Outcome{}, ctx.Err()
			}
			return s.failScan(ctx, claim, owner, "batch_commit_failed", err.Error())
		}
		scanned += int64(len(batch))
		hashed += int64(len(batch))
		for _, obs := range batch {
			if obs.file != nil {
				bytes += obs.file.SizeBytes
			}
		}
		start = end
	}

	// Target pass of the same run: the frozen acquisition targets are stat'ed
	// and their latest TargetObservation rows replaced, so the RSS row status
	// reflects this scan. It is a stat only (no filesystem effect) and runs
	// before completion: a failure leaves the owner running and the claim
	// retries from the committed checkpoint.
	if s.targets != nil {
		if err := s.targets.RefreshObservations(ctx, claim.Owner.ID); err != nil {
			if ctx.Err() != nil {
				return worker.Outcome{}, ctx.Err()
			}
			return worker.Outcome{}, fmt.Errorf("refresh target observations: %w", err)
		}
	}

	// Complete (freeze the generation) in the owner's own transaction. The
	// calibration work item is created in the SAME transaction: calibration
	// exists iff the completion committed, so a crash between the commit and
	// the handler's finish is repaired by a replay (the completed owner is
	// re-checked below and the calibrate item is a no-op).
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return worker.Outcome{}, err
	}
	defer tx.Rollback()
	if err := s.workItems.AdvanceClaimOwnerVersion(ctx, txscope.Wrap(tx), claim, owner.Version); err != nil {
		return worker.Outcome{}, err
	}
	if err := completeScanOwner(ctx, tx, claim.Owner.ID, owner.Generation, owner.Version, scanned, hashed, bytes); err != nil {
		return worker.Outcome{}, err
	}
	// The calibration consumer owner runs in its own claim. The item uses a
	// stable per-anime key and CreateOrMerge: the live unique owner+kind index
	// allows only one live calibrate item, so a newer completion merges the
	// newest generation into the item instead of failing the scan completion.
	// A merge supersedes older pending calibrations (their observations stay
	// immutable and readable; the newest generation calibrates current facts).
	if _, _, err := s.workItems.CreateOrMerge(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: claim.Owner.ID},
		Kind:                 workitems.KindReconciliationCalibrate,
		IdempotencyKey:       "recon:calibrate:" + claim.Owner.ID,
		InputSnapshot:        mustJSON(map[string]any{"generation": owner.Generation, "tree_top": scope.TreeTop}),
		MaxAttempts:          3,
		ExpectedOwnerVersion: owner.Version + 1,
	}); err != nil && !errors.Is(err, workitems.ErrLiveItemConflict) {
		return worker.Outcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return worker.Outcome{}, err
	}
	return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "completed", ReasonCode: "scan_completed", Reason: fmt.Sprintf("scanned %d files", scanned)}, nil), nil
}

// ensureCalibrateItem repairs a completed generation whose calibrate item is
// missing (legacy pre-043 completion or a lost transaction). It is idempotent
// and never re-scans.
func (s *Service) ensureCalibrateItem(ctx context.Context, owner *ScanOwner, scope ScanTreeTop) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, _, err := s.workItems.CreateOrMerge(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: owner.ManagedAnimeID},
		Kind:                 workitems.KindReconciliationCalibrate,
		IdempotencyKey:       "recon:calibrate:" + owner.ManagedAnimeID,
		InputSnapshot:        mustJSON(map[string]any{"generation": owner.Generation, "tree_top": scope.TreeTop}),
		MaxAttempts:          3,
		ExpectedOwnerVersion: owner.Version,
	}); err != nil && !errors.Is(err, workitems.ErrLiveItemConflict) {
		return err
	}
	return tx.Commit()
}

// calibrateItemExists reports whether a calibrate item, live or finished,
// already exists (the completion transaction committed).
func (s *Service) calibrateItemExists(ctx context.Context, animeID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM work_item_history
  WHERE owner_type = 'file_maintenance' AND owner_id = $1
    AND kind = 'reconciliation_calibrate' AND idempotency_key = $2
)`, animeID, "recon:calibrate:"+animeID).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// ownedPlacement is one verified active Placement of the instance whose
// current physical file the reconciliation generation must observe.
type ownedPlacement struct {
	ID           string
	PhysicalPath string
	// Frozen quality evidence from the release candidate facts (resolution/
	// group/language) so calibration and the alignment planner render the same
	// target the file operation produced instead of re-deriving from names.
	Resolution string
	Group      string
	Language   string
	// LocatorPaths are the bounded observation targets: the authoritative
	// current path first, then frozen provenance fallbacks (e.g. the original
	// download source of a file_operation placement).
	LocatorPaths []string
	// Companions are the placement's registered active companion sidecars
	//, observed at their registered current path for presence and size.
	Companions []ownedCompanion
}

// ownedCompanion is one registered companion sidecar of a placement.
type ownedCompanion struct {
	ID        string
	Path      string
	MediaKind string
	Suffix    string
	Extension string
	SizeBytes int64
}

// companionObservation is the scan result of one companion: present, missing
// (companion_missing) or size_mismatch (companion_size_mismatch). It never
// changes the video's own disposition.
type companionObservation struct {
	ID           string `json:"id"`
	Path         string `json:"path"`
	MediaKind    string `json:"media_kind"`
	Suffix       string `json:"suffix"`
	Extension    string `json:"extension"`
	ExpectedSize int64  `json:"expected_size"`
	Size         int64  `json:"size"`
	State        string `json:"state"`
}

func observeCompanions(companions []ownedCompanion, found map[string]domain.ScanFile) []companionObservation {
	observations := make([]companionObservation, 0, len(companions))
	for _, companion := range companions {
		observation := companionObservation{
			ID: companion.ID, Path: companion.Path, MediaKind: companion.MediaKind,
			Suffix: companion.Suffix, Extension: companion.Extension, ExpectedSize: companion.SizeBytes,
			State: "missing",
		}
		if file, ok := found[filepath.Clean(companion.Path)]; ok {
			observation.Size = file.SizeBytes
			observation.State = "present"
			if file.SizeBytes != companion.SizeBytes {
				observation.State = "size_mismatch"
			}
		}
		observations = append(observations, observation)
	}
	return observations
}

// loadOwnedPlacements returns the instance's verified active Placements as the
// scan's expected observation set, each with its bounded locator paths.
// Replaced/superseded and unverified rows are never observed here.
func (s *Service) loadOwnedPlacements(ctx context.Context, animeID string) ([]ownedPlacement, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT p.id, p.physical_path, op.source_path,
       COALESCE(f.resolution, ''), COALESCE(f."group", ''), COALESCE(f.language, '')
FROM placements p
LEFT JOIN file_operations op ON op.id = p.source_owner_ref
  AND p.source_evidence_type = 'file_operation'
LEFT JOIN release_candidate_facts f ON f.release_id = p.quality_facts_ref
  AND f.release_version = (SELECT MAX(f2.release_version) FROM release_candidate_facts f2 WHERE f2.release_id = f.release_id)
WHERE p.managed_anime_id = $1 AND p.verification_state = 'verified' AND p.state = 'active'
ORDER BY p.created_at, p.id`, animeID)
	if err != nil {
		return nil, fmt.Errorf("load owned placements: %w", err)
	}
	defer rows.Close()
	var placements []ownedPlacement
	for rows.Next() {
		var placement ownedPlacement
		var sourcePath sql.NullString
		if err := rows.Scan(&placement.ID, &placement.PhysicalPath, &sourcePath,
			&placement.Resolution, &placement.Group, &placement.Language); err != nil {
			return nil, fmt.Errorf("scan owned placement: %w", err)
		}
		placement.LocatorPaths = []string{placement.PhysicalPath}
		if sourcePath.Valid && strings.TrimSpace(sourcePath.String) != "" && sourcePath.String != placement.PhysicalPath {
			placement.LocatorPaths = append(placement.LocatorPaths, sourcePath.String)
		}
		placements = append(placements, placement)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(placements) == 0 {
		return placements, nil
	}
	index := make(map[string]int, len(placements))
	ids := make([]string, 0, len(placements))
	for i, placement := range placements {
		index[placement.ID] = i
		ids = append(ids, placement.ID)
	}
	companionRows, err := s.db.QueryContext(ctx, `
SELECT id, placement_id, current_path, media_kind, suffix, original_extension, size_bytes
FROM placement_companions
WHERE placement_id = ANY($1) AND state = 'active'
ORDER BY placement_id, current_path`, ids)
	if err != nil {
		return nil, fmt.Errorf("load placement companions: %w", err)
	}
	defer companionRows.Close()
	for companionRows.Next() {
		var companion ownedCompanion
		var placementID string
		if err := companionRows.Scan(&companion.ID, &placementID, &companion.Path, &companion.MediaKind, &companion.Suffix, &companion.Extension, &companion.SizeBytes); err != nil {
			return nil, fmt.Errorf("scan placement companion: %w", err)
		}
		if i, ok := index[placementID]; ok {
			placements[i].Companions = append(placements[i].Companions, companion)
		}
	}
	return placements, companionRows.Err()
}

// placementObservation is one expected Placement observation: present when a
// locator file exists with the frozen identity, missing otherwise.
type placementObservation struct {
	placementID  string
	physicalPath string
	file         *domain.ScanFile // nil = absent at every locator
	locatedVia   string           // current_path | source_provenance
	resolution   string
	group        string
	language     string
	companions   []companionObservation
}

// commitBatch persists one batch of Placement observations (ON CONFLICT DO
// NOTHING: idempotent re-entry) and advances the owner checkpoint in the same
// transaction, with the claim fencing token.
func (s *Service) commitBatch(ctx context.Context, claim workitems.Claim, owner *ScanOwner, batchIndex int, batch []placementObservation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	for _, observation := range batch {
		var fingerprint, disposition string
		var size int64
		facts := map[string]any{
			"placement_id": observation.placementID,
			"disposition":  "missing",
		}
		if observation.file != nil {
			fingerprint = observation.file.Fingerprint
			size = observation.file.SizeBytes
			disposition = "present"
			facts = map[string]any{
				"placement_id": observation.placementID,
				"disposition":  "present",
				"located_via":  observation.locatedVia,
				"located_path": observation.file.Path,
				"size":         size,
				"resolution":   observation.resolution,
				"group":        observation.group,
				"language":     observation.language,
			}
		} else {
			// Missing at every locator: the fingerprint column is NOT NULL so a
			// bounded deterministic placeholder marks the row; the disposition
			// in facts is the authoritative semantic.
			sum := sha256.Sum256([]byte(observation.placementID))
			fingerprint = "missing-" + hex.EncodeToString(sum[:8])
			disposition = "missing"
		}
		facts["disposition"] = disposition
		if len(observation.companions) > 0 {
			facts["companions"] = observation.companions
		}
		raw, _ := json.Marshal(facts)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO reconciliation_files
(managed_anime_id, generation, batch_index, file_path, fingerprint, size_bytes, facts, observed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (managed_anime_id, generation, file_path) DO NOTHING`,
			claim.Owner.ID, owner.Generation, batchIndex, observation.physicalPath, fingerprint, size, raw, now); err != nil {
			return fmt.Errorf("insert reconciliation observation: %w", err)
		}
	}
	if err := advanceScanBatch(ctx, tx, claim.Owner.ID, owner.Generation, owner.Version, batchIndex, 0, 0, 0); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	owner.Version++
	return nil
}

func (s *Service) failScan(ctx context.Context, claim workitems.Claim, owner *ScanOwner, code, reason string) (worker.Outcome, error) {
	if len(reason) > 2048 {
		reason = reason[:2048]
	}
	if err := failScanOwner(ctx, s.db, claim.Owner.ID, owner.Generation, owner.Version, code, reason); err != nil {
		return worker.Outcome{}, err
	}
	return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: code, Reason: reason}, nil), nil
}
