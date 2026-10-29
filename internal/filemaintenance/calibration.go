package filemaintenance

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

	"bansoko/internal/attrrules"
	"bansoko/internal/filemaintenance/planner"
	"bansoko/internal/filemaintenance/policy"
	observabilityport "bansoko/internal/observability/port"
	releasefacts "bansoko/internal/release/facts"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

// Consumer names of one completed generation (checkpoint owner truth).
const (
	ConsumerAlignment   = "alignment"
	ConsumerReplacement = "replacement"
)

// Consumer state of one generation's consumer checkpoint.
const (
	ConsumerPending  = "pending"
	ConsumerConsumed = "consumed"
	ConsumerBlocked  = "blocked"
	ConsumerFailed   = "failed"
)

// CalibrationDisposition is the typed calibration truth of one observed file.
// missing is evidence of an external move/delete observed by the scan; it
// never deletes or supersedes a Placement (fail closed). fingerprint_conflict
// means the located bytes no longer match the frozen Placement identity; the
// placement is never mutated by calibration.
const (
	DispositionPresent             = "present"
	DispositionUnresolved          = "unresolved"
	DispositionMissing             = "missing"
	DispositionFingerprintConflict = "fingerprint_conflict"
)

// calibratedFile is one typed calibration fact of a generation.
type calibratedFile struct {
	FilePath         string
	Fingerprint      string
	SizeBytes        int64
	Disposition      string
	Coverage         []string
	Resolution       string
	Revision         string
	Source           string
	Language         string
	Group            string
	ParserVersion    string
	FactsFingerprint string
	// Derived marks the calibration-only rows of the derived missing set:
	// paths absent from this generation's observations that existed in the
	// previous generation. They carry no scan observation columns.
	Derived bool
	// PlacementID and LocatedVia/LocatedPath are Placement-owned observation
	// evidence used for direct owner-truth recovery.
	PlacementID string
	LocatedVia  string
	LocatedPath string
}

// CalibrationService owns the completed-generation calibration and the fan-out
// to the two typed consumers. Calibration only writes typed facts and
// Placements (DB owner writes); it never moves, renames, supersedes or cleans
// up files — the scan itself stays zero-file-effect. Each consumer runs in its
// own claim with its own checkpoint, so one blocked consumer never changes the
// other consumer's owner result.
type CalibrationService struct {
	db         *sql.DB
	placements *PlacementService
	policies   *policy.PolicyService
	planner    *planner.Planner
	alignment  *AlignmentService
	trigger    *ReplacementTriggerService
	workItems  *workitems.Store
	logger     observabilityport.RuntimeLogger
	now        func() time.Time
	// companionCollision is the preset collision mode source for
	// companion alignment (nil = block).
	companionCollision CompanionCollisionSource
}

// NewCalibrationService wires the calibration owner and its ports. The
// alignment/replacement ports are optional: a consumer whose port is missing
// finishes blocked without affecting the other consumer.
func NewCalibrationService(db *sql.DB, placements *PlacementService, policies *policy.PolicyService, planner *planner.Planner, workItems *workitems.Store) *CalibrationService {
	return &CalibrationService{
		db: db, placements: placements, policies: policies, planner: planner,
		workItems: workItems, logger: observabilityport.NopRuntimeLogger{}, now: time.Now,
	}
}

// SetLogger wires the optional runtime diagnostics sink.
func (s *CalibrationService) SetLogger(logger observabilityport.RuntimeLogger) {
	if s == nil {
		return
	}
	if logger == nil {
		logger = observabilityport.NopRuntimeLogger{}
	}
	s.logger = logger
}

// SetAlignmentConsumer wires the alignment consumer port (composition root).
func (s *CalibrationService) SetAlignmentConsumer(alignment *AlignmentService) {
	if s != nil {
		s.alignment = alignment
		if alignment != nil {
			alignment.SetWorkItems(s.workItems)
		}
	}
}

// SetReplacementConsumer wires the replacement consumer port (composition
// root). The trigger applies the active gate itself and merges the one live
// replacement_evaluate item per anime.
func (s *CalibrationService) SetReplacementConsumer(trigger *ReplacementTriggerService) {
	if s != nil {
		s.trigger = trigger
	}
}

// CalibrateHandler is the reconciliation_calibrate work item handler: one
// completed immutable generation becomes typed calibration facts plus the
// verified active Placements derived from the reliable facts, and is fanned
// out to the alignment/replacement consumer claims. It is idempotent per
// generation (deterministic inputs, ON CONFLICT DO NOTHING writes) so crash
// replays converge.
func (s *CalibrationService) CalibrateHandler() worker.Handler {
	return calibrationRecoveryHandler{service: s, kind: workitems.KindReconciliationCalibrate, handle: func(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
		if claim.Kind != workitems.KindReconciliationCalibrate {
			return worker.Outcome{}, fmt.Errorf("calibration handler received kind %q", claim.Kind)
		}
		var input struct {
			Generation int64 `json:"generation"`
		}
		if err := json.Unmarshal(claim.InputSnapshot, &input); err != nil || input.Generation < 1 {
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "calibration_claim_invalid", Reason: "claim input must carry generation"}, nil), nil
		}
		animeID := claim.Owner.ID
		// The claimed generation must exist as an immutable observation set.
		// The scan owner only keeps the latest generation, so historical
		// completed generations (owner advanced to a newer one) still
		// calibrate: the observations are immutable and deterministic.
		var ownerGen int64
		err := s.db.QueryRowContext(ctx, `SELECT generation FROM reconciliation_scans WHERE managed_anime_id = $1`, animeID).Scan(&ownerGen)
		if errors.Is(err, sql.ErrNoRows) {
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "calibration_scan_missing", Reason: "scan owner is missing"}, nil), nil
		}
		if err != nil {
			return worker.Outcome{}, err
		}
		if ownerGen < input.Generation {
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "calibration_generation_unknown", Reason: "claim generation is newer than the scan owner"}, nil), nil
		}
		if err := s.calibrateGeneration(ctx, animeID, input.Generation); err != nil {
			return worker.Outcome{}, err
		}
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "completed", ReasonCode: "calibration_completed", Reason: fmt.Sprintf("calibrated generation %d", input.Generation)}, nil), nil
	}}
}

