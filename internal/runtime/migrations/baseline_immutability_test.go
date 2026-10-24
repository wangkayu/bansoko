package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installedBaselineSHA256 is the checksum every database that installed the
// baseline recorded in schema_migrations. cmd/bansoko-migrate re-hashes the
// file it finds on disk and compares it with that record before applying
// anything else, so editing the baseline does not upgrade an installed
// database: it refuses to start with migration_apply_failed
// (checksum_mismatch). A schema change belongs in a forward-only migration.
// A deliberate reset updates this constant together with the
// baseline it re-created.
const installedBaselineSHA256 = "f0b2505e38f949c8e2ccda581a429a41fe6a08f86003f2e8f31d3e9a172722fb"

// This guard runs without a database, because a fresh test database is built
// from whatever baseline the tree carries and would therefore hide exactly the
// edit that stops an installed database from starting.
func TestBaselineStaysByteIdenticalToInstalledCopies(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(migrationsDir(t), baselineMigrationFile))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != installedBaselineSHA256 {
		t.Fatalf("%s sha256 = %s, want the installed checksum %s; editing the immutable baseline breaks every installed database with checksum_mismatch, so the change belongs in a forward-only migration", baselineMigrationFile, got, installedBaselineSHA256)
	}
}

// The parser kind identity was renamed from "traditional" to "regex" after
// databases had applied the baseline. The rename is carried by 012 and covers
// every surface a running server validates: the singleton creation template and
// the frozen per-instance axis. The baseline keeps the value it was installed
// with, and historical evidence (candidate envelopes, media fact snapshots)
// keeps the name it was written with.
func TestParserKindRenameArrivesAsAForwardMigration(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(migrationsDir(t), "012_parser_policy_regex.sql"))
	if err != nil {
		t.Fatal(err)
	}
	requireFragments(t, string(raw), "parser kind rename migration",
		"UPDATE global_config_sections",
		`jsonb_set(value, '{parser_kind}', '"regex"'::jsonb)`,
		"UPDATE anime_business_configs",
		"SET parser_kind = 'regex'",
		"WHERE parser_kind = 'traditional'",
	)

	baseline, err := os.ReadFile(filepath.Join(migrationsDir(t), baselineMigrationFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(baseline), `{"parser_kind": "traditional"}`) {
		t.Fatalf("%s no longer carries the seed value it was installed with; the rename belongs to the forward migration", baselineMigrationFile)
	}
}
