package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/filemaintenance/planner"
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/library/domain"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

// ---------------------------------------------------------------------------
// Fixtures: a completed generation (scan owner + immutable observations), an
// anime with a subscription and an instance retention policy, and a wired
// calibration service with both consumers.
// ---------------------------------------------------------------------------

type calibrationFixture struct {
	db          *sql.DB
	store       *workitems.Store
	calibration *CalibrationService
}

type alignmentCapturingLogger struct {
	info []observabilityport.RuntimeEvent
}

func (l *alignmentCapturingLogger) Debug(context.Context, observabilityport.RuntimeEvent) {}
func (l *alignmentCapturingLogger) Info(_ context.Context, event observabilityport.RuntimeEvent) {
	l.info = append(l.info, event)
}
func (l *alignmentCapturingLogger) Warn(context.Context, observabilityport.RuntimeEvent)  {}
func (l *alignmentCapturingLogger) Error(context.Context, observabilityport.RuntimeEvent) {}

// seedCompletedGeneration inserts a completed scan owner plus the observations
// of one generation. paths maps file path -> fingerprint. The seeded
// observation facts model the persisted output of the historical scan producer
// (which stored coverage with the file): production never derives coverage from
// a path, so a fixture that needs resolved coverage must persist it here. A
// path without a recognizable episode stays coverage-less and surfaces as
// unresolved.
func seedCompletedGeneration(t *testing.T, db *sql.DB, animeID string, generation int64, paths map[string]string) {
	t.Helper()
	scope, _ := json.Marshal(map[string]any{"tree_top": "/library"})
	progress, _ := json.Marshal(map[string]any{"last_committed_batch": "done", "scanned_files": len(paths), "hashed_files": len(paths)})
	stats, _ := json.Marshal(map[string]any{"files": len(paths), "bytes": 0})
	if _, err := db.Exec(`
INSERT INTO reconciliation_scans
(managed_anime_id, state, generation, scan_scope, progress, capacity_stats, input_fingerprint, version, started_at, completed_at, updated_at)
VALUES ($1, 'completed', $2, $3, $4, $5, $6, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT (managed_anime_id) DO UPDATE
SET state = 'completed', generation = $2, version = reconciliation_scans.version + 1, completed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP`,
		animeID, generation, scope, progress, stats, "fp-"+animeID); err != nil {
		t.Fatal(err)
	}
	for path, fingerprint := range paths {
		facts, _ := json.Marshal(persistedSeedFacts(path))
		if _, err := db.Exec(`
INSERT INTO reconciliation_files
(managed_anime_id, generation, batch_index, file_path, fingerprint, size_bytes, facts, observed_at)
VALUES ($1, $2, 0, $3, $4, 100, $5, CURRENT_TIMESTAMP)`,
			animeID, generation, path, fingerprint, facts); err != nil {
			t.Fatal(err)
		}
	}
}

// persistedSeedFacts models the coverage a historical scan producer persisted
// for a file path. It is deliberately test-local: production never derives
// coverage from a path.
func persistedSeedFacts(filePath string) map[string]any {
	facts := map[string]any{}
	if match := seedEpisodePath.FindStringSubmatch(filePath); match != nil {
		facts["coverage"] = []string{"tv:e" + strings.TrimLeft(match[1], "0")}
		facts["coverage_unresolved"] = false
	}
	return facts
}

var seedEpisodePath = regexp.MustCompile(`(?i)(?:EP|E)\s*0*([0-9]{1,3})\.mkv$`)

// seedCompletedGenerationWithRenderChains seeds the completed generation and,
// for every observed path, the completed file-operation chain whose frozen
// evidence the naming render consumes. Alignment tests need this: a placement
// without acquisition lineage carries no frozen facts, so its candidate would
// be excluded with render_facts_unavailable instead of planning an operation.
func seedCompletedGenerationWithRenderChains(t *testing.T, db *sql.DB, animeID string, generation int64, paths map[string]string, rules naming.NamingRules, bindings ...*frozenRenderBindingFacts) {
	t.Helper()
	seedCompletedGeneration(t, db, animeID, generation, paths)
	for path := range paths {
		match := seedEpisodePath.FindStringSubmatch(path)
		if match == nil {
			// An unresolved observation carries no placement and no chain: its
			// candidate stays excluded with coverage_unresolved.
			continue
		}
		episode := strings.TrimLeft(match[1], "0")
		chainSpec := placedRenderChainSpec{
			Key:     fmt.Sprintf("chain-%d-%s", generation, strings.NewReplacer("/", "-", " ", "", ".", "-").Replace(strings.TrimPrefix(path, "/"))),
			AnimeID: animeID, AnimeTitle: "Show",
			TMDBMediaKind: "tv", TMDBSeason: 1, SourceGroup: "Group",
			ReleaseTitle: "[Group] Show - " + episode,
			PlacedPath:   path, DownloadName: filepath.Base(path),
			Rules: rules, FrozenEpisode: episode,
			Coverage: []string{"tv:e" + episode},
		}
		if len(bindings) > 0 {
			chainSpec.BindingFacts = bindings[0]
		}
		chain := seedPlacedRenderChain(t, db, chainSpec)
		if _, err := db.Exec(`
UPDATE reconciliation_files SET facts = facts || jsonb_build_object('placement_id', $1::text)
WHERE managed_anime_id = $2 AND generation = $3 AND file_path = $4`,
			chain.PlacementID, animeID, generation, path); err != nil {
			t.Fatal(err)
		}
	}
}

func newCalibrationFixture(t *testing.T) *calibrationFixture {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	store, err := workitems.NewStore(db, workitems.DefaultRetentionDays, observabilityport.NopRuntimeLogger{})
	if err != nil {
		t.Fatal(err)
	}
	policies := NewPolicyService(db)
	placementService := NewPlacementService(db, nil, nil)
	calibration := NewCalibrationService(db, placementService, policies, nil, store)
	// The replacement consumer is wired by default (the trigger merges the
	// one live evaluate item behind the active gate); individual tests may
	// override it to simulate a broken consumer.
	calibration.SetReplacementConsumer(NewReplacementTriggerService(db, store))
	return &calibrationFixture{db: db, store: store, calibration: calibration}
}

// claimDue claims the next due item of one kind and returns the claim. When
// the store has no due item (the fixture seeded the generation directly
// instead of through the scan handler), the item is created through the real
// store first and then claimed — the full claim/finish lifecycle stays real.
func (f *calibrationFixture) claimDue(t *testing.T, kind string) workitems.Claim {
	t.Helper()
	claim, err := f.store.ClaimDue(context.Background(), "cal-test", []string{kind}, time.Minute)
	if err == nil {
		return claim
	}
	animeID := "anime-cal-0"
	if kind == workitems.KindReconciliationCalibrate {
		_ = f.db.QueryRow(`SELECT managed_anime_id FROM reconciliation_scans ORDER BY managed_anime_id LIMIT 1`).Scan(&animeID)
	} else {
		_ = f.db.QueryRow(`SELECT managed_anime_id FROM reconciliation_scans ORDER BY managed_anime_id LIMIT 1`).Scan(&animeID)
	}
	generation := int64(1)
	if kind == workitems.KindReconciliationCalibrate {
		_ = f.db.QueryRow(`SELECT MAX(generation) FROM reconciliation_files WHERE managed_anime_id = $1 AND NOT derived_missing`, animeID).Scan(&generation)
	} else {
		_ = f.db.QueryRow(`SELECT generation FROM reconciliation_scans WHERE managed_anime_id = $1`, animeID).Scan(&generation)
	}
	snapshot, _ := json.Marshal(map[string]any{"generation": generation})
	key := "recon:calibrate:" + animeID + ":1"
	if kind != workitems.KindReconciliationCalibrate {
		key = "recon:consume:" + kind + ":" + animeID + ":1"
	}
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Create(context.Background(), tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: animeID},
		Kind:                 kind,
		IdempotencyKey:       key,
		InputSnapshot:        snapshot,
		MaxAttempts:          3,
		ExpectedOwnerVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claim, err = f.store.ClaimDue(context.Background(), "cal-test", []string{kind}, time.Minute)
	if err != nil {
		t.Fatalf("claim %s after create: %v", kind, err)
	}
	return claim
}

