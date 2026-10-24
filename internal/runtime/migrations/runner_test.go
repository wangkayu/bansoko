package migrations

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"bansoko/internal/testutil/disposabledb/guard"
)

// migrationsDir resolves the repository db/migrations directory relative to
// this test file so the tests exercise the shipped migration set.
func migrationsDir(t *testing.T) string {
	t.Helper()
	_, filename, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
	return filepath.Join(root, "db", "migrations")
}

// openDisposableDB opens the isolated test database through the shared
// disposable guard and drops every existing object so each test starts from an
// empty public schema. BANSOKO_TEST_DATABASE_URL must never point at shared
// data. This package owns the migration set, so the helper resets the schema
// but leaves Apply to the test.
func openDisposableDB(t *testing.T) *sql.DB {
	t.Helper()
	db := guard.MustDisposable(t)
	if _, err := db.ExecContext(context.Background(), `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatalf("reset disposable schema: %v", err)
	}
	return db
}

// installedDB returns a disposable database with the shipped migration set
// applied.
func installedDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openDisposableDB(t)
	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Apply(context.Background()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return db
}

func shippedMigrationFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(migrationsDir(t))
	if err != nil {
		t.Fatal(err)
	}
	files := migrationFiles(entries)
	if len(files) == 0 {
		t.Fatal("no migration files found")
	}
	return files
}

var (
	migrationFilePattern     = regexp.MustCompile(`^[0-9]{3}_[a-z0-9_]+\.sql$`)
	alterTablePattern        = regexp.MustCompile(`(?m)^\s*ALTER TABLE\s+(\w+)`)
	inlineColumnCheckPattern = regexp.MustCompile(`(?s)ADD COLUMN\s+(\w+)([^,;]*?)CHECK\s*\(`)
	addConstraintPattern     = regexp.MustCompile(`ADD CONSTRAINT\s+(\w+)`)
	renamedFunctionPattern   = regexp.MustCompile(`(?i)ALTER\s+FUNCTION\s+[^;]*\bRENAME\s+TO\b`)
)

// The migration set is static text: the baseline comes first, names follow the
// NNN_name.sql pattern, nothing seeds credentials, and nothing renames a
// function in place (sqlc's catalog cannot follow a rename, so the generated
// persistence slices would stop regenerating).
func TestMigrationSetStaticShape(t *testing.T) {
	files := shippedMigrationFiles(t)
	if files[0] != baselineMigrationFile {
		t.Fatalf("first migration = %s, want %s", files[0], baselineMigrationFile)
	}
	for _, name := range files {
		if !migrationFilePattern.MatchString(name) {
			t.Fatalf("migration %s does not match NNN_name.sql", name)
		}
		contents, err := os.ReadFile(filepath.Join(migrationsDir(t), name))
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(contents))
		for _, seed := range []string{"insert into local_accounts", "insert into auth_sessions", "insert into api_tokens"} {
			if strings.Contains(lower, seed) {
				t.Fatalf("migration %s seeds credentials (%s)", name, seed)
			}
		}
		if renamedFunctionPattern.Match(contents) {
			t.Fatalf("migration %s renames a function in place", name)
		}
	}
}

// A column CHECK written inline in ADD COLUMN is named by PostgreSQL as
// <table>_<column>_check. An explicit ADD CONSTRAINT with that same name in the
// same migration aborts the whole set with "constraint ... already exists",
// which only the PostgreSQL lane would otherwise catch.
func TestMigrationsNameNoDuplicateColumnCheck(t *testing.T) {
	for _, name := range shippedMigrationFiles(t) {
		contents, err := os.ReadFile(filepath.Join(migrationsDir(t), name))
		if err != nil {
			t.Fatal(err)
		}
		table := ""
		derived := map[string]struct{}{}
		explicit := map[string]struct{}{}
		for _, statement := range strings.Split(string(contents), ";") {
			if match := alterTablePattern.FindStringSubmatch(statement); match != nil {
				table = match[1]
			}
			if table != "" {
				for _, match := range inlineColumnCheckPattern.FindAllStringSubmatch(statement, -1) {
					derived[table+"_"+match[1]+"_check"] = struct{}{}
				}
			}
			for _, match := range addConstraintPattern.FindAllStringSubmatch(statement, -1) {
				explicit[match[1]] = struct{}{}
			}
		}
		for constraint := range derived {
			if _, collides := explicit[constraint]; collides {
				t.Fatalf("%s: inline column CHECK and explicit constraint both claim %s; name one of them explicitly", name, constraint)
			}
		}
	}
}

func TestBaselineApplyVerifyAndIdempotency(t *testing.T) {
	db := openDisposableDB(t)
	ctx := context.Background()
	runner := Runner{DB: db, Dir: migrationsDir(t)}

	if err := runner.Apply(ctx); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Repeated application is a no-op, not an error.
	if err := runner.Apply(ctx); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if err := runner.Verify(ctx); err != nil {
		t.Fatalf("verify: %v", err)
	}

	var identity, baselineVersion string
	if err := db.QueryRowContext(ctx, `SELECT identity, baseline_version FROM schema_identity WHERE singleton_id = 1`).Scan(&identity, &baselineVersion); err != nil {
		t.Fatalf("read schema identity: %v", err)
	}
	if identity != schemaIdentityValue || baselineVersion != baselineMigrationFile {
		t.Fatalf("schema identity = %q %q", identity, baselineVersion)
	}
	files := shippedMigrationFiles(t)
	rows, err := db.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	var recorded []string
	for rows.Next() {
		var version, sum string
		if err := rows.Scan(&version, &sum); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		contents, err := os.ReadFile(filepath.Join(migrationsDir(t), version))
		if err != nil {
			_ = rows.Close()
			t.Fatalf("recorded migration %s is not shipped: %v", version, err)
		}
		if sum != checksum(contents) {
			_ = rows.Close()
			t.Fatalf("recorded checksum of %s differs from the shipped file", version)
		}
		recorded = append(recorded, version)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(recorded, ",") != strings.Join(files, ",") {
		t.Fatalf("ledger = %v, want every shipped file in order %v", recorded, files)
	}

	// The relation set is exactly the runner's allowlist.
	relations, err := publicRelations(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	for name, kind := range knownRelations {
		if relations[name] != kind {
			t.Fatalf("relation %s kind = %q, want %q", name, relations[name], kind)
		}
	}
	for name := range relations {
		if _, ok := knownRelations[name]; !ok {
			t.Fatalf("relation %s is not in the runner allowlist", name)
		}
	}
	for table, columns := range requiredColumns {
		actual, err := tableColumns(ctx, db, table)
		if err != nil {
			t.Fatal(err)
		}
		for _, column := range columns {
			if _, ok := actual[column]; !ok {
				t.Fatalf("required column %s.%s is missing", table, column)
			}
		}
	}

	// Seeds: the instance bootstrap row, the four enabled automation
	// schedules, and the instance-wide defaults.
	var bootstrapRows, scheduleCount, enabledScheduled int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM instance_bootstrap`).Scan(&bootstrapRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE enabled AND next_run_at IS NOT NULL) FROM automation_schedules`).Scan(&scheduleCount, &enabledScheduled); err != nil {
		t.Fatal(err)
	}
	if bootstrapRows != 1 || scheduleCount != 4 || enabledScheduled != 4 {
		t.Fatalf("seeds: bootstrap=%d schedules=%d enabled=%d", bootstrapRows, scheduleCount, enabledScheduled)
	}
	for _, table := range []string{"global_config_sections"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			t.Fatalf("%s has no seed row", table)
		}
	}

	// A tampered recorded checksum must fail verification.
	if _, err := db.ExecContext(ctx, `UPDATE schema_migrations SET checksum = 'sha256:tampered' WHERE version = $1`, baselineMigrationFile); err != nil {
		t.Fatal(err)
	}
	if err := runner.Verify(ctx); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("verify after tampering = %v, want checksum mismatch", err)
	}
	if err := runner.Apply(ctx); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("apply after tampering = %v, want checksum mismatch", err)
	}
}

// forwardMigrationDir copies the shipped set into a temporary directory and
// adds the given forward migrations.
func forwardMigrationDir(t *testing.T, extra map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range shippedMigrationFiles(t) {
		contents, err := os.ReadFile(filepath.Join(migrationsDir(t), name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, contents := range extra {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func ledgerVersions(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var versions []string
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return versions
}

// An installed database takes later forward migrations in order, each recorded
// with its checksum, without re-running the baseline.
func TestRunnerAppliesForwardMigrationToInstalledBaseline(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ('forward-anime', 'Forward')`); err != nil {
		t.Fatal(err)
	}
	const forward = "998_forward_probe.sql"
	dir := forwardMigrationDir(t, map[string]string{
		forward: `ALTER TABLE managed_anime ADD COLUMN forward_probe TEXT NOT NULL DEFAULT 'probe';`,
	})
	runner := Runner{DB: db, Dir: dir}
	if err := runner.Verify(ctx); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("verify before the forward migration = %v, want unsupported schema", err)
	}
	if err := runner.Apply(ctx); err != nil {
		t.Fatalf("apply forward migration: %v", err)
	}
	if err := runner.Verify(ctx); err != nil {
		t.Fatalf("verify forward migration: %v", err)
	}
	versions := ledgerVersions(t, db)
	if len(versions) == 0 || versions[len(versions)-1] != forward || len(versions) != len(shippedMigrationFiles(t))+1 {
		t.Fatalf("ledger = %v, want the shipped set followed by %s", versions, forward)
	}
	var probe string
	if err := db.QueryRowContext(ctx, `SELECT forward_probe FROM managed_anime WHERE id = 'forward-anime'`).Scan(&probe); err != nil || probe != "probe" {
		t.Fatalf("forward column on existing row = %q, %v", probe, err)
	}
	// The shipped set alone now refuses the database: its ledger records a
	// migration this build does not know.
	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Verify(ctx); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("verify with the older set = %v, want unsupported schema", err)
	}
}

