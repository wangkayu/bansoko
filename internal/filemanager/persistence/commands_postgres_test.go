package persistence

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"bansoko/internal/deployment"
	"bansoko/internal/filemaintenance"
	"bansoko/internal/filemanager"
	filesystem "bansoko/internal/library/adapter/filesystem"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

// TestCommandsReleaseAndUserOperationsOverRealRows drives the P1 writes over
// real owner rows and a real library directory: the released terminal state
// and its database guard, release-and-move, the Q9 rule that a user operation
// never overwrites a managed path, and the fenced file_manager_execute worker
// for move, rename, copy-with-suffix and delete.
func TestCommandsReleaseAndUserOperationsOverRealRows(t *testing.T) {
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
	abs := func(rel string) string { return filepath.Join(tree, filepath.FromSlash(rel)) }
	for _, rel := range []string{"Show/ep01.mkv", "Show/ep01.zh.ass", "Show/ep02.mkv", "Loose/a.mkv", "Loose/b.mkv"} {
		writeTestFile(t, abs(rel))
	}
	exec := func(query string, args ...any) error {
		_, err := owner.ExecContext(ctx, query, args...)
		return err
	}
	must := func(query string, args ...any) {
		t.Helper()
		if err := exec(query, args...); err != nil {
			t.Fatalf("%s: %v", strings.TrimSpace(query), err)
		}
	}
	must(`INSERT INTO managed_anime(id, title) VALUES ('anime-a', 'Show A')`)
	must(`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state) VALUES ('sub-a', 'anime-a', 'custom', 'Show A', 'active')`)
	placement := func(id, path, state string) error {
		return exec(`INSERT INTO placements
(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref, source_fingerprint,
 target_fingerprint, facts_version, state, replacement_eligible)
VALUES ($1, $2, 'anime-a', 'reconciliation', 'obs-'||$1, 'fp', 'fp', 'parser-v1', $3, true)`, id, path, state)
	}
	for id, rel := range map[string]string{"p-ep01": "Show/ep01.mkv", "p-ep02": "Show/ep02.mkv", "p-ghost": "Show/ghost.mkv"} {
		if err := placement(id, abs(rel), "active"); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO placement_companions
(id, placement_id, managed_anime_id, source_ref, registration, media_kind, suffix, original_extension,
 current_path, size_bytes, fingerprint, state)
VALUES ('c-ep01', 'p-ep01', 'anime-a', 'src-c', 'intake', 'subtitle', 'zh', 'ass', $1, 1, $2, 'active')`,
		abs("Show/ep01.zh.ass"), "meta:size=1")

	// The database refuses a row born released.
	expectCheck(t, placement("p-born", abs("Show/born.mkv"), "released"), "PLACEMENT_RELEASE_REQUIRES_ACTIVE")

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
	service.SetCommands(commands, nil)
	users := filemaintenance.NewUserOperationService(db, workStore, filesystem.OSFileExecutor{}, filesystem.OSFileObserver{},
		func() filemaintenance.CleanupFileExecutor {
			return filesystem.NewOSCleanupExecutor(filemaintenance.CleanupAnchor)
		}, UserOperationGuard{Service: service})
	users.SetRecorder(observability)

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
		if err := exec(`UPDATE work_items SET available_at = CURRENT_TIMESTAMP WHERE kind = $1 AND state = 'ready'`, workitems.KindFileManagerExecute); err != nil {
			t.Fatal(err)
		}
		claim, err := workStore.ClaimDue(ctx, "fm-test", []string{workitems.KindFileManagerExecute}, time.Minute)
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
	code := func(err error) string {
		var typed *filemanager.Error
		if errors.As(err, &typed) {
			return typed.Code
		}
		return ""
	}

	// Q9: a detached file is never copied over a managed file, on disk or
	// named by an owner only; with a suffix it lands beside it.
	_, err = service.SubmitOperation(ctx, filemanager.OperationRequest{IdempotencyKey: "copy-block", Actor: "web", Operation: filemanager.UserCopy,
		Path: abs("Loose/a.mkv"), ExpectedRevision: revision("Loose/a.mkv"),
		Target: &filemanager.Target{Path: abs("Show/ghost.mkv")}})
	if code(err) != filemanager.CodeTargetExists {
		t.Fatalf("copy onto a managed path: %v", err)
	}
	if _, err := service.SubmitOperation(ctx, filemanager.OperationRequest{IdempotencyKey: "copy-version", Actor: "web", Operation: filemanager.UserCopy,
		Path: abs("Loose/a.mkv"), ExpectedRevision: revision("Loose/a.mkv"),
		Target: &filemanager.Target{Path: abs("Show/ghost.mkv"), Conflict: filemanager.ConflictVersion}}); err != nil {
		t.Fatal(err)
	}
	if op := run(); op.State != filemanager.UserOperationCompleted || op.ResultPath != abs("Show/ghost (2).mkv") {
		t.Fatalf("copy with suffix = %+v", op)
	}
	if _, err := os.Stat(abs("Loose/a.mkv")); err != nil {
		t.Fatalf("copy kept the source: %v", err)
	}

	// A managed file is not moved by the user.
	_, err = service.SubmitOperation(ctx, filemanager.OperationRequest{IdempotencyKey: "move-managed", Actor: "web", Operation: filemanager.UserMove,
		Path: abs("Show/ep02.mkv"), ExpectedRevision: revision("Show/ep02.mkv"),
		Target: &filemanager.Target{Path: abs("Loose/ep02.mkv")}})
	if code(err) != filemanager.CodeOperationNotAllowed {
		t.Fatalf("move of a managed file: %v", err)
	}

	// Release and move: the placement turns released, its subtitle relation
	// ends, and the move runs as a fenced user operation.
	outcome, err := service.Release(ctx, filemanager.ReleaseRequest{IdempotencyKey: "release-1", Actor: "web",
		Path: abs("Show/ep01.mkv"), ExpectedRevision: revision("Show/ep01.mkv"),
		MoveTo: &filemanager.Target{Path: abs("Loose/ep01.mkv")}})
	if err != nil || outcome.Released != 1 || outcome.Operation == nil {
		t.Fatalf("release = %+v, %v", outcome, err)
	}
	var state, releasedBy, companion, activity string
	if err := db.QueryRowContext(ctx, `SELECT state, released_by FROM placements WHERE id = 'p-ep01'`).Scan(&state, &releasedBy); err != nil || state != "released" || releasedBy != "web" {
		t.Fatalf("placement = %s by %s, %v", state, releasedBy, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT state || ':' || ended_reason FROM placement_companions WHERE id = 'c-ep01'`).Scan(&companion); err != nil || companion != "ended:released" {
		t.Fatalf("companion = %s, %v", companion, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT reason_code FROM subscription_log_entries WHERE subject_id = 'p-ep01'`).Scan(&activity); err != nil || activity != "placement_released" {
		t.Fatalf("activity = %s, %v", activity, err)
	}
	// Terminal and immutable in the database.
	expectCheck(t, exec(`UPDATE placements SET state = 'active', released_at = NULL, released_by = NULL, release_request_key = NULL WHERE id = 'p-ep01'`), "PLACEMENT_RELEASED_TERMINAL")
	expectCheck(t, exec(`UPDATE placements SET physical_path = $1 WHERE id = 'p-ep01'`, abs("Show/x.mkv")), "PLACEMENT_RELEASED_TERMINAL")
	if op := run(); op.State != filemanager.UserOperationCompleted || op.ResultPath != abs("Loose/ep01.mkv") {
		t.Fatalf("release move = %+v", op)
	}
	if _, err := os.Stat(abs("Show/ep01.zh.ass")); err != nil {
		t.Fatalf("the subtitle stays where it was: %v", err)
	}
	detail, err := service.Detail(ctx, abs("Loose/ep01.mkv"))
	if err != nil || detail.Entry.State != filemanager.StateDetached || detail.LastOperation == nil {
		t.Fatalf("moved file = %+v, %v", detail, err)
	}
	// A replay returns the release; the same key for another file conflicts.
	if replay, err := service.Release(ctx, filemanager.ReleaseRequest{IdempotencyKey: "release-1", Actor: "web", Path: abs("Show/ep01.mkv")}); err != nil || replay.Released != 1 {
		t.Fatalf("release replay = %+v, %v", replay, err)
	}
	_, err = service.Release(ctx, filemanager.ReleaseRequest{IdempotencyKey: "release-1", Actor: "web", Path: abs("Show/ep02.mkv")})
	if code(err) != filemanager.CodeIdempotencyConflict {
		t.Fatalf("release key reuse: %v", err)
	}

	// Rename re-checks its new target; then it runs.
	_, err = service.SubmitOperation(ctx, filemanager.OperationRequest{IdempotencyKey: "rename-occupied", Actor: "web", Operation: filemanager.UserRename,
		Path: abs("Loose/b.mkv"), ExpectedRevision: revision("Loose/b.mkv"), Target: &filemanager.Target{Path: abs("Loose/a.mkv")}})
	if code(err) != filemanager.CodeTargetExists {
		t.Fatalf("rename onto a file: %v", err)
	}
	if _, err := service.SubmitOperation(ctx, filemanager.OperationRequest{IdempotencyKey: "rename", Actor: "web", Operation: filemanager.UserRename,
		Path: abs("Loose/b.mkv"), ExpectedRevision: revision("Loose/b.mkv"), Target: &filemanager.Target{Path: abs("Loose/c.mkv")}}); err != nil {
		t.Fatal(err)
	}
	if op := run(); op.State != filemanager.UserOperationCompleted || op.ResultPath != abs("Loose/c.mkv") {
		t.Fatalf("rename = %+v", op)
	}

	// Crash after the effect, before the settle: the operation stays running
	// with its receipt. Deployment recovery settles it from the receipt, and
	// the leased work item then finishes against the settled owner.
	if _, err := service.SubmitOperation(ctx, filemanager.OperationRequest{IdempotencyKey: "move-crash", Actor: "web", Operation: filemanager.UserMove,
		Path: abs("Loose/a.mkv"), ExpectedRevision: revision("Loose/a.mkv"), Target: &filemanager.Target{Path: abs("Loose/d.mkv")}}); err != nil {
		t.Fatal(err)
	}
	if err := exec(`UPDATE work_items SET available_at = CURRENT_TIMESTAMP WHERE kind = $1 AND state = 'ready'`, workitems.KindFileManagerExecute); err != nil {
		t.Fatal(err)
	}
	crashed, err := workStore.ClaimDue(ctx, "fm-crash", []string{workitems.KindFileManagerExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.ExecuteHandler().Handle(ctx, crashed); err != nil {
		t.Fatal(err)
	}
	if op, err := service.Operation(ctx, crashed.Owner.ID); err != nil || op.State != filemanager.UserOperationRunning {
		t.Fatalf("unsettled operation = %+v, %v", op, err)
	}
	reconciled, err := users.ReconcileUnknownEffect(ctx, deployment.ReconcileRequest{OperationID: "dep-1", WindowID: "w-1"})
	if err != nil || reconciled.State != deployment.StateCompleted || reconciled.Effect != deployment.EffectPresent {
		t.Fatalf("reconcile = %+v, %v", reconciled, err)
	}
	if op, err := service.Operation(ctx, crashed.Owner.ID); err != nil || op.State != filemanager.UserOperationCompleted || op.ResultPath != abs("Loose/d.mkv") {
		t.Fatalf("reconciled operation = %+v, %v", op, err)
	}
	if err := owner.QueryRowContext(ctx, `SELECT expected_owner_version FROM work_item_history WHERE id = $1`, crashed.ID).Scan(&crashed.ExpectedOwnerVersion); err != nil {
		t.Fatal(err)
	}
	recovered, err := users.ExecuteHandler().(worker.RecoveryObserver).ObserveRecovery(ctx, crashed)
	if err != nil || recovered.Disposition != workitems.DispositionConfirmed {
		t.Fatalf("recovery after reconcile = %+v, %v", recovered, err)
	}
	if err := workStore.FinishWithResult(ctx, crashed, recovered.TerminalState, recovered.OwnerResult, recovered.WriteOwner); err != nil {
		t.Fatalf("finish after reconcile: %v", err)
	}
	var itemState string
	if err := owner.QueryRowContext(ctx, `SELECT state FROM work_item_history WHERE id = $1`, crashed.ID).Scan(&itemState); err != nil || itemState != workitems.StateSucceeded {
		t.Fatalf("work item after reconcile = %s, %v", itemState, err)
	}
	if again, err := users.ReconcileUnknownEffect(ctx, deployment.ReconcileRequest{OperationID: "dep-2", WindowID: "w-1"}); err != nil || again.State != deployment.StateCompleted || again.Effect != deployment.EffectNone {
		t.Fatalf("second reconcile = %+v, %v", again, err)
	}

	// Delete captures the exact file and removes it (the capture executor is
	// wired on linux and darwin and fails closed elsewhere).
	if _, err := service.SubmitOperation(ctx, filemanager.OperationRequest{IdempotencyKey: "delete", Actor: "web", Operation: filemanager.UserDelete,
		Path: abs("Loose/c.mkv"), ExpectedRevision: revision("Loose/c.mkv")}); err != nil {
		t.Fatal(err)
	}
	op := run()
	switch runtime.GOOS {
	case "linux", "darwin":
		if op.State != filemanager.UserOperationCompleted {
			t.Fatalf("delete = %+v", op)
		}
		if _, err := os.Lstat(abs("Loose/c.mkv")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("deleted file still present: %v", err)
		}
	default:
		if op.State == filemanager.UserOperationCompleted {
			t.Fatalf("delete must fail closed without a capture executor: %+v", op)
		}
	}
	var routine string
	if err := db.QueryRowContext(ctx, `SELECT last_result_code FROM routine_activities WHERE scope_id = 'file_manager'`).Scan(&routine); err != nil || routine == "" {
		t.Fatalf("routine row = %q, %v", routine, err)
	}
}

func expectCheck(t *testing.T, err error, message string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.Message != message {
		t.Fatalf("want %s, got %v", message, err)
	}
}
