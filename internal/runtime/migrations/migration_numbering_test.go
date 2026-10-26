package migrations

import (
	"os"
	"regexp"
	"testing"
)

// Migrations apply in file-name order and the ledger is keyed by file name, so
// two lanes that each add the next free number collide only after merge: the
// later file sorts before migrations an installed database already applied and
// startup fails closed. Every number must be used exactly once.
func TestMigrationNumbersAreUnique(t *testing.T) {
	entries, err := os.ReadDir("../../../db/migrations")
	if err != nil {
		t.Fatal(err)
	}
	numbered := regexp.MustCompile(`^(\d{3})_[a-z0-9_]+\.sql$`)
	seen := map[string]string{}
	for _, name := range migrationFiles(entries) {
		match := numbered.FindStringSubmatch(name)
		if match == nil {
			t.Errorf("migration %s is not named NNN_snake_case.sql", name)
			continue
		}
		if previous, ok := seen[match[1]]; ok {
			t.Errorf("migration number %s is used by both %s and %s", match[1], previous, name)
		}
		seen[match[1]] = name
	}
}