// runHandler runs one handler to completion and finishes the claim.
func (f *calibrationFixture) runHandler(t *testing.T, handler worker.Handler, claim workitems.Claim) worker.Outcome {
	t.Helper()
	outcome, err := handler.Handle(context.Background(), claim)
	if err != nil {
		t.Fatalf("handler %s: %v", claim.Kind, err)
	}
	if err := f.store.FinishWithResult(context.Background(), claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatalf("finish %s: %v", claim.Kind, err)
	}
	return outcome
}

func (f *calibrationFixture) drainKind(t *testing.T, handler worker.Handler, kind string) []worker.Outcome {
	t.Helper()
	var outcomes []worker.Outcome
	for {
		claim, err := f.store.ClaimDue(context.Background(), "cal-test", []string{kind}, time.Minute)
		if err != nil {
			break
		}
		outcomes = append(outcomes, f.runHandler(t, handler, claim))
	}
	return outcomes
}

// ---------------------------------------------------------------------------
// Calibration: typed facts, placements, missing detection, zero file effects
// ---------------------------------------------------------------------------

func TestCalibrationProducesTypedFactsAndPlacements(t *testing.T) {
	fixture := newCalibrationFixture(t)
	defer fixture.db.Close()
	seedAnimeSubscription(t, fixture.db, "anime-cal-1", "sub-1", "stopped")
	seedInstanceReplacement(t, fixture.db, "anime-cal-1", ModeAll, ``, 1)
	seedCompletedGeneration(t, fixture.db, "anime-cal-1", 1, map[string]string{
		"/library/Show/Season 01/EP 01.mkv":   "fp-1",
		"/library/Show/Season 01/EP 02.mkv":   "fp-2",
		"/library/Show/Season 01/unknown.bin": "fp-3",
	})
	calibrate := fixture.calibration.CalibrateHandler()
	claim := fixture.claimDue(t, workitems.KindReconciliationCalibrate)
	outcome := fixture.runHandler(t, calibrate, claim)
	if outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("calibration must succeed: %+v", outcome)
	}
	// Typed facts: two present (resolved coverage) + one unresolved.
	var present, unresolved int
	if err := fixture.db.QueryRow(`
SELECT COUNT(*) FILTER (WHERE disposition = 'present'),
       COUNT(*) FILTER (WHERE disposition = 'unresolved')
FROM reconciliation_files WHERE managed_anime_id = 'anime-cal-1' AND generation = 1`).
		Scan(&present, &unresolved); err != nil {
		t.Fatal(err)
	}
	if present != 2 || unresolved != 1 {
		t.Fatalf("typed facts: present=%d unresolved=%d", present, unresolved)
	}
	// Reliable facts form verified active placements; the unresolved file does
	// not (fail closed, no guessed coverage).
	var placements int
	if err := fixture.db.QueryRow(`
SELECT COUNT(*) FROM placements WHERE managed_anime_id = 'anime-cal-1' AND state = 'active'`).Scan(&placements); err != nil {
		t.Fatal(err)
	}
	if placements != 2 {
		t.Fatalf("expected 2 placements from reliable facts, got %d", placements)
	}
	var resolvedPlacements int
	if err := fixture.db.QueryRow(`
SELECT COUNT(*) FROM placements WHERE source_owner_ref LIKE '%unknown.bin'`).Scan(&resolvedPlacements); err != nil {
		t.Fatal(err)
	}
	if resolvedPlacements != 0 {
		t.Fatalf("unresolved file must not admit a placement: %d", resolvedPlacements)
	}
	// The scan itself is zero-file-effect: no alignment/cleanup operation and
	// no file move was recorded by calibration.
	var alignmentOps, deletions int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM alignment_operations`).Scan(&alignmentOps); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM replacement_deletions`).Scan(&deletions); err != nil {
		t.Fatal(err)
	}
	if alignmentOps != 0 || deletions != 0 {
		t.Fatalf("calibration must be zero-file-effect: alignments=%d deletions=%d", alignmentOps, deletions)
	}
	// Both consumer claims were enqueued.
	for _, kind := range []string{workitems.KindReconciliationConsumeAlignment, workitems.KindReconciliationConsumeReplacement} {
		var count int
		if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM work_item_history WHERE kind = $1 AND owner_id = 'anime-cal-1'`, kind).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("consumer claim %s must be enqueued once, got %d", kind, count)
		}
	}
}

func TestCalibrationDetectsMissingFilesWithoutDeletingPlacements(t *testing.T) {
	fixture := newCalibrationFixture(t)
	defer fixture.db.Close()
	seedAnimeSubscription(t, fixture.db, "anime-cal-2", "sub-1", "active")
	seedInstanceReplacement(t, fixture.db, "anime-cal-2", ModeAll, ``, 1)
	seedCompletedGeneration(t, fixture.db, "anime-cal-2", 1, map[string]string{
		"/library/Show/Season 01/EP 01.mkv": "fp-1",
		"/library/Show/Season 01/EP 02.mkv": "fp-2",
	})
	calibrate := fixture.calibration.CalibrateHandler()
	fixture.runHandler(t, calibrate, fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	// Generation 2: EP 02 disappeared externally.
	seedCompletedGeneration(t, fixture.db, "anime-cal-2", 2, map[string]string{
		"/library/Show/Season 01/EP 01.mkv": "fp-1",
	})
	fixture.runHandler(t, calibrate, fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	// EP 02 is typed missing; no placement is deleted or superseded.
	var missing int
	if err := fixture.db.QueryRow(`
SELECT COUNT(*) FROM reconciliation_files
WHERE managed_anime_id = 'anime-cal-2' AND generation = 2 AND disposition = 'missing' AND file_path LIKE '%EP 02%'`).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing != 1 {
		t.Fatalf("disappeared file must be typed missing: %d", missing)
	}
	var deleted, superseded int
	if err := fixture.db.QueryRow(`
SELECT COUNT(*) FILTER (WHERE state = 'superseded'), COUNT(*) FROM placements WHERE managed_anime_id = 'anime-cal-2'`).
		Scan(&superseded, &deleted); err != nil {
		t.Fatal(err)
	}
	// Generation 1 admitted two placements; generation 2 observes EP 01 at a
	// path its placement already holds, so it admits nothing new.
	if deleted != 2 || superseded != 0 {
		t.Fatalf("missing facts must not delete or supersede placements, and a held path is not admitted again: total=%d superseded=%d", deleted, superseded)
	}
}

// A rescan never admits a second Placement for a file that already has one:
// neither a Placement-owned observation nor a path an earlier admission holds.
func TestCalibrationDoesNotReadmitPlacedFiles(t *testing.T) {
	fixture := newCalibrationFixture(t)
	defer fixture.db.Close()
	seedAnimeSubscription(t, fixture.db, "anime-cal-readmit", "sub-1", "active")
	seedInstanceReplacement(t, fixture.db, "anime-cal-readmit", ModeAll, ``, 1)
	seedCompletedGeneration(t, fixture.db, "anime-cal-readmit", 1, map[string]string{
		"/library/Show/Season 01/EP 01.mkv": "fp-1",
	})
	calibrate := fixture.calibration.CalibrateHandler()
	fixture.runHandler(t, calibrate, fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	for generation := int64(2); generation <= 3; generation++ {
		seedCompletedGeneration(t, fixture.db, "anime-cal-readmit", generation, map[string]string{
			"/library/Show/Season 01/EP 01.mkv": "fp-1",
		})
		fixture.runHandler(t, calibrate, fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	}
	var active int
	if err := fixture.db.QueryRow(`
SELECT COUNT(*) FROM placements WHERE managed_anime_id = 'anime-cal-readmit' AND state = 'active'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("three scans of one file must leave one active placement, got %d", active)
	}
}

