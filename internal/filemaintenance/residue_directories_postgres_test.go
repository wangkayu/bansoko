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
	ctx := context.Background()
	seed := func(id, ownerType, ownerID, kind, state string) {
		t.Helper()
		var lease any
		if state == "leased" {
			lease = "2099-01-01T00:00:00Z"
		}
		if _, err := db.Exec(`
INSERT INTO work_items (id, owner_type, owner_id, kind, state, lease_until, idempotency_key, max_attempts, input_snapshot, expected_owner_version)
VALUES ($1,$2,$3,$4,$5,$6,$1,3,'{}',1)`,
			id, ownerType, ownerID, kind, state, lease); err != nil {
			t.Fatal(err)
		}
	}
	seed("wi-del-available", "replacement_deletion", "del-available", "replacement_delete", "available")
	seed("wi-del-leased", "replacement_deletion", "del-leased", "replacement_delete", "leased")
	seed("wi-fm-available", "file_manager_operation", "fm-available", "file_manager_execute", "available")
	// A live item of another owner type never owns a delete namespace.
	seed("wi-release", "release", "rel-live", "release_evaluate", "available")

	protected, err := TransientResidueProtectedOwners(ctx, db, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"del-available", "del-leased", "fm-available"} {
		if _, ok := protected[owner]; !ok {
			t.Fatalf("live delete owner %q is not protected: %v", owner, protected)
		}
	}
	if _, ok := protected["rel-live"]; ok {
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