// calibrateGeneration persists the typed calibration facts and placement
// reconciliation of one generation, then enqueues the two consumer claims in
// the same transaction (consumers exist iff calibration committed).
func (s *CalibrationService) calibrateGeneration(ctx context.Context, animeID string, generation int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	facts, err := s.buildCalibrationFacts(ctx, tx, animeID, generation)
	if err != nil {
		return fmt.Errorf("build calibration facts: %w", err)
	}
	for _, file := range facts {
		if err := s.insertCalibrationFact(ctx, tx, animeID, generation, file); err != nil {
			return fmt.Errorf("insert calibration fact %s: %w", file.FilePath, err)
		}
		// Direct owner-truth recovery: the current path is absent but exactly
		// one frozen provenance locator still carries the Placement bytes.
		// Commit the located path as the authoritative current location (no
		// file effect); naming alignment remains separately authorized.
		if file.Disposition == DispositionPresent && file.LocatedVia == "source_provenance" && file.PlacementID != "" && file.LocatedPath != "" {
			if _, err := tx.ExecContext(ctx, `
UPDATE placements
SET physical_path = $1, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $2 AND verification_state = 'verified' AND state = 'active'`,
				file.LocatedPath, file.PlacementID); err != nil {
				return fmt.Errorf("recover drifted placement %s: %w", file.PlacementID, err)
			}
		}
		// Reliable facts form the same verified active Placement as the
		// incremental path (idempotent per observation ref). Unresolved and
		// missing facts are typed evidence only — never a Placement change,
		// never a deletion.
		if file.Disposition != DispositionPresent || len(file.Coverage) == 0 {
			continue
		}
		// A file that already has an active Placement is observed, not
		// admitted again: a Placement-owned observation names its Placement,
		// and a path an active Placement of this anime already holds (an
		// earlier generation's admission included) stays with that Placement.
		if file.PlacementID != "" {
			continue
		}
		held, err := activePlacementHoldsPathTx(ctx, tx, animeID, file.FilePath)
		if err != nil {
			return fmt.Errorf("check placement at %s: %w", file.FilePath, err)
		}
		if held {
			continue
		}
		if s.placements == nil {
			return errors.New("placement service is not wired")
		}
		observationRef := fmt.Sprintf("%s:%d:%s", animeID, generation, file.FilePath)
		if _, err := s.placements.admitTx(ctx, tx, placementInput{
			SourceEvidenceType: "reconciliation",
			SourceOwnerRef:     observationRef,
			PhysicalPath:       file.FilePath,
			SourceFingerprint:  file.Fingerprint,
			TargetFingerprint:  file.Fingerprint,
			FactsVersion:       file.ParserVersion,
			Coverage:           file.Coverage,
			ManagedAnimeID:     animeID,
			SnapshotGeneration: generation,
			// A scan-managed file is a managed file: it takes part in
			// replacement (no group, no values until a release claims it)
			// and, as a loser, is deleted like any managed loser.
			ReplacementEligible: true,
		}); err != nil {
			return fmt.Errorf("admit calibration placement %s: %w", file.FilePath, err)
		}
	}
	if err := s.enqueueConsumers(ctx, tx, animeID, generation); err != nil {
		return fmt.Errorf("enqueue generation consumers: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit calibration: %w", err)
	}
	return nil
}

// buildCalibrationFacts parses the immutable observations of one generation
// into typed facts, and derives the missing set from the immediately previous
// generation (files that disappeared since then). All inputs are immutable,
// so the output is deterministic.
func (s *CalibrationService) buildCalibrationFacts(ctx context.Context, tx *sql.Tx, animeID string, generation int64) ([]calibratedFile, error) {
	isMovie, err := calibrationAnimeIsMovie(ctx, tx, animeID)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `
SELECT file_path, fingerprint, size_bytes, facts
FROM reconciliation_files
WHERE managed_anime_id = $1 AND generation = $2 AND NOT derived_missing
ORDER BY file_path`, animeID, generation)
	if err != nil {
		return nil, err
	}
	type observed struct {
		path        string
		fingerprint string
		size        int64
		facts       []byte
	}
	var observedFiles []observed
	for rows.Next() {
		var file observed
		if err := rows.Scan(&file.path, &file.fingerprint, &file.size, &file.facts); err != nil {
			rows.Close()
			return nil, err
		}
		observedFiles = append(observedFiles, file)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Missing files: present in the immediately previous generation, absent
	// now. The previous generation is immutable, so the derived missing set
	// is deterministic across calibration replays.
	var prevGeneration int64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(generation), 0)
FROM reconciliation_files
WHERE managed_anime_id = $1 AND generation < $2 AND NOT derived_missing`, animeID, generation).Scan(&prevGeneration); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(observedFiles))
	for _, file := range observedFiles {
		seen[file.path] = struct{}{}
	}
	parserVersion, err := attrrules.CurrentProfileVersion()
	if err != nil {
		return nil, fmt.Errorf("read active attr profile version: %w", err)
	}
	facts := make([]calibratedFile, 0, len(observedFiles))
	for _, file := range observedFiles {
		observation := decodePlacementObservation(file.facts)
		disposition := DispositionPresent
		coverage := persistedCoverage(file.facts)
		if isMovie && observation.PlacementID == "" {
			// A legacy full-library observation has no frozen coverage to
			// project, so the metadata movie classification is the caller's
			// non-parsing evidence. A Placement-owned observation keeps its
			// frozen coverage: a metadata edit after submission must not
			// re-classify an already-placed file.
			coverage = []string{"movie:0"}
		}
		if observation.Disposition == "missing" {
			// Placement-owned scan marked the placement absent at every locator.
			disposition = DispositionMissing
			coverage = []string{}
		} else if observation.PlacementID != "" {
			// Placement-owned observation: coverage comes from the frozen
			// placement coverage set, never from re-parsing the filename and
			// never from the current metadata binding.
			coverage = placementCoverageTx(ctx, tx, observation.PlacementID)
			// The located file must match the frozen Placement identity; a
			// mismatch is evidence of external replacement, never a mutation.
			// Path-level (owner policy): the scan records size + inode, so
			// "changed" is a size (and recorded inode) mismatch.
			conflict, err := placementObservationConflictTx(ctx, tx, observation.PlacementID, file.fingerprint)
			if err != nil {
				return nil, fmt.Errorf("compare placement %s identity: %w", observation.PlacementID, err)
			}
			if conflict {
				disposition = DispositionFingerprintConflict
			}
		}
		if len(coverage) == 0 || coverage[0] == releasefacts.CoverageUnresolved {
			if disposition == DispositionPresent {
				disposition = DispositionUnresolved
			}
			coverage = []string{releasefacts.CoverageUnresolved}
		}
		facts = append(facts, calibratedFile{
			FilePath: file.path, Fingerprint: file.fingerprint, SizeBytes: file.size,
			Disposition: disposition, Coverage: SortCoverage(coverage),
			Resolution:       persistedQuality(file.facts, "resolution"),
			Language:         persistedQuality(file.facts, "language"),
			Group:            persistedQuality(file.facts, "group"),
			ParserVersion:    parserVersion,
			FactsFingerprint: calibrationFactsDigest(disposition, coverage, persistedQuality(file.facts, "resolution"), persistedQuality(file.facts, "language"), persistedQuality(file.facts, "group")),
			PlacementID:      observation.PlacementID,
			LocatedVia:       observation.LocatedVia,
			LocatedPath:      observation.LocatedPath,
		})
	}
	// Missing derivation from the previous generation is a legacy fallback for
	// pre-Placement-owned observations. The Placement-owned scan already marks
	// every expected placement present/missing per generation (including after
	// an alignment commit moves the current path), so deriving missing rows
	// from the previous generation would re-report the old path as missing.
	placementOwned := true
	for _, file := range observedFiles {
		if decodePlacementObservation(file.facts).PlacementID == "" {
			placementOwned = false
			break
		}
	}
	if prevGeneration > 0 && !placementOwned {
		prevRows, err := tx.QueryContext(ctx, `
