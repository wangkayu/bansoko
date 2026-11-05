package persistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bansoko/internal/filemaintenance"
	"bansoko/internal/filemanager"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

type deletePlacementFixture struct {
	ctx       context.Context
	db        *sql.DB
	owner     *sql.DB
	root      string
	service   *filemanager.Service
	commands  *Commands
	workStore *workitems.Store
}

func newDeletePlacementFixture(t *testing.T) *deletePlacementFixture {
	t.Helper()
	db := disposabledb.ResetDisposableSchema(t)
	t.Cleanup(func() { db.Close() })
	owner := disposabledb.Owner(t)
	t.Cleanup(func() { owner.Close() })
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	service, err := filemanager.NewService(store, filemanager.NewDenialZone(filepath.Join(filepath.Dir(root), "appdata")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddRoot(context.Background(), filemanager.AddRootRequest{Path: root}); err != nil {
		t.Fatal(err)
	}
	workStore, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	commands, err := NewCommands(db, workStore, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.SetCommands(commands, nil)
	return &deletePlacementFixture{
		ctx: context.Background(), db: db, owner: owner, root: root,
		service: service, commands: commands, workStore: workStore,
	}
}

func (f *deletePlacementFixture) path(name string) string {
	return filepath.Join(f.root, filepath.FromSlash(name))
}

func (f *deletePlacementFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.owner.ExecContext(f.ctx, query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// seedPlacement creates the stable provider/group chain used by authorization.
// A false selected value models a group excluded or unselected by the current
// subscription policy; the anime subscription itself remains active.
func (f *deletePlacementFixture) seedPlacement(t *testing.T, id, video string, selected bool, companions ...string) {
	t.Helper()
	f.exec(t, `INSERT INTO managed_anime(id, title) VALUES ('anime-delete', 'Show') ON CONFLICT (id) DO NOTHING`)
	f.exec(t, `INSERT INTO subscriptions(id, managed_anime_id, origin, state)
VALUES ('sub-delete', 'anime-delete', 'anibt', 'active') ON CONFLICT (id) DO NOTHING`)
	f.exec(t, `INSERT INTO provider_group_selections
(id, subscription_id, provider, group_ref, display_name, selection_mode, effective_selected, effective_score)
VALUES ('sel-delete', 'sub-delete', 'anibt', 'group-stable', 'Display Name', 'auto', $1, 50)
ON CONFLICT (id) DO UPDATE SET effective_selected = EXCLUDED.effective_selected`, selected)
	provenance := `{"provider":"anibt","provider_entry_ref":"entry-1","provider_group_ref":"group-stable","discovery_snapshot_version":"snapshot-1"}`
	f.exec(t, `INSERT INTO rss_sources
(id, subscription_id, source_kind, locator, locator_display, group_name, title, provenance,
 provider, provider_entry_ref, provider_group_ref, discovery_snapshot_version)
VALUES ('src-'||$1, 'sub-delete', 'provider_group', 'https://example.test/'||$1, 'example.test', 'Display Name', 'Show',
 $2::jsonb, 'anibt', 'entry-1', 'group-stable', 'snapshot-1')`, id, provenance)
	f.exec(t, `INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, verdict, reason_code)
VALUES ('rel-'||$1, 'src-'||$1, $1, '{}', '{}', 'accepted_pending_download', 'accepted')`, id)
	writeTestFile(t, f.path(video))
	f.exec(t, `INSERT INTO placements
(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref, source_fingerprint,
 target_fingerprint, quality_facts_ref, facts_version, state, replacement_eligible)
VALUES ($1, $2, 'anime-delete', 'reconciliation', 'obs-'||$1, 'fp', 'fp', 'rel-'||$1, 'parser-v1', 'active', true)`, id, f.path(video))
	for index, companion := range companions {
		companionID := id + "-c" + string(rune('a'+index))
		writeTestFile(t, f.path(companion))
		f.exec(t, `INSERT INTO placement_companions
(id, placement_id, managed_anime_id, source_ref, registration, media_kind, suffix, original_extension,
 current_path, size_bytes, fingerprint, state)
VALUES ($1, $2, 'anime-delete', 'source-'||$1, 'intake', 'subtitle', 'zh', 'ass', $3, 1,
 'meta:size=1', 'active')`, companionID, id, f.path(companion))
	}
}

func deleteErrorCode(err error) string {
	var typed *filemanager.Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}

func (f *deletePlacementFixture) assertDeleteCounts(t *testing.T, placementState string, companionState string, operations int) {
	t.Helper()
	var state string
	if err := f.db.QueryRowContext(f.ctx, `SELECT state FROM placements WHERE id = 'p-main'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != placementState {
		t.Fatalf("placement state = %s, want %s", state, placementState)
	}
	if companionState != "" {
		if err := f.db.QueryRowContext(f.ctx, `SELECT state FROM placement_companions WHERE id = 'p-main-ca'`).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != companionState {
			t.Fatalf("companion state = %s, want %s", state, companionState)
		}
	}
	var count int
	if err := f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM file_manager_operations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != operations {
		t.Fatalf("operation count = %d, want %d", count, operations)
	}
}

func TestDeletePlacementAdmitsWholeUnitAndReplays(t *testing.T) {
	f := newDeletePlacementFixture(t)
	f.seedPlacement(t, "p-main", "Show/ep01.mkv", false, "Show/ep01.zh.ass")
	f.exec(t, `INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
VALUES ('p-main', 'tv:e1', 0, 'parser-v1', 'obs-p-main'), ('p-main', 'tv:e2', 1, 'parser-v1', 'obs-p-main')`)
	request := filemanager.DeletePlacementRequest{
		IdempotencyKey: "delete-placement-1", Actor: "web", PlacementID: "p-main", PlacementVersion: 1, Path: f.path("Show/ep01.mkv"),
	}
	outcome, err := f.service.DeletePlacement(f.ctx, request)
	if err != nil || outcome.Operation == nil || outcome.Released != 1 {
		t.Fatalf("delete placement = %+v, %v", outcome, err)
	}
	f.assertDeleteCounts(t, "released", "ended", 2)
	var items int
	if err := f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM work_items WHERE kind = $1`, workitems.KindFileManagerExecute).Scan(&items); err != nil || items != 2 {
		t.Fatalf("execute items = %d, %v; want video and companion", items, err)
	}

	replay, err := f.service.DeletePlacement(f.ctx, request)
	if err != nil || replay.Operation == nil || replay.Operation.ID != outcome.Operation.ID {
		t.Fatalf("exact replay = %+v, %v; original %+v", replay, err, outcome)
	}
	f.assertDeleteCounts(t, "released", "ended", 2)
	changed := request
	changed.PlacementVersion++
	if _, err := f.service.DeletePlacement(f.ctx, changed); deleteErrorCode(err) != filemanager.CodeIdempotencyConflict {
		t.Fatalf("same key with changed request = %v", err)
	}

	users := filemaintenance.NewUserOperationService(f.db, f.workStore, maintenanceTestFileExecutor(), &noContentObserver{},
		func() filemaintenance.CleanupFileExecutor {
			return &noContentCleanup{inner: maintenanceTestCleanupExecutor(filemaintenance.CleanupAnchor)}
		}, UserOperationGuard{Service: f.service})
	for range 2 {
		f.exec(t, `UPDATE work_items SET available_at = CURRENT_TIMESTAMP WHERE kind = $1 AND state = 'ready'`, workitems.KindFileManagerExecute)
		claim, err := f.workStore.ClaimDue(f.ctx, "delete-test", []string{workitems.KindFileManagerExecute}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		result, err := users.ExecuteHandler().Handle(f.ctx, claim)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.workStore.FinishWithResult(f.ctx, claim, result.TerminalState, result.OwnerResult, result.WriteOwner); err != nil {
			t.Fatal(err)
		}
		op, err := f.service.Operation(f.ctx, claim.Owner.ID)
		if err != nil || op.State != filemanager.UserOperationCompleted {
			t.Fatalf("delete operation = %+v, %v", op, err)
		}
	}
	for _, path := range []string{f.path("Show/ep01.mkv"), f.path("Show/ep01.zh.ass")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("deleted file remains at %s: %v", path, err)
		}
	}
}

func TestDeletePlacementAuthorizationAndRollback(t *testing.T) {
	for _, test := range []struct {
		name     string
		selected bool
		unknown  bool
		code     string
	}{
		{name: "selected group", selected: true, code: filemanager.CodeGroupSelected},
		{name: "unselected group", selected: false},
		{name: "unknown group", unknown: true, code: filemanager.CodeGroupSelected},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newDeletePlacementFixture(t)
			f.seedPlacement(t, "p-main", "Show/ep01.mkv", test.selected, "Show/ep01.zh.ass")
			if test.unknown {
				f.exec(t, `DELETE FROM provider_group_selections WHERE id = 'sel-delete'`)
			}
			_, err := f.service.DeletePlacement(f.ctx, filemanager.DeletePlacementRequest{
				IdempotencyKey: "delete-" + test.name, Actor: "web", PlacementID: "p-main", PlacementVersion: 1, Path: f.path("Show/ep01.mkv"),
			})
			if deleteErrorCode(err) != test.code {
				t.Fatalf("delete error = %v, want %s", err, test.code)
			}
			if test.code != "" {
				f.assertDeleteCounts(t, "active", "active", 0)
				if _, err := os.Stat(f.path("Show/ep01.mkv")); err != nil {
					t.Fatalf("refusal changed the video: %v", err)
				}
			}
		})
	}
}

func deleteDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// directAdmission reaches the transaction with an already-read unit, allowing
// the test to change companion state after that read as a concurrent writer would.
func (f *deletePlacementFixture) directAdmission(t *testing.T, key string, companionVersion int64) filemanager.ReleaseAdmission {
	t.Helper()
	video := f.path("Show/ep01.mkv")
	companion := f.path("Show/ep01.zh.ass")
	videoAdmission := filemanager.OperationAdmission{
		ID: "op-video-" + key, IdempotencyKey: key, Digest: deleteDigest(key + ":video"), Actor: "web",
		Operation: filemanager.UserDelete, SourcePath: video, SourceSpellings: []string{video}, ExpectedSize: 1,
		Collision: filemanager.ConflictBlock, RequireDetachedSource: true, ReleasePlacementID: "p-main",
	}
	companionAdmission := filemanager.OperationAdmission{
		ID: "op-companion-" + key, IdempotencyKey: key + ":companion", Digest: deleteDigest(key + ":companion"), Actor: "web",
		Operation: filemanager.UserDelete, SourcePath: companion, SourceSpellings: []string{companion}, ExpectedSize: 1,
		Collision: filemanager.ConflictBlock, RequireDetachedSource: true, ReleasePlacementID: "p-main",
	}
	return filemanager.ReleaseAdmission{
		IdempotencyKey: key, Actor: "web", ManagedAnimeID: "anime-delete", AnimeTitle: "Show",
		Placement: filemanager.VersionRef{ID: "p-main", Version: 1}, Spellings: []string{video},
		Delete: &filemanager.ReleaseDeleteAdmission{Video: videoAdmission, Companions: []filemanager.ReleaseCompanionDelete{{
			Companion: filemanager.VersionRef{ID: "p-main-ca", Version: companionVersion}, Path: companion, Operation: companionAdmission,
		}}},
	}
}

func TestDeletePlacementRejectsChangedUnitAndSharedPathWithoutEffects(t *testing.T) {
	t.Run("stale companion", func(t *testing.T) {
		f := newDeletePlacementFixture(t)
		f.seedPlacement(t, "p-main", "Show/ep01.mkv", false, "Show/ep01.zh.ass")
		_, err := f.commands.Release(f.ctx, f.directAdmission(t, "stale-companion", 2))
		if deleteErrorCode(err) != filemanager.CodeEntryChanged {
			t.Fatalf("stale companion error = %v", err)
		}
		f.assertDeleteCounts(t, "active", "active", 0)
	})

	t.Run("shared active path", func(t *testing.T) {
		f := newDeletePlacementFixture(t)
		f.seedPlacement(t, "p-main", "Show/ep01.mkv", false, "Show/ep01.zh.ass")
		f.exec(t, `INSERT INTO placements
(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref, source_fingerprint,
 target_fingerprint, quality_facts_ref, facts_version, state, replacement_eligible)
VALUES ('p-other', $1, 'anime-delete', 'reconciliation', 'obs-p-other', 'fp', 'fp', 'rel-p-main', 'parser-v1', 'active', true)`, f.path("Show/ep01.mkv"))
		_, err := f.commands.Release(f.ctx, f.directAdmission(t, "shared-path", 1))
		if deleteErrorCode(err) != filemanager.CodeEntryChanged {
			t.Fatalf("shared path error = %v", err)
		}
		f.assertDeleteCounts(t, "active", "active", 0)
	})

	t.Run("companion admission rolls back video and release", func(t *testing.T) {
		f := newDeletePlacementFixture(t)
		f.seedPlacement(t, "p-main", "Show/ep01.mkv", false, "Show/ep01.zh.ass")
		admission := f.directAdmission(t, "admission-rollback", 1)
		// Reuse the companion's key with another digest. Video admission succeeds
		// first; the companion conflict must roll back both it and the release.
		f.exec(t, `INSERT INTO file_manager_operations
(id, idempotency_key, request_digest, actor, operation, source_path, expected_size_bytes, collision)
VALUES ('op-existing', 'admission-rollback:companion', $1, 'web', 'delete', '/detached/other', 1, 'block')`, deleteDigest("another-request"))
		_, err := f.commands.Release(f.ctx, admission)
		if deleteErrorCode(err) != filemanager.CodeIdempotencyConflict {
			t.Fatalf("companion admission error = %v", err)
		}
		f.assertDeleteCounts(t, "active", "active", 1)
		var videoOperations int
		if err := f.db.QueryRowContext(f.ctx, `SELECT count(*) FROM file_manager_operations WHERE id = 'op-video-admission-rollback'`).Scan(&videoOperations); err != nil || videoOperations != 0 {
			t.Fatalf("rolled back video operations = %d, %v", videoOperations, err)
		}
	})
}
