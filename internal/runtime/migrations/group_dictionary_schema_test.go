package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The subgroup dictionary's user layer is a new relation, so it arrives as a
// forward-only migration. It must not be folded into 001_baseline.sql: the
// baseline is immutable, and an installed database verifies the checksum of
// every migration it has already applied before it applies anything else, so an
// edit to 001 would stop an existing install from starting instead of giving it
// the table. Only a full reset would hide that, which is why this guard is
// static and runs without a database.
func TestGroupDictionaryArrivesAsAForwardMigration(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(migrationsDir(t), "006_group_dictionary.sql"))
	if err != nil {
		t.Fatal(err)
	}
	requireFragments(t, string(raw), "group dictionary migration",
		"CREATE TABLE provider_group_dictionary",
		"provider_group_dictionary_provider_provider_group_ref_key UNIQUE (provider, provider_group_ref)",
		"provider_group_dictionary_provider_check CHECK (provider IN ('anibt', 'mikan'))",
		"provider_group_dictionary_source_check CHECK (source IN ('collected', 'user'))",
		"provider_group_dictionary_version_check CHECK (version >= 1)",
		"GRANT SELECT, INSERT, DELETE, UPDATE ON TABLE provider_group_dictionary TO bansoko_runtime",
	)
	// The columns the catalog's repository reads and writes by name.
	for _, column := range []string{
		"provider TEXT NOT NULL", "provider_group_ref TEXT NOT NULL", "display_name TEXT NOT NULL",
		"available BOOLEAN NOT NULL", "source TEXT NOT NULL", "added_at TIMESTAMPTZ NOT NULL", "version BIGINT NOT NULL",
	} {
		if !strings.Contains(string(raw), column) {
			t.Errorf("group dictionary migration is missing the column %q", column)
		}
	}

	baseline, err := os.ReadFile(filepath.Join(migrationsDir(t), baselineMigrationFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(baseline), "provider_group_dictionary") {
		t.Fatalf("provider_group_dictionary is registered in the immutable %s; a new relation belongs to a forward-only migration", baselineMigrationFile)
	}
}
