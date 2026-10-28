package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/filemaintenance/planner"
	"bansoko/internal/library/displaypath"
	"bansoko/internal/workitems"
)

// The per-anime files read: one anime's managed
// files — its active video placements with their bound companions — each with
// the result of the latest calibrated scan, the target the alignment chain
// would move it to, and the actions available now. It is a read aggregation
// over the placement, calibration, path-diff and alignment owners; it never
// scans, renders a path the alignment renderer would not, or touches a file.
// The server decides every status; the browser only displays them.

// Video row statuses.
const (
	AnimeFileUnscanned  = "unscanned"
	AnimeFileAligned    = "aligned"
	AnimeFileMisaligned = "misaligned"
	AnimeFileMissing    = "missing"
	AnimeFileChanged    = "changed"
	AnimeFileNoTarget   = "no_target"
)

// Companion row statuses (besides unscanned/aligned/misaligned/missing).
const AnimeFileSizeMismatch = "size_mismatch"

// Action reasons. They travel as reason codes and are shown only through the
// copy catalog.
const (
	AnimeFilesReasonOwnerRetired      = "owner_retired"
	AnimeFilesReasonNotStarted        = "not_started"
	AnimeFilesReasonScanRunning       = "scan_running"
	AnimeFilesReasonScanChanged       = "scan_changed"
	AnimeFilesReasonAlignmentRunning  = "alignment_in_progress"
	AnimeFilesReasonRescanRequired    = "rescan_required"
	AnimeFilesReasonNothingToAlign    = "nothing_to_align"
	AnimeFilesReasonFileNotMissing    = "file_not_missing"
	AnimeFilesReasonSourceUnavailable = "source_release_unavailable"
	AnimeFilesReasonScanRequired      = "scan_required"
	// AnimeFilesReasonRecoveryGate is why a recovery confirm is unavailable
	// for a row whose candidate exists but the read is stale or busy: a
	// newer generation decided, or an alignment is in flight. The confirm's
	// own transaction re-checks the same gate.
	AnimeFilesReasonRecoveryGate       = "recovery_unavailable"
	animeFilesMaxPlacements            = 500
	animeFilesPathDiffPage             = 500
	animeFilesAlignmentStateInProgress = "in_progress"
	animeFilesAlignmentStateFailed     = "failed"
)

// AnimeFileAction is one action with its availability; ReasonCode explains an
// unavailable action.
type AnimeFileAction struct {
	Available  bool
	ReasonCode string
}

// AnimeFileAlignmentView is the alignment operation of the latest scan for a
// file that has not settled successfully: in_progress, or failed with the
// failure reason.
type AnimeFileAlignmentView struct {
	State      string
	ReasonCode string
}

// AnimeFileRecoveryView is the frozen recovery candidate of one file row:
// the target the alignment renderer produced for the placement, the file the
// scan stat'ed there, and the companions frozen with it. The confirm
// endpoint re-verifies every fact before ownership re-attaches.
type AnimeFileRecoveryView struct {
	Generation int64
	TargetPath string
	// TargetRelativePath is TargetPath relative to the view root, so the client
	// displays the library-relative target without composing a path itself.
	TargetRelativePath string
	SizeBytes          int64
	// The frozen identity tokens the confirm echoes back: a stale dialog
	// (row re-pointed or re-rendered since) can never act.
	PlacementVersion int64
	Fingerprint      string
	// Companions is the frozen companion count. Recovery is atomic over the
	// whole unit: the confirm refuses unless EVERY companion still sits at
	// its frozen path, so there is no partial-present report.
	Companions int
}

// AnimeFileCompanionView is one bound companion of a video row.
type AnimeFileCompanionView struct {
	ID                 string
	MediaKind          string
	Path               string
	RelativePath       string
	SizeBytes          int64
	Status             string
	TargetPath         *string
	TargetRelativePath *string
	Alignment          *AnimeFileAlignmentView
}

// AnimeFileActions are the per-row actions.
type AnimeFileActions struct {
	Align   AnimeFileAction
	Rerun   AnimeFileAction
	Release AnimeFileAction
	// Recover is the explicit recovery confirm of a frozen scan candidate;
	// unavailable (nothing_to_recover) on rows without one.
	Recover AnimeFileAction
}

// AnimeFileView is one managed video file.
type AnimeFileView struct {
	PlacementID string
	ReleaseID   string
	// GroupName is the subtitle group of the placement's release: the display
	// name its rss_sources row records, the same value anime.group renders;
	// "" when the placement names no release. The name is never derived from
	// the release title.
	GroupName string
	// ReleaseTitle is the release's feed title; "" when unknown.
	ReleaseTitle       string
	MediaKind          string // tv | movie | unresolved
	Episodes           []string
	Path               string
	RelativePath       string
	SizeBytes          *int64
	Status             string
	StatusReason       string
	TargetPath         *string
	TargetRelativePath *string
	Alignment          *AnimeFileAlignmentView
	Actions            AnimeFileActions
	Companions         []AnimeFileCompanionView
	// Recovery is the frozen scan candidate when the latest scan found the
	// file's target missing and stat'ed the file there: the file the user may
	// re-adopt by explicit confirm. Nil = nothing to recover.
	Recovery *AnimeFileRecoveryView
}

// AnimeFilesScanView is the scan state of the tab: running while a scan or
// its calibration is pending; Generation is the latest calibrated generation
// the statuses come from (0 = never scanned).
type AnimeFilesScanView struct {
	State       string // idle | running | failed
	Generation  int64
	ScannedAt   *time.Time
	FailureCode *string
	// RescanRequired says this generation's alignment finished and moved at
	// least one file. The statuses still describe the pre-move scan, so only a
	// new scan shows the outcome. It is a server fact because the move can
	// finish between two reads of this view: the browser cannot see an
	// alignment that both started and settled between its polls, and a client
	// that inferred it from the in-flight flag alone skipped the rescan and left
	// the file it had just placed reading as 未扫描.
	RescanRequired bool
}

// AnimeFilesView is the per-anime files read.
type AnimeFilesView struct {
	ManagedAnimeID string
	Root           string
	Scan           AnimeFilesScanView
	Aligning       bool
	ScanAction     AnimeFileAction
	AlignAllAction AnimeFileAction
	Files          []AnimeFileView
}

// AnimeFilesAlignError says the one-off alignment is not admissible now;
// Reason is one of the AnimeFilesReason* codes.
type AnimeFilesAlignError struct{ Reason string }

