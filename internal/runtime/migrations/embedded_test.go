package migrations

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"
)

// TestEmbeddedMigrationsMatchDisk verifies the compiled-in migration set is
// byte-identical to the repository db/migrations directory it ships from, so
// native binaries and directory-mounted deployments validate the same schema.
func TestEmbeddedMigrationsMatchDisk(t *testing.T) {
	_, filename, _, _ := runtime.Caller(0)
	diskDir := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "..", "db", "migrations"))

	embedded, err := Runner{}.loadMigrationsFromFS()
	if err != nil {
		t.Fatalf("load embedded migrations: %v", err)
	}
	onDisk, err := Runner{Dir: diskDir}.loadMigrations()
	if err != nil {
		t.Fatalf("load disk migrations: %v", err)
	}
	if len(embedded) != len(onDisk) {
		t.Fatalf("embedded has %d migrations, disk has %d", len(embedded), len(onDisk))
	}
	for i := range embedded {
		if embedded[i].version != onDisk[i].version {
			t.Fatalf("migration %d: embedded %q, disk %q", i, embedded[i].version, onDisk[i].version)
		}
		if string(embedded[i].contents) != string(onDisk[i].contents) {
			t.Fatalf("migration %s contents differ between embedded FS and disk", embedded[i].version)
		}
	}
}

// TestEmbeddedFallbackRequiresNoDirectory proves a Runner without Dir reads
// the embedded set (native single-binary behavior), while an explicit FS
// override is honored.
func TestEmbeddedFallbackRequiresNoDirectory(t *testing.T) {
	migrations, err := Runner{}.loadMigrations()
	if err != nil {
		t.Fatalf("embedded fallback: %v", err)
	}
	if len(migrations) == 0 || migrations[0].version != baselineMigrationFile {
		t.Fatalf("embedded fallback first migration = %v, want %s first", migrations[0].version, baselineMigrationFile)
	}
	if got := TargetVersion(); got != migrations[len(migrations)-1].version {
		t.Fatalf("TargetVersion = %q, want %q", got, migrations[len(migrations)-1].version)
	}

	override := fstest.MapFS{
		"001_baseline.sql":  {Data: []byte("SELECT 1;")},
		"002_follow_up.sql": {Data: []byte("SELECT 2;")},
	}
	overridden, err := Runner{FS: override}.loadMigrations()
	if err != nil {
		t.Fatalf("FS override: %v", err)
	}
	if len(overridden) != 2 || overridden[1].version != "002_follow_up.sql" {
		t.Fatalf("FS override migrations = %v", overridden)
	}
}

// TestExplicitDirStillWins pins the precedence: an explicitly configured
// directory is read from disk even though the embedded set exists.
func TestExplicitDirStillWins(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "001_baseline.sql"), []byte("SELECT 1;"), 0o600); err != nil {
		t.Fatal(err)
	}
	migrations, err := Runner{Dir: dir}.loadMigrations()
	if err != nil {
		t.Fatalf("explicit dir: %v", err)
	}
	if len(migrations) != 1 || string(migrations[0].contents) != "SELECT 1;" {
		t.Fatalf("explicit dir migrations = %v", migrations)
	}
}