// Calibration coverage for a Placement-owned observation comes from the frozen
// placement coverage set, not from the current metadata binding: a metadata
// flip to a movie after admission must not re-classify the already-placed file
// in the calibrated facts the replacement consumers read. A legacy observation
// without frozen coverage keeps consuming the metadata movie classification.
func TestCalibrationPlacementCoverageSurvivesMetadataRebinding(t *testing.T) {
	fixture := newCalibrationFixture(t)
	defer fixture.db.Close()
	seedAnimeSubscription(t, fixture.db, "anime-cal-rebind", "sub-1", "stopped")
	seedInstanceReplacement(t, fixture.db, "anime-cal-rebind", ModeAll, ``, 1)
	seedCompletedGeneration(t, fixture.db, "anime-cal-rebind", 1, map[string]string{})
	// One Placement-owned observation (frozen coverage) and one legacy
	// observation with no frozen coverage at all.
	seedPlacementOwnedObservation(t, fixture.db, "anime-cal-rebind", 1, 0, "plc-cal-rebind", "/library/Show/EP 01.mkv", "fp-1", "tv:e1")
	seedObservationFacts(t, fixture.db, "anime-cal-rebind", 1, 1, "/library/Show/legacy.mkv", "fp-legacy", 100, `{}`)
	// The instance metadata binding flips to a movie before calibration runs.
	seedMovieMetadata(t, fixture.db, "anime-cal-rebind")

	calibrate := fixture.calibration.CalibrateHandler()
	outcome := fixture.runHandler(t, calibrate, fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	if outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("calibration must succeed: %+v", outcome)
	}

	calibratedCoverage := func(filePath string) []string {
		t.Helper()
		var raw []byte
		if err := fixture.db.QueryRow(`
SELECT coverage FROM reconciliation_files
WHERE managed_anime_id = 'anime-cal-rebind' AND generation = 1 AND file_path = $1`, filePath).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var coverage []string
		if err := json.Unmarshal(raw, &coverage); err != nil {
			t.Fatal(err)
		}
		return coverage
	}
	if got := calibratedCoverage("/library/Show/EP 01.mkv"); len(got) != 1 || got[0] != "tv:e1" {
		t.Fatalf("placement-owned calibrated coverage = %v, want the frozen tv:e1", got)
	}
	if got := calibratedCoverage("/library/Show/legacy.mkv"); len(got) != 1 || got[0] != "movie:0" {
		t.Fatalf("legacy calibrated coverage = %v, want the metadata movie classification", got)
	}
	var frozenKey string
	if err := fixture.db.QueryRow(`
SELECT observation_key FROM placement_coverage WHERE placement_id = 'plc-cal-rebind'`).Scan(&frozenKey); err != nil {
		t.Fatal(err)
	}
	if frozenKey != "tv:e1" {
		t.Fatalf("frozen placement coverage = %q, want tv:e1", frozenKey)
	}
}

func TestCalibrationReplayIsIdempotent(t *testing.T) {
	fixture := newCalibrationFixture(t)
	defer fixture.db.Close()
	seedAnimeSubscription(t, fixture.db, "anime-cal-3", "sub-1", "active")
	seedInstanceReplacement(t, fixture.db, "anime-cal-3", ModeAll, ``, 1)
	seedCompletedGeneration(t, fixture.db, "anime-cal-3", 1, map[string]string{
		"/library/Show/Season 01/EP 01.mkv": "fp-1",
	})
	calibrate := fixture.calibration.CalibrateHandler()
	fixture.runHandler(t, calibrate, fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	// Crash replay: the same claim input runs again (a fresh claim on the same
	// item is simulated by a direct handler call with an identical claim).
	claim := workitems.Claim{
		Kind:                 workitems.KindReconciliationCalibrate,
		Owner:                workitems.OwnerRef{Type: workitems.OwnerFileMaintenance, ID: "anime-cal-3"},
		InputSnapshot:        json.RawMessage(`{"generation":1}`),
		ExpectedOwnerVersion: 1,
		FencingToken:         2,
	}
	outcome, err := calibrate.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateSucceeded {
		t.Fatalf("replay must converge: %+v", outcome)
	}
	var facts, placements, calibrateItems int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM reconciliation_files WHERE managed_anime_id = 'anime-cal-3'`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM placements WHERE managed_anime_id = 'anime-cal-3'`).Scan(&placements); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM work_item_history WHERE kind = 'reconciliation_calibrate' AND owner_id = 'anime-cal-3'`).Scan(&calibrateItems); err != nil {
		t.Fatal(err)
	}
	if facts != 1 || placements != 1 || calibrateItems != 1 {
		t.Fatalf("replay must not duplicate owner rows: facts=%d placements=%d items=%d", facts, placements, calibrateItems)
	}
}

// ---------------------------------------------------------------------------
// Fan-out: stopped generation alignment runs, replacement increments stay zero
// ---------------------------------------------------------------------------

// enableAlignment enables the anime-scope alignment policy.
func enableAlignment(t *testing.T, db *sql.DB, animeID string) {
	t.Helper()
	policies := NewPolicyService(db)
	enabled := true
	if _, err := policies.SavePolicy(context.Background(), "anime:"+animeID, 0, PolicyRow{
		Enabled: true, AlignmentEnabled: &enabled, Safety: json.RawMessage(`{}`),
	}, "owner", "test"); err != nil {
		t.Fatal(err)
	}
}

func TestStoppedGenerationAlignmentRunsReplacementZero(t *testing.T) {
	fixture := newCalibrationFixture(t)
	defer fixture.db.Close()
	seedAnimeSubscription(t, fixture.db, "anime-fan-1", "sub-1", "stopped")
	seedInstanceReplacement(t, fixture.db, "anime-fan-1", ModePerGroup,
		`{"version":"profile-v1","items":[{"key":"release.resolution","ranks":[["1080p"],["720p"],["480p"]]}]}`, 1)
	enableAlignment(t, fixture.db, "anime-fan-1")
	seedCompletedGeneration(t, fixture.db, "anime-fan-1", 1, map[string]string{
		// Target renderer is absent (planner nil) in this fixture, so the
		// alignment consumer finishes blocked on the unavailable planner; the
		// replacement consumer must still record its own stopped outcome.
		"/library/Show/EP 01.mkv": "fp-1",
	})
	fixture.runHandler(t, fixture.calibration.CalibrateHandler(), fixture.claimDue(t, workitems.KindReconciliationCalibrate))

	// Alignment consumer: planner not wired in this fixture -> blocked, its own
	// outcome, never touching replacement.
	alignOutcomes := fixture.drainKind(t, fixture.calibration.ConsumeAlignmentHandler(), workitems.KindReconciliationConsumeAlignment)
	if len(alignOutcomes) != 1 || alignOutcomes[0].OwnerResult.State != "blocked" {
		t.Fatalf("alignment consumer must record its own blocked outcome: %+v", alignOutcomes)
	}
	// Replacement consumer on a stopped subscription: blocked with zero
	// replacement increments (no evaluate item, no decision, no cleanup).
	replOutcomes := fixture.drainKind(t, fixture.calibration.ConsumeReplacementHandler(), workitems.KindReconciliationConsumeReplacement)
	if len(replOutcomes) != 1 || replOutcomes[0].OwnerResult.State != "blocked" || replOutcomes[0].OwnerResult.ReasonCode != "subscription_stopped" {
		t.Fatalf("replacement consumer must be blocked on stopped: %+v", replOutcomes)
	}
	var evaluateItems, decisions, deletions int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM work_item_history WHERE kind = 'replacement_evaluate' AND owner_id = 'anime-fan-1'`).Scan(&evaluateItems); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM replacement_decisions WHERE managed_anime_id = 'anime-fan-1'`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM replacement_deletions WHERE managed_anime_id = 'anime-fan-1'`).Scan(&deletions); err != nil {
		t.Fatal(err)
	}
	if evaluateItems != 0 || decisions != 0 || deletions != 0 {
		t.Fatalf("stopped generation replacement increments must be zero: items=%d decisions=%d deletions=%d", evaluateItems, decisions, deletions)
	}
	// Checkpoints exist and are independent owner truth.
	var alignmentState, replacementState string
	if err := fixture.db.QueryRow(`
SELECT alignment_checkpoint_state FROM reconciliation_scans WHERE managed_anime_id = 'anime-fan-1'`).Scan(&alignmentState); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`
SELECT replacement_checkpoint_state FROM reconciliation_scans WHERE managed_anime_id = 'anime-fan-1'`).Scan(&replacementState); err != nil {
		t.Fatal(err)
	}
	if alignmentState != "blocked" || replacementState != "blocked" {
		t.Fatalf("both consumers must record their own outcome: alignment=%s replacement=%s", alignmentState, replacementState)
	}
}

// ---------------------------------------------------------------------------
// One consumer blocked never changes the other consumer's owner result
// ---------------------------------------------------------------------------

