//go:build soakscale

package soak

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"bansoko/internal/runtime/migrations"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// soakEnv is the validated run configuration. The soak refuses any database
// that the harness script did not declare disposable and local.
type soakEnv struct {
	OwnerDSN   string
	RuntimeDSN string
	Profile    Profile
	Evidence   string
	Phases     map[string]bool
	Idle       time.Duration
	RepoRoot   string
}

var (
	envOnce sync.Once
	envVal  *soakEnv
	envErr  error

	dbOnce  sync.Once
	ownerDB *sql.DB
	rtDB    *sql.DB
	dbErr   error
)

func getEnv(t *testing.T) *soakEnv {
	t.Helper()
	envOnce.Do(func() { envVal, envErr = loadEnv() })
	if envErr != nil {
		t.Fatalf("soak environment: %v", envErr)
	}
	return envVal
}

func loadEnv() (*soakEnv, error) {
	if os.Getenv("BANSOKO_SOAK_DISPOSABLE") != "1" {
		return nil, fmt.Errorf("BANSOKO_SOAK_DISPOSABLE=1 is required (run through scripts/verify/soak-scale.sh)")
	}
	e := &soakEnv{OwnerDSN: os.Getenv("BANSOKO_SOAK_OWNER_DSN"), RuntimeDSN: os.Getenv("BANSOKO_SOAK_RUNTIME_DSN")}
	for _, dsn := range []string{e.OwnerDSN, e.RuntimeDSN} {
		if err := validateLocalDisposableDSN(dsn); err != nil {
			return nil, err
		}
	}
	prof, err := profileByName(os.Getenv("SOAK_PROFILE"))
	if err != nil {
		return nil, err
	}
	e.Profile = prof
	e.Evidence = os.Getenv("SOAK_EVIDENCE_DIR")
	if e.Evidence == "" {
		e.Evidence = os.TempDir()
	}
	e.Phases = map[string]bool{}
	for _, p := range strings.Split(envDefault("SOAK_PHASES", "dataset,read,worker,retention"), ",") {
		e.Phases[strings.TrimSpace(p)] = true
	}
	e.Idle = 10 * time.Minute
	if v := os.Getenv("SOAK_IDLE_SECONDS"); v != "" {
		var sec int
		if _, err := fmt.Sscanf(v, "%d", &sec); err != nil || sec < 0 {
			return nil, fmt.Errorf("bad SOAK_IDLE_SECONDS %q", v)
		}
		e.Idle = time.Duration(sec) * time.Second
	}
	_, file, _, _ := runtime.Caller(0)
	e.RepoRoot = filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	return e, nil
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// validateLocalDisposableDSN is the soak's own fail-closed guard: loopback host
// only and a database name that says it is disposable. The scale soak never
// resets a schema (the container is fresh), so it needs no destructive helper.
func validateLocalDisposableDSN(dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" {
		return fmt.Errorf("unusable soak DSN")
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
	default:
		return fmt.Errorf("soak DSN host %q is not loopback", u.Hostname())
	}
	name := strings.TrimPrefix(u.Path, "/")
	if !strings.Contains(name, "disp") || strings.Contains(name, "prod") {
		return fmt.Errorf("soak database %q is not named like a disposable database", name)
	}
	return nil
}

// dbs opens the owner (superuser, loads data) and runtime (production
// privilege boundary, measured) pools once.
func dbs(t *testing.T) (owner, runtimeDB *sql.DB) {
	t.Helper()
	e := getEnv(t)
	dbOnce.Do(func() {
		ownerDB, dbErr = sql.Open("pgx", e.OwnerDSN)
		if dbErr != nil {
			return
		}
		ownerDB.SetMaxOpenConns(8)
		rtDB, dbErr = sql.Open("pgx", e.RuntimeDSN)
		if dbErr != nil {
			return
		}
		rtDB.SetMaxOpenConns(10) // the server default (database.Open)
		rtDB.SetMaxIdleConns(5)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if dbErr = ownerDB.PingContext(ctx); dbErr != nil {
			return
		}
		dbErr = rtDB.PingContext(ctx)
	})
	if dbErr != nil {
		t.Fatalf("open soak databases: %v", dbErr)
	}
	return ownerDB, rtDB
}

// applyMigrations applies the baseline to a database that has no schema yet.
func applyMigrations(ctx context.Context, e *soakEnv, owner *sql.DB) error {
	var exists bool
	if err := owner.QueryRowContext(ctx, `SELECT to_regclass('public.managed_anime') IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	return migrations.Runner{DB: owner, Dir: filepath.Join(e.RepoRoot, "db", "migrations")}.Apply(ctx)
}
