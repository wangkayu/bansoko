package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/testutil/disposabledb"
)

func TestPGTransientResidueSweepClearsRecordedDirectoriesOnly(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	root := t.TempDir()
	if _, err := db.Exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-a', 'Show A')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, facts_version, state)
VALUES ('plc-a-1', $1, 'anime-a', 'reconciliation', 'obs-1', 'fp', 'fp', 'parser-v1', 'active')`,
		filepath.Join(root, "Show A - E01.mkv")); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	residue := filepath.Join(root, ".bansoko-permit-"+strings.Repeat("a", 64))
	fresh := filepath.Join(root, ".bansoko-copy-"+strings.Repeat("b", 64))
	writeResidueFile(t, residue, old)
	writeResidueFile(t, fresh, time.Now())
	unrecorded := t.TempDir()
	outside := filepath.Join(unrecorded, ".bansoko-permit-"+strings.Repeat("c", 64))
	writeResidueFile(t, outside, old)

	runTransientResidueSweep(context.Background(), db, observabilityport.NopRuntimeLogger{})

	if _, err := os.Lstat(residue); !os.IsNotExist(err) {
		t.Fatalf("recorded-directory residue must be removed: %v", err)
	}
	if _, err := os.Lstat(fresh); err != nil {
		t.Fatalf("a fresh temporary must be left alone: %v", err)
	}
	if _, err := os.Lstat(outside); err != nil {
		t.Fatalf("a directory no recorded path names must not be swept: %v", err)
	}
}

func writeResidueFile(t *testing.T, path string, modified time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}