SELECT file_path, fingerprint, size_bytes
FROM reconciliation_files
WHERE managed_anime_id = $1 AND generation = $2 AND NOT derived_missing
ORDER BY file_path`, animeID, prevGeneration)
		if err != nil {
			return nil, err
		}
		for prevRows.Next() {
			var file observed
			if err := prevRows.Scan(&file.path, &file.fingerprint, &file.size); err != nil {
				prevRows.Close()
				return nil, err
			}
			if _, present := seen[file.path]; present {
				continue
			}
			facts = append(facts, calibratedFile{
				FilePath: file.path, Fingerprint: file.fingerprint, SizeBytes: file.size,
				Disposition: DispositionMissing, Coverage: []string{},
				Derived:          true,
				ParserVersion:    parserVersion,
				FactsFingerprint: calibrationFactsDigest(DispositionMissing, nil, "", "", ""),
			})
		}
		if err := prevRows.Close(); err != nil {
			return nil, err
		}
		if err := prevRows.Err(); err != nil {
			return nil, err
		}
	}
	return facts, nil
}

// insertCalibrationFact fills the calibration half of one merged
// reconciliation_files row exactly once (deterministic per generation, so
// replays converge). The observation half of an observed path is written by
// the scan, so only its calibration columns are filled here; a path that
// exists only as a calibration fact (the derived missing set) is inserted as a
// calibration-only row.
func (s *CalibrationService) insertCalibrationFact(ctx context.Context, tx *sql.Tx, animeID string, generation int64, file calibratedFile) error {
	coverage, err := json.Marshal(file.Coverage)
	if err != nil {
		return err
	}
	if file.Derived {
		_, err = tx.ExecContext(ctx, `
INSERT INTO reconciliation_files
(managed_anime_id, generation, file_path, derived_missing, fingerprint, size_bytes,
 disposition, coverage, resolution, revision, source, language, "group", parser_version, facts_fingerprint, version, created_at, updated_at)
VALUES ($1, $2, $3, TRUE, $4, $5, $6, $7, NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''), NULLIF($11, ''), NULLIF($12, ''), $13, $14, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT (managed_anime_id, generation, file_path) DO NOTHING`,
			animeID, generation, file.FilePath, file.Fingerprint, file.SizeBytes,
			file.Disposition, coverage, file.Resolution, file.Revision, file.Source, file.Language, file.Group,
			file.ParserVersion, file.FactsFingerprint)
		return err
	}
	// Fill the calibration half of the scan's observation row once; a replay
	// (disposition already set) leaves version and updated_at untouched.
	_, err = tx.ExecContext(ctx, `
UPDATE reconciliation_files
SET disposition = $4,
    coverage = $5,
    resolution = NULLIF($6, ''),
    revision = NULLIF($7, ''),
    source = NULLIF($8, ''),
    language = NULLIF($9, ''),
    "group" = NULLIF($10, ''),
    parser_version = $11,
    facts_fingerprint = $12,
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE managed_anime_id = $1 AND generation = $2 AND file_path = $3 AND disposition IS NULL`,
		animeID, generation, file.FilePath,
		file.Disposition, coverage, file.Resolution, file.Revision, file.Source, file.Language, file.Group,
		file.ParserVersion, file.FactsFingerprint)
	return err
}

// enqueueConsumers merges the two consumer claims inside the caller's
// transaction. The idempotency key is stable per (anime, consumer): the live
// unique owner+kind index (001) allows only one live item per anime/consumer,
// so a newer completed generation MERGES into the live item (refreshing the
// input to the newest generation) instead of failing. The folded checkpoint
// keeps only the latest settled generation per consumer (PRUNE-046 S10), so a
// superseded generation's checkpoint is simply not retained: the checkpoint
// write is anchored to the scan owner's generation, and a late write from a
// stale claim is dropped instead of overwriting a newer consumer outcome.
func (s *CalibrationService) enqueueConsumers(ctx context.Context, tx *sql.Tx, animeID string, generation int64) error {
	if s.workItems == nil {
		return errors.New("calibration work item store is not wired")
	}
	for _, consumer := range []struct {
		kind string
		name string
	}{
		{kind: workitems.KindReconciliationConsumeAlignment, name: ConsumerAlignment},
		{kind: workitems.KindReconciliationConsumeReplacement, name: ConsumerReplacement},
	} {
		key := fmt.Sprintf("recon:consume:%s:%s", consumer.name, animeID)
		_, _, err := s.workItems.CreateOrMerge(ctx, tx, workitems.WorkItemSpec{
			Owner:          workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: animeID},
			Kind:           consumer.kind,
			IdempotencyKey: key,
			InputSnapshot:  mustJSONConsumer(map[string]any{"generation": generation}),
			MaxAttempts:    3,
			// The consumers are generation-addressed (their writes are
			// idempotent per generation), so the fencing hint is the static
			// minimum and never gates a replay.
			ExpectedOwnerVersion: 1,
		})
		if err != nil && !errors.Is(err, workitems.ErrLiveItemConflict) {
			return fmt.Errorf("enqueue %s consumer: %w", consumer.name, err)
		}
	}
	return nil
}

func mustJSONConsumer(value map[string]any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

// calibrationFactsDigest is the deterministic digest of one typed fact set.
func calibrationFactsDigest(disposition string, coverage []string, resolution, language, group string) string {
	raw, _ := json.Marshal(map[string]any{
		"disposition": disposition, "coverage": coverage,
		"resolution": resolution, "language": language, "group": group,
	})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// calibrationAnimeIsMovie resolves the owner media kind inside the caller's
// transaction (movie -> observation key movie:0).
func calibrationAnimeIsMovie(ctx context.Context, q queryer, animeID string) (bool, error) {
	var override sql.NullString
	err := q.QueryRowContext(ctx, `
