package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bansoko/internal/filemaintenance/planner"
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/library/domain"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

// offsetFixture is anime-1 (active, openFrozenTestDB) with real files under
// a temp root: retention wired to the cleanup lane that deletes its losers,
// the alignment admission and executor with real file ports, and the scan
// scheduler's request dispatch with the real activity writer.
type offsetFixture struct {
	db          *sql.DB
	root        string
	store       *workitems.Store
	cleanup     *CleanupService
	alignment   *AlignmentService
	calibration *CalibrationService
	scheduler   *ScanScheduler
}

type fixedCollision domain.CollisionPolicy

func (c fixedCollision) EffectiveCollisionPolicy(context.Context, string) (string, error) {
	return string(c), nil
}

const offsetFixtureProfile = `{"version":"profile-v1","items":[{"key":"release.resolution","ranks":[["2160p"],["1080p"],["720p"],["480p"]]}]}`

func newOffsetFixture(t *testing.T, mode AdmissionMode, collision domain.CollisionPolicy) *offsetFixture {
	t.Helper()
	db := openFrozenTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedInstanceReplacement(t, db, "anime-1", mode, offsetFixtureProfile, 1)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := cleanupWorkItemStore(t, db)
	cleanup := NewCleanupService(db)
	cleanup.SetFileExecutor(filesystem.NewOSCleanupExecutor(root))
	cleanup.SetWorkItems(store)
	retention := NewReplacementDecisionService(db)
	retention.SetCleanupHandoff(cleanup)
	alignment := NewAlignmentService(db)
	alignment.SetFilePorts(filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	alignment.SetRetention(retention)
	calibration := NewCalibrationService(db, NewPlacementService(db, nil, nil), NewPolicyService(db), nil, store)
	calibration.SetAlignmentConsumer(alignment)
	calibration.SetCompanionCollisionSource(fixedCollision(collision))
	activity, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewScanScheduler(db, nil, nil, nil, nil)
	scheduler.SetAlignmentRequests(retention, activity)
	return &offsetFixture{db: db, root: root, store: store, cleanup: cleanup, alignment: alignment, calibration: calibration, scheduler: scheduler}
}

// offsetFile is one seeded placed file.
type offsetFile struct {
	placementID, path, fingerprint string
	content                        []byte
}

// seedFile seeds a verified placed file of anime-1 on its own link
// (src-<release>) at root/rel: the release chain retention scores, the file's
// metadata fingerprint as its recorded identity, and — when raw is set — the
// frozen identity (raw episode raw) an offset re-resolves.
func (f *offsetFixture) seedFile(t *testing.T, animeID, subID, releaseID, rel, coverage, raw string, facts Facts, createdAt string) offsetFile {
	t.Helper()
	ctx := context.Background()
	placementID := seedFilePlacement(t, f.db, animeID, subID, releaseID, []string{coverage}, facts, createdAt)
	file := offsetFile{placementID: placementID, path: filepath.Join(f.root, rel), content: []byte("video of " + releaseID)}
	if err := os.MkdirAll(filepath.Dir(file.path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file.path, file.content, 0o644); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := (filesystem.OSFileObserver{}).ObserveMetadata(ctx, file.path)
	if err != nil {
		t.Fatal(err)
	}
	file.fingerprint = fingerprint
	if _, err := f.db.Exec(`UPDATE placements SET physical_path = $2, source_fingerprint = $3, target_fingerprint = $3 WHERE id = $1`, placementID, file.path, fingerprint); err != nil {
		t.Fatal(err)
	}
	if raw != "" {
		owner := disposabledb.Owner(t)
		if _, err := owner.Exec(`
INSERT INTO media_fact_snapshots(id, release_id, schema_version, resolver_version, parser_kind, input_digest, release_identity, release_facts)
VALUES ($1, $2, 'media-facts-v1', 'episode-identity-v1', 'regex', $3, '{}'::jsonb, '{}'::jsonb)`,
			"snap-"+releaseID, releaseID, "digest-"+releaseID); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.Exec(`
INSERT INTO media_fact_files(snapshot_id, file_ref, media_kind, size_bytes, file_evidence, effective_facts)
VALUES ($1, 'ep.mkv', 'video', $2, '{}'::jsonb, $3::jsonb)`,
			"snap-"+releaseID, len(file.content), string(frozenIdentityFacts(t, "tv_episode", raw))); err != nil {
			t.Fatal(err)
		}
	}
	return file
}

// changeOffset changes the link's offset the way the link owner does: the
// version moves and the offset alignment re-keys in the same transaction.
func (f *offsetFixture) changeOffset(t *testing.T, sourceID string, oldOffset, newOffset int) (string, bool) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var version int64
	if err := tx.QueryRowContext(ctx, `UPDATE rss_sources SET episode_offset = $2, version = version + 1 WHERE id = $1 RETURNING version`, sourceID, newOffset).Scan(&version); err != nil {
		t.Fatal(err)
	}
	changeID, created, err := NewOffsetAlignmentService().AdmitRSSSourceTx(ctx, txscope.Wrap(tx), "anime-1", sourceID, version, oldOffset, newOffset)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return changeID, created
}

// expectPlacement checks the placement's key and path, and its version when
// version > 0 (a move bumps it once more).
func (f *offsetFixture) expectPlacement(t *testing.T, placementID, key string, version int64, path string) {
	t.Helper()
	var gotKey, gotPath string
	var gotVersion int64
	if err := f.db.QueryRow(`
SELECT (SELECT string_agg(observation_key, ',' ORDER BY observation_key) FROM placement_coverage WHERE placement_id = p.id), p.version, p.physical_path
FROM placements p WHERE p.id = $1`, placementID).Scan(&gotKey, &gotVersion, &gotPath); err != nil {
		t.Fatal(err)
	}
	if gotKey != key || (version > 0 && gotVersion != version) || gotPath != path {
		t.Fatalf("placement %s key=%s version=%d path=%s, want %s %d %s", placementID, gotKey, gotVersion, gotPath, key, version, path)
	}
}

func (f *offsetFixture) changeState(t *testing.T, changeID string) (state string, rekeyed, deleted int) {
	t.Helper()
	if err := f.db.QueryRow(`SELECT state, rekeyed_count, deleted_count FROM offset_alignment_changes WHERE id = $1`, changeID).Scan(&state, &rekeyed, &deleted); err != nil {
		t.Fatal(err)
	}
	return
}

// startScan records the scan whose alignment covers the requests made
// before it.
func (f *offsetFixture) startScan(t *testing.T, generation int64) {
	t.Helper()
	seedCompletedGeneration(t, f.db, "anime-1", generation, map[string]string{})
}

// admit runs the alignment admission of one scan over plan, as the alignment
// consumer does: only requested paths when the setting is off, the routes
// resolving the requests.
func (f *offsetFixture) admit(t *testing.T, generation int64, alignAll bool, candidates ...planner.PlanCandidate) map[string]alignmentRoute {
	t.Helper()
	ctx := context.Background()
	requests, err := requestsBeforeScan(ctx, f.db, "anime-1", generation)
	if err != nil {
		t.Fatal(err)
	}
	var include func(planner.PlanCandidate) bool
	if !alignAll {
		paths := requestedPaths(requests)
		include = func(candidate planner.PlanCandidate) bool { return paths[filepath.Clean(candidate.SourcePath)] }
	}
	plan := &planner.PlanResult{AnimeID: "anime-1", Generation: generation, PolicyVersion: 1, Candidates: candidates}
	routes := map[string]alignmentRoute{}
	if _, err := f.calibration.admitAlignmentOperationsFor(ctx, "anime-1", generation, plan, include, routes); err != nil {
		t.Fatal(err)
	}
	if err := resolveAlignmentRequests(ctx, f.db, requests, routes); err != nil {
		t.Fatal(err)
	}
	return routes
}

func moveCandidate(file offsetFile, target, key string) planner.PlanCandidate {
	return planner.PlanCandidate{ObservationRef: "obs-" + file.placementID, SourcePath: file.path, Fingerprint: file.fingerprint, TargetPath: target, Coverage: []string{key}}
}

// runDue claims and runs every due item of kind to its finish.
func (f *offsetFixture) runDue(t *testing.T, kind string, handler worker.Handler) int {
	t.Helper()
	ctx := context.Background()
	ran := 0
	for {
		claim, err := f.store.ClaimDue(ctx, "offset-test", []string{kind}, time.Minute)
		if errors.Is(err, workitems.ErrNoDueWork) {
			return ran
		}
		if err != nil {
			t.Fatal(err)
		}
		outcome, err := handler.Handle(ctx, claim)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if outcome.Kind != worker.OutcomeFinish {
			t.Fatalf("%s outcome %+v, want a finish", kind, outcome)
		}
		if err := f.store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
			t.Fatal(err)
		}
		ran++
	}
}

