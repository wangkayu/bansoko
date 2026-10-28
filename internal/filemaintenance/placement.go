package filemaintenance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	planner "bansoko/internal/filemaintenance/planner"
	"bansoko/internal/observability/businesslog"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	facts "bansoko/internal/release/facts"
)

// FileOperationEvidence is the core-chain verified evidence port: only
// Artifact verified -> FileOperation completed -> target fingerprint reread
// matched is sufficient for admission.
type FileOperationEvidence struct {
	OperationID       string
	ManagedAnimeID    string
	ReleaseID         string
	ArtifactVerified  bool
	TargetPath        string
	SourceFingerprint string
	TargetFingerprint string
	FileKind          string // video | subtitle (artifact file media kind)
	FileRef           string // artifact file ref matching the frozen media fact file row
	CorrelationID     string // media lineage, inherited from the file operation
	// UnknownFile marks an operation the plan rendered with the unknown
	// template (render snapshot unknown_file): a video without an episode
	// identity, including a movie's non-main video.
	UnknownFile bool
}

// FileOperationEvidenceSource reads the frozen file operation evidence.
type FileOperationEvidenceSource interface {
	ReadVerifiedEvidence(ctx context.Context, fileOperationID string) (*FileOperationEvidence, bool, error)
}

// MediaFileFactsReader reads the frozen selected-parser evidence of a Release
// media fact snapshot. Placement coverage is a projection of that frozen
// per-file EpisodeIdentity, never a re-parse of a title or filename.
type MediaFileFactsReader interface {
	ReadSnapshot(ctx context.Context, releaseID string) (*facts.Snapshot, error)
	ListFileRows(ctx context.Context, snapshotID string) ([]facts.FileRow, error)
}

type SnapshotEvidenceReader interface {
	GenerationUsable(ctx context.Context, animeID string, generation int64) (bool, error)
	ReadObservation(ctx context.Context, animeID string, generation int64, observationRef string) (*planner.SnapshotObservation, bool, error)
}

// PlacementService owns verified physical placement facts. It records only
// proven facts and never decides admission policy (P3.5 owns all/per_group/
// single_copy settlement).
type PlacementService struct {
	db       *sql.DB
	fileOp   FileOperationEvidenceSource
	snapshot SnapshotEvidenceReader
	now      func() time.Time
	activity observabilityport.ActivityWriter
}

// NewPlacementService wires the placement owner.
func NewPlacementService(db *sql.DB, fileOp FileOperationEvidenceSource, snapshot SnapshotEvidenceReader) *PlacementService {
	return &PlacementService{db: db, fileOp: fileOp, snapshot: snapshot, now: time.Now}
}

// writer. Unset (nil) keeps admission behavior identical without Activity.
func (s *PlacementService) SetActivityWriter(writer observabilityport.ActivityWriter) {
	if s != nil {
		s.activity = writer
	}
}

// FrozenFileCoverage resolves the frozen coverage key of one artifact file from
// the latest Release media fact snapshot. It distinguishes three outcomes:
//
//   - ok=true: the frozen identity is resolved; coverage is the frozen
//     resolver keys (several for a multi-episode file) and factsVersion the frozen snapshot schema version.
//   - ok=false, err=nil: the frozen identity is genuinely unresolved; callers
//     treat the file as an unknown file (never a re-parse).
//   - err non-nil: the evidence is missing (ErrPlacementEvidenceMissing) or a
//     database/decode read failed; the caller must retry and must NOT write a
//     permanent unknown-file placement.
func FrozenFileCoverage(ctx context.Context, reader MediaFileFactsReader, releaseID, fileRef string) ([]string, string, bool, error) {
	if reader == nil {
		return nil, "", false, nil
	}
	snapshot, err := reader.ReadSnapshot(ctx, releaseID)
	if errors.Is(err, sql.ErrNoRows) {
		// A reader that reports the absence of the snapshot row as sql.ErrNoRows
		// must still land in the typed "evidence does not exist" class: treating
		// it as a database read failure would report a missing frozen snapshot as
		// a transient backend fault.
		return nil, "", false, fmt.Errorf("%w: release %s has no media fact snapshot", ErrPlacementEvidenceMissing, releaseID)
	}
	if err != nil {
		return nil, "", false, fmt.Errorf("read media fact snapshot: %w", err)
	}
	if snapshot == nil {
		return nil, "", false, fmt.Errorf("%w: release %s has no media fact snapshot", ErrPlacementEvidenceMissing, releaseID)
	}
	if strings.TrimSpace(fileRef) == "" {
		return nil, "", false, fmt.Errorf("%w: artifact file carries no frozen file ref", ErrPlacementEvidenceMissing)
	}
	rows, err := reader.ListFileRows(ctx, snapshot.ID)
	if err != nil {
		return nil, "", false, fmt.Errorf("list media fact file rows: %w", err)
	}
	for _, row := range rows {
		if row.FileRef != fileRef {
			continue
		}
		identity := row.EffectiveFacts.Identity
		keys := identity.Keys()
		if identity.State != "resolved" || len(keys) == 0 {
			return nil, "", false, nil
		}
		// A multi-episode file occupies every member key.
		return keys, snapshot.SchemaVersion, true, nil
	}
	return nil, "", false, fmt.Errorf("%w: snapshot %s has no row for file %s", ErrPlacementEvidenceMissing, snapshot.ID, fileRef)
}