SELECT media_kind
FROM anime_metadata
WHERE managed_anime_id = $1`, animeID).Scan(&override)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return ownerMediaKind(override.String) == "movie", nil
}

// consumerCheckpointRow is the folded checkpoint of one consumer generation.
type consumerCheckpointRow struct {
	State      string
	ReasonCode string
	Reason     string
}

// consumerCheckpointStatements are the folded checkpoint statements of the two
// consumers. PRUNE-046 S10 folded reconciliation_consumer_checkpoints into the
// scan owner keyed by a per-consumer checkpoint generation: only the latest
// settled generation per consumer is kept, the cross-generation history is
// gone. The consumer name is a compile-time constant, so every statement stays
// static SQL. A settle only applies while the scan owner is at that generation,
// and the terminal-state preservation and the version counter are scoped to the
// checkpoint generation, so a newer generation never inherits an older outcome.
var consumerCheckpointStatements = map[string]struct {
	load       string
	settle     string
	transition string
}{
	ConsumerAlignment: {
		load: `SELECT alignment_checkpoint_state, alignment_checkpoint_reason_code, alignment_checkpoint_reason
FROM reconciliation_scans
WHERE managed_anime_id = $1 AND alignment_checkpoint_generation = $2`,
		settle: `UPDATE reconciliation_scans
SET alignment_checkpoint_generation = $2,
    alignment_checkpoint_state = CASE WHEN alignment_checkpoint_generation = $2 AND alignment_checkpoint_state IN ('consumed', 'blocked')
        THEN alignment_checkpoint_state ELSE $3 END,
    alignment_checkpoint_reason_code = CASE WHEN alignment_checkpoint_generation = $2 AND alignment_checkpoint_state IN ('consumed', 'blocked')
        THEN alignment_checkpoint_reason_code ELSE NULLIF($4, '') END,
    alignment_checkpoint_reason = CASE WHEN alignment_checkpoint_generation = $2 AND alignment_checkpoint_state IN ('consumed', 'blocked')
        THEN alignment_checkpoint_reason ELSE NULLIF($5, '') END,
    alignment_checkpoint_version = CASE WHEN alignment_checkpoint_generation = $2 THEN alignment_checkpoint_version + 1 ELSE 1 END,
    alignment_checkpoint_updated_at = CURRENT_TIMESTAMP
WHERE managed_anime_id = $1 AND generation = $2`,
		transition: `UPDATE reconciliation_scans
SET alignment_checkpoint_generation = $2,
    alignment_checkpoint_state = $3,
    alignment_checkpoint_reason_code = NULLIF($4, ''),
    alignment_checkpoint_reason = NULLIF($5, ''),
    alignment_checkpoint_version = CASE WHEN alignment_checkpoint_generation = $2 THEN alignment_checkpoint_version + 1 ELSE 1 END,
    alignment_checkpoint_updated_at = CURRENT_TIMESTAMP
WHERE managed_anime_id = $1 AND generation = $2
  AND (alignment_checkpoint_generation IS DISTINCT FROM $2
       OR alignment_checkpoint_state IS NULL
       OR alignment_checkpoint_state NOT IN ('consumed', 'blocked'))
RETURNING TRUE`,
	},
	ConsumerReplacement: {
		load: `SELECT replacement_checkpoint_state, replacement_checkpoint_reason_code, replacement_checkpoint_reason
FROM reconciliation_scans
WHERE managed_anime_id = $1 AND replacement_checkpoint_generation = $2`,
		settle: `UPDATE reconciliation_scans
SET replacement_checkpoint_generation = $2,
    replacement_checkpoint_state = CASE WHEN replacement_checkpoint_generation = $2 AND replacement_checkpoint_state IN ('consumed', 'blocked')
        THEN replacement_checkpoint_state ELSE $3 END,
    replacement_checkpoint_reason_code = CASE WHEN replacement_checkpoint_generation = $2 AND replacement_checkpoint_state IN ('consumed', 'blocked')
        THEN replacement_checkpoint_reason_code ELSE NULLIF($4, '') END,
    replacement_checkpoint_reason = CASE WHEN replacement_checkpoint_generation = $2 AND replacement_checkpoint_state IN ('consumed', 'blocked')
        THEN replacement_checkpoint_reason ELSE NULLIF($5, '') END,
    replacement_checkpoint_version = CASE WHEN replacement_checkpoint_generation = $2 THEN replacement_checkpoint_version + 1 ELSE 1 END,
    replacement_checkpoint_updated_at = CURRENT_TIMESTAMP