func (f *offsetFixture) operation(t *testing.T, sourcePath string) (state, code, target string) {
	t.Helper()
	if err := f.db.QueryRow(`SELECT state, COALESCE(failure_code, ''), target_path FROM alignment_operations WHERE source_path = $1 AND companion_id IS NULL`, sourcePath).Scan(&state, &code, &target); err != nil {
		t.Fatal(err)
	}
	return
}

func (f *offsetFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// offsetRecord is the one activity entry of the link's offset change.
func (f *offsetFixture) offsetRecord(t *testing.T, sourceID string) map[string]any {
	t.Helper()
	var raw []byte
	if err := f.db.QueryRow(`
SELECT message_params FROM subscription_log_entries
WHERE kind = 'placement_changed' AND message_code = 'offset_aligned' AND subject_id = $1`, sourceID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	params := map[string]any{}
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatal(err)
	}
	return params
}

func expectCounts(t *testing.T, params map[string]any, group string, rekeyed, deleted, versioned, overwritten, skipped float64) {
	t.Helper()
	want := map[string]any{"group_name": group, "rekeyed": rekeyed, "deleted": deleted, "versioned": versioned, "overwritten": overwritten, "skipped": skipped}
	for key, value := range want {
		if params[key] != value {
			t.Fatalf("offset record %v, want %v", params, want)
		}
	}
}

func assertFileContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(want) {
		t.Fatalf("%s = %q err=%v, want %q", path, got, err, want)
	}
}

func assertGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("%s must be gone: %v", path, err)
	}
}

// ---------------------------------------------------------------------------
// Offset change: re-key in the link owner's transaction
// ---------------------------------------------------------------------------

// The offset transaction re-keys the link's single-episode files, touches no
// file and requests their alignment; another link keeps its keys, a replay of
// the link version re-keys nothing twice, and a link with nothing to re-key
// records its change at once.
func TestPGOffsetChangeRekeysTheLinkFiles(t *testing.T) {
	f := newOffsetFixture(t, ModePerGroup, domain.CollisionBlock)
	moved := f.seedFile(t, "anime-1", "sub-1", "rel-moved", "Show/ep1.mkv", "tv:e1", "1", Facts{Resolution: "1080p", Group: "B"}, "2026-07-01T00:00:00Z")
	other := f.seedFile(t, "anime-1", "sub-1", "rel-other", "Show/other1.mkv", "tv:e1", "1", Facts{Resolution: "1080p", Group: "A"}, "2026-07-01T00:00:00Z")
	noFacts := f.seedFile(t, "anime-1", "sub-1", "rel-nofacts", "Show/nofacts.mkv", "tv:e3", "", Facts{Resolution: "1080p", Group: "C"}, "2026-07-01T00:00:00Z")

	changeID, created := f.changeOffset(t, "src-rel-moved", 0, 1)
	if !created {
		t.Fatal("the first offset change of the link version must be recorded")
	}
	f.expectPlacement(t, moved.placementID, "tv:e2", 2, moved.path)
	f.expectPlacement(t, other.placementID, "tv:e1", 1, other.path)
	assertFileContent(t, moved.path, moved.content)
	if state, rekeyed, _ := f.changeState(t, changeID); state != offsetChangeComparing || rekeyed != 1 {
		t.Fatalf("change state=%s rekeyed=%d, want comparing with 1", state, rekeyed)
	}
	if n := f.count(t, `SELECT count(*) FROM alignment_requests WHERE placement_id = $1 AND offset_change_id = $2 AND outcome IS NULL`, moved.placementID, changeID); n != 1 {
		t.Fatalf("the re-keyed file must be requested for alignment with its change, got %d", n)
	}

	// A replay of the same link version is the change already recorded.
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := tx.QueryRow(`SELECT version FROM rss_sources WHERE id = 'src-rel-moved'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	replayID, replayCreated, err := NewOffsetAlignmentService().AdmitRSSSourceTx(context.Background(), txscope.Wrap(tx), "anime-1", "src-rel-moved", version, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if replayID != changeID || replayCreated {
		t.Fatalf("replay id=%s created=%v, want %s false", replayID, replayCreated, changeID)
	}
	f.expectPlacement(t, moved.placementID, "tv:e2", 2, moved.path)

	// A link whose file has no frozen identity re-keys nothing.
	emptyID, _ := f.changeOffset(t, "src-rel-nofacts", 0, 1)
	if state, rekeyed, _ := f.changeState(t, emptyID); state != offsetChangeRecorded || rekeyed != 0 {
		t.Fatalf("an empty change state=%s rekeyed=%d, want recorded with 0", state, rekeyed)
	}
	f.expectPlacement(t, noFacts.placementID, "tv:e3", 1, noFacts.path)
	if n := f.count(t, `SELECT count(*) FROM alignment_requests WHERE offset_change_id = $1`, emptyID); n != 0 {
		t.Fatalf("an empty change requests nothing, got %d", n)
	}
}

// single_copy: the re-keyed 1080p episode lands on the key of a 720p file of
// another link that sits at the rendered target. Retention compares the keys
// first and deletes the 720p file through the retention cleanup; the scan
// then moves the winner onto the freed target and the change's one record
// counts one re-keyed and one deleted file.
func TestPGOffsetChangeCompetesThenTheWinnerMoves(t *testing.T) {
	f := newOffsetFixture(t, ModeSingleCopy, domain.CollisionBlock)
	ctx := context.Background()
	incumbent := f.seedFile(t, "anime-1", "sub-1", "rel-incumbent", "Show/Season 01/Show - E02.mkv", "tv:e2", "2", Facts{Resolution: "720p", Group: "A"}, "2026-07-01T00:00:00Z")
	mover := f.seedFile(t, "anime-1", "sub-1", "rel-mover", "Show/Season 01/Show - E01.mkv", "tv:e1", "1", Facts{Resolution: "1080p", Group: "B"}, "2026-08-01T00:00:00Z")

	changeID, _ := f.changeOffset(t, "src-rel-mover", 0, 1)
	f.expectPlacement(t, mover.placementID, "tv:e2", 2, mover.path)

	if err := f.scheduler.compareOffsetChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := f.changeState(t, changeID); state != offsetChangeAligning {
		t.Fatalf("change state=%s after the comparison, want aligning", state)
	}
	if state := placementState(t, f.db, incumbent.placementID); state != "superseded" {
		t.Fatalf("the 720p file must lose retention, state=%s", state)
	}
	if state := placementState(t, f.db, mover.placementID); state != "active" {
		t.Fatalf("the re-keyed winner must stay active, state=%s", state)
	}
	if n := f.runDue(t, workitems.KindReplacementDelete, f.cleanup.DeleteHandler()); n != 1 {
		t.Fatalf("one retention deletion must run, ran %d", n)
	}
	assertGone(t, incumbent.path)

	f.startScan(t, 1)
	routes := f.admit(t, 1, false, moveCandidate(mover, incumbent.path, "tv:e2"))
	if route := routes[mover.placementID]; route.Action != routeProceed {
		t.Fatalf("the winner's route=%+v, want proceed onto the freed target", route)
	}
	if n := f.runDue(t, workitems.KindAlignmentExecute, f.alignment.ExecuteHandler()); n != 1 {
		t.Fatalf("one alignment must run, ran %d", n)
	}
	f.expectPlacement(t, mover.placementID, "tv:e2", 0, incumbent.path)
	assertFileContent(t, incumbent.path, mover.content)
	assertGone(t, mover.path)

	if err := f.scheduler.recordOffsetChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if state, rekeyed, deleted := f.changeState(t, changeID); state != offsetChangeRecorded || rekeyed != 1 || deleted != 1 {
		t.Fatalf("change state=%s rekeyed=%d deleted=%d, want recorded 1 1", state, rekeyed, deleted)
	}
	expectCounts(t, f.offsetRecord(t, "src-rel-mover"), "B", 1, 1, 0, 0, 0)
	if n := f.count(t, `SELECT count(*) FROM alignment_requests WHERE offset_change_id = $1`, changeID); n != 0 {
		t.Fatalf("a recorded change keeps no requests, got %d", n)
	}
}

// all + version: nothing is compared, the re-keyed file meets the other
// link's file at its target and takes a version number; the record counts it.
func TestPGOffsetChangeUnderAllTakesAVersionNumber(t *testing.T) {
	f := newOffsetFixture(t, ModeAll, domain.CollisionVersion)
	ctx := context.Background()
	other := f.seedFile(t, "anime-1", "sub-1", "rel-other", "Show/Season 01/Show - E02.mkv", "tv:e2", "2", Facts{Resolution: "720p", Group: "A"}, "2026-07-01T00:00:00Z")
	mover := f.seedFile(t, "anime-1", "sub-1", "rel-mover", "Show/Season 01/Show - E01.mkv", "tv:e1", "1", Facts{Resolution: "1080p", Group: "B"}, "2026-08-01T00:00:00Z")

	changeID, _ := f.changeOffset(t, "src-rel-mover", 0, 1)
	if err := f.scheduler.compareOffsetChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM replacement_decisions WHERE managed_anime_id = 'anime-1'`); n != 0 {
		t.Fatalf("all compares nothing, got %d decisions", n)
	}
	f.startScan(t, 1)
	routes := f.admit(t, 1, false, moveCandidate(mover, other.path, "tv:e2"))
	if route := routes[mover.placementID]; route.Action != routeVersion {
		t.Fatalf("route=%+v, want version", route)
	}
	f.runDue(t, workitems.KindAlignmentExecute, f.alignment.ExecuteHandler())
	versioned := alignmentVersionedPath(other.path, 2)
	f.expectPlacement(t, mover.placementID, "tv:e2", 0, versioned)
	assertFileContent(t, versioned, mover.content)
	assertFileContent(t, other.path, other.content)

	if err := f.scheduler.recordOffsetChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if state, _, deleted := f.changeState(t, changeID); state != offsetChangeRecorded || deleted != 0 {
		t.Fatalf("change state=%s deleted=%d, want recorded with nothing deleted", state, deleted)
	}
	expectCounts(t, f.offsetRecord(t, "src-rel-mover"), "B", 1, 0, 1, 0, 0)
}