func (e *AnimeFilesAlignError) Error() string { return "ANIME_FILES_ALIGN_UNAVAILABLE: " + e.Reason }

// AnimeFilesAlignResult is the outcome of a one-off alignment admission.
type AnimeFilesAlignResult struct {
	Generation int64
	Admitted   int
}

// ---- loaded facts -------------------------------------------------------

type animeFilesOwner struct {
	Lifecycle         string
	SubscriptionState string // "" = no subscription
}

type animeFilesPlacement struct {
	ID                  string
	Path                string
	ReleaseID           string // owned by this anime's subscription, else ""
	GroupName           string
	ReleaseTitle        string
	ExpectedSize        *int64
	Coverage            []string
	Companions          []animeFilesCompanion
	RedownloadRequested bool
	// Recovery is the placement's frozen recovery candidate from the latest
	// calibrated generation, if the scan froze one.
	Recovery *animeFilesRecoveryCandidate
}

type animeFilesCompanion struct {
	ID        string
	MediaKind string
	Path      string
	SizeBytes int64
}

type animeFilesAlignmentOp struct {
	ObservationRef string
	CompanionID    string
	State          string
	FailureCode    string
}

// animeFilesRecoveryCandidate is the frozen scan fact of one placement's
// all-locator-missing target: the COMPLETE rendered unit the user may
// re-adopt by explicit confirm. It mirrors the candidate the scan wrote;
// the confirm re-verifies every field (including the placement version and
// target fingerprint tokens) before the placement re-attaches.
type animeFilesRecoveryCandidate struct {
	PlacementID      string
	TargetPath       string
	Generation       int64
	SizeBytes        int64
	PlacementVersion int64
	Fingerprint      string
	// Companions is the frozen companion count; the confirm requires ALL of
	// them at their frozen paths, so there is no partial-present state.
	Companions int
	// CompanionTargets is each frozen companion's identity and rendered
	// target, in the order the scan froze them. The read model compares these
	// path for path against the CURRENT render: a count alone would offer a
	// confirm button for a unit whose companions now render elsewhere (and
	// whose confirm transaction must therefore refuse it).
	CompanionTargets []animeFilesRecoveryCompanionTarget
	// currentTargets is what the anime's CURRENT naming rules render for this
	// placement and each of its active companions, keyed by placement id and
	// companion id. It is produced at load time by the SAME renderer instance
	// the confirm re-renders with (pathDiff.renderer), so the offered action
	// and the accepted action can never come from two different rule sets.
	// A nil map means the render failed: the liveness gate then fails closed.
	currentTargets map[string]string
}

// animeFilesRecoveryCompanionTarget is one frozen companion of a recovery
// candidate: which companion, and where the scan froze its target.
type animeFilesRecoveryCompanionTarget struct {
	ID         string
	TargetPath string
}

type animeFilesScanOwner struct {
	State       string
	Generation  int64
	CompletedAt *time.Time
	FailureCode *string
}

type animeFilesFacts struct {
	AnimeID          string
	Owner            animeFilesOwner
	Scan             *animeFilesScanOwner
	CalibratePending bool
	Generation       int64 // latest calibrated generation (0 = none)
	GenerationAt     *time.Time
	Placements       []animeFilesPlacement
	Diffs            []PathDiffView
	Alignments       []animeFilesAlignmentOp
	AlignmentLive    bool
	// RecoveryReady marks the scan-generation invariant that admits a
	// recovery confirm: the files view is not stale behind a newer
	// generation and nothing is in flight. Per-row availability still
	// requires the placement's own frozen candidate.
	RecoveryReady bool
	// AlignmentMoved is set when an alignment of this generation completed a
	// move and nothing is in flight any more: the rows below still describe the
	// paths observed before that move.
	AlignmentMoved bool
}

// ListAnimeFilesApplication returns the per-anime files read.
func (s *Service) ListAnimeFilesApplication(ctx context.Context, animeID string) (AnimeFilesView, error) {
	if s.placements == nil || s.placements.db == nil || s.pathDiff == nil || s.pathDiff.renderer == nil {
		return AnimeFilesView{}, &apperrors.NotFound{Resource: "anime_files", ID: "not_wired"}
	}
	facts, err := loadAnimeFilesFacts(ctx, s.placements.db, s.pathDiff, animeID)
	if err != nil {
		return AnimeFilesView{}, err
	}
	return deriveAnimeFiles(facts), nil
}

// AlignAnimeFilesApplication admits a one-off alignment of this anime's files.
func (s *Service) AlignAnimeFilesApplication(ctx context.Context, animeID string, generation int64, placementIDs []string) (AnimeFilesAlignResult, error) {
	if s.calibration == nil {
		return AnimeFilesAlignResult{}, &apperrors.NotFound{Resource: "anime_files", ID: "not_wired"}
	}
	return s.calibration.AdmitOneOffAlignment(ctx, animeID, generation, placementIDs)
}

// AlignAnimeFilesWithRefreshApplication is the 对齐（刷新元数据）variant of the
// align action (owner decree 2026-10-22): it re-pulls the bound works
// bypassing the positive caches, re-reads the file rows so the awaiting-
// alignment new paths visibly reflect the fresh cache before the owner reads
// them, and only then admits the alignment. The plain align stays cache-first.
// A failed re-pull is a typed error and the alignment is never admitted.
func (s *Service) AlignAnimeFilesWithRefreshApplication(ctx context.Context, animeID string, generation int64, placementIDs []string) (AnimeFilesAlignResult, error) {
	if s.metadataRefresher == nil {
		return AnimeFilesAlignResult{}, &apperrors.Business{Status: 409, Code: "METADATA_REFRESH_UNAVAILABLE", Message: "The metadata re-pull is not wired; nothing was refreshed."}
	}
	if _, err := s.metadataRefresher.RefreshAnimeMetadataApplication(ctx, animeID); err != nil {
		return AnimeFilesAlignResult{}, err
	}
	if s.calibration == nil {
		return AnimeFilesAlignResult{}, &apperrors.NotFound{Resource: "anime_files", ID: "not_wired"}
	}
	return s.calibration.AdmitOneOffAlignment(ctx, animeID, generation, placementIDs)
}