// A forward migration that creates a relation outside the runner allowlist is
// rolled back as a whole: nothing is recorded and the relation does not stay.
func TestRunnerRollsBackMigrationCreatingUnknownRelation(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	dir := forwardMigrationDir(t, map[string]string{
		"998_unknown_relation.sql": `CREATE TABLE forward_unknown (id TEXT PRIMARY KEY);`,
	})
	if err := (Runner{DB: db, Dir: dir}).Apply(ctx); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("apply = %v, want unsupported schema", err)
	}
	var present bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('public.forward_unknown') IS NOT NULL`).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if present || len(ledgerVersions(t, db)) != len(shippedMigrationFiles(t)) {
		t.Fatalf("unknown relation present=%v ledger=%v; the failed apply must roll back", present, ledgerVersions(t, db))
	}
}

func TestRunnerRejectsForeignSchemaAndWrongIdentity(t *testing.T) {
	ctx := context.Background()

	// A database that already has relations but no schema identity was built by
	// something else and is refused, never adopted.
	foreign := openDisposableDB(t)
	if _, err := foreign.ExecContext(ctx, `CREATE TABLE schema_migrations(version TEXT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if err := (Runner{DB: foreign, Dir: migrationsDir(t)}).Apply(ctx); !errors.Is(err, ErrSchemaIdentity) {
		t.Fatalf("apply on a foreign schema = %v, want schema identity mismatch", err)
	}

	db := installedDB(t)
	runner := Runner{DB: db, Dir: migrationsDir(t)}
	if _, err := db.ExecContext(ctx, `UPDATE schema_identity SET identity = 'other:baseline' WHERE singleton_id = 1`); err == nil {
		t.Fatal("the schema identity CHECK must reject another identity")
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE schema_identity DROP CONSTRAINT schema_identity_identity_check`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE schema_identity SET identity = 'other:baseline' WHERE singleton_id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := runner.Verify(ctx); !errors.Is(err, ErrSchemaIdentity) {
		t.Fatalf("verify with another identity = %v, want schema identity mismatch", err)
	}
	if err := runner.Apply(ctx); !errors.Is(err, ErrSchemaIdentity) {
		t.Fatalf("apply with another identity = %v, want schema identity mismatch", err)
	}
}

// A ledger written by another migration history (an old chain's file names)
// is refused even when the identity row is right.
func TestRunnerRejectsUnknownLedgerHistory(t *testing.T) {
	db := installedDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version, checksum) VALUES ('380_old_chain.sql', 'sha256:old')`); err != nil {
		t.Fatal(err)
	}
	if err := (Runner{DB: db, Dir: migrationsDir(t)}).Verify(ctx); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("verify with an unknown ledger row = %v, want unsupported schema", err)
	}
}

func TestRunnerRejectsMigrationSetWithoutBaseline(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "002_forward.sql"), []byte("SELECT 1;"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Runner{Dir: dir}).loadMigrations(); !errors.Is(err, ErrInvalidMigrationSet) {
		t.Fatalf("load without baseline = %v, want invalid migration set", err)
	}
	if err := os.WriteFile(filepath.Join(dir, baselineMigrationFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Runner{Dir: dir}).loadMigrations(); !errors.Is(err, ErrInvalidMigrationSet) {
		t.Fatalf("load with an empty baseline = %v, want invalid migration set", err)
	}
}