// ---------------------------------------------------------------------------
// Alignment routing at a real target: retention first, then the policy
// ---------------------------------------------------------------------------

// single_copy: the moving 1080p file meets a same-key 720p file at its
// target. Retention compares them (the 720p file is superseded and its
// deletion admitted); the move waits with replacement_pending while the
// deletion is in flight, then moves onto the freed target.
func TestPGAlignmentRoutingWaitsForRetentionThenTheKeeperMoves(t *testing.T) {
	f := newOffsetFixture(t, ModeSingleCopy, domain.CollisionBlock)
	ctx := context.Background()
	occupant := f.seedFile(t, "anime-1", "sub-1", "rel-occupant", "Show/Season 01/Show - E01.mkv", "tv:e1", "", Facts{Resolution: "720p", Group: "A"}, "2026-07-01T00:00:00Z")
	mover := f.seedFile(t, "anime-1", "sub-1", "rel-mover", "Incoming/ep1.mkv", "tv:e1", "", Facts{Resolution: "1080p", Group: "B"}, "2026-08-01T00:00:00Z")

	f.startScan(t, 1)
	routes := f.admit(t, 1, true, moveCandidate(mover, occupant.path, "tv:e1"))
	if route := routes[mover.placementID]; route.Action != routeHold || route.Reason != ReasonReplacementPending || route.Settle {
		t.Fatalf("route=%+v, want a replacement_pending hold", route)
	}
	if state := placementState(t, f.db, occupant.placementID); state != "superseded" {
		t.Fatalf("the 720p occupant must lose retention, state=%s", state)
	}

	claim, err := f.store.ClaimDue(ctx, "offset-test", []string{workitems.KindAlignmentExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := f.alignment.ExecuteHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Kind != worker.OutcomeRetry || waiting.ReasonCode != ReasonReplacementPending {
		t.Fatalf("outcome=%+v, want a replacement_pending wait while the loser's deletion is in flight", waiting)
	}
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waiting.WriteOwner(ctx, txscope.Wrap(tx), waiting.OwnerResult); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if state, code, _ := f.operation(t, mover.path); state != string(AlignmentReady) || code != ReasonReplacementPending {
		t.Fatalf("operation state=%s code=%s, want ready waiting with replacement_pending", state, code)
	}
	assertFileContent(t, mover.path, mover.content)

	if n := f.runDue(t, workitems.KindReplacementDelete, f.cleanup.DeleteHandler()); n != 1 {
		t.Fatalf("one retention deletion must run, ran %d", n)
	}
	moved, err := f.alignment.ExecuteHandler().Handle(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.FinishWithResult(ctx, claim, moved.TerminalState, moved.OwnerResult, moved.WriteOwner); err != nil {
		t.Fatal(err)
	}
	f.expectPlacement(t, mover.placementID, "tv:e1", 0, occupant.path)
	assertFileContent(t, occupant.path, mover.content)
	assertGone(t, mover.path)
}

// all: retention keeps both same-key files, so the collision policy decides
// at the target — overwrite replaces the occupant (displaced), skip leaves
// the moving file where it is.
func TestPGAlignmentRoutingUnderAllFollowsTheCollisionPolicy(t *testing.T) {
	t.Run("overwrite", func(t *testing.T) {
		f := newOffsetFixture(t, ModeAll, domain.CollisionOverwrite)
		occupant := f.seedFile(t, "anime-1", "sub-1", "rel-occupant", "Show/Season 01/Show - E01.mkv", "tv:e1", "", Facts{Resolution: "720p", Group: "A"}, "2026-07-01T00:00:00Z")
		mover := f.seedFile(t, "anime-1", "sub-1", "rel-mover", "Incoming/ep1.mkv", "tv:e1", "", Facts{Resolution: "1080p", Group: "B"}, "2026-08-01T00:00:00Z")
		f.startScan(t, 1)
		if route := f.admit(t, 1, true, moveCandidate(mover, occupant.path, "tv:e1"))[mover.placementID]; route.Action != routeOverwrite {
			t.Fatalf("route=%+v, want overwrite", route)
		}
		f.runDue(t, workitems.KindAlignmentExecute, f.alignment.ExecuteHandler())
		f.expectPlacement(t, mover.placementID, "tv:e1", 0, occupant.path)
		assertFileContent(t, occupant.path, mover.content)
		var state, cause, successor string
		if err := f.db.QueryRow(`SELECT state, COALESCE(displaced_cause, ''), COALESCE(displaced_by_placement_id, '') FROM placements WHERE id = $1`, occupant.placementID).Scan(&state, &cause, &successor); err != nil {
			t.Fatal(err)
		}
		if state != "displaced" || cause != "overwrite" || successor != mover.placementID {
			t.Fatalf("occupant state=%s cause=%s successor=%s, want displaced by the moved file", state, cause, successor)
		}
		if n := f.count(t, `SELECT count(*) FROM replacement_deletions WHERE managed_anime_id = 'anime-1'`); n != 0 {
			t.Fatalf("all deletes nothing through retention, got %d", n)
		}
	})
	t.Run("skip", func(t *testing.T) {
		f := newOffsetFixture(t, ModeAll, domain.CollisionBlock)
		occupant := f.seedFile(t, "anime-1", "sub-1", "rel-occupant", "Show/Season 01/Show - E01.mkv", "tv:e1", "", Facts{Resolution: "720p", Group: "A"}, "2026-07-01T00:00:00Z")
		mover := f.seedFile(t, "anime-1", "sub-1", "rel-mover", "Incoming/ep1.mkv", "tv:e1", "", Facts{Resolution: "1080p", Group: "B"}, "2026-08-01T00:00:00Z")
		f.startScan(t, 1)
		if route := f.admit(t, 1, true, moveCandidate(mover, occupant.path, "tv:e1"))[mover.placementID]; route.Action != routeSkip {
			t.Fatalf("route=%+v, want skip", route)
		}
		if state, code, _ := f.operation(t, mover.path); state != string(AlignmentBlocked) || code != "target_occupied" {
			t.Fatalf("operation state=%s code=%s, want blocked target_occupied", state, code)
		}
		if n := f.runDue(t, workitems.KindAlignmentExecute, f.alignment.ExecuteHandler()); n != 0 {
			t.Fatalf("a skipped move runs nothing, ran %d", n)
		}
		assertFileContent(t, mover.path, mover.content)
		assertFileContent(t, occupant.path, occupant.content)
	})
}

// A file of another instance at the target is never compared: even under
// single_copy the collision policy decides (version here).
func TestPGAlignmentRoutingAnotherInstanceFollowsThePolicy(t *testing.T) {
	f := newOffsetFixture(t, ModeSingleCopy, domain.CollisionVersion)
	seedAnimeSubscription(t, f.db, "anime-2", "sub-2", "active")
	seedInstanceReplacement(t, f.db, "anime-2", ModeSingleCopy, offsetFixtureProfile, 1)
	foreign := f.seedFile(t, "anime-2", "sub-2", "rel-foreign", "Show/Season 01/Show - E01.mkv", "tv:e1", "", Facts{Resolution: "480p", Group: "A"}, "2026-07-01T00:00:00Z")
	mover := f.seedFile(t, "anime-1", "sub-1", "rel-mover", "Incoming/ep1.mkv", "tv:e1", "", Facts{Resolution: "1080p", Group: "B"}, "2026-08-01T00:00:00Z")
	f.startScan(t, 1)
	if route := f.admit(t, 1, true, moveCandidate(mover, foreign.path, "tv:e1"))[mover.placementID]; route.Action != routeVersion {
		t.Fatalf("route=%+v, want version", route)
	}
	f.runDue(t, workitems.KindAlignmentExecute, f.alignment.ExecuteHandler())
	versioned := alignmentVersionedPath(foreign.path, 2)
	f.expectPlacement(t, mover.placementID, "tv:e1", 0, versioned)
	assertFileContent(t, foreign.path, foreign.content)
	if state := placementState(t, f.db, foreign.placementID); state != "active" {
		t.Fatalf("the other instance's file must stay active, state=%s", state)
	}
	if n := f.count(t, `SELECT count(*) FROM replacement_decisions`); n != 0 {
		t.Fatalf("another instance's file is never compared, got %d decisions", n)
	}
}

// ---------------------------------------------------------------------------
// Request dispatch: scans for active instances once the keys are compared
// ---------------------------------------------------------------------------

// A comparing change's request waits for the comparison; once compared, the
// tick submits one scan for the active instance. A stopped instance's
// request is neither compared nor scanned.
func TestPGAlignmentRequestsAreScannedOnceCompared(t *testing.T) {
	f := newOffsetFixture(t, ModePerGroup, domain.CollisionBlock)
	ctx := context.Background()
	f.seedFile(t, "anime-1", "sub-1", "rel-mover", "Show/ep1.mkv", "tv:e1", "1", Facts{Resolution: "1080p", Group: "B"}, "2026-07-01T00:00:00Z")
	seedAnimeSubscription(t, f.db, "anime-stopped", "sub-stopped", "stopped")
	stopped := f.seedFile(t, "anime-stopped", "sub-stopped", "rel-stopped", "Stopped/ep1.mkv", "tv:e1", "", Facts{Resolution: "1080p", Group: "B"}, "2026-07-01T00:00:00Z")
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := requestAlignmentTx(ctx, tx, stopped.placementID, ""); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	changeID, _ := f.changeOffset(t, "src-rel-mover", 0, 1)

	submitter := &fakeSubmitter{}
	f.scheduler.submitter = submitter
	retention := f.scheduler.retention
	f.scheduler.retention = nil
	if err := f.scheduler.dispatchAlignmentRequests(ctx); err != nil {
		t.Fatal(err)
	}
	if submitter.calls["anime-1"] != 0 || submitter.calls["anime-stopped"] != 0 {
		t.Fatalf("nothing is scanned before the comparison: %v", submitter.calls)
	}
	f.scheduler.retention = retention
	if err := f.scheduler.dispatchAlignmentRequests(ctx); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := f.changeState(t, changeID); state != offsetChangeAligning {
		t.Fatalf("change state=%s, want aligning", state)
	}
	if submitter.calls["anime-1"] != 1 || submitter.calls["anime-stopped"] != 0 {
		t.Fatalf("one scan for the active instance only: %v", submitter.calls)
	}
	if top := submitter.tops["anime-1"]; top == "" {
		t.Fatal("the requested scan must carry the instance's tree top")
	}
}

// ---------------------------------------------------------------------------
// Post-import correction
// ---------------------------------------------------------------------------

// A new file-operation placement is requested for alignment in its own
// transaction; a replay of the admission does not request it again.
func TestPGImportedPlacementRequestsAlignment(t *testing.T) {
	db := openPlacementTestDB(t)
	defer db.Close()
	reader := &fakeFileOpReader{found: true, evidence: &FileOperationEvidence{
		OperationID: "op-import", ManagedAnimeID: "anime-1", ArtifactVerified: true, TargetPath: "/library/Show/EP01.mkv",
		SourceFingerprint: "fp-src", TargetFingerprint: "fp-src",
	}}
	service := NewPlacementService(db, reader, nil)
	ctx := context.Background()
	placementID, err := service.AdmitFromFileOperation(ctx, "op-import", []string{"tv:e1"}, "facts-v1")
	if err != nil {
		t.Fatal(err)
	}
	var requests int
	if err := db.QueryRow(`SELECT count(*) FROM alignment_requests WHERE placement_id = $1 AND offset_change_id IS NULL AND outcome IS NULL`, placementID).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("a new import must be requested for alignment, got %d", requests)
	}
	if _, err := db.Exec(`DELETE FROM alignment_requests WHERE placement_id = $1`, placementID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AdmitFromFileOperation(ctx, "op-import", []string{"tv:e1"}, "facts-v1"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM alignment_requests`).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if requests != 0 {
		t.Fatalf("a replayed admission must not request the placement again, got %d", requests)
	}
}

// postImportFixture: an instance with alignment disabled and two files at
// misaligned paths, the real planner and naming renderer wired.
func postImportFixture(t *testing.T) *calibrationFixture {
	t.Helper()
	fixture := newCalibrationFixture(t)
	seedAnimeSubscription(t, fixture.db, "anime-post", "sub-post", "active")
	seedAnimeForRenderer(t, fixture.db, "anime-post", "Show", "tv")
	seedInstanceReplacement(t, fixture.db, "anime-post", ModeAll, ``, 1)
	seedCompletedGenerationWithRenderChains(t, fixture.db, "anime-post", 1, map[string]string{
		"/library/EP 01.mkv": "fp-1",
		"/library/EP 02.mkv": "fp-2",
	}, rendererRules())
	// The observations carry the placements' own bytes: only the path differs.
	if _, err := fixture.db.Exec(`
UPDATE reconciliation_files rf SET fingerprint = (p.source_fingerprint::jsonb)->>'value'
FROM placements p
WHERE p.id = rf.facts->>'placement_id' AND rf.managed_anime_id = 'anime-post'`); err != nil {
		t.Fatal(err)
	}
	fixture.calibration.SetAlignmentConsumer(NewAlignmentService(fixture.db))
	snapshot := NewReconciliationSnapshotReader(fixture.db)
	renderer := NewNamingTargetRenderer(fixture.db, fakeNamingRulesSource{rules: rendererRules()})
	fixture.calibration.planner = NewPlanner(snapshot, renderer)
	return fixture
}

// With the alignment setting off, the scan after an import aligns the newly
// imported file (its request) and leaves the file already in place alone;
// the request is resolved.
func TestPostImportRequestAlignsWhileAlignmentIsDisabled(t *testing.T) {
	fixture := postImportFixture(t)
	defer fixture.db.Close()
	var placementID string
	if err := fixture.db.QueryRow(`SELECT id FROM placements WHERE managed_anime_id = 'anime-post' AND physical_path = '/library/EP 01.mkv'`).Scan(&placementID); err != nil {
		t.Fatal(err)
	}
	// The import's request predates the scan.
	if _, err := fixture.db.Exec(`INSERT INTO alignment_requests(placement_id, requested_at) VALUES ($1, CURRENT_TIMESTAMP - interval '1 hour')`, placementID); err != nil {
		t.Fatal(err)
	}
	fixture.runHandler(t, fixture.calibration.CalibrateHandler(), fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	outcomes := fixture.drainKind(t, fixture.calibration.ConsumeAlignmentHandler(), workitems.KindReconciliationConsumeAlignment)
	if len(outcomes) != 1 || outcomes[0].OwnerResult.State != "consumed" {
		t.Fatalf("a requested import aligns with the setting off: %+v", outcomes)
	}
	rows, err := fixture.db.Query(`SELECT source_path, target_path FROM alignment_operations WHERE managed_anime_id = 'anime-post' AND companion_id IS NULL`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var operations []string
	for rows.Next() {
		var source, target string
		if err := rows.Scan(&source, &target); err != nil {
			t.Fatal(err)
		}
		operations = append(operations, source+" -> "+target)
	}
	if len(operations) != 1 || operations[0] != "/library/EP 01.mkv -> /library/Show/Season 01/Show - E01.mkv" {
		t.Fatalf("operations=%v, want only the imported file aligned", operations)
	}
	var requests int
	if err := fixture.db.QueryRow(`SELECT count(*) FROM alignment_requests`).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if requests != 0 {
		t.Fatalf("the scan resolves the import's request, %d left", requests)
	}
}

// Without a request the setting gates every file: the consumer stays
// blocked and nothing moves.
func TestAlignmentDisabledWithoutRequestsStaysBlocked(t *testing.T) {
	fixture := postImportFixture(t)
	defer fixture.db.Close()
	fixture.runHandler(t, fixture.calibration.CalibrateHandler(), fixture.claimDue(t, workitems.KindReconciliationCalibrate))
	outcomes := fixture.drainKind(t, fixture.calibration.ConsumeAlignmentHandler(), workitems.KindReconciliationConsumeAlignment)
	if len(outcomes) != 1 || outcomes[0].OwnerResult.State != "blocked" || outcomes[0].OwnerResult.ReasonCode != "alignment_disabled" {
		t.Fatalf("alignment off without a request must stay blocked: %+v", outcomes)
	}
	var operations int
	if err := fixture.db.QueryRow(`SELECT count(*) FROM alignment_operations WHERE managed_anime_id = 'anime-post'`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if operations != 0 {
		t.Fatalf("nothing moves with the setting off, got %d operations", operations)
	}
}
