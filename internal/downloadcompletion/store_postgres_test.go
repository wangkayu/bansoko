package downloadcompletion

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"testing"
	"time"

	"bansoko/internal/batchplan"
	"bansoko/internal/release/facts"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

const (
	completionSource = "source-completion"
	hashA            = "1111111111111111111111111111111111111111"
	hashB            = "2222222222222222222222222222222222222222"
)

type fakeSnapshots map[string]string

func (f fakeSnapshots) ReadSnapshot(_ context.Context, releaseID string) (*facts.Snapshot, error) {
	action, ok := f[releaseID]
	if !ok {
		return nil, nil
	}
	return &facts.Snapshot{ReleaseID: releaseID, ReleaseFacts: facts.ReleaseFacts{BatchPlan: &batchplan.Plan{DownloadCompletion: action}}}, nil
}

func correlationJSON(hash string) string {
	return fmt.Sprintf(`{"external_id":"hash:%[1]s","info_hash":"%[1]s","files_complete":true,"path_snapshot":{"remote_root":"/downloads","application_root":"/downloads"}}`, hash)
}

// seedRoot creates one download root of the shared subscription chain, with
// the given observed correlation, and settles it to the given state by the
// owner (the pass reads the final state; the settler itself is not under test).
// It opens completion_open as acquisition_write_terminal does for a succeeded
// download root.
func seedRoot(t *testing.T, owner *sql.DB, name, correlation, state string) (root, release string) {
	t.Helper()
	ctx := context.Background()
	root, release = "root-"+name, "release-"+name
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, verdict, reason_code)
VALUES ($1, $2, $3, '{"title":"[Group] Completion Show - 01"}', '{}', '{}', 'accepted_pending_download', 'accepted')`, release, completionSource, "guid:"+name); err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownloadManifest(t, tx, root, "task-"+name, release, "download:"+name, `{}`, `{}`, "completed", correlation,
		`[{"file_ref":"primary","ordinal":0,"relative_path":"Completion Show - 01.mkv","exact_size":7,"media_kind":"video","identity_evidence":{}}]`)
	if state != "running" {
		if _, err := tx.ExecContext(ctx, `UPDATE acquisitions SET state=$2,completion_open=($2='succeeded'),terminal_at=statement_timestamp(),terminal_schema='acquisition-terminal:v1',terminal_receipt='{}',detail_until=statement_timestamp()+detail_retention_seconds*INTERVAL '1 second',receipt_until=greatest(receipt_until,statement_timestamp()+receipt_after_terminal_seconds*INTERVAL '1 second') WHERE id=$1`, root, state); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return root, release
}

type fixture struct {
	db      *sql.DB
	owner   *sql.DB
	work    *workitems.Store
	store   *Store
	service *Service
	qb      *fakeQB
}

func newFixture(t *testing.T, snapshots fakeSnapshots) fixture {
	t.Helper()
	db := animetest.OpenTestDB(t)
	owner := disposabledb.Owner(t)
	animetest.SeedReleaseOwnerChain(t, db, completionSource)
	work, err := workitems.NewStore(db, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{DB: db, Work: work}
	qb := &fakeQB{}
	service, err := NewService(store, snapshots, qb)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{db: db, owner: owner, work: work, store: store, service: service, qb: qb}
}

func (f fixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.owner.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// runOne claims the next completion work item and carries its outcome through
// the same WorkItem store transitions the worker uses.
func (f fixture) runOne(t *testing.T) worker.Outcome {
	t.Helper()
	ctx := context.Background()
	claim, err := f.work.ClaimDue(ctx, "completion-test", []string{workitems.KindCompletionExecute}, time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	outcome, err := f.service.Handler().Handle(ctx, claim)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	switch outcome.Kind {
	case worker.OutcomeFinish:
		err = f.work.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner)
	case worker.OutcomeRetry:
		_, err = f.work.Retry(ctx, claim, outcome.ReasonCode, outcome.Reason, outcome.RetryAfter, outcome.WriteOwner)
	default:
		t.Fatalf("unexpected outcome %+v", outcome)
	}
	if err != nil {
		t.Fatalf("apply outcome: %v", err)
	}
	return outcome
}

func (f fixture) makeDue(t *testing.T) {
	t.Helper()
	if _, err := f.owner.Exec(`UPDATE work_items SET available_at = CURRENT_TIMESTAMP WHERE kind = 'completion_execute' AND state = 'available'`); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) row(t *testing.T, root string) Row {
	t.Helper()
	row, err := f.store.ByAcquisition(context.Background(), root)
	if err != nil || row == nil {
		t.Fatalf("completion of %s: %v %v", root, row, err)
	}
	return *row
}

func TestPassFreezesTheAcceptedActionAndEnqueuesOnce(t *testing.T) {
	f := newFixture(t, fakeSnapshots{"release-wipe": ActionRemoveWithData, "release-keep": ActionSeed, "release-pause": ActionPause,
		"release-failed": ActionRemove, "release-running": ActionRemove, "release-nohash": ActionRemove})
	wipe, _ := seedRoot(t, f.owner, "wipe", correlationJSON(hashA), "succeeded")
	keep, _ := seedRoot(t, f.owner, "keep", correlationJSON(hashB), "succeeded")
	// No snapshot at all reads as seed: no action.
	bare, _ := seedRoot(t, f.owner, "bare", correlationJSON(hashB), "succeeded")
	failed, _ := seedRoot(t, f.owner, "failed", correlationJSON(hashA), "failed")
	running, _ := seedRoot(t, f.owner, "running", correlationJSON(hashA), "running")
	nohash, _ := seedRoot(t, f.owner, "nohash", `{"external_id":"tag:bansoko-x","files_complete":true}`, "succeeded")

	ctx := context.Background()
	result, err := f.service.RunPass(ctx)
	if err != nil || result.Created != 4 {
		t.Fatalf("pass = %+v %v (want wipe, keep, bare, nohash)", result, err)
	}

	row := f.row(t, wipe)
	if row.Action != ActionRemoveWithData || row.State != StatePending || row.InfoHash != hashA || row.RemoteRoot != "/downloads" || row.ApplicationRoot != "/downloads" || row.Version != 1 {
		t.Fatalf("wipe row = %+v", row)
	}
	if n := f.count(t, `SELECT count(*) FROM work_items WHERE owner_type='download_completion' AND owner_id=$1 AND kind='completion_execute' AND state='available'`, row.ID); n != 1 {
		t.Fatalf("live work items of the wipe completion = %d", n)
	}
	for _, seeded := range []string{keep, bare} {
		row := f.row(t, seeded)
		if row.Action != ActionSeed || row.State != StateSucceeded || row.ResultCode != ResultSeedKept {
			t.Fatalf("seed row = %+v", row)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM work_item_history WHERE owner_type='download_completion'`); n != 1 {
		t.Fatalf("only the wipe completion has work, got %d work items", n)
	}
	// A task that never recorded its qB identity fails visibly and queues nothing.
	if row := f.row(t, nohash); row.State != StateFailed || row.ResultCode != FailureTaskUnknown {
		t.Fatalf("nohash row = %+v", row)
	}
	// A failed or still-running acquisition keeps its task: no row at all.
	for _, root := range []string{failed, running} {
		if got, err := f.store.ByAcquisition(ctx, root); err != nil || got != nil {
			t.Fatalf("%s has a completion row: %+v %v", root, got, err)
		}
	}

	// Idempotent: a second pass finds every succeeded acquisition marked.
	if again, err := f.service.RunPass(ctx); err != nil || again.Created != 0 {
		t.Fatalf("second pass = %+v %v", again, err)
	}
	if n := f.count(t, `SELECT count(*) FROM work_item_history WHERE owner_type='download_completion'`); n != 1 {
		t.Fatalf("second pass changed the work items: %d", n)
	}
}

