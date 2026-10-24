package disposabledb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"bansoko/internal/runtime/migrations"
	"bansoko/internal/testutil/disposabledb/guard"
)

// ResetDisposableSchema opens BANSOKO_TEST_DATABASE_URL through the disposable
// guard, drops the shared schema, and applies the migration chain. It is
// the single entry point for every package-level PG test helper so that no
// destructive reset can bypass the fail-closed guard (PRD P0.5 R3).
//
// The reset and the migration chain always run as the schema owner
// (BANSOKO_TEST_DATABASE_URL). The returned handle is the segregated runtime role
// when the harness exposes one (`BANSOKO_TEST_RUNTIME_DATABASE_URL`), so application
// tests exercise the real runtime privilege boundary; otherwise it is the owner
// handle and behavior is unchanged.
//
// Reset mechanism: the migration chain is applied once into a template database
// (named from a hash of the shipped migration files and the owner role, built
// under a temporary name and renamed into place under an advisory lock), and each
// reset is `DROP DATABASE target WITH (FORCE)` + `CREATE DATABASE target TEMPLATE
// template`. The target keeps its name, so tests that open BANSOKO_TEST_DATABASE_URL
// themselves still see the same database. Roles are cluster-level and migrations
// hold every grant, so the clone is identical to a freshly migrated schema. Packages
// whose tests exercise the migration runner itself (internal/runtime/migrations)
// do not use this helper and keep their real DROP SCHEMA + Apply.
//
// Packages must keep running their PG tests serially (-p 1) and migration-last:
// every helper resets the same disposable schema, so parallel packages would
// race each other's DROP SCHEMA.
func ResetDisposableSchema(t *testing.T) *sql.DB {
	t.Helper()
	resetDisposableSchema(t)
	return guard.MustRuntime(t)
}

// ResetDisposableSchemaAsOwner is ResetDisposableSchema for the few tests that
// must exercise DDL or another owner-only path. It returns the owner handle.
func ResetDisposableSchemaAsOwner(t *testing.T) *sql.DB {
	t.Helper()
	resetDisposableSchema(t)
	return guard.MustDisposable(t)
}

// Owner returns the schema-owner handle for tests that must run DDL or another
// owner-only path while the main handle is the runtime role. When the harness
// does not expose a segregated runtime role this is the same disposable DSN.
func Owner(t *testing.T) *sql.DB {
	t.Helper()
	return guard.MustDisposable(t)
}

var templateVerified sync.Map // template name -> struct{}, per test process

func resetDisposableSchema(t *testing.T) {
	t.Helper()
	// The guard validates the target DSN (host allowlist, deny names, disposable
	// declaration) before anything is dropped.
	probe := guard.MustDisposable(t)
	_ = probe.Close()
	ctx := context.Background()
	dsn := os.Getenv("BANSOKO_TEST_DATABASE_URL")
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse test database url: %v", err)
	}
	target := strings.TrimPrefix(u.Path, "/")
	switch strings.ToLower(target) {
	case "", "postgres", "template0", "template1":
		t.Fatalf("refusing to reset maintenance database %q", target)
	}
	user := ""
	if u.User != nil {
		user = u.User.Username()
	}
	hash, err := migrationHash()
	if err != nil {
		t.Fatalf("hash migrations: %v", err)
	}
	sum := sha256.Sum256([]byte(hash + "\x00" + user))
	tmpl := "bansoko_tmpl_" + hex.EncodeToString(sum[:8])
	admin := withDatabase(u, "postgres")
	db, err := sql.Open("pgx", admin)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("admin connection: %v", err)
	}
	defer conn.Close()
	if _, ok := templateVerified.Load(tmpl); !ok {
		ensureTemplate(t, ctx, conn, u, tmpl)
		templateVerified.Store(tmpl, struct{}{})
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, quoteIdent(target))); err != nil {
		t.Fatalf("drop disposable database: %v", err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %s TEMPLATE %s`, quoteIdent(target), quoteIdent(tmpl))); err != nil {
		t.Fatalf("clone disposable database from template: %v", err)
	}
}

// ensureTemplate builds the migrated template once per cluster and migration
// set. It runs under a cluster-wide advisory lock so concurrent test processes
// build it at most once; a half-built template never carries the final name.
func ensureTemplate(t *testing.T, ctx context.Context, conn *sql.Conn, u *url.URL, tmpl string) {
	t.Helper()
	const lockKey = 7426001
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		t.Fatalf("template lock: %v", err)
	}
	defer conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, lockKey) //nolint:errcheck
	var exists bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)`, tmpl).Scan(&exists); err != nil {
		t.Fatalf("check template: %v", err)
	}
	if exists {
		return
	}
	build := fmt.Sprintf("%s_b%d", tmpl, os.Getpid())
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, quoteIdent(build))); err != nil {
		t.Fatalf("drop stale template build: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `CREATE DATABASE `+quoteIdent(build)); err != nil {
		t.Fatalf("create template build: %v", err)
	}
	built := false
	defer func() {
		if !built {
			_, _ = conn.ExecContext(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, quoteIdent(build)))
		}
	}()
	bdb, err := sql.Open("pgx", withDatabase(u, build))
	if err != nil {
		t.Fatalf("open template build: %v", err)
	}
	_, filename, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
	applyErr := (migrations.Runner{DB: bdb, Dir: filepath.Join(root, "db", "migrations")}).Apply(ctx)
	_ = bdb.Close()
	if applyErr != nil {
		t.Fatalf("apply baseline into template: %v", applyErr)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`ALTER DATABASE %s RENAME TO %s`, quoteIdent(build), quoteIdent(tmpl))); err != nil {
		t.Fatalf("publish template: %v", err)
	}
	built = true
}

func migrationHash() (string, error) {
	_, filename, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "..")), "db", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", n, len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func withDatabase(u *url.URL, name string) string {
	c := *u
	c.Path = "/" + name
	c.RawPath = ""
	return c.String()
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
