package config

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestFromFlagsDefaultsUpdateModeToSharedSafeValue(t *testing.T) {
	t.Setenv("BANSOKO_UPDATE_MODE", "")
	t.Setenv("BANSOKO_ENVIRONMENT", "development")
	cfg, err := FromFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UpdateMode != DefaultUpdateMode || !IsValidUpdateMode(cfg.UpdateMode) {
		t.Fatalf("default update mode = %q, want %q", cfg.UpdateMode, DefaultUpdateMode)
	}
}

func TestFromFlagsServesWithDerivedRuntimeConnection(t *testing.T) {
	t.Setenv("BANSOKO_ENVIRONMENT", "development")
	t.Setenv("BANSOKO_DATABASE_URL", "postgres://owner:pw@db/bansoko")
	t.Setenv("BANSOKO_RUNTIME_DATABASE_URL", "postgres://bansoko_app:pw@db/bansoko")
	cfg, err := FromFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgres://bansoko_app:pw@db/bansoko" {
		t.Fatalf("serving DSN = %q, want the derived runtime connection", cfg.DatabaseURL)
	}
	t.Setenv("BANSOKO_RUNTIME_DATABASE_URL", "")
	cfg, err = FromFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgres://owner:pw@db/bansoko" {
		t.Fatalf("serving DSN without the bootstrap = %q, want BANSOKO_DATABASE_URL", cfg.DatabaseURL)
	}
}

func TestUpdateModeFromEnvAcceptsTwoValuesAndPreservesInvalidValues(t *testing.T) {
	for _, mode := range []string{"embedded", "manual"} {
		if got := UpdateModeFromEnv(func(string) string { return mode }); got != mode || !IsValidUpdateMode(got) {
			t.Fatalf("mode %q was not accepted consistently", mode)
		}
	}
	if got := UpdateModeFromEnv(func(string) string { return "invalid" }); got != "invalid" || IsValidUpdateMode(got) {
		t.Fatalf("invalid mode result = %q, valid=%v", got, IsValidUpdateMode(got))
	}
}

func TestFromFlagsRejectsProductionSkipAuth(t *testing.T) {
	_, err := FromFlags([]string{"-environment", "production", "-update-mode", "manual", "-skip-auth"})
	if err != ErrProductionSkipAuth {
		t.Fatalf("expected ErrProductionSkipAuth, got %v", err)
	}
}

func TestFromFlagsRequiresExplicitProductionUpdateMode(t *testing.T) {
	t.Setenv("BANSOKO_UPDATE_MODE", "")
	t.Setenv("BANSOKO_SESSION_SECRET", "01234567890123456789012345678901")
	_, err := FromFlags([]string{"-environment", "production"})
	if err != ErrProductionUpdateMode {
		t.Fatalf("expected ErrProductionUpdateMode, got %v", err)
	}
}

func TestFromFlagsAcceptsAllExplicitUpdateModes(t *testing.T) {
	t.Setenv("BANSOKO_SESSION_SECRET", "01234567890123456789012345678901")
	for _, mode := range []string{"embedded", "manual"} {
		t.Run(mode, func(t *testing.T) {
			args := []string{"-environment", "production", "-update-mode", mode}
			cfg, err := FromFlags(args)
			if err != nil {
				t.Fatalf("mode %s rejected: %v", mode, err)
			}
			if cfg.UpdateMode != mode {
				t.Fatalf("mode = %q, want %q", cfg.UpdateMode, mode)
			}
		})
	}
}

func TestFromFlagsDefaultsToLANHTTPListener(t *testing.T) {
	t.Setenv("BANSOKO_LISTEN_ADDRESS", "")
	t.Setenv("BANSOKO_ENVIRONMENT", "development")
	t.Setenv("BANSOKO_SKIP_AUTH", "false")

	cfg, err := FromFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddress != "0.0.0.0:8080" {
		t.Fatalf("default HTTP listener = %q, want 0.0.0.0:8080", cfg.ListenAddress)
	}
}

func TestFromFlagsUsesExplicitValues(t *testing.T) {
	cfg, err := FromFlags([]string{"-listen", "127.0.0.1:9999", "-database-url", "postgres://example"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddress != "127.0.0.1:9999" || cfg.DatabaseURL != "postgres://example" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestFromFlagsRejectsRetiredSupervisedMode(t *testing.T) {
	if _, err := FromFlags([]string{"-update-mode", "supervised"}); err == nil {
		t.Fatal("the retired supervised update mode must be rejected")
	}
}

func TestFromFlagsParsesTestFixtureHostsAndBuildsResolver(t *testing.T) {
	t.Setenv("BANSOKO_ENVIRONMENT", "test")
	t.Setenv("BANSOKO_TEST_FIXTURE_HOSTS", "Fixture.Local.=127.0.0.1")
	cfg, err := FromFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	allowlist, resolver := cfg.TestFixtureNetwork()
	if len(allowlist) != 1 || allowlist[0] != "fixture.local" || resolver == nil {
		t.Fatalf("unexpected fixture network allowlist: %v resolver_nil=%t", allowlist, resolver == nil)
	}
	ips, err := resolver(context.Background(), "FIXTURE.LOCAL.")
	if err != nil || len(ips) != 1 || !ips[0].Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("fixture resolver result = %v, err=%v", ips, err)
	}
}

func TestFromFlagsRejectsProductionTestFixtureHosts(t *testing.T) {
	t.Setenv("BANSOKO_ENVIRONMENT", "production")
	t.Setenv("BANSOKO_SESSION_SECRET", "01234567890123456789012345678901")
	t.Setenv("BANSOKO_TEST_FIXTURE_HOSTS", "fixture.local=127.0.0.1")
	_, err := FromFlags(nil)
	if err != ErrProductionFixtureHosts {
		t.Fatalf("expected ErrProductionFixtureHosts, got %v", err)
	}
}

func TestFromFlagsRejectsMalformedTestFixtureHosts(t *testing.T) {
	t.Setenv("BANSOKO_TEST_FIXTURE_HOSTS", "fixture.local=not-an-ip")
	_, err := FromFlags(nil)
	if !errors.Is(err, ErrFixtureHostsConfig) {
		t.Fatalf("expected ErrFixtureHostsConfig, got %v", err)
	}
}

func TestFromFlagsResolvesFixedSeedPaths(t *testing.T) {
	t.Setenv("BANSOKO_CONFIG_DIR", "/srv/bansoko-config")
	cfg, err := FromFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AttrRulesConfigPath != "/srv/bansoko-config/attr-rules.yaml" || cfg.NamingVarsConfigPath != "/srv/bansoko-config/naming-vars.js" {
		t.Fatalf("config workspace paths = %q %q", cfg.AttrRulesConfigPath, cfg.NamingVarsConfigPath)
	}
	if cfg.AttrRulesDefaultsPath != "deployment/defaults/attr-rules.yaml" || cfg.NamingVarsDefaultsPath != "deployment/defaults/naming-vars.js" {
		t.Fatalf("bundled defaults paths = %q %q", cfg.AttrRulesDefaultsPath, cfg.NamingVarsDefaultsPath)
	}
}