// ErrPlacementCoverageMissing reports a verified video file operation whose
// frozen per-file coverage the caller could not resolve. An eligible placement
// must carry provenance-backed coverage; the owner never invents an episode, and
// the caller must choose the unknown-file admission.
var ErrPlacementCoverageMissing = errors.New("verified video file operation has no frozen coverage")

// ErrPlacementEvidenceMissing reports that the frozen per-file evidence a
// verified video placement must carry does not exist at all: the Release has
// no media fact snapshot or the snapshot has no row for the artifact file.
// Unlike an unresolved identity (an unknown file, released at admission),
// missing evidence is typed, retryable, and must never be recorded as a
// permanent unknown-file placement.
var ErrPlacementEvidenceMissing = errors.New("frozen media fact evidence is missing")

// AdmitFromFileOperation records a verified placement from the core chain.
// coverage/factsVersion must be the frozen media fact coverage the caller
// resolved (see FrozenFileCoverage); this owner never re-derives an episode.
// Idempotent per file operation (source unique index).
func (s *PlacementService) AdmitFromFileOperation(ctx context.Context, fileOperationID string, coverage []string, factsVersion string) (string, error) {
	id, _, err := s.admitFromFileOperation(ctx, fileOperationID, coverage, factsVersion)
	return id, err
}

// AdmitFromFileOperationResult applies the verified file-operation admission
// policy and reports whether a placement was admitted. Non-video operations are
// valid inputs but do not own an episode placement or replacement trigger. An
// eligible video without frozen coverage fails closed with
// ErrPlacementCoverageMissing.
func (s *PlacementService) AdmitFromFileOperationResult(ctx context.Context, fileOperationID string, coverage []string, factsVersion string) (string, bool, error) {
	return s.admitFromFileOperation(ctx, fileOperationID, coverage, factsVersion)
}

