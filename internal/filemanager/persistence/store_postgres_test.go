package persistence

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bansoko/internal/filemanager"
	"bansoko/internal/testutil/disposabledb"
)

// TestStoreDerivesStatesFromRealOwnerRows proves the file manager's state
// derivation over real placement, companion, lifecycle, one-shot and
// non-terminal owner rows, browsing a real library directory read-only.
func TestStoreDerivesStatesFromRealOwnerRows(t *testing.T) {
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
	abs := func(rel string) string { return filepath.Join(tree, filepath.FromSlash(rel)) }
	for _, rel := range []string{
		"Show/ep01.mkv", "Show/ep01.zh.ass", "Show/ep02.mkv", "Show/ep02.zh.ass", "Show/old.mkv",
		"Show/extras/extra.mkv", "Ended/e01.mkv", "Movie/movie.mkv", "Movie/movie.zh.ass",
		"Loose/external.mkv", "Loose/old.zh.ass", "Cleanup/q.mkv",
	} {
		writeTestFile(t, abs(rel))
	}
	writeTestFile(t, filepath.Join(downloads, "Show", "ep03.mkv"))

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := owner.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", strings.TrimSpace(query), err)
		}
	}
	exec(`INSERT INTO connections(id, kind, name, endpoint, secret_ref, state, config)
VALUES ('qb-main', 'qbittorrent', 'Main', 'http://qb.local', 'secret-qb', 'configured', $1::jsonb)`,
		`{"username":"admin","category":"anime","save_root":"/remote/downloads","downloader_root":"`+downloads+`","user_tags":[]}`)
	exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-a', 'Show A'), ('anime-r', 'Show R'), ('anime-o', 'Movie O')`)
	exec(`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ('sub-a', 'anime-a', 'custom', 'Show A', 'stopped'), ('sub-r', 'anime-r', 'custom', 'Show R', 'stopped'),
       ('sub-o', 'anime-o', 'custom', 'Movie O', 'stopped')`)
	placement := func(id, path, anime, state string, eligible bool) {
		exec(`INSERT INTO placements
(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref, source_fingerprint,
 target_fingerprint, facts_version, state, replacement_eligible)
VALUES ($1, $2, $3, 'reconciliation', 'obs-'||$1, 'fp', 'fp', 'parser-v1', $4, $5)`, id, path, anime, state, eligible)
	}
	placement("p-managed", abs("Show/ep01.mkv"), "anime-a", "active", true)
	placement("p-busy", abs("Show/ep02.mkv"), "anime-a", "active", true)
	placement("p-old", abs("Show/old.mkv"), "anime-a", "superseded", true)
	// An unknown file is released at admission: it is not managed.
	placement("p-extra", abs("Show/extras/extra.mkv"), "anime-a", "active", false)
	exec(`UPDATE placements SET state = 'released', released_at = CURRENT_TIMESTAMP,
 released_by = 'system:unknown_file_import', release_request_key = 'unknown-file:p-extra' WHERE id = 'p-extra'`)
	placement("p-retired", abs("Ended/e01.mkv"), "anime-r", "active", true)
	placement("p-oneshot", abs("Movie/movie.mkv"), "anime-o", "active", true)
	companion := func(id, placementID, anime, path, state string) {
		var reason any
		if state == "ended" {
			reason = "replaced"
		}
		exec(`INSERT INTO placement_companions
(id, placement_id, managed_anime_id, source_ref, registration, media_kind, suffix, original_extension,
 current_path, size_bytes, fingerprint, state, ended_reason)
VALUES ($1, $2, $3, 'src-'||$1, 'intake', 'subtitle', 'zh', 'ass', $4, 1, $5, $6, $7)`,
			id, placementID, anime, path, "meta:size=1", state, reason)
	}
	companion("c-managed", "p-managed", "anime-a", abs("Show/ep01.zh.ass"), "active")
	companion("c-busy", "p-busy", "anime-a", abs("Show/ep02.zh.ass"), "active")
	companion("c-ended", "p-managed", "anime-a", abs("Loose/old.zh.ass"), "ended")
	companion("c-oneshot", "p-oneshot", "anime-o", abs("Movie/movie.zh.ass"), "active")
	exec(`INSERT INTO one_shot_acquisitions(
    id, managed_anime_id, subscription_id, rss_source_id, release_id, download_task_id,
    correlation_id, request_idempotency_key, request_hash, result_snapshot, state)
VALUES ('os-1', 'anime-o', 'sub-o', 'src-o', 'rel-o', 'task-o', 'corr-o', 'req-key-os-1', $1, '{}'::jsonb, 'running')`,
		"sha256:"+strings.Repeat("0", 64))
	exec(`INSERT INTO alignment_operations
(id, idempotency_key, managed_anime_id, scan_generation, observation_ref, candidate_ref, policy_version,
 metadata_version, rules_version, naming_version, source_path, source_fingerprint, target_path,
 target_contract, dry_run_result, state, version, created_at, updated_at)
VALUES ('alg-busy', 'align:busy', 'anime-a', 1, 'obs-busy', 'c', 1, 1, 1, 1,
 $1, 'fp', $2, '{}', '{}', 'planned', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, abs("Show/ep02.mkv"), abs("Show/Season 01/ep02.mkv"))
	exec(`INSERT INTO replacement_deletions
(id, idempotency_key, placement_id, managed_anime_id, coverage_keys, replacement_ref,
 admission_mode, source_path, source_fingerprint, state, version, created_at, updated_at)
VALUES ('cln-q', 'cln:q', 'p-old', 'anime-a', '["tv:e1"]', 'dec-q',
 'single_copy', $1, 'fp', 'admitted', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, abs("Cleanup/q.mkv"))
	// Retire anime-r last: its owner rows are fenced once it is retired.
	exec(`UPDATE managed_anime SET lifecycle_state = 'retired', fence_epoch = fence_epoch + 1,
 retired_at = CURRENT_TIMESTAMP, retired_subscription_version = 1, lifecycle_version = lifecycle_version + 1
WHERE id = 'anime-r'`)

	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	service, err := filemanager.NewService(store, filemanager.NewDenialZone(filepath.Join(base, "appdata")))
	if err != nil {
		t.Fatal(err)
	}

	// The quick entries: the filesystem root, the download connection's
	// application root, and one tree top per work that owns an active
	// placement. A work with no placement evidence has no tree top.
	mounts, err := service.Mounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 5 || !mounts[0].Available || mounts[0].Key != filemanager.RootMountKey ||
		mounts[0].Kind != filemanager.MountRoot || mounts[0].Path != "/" ||
		!mounts[1].Available || mounts[1].Key != "download:qb-main" || mounts[1].Kind != filemanager.MountDownload ||
		mounts[1].ConnectionID != "qb-main" || mounts[1].Path != downloads || mounts[1].Label != "Main" ||
		!mounts[2].Available || mounts[2].Key != "anime:anime-a" || mounts[2].Path != abs("Show") || mounts[2].Label != "Show A" ||
		!mounts[3].Available || mounts[3].Key != "anime:anime-o" || mounts[3].Path != abs("Movie") || mounts[3].Label != "Movie O" ||
		!mounts[4].Available || mounts[4].Key != "anime:anime-r" || mounts[4].Path != abs("Ended") || mounts[4].Label != "Show R" {
		t.Fatalf("mounts = %+v", mounts)
	}

	states := func(directory string) map[string]filemanager.Entry {
		t.Helper()
		listing, err := service.List(ctx, abs(directory), "", 200)
		if err != nil {
			t.Fatal(err)
		}
		result := map[string]filemanager.Entry{}
		for _, entry := range listing.Entries {
			result[entry.Name] = entry
		}
		return result
	}
	expect := func(directory string, want map[string]filemanager.State) {
		t.Helper()
		got := states(directory)
		for name, state := range want {
			if got[name].State != state {
				t.Fatalf("%s/%s: state %s, want %s (%+v)", directory, name, got[name].State, state, got[name])
			}
		}
	}
	expect("", map[string]filemanager.State{
		"Show": filemanager.StateInProgress, "Ended": filemanager.StateDetached, "Movie": filemanager.StateDetached,
		"Loose": filemanager.StateDetached, "Cleanup": filemanager.StateInProgress,
	})
	expect("Show", map[string]filemanager.State{
		"ep01.mkv": filemanager.StateManaged, "ep01.zh.ass": filemanager.StateManaged,
		"ep02.mkv": filemanager.StateInProgress, "ep02.zh.ass": filemanager.StateInProgress,
		"old.mkv": filemanager.StateDetached, "extras": filemanager.StateDetached,
	})
	expect("Show/extras", map[string]filemanager.State{"extra.mkv": filemanager.StateDetached})
	expect("Ended", map[string]filemanager.State{"e01.mkv": filemanager.StateDetached})
	expect("Movie", map[string]filemanager.State{"movie.mkv": filemanager.StateDetached, "movie.zh.ass": filemanager.StateDetached})
	expect("Loose", map[string]filemanager.State{"external.mkv": filemanager.StateDetached, "old.zh.ass": filemanager.StateDetached})
	expect("Cleanup", map[string]filemanager.State{"q.mkv": filemanager.StateInProgress})
	if subtitle := states("Show")["ep01.zh.ass"]; subtitle.Role != filemanager.RoleSubtitle || subtitle.CompanionOf != abs("Show/ep01.mkv") || subtitle.NotAligned {
		t.Fatalf("subtitle follows its video: %+v", subtitle)
	}

	detail, err := service.Detail(ctx, abs("Show/ep01.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if detail.Anime == nil || detail.Anime.ID != "anime-a" || detail.Anime.Title != "Show A" || detail.Anime.SubscriptionState != "stopped" ||
		len(detail.Companions) != 1 || detail.Companions[0].Path != abs("Show/ep01.zh.ass") || !detail.Companions[0].Present {
		t.Fatalf("managed video detail = %+v", detail)
	}
	for path, reason := range map[string]filemanager.DetachedReason{
		"Ended/e01.mkv":      filemanager.DetachedSubscriptionEnded,
		"Movie/movie.mkv":    filemanager.DetachedOneShot,
		"Show/old.mkv":       filemanager.DetachedReplaced,
		"Loose/old.zh.ass":   filemanager.DetachedCompanionEnded,
		"Loose/external.mkv": filemanager.DetachedNoRecord,
	} {
		detail, err := service.Detail(ctx, abs(path))
		if err != nil || detail.DetachedReason != reason {
			t.Fatalf("%s: reason %q, want %q (%v)", path, detail.DetachedReason, reason, err)
		}
	}
	busy, err := service.Detail(ctx, abs("Show/ep02.zh.ass"))
	if err != nil || len(busy.InProgress) != 1 || busy.InProgress[0].Kind != filemanager.InProgressAlignment {
		t.Fatalf("a subtitle inherits its video's alignment owner: %+v, %v", busy, err)
	}
	cleanup, err := service.Detail(ctx, abs("Cleanup/q.mkv"))
	if err != nil || len(cleanup.InProgress) != 1 || cleanup.InProgress[0].Kind != filemanager.InProgressCleanup {
		t.Fatalf("an admitted cleanup holds its source: %+v, %v", cleanup, err)
	}

	location, err := service.Locate(ctx, "anime-a")
	if err != nil || location.Path != abs("Show") {
		t.Fatalf("locate = %+v, %v", location, err)
	}
	listing, err := service.List(ctx, filepath.Join(downloads, "Show"), "", 0)
	if err != nil || len(listing.Entries) != 1 || listing.Entries[0].State != filemanager.StateDetached {
		t.Fatalf("download root listing = %+v, %v", listing.Entries, err)
	}

	// The reader never writes: no owner row changed.
	var placements, companions int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM placements WHERE version = 1), (SELECT count(*) FROM placement_companions WHERE version = 1)`).Scan(&placements, &companions); err != nil {
		t.Fatal(err)
	}
	if placements != 6 || companions != 4 {
		t.Fatalf("owner rows changed: placements=%d companions=%d", placements, companions)
	}
}

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}
