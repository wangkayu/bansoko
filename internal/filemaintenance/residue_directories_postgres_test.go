package filemaintenance

import (
	"context"
	"testing"

	"bansoko/internal/testutil/disposabledb"
)

func TestPGTransientResidueDirectoriesDedupesPlacementTrees(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()
	seedSchedulerInstance(t, db, "anime-a", "active")
	seedSchedulerPlacement(t, db, "anime-a", "plc-a-1", "/media/Show A/Season 01/Show A - E01.mkv")
	seedSchedulerPlacement(t, db, "anime-a", "plc-a-2", "/media/Show A/Season 01/Show A - E02.mkv")
	seedSchedulerPlacement(t, db, "anime-a", "plc-a-3", "/media/Show A/Show A - OVA.mkv")

	dirs, err := TransientResidueDirectories(ctx, db, 100)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool, len(dirs))
	for _, dir := range dirs {
		if got[dir] {
			t.Fatalf("directory %q listed twice: %v", dir, dirs)
		}
		got[dir] = true
	}
	if !got["/media/Show A/Season 01"] || !got["/media/Show A"] || len(dirs) != 2 {
		t.Fatalf("unexpected directories: %v", dirs)
	}

	limited, err := TransientResidueDirectories(ctx, db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || !got[limited[0]] {
		t.Fatalf("a bounded read must stay within the recorded paths: %v", limited)
	}
}

func TestPGTransientResidueProtectedOwnersListsLiveDeletes(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	// The runtime role reads work items but never writes them; the rows are
	// seeded through the schema owner, as the queue's own writers would.
	ownerDB := disposabledb.Owner(t)
	ctx := context.Background()
	seed := func(id, ownerType, ownerID, kind, state string) {
		t.Helper()
		var lease any
		if state == "leased" {
			lease = "2099-01-01T00:00:00Z"
		}
		if _, err := ownerDB.Exec(`
INSERT INTO work_items (id, owner_type, owner_id, kind, state, lease_until, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
VALUES ($1,$2,$3,$4,$5,$6,$1,3,'{}',1)`,
			id, ownerType, ownerID, kind, state, lease); err != nil {
			t.Fatal(err)
		}
	}
	// The retirement fence admits a work item only when its owner row exists.
	if _, err := ownerDB.Exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-a', 'Show')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"del-available", "del-leased"} {
		if _, err := ownerDB.Exec(`
INSERT INTO replacement_deletions (id, idempotency_key, placement_id, managed_anime_id, coverage_keys, replacement_ref, admission_mode, source_path, source_fingerprint)
VALUES ($1,$1,'plc-a-1','anime-a','[]','replacement','single_copy','/media/Show A/old.mkv','fp')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ownerDB.Exec(`
INSERT INTO file_manager_operations (id, idempotency_key, request_digest, actor, operation, source_path, target_path, expected_size_bytes, collision)
VALUES ('fm-available','fm-available',repeat('a',64),'owner','rename','/media/a.mkv','/media/b.mkv',1,'block')`); err != nil {
		t.Fatal(err)
	}
	seed("wi-del-available", "replacement_deletion", "del-available", "replacement_delete", "available")
	seed("wi-del-leased", "replacement_deletion", "del-leased", "replacement_delete", "leased")
	seed("wi-fm-available", "file_manager_operation", "fm-available", "file_manager_execute", "available")
	// A live item of another owner type never owns a delete namespace.
	seed("wi-maintenance", "file_maintenance", "anime-a", "replacement_evaluate", "available")

	protected, err := TransientResidueProtectedOwners(ctx, db, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"del-available", "del-leased", "fm-available"} {
		if _, ok := protected[owner]; !ok {
			t.Fatalf("live delete owner %q is not protected: %v", owner, protected)
		}
	}
	if _, ok := protected["anime-a"]; ok {
		t.Fatalf("a non-delete owner is protected: %v", protected)
	}

	limited, err := TransientResidueProtectedOwners(ctx, db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 {
		t.Fatalf("a bounded read returned %d owners: %v", len(limited), limited)
	}
	for owner := range limited {
		if _, ok := protected[owner]; !ok {
			t.Fatalf("a bounded read returned an unknown owner %q", owner)
		}
	}
}