WHERE managed_anime_id = $1 AND generation = $2`,
	},
}

// loadConsumerCheckpoint reads the folded checkpoint of one consumer generation
// (nil when none is stored for that generation).
func loadConsumerCheckpoint(ctx context.Context, q queryer, animeID string, generation int64, consumer string) (*consumerCheckpointRow, error) {
	statement, ok := consumerCheckpointStatements[consumer]
	if !ok {
		return nil, fmt.Errorf("unknown reconciliation consumer %q", consumer)
	}
	var (
		state      sql.NullString
		reasonCode sql.NullString
		reason     sql.NullString
	)
	err := q.QueryRowContext(ctx, statement.load, animeID, generation).Scan(&state, &reasonCode, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !state.Valid {
		return nil, nil
	}
	return &consumerCheckpointRow{State: state.String, ReasonCode: reasonCode.String, Reason: reason.String}, nil
}

// settleConsumerCheckpoint records the terminal consumer outcome (idempotent;
// this generation's terminal state is never overwritten by a replay). The write
// is anchored to the scan owner's generation, so a late write for a superseded
// generation never overwrites a newer consumer outcome.
func settleConsumerCheckpoint(ctx context.Context, db *sql.DB, animeID string, generation int64, consumer, state, reasonCode, reason string) error {
	statement, ok := consumerCheckpointStatements[consumer]
	if !ok {
		return fmt.Errorf("unknown reconciliation consumer %q", consumer)
	}
	_, err := db.ExecContext(ctx, statement.settle, animeID, generation, state, reasonCode, reason)
	return err
}

// settleAlignmentConsumerCheckpoint atomically claims the first terminal
// alignment transition. A replay whose checkpoint was already settled returns
// false, so runtime summaries remain at-most-once for the successful consumer
// transition without changing the durable checkpoint contract. PostgreSQL
// returns a row only when the guarded update actually changes the checkpoint to
// a terminal state; an existing terminal row yields no row and therefore loses
// the summary race, and a checkpoint of a superseded generation is skipped by
// the generation guard.
func settleAlignmentConsumerCheckpoint(ctx context.Context, db *sql.DB, animeID string, generation int64, state, reasonCode, reason string) (bool, error) {
	var transitioned bool
	err := db.QueryRowContext(ctx, consumerCheckpointStatements[ConsumerAlignment].transition,
		animeID, generation, state, reasonCode, reason).Scan(&transitioned)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return transitioned, nil
}

// ConsumeAlignmentHandler is the alignment consumer of one completed
// generation. It reads the reliable path/fingerprint/coverage facts (never
// the replacement facts), and — when the effective alignment policy enables
// it — plans rename/move operations with the naming target renderer and
// admits alignment_execute claims. The Subscription state is NOT a gate:
// stopped instances still align under their own policy. The alignment
// setting gates only the files already in place: the alignment requests
// recorded before the scan (a new import, an offset change) are aligned
// either way and resolved here. A disabled alignment policy without requests
// or an unwired planner finishes the consumer blocked; the replacement
// consumer is untouched.
func (s *CalibrationService) ConsumeAlignmentHandler() worker.Handler {
	return calibrationRecoveryHandler{service: s, kind: workitems.KindReconciliationConsumeAlignment, handle: func(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
		if claim.Kind != workitems.KindReconciliationConsumeAlignment {
			return worker.Outcome{}, fmt.Errorf("alignment consumer received kind %q", claim.Kind)
		}
		animeID := claim.Owner.ID
		generation, err := claimGeneration(claim)
		if err != nil {
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "alignment_claim_invalid", Reason: err.Error()}, nil), nil
		}
		checkpoint, err := loadConsumerCheckpoint(ctx, s.db, animeID, generation, ConsumerAlignment)
		if err != nil {
			return worker.Outcome{}, err
		}
		if checkpoint != nil && (checkpoint.State == ConsumerConsumed || checkpoint.State == ConsumerBlocked) {
			return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: checkpoint.State, ReasonCode: "alignment_consumer_already_settled", Reason: "consumer checkpoint already terminal"}, nil), nil
		}
		if s.alignment == nil || s.policies == nil || s.planner == nil || s.workItems == nil {
			if err := settleConsumerCheckpoint(ctx, s.db, animeID, generation, ConsumerAlignment, ConsumerBlocked, "alignment_consumer_unavailable", "alignment consumer is not fully wired"); err != nil {
				return worker.Outcome{}, err
			}
			return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "blocked", ReasonCode: "alignment_consumer_unavailable", Reason: "alignment consumer is not fully wired"}, nil), nil
		}
		effective, err := s.policies.GetEffective(ctx, animeID)
		if err != nil {
			return worker.Outcome{}, err
		}
		requests, err := requestsBeforeScan(ctx, s.db, animeID, generation)
		if err != nil {
			return worker.Outcome{}, err
		}
		enabled := effective.AlignmentEnabled != nil && *effective.AlignmentEnabled
		if !enabled && len(requests) == 0 {
			// Alignment is policy-gated, not subscription-gated: the consumer
			// records its own disabled outcome and never touches replacement.
			if err := settleConsumerCheckpoint(ctx, s.db, animeID, generation, ConsumerAlignment, ConsumerBlocked, "alignment_disabled", "alignment is disabled by the effective policy"); err != nil {
				return worker.Outcome{}, err
			}
			return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "blocked", ReasonCode: "alignment_disabled", Reason: "alignment is disabled by the effective policy"}, nil), nil
		}
		// The policy version is provenance on the operation; an instance whose
		// policy was never saved aligns its requests under the first version.
		policyVersion := effective.PolicyVersion
		if policyVersion < 1 {
			policyVersion = 1
		}
		plan, err := s.planner.Plan(ctx, animeID, generation, policyVersion)
		if err != nil {
			return worker.Outcome{}, err
		}
		var include func(planner.PlanCandidate) bool
		if !enabled {
			paths := requestedPaths(requests)
			include = func(candidate planner.PlanCandidate) bool {
				return paths[filepath.Clean(candidate.SourcePath)]
			}
		}
		routes := map[string]alignmentRoute{}
		admitted, err := s.admitAlignmentOperationsFor(ctx, animeID, generation, plan, include, routes)
		if err != nil {
			return worker.Outcome{}, err
		}
		if err := resolveAlignmentRequests(ctx, s.db, requests, routes); err != nil {
			return worker.Outcome{}, err
		}
		settled, err := settleAlignmentConsumerCheckpoint(ctx, s.db, animeID, generation, ConsumerConsumed, "alignment_consumed", fmt.Sprintf("aligned %d candidates", admitted))
		if err != nil {
			return worker.Outcome{}, err
		}
		if !settled {
			return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "consumed", ReasonCode: "alignment_consumer_already_settled", Reason: "consumer checkpoint already terminal"}, nil), nil
		}
		candidateCount, executableCount, excludeReasonCounts := alignmentCandidateSummary(plan.Candidates)
		s.logger.Info(ctx, observabilityport.RuntimeEvent{
			Message:             "alignment candidate summary",
			Component:           "filemaintenance",
			Operation:           "alignment_plan",
			Outcome:             "success",
			ReasonCode:          "alignment_candidate_summary",
			OwnerID:             animeID,
			Generation:          generation,
			CandidateCount:      &candidateCount,
			ExecutableCount:     &executableCount,
			ExcludeReasonCounts: excludeReasonCounts,
		})
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "consumed", ReasonCode: "alignment_consumed", Reason: fmt.Sprintf("aligned %d candidates", admitted)}, nil), nil
	}}
}

// classifyAlignmentCandidate is the bounded admission view of one planner
// candidate. reason is non-empty for candidates that must not create an
// alignment operation; target is the absolute target for executable candidates.
// Naming templates render absolute targets, so a relative render is a naming
// failure, not a missing global anchor.
type alignmentCandidateClassification struct {
	target string
	reason string
}

func classifyAlignmentCandidate(candidate planner.PlanCandidate) alignmentCandidateClassification {
	if candidate.Excluded {
		return alignmentCandidateClassification{reason: normalizeAlignmentExcludeReasonCode(candidate.ExcludeReasonCode)}
	}
	if candidate.TargetPath == "" {
		return alignmentCandidateClassification{reason: "target_missing"}
	}
	target := filepath.Clean(candidate.TargetPath)
	if !filepath.IsAbs(target) {
		return alignmentCandidateClassification{reason: "target_not_absolute"}
	}
	if target == candidate.SourcePath {
		return alignmentCandidateClassification{reason: "already_aligned"}
	}
	return alignmentCandidateClassification{target: target}
}

// alignmentCandidateSummary reports the bounded planner result without exposing
// candidate paths or free-form exclusion reasons. executable_count is the
// number of candidates eligible for an alignment effect; it is deliberately
// distinct from the number of newly inserted operations on a replay.
func alignmentCandidateSummary(candidates []planner.PlanCandidate) (candidateCount, executableCount int, excludeReasonCounts map[string]int) {
	candidateCount = len(candidates)
	excludeReasonCounts = make(map[string]int)
	for _, candidate := range candidates {
		classification := classifyAlignmentCandidate(candidate)
		if classification.reason != "" {
			excludeReasonCounts[classification.reason]++
			continue
		}
		executableCount++
	}
	return candidateCount, executableCount, excludeReasonCounts
}

func normalizeAlignmentExcludeReasonCode(value string) string {
	switch value {
	case "coverage_unresolved", "target_render_failed", "render_facts_unavailable", "episode_title_lookup_failed", "unknown", "target_missing", "already_aligned", "target_not_absolute", ReasonPlacementExcluded:
		return value
	}
	if isConfigIncompleteReason(value) {
		return value
	}
	return "unknown"
}

// admitAlignmentOperationsFor creates an alignment operation for every plan
// candidate include accepts (nil = all) whose naming target differs from the
// observed path, marks them ready and enqueues the alignment_execute claims.
// Everything is idempotent per (generation, observation), so consumer replays
// converge. Each video unit's target is routed (alignment_routing.go) against
// what holds it: a file on disk, or a candidate of the same plan that claimed
// the same target earlier. routes, when given, receives each routed
// placement's route.
func (s *CalibrationService) admitAlignmentOperationsFor(ctx context.Context, animeID string, generation int64, plan *planner.PlanResult, include func(planner.PlanCandidate) bool, routes map[string]alignmentRoute) (int, error) {
	metadataVersion, rulesVersion, err := ownerVersions(ctx, s.db, animeID)
	if err != nil {
		return 0, err
	}
	admitted := 0
	companions, err := s.generationCompanions(ctx, animeID, generation)
	if err != nil {
		return 0, err
	}
	policy := s.unitCollisionPolicy(ctx, animeID)
	classifications := make([]alignmentCandidateClassification, len(plan.Candidates))
	for index, candidate := range plan.Candidates {
		classifications[index] = classifyAlignmentCandidate(candidate)
	}
	// claimants are the placements that claimed a target earlier in this
	// plan; claimed are the versioned targets taken by earlier units.
	claimants := map[string]string{}
	claimed := map[string]bool{}
	for index, candidate := range plan.Candidates {
		if include != nil && !include(candidate) {
			continue
		}
		classification := classifications[index]
		base := AlignmentOperation{
			ManagedAnimeID: animeID, ScanGeneration: generation, ObservationRef: candidate.ObservationRef, CandidateRef: candidate.ObservationRef,
			PolicyVersion: plan.PolicyVersion, MetadataVersion: metadataVersion, RulesVersion: rulesVersion, NamingVersion: rulesVersion,
		}
		unit, err := s.unitCompanions(ctx, companions[candidate.ObservationRef])
		if err != nil {
			return admitted, err
		}
		if classification.reason == "already_aligned" && len(unit) > 0 {
			// The video is already correct: only its companions move.
			count, err := s.admitCompanionAlignments(ctx, base, candidate, candidate.SourcePath, unit, policy, true)
			if err != nil {
				return admitted, err
			}
			admitted += count
			continue
		}
		if classification.reason != "" {
			if classification.reason == "target_not_absolute" {
				return admitted, fmt.Errorf("alignment target %q is not absolute; naming templates must render absolute targets", candidate.TargetPath)
			}
			continue
		}
		target := classification.target
		moverID, _, err := activePlacementAt(ctx, s.db, animeID, candidate.SourcePath)
		if err != nil {
			return admitted, err
		}
		route, err := s.alignment.routeUnitTarget(ctx, animeID, moverID, candidate.SourcePath, candidate.Fingerprint, target, claimants[target], policy)
		if err != nil {
			return admitted, err
		}
		if routes != nil && moverID != "" {
			routes[moverID] = route
		}
		if route.Action == routeDrop {
			// The file lost retention to the occupant: its deletion owns it.
			continue
		}
		if claimants[target] == "" {
			claimants[target] = moverID
		}
		if route.Action == routeVersion || route.Action == routeProceed {
			// The video and its companions are one unit: under version one
			// suffix keeps every target in the unit free (and clear of the
			// targets earlier units of this plan took).
			target, err = s.chooseUnitTarget(ctx, policy, candidate.SourcePath, candidate.Fingerprint, target, unit, claimed)
			if err != nil {
				return admitted, err
			}
			claimed[target] = true
			if samePath(target, candidate.SourcePath) {
				// Already at its versioned target: only its companions move.
				count, err := s.admitCompanionAlignments(ctx, base, candidate, candidate.SourcePath, unit, policy, true)
				if err != nil {
					return admitted, err
				}
				admitted += count
				continue
			}
		}
		idempotencyKey := "align:" + candidate.ObservationRef
		op := AlignmentOperation{
			ID:                alignmentOperationID(idempotencyKey),
			IdempotencyKey:    idempotencyKey,
			ManagedAnimeID:    animeID,
			ScanGeneration:    generation,
			ObservationRef:    candidate.ObservationRef,
			CandidateRef:      candidate.ObservationRef,
			PolicyVersion:     plan.PolicyVersion,
			MetadataVersion:   metadataVersion,
			RulesVersion:      rulesVersion,
			NamingVersion:     rulesVersion,
			SourcePath:        candidate.SourcePath,
			SourceFingerprint: candidate.Fingerprint,
			TargetPath:        target,
		}
		videoContract := videoAlignmentContract{TargetPath: target, Fingerprint: candidate.Fingerprint, Collision: string(policy)}
		for _, companion := range unit {
			if companionTarget := companion.target(target); !samePath(companionTarget, companion.fact.Path) {
				videoContract.Companions = append(videoContract.Companions, unitCompanionTarget{TargetPath: companionTarget, Fingerprint: companion.fingerprint})
			}
		}
		contract, _ := json.Marshal(videoContract)
		dryRun, _ := json.Marshal(candidate)
		op.TargetContract = contract
		op.DryRunResult = dryRun
		if route.Action == routeSkip || (route.Action == routeHold && route.Settle) {
			// The collision policy is skip, or retention cannot decide now:
			// the file stays where it is, with the reason.
			code, reason := "target_occupied", safeReason(fmt.Errorf("occupied: %s", filepath.Base(target)))
			if route.Action == routeHold {
				code, reason = ReasonReplacementPending, "waiting for retention to compare the file at the target: "+filepath.Base(target)
			}
			created, err := s.alignment.CreateOperation(ctx, op)
			if err != nil {
				return admitted, err
			}
			if created.State == AlignmentPlanned {
				if err := s.alignment.BlockPlanned(ctx, created.ID, code, reason); err != nil {
					return admitted, err
				}
			}
			continue
		}

		created, err := s.alignment.CreateOperation(ctx, op)
		if err != nil {
			return admitted, err
		}
		if created.State == AlignmentReady || created.State == AlignmentRunning {
			if err := s.ensureAlignmentExecuteWorkItem(ctx, created.ID, created.Version); err != nil {
				return admitted, err
			}
			continue // already admitted by a previous replay
		}
		if created.State != AlignmentPlanned {
			continue // terminal operations are never re-enqueued
		}
		version, err := s.alignment.MarkReady(ctx, created.ID, created.Version)
		if err != nil {
			// The automatic consumer and a manual admission of the same
			// generation share the operation key: when the other one marked
			// it ready first, follow its version instead of failing.
			current, loadErr := s.alignment.load(ctx, created.ID)
			if loadErr != nil || current == nil || (current.State != AlignmentReady && current.State != AlignmentRunning) {
				return admitted, err
			}
			if err := s.ensureAlignmentExecuteWorkItem(ctx, current.ID, current.Version); err != nil {
				return admitted, err
			}
			continue
		}
		if err := s.ensureAlignmentExecuteWorkItem(ctx, created.ID, version); err != nil {
			return admitted, err
		}
		admitted++
		// The video's companions follow it: each companion operation is
		// admitted after the video's and moves only once the video settled
		// at its target.
		if len(unit) > 0 {
			count, err := s.admitCompanionAlignments(ctx, base, candidate, target, unit, policy, false)
			if err != nil {
				return admitted, err
			}
			admitted += count
		}
	}
	return admitted, nil
}

func (s *CalibrationService) ensureAlignmentExecuteWorkItem(ctx context.Context, operationID string, expectedVersion int64) error {
	if s.workItems == nil {
		return errors.New("alignment execute work item store is not configured")
	}
	idempotencyKey := "align:execute:" + operationID
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	var existingID string
	err = tx.QueryRowContext(ctx, `
