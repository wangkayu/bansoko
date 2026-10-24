package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"bansoko/internal/testutil/disposabledb"
	updatefence "bansoko/internal/update/fence"
	journal "bansoko/internal/update/journal"
)

// Real owner-scoped PG closure and new admission, real signed trees, pointer,
// archive and journal. Only dump/restore is scripted here; Docker acceptance
// independently exercises the real backup toolchain.
func TestPGConsecutiveMarkerConsumesPreserveClosedHistory(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	root := markerRoot(t)
	marker := markerForRelease(t, root, markerTargetRelease, markerPreviousRelease, "pg-next-op")
	writePendingMarker(t, root, marker)
	ctx := context.Background()
	fence := updatefence.New(db)
	if err := fence.Acquire(ctx, "pg-previous-op", "embedded", 1, marker.ManifestDigest); err != nil {
		t.Fatal(err)
	}
	if err := fence.ReconcileExactCurrentState(ctx, "pg-previous-op", "embedded", marker.ManifestDigest, 1, "succeeded"); err != nil {
		t.Fatal(err)
	}
	j, err := journal.Open(filepath.Join(root, journalFileName))
	if err != nil {
		t.Fatal(err)
	}
	record, err := j.Create(journal.Record{OperationID: "pg-previous-op", Channel: "embedded", Step: journal.StepPendingMigration, TargetReleaseID: markerPreviousRelease, TargetReleaseSequence: 1, ManifestDigest: marker.ManifestDigest})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []journal.Step{journal.StepMigrationSucceeded, journal.StepPromoted, journal.StepSucceeded} {
		record, err = j.Advance(record.OperationID, record.Version, step, "", "verified fixture", func(r *journal.Record) {
			if step == journal.StepSucceeded {
				r.FenceClosure = journal.FenceClosureClosed
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	j.Close()
	before, err := os.ReadFile(filepath.Join(root, journalFileName))
	if err != nil {
		t.Fatal(err)
	}
	oldFence, oldBackup := openMarkerFence, writeMarkerBackup
	openMarkerFence = func(*sql.DB) markerFence { return fence }
	backup := &recordingMarkerBackup{}
	writeMarkerBackup = backup.write
	defer func() { openMarkerFence = oldFence; writeMarkerBackup = oldBackup }()
	t.Setenv("BANSOKO_DATABASE_URL", os.Getenv("BANSOKO_TEST_DATABASE_URL"))
	if err := consumePendingMarker(root); err != nil {
		t.Fatal(err)
	}
	archives, err := filepath.Glob(filepath.Join(root, "archive", "journal-*.json"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("archives=%v err=%v", archives, err)
	}
	retained, _ := os.ReadFile(archives[0])
	if !bytes.Equal(before, retained) {
		t.Fatal("closed history changed")
	}
	next, found, err := journal.Snapshot(filepath.Join(root, journalFileName))
	if err != nil || !found || next.OperationID != marker.OperationID || next.Step != journal.StepPendingMigration {
		t.Fatalf("next=%+v found=%v err=%v", next, found, err)
	}
	previous, present, err := fence.Lookup(ctx, "pg-previous-op")
	if err != nil || !present || !previous.Released || previous.State != "settled" {
		t.Fatalf("previous=%+v present=%v err=%v", previous, present, err)
	}
	rows, err := fence.ListOpen(ctx, "embedded")
	if err != nil || len(rows) != 1 || rows[0].OperationID != marker.OperationID || rows[0].Sequence != marker.ReleaseSequence {
		t.Fatalf("open=%+v err=%v", rows, err)
	}
	if backup.calls != 1 || backup.journalBefore {
		t.Fatal("backup preceded proven archive")
	}
}