func TestEditingTheConfigurationDoesNotChangeAFrozenAction(t *testing.T) {
	snapshots := fakeSnapshots{"release-frozen": ActionPause}
	f := newFixture(t, snapshots)
	root, _ := seedRoot(t, f.owner, "frozen", correlationJSON(hashA), "succeeded")
	// The Global section says remove_with_data now, but the release was
	// accepted under pause: the pass reads the frozen plan only.
	if _, err := f.owner.Exec(`INSERT INTO global_config_sections(section_name, schema_version, value, version) VALUES ('download_completion','download-completion-v2','{"action":"remove_with_data"}',2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RunPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if row := f.row(t, root); row.Action != ActionPause {
		t.Fatalf("action = %s, want the frozen pause", row.Action)
	}
}

func TestHandlerSettlesTheRowAndTheWorkItem(t *testing.T) {
	f := newFixture(t, fakeSnapshots{"release-done": ActionRemove})
	root, _ := seedRoot(t, f.owner, "done", correlationJSON(hashA), "succeeded")
	ctx := context.Background()
	if _, err := f.service.RunPass(ctx); err != nil {
		t.Fatal(err)
	}
	f.qb.torrent = &Torrent{Hash: hashA, State: "uploading", SavePath: "/downloads", ContentPath: "/downloads/Show", FileNames: []string{"Show/a.mkv"}}
	f.runOne(t)
	row := f.row(t, root)
	if row.State != StateSucceeded || row.ResultCode != ResultRemoved || row.Version != 2 || f.qb.torrent != nil {
		t.Fatalf("row after remove = %+v (task left: %v)", row, f.qb.torrent)
	}
	if n := f.count(t, `SELECT count(*) FROM work_item_history WHERE owner_type='download_completion' AND state='succeeded'`); n != 1 {
		t.Fatalf("finished work items = %d", n)
	}
	// A settled row cannot change again.
	if _, err := f.owner.Exec(`UPDATE download_completions SET state='failed', result_code='x', completed_at=CURRENT_TIMESTAMP, version=version+1 WHERE acquisition_id=$1`, root); err == nil {
		t.Fatal("a succeeded completion was changed")
	}
}

func TestTransientFailureRetriesThenFailsVisibly(t *testing.T) {
	f := newFixture(t, fakeSnapshots{"release-flaky": ActionRemove})
	root, _ := seedRoot(t, f.owner, "flaky", correlationJSON(hashA), "succeeded")
	ctx := context.Background()
	if _, err := f.service.RunPass(ctx); err != nil {
		t.Fatal(err)
	}
	f.qb.infoErr = errors.New("connection reset by peer")
	for attempt := 1; attempt <= completionMaxAttempts; attempt++ {
		outcome := f.runOne(t)
		if outcome.Kind != worker.OutcomeRetry {
			t.Fatalf("attempt %d: %+v", attempt, outcome)
		}
		row := f.row(t, root)
		if attempt < completionMaxAttempts {
			if row.State != StatePending || row.Version != int64(attempt)+1 {
				t.Fatalf("attempt %d left the row %+v", attempt, row)
			}
			f.makeDue(t)
		}
	}
	row := f.row(t, root)
	if row.State != StateFailed || row.ResultCode != FailureQBUnavailable {
		t.Fatalf("budget spent: row = %+v", row)
	}
	// The completion retry is retired: a failed completion stays failed.
}

func TestATypedRefusalFailsTheRowWithoutRetrying(t *testing.T) {
	f := newFixture(t, fakeSnapshots{"release-unsafe": ActionRemoveWithData})
	root, _ := seedRoot(t, f.owner, "unsafe", correlationJSON(hashA), "succeeded")
	ctx := context.Background()
	if _, err := f.service.RunPass(ctx); err != nil {
		t.Fatal(err)
	}
	// qBittorrent saves elsewhere than the frozen root: refused, task kept.
	f.qb.torrent = &Torrent{Hash: hashA, State: "uploading", SavePath: "/elsewhere", ContentPath: "/elsewhere/Show", FileNames: []string{"Show/a.mkv"}}
	outcome := f.runOne(t)
	if outcome.Kind != worker.OutcomeFinish || outcome.TerminalState != workitems.StateFailed {
		t.Fatalf("outcome = %+v", outcome)
	}
	if row := f.row(t, root); row.State != StateFailed || row.ResultCode != CodeQBTaskMismatch || f.qb.torrent == nil {
		t.Fatalf("row = %+v (task kept: %v)", row, f.qb.torrent != nil)
	}
}

func TestSharedTorrentAndLibraryPaths(t *testing.T) {
	f := newFixture(t, fakeSnapshots{"release-first": ActionRemove, "release-second": ActionRemove})
	first, _ := seedRoot(t, f.owner, "first", correlationJSON(hashA), "succeeded")
	// A second acquisition that still runs and holds the same torrent.
	second, _ := seedRoot(t, f.owner, "second", correlationJSON(hashA), "running")
	ctx := context.Background()
	if shared, err := f.store.SharedByOther(ctx, hashA, first); err != nil || !shared {
		t.Fatalf("shared with the running one = %v %v", shared, err)
	}
	if shared, err := f.store.SharedByOther(ctx, hashA, second); err != nil || shared {
		t.Fatalf("a running acquisition is not shared with a succeeded one = %v %v", shared, err)
	}
	if shared, err := f.store.SharedByOther(ctx, hashB, first); err != nil || shared {
		t.Fatalf("another torrent is not shared = %v %v", shared, err)
	}
	if _, err := f.service.RunPass(ctx); err != nil {
		t.Fatal(err)
	}
	f.qb.torrent = &Torrent{Hash: hashA, State: "uploading", SavePath: "/downloads", ContentPath: "/downloads/Show", FileNames: []string{"Show/a.mkv"}}
	f.runOne(t)
	if row := f.row(t, first); row.State != StateSucceeded || row.ResultCode != ResultTorrentShared || f.qb.torrent == nil {
		t.Fatalf("shared torrent: row = %+v (task kept: %v)", row, f.qb.torrent != nil)
	}

	if _, err := f.owner.Exec(`INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref, source_fingerprint, target_fingerprint, facts_version, state)
VALUES ('placement-a', '/library/Show/S01E01.mkv', 'anime-trace', 'reconciliation', 'ref-a', 'sf', 'tf', 'v1', 'active'), ('placement-gone', '/library/Show/S01E02.mkv', 'anime-trace', 'reconciliation', 'ref-b', 'sf', 'tf', 'v1', 'superseded')`); err != nil {
		t.Fatal(err)
	}
	// An alignment (an offset change's included) moves the file to its target
	// before the placement names that path.
	if _, err := f.owner.Exec(`INSERT INTO alignment_operations
(id, idempotency_key, managed_anime_id, scan_generation, observation_ref, candidate_ref, policy_version,
 metadata_version, rules_version, naming_version, source_path, source_fingerprint, target_path,
 target_contract, dry_run_result, state)
VALUES ('align-a', 'align:a', 'anime-trace', 1, 'obs-a', 'c', 1, 1, 1, 1,
 '/library/Show/S01E01.mkv', 'fp', '/library/Show/Season 01/E02.mkv', '{}', '{}', 'planned')`); err != nil {
		t.Fatal(err)
	}
	var paths []string
	if err := f.store.EachLibraryPath(ctx, func(path string) error { paths = append(paths, path); return nil }); err != nil {
		t.Fatal(err)
	}
	// Every placement counts whatever its state: a superseded or released file
	// is still somebody's file.
	sort.Strings(paths)
	want := []string{"/library/Show/S01E01.mkv", "/library/Show/S01E02.mkv", "/library/Show/Season 01/E02.mkv"}
	if !slices.Equal(paths, want) {
		t.Fatalf("library paths = %v, want the active and the superseded placement and the alignment target", paths)
	}
}

func completionOpen(t *testing.T, f fixture, root string) bool {
	t.Helper()
	var open bool
	if err := f.owner.QueryRow(`SELECT completion_open FROM acquisitions WHERE id=$1`, root).Scan(&open); err != nil {
		t.Fatal(err)
	}
	return open
}

// PRUNE-046: a succeeded download root enters the completion scan through
// completion_open and leaves it in the transaction that inserts its completion
// row. A failed insert leaves the root open, and the next pass picks it up.
func TestCompletionOpenEntersAndLeavesTheScan(t *testing.T) {
	f := newFixture(t, fakeSnapshots{"release-open": ActionSeed, "release-shut": ActionSeed})
	open, _ := seedRoot(t, f.owner, "open", correlationJSON(hashA), "succeeded")
	failed, _ := seedRoot(t, f.owner, "shut", correlationJSON(hashB), "failed")
	if !completionOpen(t, f, open) || completionOpen(t, f, failed) {
		t.Fatalf("completion_open: succeeded=%v failed=%v, want true/false", completionOpen(t, f, open), completionOpen(t, f, failed))
	}
	ctx := context.Background()
	if _, err := f.owner.Exec(`CREATE FUNCTION reject_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected completion failure'; END $$;
CREATE TRIGGER reject_completion BEFORE INSERT ON download_completions FOR EACH ROW EXECUTE FUNCTION reject_completion()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RunPass(ctx); err == nil {
		t.Fatal("pass with a rejected completion insert succeeded")
	}
	if !completionOpen(t, f, open) {
		t.Fatal("a rolled-back completion insert closed the root")
	}
	if _, err := f.owner.Exec(`DROP TRIGGER reject_completion ON download_completions; DROP FUNCTION reject_completion()`); err != nil {
		t.Fatal(err)
	}
	if result, err := f.service.RunPass(ctx); err != nil || result.Created != 1 {
		t.Fatalf("retry pass = %+v %v, want one completion", result, err)
	}
	if completionOpen(t, f, open) {
		t.Fatal("the completion insert left the root open")
	}
	if candidates, err := f.store.listCandidates(ctx, 10); err != nil || len(candidates) != 0 {
		t.Fatalf("candidates after the pass = %+v %v, want none", candidates, err)
	}
	if n := f.count(t, `SELECT count(*) FROM acquisitions WHERE completion_open`); n != 0 {
		t.Fatalf("%d roots still open", n)
	}
}