func (s *PlacementService) admitFromFileOperation(ctx context.Context, fileOperationID string, coverage []string, factsVersion string) (string, bool, error) {
	evidence, found, err := s.fileOp.ReadVerifiedEvidence(ctx, fileOperationID)
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, fmt.Errorf("file operation %s not found", fileOperationID)
	}
	if !evidence.ArtifactVerified || evidence.TargetFingerprint == "" {
		return "", false, fmt.Errorf("file operation %s is not verified evidence (artifact verified=%v target fingerprint=%q)",
			fileOperationID, evidence.ArtifactVerified, evidence.TargetFingerprint)
	}
	if evidence.FileKind != "" && evidence.FileKind != "video" {
		return "", false, nil
	}
	if len(coverage) == 0 {
		// An eligible video placement must carry the frozen per-file coverage the
		// caller resolved from the selected parser's evidence. The placement owner
		// never re-derives an episode from a title or filename, so a missing
		// coverage is a fail-closed condition: the caller must choose the explicit
		// unknown-file admission instead.
		return "", false, fmt.Errorf("%w: file operation %s", ErrPlacementCoverageMissing, fileOperationID)
	}
	id, err := s.admit(ctx, placementInput{
		SourceEvidenceType:  "file_operation",
		SourceOwnerRef:      fileOperationID,
		PhysicalPath:        evidence.TargetPath,
		SourceFingerprint:   evidence.SourceFingerprint,
		TargetFingerprint:   evidence.TargetFingerprint,
		QualityFactsRef:     evidence.ReleaseID,
		FactsVersion:        factsVersion,
		Coverage:            coverage,
		ManagedAnimeID:      evidence.ManagedAnimeID,
		CorrelationID:       evidence.CorrelationID,
		CausationType:       "file_operation",
		CausationID:         fileOperationID,
		ReplacementEligible: true,
	})
	if err != nil {
		return id, false, err
	}
	// Sidecars that completed before the video's handoff are registered in
	// the same continuation; a failure retries with the idempotent admission.
	if _, err := s.RegisterCompanions(ctx, fileOperationID); err != nil {
		return id, false, fmt.Errorf("%w: %v", ErrCompanionRegistration, err)
	}
	return id, true, nil
}

// ErrCompanionRegistration reports that the placement committed but its
// companion registration did not; the caller's continuation retries both.
var ErrCompanionRegistration = errors.New("companion registration failed")

// AdmitSidecarFromFileOperation registers a completed bound subtitle operation
// as a companion of its video's placement.
// It is a no-op until the video's placement exists; the video's own admission
// then registers it.
func (s *PlacementService) AdmitSidecarFromFileOperation(ctx context.Context, fileOperationID string) error {
	if _, err := s.RegisterCompanions(ctx, fileOperationID); err != nil {
		return fmt.Errorf("%w: %v", ErrCompanionRegistration, err)
	}
	return nil
}

// UnknownFileReleaseActor records who released an unknown file's placement:
// the import itself, not a user.
const UnknownFileReleaseActor = "system:unknown_file_import"

