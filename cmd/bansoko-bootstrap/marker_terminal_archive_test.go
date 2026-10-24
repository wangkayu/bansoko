package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	updatefence "bansoko/internal/update/fence"
	journal "bansoko/internal/update/journal"
)

type previousTerminalFence struct {
	*recordingMarkerFence
	previous updatefence.Row
}

func (f *previousTerminalFence) Lookup(ctx context.Context, id string) (updatefence.Row, bool, error) {
	if id == f.previous.OperationID {
		return f.previous, true, nil
	}
	return f.recordingMarkerFence.Lookup(ctx, id)
}

func TestConsumeNextMarkerArchivesOnlyExactProvenTerminal(t *testing.T) {
	for _, mutant := range []string{"valid", "owner", "digest", "sequence", "open", "state", "missing-proof"} {
		t.Run(mutant, func(t *testing.T) {
			root := markerRoot(t)
			marker := markerForRelease(t, root, markerTargetRelease, markerPreviousRelease, "next-update-op")
			writePendingMarker(t, root, marker)
			store, err := journal.Open(filepath.Join(root, journalFileName))
			if err != nil {
				t.Fatal(err)
			}
			record, err := store.Create(journal.Record{OperationID: "previous-update-op", Channel: "embedded", Step: journal.StepPendingMigration, TargetReleaseID: markerPreviousRelease, TargetReleaseSequence: 1, ManifestDigest: marker.ManifestDigest})
			if err != nil {
				t.Fatal(err)
			}
			for _, step := range []journal.Step{journal.StepMigrationSucceeded, journal.StepPromoted, journal.StepSucceeded} {
				record, err = store.Advance(record.OperationID, record.Version, step, "", "fixture", func(r *journal.Record) {
					if step == journal.StepSucceeded {
						r.FenceClosure = journal.FenceClosureClosed
					}
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			store.Close()
			before, err := os.ReadFile(filepath.Join(root, journalFileName))
			if err != nil {
				t.Fatal(err)
			}
			row := updatefence.Row{OperationID: record.OperationID, AdmittedBy: "embedded", Sequence: 1, ManifestDigest: record.ManifestDigest, State: "settled", Released: true}
			switch mutant {
			case "owner":
				row.AdmittedBy = "other"
			case "digest":
				row.ManifestDigest = "wrong"
			case "sequence":
				row.Sequence++
			case "open":
				row.Released = false
			case "state":
				row.State = "failed"
			case "missing-proof":
				before = bytes.Replace(before, []byte(`"fence_closure":"closed"`), []byte(`"fence_closure":""`), 1)
				// The encoder indents JSON, so use the actual encoded spacing as well.
				before = bytes.Replace(before, []byte(`"fence_closure": "closed"`), []byte(`"fence_closure": ""`), 1)
				if err := os.WriteFile(filepath.Join(root, journalFileName), before, 0600); err != nil {
					t.Fatal(err)
				}
			}
			fence := &previousTerminalFence{recordingMarkerFence: &recordingMarkerFence{}, previous: row}
			backup := &recordingMarkerBackup{}
			previousFence, previousBackup := openMarkerFence, writeMarkerBackup
			openMarkerFence = func(*sql.DB) markerFence { return fence }
			writeMarkerBackup = backup.write
			defer func() { openMarkerFence = previousFence; writeMarkerBackup = previousBackup }()
			t.Setenv("BANSOKO_DATABASE_URL", markerTestDSN)
			err = consumePendingMarker(root)
			archives, _ := filepath.Glob(filepath.Join(root, "archive", "journal-*.json"))
			if mutant != "valid" {
				if err == nil {
					t.Fatal("invalid previous terminal proof accepted")
				}
				after, _ := os.ReadFile(filepath.Join(root, journalFileName))
				if !bytes.Equal(before, after) || len(archives) != 0 || backup.calls != 0 || len(fence.acquires) != 0 {
					t.Fatal("refusal altered history or acquired new effects")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(archives) != 1 {
				t.Fatalf("archives=%v", archives)
			}
			archived, _ := os.ReadFile(archives[0])
			if !bytes.Equal(before, archived) {
				t.Fatal("previous evidence was rewritten")
			}
			next, found, err := journal.Snapshot(filepath.Join(root, journalFileName))
			if err != nil || !found || next.OperationID != marker.OperationID || next.Step != journal.StepPendingMigration {
				t.Fatalf("next=%+v found=%v err=%v", next, found, err)
			}
			if backup.calls != 1 || backup.journalBefore {
				t.Fatal("backup must follow proven archive and precede next journal")
			}
		})
	}
}
