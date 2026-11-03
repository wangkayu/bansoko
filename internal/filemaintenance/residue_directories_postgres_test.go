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