// failingTrigger simulates a replacement consumer whose trigger cannot merge
// the evaluate claim (e.g. transient store failure). It is a *wrapped* port:
// the real trigger type is not an interface, so the test builds a
// ReplacementTriggerService whose work item store is broken instead.
func brokenTrigger(db *sql.DB) *ReplacementTriggerService {
	return NewReplacementTriggerService(db, nil)
}

func TestConsumersAreIndependentWhenOneFails(t *testing.T) {
	fixture := newCalibrationFixture(t)
	defer fixture.db.Close()
	seedAnimeSubscription(t, fixture.db, "anime-fan-2", "sub-1", "active")
	seedInstanceReplacement(t, fixture.db, "anime-fan-2", ModeAll, ``, 1)
	seedCompletedGeneration(t, fixture.db, "anime-fan-2", 1, map[string]string{
		"/library/Show/Season 01/EP 01.mkv": "fp-1",
	})
	// Replacement trigger broken (nil work item store); alignment consumer
	// wired. The alignment consumer is untouched by the failure.
	fixture.calibration.SetReplacementConsumer(brokenTrigger(fixture.db))
	fixture.runHandler(t, fixture.calibration.CalibrateHandler(), fixture.claimDue(t, workitems.KindReconciliationCalibrate))

	// Alignment consumer: fully wired path (planner absent -> blocked with its
	// own reason) still records its outcome.
	alignOutcomes := fixture.drainKind(t, fixture.calibration.ConsumeAlignmentHandler(), workitems.KindReconciliationConsumeAlignment)
	if len(alignOutcomes) != 1 || alignOutcomes[0].OwnerResult.State != "blocked" {
		t.Fatalf("alignment must record its own outcome despite replacement failure: %+v", alignOutcomes)
	}
	// Replacement consumer: the trigger error is retried (handler error, not a
	// finish), leaving the checkpoint pending.
	claim, err := fixture.store.ClaimDue(context.Background(), "cal-test", []string{workitems.KindReconciliationConsumeReplacement}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.calibration.ConsumeReplacementHandler().Handle(context.Background(), claim)
	if err == nil {
		t.Fatal("replacement consumer must surface the trigger error for its own retry")
	}
	// The failed consumer leaves no terminal checkpoint (retry evidence stays
	// on the work item itself), so a retry re-runs the consumer.
	var checkpoints int
	if err := fixture.db.QueryRow(`
SELECT COUNT(*) FROM reconciliation_scans WHERE managed_anime_id = 'anime-fan-2' AND replacement_checkpoint_state IS NOT NULL`).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if checkpoints != 0 {
		t.Fatalf("failed replacement consumer must leave the checkpoint pending/absent for retry, got %d rows", checkpoints)
	}
}

// ---------------------------------------------------------------------------
// Maintenance-only local-owner cleanup calibration
// ---------------------------------------------------------------------------

func TestLocalOwnerCleanupConflictRetainsOwners(t *testing.T) {
	fixture := newCalibrationFixture(t)
	defer fixture.db.Close()
	seedAnimeSubscription(t, fixture.db, "anime-unsub-1", "sub-1", "active")
	seedInstanceReplacement(t, fixture.db, "anime-unsub-1", ModeAll, ``, 1)
	// A pending destructive effect: an admitted cleanup operation.
	if _, err := fixture.db.Exec(`
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, quality_facts_ref, facts_version, version, created_at, updated_at)
VALUES ('plc-1', '/library/Old/EP01.mkv', 'anime-unsub-1', 'reconciliation', 'obs-old', 'fp', 'fp', NULL, 'parser-v1', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`
INSERT INTO replacement_deletions
(id, idempotency_key, placement_id, managed_anime_id, coverage_keys, replacement_ref,
 admission_mode, source_path, source_fingerprint, state, version, created_at, updated_at)
VALUES ('cln-1', 'cln:plc-1:dec-1', 'plc-1', 'anime-unsub-1', '["tv:e1"]', 'dec-1',
 'single_copy', '/library/Old/EP01.mkv', 'fp', 'admitted', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	cleanup := NewLocalOwnerCleanup(fixture.db)
	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	err = cleanup.CleanupLocalOwnersTx(context.Background(), txscope.Wrap(tx), "anime-unsub-1")
	_ = tx.Rollback()
	var business *apperrors.Business
	if err == nil || !errors.As(err, &business) || business.Code != "LOCAL_OWNER_CLEANUP_ACTIVE_EFFECT" {
		t.Fatalf("checkpointed effect must produce the typed conflict, got %v", err)
	}
	// Owners retained for recovery.
	var placements, deletions int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM placements WHERE managed_anime_id = 'anime-unsub-1'`).Scan(&placements); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM replacement_deletions WHERE managed_anime_id = 'anime-unsub-1'`).Scan(&deletions); err != nil {
		t.Fatal(err)
	}
	if placements != 1 || deletions != 1 {
		t.Fatalf("conflict must retain owners: placements=%d deletions=%d", placements, deletions)
	}
}

// TestLocalOwnerCleanupRemovesEveryFileMaintenanceOwnerRow seeds every
// FileMaintenance owner family in a terminal state and proves the leaf-first
// cleanup leaves zero orphan rows behind.
func TestLocalOwnerCleanupRemovesEveryFileMaintenanceOwnerRow(t *testing.T) {
	fixture := newCalibrationFixture(t)
	defer fixture.db.Close()
	seedAnimeSubscription(t, fixture.db, "anime-unsub-2", "sub-1", "stopped")
	seedInstanceReplacement(t, fixture.db, "anime-unsub-2", ModeAll, ``, 1)
	// An instance FileMaintenance policy override must die with the instance
	// (otherwise the scheduler keeps submitting scans for a deleted ID).
	if _, err := fixture.db.Exec(`
INSERT INTO filemaintenance_policies
(scope_key, enabled, scan_interval_seconds, alignment_enabled, safety, version, updated_at)
VALUES ('anime:anime-unsub-2', true, 3600, true, '{"max_operations":100}'::jsonb, 1, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	seedCompletedGeneration(t, fixture.db, "anime-unsub-2", 1, map[string]string{
		"/library/Show/Season 01/EP 01.mkv": "fp-1",
	})
	// Calibration rows + consumer checkpoints.
	fixture.runHandler(t, fixture.calibration.CalibrateHandler(), fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	fixture.drainKind(t, fixture.calibration.ConsumeAlignmentHandler(), workitems.KindReconciliationConsumeAlignment)
	fixture.drainKind(t, fixture.calibration.ConsumeReplacementHandler(), workitems.KindReconciliationConsumeReplacement)
	// A settled decision + superseded placement + completed cleanup + a
	// completed alignment operation (all terminal: no live effect).
	if _, err := fixture.db.Exec(`
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, quality_facts_ref, facts_version, state, version, created_at, updated_at)
VALUES ('plc-old', '/library/Old/EP01.mkv', 'anime-unsub-2', 'reconciliation', 'obs-old', 'fp', 'fp', NULL, 'parser-v1', 'superseded', 2, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	seedPlacement(t, fixture.db, "plc-new", "anime-unsub-2", "/library/Show/EP01.mkv", "fp-new", []string{"tv:e1"}, "2026-08-02T00:00:00Z")
	if _, err := fixture.db.Exec(`
INSERT INTO replacement_decisions
(id, idempotency_key, managed_anime_id, candidate_release_id, candidate_placement_id, policy_version,
 profile_version, subscription_version, candidate_facts_ref, coverage_key, coverage_keys, admission_mode, pools, verdict, state, version, created_at, updated_at)
VALUES ('dec-settled', 'repl:anime-unsub-2:x', 'anime-unsub-2', 'rel-1', 'plc-new', 1, 'profile-v1', 1,
 'rel-1:1', 'tv:e1', '["tv:e1"]', 'single_copy', '[]', 'replace', 'settled', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`
INSERT INTO replacement_deletions
(id, idempotency_key, placement_id, managed_anime_id, coverage_keys, replacement_ref,
 admission_mode, source_path, source_fingerprint, state, version, created_at, updated_at)
VALUES ('cln-done', 'cln:plc-old:dec-settled', 'plc-old', 'anime-unsub-2', '["tv:e1"]', 'dec-settled',
 'single_copy', '/library/Old/EP01.mkv', 'fp', 'deleted', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(`
INSERT INTO alignment_operations
(id, idempotency_key, managed_anime_id, scan_generation, observation_ref, candidate_ref, policy_version,
 metadata_version, rules_version, naming_version, source_path, source_fingerprint, target_path,
 target_contract, dry_run_result, state, version, created_at, updated_at)
VALUES ('alg-done', 'align:x', 'anime-unsub-2', 1, 'anime-unsub-2:1:/library/Show/EP01.mkv', 'c', 1, 1, 1, 1,
 '/library/Show/EP01.mkv', 'fp', '/library/Show/Season 01/EP01.mkv', '{}', '{}', 'completed', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

	// A resolved alignment request of the placement goes with it.
	if _, err := fixture.db.Exec(`INSERT INTO alignment_requests (placement_id) VALUES ('plc-new')`); err != nil {
		t.Fatal(err)
	}
	cleanup := NewLocalOwnerCleanup(fixture.db)
	tx, err := fixture.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := cleanup.CleanupLocalOwnersTx(context.Background(), txscope.Wrap(tx), "anime-unsub-2"); err != nil {
		t.Fatalf("terminal-only owners must clean up locally: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for table, columns := range map[string]string{
		"work_item_history":        "id = 'wi-unsub' OR owner_id = 'anime-unsub-2' OR (owner_type = 'alignment_operation' AND owner_id IN (SELECT id FROM alignment_operations WHERE managed_anime_id = 'anime-unsub-2')) OR (owner_type = 'replacement_deletion' AND owner_id IN (SELECT id FROM replacement_deletions WHERE managed_anime_id = 'anime-unsub-2'))",
		"alignment_requests":       "placement_id = 'plc-new'",
		"replacement_decisions":    "managed_anime_id = 'anime-unsub-2'",
		"placements":               "managed_anime_id = 'anime-unsub-2'",
		"placement_coverage":       "placement_id IN (SELECT id FROM placements WHERE managed_anime_id = 'anime-unsub-2')",
		"reconciliation_files":     "managed_anime_id = 'anime-unsub-2'",
		"reconciliation_scans":     "managed_anime_id = 'anime-unsub-2'",
		"anime_business_configs":   "managed_anime_id = 'anime-unsub-2'",
		"filemaintenance_policies": "scope_key = 'anime:anime-unsub-2'",
	} {
		var count int
		if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE ` + columns).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("orphan rows in %s after unsubscribe: %d", table, count)
		}
	}
}

// ---------------------------------------------------------------------------
// Resume: replacement uses the latest generation facts in a fresh epoch and
// never replays a pre-stop decision
// ---------------------------------------------------------------------------

// TestResumeCreatesFreshEpochFromLatestFacts drives the full stopped/resume
// chain: an active generation evaluates and settles, a stopped generation
// blocks the replacement consumer with zero increments, and resume merges a
// fresh epoch item that freezes the NEW subscription version — the pre-stop
// decision is never replayed.
func TestResumeCreatesFreshEpochFromLatestFacts(t *testing.T) {
	fixture := newCalibrationFixture(t)
	defer fixture.db.Close()
	seedAnimeSubscription(t, fixture.db, "anime-resume-1", "sub-1", "active")
	seedInstanceReplacement(t, fixture.db, "anime-resume-1", ModePerGroup,
		`{"version":"profile-v1","items":[{"key":"release.resolution","ranks":[["1080p"],["720p"],["480p"]]}]}`, 1)
	// Release-backed verified placement: the evaluate candidate chain.
	seedFilePlacement(t, fixture.db, "anime-resume-1", "sub-1", "release-r1",
		[]string{"tv:e1"}, Facts{Resolution: "720p", Group: "GroupA"}, "2026-07-01T00:00:00Z")
	ctx := context.Background()

	// Generation 1 while active: replacement consumer merges the evaluate
	// item, the evaluate handler freezes and settles a decision.
	seedCompletedGeneration(t, fixture.db, "anime-resume-1", 1, map[string]string{
		"/library/Show/Season 01/EP 01.mkv": "fp-1",
	})
	fixture.runHandler(t, fixture.calibration.CalibrateHandler(), fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	replOutcomes := fixture.drainKind(t, fixture.calibration.ConsumeReplacementHandler(), workitems.KindReconciliationConsumeReplacement)
	if len(replOutcomes) != 1 || replOutcomes[0].OwnerResult.State != "consumed" {
		t.Fatalf("active generation must consume replacement: %+v", replOutcomes)
	}
	evaluate := NewReplacementDecisionService(fixture.db)
	cleanup := NewCleanupService(fixture.db)
	cleanup.SetWorkItems(fixture.store)
	evaluate.SetCleanupHandoff(cleanup)
	evalOutcomes := fixture.drainKind(t, evaluate.EvaluateHandler(), workitems.KindReplacementEvaluate)
	if len(evalOutcomes) != 1 || evalOutcomes[0].OwnerResult.State == "" {
		t.Fatalf("evaluate must run: %+v", evalOutcomes)
	}
	var decisionVersion int64
	if err := fixture.db.QueryRow(`
SELECT subscription_version FROM replacement_decisions
WHERE managed_anime_id = 'anime-resume-1' ORDER BY created_at DESC LIMIT 1`).Scan(&decisionVersion); err != nil {
		t.Fatal(err)
	}
	if decisionVersion != 1 {
		t.Fatalf("active decision must freeze subscription version 1: %d", decisionVersion)
	}

	// Stop: the version bump invalidates further settle; a stopped generation
	// blocks the replacement consumer with zero increments.
	if _, err := fixture.db.Exec(`UPDATE subscriptions SET state = 'stopped', version = version + 1 WHERE managed_anime_id = 'anime-resume-1'`); err != nil {
		t.Fatal(err)
	}
	seedCompletedGeneration(t, fixture.db, "anime-resume-1", 2, map[string]string{
		"/library/Show/Season 01/EP 01.mkv": "fp-1",
	})
	fixture.runHandler(t, fixture.calibration.CalibrateHandler(), fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	stopped := fixture.drainKind(t, fixture.calibration.ConsumeReplacementHandler(), workitems.KindReconciliationConsumeReplacement)
	if len(stopped) != 1 || stopped[0].OwnerResult.ReasonCode != "subscription_stopped" {
		t.Fatalf("stopped generation must block replacement: %+v", stopped)
	}
	var decisionsAfterStop int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM replacement_decisions WHERE managed_anime_id = 'anime-resume-1'`).Scan(&decisionsAfterStop); err != nil {
		t.Fatal(err)
	}
	if decisionsAfterStop != 1 {
		t.Fatalf("stopped generation must add zero decisions: %d", decisionsAfterStop)
	}

	// Resume: a fresh epoch item with the NEW subscription version; the old
	// decision is not replayed.
	if _, err := fixture.db.Exec(`UPDATE subscriptions SET state = 'active', version = version + 1 WHERE managed_anime_id = 'anime-resume-1'`); err != nil {
		t.Fatal(err)
	}
	trigger := NewReplacementTriggerService(fixture.db, fixture.store)
	if err := trigger.TriggerSubscriptionResumed(ctx, "anime-resume-1"); err != nil {
		t.Fatal(err)
	}
	item := loadLiveEvaluateItem(t, fixture.db, "anime-resume-1")
	if item == nil || item.Epoch != 3 || item.SubscriptionVersion != 3 {
		t.Fatalf("resume must create the fresh epoch item: %+v", item)
	}
	evalOutcomes = fixture.drainKind(t, evaluate.EvaluateHandler(), workitems.KindReplacementEvaluate)
	if len(evalOutcomes) != 1 {
		t.Fatalf("resume evaluate must run: %+v", evalOutcomes)
	}
	var latestDecisionVersion int64
	if err := fixture.db.QueryRow(`
SELECT subscription_version FROM replacement_decisions
WHERE managed_anime_id = 'anime-resume-1' ORDER BY created_at DESC LIMIT 1`).Scan(&latestDecisionVersion); err != nil {
		t.Fatal(err)
	}
	if latestDecisionVersion != 3 {
		t.Fatalf("resume decision must freeze the new epoch version 3: %d", latestDecisionVersion)
	}
}

// ---------------------------------------------------------------------------
// Alignment consumer full path: stopped instances still plan under their own
// policy; replacement increments stay zero
// ---------------------------------------------------------------------------

// TestAlignmentCandidateSummary verifies the planner aggregation mirrors
// admission classification, including target anchoring and no-op skips.
func TestAlignmentCandidateSummary(t *testing.T) {
	candidates := []PlanCandidate{
		{SourcePath: "/library/source.mkv", TargetPath: "/library/target.mkv"},
		// The source already sits at its absolute rendered target: the
		// no-op classification requires same absolute paths on both sides.
		{SourcePath: "/library/aligned.mkv", TargetPath: "/library/aligned.mkv"},
		{SourcePath: "/library/missing.mkv"},
		{SourcePath: "/library/relative-render.mkv", TargetPath: "root-relative-target.mkv"},
		{Excluded: true, ExcludeReasonCode: "coverage_unresolved"},
		{Excluded: true, ExcludeReasonCode: "target_render_failed"},
		{Excluded: true, ExcludeReasonCode: "unexpected raw error /var/file"},
	}

	candidateCount, executableCount, reasons := alignmentCandidateSummary(candidates)
	if candidateCount != 7 || executableCount != 1 {
		t.Fatalf("summary counts = (%d, %d), want (7, 1)", candidateCount, executableCount)
	}
	for key, want := range map[string]int{
		"coverage_unresolved":  1,
		"target_render_failed": 1,
		"unknown":              1,
		"target_missing":       1,
		"already_aligned":      1,
		"target_not_absolute":  1,
	} {
		if got := reasons[key]; got != want {
			t.Fatalf("summary reasons[%q] = %d, want %d", key, got, want)
		}
	}

	// The summary report is the bounded admission view; the second call proves
	// a relative rendered target is excluded as a naming failure and never
	// counted executable.
	_, executableCount, reasons = alignmentCandidateSummary([]PlanCandidate{
		{SourcePath: "/library/relative-render.mkv", TargetPath: "relative-render-target.mkv"},
	})
	if executableCount != 0 || reasons["target_not_absolute"] != 1 {
		t.Fatalf("relative target summary = (%d, %#v), want (0, target_not_absolute=1)", executableCount, reasons)
	}
}

// Two candidates of one plan rendering the same target are routed like any
// occupant: the first claims the target, the second meets it. Under all mode
// retention keeps both, so the collision policy decides: skip leaves the
// second where it is (target_occupied) with no work item, version gives it the
// first free suffixed target. Each candidate is an active placement, as every
// scan candidate is; one without an active placement claims nothing (its
// execution settles placement_not_active).
func TestAlignmentDuplicatePlannerTargetsRouteTheSecondByPolicy(t *testing.T) {
	cases := []struct {
		policy      domain.CollisionPolicy
		secondState string
		secondCode  string
		secondName  string
		items       int
	}{
		{policy: domain.CollisionBlock, secondState: "blocked", secondCode: "target_occupied", secondName: "EP01.mkv", items: 1},
		{policy: domain.CollisionVersion, secondState: "ready", secondName: "EP01 (2).mkv", items: 2},
	}
	for _, tc := range cases {
		t.Run(string(tc.policy), func(t *testing.T) {
			fixture := newCalibrationFixture(t)
			defer fixture.db.Close()
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			seedAnimeSubscription(t, fixture.db, "anime-dupes", "sub-dupes", "stopped")
			seedAnimeForRenderer(t, fixture.db, "anime-dupes", "Show", "tv")
			seedInstanceReplacement(t, fixture.db, "anime-dupes", ModeAll, ``, 1)
			enableAlignment(t, fixture.db, "anime-dupes")
			first, second, target := filepath.Join(root, "A", "EP01.mkv"), filepath.Join(root, "B", "EP01.mkv"), filepath.Join(root, "Show", "EP01.mkv")
			seedPlacement(t, fixture.db, "plc-dupe-a", "anime-dupes", first, "fp-a", []string{"tv:e1"}, "2026-01-01T00:00:00Z")
			seedPlacement(t, fixture.db, "plc-dupe-b", "anime-dupes", second, "fp-b", []string{"tv:e1"}, "2026-01-02T00:00:00Z")
			service := NewAlignmentService(fixture.db)
			service.SetFilePorts(filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
			fixture.calibration.SetAlignmentConsumer(service)
			fixture.calibration.SetCompanionCollisionSource(fixedCollision(tc.policy))
			plan := &planner.PlanResult{AnimeID: "anime-dupes", Generation: 1, PolicyVersion: 1, Candidates: []planner.PlanCandidate{
				{ObservationRef: "obs-1", SourcePath: first, Fingerprint: "fp-a", TargetPath: target, Coverage: []string{"tv:e1"}},
				{ObservationRef: "obs-2", SourcePath: second, Fingerprint: "fp-b", TargetPath: target, Coverage: []string{"tv:e1"}},
			}}

			if _, err := fixture.calibration.admitAlignmentOperationsFor(context.Background(), "anime-dupes", 1, plan, nil, nil); err != nil {
				t.Fatal(err)
			}
			var firstState, firstTarget, secondState, secondCode, secondTarget string
			if err := fixture.db.QueryRow(`SELECT state, target_path FROM alignment_operations WHERE managed_anime_id='anime-dupes' AND source_path=$1`, first).Scan(&firstState, &firstTarget); err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.QueryRow(`SELECT state, COALESCE(failure_code, ''), target_path FROM alignment_operations WHERE managed_anime_id='anime-dupes' AND source_path=$1`, second).Scan(&secondState, &secondCode, &secondTarget); err != nil {
				t.Fatal(err)
			}
			var items int
			if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM work_item_history WHERE kind='alignment_execute' AND owner_id IN (SELECT id FROM alignment_operations WHERE managed_anime_id='anime-dupes')`).Scan(&items); err != nil {
				t.Fatal(err)
			}
			if firstState != "ready" || firstTarget != target {
				t.Fatalf("first = %s -> %s, want ready -> %s", firstState, firstTarget, target)
			}
			wantSecond := filepath.Join(root, "Show", tc.secondName)
			if secondState != tc.secondState || secondCode != tc.secondCode || secondTarget != wantSecond || items != tc.items {
				t.Fatalf("second = %s/%q -> %s, work items %d; want %s/%q -> %s, %d", secondState, secondCode, secondTarget, items, tc.secondState, tc.secondCode, wantSecond, tc.items)
			}
		})
	}
}

// TestStoppedGenerationAlignmentPlansExecutableOperations proves the
// acceptance chain: a stopped subscription still produces a completed
// generation whose alignment consumer (policy-gated, subscription-neutral)
// plans and admits executable alignment operations, while replacement
// decision/supersede/cleanup increments stay zero.
func TestStoppedGenerationAlignmentPlansExecutableOperations(t *testing.T) {
	fixture := newCalibrationFixture(t)
	defer fixture.db.Close()
	seedAnimeSubscription(t, fixture.db, "anime-align-1", "sub-1", "stopped")
	seedAnimeForRenderer(t, fixture.db, "anime-align-1", "Show", "tv")
	seedInstanceReplacement(t, fixture.db, "anime-align-1", ModeAll, ``, 1)
	enableAlignment(t, fixture.db, "anime-align-1")
	// The file sits at a non-aligned path: the naming target differs.
	seedCompletedGenerationWithRenderChains(t, fixture.db, "anime-align-1", 1, map[string]string{
		"/library/EP 01.mkv": "fp-1",
	}, rendererRules())

	// Wire the real planner + naming renderer so the alignment consumer can
	// render targets and admit executable operations. The naming template
	// renders the absolute /library target itself; no root anchor exists.
	fixture.calibration.SetAlignmentConsumer(NewAlignmentService(fixture.db))
	snapshot := NewReconciliationSnapshotReader(fixture.db)
	renderer := NewNamingTargetRenderer(fixture.db, fakeNamingRulesSource{rules: rendererRules()})
	fixture.calibration.planner = NewPlanner(snapshot, renderer)

	logger := &alignmentCapturingLogger{}
	fixture.calibration.SetLogger(logger)
	fixture.runHandler(t, fixture.calibration.CalibrateHandler(), fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	alignOutcomes := fixture.drainKind(t, fixture.calibration.ConsumeAlignmentHandler(), workitems.KindReconciliationConsumeAlignment)
	if len(alignOutcomes) != 1 || alignOutcomes[0].OwnerResult.State != "consumed" {
		t.Fatalf("stopped alignment must consume under its own policy: %+v", alignOutcomes)
	}
	if len(logger.info) != 1 {
		t.Fatalf("alignment summary events = %d, want 1", len(logger.info))
	}
	event := logger.info[0]
	if event.Message != "alignment candidate summary" || event.Component != "filemaintenance" || event.Operation != "alignment_plan" || event.Outcome != "success" || event.ReasonCode != "alignment_candidate_summary" || event.OwnerID != "anime-align-1" || event.Generation != 1 {
		t.Fatalf("unexpected alignment summary event: %+v", event)
	}
	if event.ExcludeReasonCounts == nil {
		t.Fatal("alignment summary reason map must be non-nil")
	}
	if event.CandidateCount == nil || *event.CandidateCount != 1 || event.ExecutableCount == nil || *event.ExecutableCount != 1 {
		t.Fatalf("unexpected alignment summary counts: %+v", event)
	}

	// An executable alignment operation was planned (ready) with its
	// alignment_execute claim.
	var operations, executeItems int
	if err := fixture.db.QueryRow(`
SELECT COUNT(*) FROM alignment_operations
WHERE managed_anime_id = 'anime-align-1' AND state = 'ready'`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`
SELECT COUNT(*) FROM work_item_history WHERE kind = 'alignment_execute'
AND owner_type = 'alignment_operation' AND owner_id IN (SELECT id FROM alignment_operations WHERE managed_anime_id = 'anime-align-1')`).Scan(&executeItems); err != nil {
		t.Fatal(err)
	}
	if operations != 1 || executeItems != 1 {
		t.Fatalf("stopped instance must plan one executable alignment: ops=%d items=%d", operations, executeItems)
	}
	var target string
	if err := fixture.db.QueryRow(`
SELECT target_path FROM alignment_operations WHERE managed_anime_id = 'anime-align-1'`).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if target == "/library/EP 01.mkv" {
		t.Fatalf("alignment target must differ from the observed path: %s", target)
	}
	if !filepath.IsAbs(target) {
		t.Fatalf("alignment target must be anchored absolute for the executor: %s", target)
	}
	wantTarget := "/library/Show/Season 01/Show - E01.mkv"
	if target != wantTarget {
		t.Fatalf("alignment target=%q, want %q", target, wantTarget)
	}

	// Replacement increments stay zero: no evaluate item, no decision, no
	// supersede, no cleanup admission.
	var evaluateItems, decisions, superseded, deletions int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM work_item_history WHERE kind = 'replacement_evaluate' AND owner_id = 'anime-align-1'`).Scan(&evaluateItems); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM replacement_decisions WHERE managed_anime_id = 'anime-align-1'`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM placements WHERE managed_anime_id = 'anime-align-1' AND state = 'superseded'`).Scan(&superseded); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM replacement_deletions WHERE managed_anime_id = 'anime-align-1'`).Scan(&deletions); err != nil {
		t.Fatal(err)
	}
	if evaluateItems != 0 || decisions != 0 || superseded != 0 || deletions != 0 {
		t.Fatalf("stopped replacement increments must be zero: items=%d decisions=%d superseded=%d deletions=%d",
			evaluateItems, decisions, superseded, deletions)
	}
}

// TestAlignmentConsumerReplayWithLiveExecuteItem verifies that replaying an
// alignment consumer after its checkpoint was lost treats the existing live
// alignment_execute item as idempotent success and emits one summary.
func TestAlignmentConsumerReplayWithLiveExecuteItem(t *testing.T) {
	fixture := newCalibrationFixture(t)
	defer fixture.db.Close()
	seedAnimeSubscription(t, fixture.db, "anime-align-replay", "sub-replay", "stopped")
	seedAnimeForRenderer(t, fixture.db, "anime-align-replay", "Show", "tv")
	seedInstanceReplacement(t, fixture.db, "anime-align-replay", ModeAll, ``, 1)
	enableAlignment(t, fixture.db, "anime-align-replay")
	seedCompletedGenerationWithRenderChains(t, fixture.db, "anime-align-replay", 1, map[string]string{
		"/library/EP 01.mkv": "fp-1",
	}, rendererRules())

	fixture.calibration.SetAlignmentConsumer(NewAlignmentService(fixture.db))
	snapshot := NewReconciliationSnapshotReader(fixture.db)
	renderer := NewNamingTargetRenderer(fixture.db, fakeNamingRulesSource{rules: rendererRules()})
	fixture.calibration.planner = NewPlanner(snapshot, renderer)
	logger := &alignmentCapturingLogger{}
	fixture.calibration.SetLogger(logger)

	fixture.runHandler(t, fixture.calibration.CalibrateHandler(), fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	fixture.drainKind(t, fixture.calibration.ConsumeAlignmentHandler(), workitems.KindReconciliationConsumeAlignment)
	fixture.drainKind(t, fixture.calibration.ConsumeReplacementHandler(), workitems.KindReconciliationConsumeReplacement)

	var executeItems int
	if err := fixture.db.QueryRow(`
SELECT COUNT(*) FROM work_items
WHERE kind = 'alignment_execute' AND idempotency_key = 'align:execute:' || (
	SELECT id FROM alignment_operations WHERE managed_anime_id = 'anime-align-replay')
  AND state IN ('available', 'leased')`).Scan(&executeItems); err != nil {
		t.Fatal(err)
	}
	if executeItems != 1 {
		t.Fatalf("normal admission must leave one live alignment item: %d", executeItems)
	}
	if _, err := fixture.db.Exec(`
UPDATE reconciliation_scans
SET alignment_checkpoint_state = NULL, alignment_checkpoint_generation = NULL
WHERE managed_anime_id = 'anime-align-replay' AND generation = 1`); err != nil {
		t.Fatal(err)
	}
	// Keep the replay's summary assertion independent from the first admission.
	logger.info = nil

	claim := fixture.claimDue(t, workitems.KindReconciliationConsumeAlignment)
	outcome := fixture.runHandler(t, fixture.calibration.ConsumeAlignmentHandler(), claim)
	if outcome.TerminalState != workitems.StateSucceeded || outcome.OwnerResult.State != "consumed" {
		t.Fatalf("alignment replay must terminate successfully: %+v", outcome)
	}
	if len(logger.info) != 1 {
		t.Fatalf("alignment replay summary events = %d, want 1", len(logger.info))
	}
	if err := fixture.db.QueryRow(`
SELECT COUNT(*) FROM work_items
WHERE kind = 'alignment_execute' AND idempotency_key = 'align:execute:' || (
	SELECT id FROM alignment_operations WHERE managed_anime_id = 'anime-align-replay')
  AND state IN ('available', 'leased')`).Scan(&executeItems); err != nil {
		t.Fatal(err)
	}
	if executeItems != 1 {
		t.Fatalf("alignment replay must retain one live execute item: %d", executeItems)
	}
}

// consumer emits one bounded summary even when every candidate is excluded by
// distinct planner reasons.
func TestAlignmentSummaryAggregatesPlannerExclusions(t *testing.T) {
	fixture := newCalibrationFixture(t)
	defer fixture.db.Close()
	seedAnimeSubscription(t, fixture.db, "anime-align-summary", "sub-summary", "stopped")
	seedAnimeForRenderer(t, fixture.db, "anime-align-summary", "Show", "tv")
	seedInstanceReplacement(t, fixture.db, "anime-align-summary", ModeAll, ``, 1)
	enableAlignment(t, fixture.db, "anime-align-summary")
	// The placed file freezes rules that no longer compile, so its render fails
	// with a non-missing-facts error; the other observation has no coverage.
	seedCompletedGenerationWithRenderChains(t, fixture.db, "anime-align-summary", 1, map[string]string{
		"/library/SPECIAL.mkv": "fp-unresolved",
		"/library/EP 01.mkv":   "fp-render-failed",
	}, naming.NamingRules{
		Language: naming.Language,
		TV: naming.NamingVariant{
			DirectoryTemplate:     "${anime.title_base",
			VideoFilenameTemplate: "${file.ext}",
		},
		Movie: naming.NamingVariant{
			DirectoryTemplate:     "${anime.title_base}",
			VideoFilenameTemplate: "${file.ext}",
		},
	})

	fixture.calibration.SetAlignmentConsumer(NewAlignmentService(fixture.db))
	snapshot := NewReconciliationSnapshotReader(fixture.db)
	renderer := NewNamingTargetRenderer(fixture.db, fakeNamingRulesSource{rules: rendererRules(), err: context.Canceled})
	fixture.calibration.planner = NewPlanner(snapshot, renderer)
	logger := &alignmentCapturingLogger{}
	fixture.calibration.SetLogger(logger)

	fixture.runHandler(t, fixture.calibration.CalibrateHandler(), fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	claim := fixture.claimDue(t, workitems.KindReconciliationConsumeAlignment)
	outcome := fixture.runHandler(t, fixture.calibration.ConsumeAlignmentHandler(), claim)
	if outcome.OwnerResult.State != "consumed" {
		t.Fatalf("excluded-only alignment must consume: %+v", outcome)
	}
	if len(logger.info) != 1 {
		t.Fatalf("alignment summary events = %d, want 1", len(logger.info))
	}
	event := logger.info[0]
	if event.Generation != 1 || event.CandidateCount == nil || *event.CandidateCount != 2 || event.ExecutableCount == nil || *event.ExecutableCount != 0 {
		t.Fatalf("unexpected exclusion summary counts: %+v", event)
	}
	for reason, want := range map[string]int{"coverage_unresolved": 1, "target_render_failed": 1} {
		if got := event.ExcludeReasonCounts[reason]; got != want {
			t.Fatalf("summary reasons[%q] = %d, want %d: %+v", reason, got, want, event.ExcludeReasonCounts)
		}
	}
	var operations int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM alignment_operations WHERE managed_anime_id = 'anime-align-summary'`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if operations != 0 {
		t.Fatalf("excluded candidates must not admit operations: %d", operations)
	}

	// A terminal replay must not emit a second success summary.
	if _, err := fixture.calibration.ConsumeAlignmentHandler().Handle(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if len(logger.info) != 1 {
		t.Fatalf("terminal replay summary events = %d, want 1", len(logger.info))
	}
}

// TestAlignmentAdmissionFailsClosedOnRelativeRender proves that a naming
// template that renders a relative directory fails the alignment admission
// closed — the candidate is a typed render failure and no operation is
// admitted: nothing may reach the filesystem executor as an invalid relative
// path and nothing is silently admitted.
func TestAlignmentAdmissionFailsClosedOnRelativeRender(t *testing.T) {
	fixture := newCalibrationFixture(t)
	defer fixture.db.Close()
	seedAnimeSubscription(t, fixture.db, "anime-align-2", "sub-2", "active")
	seedAnimeForRenderer(t, fixture.db, "anime-align-2", "Show", "tv")
	seedInstanceReplacement(t, fixture.db, "anime-align-2", ModeAll, ``, 1)
	enableAlignment(t, fixture.db, "anime-align-2")
	relativeRules := frozenRulesForTest()
	// The directory template renders a relative path: the engine's
	// NAMING_TARGET_NOT_ABSOLUTE rule refuses it at render time.
	relativeRules.TV.DirectoryTemplate = "${sanitize(anime.title)}/Season ${pad(tmdb.season, 2)}"
	seedCompletedGenerationWithRenderChains(t, fixture.db, "anime-align-2", 1, map[string]string{
		"/library/EP 01.mkv": "fp-1",
	}, relativeRules)

	fixture.calibration.SetAlignmentConsumer(NewAlignmentService(fixture.db))
	snapshot := NewReconciliationSnapshotReader(fixture.db)
	// The anime's current rules are the relative ones (renders read the
	// current rules, not the frozen lineage).
	renderer := NewNamingTargetRenderer(fixture.db, fakeNamingRulesSource{rules: relativeRules})
	fixture.calibration.planner = NewPlanner(snapshot, renderer)
	logger := &alignmentCapturingLogger{}
	fixture.calibration.SetLogger(logger)
	// The relative render is a naming failure: the alignment admission counts
	// only executable candidates and consumes with none admitted.

	fixture.runHandler(t, fixture.calibration.CalibrateHandler(), fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	claim := fixture.claimDue(t, workitems.KindReconciliationConsumeAlignment)
	outcome, err := fixture.calibration.ConsumeAlignmentHandler().Handle(context.Background(), claim)
	if err != nil {
		t.Fatalf("a relative render must exclude its candidate, not fail the consumer: %v", err)
	}
	if outcome.OwnerResult.State != "consumed" {
		t.Fatalf("relative-render alignment outcome = %+v, want consumed with zero candidates", outcome)
	}
	if len(logger.info) != 1 {
		t.Fatalf("summary events = %d, want the one bounded success summary", len(logger.info))
	}
	event := logger.info[0]
	if event.ExecutableCount == nil || *event.ExecutableCount != 0 || event.CandidateCount == nil || *event.CandidateCount != 1 {
		t.Fatalf("summary counts = %+v, want candidate=1 executable=0", event)
	}
	if event.ExcludeReasonCounts == nil || event.ExcludeReasonCounts["target_render_failed"] != 1 {
		t.Fatalf("summary reasons = %+v, want one target_render_failed exclusion", event.ExcludeReasonCounts)
	}
	var operations int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM alignment_operations WHERE managed_anime_id = 'anime-align-2'`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if operations != 0 {
		t.Fatalf("no operation may be admitted from a relative render: ops=%d", operations)
	}
}

// TestConsumerCheckpointIsAnchoredToItsGeneration proves the folded consumer
// checkpoint (PRUNE-046 S10): only the latest settled generation per consumer
// is kept. A checkpoint reads only for its own generation, and a late write for
// a superseded generation never overwrites a newer consumer outcome.
func TestConsumerCheckpointIsAnchoredToItsGeneration(t *testing.T) {
	fixture := newCalibrationFixture(t)
	defer fixture.db.Close()
	ctx := context.Background()
	seedAnimeSubscription(t, fixture.db, "anime-fan-3", "sub-1", "stopped")
	seedInstanceReplacement(t, fixture.db, "anime-fan-3", ModeAll, ``, 1)
	seedCompletedGeneration(t, fixture.db, "anime-fan-3", 1, map[string]string{
		"/library/Show/Season 01/EP 01.mkv": "fp-1",
	})
	fixture.runHandler(t, fixture.calibration.CalibrateHandler(), fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	alignOutcomes := fixture.drainKind(t, fixture.calibration.ConsumeAlignmentHandler(), workitems.KindReconciliationConsumeAlignment)
	if len(alignOutcomes) != 1 || alignOutcomes[0].OwnerResult.State != "blocked" {
		t.Fatalf("alignment consumer must settle generation 1 blocked: %+v", alignOutcomes)
	}
	generationOne, err := loadConsumerCheckpoint(ctx, fixture.db, "anime-fan-3", 1, ConsumerAlignment)
	if err != nil || generationOne == nil || generationOne.State != ConsumerBlocked {
		t.Fatalf("generation 1 checkpoint: %+v err=%v", generationOne, err)
	}

	// A newer completed generation advances the scan owner but not the folded
	// checkpoint: the stored outcome still belongs to generation 1.
	seedCompletedGeneration(t, fixture.db, "anime-fan-3", 2, map[string]string{
		"/library/Show/Season 01/EP 01.mkv": "fp-1",
	})
	if stale, err := loadConsumerCheckpoint(ctx, fixture.db, "anime-fan-3", 2, ConsumerAlignment); err != nil || stale != nil {
		t.Fatalf("generation 2 must not read generation 1's checkpoint: %+v err=%v", stale, err)
	}

	// Generation 2 settles, then a late write for generation 1 arrives.
	if err := settleConsumerCheckpoint(ctx, fixture.db, "anime-fan-3", 2, ConsumerAlignment, ConsumerConsumed, "alignment_consumed", "gen 2"); err != nil {
		t.Fatal(err)
	}
	if err := settleConsumerCheckpoint(ctx, fixture.db, "anime-fan-3", 1, ConsumerAlignment, ConsumerConsumed, "alignment_consumed", "late gen 1"); err != nil {
		t.Fatal(err)
	}
	generationTwo, err := loadConsumerCheckpoint(ctx, fixture.db, "anime-fan-3", 2, ConsumerAlignment)
	if err != nil || generationTwo == nil || generationTwo.State != ConsumerConsumed {
		t.Fatalf("stale write must not overwrite the newer outcome: %+v err=%v", generationTwo, err)
	}
	if generationOne, err := loadConsumerCheckpoint(ctx, fixture.db, "anime-fan-3", 1, ConsumerAlignment); err != nil || generationOne != nil {
		t.Fatalf("the generation anchor must move to the newest settle: %+v err=%v", generationOne, err)
	}
}
