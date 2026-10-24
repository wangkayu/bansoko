// Package guard contains the fail-closed rules that protect every destructive
// PostgreSQL test helper.
//
// All PG tests share one disposable schema and execute
// `DROP SCHEMA public CASCADE` before migrating. A mistake in the DSN (an
// empty value, a production database name, a non-loopback host) would destroy
// data outside the test boundary, so every mutation goes through this package
// first. The rules live here once and are consumed by the test helpers
// (MustDisposable), by the shell guard `scripts/ci/require-disposable-db.sh`
// via the `cmd/disposable-db-check` CLI, and by the migrations package's own
// reset helper (which must not import the parent package because of the
// migrations dependency in reset.go).
//
// The parent package `disposabledb` wraps this guard with
// ResetDisposableSchema (drop + migrate); this subpackage deliberately does
// not depend on the migration runner so packages under test that import the
// migration runner themselves can still use the guard without an import cycle.
package guard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Options controls what the guard accepts.
type Options struct {
	// RequireDisposable requires the caller to explicitly declare the target
	// disposable. Set automatically by MustDisposable (tests are always
	// disposable); shell callers must pass BANSOKO_TEST_DB_DISPOSABLE=1.
	RequireDisposable bool

	// AllowHosts is the closed set of accepted DSN hosts. Defaults to loopback
	// plus "postgres" (the GitHub Actions service hostname).
	AllowHosts []string

	// DenyNames lists database names that are never acceptable test targets.
	// Defaults cover obvious production-looking names.
	DenyNames []string

	// DenyEnv, when non-empty, lists environment variables that mark the
	// process as production; guard refuses when any is set. Defaults to
	// BANSOKO_ENV=production style via EnvDeny map below.
}

// DefaultDenyNames are database names that must never be dropped by a test.
// These are production-looking names only. The default database of a new
// stack is named bansoko, which also serves local development, so it is not
// on this list (the host allowlist plus explicit disposable declaration are the
// real safeguards).
var DefaultDenyNames = []string{
	"production",
	"prod",
	"bansoko-prod",
	"bansoko_prod",
	"bansoko-production",
	"bansoko_production",
}

// DefaultAllowHosts accepts loopback addresses and the CI service hostname.
var DefaultAllowHosts = []string{
	"localhost",
	"127.0.0.1",
	"::1",
	"postgres",
}

// ProductionEnvMarkers are env vars that, when set to a truthy value, make the
// guard refuse the target unconditionally.
var ProductionEnvMarkers = []struct {
	Key    string
	Reject string // truthy values are rejected
}{
	{Key: "BANSOKO_ENV", Reject: "production"},
}

// ErrNotDisposable is the guard rejection error.
var ErrNotDisposable = errors.New("database target is not disposable")

// ValidateDSN rejects unsafe test database targets before any mutation.
func ValidateDSN(dsn string, opts Options) error {
	if strings.TrimSpace(dsn) == "" {
		return fmt.Errorf("%w: empty BANSOKO_TEST_DATABASE_URL", ErrNotDisposable)
	}

	u, err := url.Parse(dsn)
	if err != nil {
		return fmt.Errorf("%w: unparseable DSN: %v", ErrNotDisposable, err)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: DSN has no host", ErrNotDisposable)
	}

	allowHosts := opts.AllowHosts
	if len(allowHosts) == 0 {
		allowHosts = DefaultAllowHosts
	}
	allowed := false
	for _, h := range allowHosts {
		if strings.EqualFold(host, h) {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("%w: host %q is not in the allowlist %v", ErrNotDisposable, host, allowHosts)
	}

	denyNames := opts.DenyNames
	if len(denyNames) == 0 {
		denyNames = DefaultDenyNames
	}
	name := strings.TrimPrefix(u.Path, "/")
	for _, d := range denyNames {
		if strings.EqualFold(name, d) {
			return fmt.Errorf("%w: database name %q is denied", ErrNotDisposable, name)
		}
	}

	if opts.RequireDisposable && os.Getenv("BANSOKO_TEST_DB_DISPOSABLE") != "1" {
		return fmt.Errorf("%w: BANSOKO_TEST_DB_DISPOSABLE=1 is required for shell callers", ErrNotDisposable)
	}

	for _, m := range ProductionEnvMarkers {
		v := os.Getenv(m.Key)
		if v == "" {
			continue
		}
		if strings.EqualFold(v, m.Reject) {
			return fmt.Errorf("%w: %s=%s marks a production process", ErrNotDisposable, m.Key, v)
		}
	}

	return nil
}

// Check opens a connection to dsn and validates reachability after the static
// rules pass. It is used by the CLI subcommand and by MustDisposable.
func Check(ctx context.Context, dsn string, opts Options) error {
	if err := ValidateDSN(dsn, opts); err != nil {
		return err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("%w: open: %v", ErrNotDisposable, err)
	}
	defer db.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return fmt.Errorf("%w: unreachable: %v", ErrNotDisposable, err)
	}
	return nil
}

// MustDisposable returns an open *sql.DB for a test, refusing (t.Fatal, never
// t.Skip) any target that fails the disposable guard. PG acceptance tests must
// actually run; a skip here would silently shrink the gate.
func MustDisposable(t *testing.T) *sql.DB {
	t.Helper()
	return openGuarded(t, os.Getenv("BANSOKO_TEST_DATABASE_URL"))
}

// MustRuntime returns the handle application tests must use. When the harness
// exposes a segregated runtime role (`BANSOKO_TEST_RUNTIME_DATABASE_URL`) that role is
// used, so every application test exercises the real runtime privilege boundary;
// otherwise it falls back to the single disposable DSN and behaves as before.
// The owner DSN stays BANSOKO_TEST_DATABASE_URL and is only used for reset/migration
// or owner-only tests.
func MustRuntime(t *testing.T) *sql.DB {
	t.Helper()
	if dsn := os.Getenv("BANSOKO_TEST_RUNTIME_DATABASE_URL"); dsn != "" {
		return openGuarded(t, dsn)
	}
	return MustDisposable(t)
}

func openGuarded(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	if err := ValidateDSN(dsn, Options{}); err != nil {
		t.Fatalf("disposable DB guard: %v", err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping test database: %v", err)
	}
	return db
}