SELECT id
FROM work_items
WHERE owner_type = $1 AND owner_id = $2 AND kind = $3
  AND idempotency_key = $4 AND state IN ('available', 'leased')`,
		workitems.OwnerAlignment, operationID, workitems.KindAlignmentExecute, idempotencyKey).Scan(&existingID)
	if err == nil {
		_ = tx.Rollback()
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return err
	}

	input, err := json.Marshal(map[string]string{"trigger": "generation_completed"})
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	_, err = s.workItems.Create(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerAlignment, ID: operationID},
		Kind:                 workitems.KindAlignmentExecute,
		IdempotencyKey:       idempotencyKey,
		InputSnapshot:        input,
		MaxAttempts:          3,
		ExpectedOwnerVersion: expectedVersion,
	})
	if err == nil {
		return tx.Commit()
	}
	if !errors.Is(err, workitems.ErrLiveItemConflict) {
		_ = tx.Rollback()
		return err
	}
	originalErr := err
	_ = tx.Rollback()

	if err := s.db.QueryRowContext(ctx, `
SELECT id
FROM work_items
WHERE owner_type = $1 AND owner_id = $2 AND kind = $3
  AND idempotency_key = $4 AND state IN ('available', 'leased')`,
		workitems.OwnerAlignment, operationID, workitems.KindAlignmentExecute, idempotencyKey).Scan(&existingID); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return originalErr
}

// alignmentOperationID derives the stable operation id from the idempotency
// key (bounded length).
func alignmentOperationID(idempotencyKey string) string {
	sum := sha256.Sum256([]byte(idempotencyKey))
	return "alg-" + hex.EncodeToString(sum[:16])
}

// ownerVersions reads the metadata owner version and the rules config owner
// version (the naming rules live in the rules owner).
// Subscribe always creates both owners at version 1, so a missing row means a
// pre-subscribe edge and reports the minimum 1 (the frozen alignment input
// requires versions >= 1).
func ownerVersions(ctx context.Context, db *sql.DB, animeID string) (int64, int64, error) {
	var metadataVersion int64
	err := db.QueryRowContext(ctx, `SELECT version FROM anime_metadata WHERE managed_anime_id = $1`, animeID).Scan(&metadataVersion)
	if errors.Is(err, sql.ErrNoRows) {
		metadataVersion = 1
	} else if err != nil {
		return 0, 0, err
	}
	var rulesVersion int64
	err = db.QueryRowContext(ctx, `SELECT rules_version FROM anime_business_configs WHERE managed_anime_id = $1`, animeID).Scan(&rulesVersion)
	if errors.Is(err, sql.ErrNoRows) {
		rulesVersion = 1
	} else if err != nil {
		return 0, 0, err
	}
	return metadataVersion, rulesVersion, nil
}

// ConsumeReplacementHandler is the replacement consumer of one completed
// generation. It reads Placement coverage/group/quality facts behind the
// Subscription active gate: only an active Subscription merges the
// replacement_evaluate claim (evaluate/settle/cleanup run in their own frozen
// chain). A stopped or missing Subscription finishes the consumer blocked with
// zero replacement increments; resume uses the latest generation facts in a
// fresh epoch. A trigger failure returns the error for the consumer's own
// retry policy — the alignment consumer is never touched.
func (s *CalibrationService) ConsumeReplacementHandler() worker.Handler {
	return calibrationRecoveryHandler{service: s, kind: workitems.KindReconciliationConsumeReplacement, handle: func(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
		if claim.Kind != workitems.KindReconciliationConsumeReplacement {
			return worker.Outcome{}, fmt.Errorf("replacement consumer received kind %q", claim.Kind)
		}
		animeID := claim.Owner.ID
		generation, err := claimGeneration(claim)
		if err != nil {
			return worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: "failed", ReasonCode: "replacement_claim_invalid", Reason: err.Error()}, nil), nil
		}
		checkpoint, err := loadConsumerCheckpoint(ctx, s.db, animeID, generation, ConsumerReplacement)
		if err != nil {
			return worker.Outcome{}, err
		}
		if checkpoint != nil && (checkpoint.State == ConsumerConsumed || checkpoint.State == ConsumerBlocked) {
			return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: checkpoint.State, ReasonCode: "replacement_consumer_already_settled", Reason: "consumer checkpoint already terminal"}, nil), nil
		}
		var subState string
		err = s.db.QueryRowContext(ctx, `SELECT state FROM subscriptions WHERE managed_anime_id = $1`, animeID).Scan(&subState)
		if errors.Is(err, sql.ErrNoRows) {
			if err := settleConsumerCheckpoint(ctx, s.db, animeID, generation, ConsumerReplacement, ConsumerBlocked, "subscription_missing", "no subscription; replacement waits for a fresh subscribe epoch"); err != nil {
				return worker.Outcome{}, err
			}
			return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "blocked", ReasonCode: "subscription_missing", Reason: "no subscription; replacement waits for a fresh subscribe epoch"}, nil), nil
		}
		if err != nil {
			return worker.Outcome{}, err
		}
		if subState != "active" {
			// Stopped generation: observations/calibrated facts stay, alignment
			// may run independently, replacement increments stay zero; resume
			// creates a fresh epoch from the latest facts.
			if err := settleConsumerCheckpoint(ctx, s.db, animeID, generation, ConsumerReplacement, ConsumerBlocked, "subscription_stopped", "subscription is stopped; replacement evaluates on resume with the latest generation"); err != nil {
				return worker.Outcome{}, err
			}
			return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "blocked", ReasonCode: "subscription_stopped", Reason: "subscription is stopped; replacement evaluates on resume with the latest generation"}, nil), nil
		}
		if s.trigger == nil {
			if err := settleConsumerCheckpoint(ctx, s.db, animeID, generation, ConsumerReplacement, ConsumerBlocked, "replacement_consumer_unavailable", "replacement trigger is not wired"); err != nil {
				return worker.Outcome{}, err
			}
			return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "blocked", ReasonCode: "replacement_consumer_unavailable", Reason: "replacement trigger is not wired"}, nil), nil
		}
		// Active gate passed: the trigger merges the one live evaluate claim
		// (frozen Subscription version epoch). Any error is retried by this
		// consumer's own claim policy; the checkpoint stays pending.
		if err := s.trigger.TriggerGenerationCompleted(ctx, animeID); err != nil {
			return worker.Outcome{}, fmt.Errorf("replacement consumer trigger: %w", err)
		}
		if err := settleConsumerCheckpoint(ctx, s.db, animeID, generation, ConsumerReplacement, ConsumerConsumed, "replacement_consumed", "replacement evaluation merged"); err != nil {
			return worker.Outcome{}, err
		}
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "consumed", ReasonCode: "replacement_consumed", Reason: "replacement evaluation merged"}, nil), nil
	}}
}

// claimGeneration decodes the generation from a consumer claim input.
func claimGeneration(claim workitems.Claim) (int64, error) {
	var input struct {
		Generation int64 `json:"generation"`
	}
	if err := json.Unmarshal(claim.InputSnapshot, &input); err != nil || input.Generation < 1 {
		return 0, errors.New("claim input must carry generation")
	}
	return input.Generation, nil
}

// ListConsumerCheckpoints returns the folded consumer outcomes of one anime
// (side-effect free) for the owner truth surface. PRUNE-046 S10 keeps only the
// latest settled generation per consumer, so the list holds at most one row per
// consumer instead of the cross-generation history.
func (s *CalibrationService) ListConsumerCheckpointsApplication(ctx context.Context, animeID string, limit, offset int) ([]ReconciliationConsumerStateView, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT managed_anime_id, alignment_checkpoint_generation, 'alignment',
       alignment_checkpoint_state, COALESCE(alignment_checkpoint_reason_code, ''),
       COALESCE(alignment_checkpoint_reason, ''), alignment_checkpoint_version,
       COALESCE(alignment_checkpoint_updated_at, updated_at)
FROM reconciliation_scans
WHERE managed_anime_id = $1 AND alignment_checkpoint_state IS NOT NULL
UNION ALL
SELECT managed_anime_id, replacement_checkpoint_generation, 'replacement',
       replacement_checkpoint_state, COALESCE(replacement_checkpoint_reason_code, ''),
       COALESCE(replacement_checkpoint_reason, ''), replacement_checkpoint_version,
       COALESCE(replacement_checkpoint_updated_at, updated_at)
FROM reconciliation_scans
WHERE managed_anime_id = $1 AND replacement_checkpoint_state IS NOT NULL
ORDER BY 2 DESC, 3
LIMIT $2 OFFSET $3`, animeID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	views := make([]ReconciliationConsumerStateView, 0, limit)
	for rows.Next() {
		var (
			view       ReconciliationConsumerStateView
			consumer   string
			state      string
			reasonCode string
			reason     string
		)
		if err := rows.Scan(&view.ManagedAnimeID, &view.Generation, &consumer, &state,
			&reasonCode, &reason, &view.Version, &view.UpdatedAt); err != nil {
			return nil, err
		}
		view.Consumer = consumer
		view.State = state
		if reasonCode != "" {
			view.ReasonCode = &reasonCode
		}
		if reason != "" {
			view.Reason = &reason
		}
		views = append(views, view)
	}
	return views, rows.Err()
}

