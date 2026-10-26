package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/events"
	"bansoko/internal/library/domain"
	"bansoko/internal/workitems"
	"bansoko/internal/worktree"
)

// FileScanner is the read-only filesystem scan port. The concrete adapter is
// library/adapter/filesystem.OSFileScanner.
type FileScanner interface {
	Scan(ctx context.Context, request domain.ScanRequest) (domain.ScanResult, error)
}

// Service is the reconciliation snapshot provider: it owns the durable scan
// owner rows and immutable observation generations, and exposes only read
// queries plus scan submission. It never produces filesystem effects (the
// old synchronous Apply is gone; future FileMaintenance owners consume
// completed generations through GenerationUsable).
type Service struct {
	db        *sql.DB
	scanner   FileScanner
	targets   TargetObserver
	workItems *workitems.Store
	eventHub  *events.Hub
	now       func() time.Time
}

// TargetObserver refreshes the latest TargetObservation rows of one instance
// (stat of its frozen target paths, no filesystem effect). The scan run calls
// it once per generation so the RSS row status and the reconciliation results
// come from the same run; a missing or retired instance is a no-op.
type TargetObserver interface {
	RefreshObservations(ctx context.Context, managedAnimeID string) error
}

// NewService wires the reconciliation owner. Only the scanner and work item
// store are required: the provider observes, it does not mutate files.
func NewService(db *sql.DB, workItems *workitems.Store, scanner FileScanner, clocks ...func() time.Time) (*Service, error) {
	if db == nil {
		return nil, errors.New("reconciliation service database is nil")
	}
	if scanner == nil {
		return nil, errors.New("reconciliation service scanner is nil")
	}
	if workItems == nil {
		return nil, errors.New("reconciliation service work item store is nil")
	}
	now := time.Now
	if len(clocks) > 0 && clocks[0] != nil {
		now = clocks[0]
	}
	return &Service{db: db, scanner: scanner, workItems: workItems, now: now}, nil
}

// SetTargetObserver wires the target pass of every scan run.
func (s *Service) SetTargetObserver(observer TargetObserver) {
	if s != nil {
		s.targets = observer
	}
}

// SetEventHub wires the optional in-process read-model invalidation boundary.
func (s *Service) SetEventHub(hub *events.Hub) {
	if s != nil {
		s.eventHub = hub
	}
}

// SubmitScan starts (or idempotently replays) an asynchronous scan for one
// anime. Same input fingerprint with a completed previous generation returns
// the existing generation; a running scan is a typed conflict. An omitted
// tree_top reuses the last scan scope's tree top (the fingerprint is computed
// over the effective tree top).
func (s *Service) SubmitScan(ctx context.Context, input ScanSubmitInput) (ScanView, error) {
	input = cloneScanSubmitInput(input)
	animeID := input.ManagedAnimeID
	if err := s.enforceLiveInstanceGate(ctx, animeID, input.Trigger); err != nil {
		return ScanView{}, err
	}
	treeTop := ""
	if input.TreeTop != nil {
		treeTop = *input.TreeTop
	}
	if treeTop == "" {
		// A caller that does not know where this work's files are (the UI
		// never does) reuses the last scan scope of this instance. A work
		// still has to name a scope before it can be scanned: the scope bounds
		// what a generation may decide about, so it is never invented.
		owner, err := loadScanOwner(ctx, s.db, animeID)
		if err != nil {
			return ScanView{}, err
		}
		if owner != nil {
			var scope ScanTreeTop
			if err := json.Unmarshal(owner.ScanScope, &scope); err == nil {
				treeTop = scope.TreeTop
			}
		}
		if treeTop == "" {
			// No stored scope yet (the instance was never scanned): the scope
			// is the work's own tree top, derived from the files its active
			// placements put on disk, exactly as the periodic scheduler derives
			// it. A work with no placement evidence has no scope.
			top, ok, err := s.placementTreeTop(ctx, animeID)
			if err != nil {
				return ScanView{}, err
			}
			if !ok {
				return ScanView{}, &apperrors.BadRequest{Code: "SCAN_ROOT_MISSING", Message: "tree_top is required for the first scan of an instance without placed files"}
			}
			treeTop = top
		}
		root := treeTop
		input.TreeTop = &root
	}
	scope, err := json.Marshal(ScanTreeTop{TreeTop: treeTop})
	if err != nil {
		return ScanView{}, err
	}
	fingerprint := fingerprintFor(input)

	// Owner admission and the matching WorkItem must share one transaction. A
	// failed enqueue must not leave a running generation with no worker claim.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ScanView{}, err
	}
	defer tx.Rollback()
	owner, created, err := startScanOwnerTx(ctx, tx, s.now().UTC(), animeID, scope, fingerprint)
	if err != nil {
		if errors.Is(err, ErrScanInProgress) {
			return ScanView{}, scanInProgressError()
		}
		return ScanView{}, err
	}
	if !created && owner.State == ScanCompleted {
		return s.viewOf(owner), nil
	}

	// Create the reconciliation_scan work item with a stable idempotency key in
	// the same caller-owned transaction as the owner start/update.
	if _, err := s.workItems.Create(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: animeID},
		Kind:                 workitems.KindReconciliationScan,
		IdempotencyKey:       "recon:scan:" + animeID + ":" + fmt.Sprintf("%d", owner.Generation),
		InputSnapshot:        scope,
		MaxAttempts:          3,
		ExpectedOwnerVersion: owner.Version,
	}); err != nil {
		return ScanView{}, fmt.Errorf("create reconciliation scan work item: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ScanView{}, err
	}
	return s.viewOf(owner), nil
}

