package persistence

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"bansoko/internal/filemaintenance"
	"bansoko/internal/filemanager"
	filesystem "bansoko/internal/library/adapter/filesystem"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

// pathRenderer stands in for the naming renderer (its render core is covered in
// filemaintenance): it renders the relative path a test sets under the tree, as
// the real renderer does with an absolute directory template.
type pathRenderer struct {
	root string
	path string
}

func (r *pathRenderer) MatchPath(rel string) string {
	if rel == "" {
		return ""
	}
	return filepath.Join(r.root, filepath.FromSlash(rel))
}

func (r *pathRenderer) RenderMatch(_ context.Context, _ filemaintenance.MatchRenderInput) (filemaintenance.MatchRender, error) {
	return filemaintenance.MatchRender{Path: r.path}, nil
}

// tableState is a count and a content digest of one table, to prove a read or
// a refused write changed nothing.
func tableState(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	var state string
	err := db.QueryRowContext(context.Background(),
		`SELECT count(*)::text || ':' || COALESCE(md5(string_agg(x::text, '|' ORDER BY x::text)), '') FROM `+table+` x`).Scan(&state)
	if err != nil {
		t.Fatalf("state of %s: %v", table, err)
	}
	return state
}

// TestMatchOverRealRows drives the match flow over real owner rows and a real library
// directory: a preview that writes nothing, the typed refusals, the admission
// and worker re-checks (managed mid-way, anime retired mid-way), directory
// creation inside the root, a symlink that appears after admission, the
// version suffix, and a result that stays detached.
func TestMatchOverRealRows(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	owner := disposabledb.Owner(t)
	defer owner.Close()
	ctx := context.Background()

	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(base, "anime")
	downloads := filepath.Join(base, "downloads")
	outside := filepath.Join(base, "outside")
	abs := func(rel string) string { return filepath.Join(tree, filepath.FromSlash(rel)) }
	for _, rel := range []string{
		"Show/extras/orig.mkv", "Show/extras/orig.zh.ass", "Show/extras/other.ass", "Show/ep01.mkv",
		"Show/Season 01/Show - E02.mkv", "Loose/a.mkv", "Loose/b.mkv", "Loose/c.mkv", "Loose/d.mkv",
	} {
		writeTestFile(t, abs(rel))
	}
	writeTestFile(t, filepath.Join(downloads, "ep.mkv"))
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	must := func(query string, args ...any) {
		t.Helper()
		if _, err := owner.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", strings.TrimSpace(query), err)
		}
	}
	must(`INSERT INTO connections(id, kind, name, endpoint, secret_ref, state, config)
VALUES ('qb-main', 'qbittorrent', 'Main', 'http://qb.local', 'secret-qb', 'configured', $1::jsonb)`,
		`{"username":"admin","category":"anime","save_root":"/remote/downloads","downloader_root":"`+downloads+`","user_tags":[]}`)
	must(`INSERT INTO managed_anime(id, title) VALUES ('anime-a', 'Show A'), ('anime-b', 'Show B'), ('anime-c', 'Show C'),
 ('anime-r', 'Show R'), ('anime-o', 'Temp O'), ('anime-n', 'Show N')`)
	must(`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES
 ('sub-a', 'anime-a', 'custom', 'Show A', 'active'), ('sub-b', 'anime-b', 'custom', 'Show B', 'stopped'),
 ('sub-c', 'anime-c', 'custom', 'Show C', 'stopped'), ('sub-r', 'anime-r', 'custom', 'Show R', 'stopped'),
 ('sub-o', 'anime-o', 'custom', 'Temp O', 'stopped')`)
	must(`INSERT INTO one_shot_acquisitions(
    id, managed_anime_id, subscription_id, rss_source_id, release_id, download_task_id,
    correlation_id, request_idempotency_key, request_hash, result_snapshot, state)
VALUES ('os-1', 'anime-o', 'sub-o', 'src-o', 'rel-o', 'task-o', 'corr-o', 'req-key-os-1', $1, '{}'::jsonb, 'running')`,
		"sha256:"+strings.Repeat("0", 64))
	placement := func(id, path string, eligible bool) {
		must(`INSERT INTO placements
(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref, source_fingerprint,
 target_fingerprint, facts_version, state, replacement_eligible)
VALUES ($1, $2, 'anime-a', 'reconciliation', 'obs-'||$1, 'fp', 'fp', 'parser-v1', 'active', $3)`, id, path, eligible)
	}
	placement("p-managed", abs("Show/ep01.mkv"), true)
	// The unknown file the match starts from: released at import.
	placement("p-orig", abs("Show/extras/orig.mkv"), false)
	must(`UPDATE placements SET state = 'released', released_at = CURRENT_TIMESTAMP,
 released_by = 'system:unknown_file_import', release_request_key = 'unknown-file:p-orig' WHERE id = 'p-orig'`)
	must(`UPDATE managed_anime SET lifecycle_state = 'retired', fence_epoch = fence_epoch + 1,
 retired_at = CURRENT_TIMESTAMP, retired_subscription_version = 1, lifecycle_version = lifecycle_version + 1 WHERE id = 'anime-r'`)

	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	service, err := filemanager.NewService(store, filemanager.NewDenialZone(filepath.Join(base, "appdata")))
	if err != nil {
		t.Fatal(err)
	}
	workStore, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	observability, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	commands, err := NewCommands(db, workStore, observability, nil)
	if err != nil {
		t.Fatal(err)
	}
	renderer := &pathRenderer{root: tree}
	service.SetCommands(commands, nil)
	service.SetMatch(renderer, store, nil)
	users := filemaintenance.NewUserOperationService(db, workStore, filesystem.OSFileExecutor{}, filesystem.OSFileObserver{},
		func() filemaintenance.CleanupFileExecutor {
			return filesystem.NewOSCleanupExecutor(filemaintenance.CleanupAnchor)
		}, UserOperationGuard{Service: service})
	users.SetRecorder(observability)

	code := func(err error) string {
		var typed *filemanager.Error
		if errors.As(err, &typed) {
			return typed.Code
		}
		return ""
	}
	revision := func(rel string) string {
		t.Helper()
		detail, err := service.Detail(ctx, abs(rel))
		if err != nil {
			t.Fatalf("detail %s: %v", rel, err)
		}
		return detail.Revision
	}
	run := func() filemanager.UserOperation {
		t.Helper()
		must(`UPDATE work_items SET available_at = CURRENT_TIMESTAMP WHERE kind = $1 AND state = 'ready'`, workitems.KindFileManagerExecute)
		claim, err := workStore.ClaimDue(ctx, "fm-match", []string{workitems.KindFileManagerExecute}, time.Minute)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		outcome, err := users.ExecuteHandler().Handle(ctx, claim)
		if err != nil {
			t.Fatalf("handle: %v", err)
		}
		if err := workStore.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
			t.Fatalf("finish: %v", err)
		}
		op, err := service.Operation(ctx, claim.Owner.ID)
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	submitAt := func(key, rel, rev, anime string, episode int, choice filemanager.ConflictChoice, target string) (filemanager.UserOperation, error) {
		t.Helper()
		renderer.path = renderer.MatchPath(target)
		return service.SubmitMatch(ctx, filemanager.SubmitMatchRequest{
			IdempotencyKey: key, Actor: "web", Path: abs(rel),
			ExpectedRevision: rev, ManagedAnimeID: anime, Season: 1, Episode: episode,
			ExpectedTarget: renderer.MatchPath(target), Conflict: choice,
		})
	}
	submit := func(key, rel, anime string, episode int, choice filemanager.ConflictChoice, target string) (filemanager.UserOperation, error) {
		t.Helper()
		return submitAt(key, rel, revision(rel), anime, episode, choice, target)
	}
	preview := func(rel, anime, target string) (filemanager.MatchPreview, error) {
		renderer.path = renderer.MatchPath(target)
		return service.PreviewMatch(ctx, filemanager.MatchRequest{Path: abs(rel), ManagedAnimeID: anime, Season: 1, Episode: 3})
	}
	ownerTables := []string{"placements", "placement_companions", "placement_coverage"}
	snapshot := func() map[string]string {
		t.Helper()
		state := map[string]string{}
		for _, table := range append([]string{"file_manager_operations", "work_items"}, ownerTables...) {
			state[table] = tableState(t, owner, table)
		}
		return state
	}
	assertSame := func(before map[string]string, tables ...string) {
		t.Helper()
		for _, table := range tables {
			if now := tableState(t, owner, table); now != before[table] {
				t.Fatalf("%s changed: %s -> %s", table, before[table], now)
			}
		}
	}

	// The preview writes nothing: no operation, work item, placement, coverage
	// or title refresh, and no directory.
	before := snapshot()
	got, err := preview("Show/extras/orig.mkv", "", "Show/Season 05/Show - E03.mkv")
	if err != nil || got.Anime.ID != "anime-a" || got.TargetPath != abs("Show/Season 05/Show - E03.mkv") ||
		len(got.DirectoriesToCreate) != 1 || got.DirectoriesToCreate[0] != abs("Show/Season 05") || got.Conflict != nil || len(got.Blockers) != 0 {
		t.Fatalf("preview = %+v, %v", got, err)
	}
	if len(got.SubtitlesLeftBehind) != 1 || got.SubtitlesLeftBehind[0] != abs("Show/extras/orig.zh.ass") {
		t.Fatalf("only the subtitle that follows the video's name stays behind: %v", got.SubtitlesLeftBehind)
	}
	if _, err := os.Lstat(abs("Show/Season 05")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a preview creates no directory: %v", err)
	}
	// Conflicts and typed refusals are previews too.
	if got, err = preview("Show/extras/orig.mkv", "anime-a", "Show/Season 01/Show - E02.mkv"); err != nil ||
		got.Conflict == nil || got.Conflict.OccupiedBy != filemanager.OccupantFile || got.Conflict.VersionPath != abs("Show/Season 01/Show - E02 (2).mkv") {
		t.Fatalf("occupied by a file = %+v, %v", got, err)
	}
	if got, err = preview("Show/extras/orig.mkv", "anime-a", "Show/ep01.mkv"); err != nil || got.Conflict == nil || got.Conflict.OccupiedBy != filemanager.OccupantManaged {
		t.Fatalf("a managed target (it is a file on disk too, and must read as managed) = %+v, conflict %+v, %v", got, got.Conflict, err)
	}
	if err := os.Symlink(outside, abs("Linked")); err != nil {
		t.Fatal(err)
	}
	if got, err = preview("Show/extras/orig.mkv", "anime-a", "Linked/Season 01/x.mkv"); err != nil || len(got.Blockers) != 1 || got.Blockers[0].Code != filemanager.MatchBlockedTargetInvalid {
		t.Fatalf("a symlink in the target chain = %+v, %v", got, err)
	}
	for anime, want := range map[string]string{
		"anime-r": filemanager.CodeAnimeRetired, "anime-o": filemanager.CodeMatchAnimeUnavailable,
		"anime-n": filemanager.CodeMatchAnimeUnavailable, "anime-x": filemanager.CodeMatchAnimeUnavailable,
	} {
		if _, err := preview("Show/extras/orig.mkv", anime, "Show/Season 05/x.mkv"); code(err) != want {
			t.Fatalf("anime %s: %v", anime, err)
		}
	}
	if _, err := preview("Loose/a.mkv", "", "Show/Season 05/x.mkv"); code(err) != filemanager.CodeMatchAnimeRequired {
		t.Fatalf("a file with no lineage needs an anime: %v", err)
	}
	if _, err := preview("Show/ep01.mkv", "anime-a", "Show/Season 05/x.mkv"); code(err) != filemanager.CodeMatchRequiresDetached {
		t.Fatalf("a managed file is aligned: %v", err)
	}
	renderer.path = abs("Show/Season 05/x.mkv")
	matchable, err := service.PreviewMatch(ctx, filemanager.MatchRequest{Path: filepath.Join(downloads, "ep.mkv"), ManagedAnimeID: "anime-a", Season: 1, Episode: 1})
	if err != nil || matchable.TargetPath != abs("Show/Season 05/x.mkv") || len(matchable.Blockers) != 0 {
		t.Fatalf("a file in a downloader's directory matches into the work's tree: %+v, %v", matchable, err)
	}
	assertSame(before, "file_manager_operations", "work_items", "placements", "placement_companions", "placement_coverage")

	// The default anime of a released file is the placement's.
	detail, err := service.Detail(ctx, abs("Show/extras/orig.mkv"))
	if err != nil || detail.MatchDefaultAnime == nil || detail.MatchDefaultAnime.ID != "anime-a" {
		t.Fatalf("default anime = %+v, %v", detail.MatchDefaultAnime, err)
	}

	// An occupied target is cancelled under block and never overwritten; the
	// version choice lands beside it.
	if _, err := submit("m-block", "Show/extras/orig.mkv", "anime-a", 2, filemanager.ConflictBlock, "Show/Season 01/Show - E02.mkv"); code(err) != filemanager.CodeTargetExists {
		t.Fatalf("block on an occupied target: %v", err)
	}
	if _, err := submit("m-version", "Loose/a.mkv", "anime-b", 2, filemanager.ConflictVersion, "Show/Season 01/Show - E02.mkv"); err != nil {
		t.Fatal(err)
	}
	op := run()
	if op.State != filemanager.UserOperationCompleted || op.Operation != filemanager.UserMatch || op.ResultPath != abs("Show/Season 01/Show - E02 (2).mkv") ||
		op.Match == nil || op.Match.ManagedAnimeID != "anime-b" || op.Match.Season != 1 || op.Match.Episode != 2 || len(op.CreatedDirs) != 0 {
		t.Fatalf("version match = %+v", op)
	}
	if _, err := os.Stat(abs("Show/Season 01/Show - E02.mkv")); err != nil {
		t.Fatalf("the occupant was kept: %v", err)
	}
	if _, err := os.Lstat(abs("Loose/a.mkv")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the file moved: %v", err)
	}

	// A new directory is created inside the root, the match is recorded, and
	// the file stays detached with no placement and no coverage.
	before = snapshot()
	confirmed := revision("Show/extras/orig.mkv")
	first, err := submitAt("m-new", "Show/extras/orig.mkv", confirmed, "anime-a", 3, filemanager.ConflictBlock, "Show/Season 05/Show - E03.mkv")
	if err != nil || first.State != filemanager.UserOperationReady || first.Match == nil {
		t.Fatalf("admitted = %+v, %v", first, err)
	}
	if replay, err := submitAt("m-new", "Show/extras/orig.mkv", confirmed, "anime-a", 3, filemanager.ConflictBlock, "Show/Season 05/Show - E03.mkv"); err != nil || replay.ID != first.ID {
		t.Fatalf("a replay returns the original: %+v, %v", replay, err)
	}
	if _, err := submitAt("m-new", "Show/extras/orig.mkv", confirmed, "anime-a", 4, filemanager.ConflictBlock, "Show/Season 05/Show - E04.mkv"); code(err) != filemanager.CodeIdempotencyConflict {
		t.Fatalf("the key for another request conflicts: %v", err)
	}
	op = run()
	if op.State != filemanager.UserOperationCompleted || op.ResultPath != abs("Show/Season 05/Show - E03.mkv") ||
		len(op.CreatedDirs) != 1 || op.CreatedDirs[0] != abs("Show/Season 05") {
		t.Fatalf("match = %+v", op)
	}
	if info, err := os.Lstat(abs("Show/Season 05")); err != nil || !info.IsDir() {
		t.Fatalf("created directory: %v", err)
	}
	if _, err := os.Stat(abs("Show/extras/orig.zh.ass")); err != nil {
		t.Fatalf("the subtitle stays where it was: %v", err)
	}
	assertSame(before, ownerTables...)
	detail, err = service.Detail(ctx, abs("Show/Season 05/Show - E03.mkv"))
	if err != nil || detail.Entry.State != filemanager.StateDetached || detail.LastOperation == nil || detail.LastOperation.Match == nil {
		t.Fatalf("the result stays detached: %+v, %v", detail, err)
	}
	var routine string
	if err := db.QueryRowContext(ctx, `SELECT last_result_code FROM routine_activities WHERE scope_id = 'file_manager'`).Scan(&routine); err != nil || routine == "" {
		t.Fatalf("routine row = %q, %v", routine, err)
	}

	// Both re-checks catch a file that became managed after the preview: the
	// admission refuses it, and a file adopted after admission is not moved.
	if _, err := submit("m-race", "Loose/b.mkv", "anime-a", 4, filemanager.ConflictBlock, "Fresh/Season 01/Show - E04.mkv"); err != nil {
		t.Fatal(err)
	}
	placement("p-adopted", abs("Loose/b.mkv"), true)
	op = run()
	if op.State != filemanager.UserOperationBlocked || op.Failure != filemanager.FailureSourceManaged {
		t.Fatalf("managed after admission = %+v", op)
	}
	if _, err := os.Lstat(abs("Fresh")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a blocked match created no directory: %v", err)
	}
	if _, err := os.Stat(abs("Loose/b.mkv")); err != nil {
		t.Fatalf("a blocked match left the file: %v", err)
	}
	if _, err := preview("Loose/b.mkv", "anime-a", "Fresh/Season 01/Show - E04.mkv"); code(err) != filemanager.CodeMatchRequiresDetached {
		t.Fatalf("a managed file is refused at the preview: %v", err)
	}
	adopt := filemanager.OperationAdmission{
		ID: "op-raw", IdempotencyKey: "raw-managed", Digest: strings.Repeat("a", 64), Actor: "web", Operation: filemanager.UserMatch,
		SourcePath: abs("Loose/b.mkv"), SourceSpellings: []string{abs("Loose/b.mkv")},
		TargetPath: abs("Fresh/x.mkv"), TargetSpellings: []string{abs("Fresh/x.mkv")},
		ExpectedSize: 1, Collision: filemanager.ConflictBlock, RequireDetachedSource: true,
		Match: &filemanager.MatchAdmission{ManagedAnimeID: "anime-a", Season: 1, Episode: 1},
	}
	if _, err := commands.AdmitOperation(ctx, adopt); code(err) != filemanager.CodeEntryChanged {
		t.Fatalf("the admission transaction re-checks that the source is detached: %v", err)
	}

	// The admission transaction re-checks the anime; the worker checks again.
	adopt.ID, adopt.IdempotencyKey, adopt.SourcePath, adopt.SourceSpellings = "op-raw-2", "raw-retired", abs("Loose/c.mkv"), []string{abs("Loose/c.mkv")}
	adopt.Match = &filemanager.MatchAdmission{ManagedAnimeID: "anime-r", Season: 1, Episode: 1}
	if _, err := commands.AdmitOperation(ctx, adopt); code(err) != filemanager.CodeAnimeRetired {
		t.Fatalf("a retired anime at admission: %v", err)
	}
	adopt.IdempotencyKey, adopt.ID = "raw-oneshot", "op-raw-3"
	adopt.Match = &filemanager.MatchAdmission{ManagedAnimeID: "anime-o", Season: 1, Episode: 1}
	if _, err := commands.AdmitOperation(ctx, adopt); code(err) != filemanager.CodeMatchAnimeUnavailable {
		t.Fatalf("a one-shot's anime at admission: %v", err)
	}
	if _, err := submit("m-retire", "Loose/c.mkv", "anime-c", 5, filemanager.ConflictBlock, "Gone/Season 01/Show - E05.mkv"); err != nil {
		t.Fatal(err)
	}
	must(`UPDATE managed_anime SET lifecycle_state = 'retired', fence_epoch = fence_epoch + 1,
 retired_at = CURRENT_TIMESTAMP, retired_subscription_version = 1, lifecycle_version = lifecycle_version + 1 WHERE id = 'anime-c'`)
	op = run()
	if op.State != filemanager.UserOperationBlocked || op.Failure != filemanager.FailureAnimeUnavailable {
		t.Fatalf("anime retired after admission = %+v", op)
	}
	if _, err := os.Lstat(abs("Gone")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a blocked match created no directory: %v", err)
	}

	// A symlink that appears in the target chain after admission is never
	// followed: nothing is created outside the root and the file stays.
	if _, err := submit("m-link", "Loose/d.mkv", "anime-a", 6, filemanager.ConflictBlock, "Later/Season 01/Show - E06.mkv"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, abs("Later")); err != nil {
		t.Fatal(err)
	}
	op = run()
	if op.State == filemanager.UserOperationCompleted {
		t.Fatalf("a symlink in the target chain must stop the match: %+v", op)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("nothing is created through the symlink: %v, %v", entries, err)
	}
	if _, err := os.Stat(abs("Loose/d.mkv")); err != nil {
		t.Fatalf("the file stays: %v", err)
	}

	// The database keeps the match columns frozen and consistent.
	var frozen error
	_, frozen = owner.ExecContext(ctx, `UPDATE file_manager_operations SET match_season = 9 WHERE id = $1`, first.ID)
	var pgErr *pgconn.PgError
	if !errors.As(frozen, &pgErr) || pgErr.Message != "FILE_MANAGER_OPERATION_TERMINAL" {
		t.Fatalf("a settled operation never changes: %v", frozen)
	}
	_, err = owner.ExecContext(ctx, `INSERT INTO file_manager_operations
(id, idempotency_key, request_digest, actor, operation, source_path, expected_size_bytes, collision, match_anime_id)
VALUES ('op-bad', 'bad', $1, 'web', 'delete', '/l/a.mkv', 1, 'block', 'anime-a')`, strings.Repeat("a", 64))
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "file_manager_operations_match_check" {
		t.Fatalf("only a match carries the match columns: %v", err)
	}
	_, err = owner.ExecContext(ctx, `INSERT INTO file_manager_operations
(id, idempotency_key, request_digest, actor, operation, source_path, target_path, expected_size_bytes, collision)
VALUES ('op-bad-2', 'bad-2', $1, 'web', 'match', '/l/a.mkv', '/l/b.mkv', 1, 'block')`, strings.Repeat("a", 64))
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "file_manager_operations_match_check" {
		t.Fatalf("a match needs its anime, season and episode: %v", err)
	}
}