// AdmitUnknownFromFileOperation records a verified unknown file:
// a video without an episode identity that the `route` choice of
// unclassified_route wrote with the unknown template. Its placement is inserted and released in one
// transaction together with its prefix-bound subtitles, so it is never
// managed: no coverage, scan, alignment or replacement, and an unsubscribe
// never cleans it up. It is an ordinary released file from then on.
// Idempotent per file operation: a replay registers late subtitles as
// released and appends no second activity entry.
func (s *PlacementService) AdmitUnknownFromFileOperation(ctx context.Context, fileOperationID string) (string, error) {
	evidence, found, err := s.fileOp.ReadVerifiedEvidence(ctx, fileOperationID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("file operation %s not found", fileOperationID)
	}
	if !evidence.ArtifactVerified || evidence.TargetFingerprint == "" {
		return "", fmt.Errorf("file operation %s is not verified evidence (artifact verified=%v target fingerprint=%q)",
			fileOperationID, evidence.ArtifactVerified, evidence.TargetFingerprint)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	id, err := s.admitTx(ctx, tx, placementInput{
		SourceEvidenceType:  "file_operation",
		SourceOwnerRef:      fileOperationID,
		PhysicalPath:        evidence.TargetPath,
		SourceFingerprint:   evidence.SourceFingerprint,
		TargetFingerprint:   evidence.TargetFingerprint,
		QualityFactsRef:     evidence.ReleaseID,
		FactsVersion:        "unknown",
		ManagedAnimeID:      evidence.ManagedAnimeID,
		CorrelationID:       evidence.CorrelationID,
		CausationType:       "file_operation",
		CausationID:         fileOperationID,
		ReplacementEligible: false,
	})
	if err != nil {
		return "", err
	}
	// An unknown file can still replace a managed file under the overwrite
	// setting: that placement is displaced before this one is released.
	if err := displaceForFileOperationTx(ctx, tx, id, fileOperationID, evidence.TargetPath); err != nil {
		return "", err
	}
	var version int64
	err = tx.QueryRowContext(ctx, `
UPDATE placements
SET state = 'released', released_at = CURRENT_TIMESTAMP, released_by = $2, release_request_key = $3,
    version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND state = 'active'
RETURNING version`, id, UnknownFileReleaseActor, "unknown-file:"+id).Scan(&version)
	releasedNow := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("release unknown file placement: %w", err)
	}
	// The placement is released before its subtitles register, so they are
	// recorded as released with it (ended_reason released).
	if _, err := registerCompanionsTx(ctx, tx, fileOperationID); err != nil {
		return "", fmt.Errorf("%w: %v", ErrCompanionRegistration, err)
	}
	if releasedNow && s.activity != nil {
		subscriptionID, err := subscriptionIDForAnime(ctx, tx, evidence.ManagedAnimeID)
		if err != nil {
			return "", fmt.Errorf("read subscription for placement activity: %w", err)
		}
		runContext, err := releaseRunContextTx(ctx, tx, evidence.ReleaseID)
		if err != nil {
			return "", err
		}
		details, err := runDetails(runContext, map[string]any{
			"file_name": businesslog.FileName(evidence.TargetPath), "library_folder": businesslog.LibraryFolder(evidence.TargetPath),
		})
		if err != nil {
			return "", fmt.Errorf("encode placement activity details: %w", err)
		}
		if err := s.activity.AppendTx(ctx, txscope.Wrap(tx), observabilityport.ActivityRecord{
			SubscriptionID:  subscriptionID,
			ManagedAnimeID:  evidence.ManagedAnimeID,
			ReleaseID:       evidence.ReleaseID,
			Details:         details,
			Kind:            "placement_changed",
			Outcome:         "info",
			ReasonCode:      "unknown_file_released",
			SubjectType:     "placement",
			SubjectID:       id,
			SubjectVersion:  version,
			StableResultKey: "unknown_file_released",
			MessageCode:     observabilityport.MessageUnknownFileReleased,
			CorrelationID:   evidence.CorrelationID,
			CausationType:   "file_operation",
			CausationID:     fileOperationID,
		}); err != nil {
			return "", fmt.Errorf("append placement activity: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

// AdmitFromSnapshot records a verified placement from a completed immutable
// snapshot generation. Unresolved coverage or partial generations fail
// closed.
func (s *PlacementService) AdmitFromSnapshot(ctx context.Context, animeID string, generation int64, observationRef string, factsVersion string) (string, error) {
	usable, err := s.snapshot.GenerationUsable(ctx, animeID, generation)
	if err != nil {
		return "", err
	}
	if !usable {
		return "", fmt.Errorf("snapshot generation %d for %s is not completed/immutable", generation, animeID)
	}
	observation, found, err := s.snapshot.ReadObservation(ctx, animeID, generation, observationRef)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("observation %s not found in generation %d", observationRef, generation)
	}
	if len(observation.Coverage) == 0 || (len(observation.Coverage) == 1 && observation.Coverage[0] == "unresolved") {
		return "", fmt.Errorf("observation %s has unresolved coverage; placement excluded", observationRef)
	}
	return s.admit(ctx, placementInput{
		SourceEvidenceType:  "reconciliation",
		SourceOwnerRef:      observationRef,
		PhysicalPath:        observation.FilePath,
		SourceFingerprint:   observation.Fingerprint,
		TargetFingerprint:   observation.Fingerprint,
		FactsVersion:        factsVersion,
		Coverage:            observation.Coverage,
		ManagedAnimeID:      animeID,
		SnapshotGeneration:  generation,
		ReplacementEligible: true,
	})
}

type placementInput struct {
	SourceEvidenceType  string
	SourceOwnerRef      string
	PhysicalPath        string
	SourceFingerprint   string
	TargetFingerprint   string
	QualityFactsRef     string
	FactsVersion        string
	Coverage            []string
	ManagedAnimeID      string
	SnapshotGeneration  int64
	CorrelationID       string
	CausationType       string
	CausationID         string
	ReplacementEligible bool
}

func (s *PlacementService) admit(ctx context.Context, input placementInput) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	id, err := s.admitTx(ctx, tx, input)
	if err != nil {
		return "", err
	}
	if s.activity != nil {
		subscriptionID, err := subscriptionIDForAnime(ctx, tx, input.ManagedAnimeID)
		if err != nil {
			return "", fmt.Errorf("read subscription for placement activity: %w", err)
		}
		// A file-operation placement belongs to its Release's download run:
		// the quality facts ref is that Release.
		releaseID := ""
		if input.SourceEvidenceType == "file_operation" {
			releaseID = input.QualityFactsRef
		}
		runContext, err := releaseRunContextTx(ctx, tx, releaseID)
		if err != nil {
			return "", err
		}
		details, err := runDetails(runContext, map[string]any{
			"file_name": businesslog.FileName(input.PhysicalPath), "library_folder": businesslog.LibraryFolder(input.PhysicalPath),
		})
		if err != nil {
			return "", fmt.Errorf("encode placement activity details: %w", err)
		}
		if err := s.activity.AppendTx(ctx, txscope.Wrap(tx), observabilityport.ActivityRecord{
			SubscriptionID:  subscriptionID,
			ManagedAnimeID:  input.ManagedAnimeID,
			Kind:            "placement_changed",
			Outcome:         "success",
			ReasonCode:      "admitted",
			SubjectType:     "placement",
			SubjectID:       id,
			SubjectVersion:  1,
			StableResultKey: "admitted",
			MessageCode:     observabilityport.MessagePlacementRecorded,
			ReleaseID:       releaseID,
			Details:         details,
			CorrelationID:   input.CorrelationID,
			CausationType:   nullableTextString(input.CausationType),
			CausationID:     nullableTextString(input.CausationID),
		}); err != nil {
			return "", fmt.Errorf("append placement activity: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

// admitTx persists one placement and its coverage set inside the caller's
// transaction (idempotent per source evidence). The calibration owner reuses
// it so a whole generation's placements commit atomically with the typed
// calibration facts.
func (s *PlacementService) admitTx(ctx context.Context, tx *sql.Tx, input placementInput) (string, error) {
	placementID := PlacementIDFor(input.SourceEvidenceType, input.SourceOwnerRef)
	var generation any
	if input.SnapshotGeneration > 0 {
		generation = input.SnapshotGeneration
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO placements
(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, quality_facts_ref, facts_version, snapshot_generation,
 correlation_id, causation_type, causation_id, replacement_eligible, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT (source_evidence_type, source_owner_ref) DO NOTHING`,
		placementID, input.PhysicalPath, input.ManagedAnimeID, input.SourceEvidenceType, input.SourceOwnerRef,
		input.SourceFingerprint, input.TargetFingerprint, nullableText(input.QualityFactsRef), input.FactsVersion, generation,
		nullableText(input.CorrelationID), nullableText(input.CausationType), nullableText(input.CausationID),
		input.ReplacementEligible)
	if err != nil {
		return "", err
	}
	// Coverage set must commit atomically with the placement. Missing coverage
	// is a fail-closed condition for eligible placements; an unknown file
	// (replacement_eligible=false, released in the same transaction)
	// deliberately carries none.
	if len(input.Coverage) == 0 && input.ReplacementEligible {
		return "", fmt.Errorf("placement %s has no coverage set; not admitted", placementID)
	}
	for ordinal, key := range input.Coverage {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO placement_coverage
(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (placement_id, observation_key) DO NOTHING`,
			placementID, key, ordinal, input.FactsVersion, input.SourceOwnerRef); err != nil {
			return "", err
		}
	}
	if input.SourceEvidenceType == "file_operation" {
		if err := displaceForFileOperationTx(ctx, tx, placementID, input.SourceOwnerRef, input.PhysicalPath); err != nil {
			return "", err
		}
	}
	return placementID, nil
}

// displaceForFileOperationTx runs the displacement transition in the
// transaction that activates a file operation's placement. Another active or superseded placement at the same path lost
// its file to this one — replaced under the overwrite setting, or already
// gone — and becomes displaced with this placement as its successor, unless
// the delivery's approved quality replacement names the path: that loser is
// superseded by its decision instead. A placement of the same instance with a
// pending redownload request that shares a covered episode is displaced
// wherever the new file landed. A replay is a no-op.
func displaceForFileOperationTx(ctx context.Context, tx *sql.Tx, placementID, fileOperationID, path string) error {
	if _, err := tx.ExecContext(ctx, `
SELECT public.placement_displace($1, 'file_operation:'||$2, NOT EXISTS (
    SELECT 1 FROM file_operations o
    JOIN acquisitions i ON i.id = o.acquisition_id
    CROSS JOIN LATERAL public.delivery_replacements(i.input->'delivery') r(value)
    WHERE o.id = $2 AND r.value->'expected_object'->'location'->>'canonical_path' = $3), NULL)`,
		placementID, fileOperationID, path); err != nil {
		return fmt.Errorf("displace placements replaced by %s: %w", placementID, err)
	}
	return nil
}

// ListPlacements returns verified placements (side-effect free).
func (s *PlacementService) ListPlacementsApplication(ctx context.Context, animeID string, limit, offset int) ([]PlacementView, error) {
	return s.listPlacements(ctx, animeID, limit, offset, true)
}

// ListPlacementsAllStates returns every placement of one anime including
// superseded ones (the replacement/cleanup owner truth of the overview).
func (s *PlacementService) ListPlacementsAllStates(ctx context.Context, animeID string, limit, offset int) ([]PlacementView, error) {
	return s.listPlacements(ctx, animeID, limit, offset, false)
}

func (s *PlacementService) listPlacements(ctx context.Context, animeID string, limit, offset int, activeOnly bool) ([]PlacementView, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	filter := ""
	if activeOnly {
		filter = "AND verification_state = 'verified' AND state = 'active'"
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
       target_fingerprint, facts_version, verification_state, state, version, created_at
FROM placements
WHERE ($1 = '' OR managed_anime_id = $1) `+filter+`
ORDER BY created_at DESC
LIMIT $2 OFFSET $3`, animeID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	views := make([]PlacementView, 0, limit)
	for rows.Next() {
		var (
			view         PlacementView
			state        string
			verification string
		)
		if err := rows.Scan(&view.ID, &view.PhysicalPath, &view.ManagedAnimeID, &view.SourceEvidenceType,
			&view.SourceOwnerRef, &view.TargetFingerprint, &view.FactsVersion, &verification, &state,
			&view.Version, &view.CreatedAt); err != nil {
			return nil, err
		}
		view.VerificationState = verification
		view.State = state
		views = append(views, view)
	}
	return views, rows.Err()
}

// GetCoverage returns the full coverage set of one placement.
func (s *PlacementService) GetCoverageApplication(ctx context.Context, placementID string) ([]PlacementCoverageView, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT observation_key, coverage_ordinal, facts_version, evidence_ref
FROM placement_coverage WHERE placement_id = $1 ORDER BY coverage_ordinal`, placementID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	views := make([]PlacementCoverageView, 0)
	for rows.Next() {
		var view PlacementCoverageView
		if err := rows.Scan(&view.ObservationKey, &view.CoverageOrdinal, &view.FactsVersion, &view.EvidenceRef); err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, rows.Err()
}

// nullableTextString flattens an empty string to "" for Activity fields.
func nullableTextString(value string) string {
	return value
}

// subscriptionIDForAnime resolves the owning subscription id for Activity
// records inside the caller's transaction.
func subscriptionIDForAnime(ctx context.Context, tx *sql.Tx, animeID string) (string, error) {
	var subscriptionID string
	err := tx.QueryRowContext(ctx, `
SELECT id FROM subscriptions WHERE managed_anime_id = $1`, animeID).Scan(&subscriptionID)
	return subscriptionID, err
}

// PlacementIDFor derives the stable bounded placement id from the evidence
// identity. The hash keeps long observation refs (reconciliation file paths)
// within the id CHECK bound, while the (source_evidence_type, source_owner_ref)
// unique index keeps admission idempotent for any legacy id format. Every
// consumer that resolves a placement by evidence must use this same function.
func PlacementIDFor(sourceEvidenceType, sourceOwnerRef string) string {
	sum := sha256.Sum256([]byte(sourceEvidenceType + "|" + sourceOwnerRef))
	return "plc-" + hex.EncodeToString(sum[:16])
}
