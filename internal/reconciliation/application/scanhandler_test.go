package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/library/domain"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

type fakeScanner struct {
	mu         sync.Mutex
	files      []domain.ScanFile
	calls      int
	cancelErr  error
	ctxChecked bool
}

func (f *fakeScanner) Scan(ctx context.Context, _ domain.ScanRequest) (domain.ScanResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.ctxChecked = ctx != nil
	if err := ctx.Err(); err != nil {
		return domain.ScanResult{}, err
	}
	return domain.ScanResult{TreeTop: "/library", ScannerVersion: "test", Files: f.files}, nil
}

func openReconTestDB(t *testing.T) (*sql.DB, *workitems.Store) {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	return db, store
}

func stringPtr(value string) *string {
	return &value
}

func testFiles(n int) []domain.ScanFile {
	files := make([]domain.ScanFile, 0, n)
	for i := 0; i < n; i++ {
		files = append(files, domain.ScanFile{
			Path:        fmt.Sprintf("/library/Show/Season 01/EP %03d.mkv", i),
			Fingerprint: fmt.Sprintf("fp-%03d", i),
			SizeBytes:   int64(100 + i),
			ObservedAt:  time.Now().UTC(),
		})
	}
	return files
}

func testFilePaths(files []domain.ScanFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

// seedReconInstance creates a live instance (anime + subscription) with the
// given verified active Placements. Placements carry the same path/fingerprint
// scheme as testFiles so a fake scanner's files match present observations.
func seedReconInstance(t *testing.T, db *sql.DB, animeID, subID, state string, files []domain.ScanFile) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO managed_anime(id, title) VALUES ($1, 'Rec Anime')`, animeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state, version)
VALUES ($1, $2, 'custom', 'Rec', $3, 1)`, subID, animeID, state); err != nil {
		t.Fatal(err)
	}
	for i, file := range files {
		if _, err := db.Exec(`
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, facts_version, version, created_at, updated_at)
VALUES ($1, $2, $3, 'reconciliation', $4, $5, $5, 'parser-v1', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			fmt.Sprintf("plc-%s-%03d", animeID, i), file.Path, animeID,
			fmt.Sprintf("obs-%s-%03d", animeID, i), file.Fingerprint); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSubmitScanCreatesOwnerAndWorkItem(t *testing.T) {
	db, store := openReconTestDB(t)
	defer db.Close()
	files := testFiles(3)
	seedReconInstance(t, db, "anime-rec-1", "sub-rec-1", "active", files)
	service, err := NewService(db, store, &fakeScanner{files: files})
	if err != nil {
		t.Fatal(err)
	}
	view, err := service.SubmitScan(context.Background(), ScanSubmitInput{
		ManagedAnimeID: "anime-rec-1",
		TreeTop:        stringPtr("/library"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.State != "running" || view.Generation != 1 || view.Version != 1 {
		t.Fatalf("unexpected view: %+v", view)
	}
	var kind, ownerType string
	var ownerVersion int64
	if err := db.QueryRow(`SELECT kind, owner_type, expected_owner_version FROM work_item_history WHERE owner_id = 'anime-rec-1'`).Scan(&kind, &ownerType, &ownerVersion); err != nil {
		t.Fatal(err)
	}
	if kind != workitems.KindReconciliationScan || ownerType != workitems.OwnerFileMaintenance || ownerVersion != 1 {
		t.Fatalf("unexpected work item: kind=%s owner=%s ver=%d", kind, ownerType, ownerVersion)
	}
	// Duplicate submit while running conflicts.
	if _, err := service.SubmitScan(context.Background(), ScanSubmitInput{
		ManagedAnimeID: "anime-rec-1", TreeTop: stringPtr("/library"),
	}); err == nil {
		t.Fatal("running scan must conflict")
	}
}

func TestSubmitScanRejectsDeletedInstance(t *testing.T) {
	db, store := openReconTestDB(t)
	defer db.Close()
	service, err := NewService(db, store, &fakeScanner{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SubmitScan(context.Background(), ScanSubmitInput{
		ManagedAnimeID: "anime-deleted",
		TreeTop:        stringPtr("/library"),
	})
	var notFound *apperrors.NotFound
	if err == nil || !asNotFound(err, &notFound) {
		t.Fatalf("deleted instance must fail closed with not found, got %v", err)
	}
	// No owner row may exist for the deleted ID.
	var scans int
	if err := db.QueryRow(`SELECT COUNT(*) FROM reconciliation_scans WHERE managed_anime_id = 'anime-deleted'`).Scan(&scans); err != nil {
		t.Fatal(err)
	}
	if scans != 0 {
		t.Fatalf("deleted instance must not create a scan owner, got %d", scans)
	}
}

func asNotFound(err error, target **apperrors.NotFound) bool {
	for err != nil {
		if notFound, ok := err.(*apperrors.NotFound); ok {
			*target = notFound
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

func TestSubmitScanPeriodicGateForStoppedInstance(t *testing.T) {
	db, store := openReconTestDB(t)
	defer db.Close()
	seedReconInstance(t, db, "anime-rec-stopped", "sub-rec-stopped", "stopped", nil)
	service, err := NewService(db, store, &fakeScanner{})
	if err != nil {
		t.Fatal(err)
	}
	periodic := ScanSubmitTriggerPeriodic
	_, err = service.SubmitScan(context.Background(), ScanSubmitInput{
		ManagedAnimeID: "anime-rec-stopped",
		TreeTop:        stringPtr("/library"),
		Trigger:        &periodic,
	})
	if err == nil {
		t.Fatal("periodic scan of a stopped instance must be rejected")
	}
	// An explicit manual trigger remains available for read-only preview.
	manual := ScanSubmitTriggerManual
	view, err := service.SubmitScan(context.Background(), ScanSubmitInput{
		ManagedAnimeID: "anime-rec-stopped",
		TreeTop:        stringPtr("/library"),
		Trigger:        &manual,
	})
	if err != nil {
		t.Fatalf("manual scan of a stopped instance must be allowed: %v", err)
	}
	if view.State != "running" {
		t.Fatalf("manual scan must start: %+v", view)
	}
}

func TestScanHandlerBatchesAndCompletesImmutableGeneration(t *testing.T) {
	db, store := openReconTestDB(t)
	defer db.Close()
	files := testFiles(450) // 3 batches (200/200/50)
	seedReconInstance(t, db, "anime-rec-2", "sub-rec-2", "active", files)
	service, err := NewService(db, store, &fakeScanner{files: files})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := service.SubmitScan(ctx, ScanSubmitInput{ManagedAnimeID: "anime-rec-2", TreeTop: stringPtr("/library")}); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(ctx, "rec-worker", []string{workitems.KindReconciliationScan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.ScanHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	var state string
	var generation int64
	if err := db.QueryRow(`SELECT state, generation FROM reconciliation_scans WHERE managed_anime_id = 'anime-rec-2'`).Scan(&state, &generation); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || generation != 1 {
		t.Fatalf("scan not completed: state=%s gen=%d", state, generation)
	}
	// Placement-owned: one observation per owned Placement (present).
	var observations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM reconciliation_files WHERE managed_anime_id = 'anime-rec-2' AND generation = 1 AND facts->>'disposition' = 'present'`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if observations != 450 {
		t.Fatalf("expected 450 present observations, got %d", observations)
	}
	// Generation usable only when completed.
	usable, err := service.GenerationUsable(ctx, "anime-rec-2", 1)
	if err != nil || !usable {
		t.Fatalf("completed generation must be usable: %v %v", usable, err)
	}
	if usable, _ := service.GenerationUsable(ctx, "anime-rec-2", 2); usable {
		t.Fatal("unknown generation must not be usable")
	}
	// Observations are pageable.
	page, err := service.ListObservations(ctx, "anime-rec-2", 1, 100, 0)
	if err != nil || len(page.Items) != 100 {
		t.Fatalf("paged observations: %d items err=%v", len(page.Items), err)
	}
}

func TestScanHandlerMissingPlacementObservation(t *testing.T) {
	db, store := openReconTestDB(t)
	defer db.Close()
	files := testFiles(2)
	seedReconInstance(t, db, "anime-rec-3", "sub-rec-3", "active", files)
	// Scanner returns only one file: the second placement is missing.
	service, err := NewService(db, store, &fakeScanner{files: files[:1]})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := service.SubmitScan(ctx, ScanSubmitInput{ManagedAnimeID: "anime-rec-3", TreeTop: stringPtr("/library")}); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(ctx, "rec-worker", []string{workitems.KindReconciliationScan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.ScanHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	var present, missing int
	if err := db.QueryRow(`SELECT COUNT(*) FROM reconciliation_files WHERE managed_anime_id = 'anime-rec-3' AND facts->>'disposition' = 'present'`).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM reconciliation_files WHERE managed_anime_id = 'anime-rec-3' AND facts->>'disposition' = 'missing'`).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if present != 1 || missing != 1 {
		t.Fatalf("expected 1 present + 1 missing observation, got present=%d missing=%d", present, missing)
	}
	var locatedVia string
	if err := db.QueryRow(`SELECT facts->>'located_via' FROM reconciliation_files WHERE managed_anime_id = 'anime-rec-3' AND facts->>'disposition' = 'present'`).Scan(&locatedVia); err != nil {
		t.Fatal(err)
	}
	if locatedVia != "current_path" {
		t.Fatalf("present observation must be located via current_path, got %q", locatedVia)
	}
}

func TestScanHandlerResumesFromCheckpointWithoutRehashing(t *testing.T) {
	db, store := openReconTestDB(t)
	defer db.Close()
	files := testFiles(400) // 2 batches
	seedReconInstance(t, db, "anime-rec-6", "sub-rec-6", "active", files)
	service, err := NewService(db, store, &fakeScanner{files: files})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Seed an owner already checkpointed at batch 0 (first batch committed),
	// plus the first batch of observations, then a fresh work item at the
	// current owner version.
	scope, _ := json.Marshal(map[string]any{"tree_top": "/library"})
	progress, _ := json.Marshal(map[string]any{"last_committed_batch": 0, "scanned_files": 200, "hashed_files": 200})
	stats, _ := json.Marshal(map[string]any{"files": 0, "bytes": 0})
	if _, err := db.Exec(`
INSERT INTO reconciliation_scans
(managed_anime_id, state, generation, scan_scope, progress, capacity_stats, input_fingerprint, version, started_at, updated_at)
VALUES ('anime-rec-6', 'running', 1, $1, $2, $3, 'fp-seeded', 2, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		scope, progress, stats); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		file := files[i]
		facts, _ := json.Marshal(map[string]any{"placement_id": fmt.Sprintf("plc-anime-rec-6-%03d", i), "disposition": "present", "located_via": "current_path"})
		if _, err := tx.Exec(`
INSERT INTO reconciliation_files
(managed_anime_id, generation, batch_index, file_path, fingerprint, size_bytes, facts, observed_at)
VALUES ('anime-rec-6', 1, 0, $1, $2, $3, $4, CURRENT_TIMESTAMP)`,
			file.Path, file.Fingerprint, file.SizeBytes, facts); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	itemTx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, itemTx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: "anime-rec-6"},
		Kind:                 workitems.KindReconciliationScan,
		IdempotencyKey:       "recon:scan:anime-rec-6:1",
		InputSnapshot:        scope,
		MaxAttempts:          3,
		ExpectedOwnerVersion: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := itemTx.Commit(); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(ctx, "rec-worker", []string{workitems.KindReconciliationScan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.ScanHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("resume must complete: %+v", outcome)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	// The resume must have scanned the remaining batch only, yet observations
	// cover every placement exactly once (idempotent ON CONFLICT DO NOTHING).
	var observations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM reconciliation_files WHERE managed_anime_id = 'anime-rec-6' AND generation = 1`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if observations != 400 {
		t.Fatalf("expected 400 observations after resume, got %d", observations)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM reconciliation_scans WHERE managed_anime_id = 'anime-rec-6'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("scan must complete after resume, got %q", state)
	}
}

func TestScanHandlerFencingRejectsStaleClaim(t *testing.T) {
	db, store := openReconTestDB(t)
	defer db.Close()
	files := testFiles(10)
	seedReconInstance(t, db, "anime-rec-4", "sub-rec-4", "active", files)
	service, err := NewService(db, store, &fakeScanner{files: files})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := service.SubmitScan(ctx, ScanSubmitInput{ManagedAnimeID: "anime-rec-4", TreeTop: stringPtr("/library")}); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(ctx, "rec-worker", []string{workitems.KindReconciliationScan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Advance the owner out from under the claim (simulate a concurrent
	// resubmission that bumped the version).
	if _, err := db.Exec(`UPDATE reconciliation_scans SET version = version + 1 WHERE managed_anime_id = 'anime-rec-4'`); err != nil {
		t.Fatal(err)
	}
	outcome, err := service.ScanHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.TerminalState != workitems.StateFailed || outcome.OwnerResult.ReasonCode != "scan_fencing_stale" {
		t.Fatalf("stale fencing must fail closed: %+v", outcome)
	}
	var observations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM reconciliation_files WHERE managed_anime_id = 'anime-rec-4'`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if observations != 0 {
		t.Fatalf("stale claim must not commit observations, got %d", observations)
	}
}

func TestSubmitScanIdempotentReplayOfCompletedGeneration(t *testing.T) {
	db, store := openReconTestDB(t)
	defer db.Close()
	files := testFiles(5)
	seedReconInstance(t, db, "anime-rec-5", "sub-rec-5", "active", files)
	service, err := NewService(db, store, &fakeScanner{files: files})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := service.SubmitScan(ctx, ScanSubmitInput{ManagedAnimeID: "anime-rec-5", TreeTop: stringPtr("/library")})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(ctx, "rec-worker", []string{workitems.KindReconciliationScan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.ScanHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	replay, err := service.SubmitScan(ctx, ScanSubmitInput{ManagedAnimeID: "anime-rec-5", TreeTop: stringPtr("/library")})
	if err != nil {
		t.Fatal(err)
	}
	if replay.State != "completed" || replay.Generation != first.Generation {
		t.Fatalf("idempotent replay must return the completed generation: %+v", replay)
	}
	// The replay must not create a new scan work item (the completed
	// generation's calibrate item is a separate owner and stays untouched).
	var scanItems int
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_item_history WHERE owner_id = 'anime-rec-5' AND kind = 'reconciliation_scan'`).Scan(&scanItems); err != nil {
		t.Fatal(err)
	}
	if scanItems != 1 {
		t.Fatalf("replay must not create a new scan work item, got %d", scanItems)
	}
}

// TestScanHandlerCollapsesDuplicatePlacementPaths proves the merged
// reconciliation_files key (PRUNE-046 S10): two placements that carry the same
// physical path produce one row, not one row per placement. Downstream effects
// were already path-keyed (the planner's observation ref and the alignment
// idempotency key are "<anime>:<generation>:<path>"), so the merged key keeps
// the same effect semantics.
func TestScanHandlerCollapsesDuplicatePlacementPaths(t *testing.T) {
	db, store := openReconTestDB(t)
	defer db.Close()
	files := testFiles(1)
	seedReconInstance(t, db, "anime-rec-dup", "sub-rec-dup", "active", files)
	// A second placement for the same file (a second coverage owner of the
	// same bytes).
	if _, err := db.Exec(`
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, facts_version, version, created_at, updated_at)
VALUES ('plc-dup-extra', $1, 'anime-rec-dup', 'reconciliation', 'obs-dup-extra', $2, $2, 'parser-v1', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		files[0].Path, files[0].Fingerprint); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(db, store, &fakeScanner{files: files})
	if err != nil {
		t.Fatal(err)
	}
	if generation := completeScan(t, service, store, manualTrigger("anime-rec-dup", "dup-1")); generation != 1 {
		t.Fatalf("duplicate-path scan generation = %d, want 1", generation)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM reconciliation_files WHERE managed_anime_id = 'anime-rec-dup' AND generation = 1`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("duplicate placement paths must collapse to one reconciliation file row, got %d", rows)
	}
	// The surviving row keeps the observation half of the scanned path.
	var observed bool
	if err := db.QueryRow(`SELECT observed_at IS NOT NULL AND facts->>'disposition' = 'present' FROM reconciliation_files WHERE managed_anime_id = 'anime-rec-dup' AND generation = 1 AND file_path = $1`, files[0].Path).Scan(&observed); err != nil {
		t.Fatal(err)
	}
	if !observed {
		t.Fatal("the surviving row must keep the scan observation half")
	}
}