type limitSnapshots map[string]*batchplan.SeedLimits

func (f limitSnapshots) ReadSnapshot(_ context.Context, releaseID string) (*facts.Snapshot, error) {
	limits, ok := f[releaseID]
	if !ok {
		return nil, nil
	}
	return &facts.Snapshot{ReleaseID: releaseID, ReleaseFacts: facts.ReleaseFacts{BatchPlan: &batchplan.Plan{DownloadCompletion: ActionSeed, DownloadCompletionSeedLimits: limits}}}, nil
}

// A seed frozen with limits is pending work: the handler writes the limits to
// qBittorrent once, records the row succeeded and lets go.
func TestSeedLimitsAreFrozenWrittenOnceAndSettled(t *testing.T) {
	minutes := int64(1440)
	f := newFixture(t, nil)
	f.service.snapshot = limitSnapshots{"release-limited": {SeedingTimeMinutes: &minutes, AtLimit: batchplan.SeedLimitStop}}
	root, _ := seedRoot(t, f.owner, "limited", correlationJSON(hashA), "succeeded")
	ctx := context.Background()
	if result, err := f.service.RunPass(ctx); err != nil || result.Created != 1 {
		t.Fatalf("pass = %+v %v", result, err)
	}
	row := f.row(t, root)
	if row.Action != ActionSeed || row.State != StatePending || row.SeedLimits == nil || row.SeedLimits.SeedingTimeMinutes == nil || *row.SeedLimits.SeedingTimeMinutes != 1440 || row.SeedLimits.AtLimit != batchplan.SeedLimitStop {
		t.Fatalf("limited row = %+v", row)
	}
	// The frozen limits never change.
	if _, err := f.owner.Exec(`UPDATE download_completions SET seed_limits='{"ratio":1,"at_limit":"remove"}', version=version+1 WHERE acquisition_id=$1`, root); err == nil {
		t.Fatal("frozen seed limits were changed")
	}
	// qBittorrent 4.6 (WebAPI 2.9.x) has no per-task limit action.
	f.qb.torrent = &Torrent{Hash: hashA, State: "uploading", SavePath: "/downloads", ContentPath: "/downloads/Show", FileNames: []string{"Show/a.mkv"}}
	f.qb.webAPIVersion = "2.9.3"
	f.runOne(t)
	row = f.row(t, root)
	if row.State != StateSucceeded || row.ResultCode != ResultSeedLimitsSetGlobalAction || len(f.qb.limits) != 1 || f.qb.torrent == nil {
		t.Fatalf("settled = %+v limits=%+v", row, f.qb.limits)
	}
	if sent := f.qb.limits[0]; sent.SeedingTimeLimit != 1440 || sent.RatioLimit != -1 || sent.InactiveSeedingTimeLimit != -2 || sent.Action != "" {
		t.Fatalf("request = %+v", sent)
	}
}