// persistedCoverage reads canonical coverage from persisted scan facts. An
// observation written before coverage persistence has no provable coverage and
// is reported unresolved: it is never re-derived from the filename, so the
// legacy scan path cannot become a second coverage authority.
func persistedCoverage(facts []byte) []string {
	if len(facts) > 0 && string(facts) != "{}" && string(facts) != "null" {
		var persisted struct {
			Coverage           []string `json:"coverage"`
			CoverageUnresolved bool     `json:"coverage_unresolved"`
		}
		if err := json.Unmarshal(facts, &persisted); err == nil && len(persisted.Coverage) > 0 {
			if persisted.CoverageUnresolved {
				return []string{releasefacts.CoverageUnresolved}
			}
			return persisted.Coverage
		}
	}
	return []string{releasefacts.CoverageUnresolved}
}

// placementObservationFacts is the Placement-owned observation evidence
// written by the reconciliation scan handler.
type placementObservationFacts struct {
	PlacementID string `json:"placement_id"`
	Disposition string `json:"disposition"`
	LocatedVia  string `json:"located_via"`
	LocatedPath string `json:"located_path"`
}

// decodePlacementObservation extracts the Placement-owned observation facts;
// observations without placement_id are legacy full-library generations and
// fall back to the read-time parse path.
func decodePlacementObservation(facts []byte) placementObservationFacts {
	var observation placementObservationFacts
	if len(facts) > 0 && string(facts) != "{}" && string(facts) != "null" {
		_ = json.Unmarshal(facts, &observation)
	}
	return observation
}