func loadAnimeFilesFacts(ctx context.Context, db *sql.DB, pathDiff *PathDiffService, animeID string) (animeFilesFacts, error) {
	facts := animeFilesFacts{AnimeID: animeID}
	owner, found, err := loadAnimeFilesOwner(ctx, db, animeID)
	if err != nil {
		return facts, err
	}
	if !found {
		return facts, &apperrors.NotFound{Resource: "managed_anime", ID: animeID}
	}
	facts.Owner = owner
	if facts.Scan, err = loadAnimeFilesScan(ctx, db, animeID); err != nil {
		return facts, err
	}
	if facts.CalibratePending, err = liveFileMaintenanceItem(ctx, db, animeID, workitems.KindReconciliationCalibrate); err != nil {
		return facts, err
	}
	if facts.Generation, facts.GenerationAt, err = latestCalibratedGeneration(ctx, db, animeID); err != nil {
		return facts, err
	}
	if facts.Placements, err = loadAnimeFilesPlacements(ctx, db, animeID, pathDiff); err != nil {
		return facts, err
	}
	if facts.Generation > 0 {
		for offset := 0; ; offset += animeFilesPathDiffPage {
			page, err := pathDiff.ListPathDiffs(ctx, animeID, facts.Generation, animeFilesPathDiffPage, offset)
			if err != nil {
				return facts, err
			}
			facts.Diffs = append(facts.Diffs, page...)
			if len(page) < animeFilesPathDiffPage {
				break
			}
		}
		if facts.Alignments, err = loadAnimeFilesAlignments(ctx, db, animeID, facts.Generation); err != nil {
			return facts, err
		}
	}
	consumerLive, err := liveFileMaintenanceItem(ctx, db, animeID, workitems.KindReconciliationConsumeAlignment)
	if err != nil {
		return facts, err
	}
	var opsLive bool
	if err := db.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM alignment_operations WHERE managed_anime_id = $1 AND state IN ('planned', 'ready', 'running'))`, animeID).Scan(&opsLive); err != nil {
		return facts, fmt.Errorf("read live alignments: %w", err)
	}
	facts.AlignmentLive = consumerLive || opsLive
	if facts.Generation > 0 && !facts.AlignmentLive {
		// A completed operation of this generation is the durable trace of a
		// move: the scan behind these rows ran before it, so the rows name the
		// paths the move replaced.
		if err := db.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM alignment_operations
              WHERE managed_anime_id = $1 AND scan_generation = $2 AND state = 'completed')`,
			animeID, facts.Generation).Scan(&facts.AlignmentMoved); err != nil {
			return facts, fmt.Errorf("read settled alignments: %w", err)
		}
	}
	if facts.Generation > 0 && !facts.AlignmentLive {
		// The recovery gate of this read: the generation the rows describe is
		// still the newest decided generation (no newer calibration has
		// landed) and nothing is in flight. A stale view must not offer a
		// confirm whose own transaction would refuse it as scan_changed.
		if err := db.QueryRowContext(ctx, `
SELECT NOT EXISTS(SELECT 1 FROM reconciliation_files
              WHERE managed_anime_id = $1 AND generation > $2 AND disposition IS NOT NULL)`,
			animeID, facts.Generation).Scan(&facts.RecoveryReady); err != nil {
			return facts, fmt.Errorf("read recovery readiness: %w", err)
		}
	}
	return facts, nil
}

// recoveryScanLive reports whether a scan or its calibration is still in
// flight for the anime: the generation the user reads (and would confirm)
// could re-decide the rows under them. Read-model and confirm share the gate
// so the offered action and the accepted action agree.
func recoveryScanLive(ctx context.Context, q queryer, animeID string) (bool, error) {
	var live bool
	err := q.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM work_items
              WHERE owner_type = 'file_maintenance' AND owner_id = $1
                AND kind IN ('reconciliation_scan', 'reconciliation_calibrate')
                AND state IN ('available', 'leased'))`, animeID).Scan(&live)
	if err != nil {
		return false, fmt.Errorf("read recovery scan liveness: %w", err)
	}
	return live, nil
}
func loadAnimeFilesOwner(ctx context.Context, q queryer, animeID string) (animeFilesOwner, bool, error) {
	var owner animeFilesOwner
	err := q.QueryRowContext(ctx, `
SELECT ma.lifecycle_state, COALESCE((SELECT state FROM subscriptions WHERE managed_anime_id = ma.id), '')
FROM managed_anime ma WHERE ma.id = $1`, animeID).Scan(&owner.Lifecycle, &owner.SubscriptionState)
	if errors.Is(err, sql.ErrNoRows) {
		return owner, false, nil
	}
	if err != nil {
		return owner, false, fmt.Errorf("read anime lifecycle: %w", err)
	}
	return owner, true, nil
}

func loadAnimeFilesScan(ctx context.Context, db *sql.DB, animeID string) (*animeFilesScanOwner, error) {
	var scan animeFilesScanOwner
	var completedAt sql.NullTime
	var failureCode sql.NullString
	err := db.QueryRowContext(ctx, `
SELECT state, generation, completed_at, failure_code FROM reconciliation_scans WHERE managed_anime_id = $1`, animeID).
		Scan(&scan.State, &scan.Generation, &completedAt, &failureCode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read scan owner: %w", err)
	}
	if completedAt.Valid {
		at := completedAt.Time
		scan.CompletedAt = &at
	}
	if failureCode.Valid {
		code := failureCode.String
		scan.FailureCode = &code
	}
	return &scan, nil
}

func liveFileMaintenanceItem(ctx context.Context, db *sql.DB, animeID, kind string) (bool, error) {
	var live bool
	err := db.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM work_items WHERE owner_type = $1 AND owner_id = $2 AND kind = $3 AND state IN ('available', 'leased'))`,
		workitems.OwnerFileMaintenance, animeID, kind).Scan(&live)
	if err != nil {
		return false, fmt.Errorf("read live %s item: %w", kind, err)
	}
	return live, nil
}

