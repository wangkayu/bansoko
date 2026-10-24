package guard

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateDSNRejectsEmpty(t *testing.T) {
	if err := ValidateDSN("", Options{}); !errors.Is(err, ErrNotDisposable) {
		t.Fatalf("empty DSN must be rejected, got %v", err)
	}
}

func TestValidateDSNRejectsProductionDatabaseName(t *testing.T) {
	for _, name := range []string{"production", "prod", "bansoko-prod", "bansoko_prod", "bansoko-production", "bansoko_production"} {
		dsn := "postgres://user:pass@127.0.0.1:5432/" + name + "?sslmode=disable"
		if err := ValidateDSN(dsn, Options{}); !errors.Is(err, ErrNotDisposable) {
			t.Fatalf("database name %q must be denied, got %v", name, err)
		}
	}
}

func TestValidateDSNAllowsLocalTestDatabaseName(t *testing.T) {
	t.Setenv("BANSOKO_ENV", "")
	// The default database is named bansoko; it must not be denied by the name
	// rule (host allowlist + disposable declaration guard it instead).
	for _, name := range []string{"bansoko", "app", "app_test", "bansoko_v2", "bansoko_e2e"} {
		dsn := "postgres://user:pass@127.0.0.1:5432/" + name + "?sslmode=disable"
		if err := ValidateDSN(dsn, Options{}); err != nil {
			t.Fatalf("local test database name %q must pass, got %v", name, err)
		}
	}
}

func TestValidateDSNRejectsNonLoopbackHost(t *testing.T) {
	for _, host := range []string{"10.0.0.5", "db.internal.example.com", "192.168.1.10"} {
		dsn := "postgres://user:pass@" + host + ":5432/test_db?sslmode=disable"
		if err := ValidateDSN(dsn, Options{}); !errors.Is(err, ErrNotDisposable) {
			t.Fatalf("host %q must be denied, got %v", host, err)
		}
	}
}

func TestValidateDSNRejectsMissingDisposableDeclaration(t *testing.T) {
	t.Setenv("BANSOKO_TEST_DB_DISPOSABLE", "")
	dsn := "postgres://user:pass@127.0.0.1:5432/test_db?sslmode=disable"
	if err := ValidateDSN(dsn, Options{RequireDisposable: true}); !errors.Is(err, ErrNotDisposable) {
		t.Fatalf("missing BANSOKO_TEST_DB_DISPOSABLE must be denied, got %v", err)
	}
}

func TestValidateDSNRejectsProductionEnv(t *testing.T) {
	t.Setenv("BANSOKO_ENV", "production")
	dsn := "postgres://user:pass@127.0.0.1:5432/test_db?sslmode=disable"
	if err := ValidateDSN(dsn, Options{}); !errors.Is(err, ErrNotDisposable) {
		t.Fatalf("production env must be denied, got %v", err)
	}
}

func TestValidateDSNAllowsLoopbackDisposable(t *testing.T) {
	t.Setenv("BANSOKO_ENV", "")
	dsn := "postgres://user:pass@127.0.0.1:5432/mikan_v2_test?sslmode=disable"
	if err := ValidateDSN(dsn, Options{}); err != nil {
		t.Fatalf("loopback test DSN must pass, got %v", err)
	}
}

func TestValidateDSNAllowsCustomAllowHost(t *testing.T) {
	t.Setenv("BANSOKO_ENV", "")
	dsn := "postgres://user:pass@pg-test:5432/test_db?sslmode=disable"
	if err := ValidateDSN(dsn, Options{AllowHosts: []string{"pg-test"}}); err != nil {
		t.Fatalf("explicit allow host must pass, got %v", err)
	}
}

func TestValidateDSNRejectsMalformed(t *testing.T) {
	if err := ValidateDSN("not-a-url", Options{}); !errors.Is(err, ErrNotDisposable) {
		t.Fatalf("malformed DSN must be rejected, got %v", err)
	}
}

func TestDefaultDenyNamesCoverProductionVariants(t *testing.T) {
	joined := strings.Join(DefaultDenyNames, "|")
	for _, mustContain := range []string{"prod", "production"} {
		if !strings.Contains(joined, mustContain) {
			t.Fatalf("default deny names must cover %q: %v", mustContain, DefaultDenyNames)
		}
	}
}