// placementCoverageTx reads the frozen coverage set of one placement inside
// the caller's transaction.
func placementCoverageTx(ctx context.Context, tx *sql.Tx, placementID string) []string {
	rows, err := tx.QueryContext(ctx, `
SELECT observation_key FROM placement_coverage
WHERE placement_id = $1 ORDER BY coverage_ordinal`, placementID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var coverage []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil
		}
		coverage = append(coverage, key)
	}
	return coverage
}

// persistedQuality reads one normalized quality field from persisted scan
// facts (empty when absent).
func persistedQuality(facts []byte, field string) string {
	if len(facts) == 0 || string(facts) == "{}" || string(facts) == "null" {
		return ""
	}
	var value map[string]any
	if err := json.Unmarshal(facts, &value); err != nil {
		return ""
	}
	if raw, ok := value[field].(string); ok {
		return raw
	}
	return ""
}

// fingerprintValue extracts the plain digest from a stored typed fingerprint
// object ({"value": "..."}); legacy plain digests pass through unchanged.
func fingerprintValue(stored string) string {
	trimmed := strings.TrimSpace(stored)
	if !strings.HasPrefix(trimmed, "{") {
		return trimmed
	}
	var envelope struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		return trimmed
	}
	return envelope.Value
}

// activePlacementHoldsPathTx reports whether an active Placement of the anime
// already sits at path.
func activePlacementHoldsPathTx(ctx context.Context, tx *sql.Tx, animeID, path string) (bool, error) {
	var held bool
	err := tx.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM placements
               WHERE managed_anime_id = $1 AND physical_path = $2 AND state = 'active')`, animeID, path).Scan(&held)
	return held, err
}