func TestSeedLimitsSchemaRules(t *testing.T) {
	f := newFixture(t, fakeSnapshots{})
	root, _ := seedRoot(t, f.owner, "schema", correlationJSON(hashA), "succeeded")
	for name, query := range map[string]string{
		"seed without limits pending":   `INSERT INTO download_completions(id, acquisition_id, action, state) VALUES ('c-x', $1, 'seed', 'pending')`,
		"limits on another action":      `INSERT INTO download_completions(id, acquisition_id, action, state, seed_limits) VALUES ('c-x', $1, 'pause', 'pending', '{"ratio":1,"at_limit":"stop"}')`,
		"limits without a limit":        `INSERT INTO download_completions(id, acquisition_id, action, state, seed_limits) VALUES ('c-x', $1, 'seed', 'pending', '{"at_limit":"stop"}')`,
		"limits with an unknown action": `INSERT INTO download_completions(id, acquisition_id, action, state, seed_limits) VALUES ('c-x', $1, 'seed', 'pending', '{"ratio":1,"at_limit":"wipe"}')`,
	} {
		if _, err := f.owner.Exec(query, root); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	if _, err := f.owner.Exec(`INSERT INTO download_completions(id, acquisition_id, action, state, seed_limits) VALUES ('c-ok', $1, 'seed', 'pending', '{"ratio":1.5,"at_limit":"remove_with_data"}')`, root); err != nil {
		t.Fatalf("a valid limited seed was refused: %v", err)
	}
	// download_completion is Global-only: no anime may hold an override row.
	var animeID string
	if err := f.owner.QueryRow(`SELECT id FROM managed_anime LIMIT 1`).Scan(&animeID); err != nil {
		t.Fatalf("the owner chain has no managed anime: %v", err)
	}
	if _, err := f.owner.Exec(`INSERT INTO anime_config_overrides(managed_anime_id, owner_type, section_name, schema_version, value) VALUES ($1, 'rules', 'tag_filters', 'tag-filters-v2', '{"anibt":{"require":[],"exclude":[]},"title":{"require":[],"exclude":[]}}') ON CONFLICT DO NOTHING`, animeID); err != nil {
		t.Fatalf("a tag_filters override row was refused: %v", err)
	}
	if _, err := f.owner.Exec(`INSERT INTO anime_config_overrides(managed_anime_id, owner_type, section_name, schema_version, value) VALUES ($1, 'rules', 'download_completion', 'download-completion-v2', '{"action":"seed"}')`, animeID); err == nil {
		t.Fatal("a download_completion override was accepted")
	}
}
