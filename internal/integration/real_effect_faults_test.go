package integration

import (
	"bansoko/internal/testutil/storedsections"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	animedomain "bansoko/internal/anime/domain"
	fileoperationapplication "bansoko/internal/fileoperation/application"
	"bansoko/internal/library/adapter/filesystem"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/testutil/frozenpolicy"
	"bansoko/internal/testutil/mediafactsfixture"
	"bansoko/internal/workitems"
)

// realEffectFileFixture seeds the owner chain down to one verified
// artifact file so a file operation plan can be prepared and executed with
// the real OS executor against a scratch tree of its own.
type realEffectFileFixture struct {
	artifactID  string
	fileID      string
	root        string // canonical (EvalSymlinks) scratch tree standing in for the library
	sourcePath  string // canonical source file path
	fingerprint string
	size        int64
}

func seedRealEffectFileFixture(t *testing.T, db *sql.DB) realEffectFileFixture {
	t.Helper()
	return seedRealEffectFileFixtureWithPolicy(t, db, frozenpolicy.CompleteMap())
}

// seedRealEffectFileFixtureWithPolicy seeds the fixture under one frozen
// policy. The acquisition freezes its delivery (operation, collision) from this
// policy at acceptance, so a test that needs a different file effect must pass
// it here rather than rewrite the DownloadTask afterwards.
func seedRealEffectFileFixtureWithPolicy(t *testing.T, db *sql.DB, policy map[string]any) realEffectFileFixture {
	t.Helper()
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The naming templates decide where the real effect writes: they must name
	// the scratch root this fixture owns, because the code default is the
	// deployment mount /media. The shipped TV directory template keeps
	// anime.season_folder as a folder level of its own, so the frozen Vars
	// input carries an owner season too (fixture_anime_season).
	policy["route_root"] = root
	policy["fixture_anime_season"] = 1
	if sections, ok := policy["effective_sections"].(map[string]any); ok {
		sections[animedomain.SectionNamingRules] = rootNamingRules(root)
	}
	policyJSON, err := frozenpolicy.Encode(policy)
	if err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(t.TempDir(), "source.mkv")
	content := []byte("real-effect-fault-source")
	if err := os.WriteFile(sourcePath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	// The executor canonicalizes source paths (EvalSymlinks collapses the
	// macOS /var -> /private/var prefix), so the DB must carry the canonical
	// path or the handler reports source_path_changed.
	sourcePath, err = filepath.EvalSymlinks(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	fixture := realEffectFileFixture{
		artifactID:  "artifact-real-effect-fault",
		fileID:      "artifact-file-fault",
		root:        root,
		sourcePath:  sourcePath,
		fingerprint: hex.EncodeToString(digest[:]),
		size:        int64(len(content)),
	}
	fingerprintJSON := fmt.Sprintf(`{"algorithm":"sha256","value":"%s","size_bytes":%d}`, fixture.fingerprint, fixture.size)

	for _, stmt := range []string{
		`INSERT INTO managed_anime(id, title) VALUES ('anime-real-effect-fault', 'Fault Anime')`,
		// The shipped TV directory template keeps anime.season_folder as a
		// folder level of its own, so the fixture anime carries an owner season.
		`INSERT INTO anime_metadata(managed_anime_id, season) VALUES ('anime-real-effect-fault', 1)`,
		`INSERT INTO anime_business_configs(managed_anime_id) VALUES ('anime-real-effect-fault') ON CONFLICT (managed_anime_id) DO NOTHING`,
		`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
		 VALUES ('subscription-real-effect-fault', 'anime-real-effect-fault', 'custom', 'Fault test', 'active')`,
		`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
		 VALUES ('source-real-effect-fault', 'subscription-real-effect-fault', 'custom_rss', 'https://feed.example.test/rss', 'https://feed.example.test/rss', 'Test Group', 'Fault Anime')`,
		`INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, verdict, reason_code)
		 VALUES ('release-real-effect-fault', 'source-real-effect-fault', 'guid:fault', '{"title":"[Test Group] Fault Anime - 01"}', '{"group_name":"Test Group"}', '{"episode":1,"group_name":"Test Group"}', 'accepted_pending_download', 'accepted')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	storedsections.Seed(t, db, "anime-real-effect-fault")
	tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// The accepted manifest must describe exactly the artifact file below:
	// typed planning refuses an artifact whose files do not match it.
	manifest := fmt.Sprintf(`[{"file_ref":"primary","ordinal":0,"relative_path":"source.mkv","exact_size":%d,"media_kind":"video","identity_evidence":{}}]`, fixture.size)
	acquisitionfixture.SeedObservedDownloadManifest(t, tx, "acq-real-effect-fault", "download-real-effect-fault", "release-real-effect-fault", "download:fault", `{"release_stable_item_key":"guid:fault","source_locator_display":"https://feed.example.test/rss"}`, policyJSON, "completed", `null`, manifest)
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET download_task_id = 'download-real-effect-fault', verdict = 'submitted' WHERE id = 'release-real-effect-fault'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at)
VALUES ('artifact-real-effect-fault', 'download-real-effect-fault', 'acq-real-effect-fault', '{}', '{}', 'verified', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state)
VALUES ($1, 'artifact-real-effect-fault', 'acq-real-effect-fault', 'primary', 'source.mkv', $2, 'video', $3, $4, '{"resolution":"1080p"}', 'not_applicable')`,
		fixture.fileID, fixture.sourcePath, fixture.size, fingerprintJSON); err != nil {
		t.Fatal(err)
	}
	// "source.mkv" carries no episode of its own; its inspected row stands for
	// a file recognised as the title's episode 01.
	mediafactsfixture.Freeze(t, tx, "release-real-effect-fault", "[Test Group] Fault Anime - 01",
		mediafactsfixture.Video{FileRef: "primary", Name: "Fault Anime - 01.mkv", Path: "source.mkv", Size: fixture.size})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// runRealEffectFileClaim claims the prepared file-execute work item and
// settles the outcome exactly like the runtime worker would.
func runRealEffectFileClaim(t *testing.T, db *sql.DB, service *fileoperationapplication.Service) worker.Outcome {
	t.Helper()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(context.Background(), "real-effect-fault-worker", []string{workitems.KindFileExecute}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	switch outcome.Kind {
	case worker.OutcomeCommitted:
		// Typed file execution commits its owner result and the WorkItem
		// transition inside the handler; there is nothing left to settle.
	case worker.OutcomeFinish:
		if err := store.FinishWithResult(context.Background(), claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
			t.Fatal(err)
		}
	case worker.OutcomeRetry:
		if outcome.RetryUntilResolved {
			if _, err := store.RetryWithoutAttemptBudget(context.Background(), claim, outcome.ReasonCode, outcome.Reason, outcome.RetryAfter, outcome.WriteOwner); err != nil {
				t.Fatal(err)
			}
		} else if _, err := store.Retry(context.Background(), claim, outcome.ReasonCode, outcome.Reason, outcome.RetryAfter, outcome.WriteOwner); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unexpected outcome kind: %+v", outcome)
	}
	return outcome
}

// assertRealEffectWorkItemTerminal proves the typed commit left the
// file_execute WorkItem terminal in the expected state (no live retry loop).
func assertRealEffectWorkItemTerminal(t *testing.T, db *sql.DB, artifactID, want string) {
	t.Helper()
	var state string
	if err := db.QueryRowContext(context.Background(), `
SELECT w.state FROM work_item_history w
JOIN file_operations o ON o.id = w.owner_id
WHERE w.kind = 'file_execute' AND o.artifact_id = $1`, artifactID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != want {
		t.Fatalf("file_execute WorkItem state=%q, want %q", state, want)
	}
}

// TestRealEffectPermissionDeniedSettlesTerminalWithOwnerReason makes the
// fixture's own tree non-writable so the actual OS executor hits EACCES creating the
// target parent: the operation
// must settle terminal failed with a visible owner reason — never a silent pass,
// never an infinite retry.
func TestRealEffectPermissionDeniedSettlesTerminalWithOwnerReason(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	ctx := context.Background()
	fixture := seedRealEffectFileFixture(t, db)

	service, err := fileoperationapplication.NewService(db, filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	if err != nil {
		t.Fatal(err)
	}
	observability, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	service.SetActivityWriter(observability)
	prepareRealEffectTypedPlan(t, db, service, fixture.artifactID)
	// A real permission fault: the fixture's tree refuses the target parent the
	// typed adapter must create. Root ignores the mode, so the lane must run
	// non-root (both qB lanes do); fail rather than pass vacuously.
	if os.Geteuid() == 0 {
		t.Fatal("the permission fault needs a non-root test user")
	}
	if err := os.Chmod(fixture.root, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(fixture.root, 0o755) })

	outcome := runRealEffectFileClaim(t, db, service)
	if outcome.Kind != worker.OutcomeCommitted {
		t.Fatalf("permission fault must settle through the typed commit, got outcome=%+v", outcome)
	}
	assertRealEffectWorkItemTerminal(t, db, fixture.artifactID, workitems.StateFailed)
	if outcome.OwnerResult.State != "failed" {
		t.Fatalf("owner must read failed, got %+v", outcome.OwnerResult)
	}
	if outcome.OwnerResult.ReasonCode == "" || outcome.OwnerResult.Reason == "" {
		t.Fatalf("owner failure code and reason must be set: %+v", outcome.OwnerResult)
	}
	var state, code, reason string
	if err := db.QueryRowContext(ctx, `SELECT state, failure_code, failure_reason FROM file_operations WHERE artifact_id = $1`, fixture.artifactID).Scan(&state, &code, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || code == "" || reason == "" {
		t.Fatalf("persisted owner row missing failure evidence: state=%q code=%q reason=%q", state, code, reason)
	}
}

// TestRealEffectWritableTargetCompletes is the writable control: the same
// real OS executor on a writable target settles completed with the target
// materialized and fingerprinted — the permission fault above is therefore
// attributable to the injected mode, not to a broken plan.
func TestRealEffectWritableTargetCompletes(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	ctx := context.Background()
	fixture := seedRealEffectFileFixture(t, db)

	service, err := fileoperationapplication.NewService(db, filesystem.OSFileExecutor{}, filesystem.OSFileObserver{})
	if err != nil {
		t.Fatal(err)
	}
	observability, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	service.SetActivityWriter(observability)
	prepareRealEffectTypedPlan(t, db, service, fixture.artifactID)

	outcome := runRealEffectFileClaim(t, db, service)
	if outcome.Kind != worker.OutcomeCommitted {
		t.Fatalf("writable target must settle through the typed commit, got outcome=%+v", outcome)
	}
	assertRealEffectWorkItemTerminal(t, db, fixture.artifactID, workitems.StateSucceeded)
	if outcome.OwnerResult.State != "completed" {
		t.Fatalf("owner must read completed, got %+v", outcome.OwnerResult)
	}
	var state, targetPath string
	var targetFingerprint sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT state, target_path, target_fingerprint FROM file_operations WHERE artifact_id = $1`, fixture.artifactID).Scan(&state, &targetPath, &targetFingerprint); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || !targetFingerprint.Valid {
		t.Fatalf("completed operation missing fingerprint: state=%q fingerprint_valid=%v", state, targetFingerprint.Valid)
	}
	info, err := os.Stat(targetPath)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("target file not materialized on disk: path=%q err=%v", targetPath, err)
	}
}