// latestCalibratedGeneration is the newest generation whose calibration
// committed, with the time its files were observed.
func latestCalibratedGeneration(ctx context.Context, q queryer, animeID string) (int64, *time.Time, error) {
	var generation int64
	var observedAt sql.NullTime
	err := q.QueryRowContext(ctx, `
SELECT g.generation, (SELECT MAX(COALESCE(rf.observed_at, rf.created_at)) FROM reconciliation_files rf
                      WHERE rf.managed_anime_id = $1 AND rf.generation = g.generation)
FROM (SELECT COALESCE(MAX(generation), 0) AS generation FROM reconciliation_files
      WHERE managed_anime_id = $1 AND disposition IS NOT NULL) g`, animeID).Scan(&generation, &observedAt)
	if err != nil {
		return 0, nil, fmt.Errorf("read latest calibrated generation: %w", err)
	}
	if generation == 0 || !observedAt.Valid {
		return generation, nil, nil
	}
	at := observedAt.Time
	return generation, &at, nil
}

// loadAnimeFilesPlacements reads the placements, their frozen coverage and
// their active companions, then attaches each placement's frozen recovery
// candidate (loadAnimeFilesRecovery). The path-diff owner is threaded through
// because the recovery candidate must be gated against the SAME renderer the
// confirm re-renders with.
func loadAnimeFilesPlacements(ctx context.Context, db *sql.DB, animeID string, pathDiff *PathDiffService) ([]animeFilesPlacement, error) {
	rows, err := db.QueryContext(ctx, `
SELECT p.id, p.physical_path,
       CASE WHEN EXISTS(SELECT 1 FROM releases r JOIN rss_sources src ON src.id = r.rss_source_id
                        JOIN subscriptions sub ON sub.id = src.subscription_id
                        WHERE r.id = p.quality_facts_ref AND sub.managed_anime_id = p.managed_anime_id)
            THEN p.quality_facts_ref ELSE '' END,
       COALESCE(fo.observed_target_size_bytes, fo.expected_size_bytes),
       p.redownload_requested_at IS NOT NULL,
       COALESCE(NULLIF(btrim(src.group_name), ''), ''),
       COALESCE(rel.raw_facts->>'title', '')
FROM placements p
LEFT JOIN file_operations fo ON p.source_evidence_type = 'file_operation' AND fo.id = p.source_owner_ref
LEFT JOIN releases rel ON rel.id = p.quality_facts_ref
LEFT JOIN rss_sources src ON src.id = rel.rss_source_id
WHERE p.managed_anime_id = $1 AND p.state = 'active' AND p.verification_state = 'verified'
ORDER BY p.physical_path, p.id
LIMIT $2`, animeID, animeFilesMaxPlacements)
	if err != nil {
		return nil, fmt.Errorf("read anime placements: %w", err)
	}
	placements := []animeFilesPlacement{}
	index := map[string]int{}
	for rows.Next() {
		var placement animeFilesPlacement
		var size sql.NullInt64
		if err := rows.Scan(&placement.ID, &placement.Path, &placement.ReleaseID, &size, &placement.RedownloadRequested,
			&placement.GroupName, &placement.ReleaseTitle); err != nil {
			rows.Close()
			return nil, err
		}
		if size.Valid {
			value := size.Int64
			placement.ExpectedSize = &value
		}
		index[placement.ID] = len(placements)
		placements = append(placements, placement)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(placements) == 0 {
		return placements, nil
	}
	ids := make([]string, len(placements))
	for i, placement := range placements {
		ids[i] = placement.ID
	}
	coverage, err := db.QueryContext(ctx, `
SELECT placement_id, observation_key FROM placement_coverage
WHERE placement_id = ANY($1) ORDER BY placement_id, coverage_ordinal`, ids)
	if err != nil {
		return nil, fmt.Errorf("read placement coverage: %w", err)
	}
	for coverage.Next() {
		var id, key string
		if err := coverage.Scan(&id, &key); err != nil {
			coverage.Close()
			return nil, err
		}
		if at, ok := index[id]; ok {
			placements[at].Coverage = append(placements[at].Coverage, key)
		}
	}
	if err := coverage.Close(); err != nil {
		return nil, err
	}
	companions, err := db.QueryContext(ctx, `
SELECT placement_id, id, media_kind, current_path, size_bytes FROM placement_companions
WHERE placement_id = ANY($1) AND state = 'active' ORDER BY placement_id, current_path, id`, ids)
	if err != nil {
		return nil, fmt.Errorf("read placement companions: %w", err)
	}
	for companions.Next() {
		var placementID string
		var companion animeFilesCompanion
		if err := companions.Scan(&placementID, &companion.ID, &companion.MediaKind, &companion.Path, &companion.SizeBytes); err != nil {
			companions.Close()
			return nil, err
		}
		if at, ok := index[placementID]; ok {
			placements[at].Companions = append(placements[at].Companions, companion)
		}
	}
	if err := companions.Close(); err != nil {
		return nil, err
	}
	if err := companions.Err(); err != nil {
		return nil, err
	}
	if err := loadAnimeFilesRecovery(ctx, db, animeID, placements, pathDiff); err != nil {
		return nil, err
	}
	return placements, nil
}

// loadAnimeFilesRecovery attaches each placement's frozen recovery candidate
// from the anime's LATEST calibrated generation (side-effect free: it reads
// only the facts the scan wrote). Gate (review fix #2): the read model
// answers only while that generation is still the newest decided one and no
// scan or calibration is live — a dialog captured on an older generation
// can never act (the confirm re-checks the same gate), so it is not even
// offered.
func loadAnimeFilesRecovery(ctx context.Context, db *sql.DB, animeID string, placements []animeFilesPlacement, pathDiff *PathDiffService) error {
	if len(placements) == 0 {
		return nil
	}
	latest, _, err := latestCalibratedGeneration(ctx, db, animeID)
	if err != nil {
		return err
	}
	if live, err := recoveryScanLive(ctx, db, animeID); err != nil {
		return err
	} else if live || latest == 0 {
		return nil
	}
	// The candidate is written by the scan as a JSON ARRAY under the
	// recovery_candidates key of the observation row's facts OBJECT, so the
	// read selects the whole object — the same value readRecoveryCandidateTx
	// decodes in the confirm. Selecting the array instead
	// (rf.facts->'recovery_candidates') and decoding it into the object shape
	// fails on the first candidate any real show carries.
	rows, err := db.QueryContext(ctx, `
SELECT rf.facts, rf.facts->>'placement_id'
FROM reconciliation_files rf
WHERE rf.managed_anime_id = $1 AND rf.generation = $2
  AND rf.disposition = 'missing'
  AND rf.facts->'recovery_candidates' IS NOT NULL`, animeID, latest)
	if err != nil {
		return fmt.Errorf("read recovery candidates: %w", err)
	}
	defer rows.Close()
	type frozenCandidate struct {
		placementID string
		candidate   recoveryCandidateFact
	}
	frozen := []frozenCandidate{}
	seen := map[string]bool{}
	for rows.Next() {
		var raw []byte
		var placementID string
		if err := rows.Scan(&raw, &placementID); err != nil {
			return err
		}
		if seen[placementID] {
			continue
		}
		document, err := decodeRecoveryCandidatesDocument(raw)
		if err != nil {
			return err
		}
		for i := len(document.RecoveryCandidates) - 1; i >= 0; i-- {
			candidate := document.RecoveryCandidates[i]
			if candidate.PlacementID != placementID {
				continue
			}
			if _, ok := indexForPlacement(placements, placementID); !ok {
				break
			}
			seen[placementID] = true
			frozen = append(frozen, frozenCandidate{placementID: placementID, candidate: candidate})
			break
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(frozen) == 0 {
		return nil
	}
	// Render the CURRENT unit once, through the renderer the confirm
	// re-renders with, so the offered action and the accepted action can never
	// be produced by two different rule sets. A render failure is not an
	// answer about the unit: the candidate keeps its frozen target and the
	// liveness gate compares against an absent render (fail closed).
	renderer := (*NamingTargetRenderer)(nil)
	if pathDiff != nil {
		renderer = pathDiff.renderer
	}
	var current map[string]string
	if renderer != nil {
		ids := make([]string, len(frozen))
		for i := range frozen {
			ids[i] = frozen[i].placementID
		}
		rendered, renderErr := renderer.RenderPlacementTargets(ctx, animeID, ids)
		if renderErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			current = nil
		} else {
			current = rendered
		}
	}
	for _, entry := range frozen {
		at, ok := indexForPlacement(placements, entry.placementID)
		if !ok {
			continue
		}
		candidate := entry.candidate
		placements[at].Recovery = &animeFilesRecoveryCandidate{
			PlacementID:      candidate.PlacementID,
			TargetPath:       candidate.TargetPath,
			Generation:       candidate.Generation,
			SizeBytes:        candidate.SizeBytes,
			Companions:       len(candidate.Companions),
			CompanionTargets: recoveryCompanionTargets(candidate.Companions),
			PlacementVersion: candidate.PlacementVersion,
			Fingerprint:      candidate.TargetFingerprint,
			currentTargets:   current,
		}
	}
	return nil
}

func indexForPlacement(placements []animeFilesPlacement, id string) (int, bool) {
	for i := range placements {
		if placements[i].ID == id {
			return i, true
		}
	}
	return 0, false
}

func loadAnimeFilesAlignments(ctx context.Context, db *sql.DB, animeID string, generation int64) ([]animeFilesAlignmentOp, error) {
	rows, err := db.QueryContext(ctx, `
SELECT observation_ref, COALESCE(companion_id, ''), state, COALESCE(failure_code, '')
FROM alignment_operations WHERE managed_anime_id = $1 AND scan_generation = $2
ORDER BY created_at, id`, animeID, generation)
	if err != nil {
		return nil, fmt.Errorf("read alignment operations: %w", err)
	}
	defer rows.Close()
	ops := []animeFilesAlignmentOp{}
	for rows.Next() {
		var op animeFilesAlignmentOp
		if err := rows.Scan(&op.ObservationRef, &op.CompanionID, &op.State, &op.FailureCode); err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}

// ---- derivation (pure) --------------------------------------------------

// manualActionReason is why the anime admits no manual file command now ("" =
// it does): a retired anime admits nothing, a pending subscription nothing
// until its first start.
func (o animeFilesOwner) manualActionReason() string {
	if o.Lifecycle != "active" || o.SubscriptionState == "" {
		return AnimeFilesReasonOwnerRetired
	}
	if o.SubscriptionState == "pending" {
		return AnimeFilesReasonNotStarted
	}
	return ""
}

func (f animeFilesFacts) scanState() AnimeFilesScanView {
	view := AnimeFilesScanView{State: "idle", Generation: f.Generation, ScannedAt: f.GenerationAt, RescanRequired: f.AlignmentMoved}
	if f.Scan == nil {
		return view
	}
	switch {
	case f.Scan.State == "running" || f.CalibratePending:
		view.State = "running"
	case f.Scan.State == "failed":
		view.State = "failed"
		view.FailureCode = f.Scan.FailureCode
	}
	if f.Scan.Generation == f.Generation && f.Scan.CompletedAt != nil {
		view.ScannedAt = f.Scan.CompletedAt
	}
	return view
}

func availability(reason string) AnimeFileAction {
	if reason == "" {
		return AnimeFileAction{Available: true}
	}
	return AnimeFileAction{ReasonCode: reason}
}

func alignmentView(op *animeFilesAlignmentOp) *AnimeFileAlignmentView {
	if op == nil {
		return nil
	}
	switch op.State {
	case string(AlignmentPlanned), string(AlignmentReady), string(AlignmentRunning):
		return &AnimeFileAlignmentView{State: animeFilesAlignmentStateInProgress}
	case string(AlignmentBlocked), string(AlignmentFailed), string(AlignmentCanceled):
		return &AnimeFileAlignmentView{State: animeFilesAlignmentStateFailed, ReasonCode: op.FailureCode}
	}
	return nil
}

func deriveAnimeFiles(f animeFilesFacts) AnimeFilesView {
	view := AnimeFilesView{ManagedAnimeID: f.AnimeID, Scan: f.scanState(), Aligning: f.AlignmentLive, Files: []AnimeFileView{}}
	manualReason := f.Owner.manualActionReason()
	view.ScanAction = availability(manualReason)
	scanRunning := view.Scan.State == "running"

	diffs := make(map[string]PathDiffView, len(f.Diffs))
	for _, diff := range f.Diffs {
		if diff.PlacementID != "" {
			diffs[diff.PlacementID] = diff
		}
	}
	videoOps := map[string]*animeFilesAlignmentOp{}
	companionOps := map[string]*animeFilesAlignmentOp{}
	for i := range f.Alignments {
		op := &f.Alignments[i]
		if op.CompanionID != "" {
			companionOps[op.CompanionID] = op
		} else {
			videoOps[op.ObservationRef] = op
		}
	}

	paths := []string{}
	anyAlignable := false
	for _, placement := range f.Placements {
		row := AnimeFileView{
			PlacementID:  placement.ID,
			ReleaseID:    placement.ReleaseID,
			GroupName:    placement.GroupName,
			ReleaseTitle: placement.ReleaseTitle,
			Path:         placement.Path,
			SizeBytes:    placement.ExpectedSize,
			Status:       AnimeFileUnscanned,
		}
		row.MediaKind, row.Episodes = coverageEpisodes(placement.Coverage)
		paths = append(paths, placement.Path)
		diff, observed := diffs[placement.ID]
		var videoOp *animeFilesAlignmentOp
		if observed {
			videoOp = videoOps[fmt.Sprintf("%s:%d:%s", f.AnimeID, f.Generation, diff.FilePath)]
			if diff.SizeBytes > 0 && diff.Disposition != DispositionMissing {
				size := diff.SizeBytes
				row.SizeBytes = &size
			}
			row.Status, row.StatusReason, row.TargetPath = videoStatus(placement, diff)
			if row.TargetPath != nil {
				paths = append(paths, *row.TargetPath)
			}
			if row.Status != AnimeFileAligned {
				row.Alignment = alignmentView(videoOp)
			}
		}
		companionMisaligned := false
		var companionDiffs map[string]CompanionPathDiffView
		if observed {
			companionDiffs = make(map[string]CompanionPathDiffView, len(diff.Companions))
			for _, companion := range diff.Companions {
				key := companion.ID
				if key == "" {
					key = "path:" + filepath.Clean(companion.FilePath)
				}
				companionDiffs[key] = companion
			}
		}
		for _, companion := range placement.Companions {
			companionRow := AnimeFileCompanionView{
				ID: companion.ID, MediaKind: companion.MediaKind, Path: companion.Path,
				SizeBytes: companion.SizeBytes, Status: AnimeFileUnscanned,
			}
			paths = append(paths, companion.Path)
			if observed && row.Status != AnimeFileUnscanned {
				observedCompanion, ok := companionDiffs[companion.ID]
				if !ok {
					observedCompanion, ok = companionDiffs["path:"+filepath.Clean(companion.Path)]
				}
				if ok {
					companionRow.Status, companionRow.TargetPath = companionStatus(companion, observedCompanion)
					if companionRow.TargetPath != nil {
						paths = append(paths, *companionRow.TargetPath)
					}
					if companionRow.Status == AnimeFileMisaligned {
						companionMisaligned = true
						companionRow.Alignment = alignmentView(companionOps[companion.ID])
					}
				}
			}
			row.Companions = append(row.Companions, companionRow)
		}

		// Align: the file or one of its companions is not where the
		// alignment chain renders it.
		alignReason := ""
		switch {
		case row.Status != AnimeFileMisaligned && !companionMisaligned:
			alignReason = AnimeFilesReasonNothingToAlign
		case manualReason != "":
			alignReason = manualReason
		case scanRunning:
			alignReason = AnimeFilesReasonScanRunning
		case rowAlignmentRunning(row):
			alignReason = AnimeFilesReasonAlignmentRunning
		case rowAlignmentSettled(row, videoOp, companionOps):
			// The latest scan's operation already ended: aligning again needs
			// a fresh scan.
			alignReason = AnimeFilesReasonRescanRequired
		}
		row.Actions.Align = availability(alignReason)
		if row.Actions.Align.Available {
			anyAlignable = true
		}
		// Re-run and release from management: only for a file the latest scan
		// found missing or with other content.
		exitReason := ""
		if row.Status != AnimeFileMissing && row.Status != AnimeFileChanged {
			exitReason = AnimeFilesReasonFileNotMissing
		}
		rerunReason := exitReason
		if rerunReason == "" {
			switch {
			case manualReason != "":
				rerunReason = manualReason
			case placement.ReleaseID == "":
				rerunReason = AnimeFilesReasonSourceUnavailable
			}
		}
		row.Actions.Rerun = availability(rerunReason)
		releaseReason := exitReason
		if releaseReason == "" && f.Owner.Lifecycle != "active" {
			releaseReason = AnimeFilesReasonOwnerRetired
		}
		row.Actions.Release = availability(releaseReason)

		// Recover: the scan froze a candidate for this row (its target was
		// missing and the COMPLETE rendered unit was free of other owners).
		// Available only while the read is current and nothing is in flight;
		// the confirm's own transaction re-verifies every frozen fact again
		// (unit is atomic: video + companions or nothing).
		if placement.Recovery != nil {
			candidate := placement.Recovery
			row.Recovery = &AnimeFileRecoveryView{
				Generation:       candidate.Generation,
				TargetPath:       candidate.TargetPath,
				SizeBytes:        candidate.SizeBytes,
				PlacementVersion: candidate.PlacementVersion,
				Fingerprint:      candidate.Fingerprint,
				Companions:       candidate.Companions,
			}
			recoverReason := ""
			// Live gate: the candidate is only offered while the CURRENT
			// render of this placement and of every frozen companion still
			// equals the frozen target. A naming-rule change, an
			// unrenderable placement, or a companion membership change
			// since the scan means the frozen unit is not what the confirm
			// would produce: the user must rescan instead.
			liveReason := recoveryLiveReason(candidate, placement)
			switch {
			case row.Status != AnimeFileMissing:
				recoverReason = AnimeFilesReasonRecoveryGate
			case manualReason != "":
				recoverReason = manualReason
			case scanRunning:
				recoverReason = AnimeFilesReasonScanRunning
			case f.AlignmentLive:
				recoverReason = AnimeFilesReasonAlignmentRunning
			case liveReason != "":
				recoverReason = liveReason
			case !f.RecoveryReady:
				recoverReason = AnimeFilesReasonScanChanged
			}
			row.Actions.Recover = availability(recoverReason)
		} else {
			row.Actions.Recover = AnimeFileAction{Available: false, ReasonCode: "nothing_to_recover"}
		}
		view.Files = append(view.Files, row)
	}

	alignAllReason := ""
	switch {
	case anyAlignable:
	case manualReason != "":
		alignAllReason = manualReason
	case scanRunning:
		alignAllReason = AnimeFilesReasonScanRunning
	case f.Generation == 0:
		alignAllReason = AnimeFilesReasonScanRequired
	default:
		alignAllReason = AnimeFilesReasonNothingToAlign
	}
	view.AlignAllAction = availability(alignAllReason)

	view.Root = displaypath.Root(paths)
	for i := range view.Files {
		row := &view.Files[i]
		row.RelativePath = relativeTo(view.Root, row.Path)
		row.TargetRelativePath = relativePointer(view.Root, row.TargetPath)
		if row.Recovery != nil {
			// The scan found the file at this target: show it library-relative
			// rather than composing an absolute path in the client.
			row.Recovery.TargetRelativePath = relativeTo(view.Root, row.Recovery.TargetPath)
		}
		for j := range row.Companions {
			companion := &row.Companions[j]
			companion.RelativePath = relativeTo(view.Root, companion.Path)
			companion.TargetRelativePath = relativePointer(view.Root, companion.TargetPath)
		}
	}
	return view
}

// recoveryLiveReason decides whether the frozen candidate is still what the
// CURRENT naming rules would render for this placement's whole unit. It
// returns an empty string when the frozen candidate is live and the reason
// code to show otherwise.
//
// The read model gates here because the confirm re-renders and refuses a
// changed unit: offering a button whose own transaction must refuse it would
// be a lie.
//
// The evidence source is the placement's OWN frozen coverage rendered through
// the renderer instance the confirm uses (RenderPlacementTargets), NEVER the
// calibration row's ExpectedPath / TargetUnavailable. A real missing row has
// an empty calibration coverage, so episodeFromCoverage yields "" and
// path_diff reports coverage_unresolved with a nil ExpectedPath: gating on
// those refuses every real candidate forever (the defect this replaces). The
// two evidence sources are structurally different — the confirm already reads
// placement-frozen coverage — so the read model follows the confirm.
//
// Only a render that cannot be produced fails closed: an absent entry means
// the unit is not the frozen one and requires a rescan. A companion
// membership change is the same answer.
func recoveryLiveReason(candidate *animeFilesRecoveryCandidate, placement animeFilesPlacement) string {
	current := candidate.currentTargets
	video, ok := current[candidate.PlacementID]
	if !ok || !sameCleanPath(video, candidate.TargetPath) {
		return AnimeFilesReasonRescanRequired
	}
	// Companion membership: the frozen count must still match the placement's
	// active unit.
	if len(placement.Companions) != candidate.Companions {
		return AnimeFilesReasonRescanRequired
	}
	// A candidate that reports companions but carries no frozen targets cannot
	// be verified: fail closed rather than offer an unverifiable unit.
	if len(candidate.CompanionTargets) != candidate.Companions {
		return AnimeFilesReasonRescanRequired
	}
	// EVERY frozen companion must still render to the exact path the scan
	// froze. Comparing only the count let the read model offer a confirm for a
	// unit whose companion now renders elsewhere — a button whose own
	// transaction must refuse with companion_missing.
	for _, frozen := range candidate.CompanionTargets {
		path, ok := current[frozen.ID]
		if !ok || !sameCleanPath(path, frozen.TargetPath) {
			return AnimeFilesReasonRescanRequired
		}
	}
	return ""
}

// recoveryCompanionTargets projects the frozen companion facts onto the read
// model's comparable shape.
func recoveryCompanionTargets(companions []recoveryCompanionFact) []animeFilesRecoveryCompanionTarget {
	if len(companions) == 0 {
		return nil
	}
	targets := make([]animeFilesRecoveryCompanionTarget, 0, len(companions))
	for _, companion := range companions {
		targets = append(targets, animeFilesRecoveryCompanionTarget{
			ID:         companion.ID,
			TargetPath: companion.TargetPath,
		})
	}
	return targets
}

// videoStatus maps the latest calibration row of a placement to its status.
// A placement whose current path is no longer the observed one moved after
// the scan: it is aligned when it now sits at the rendered target (the
// alignment chain moved it), otherwise unknown until the next scan.
func videoStatus(placement animeFilesPlacement, diff PathDiffView) (string, string, *string) {
	switch diff.Disposition {
	case DispositionMissing:
		return AnimeFileMissing, "", nil
	case DispositionFingerprintConflict:
		return AnimeFileChanged, "", nil
	}
	if !samePath(placement.Path, diff.FilePath) {
		// The observation's path no longer resolves to the placement, so the
		// projection may have no target for it; that is not a missing target.
		if diff.ExpectedPath != nil && samePath(placement.Path, filepath.Clean(*diff.ExpectedPath)) {
			return AnimeFileAligned, "", nil
		}
		return AnimeFileUnscanned, "", nil
	}
	if diff.TargetUnavailable != "" || diff.ExpectedPath == nil {
		reason := diff.TargetUnavailable
		if reason == "" {
			reason = "coverage_unresolved"
		}
		return AnimeFileNoTarget, reason, nil
	}
	target := filepath.Clean(*diff.ExpectedPath)
	if diff.Differs {
		return AnimeFileMisaligned, "", &target
	}
	return AnimeFileAligned, "", nil
}

func companionStatus(companion animeFilesCompanion, diff CompanionPathDiffView) (string, *string) {
	switch diff.State {
	case "missing":
		return AnimeFileMissing, nil
	case "size_mismatch":
		return AnimeFileSizeMismatch, nil
	}
	if diff.ExpectedPath == nil {
		return AnimeFileAligned, nil
	}
	target := filepath.Clean(*diff.ExpectedPath)
	if !samePath(companion.Path, diff.FilePath) {
		if samePath(companion.Path, target) {
			return AnimeFileAligned, nil
		}
		return AnimeFileUnscanned, nil
	}
	if diff.Differs {
		return AnimeFileMisaligned, &target
	}
	return AnimeFileAligned, nil
}

func rowAlignmentRunning(row AnimeFileView) bool {
	if row.Alignment != nil && row.Alignment.State == animeFilesAlignmentStateInProgress {
		return true
	}
	for _, companion := range row.Companions {
		if companion.Alignment != nil && companion.Alignment.State == animeFilesAlignmentStateInProgress {
			return true
		}
	}
	return false
}

// rowAlignmentSettled reports whether every misaligned part of the row already
// has a terminal operation for the latest scan (a re-admission of the same
// scan would admit nothing).
func rowAlignmentSettled(row AnimeFileView, videoOp *animeFilesAlignmentOp, companionOps map[string]*animeFilesAlignmentOp) bool {
	terminal := func(op *animeFilesAlignmentOp) bool {
		return op != nil && op.State != string(AlignmentPlanned) && op.State != string(AlignmentReady) && op.State != string(AlignmentRunning)
	}
	if row.Status == AnimeFileMisaligned && !terminal(videoOp) {
		return false
	}
	for _, companion := range row.Companions {
		if companion.Status == AnimeFileMisaligned && !terminal(companionOps[companion.ID]) {
			return false
		}
	}
	return true
}

// coverageEpisodes reads the frozen coverage keys of a placement: tv keys
// (tv:e<N>, or the v1 tv:s<S>:e<N>) give the episodes, movie:0 a movie.
func coverageEpisodes(coverage []string) (string, []string) {
	episodes := []string{}
	kind := "unresolved"
	for _, key := range coverage {
		if strings.HasPrefix(key, "movie:") {
			kind = "movie"
			continue
		}
		match := coverageEpisodePattern.FindStringSubmatch(key)
		if match == nil {
			continue
		}
		kind = "tv"
		episodes = append(episodes, match[1])
	}
	return kind, episodes
}

func relativeTo(root, path string) string {
	return displaypath.Relative(root, path)
}

func relativePointer(root string, path *string) *string {
	if path == nil {
		return nil
	}
	rel := relativeTo(root, *path)
	return &rel
}

// ---- one-off alignment admission ------------------------------------------

// AdmitOneOffAlignment admits the alignment of this anime's files (all of
// them, or the given placements) from the latest calibrated scan, the
// generation the caller saw. It is the explicit one-off form of the alignment
// consumer: it is not gated by the automatic alignment toggle, and otherwise
// runs the same chain — the same planner and renderer, the same per-observation
// operation keys (so it converges with the automatic consumer), the unit
// collision policy, the fenced alignment_execute claims with their receipts
// and recovery. Only files the scan found present are moved; a missing or
// changed file has its own exits.
func (s *CalibrationService) AdmitOneOffAlignment(ctx context.Context, animeID string, generation int64, placementIDs []string) (AnimeFilesAlignResult, error) {
	if s == nil || s.db == nil || s.planner == nil || s.policies == nil || s.alignment == nil || s.workItems == nil {
		return AnimeFilesAlignResult{}, errors.New("one-off alignment is not wired")
	}
	owner, found, err := loadAnimeFilesOwner(ctx, s.db, animeID)
	if err != nil {
		return AnimeFilesAlignResult{}, err
	}
	if !found {
		return AnimeFilesAlignResult{}, &apperrors.NotFound{Resource: "managed_anime", ID: animeID}
	}
	if reason := owner.manualActionReason(); reason != "" {
		return AnimeFilesAlignResult{}, &AnimeFilesAlignError{Reason: reason}
	}
	scan, err := loadAnimeFilesScan(ctx, s.db, animeID)
	if err != nil {
		return AnimeFilesAlignResult{}, err
	}
	calibrating, err := liveFileMaintenanceItem(ctx, s.db, animeID, workitems.KindReconciliationCalibrate)
	if err != nil {
		return AnimeFilesAlignResult{}, err
	}
	latest, _, err := latestCalibratedGeneration(ctx, s.db, animeID)
	if err != nil {
		return AnimeFilesAlignResult{}, err
	}
	if decision := oneOffAlignmentAdmission(scan, calibrating, latest, generation); decision != "" {
		return AnimeFilesAlignResult{}, &AnimeFilesAlignError{Reason: decision}
	}
	present, err := presentObservationPaths(ctx, s.db, animeID, latest, placementIDs)
	if err != nil {
		return AnimeFilesAlignResult{}, err
	}
	result := AnimeFilesAlignResult{Generation: latest}
	if len(present) == 0 {
		return result, nil
	}
	effective, err := s.policies.GetEffective(ctx, animeID)
	if err != nil {
		return AnimeFilesAlignResult{}, err
	}
	// The policy version is provenance on the operation. An anime whose file
	// maintenance policy was never saved runs on the built-in defaults
	// (version 0, which the automatic consumer never reaches because those
	// defaults keep alignment off); the operation records the first version.
	policyVersion := effective.PolicyVersion
	if policyVersion < 1 {
		policyVersion = 1
	}
	plan, err := s.planner.Plan(ctx, animeID, latest, policyVersion)
	if err != nil {
		return AnimeFilesAlignResult{}, err
	}
	admitted, err := s.admitAlignmentOperationsFor(ctx, animeID, latest, plan, func(candidate planner.PlanCandidate) bool {
		_, ok := present[filepath.Clean(candidate.SourcePath)]
		return ok
	})
	if err != nil {
		return AnimeFilesAlignResult{}, err
	}
	result.Admitted = admitted
	return result, nil
}

// oneOffAlignmentAdmission decides whether a one-off alignment of the given
// generation may be admitted now ("" = yes).
func oneOffAlignmentAdmission(scan *animeFilesScanOwner, calibrating bool, latest, requested int64) string {
	if scan != nil && scan.State == "running" || calibrating {
		return AnimeFilesReasonScanRunning
	}
	if latest == 0 {
		return AnimeFilesReasonScanRequired
	}
	if requested != latest || (scan != nil && scan.State == "completed" && scan.Generation != latest) {
		return AnimeFilesReasonScanChanged
	}
	return ""
}

// presentObservationPaths are the observed paths of the generation's active
// placements the scan found present, restricted to the given placements
// (none = all).
func presentObservationPaths(ctx context.Context, db *sql.DB, animeID string, generation int64, placementIDs []string) (map[string]struct{}, error) {
	selected := make([]string, 0, len(placementIDs))
	for _, id := range placementIDs {
		if id = strings.TrimSpace(id); id != "" {
			selected = append(selected, id)
		}
	}
	sort.Strings(selected)
	rows, err := db.QueryContext(ctx, `
SELECT rf.file_path FROM reconciliation_files rf
JOIN placements p ON p.id = rf.facts->>'placement_id' AND p.managed_anime_id = rf.managed_anime_id
WHERE rf.managed_anime_id = $1 AND rf.generation = $2 AND rf.disposition = 'present'
  AND p.state = 'active' AND p.verification_state = 'verified'
  AND (cardinality($3::text[]) = 0 OR p.id = ANY($3::text[]))`, animeID, generation, selected)
	if err != nil {
		return nil, fmt.Errorf("read present observations: %w", err)
	}
	defer rows.Close()
	paths := map[string]struct{}{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths[filepath.Clean(path)] = struct{}{}
	}
	return paths, rows.Err()
}
