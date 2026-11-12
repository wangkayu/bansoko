package migrations

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// installedMigrationsManifest is the migration list of the last package an
// installed database is known to have applied (test package 0.1.4-test.226,
// source ce4d88295). Apply verifies every recorded checksum before it runs
// anything, so editing one of these files makes the next update fail with
// checksum_mismatch and roll back. A schema change belongs in a new
// forward-only migration; only a deliberate baseline reset replaces this
// list.
const installedMigrationsManifest = "testdata/installed-migrations-v0.1.4-test.226.json"

func TestInstalledMigrationsStayByteIdentical(t *testing.T) {
	raw, err := os.ReadFile(installedMigrationsManifest)
	if err != nil {
		t.Fatal(err)
	}
	var installed []struct {
		ID       string `json:"id"`
		Checksum string `json:"checksum"`
	}
	if err := json.Unmarshal(raw, &installed); err != nil {
		t.Fatalf("parse installed migration manifest: %v", err)
	}
	dir := migrationsDir(t)
	for _, item := range installed {
		contents, err := os.ReadFile(filepath.Join(dir, item.ID))
		if err != nil {
			t.Errorf("%s is missing; an installed database still records it and the next update would refuse to start", item.ID)
			continue
		}
		if got := checksum(contents); got != "sha256:"+item.Checksum {
			t.Errorf("%s content changed (%s, installed databases recorded %s); editing an applied migration breaks every upgrade with checksum_mismatch, so put the change in a new forward migration", item.ID, got, "sha256:"+item.Checksum)
		}
	}
}