// placementTreeTop is the work's tree top: the deepest directory holding the
// files its own active placements put on disk. ok=false means no evidence.
func (s *Service) placementTreeTop(ctx context.Context, animeID string) (string, bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT physical_path FROM placements
WHERE managed_anime_id = $1 AND state = 'active' ORDER BY physical_path LIMIT 1000`, animeID)
	if err != nil {
		return "", false, fmt.Errorf("read scan scope placements: %w", err)
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return "", false, fmt.Errorf("scan scope placement: %w", err)
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return "", false, fmt.Errorf("read scan scope placements: %w", err)
	}
	top, ok := worktree.Top(paths)
	return top, ok, nil
}

// enforceLiveInstanceGate validates the instance exists and, for a periodic
// trigger, that it is active. A deleted/unsubscribed ID fails closed so no scan
// owner, observation or policy can be (re)created for it. A stopped instance may
// still run an explicit manual read-only scan/preview; periodic scheduling is
// active-only (the scheduler additionally filters before submitting).
func (s *Service) enforceLiveInstanceGate(ctx context.Context, animeID string, trigger *ScanSubmitTrigger) error {
	var state string
	err := s.db.QueryRowContext(ctx, `
SELECT sub.state
FROM managed_anime a
JOIN subscriptions sub ON sub.managed_anime_id = a.id
WHERE a.id = $1`, animeID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return &apperrors.NotFound{Resource: "managed_anime", ID: animeID}
	}
	if err != nil {
		return fmt.Errorf("read instance subscription state: %w", err)
	}
	// A pending (never started) instance does no work, manual scans included.
	if state == "pending" {
		return &apperrors.Business{Status: 409, Code: "SUBSCRIPTION_NOT_STARTED", Message: "The subscription has not been started."}
	}
	if trigger != nil && *trigger == ScanSubmitTriggerPeriodic && state != "active" {
		return &apperrors.Business{
			Status:  409,
			Code:    "SUBSCRIPTION_STOPPED",
			Message: "periodic reconciliation requires an active subscription; a stopped instance allows explicit manual read-only scans only",
		}
	}
	return nil
}

// GetScan returns the current owner view for one anime.
func (s *Service) GetScan(ctx context.Context, animeID string) (ScanView, error) {
	owner, err := loadScanOwner(ctx, s.db, animeID)
	if err != nil {
		return ScanView{}, err
	}
	if owner == nil {
		return ScanView{}, &apperrors.NotFound{Resource: "reconciliation_scan", ID: animeID}
	}
	return s.viewOf(owner), nil
}

// ListObservations pages the immutable observations of one generation.
func (s *Service) ListObservations(ctx context.Context, animeID string, generation int64, limit, offset int) (ObservationPage, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT file_path, fingerprint, size_bytes, facts, observed_at
FROM reconciliation_files
WHERE managed_anime_id = $1 AND generation = $2 AND NOT derived_missing
ORDER BY batch_index, file_path
LIMIT $3 OFFSET $4`, animeID, generation, limit, offset)
	if err != nil {
		return ObservationPage{}, err
	}
	defer rows.Close()
	items := make([]Observation, 0, limit)
	for rows.Next() {
		var (
			item     Observation
			path     string
			fp       string
			size     int64
			factsRaw []byte
			at       time.Time
		)
		if err := rows.Scan(&path, &fp, &size, &factsRaw, &at); err != nil {
			return ObservationPage{}, err
		}
		if len(factsRaw) > 0 && string(factsRaw) != "null" {
			if err := json.Unmarshal(factsRaw, &item.Facts); err != nil {
				return ObservationPage{}, fmt.Errorf("decode reconciliation observation facts: %w", err)
			}
		}
		item.FilePath = path
		item.Fingerprint = fp
		item.SizeBytes = size
		item.ObservedAt = at
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ObservationPage{}, err
	}
	return ObservationPage{Items: items}, nil
}

// GenerationUsable is the downstream admission guard: only a completed scan
// generation may drive replan/alignment/replacement/cleanup effects.
func (s *Service) GenerationUsable(ctx context.Context, animeID string, generation int64) (bool, error) {
	owner, err := loadScanOwner(ctx, s.db, animeID)
	if err != nil {
		return false, err
	}
	if owner == nil {
		return false, nil
	}
	return owner.State == ScanCompleted && owner.Generation == generation, nil
}

func (s *Service) viewOf(owner *ScanOwner) ScanView {
	var progress map[string]any
	_ = json.Unmarshal(owner.Progress, &progress)
	var stats map[string]any
	_ = json.Unmarshal(owner.CapacityStats, &stats)
	return ScanView{
		ManagedAnimeID:   owner.ManagedAnimeID,
		State:            owner.State,
		Generation:       owner.Generation,
		InputFingerprint: owner.InputFingerprint,
		Progress:         progress,
		CapacityStats:    stats,
		FailureCode:      nullableString(owner.FailureCode),
		FailureReason:    nullableString(owner.FailureReason),
		Version:          owner.Version,
		StartedAt:        cloneTime(owner.StartedAt),
		CompletedAt:      cloneTime(owner.CompletedAt),
	}
}

func fingerprintFor(input ScanSubmitInput) string {
	raw, _ := json.Marshal(input)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// scanInProgressError is the typed conflict for a submit that lands while the
// instance's generation is still running: the contract answers 409
// SCAN_IN_PROGRESS, and the transport maps apperrors.Business without a
// per-condition special case.
func scanInProgressError() error {
	return &apperrors.Business{
		Status:  409,
		Code:    "SCAN_IN_PROGRESS",
		Message: "A scan is already running for this instance. Try again after it finishes.",
	}
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
