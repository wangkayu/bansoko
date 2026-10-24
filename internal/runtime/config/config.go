package config

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"

	"bansoko/internal/runtime/bootprepare"
	"bansoko/internal/runtime/deploymentidentity"
)

const DefaultUpdateMode = "embedded"

func UpdateModeFromEnv(getenv func(string) string) string {
	mode := strings.TrimSpace(getenv("BANSOKO_UPDATE_MODE"))
	if mode == "" {
		return DefaultUpdateMode
	}
	return mode
}

func IsValidUpdateMode(mode string) bool {
	switch mode {
	case "embedded", "manual":
		return true
	default:
		return false
	}
}

var ErrProductionSkipAuth = errors.New("production runtime cannot enable skip-auth")
var ErrProductionSessionSecret = errors.New("production runtime requires BANSOKO_SESSION_SECRET")
var ErrProductionFixtureHosts = errors.New("production runtime cannot enable test fixture hosts")
var ErrProductionUpdateMode = errors.New("production runtime requires an explicit BANSOKO_UPDATE_MODE")
var ErrFixtureHostsEnvironment = errors.New("test fixture hosts require development or test environment")
var ErrFixtureHostsConfig = errors.New("test fixture host mapping is invalid")

type Config struct {
	ListenAddress       string
	DatabaseURL         string
	Environment         string
	SkipAuth            bool
	SchemaDir           string
	SessionSecret       string
	SecretEncryptionKey string
	FrontendDir         string
	// Attr rules and the naming Vars seed live at fixed locations: the
	// deployment config workspace (BANSOKO_CONFIG_DIR) holds the operator
	// copies, and the image ships the defaults under deployment/defaults
	// relative to its working directory (/app). Tests inject other paths
	// through these fields.
	AttrRulesConfigPath    string
	AttrRulesDefaultsPath  string
	NamingVarsConfigPath   string
	NamingVarsDefaultsPath string
	// TestFixtureHosts is an explicit development/test-only host→IP mapping for
	// local HTTP fixtures. It is never accepted in production and is converted
	// to the shared SSRF allowlist/resolver at the composition root.
	TestFixtureHosts map[string]net.IP
	// WorkerDisabled skips the WorkItem worker loops. Used by the scratch
	// restore harness and maintenance rehearsals so a recovered instance
	// never claims the production queue.
	WorkerDisabled bool
	// UpdateMode selects the deployment's update channel: "embedded" (the
	// in-container self-update chain via the updates volume overlay) or
	// "manual" (operator-driven image switch; no update service is built).
	UpdateMode         string
	updateModeExplicit bool
}

func FromFlags(args []string) (Config, error) {
	flags := flag.NewFlagSet("bansoko-server", flag.ContinueOnError)
	cfg := Config{
		ListenAddress:          envOr("BANSOKO_LISTEN_ADDRESS", "0.0.0.0:8080"),
		DatabaseURL:            servingDatabaseURL(os.Getenv),
		Environment:            envOr("BANSOKO_ENVIRONMENT", "development"),
		SkipAuth:               os.Getenv("BANSOKO_SKIP_AUTH") == "true",
		SchemaDir:              envOr("BANSOKO_SCHEMA_DIR", "db/migrations"),
		SessionSecret:          os.Getenv("BANSOKO_SESSION_SECRET"),
		SecretEncryptionKey:    os.Getenv("BANSOKO_SECRET_ENCRYPTION_KEY"),
		FrontendDir:            envOr("BANSOKO_FRONTEND_DIR", "frontend/dist"),
		AttrRulesConfigPath:    envOr("BANSOKO_CONFIG_DIR", "/config") + "/attr-rules.yaml",
		AttrRulesDefaultsPath:  "deployment/defaults/attr-rules.yaml",
		NamingVarsConfigPath:   envOr("BANSOKO_CONFIG_DIR", "/config") + "/naming-vars.js",
		NamingVarsDefaultsPath: "deployment/defaults/naming-vars.js",
		WorkerDisabled:         os.Getenv("BANSOKO_WORKER_DISABLED") == "true",
		UpdateMode:             UpdateModeFromEnv(os.Getenv),
		updateModeExplicit:     strings.TrimSpace(os.Getenv("BANSOKO_UPDATE_MODE")) != "",
	}
	fixtureHosts, err := parseFixtureHosts(os.Getenv("BANSOKO_TEST_FIXTURE_HOSTS"))
	if err != nil {
		return Config{}, err
	}
	cfg.TestFixtureHosts = fixtureHosts
	flags.StringVar(&cfg.ListenAddress, "listen", cfg.ListenAddress, "HTTP listen address")
	flags.StringVar(&cfg.DatabaseURL, "database-url", cfg.DatabaseURL, "PostgreSQL DSN")
	flags.StringVar(&cfg.Environment, "environment", cfg.Environment, "runtime environment")
	flags.BoolVar(&cfg.SkipAuth, "skip-auth", cfg.SkipAuth, "development-only auth bypass")
	flags.StringVar(&cfg.SchemaDir, "schema-dir", cfg.SchemaDir, "migration directory")
	flags.StringVar(&cfg.SessionSecret, "session-secret", cfg.SessionSecret, "persistent session HMAC secret")
	flags.StringVar(&cfg.FrontendDir, "frontend-dir", cfg.FrontendDir, "built frontend directory")
	flags.StringVar(&cfg.UpdateMode, "update-mode", cfg.UpdateMode, "deployment update mode: embedded or manual")
	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	flags.Visit(func(flag *flag.Flag) {
		if flag.Name == "update-mode" {
			cfg.updateModeExplicit = true
		}
	})
	if cfg.Environment == "production" && len(cfg.TestFixtureHosts) > 0 {
		return Config{}, ErrProductionFixtureHosts
	}
	if len(cfg.TestFixtureHosts) > 0 && cfg.Environment != "development" && cfg.Environment != "test" {
		return Config{}, ErrFixtureHostsEnvironment
	}
	if cfg.Environment == "production" && cfg.SkipAuth {
		return Config{}, ErrProductionSkipAuth
	}
	if cfg.Environment == "production" && !cfg.updateModeExplicit {
		return Config{}, ErrProductionUpdateMode
	}
	if !IsValidUpdateMode(cfg.UpdateMode) {
		return Config{}, fmt.Errorf("BANSOKO_UPDATE_MODE must be one of embedded, manual (got %q)", cfg.UpdateMode)
	}
	if cfg.Environment == "production" && len(strings.TrimSpace(cfg.SessionSecret)) < 32 {
		return Config{}, ErrProductionSessionSecret
	}
	if cfg.ListenAddress == "" {
		return Config{}, fmt.Errorf("listen address is required")
	}
	return cfg, nil
}

// TestFixtureNetwork returns the one explicit SSRF test-fixture injection. An
// empty mapping returns nil values so callers retain the normal resolver and
// private-address rejection behavior without a special test transport.
func (c Config) TestFixtureNetwork() ([]string, func(context.Context, string) ([]net.IP, error)) {
	if len(c.TestFixtureHosts) == 0 {
		return nil, nil
	}
	hosts := make([]string, 0, len(c.TestFixtureHosts))
	for host := range c.TestFixtureHosts {
		hosts = append(hosts, host)
	}
	resolver := func(ctx context.Context, host string) ([]net.IP, error) {
		if ip, ok := c.TestFixtureHosts[normalizeFixtureHost(host)]; ok {
			return []net.IP{ip}, nil
		}
		return net.DefaultResolver.LookupIP(ctx, "ip", host)
	}
	return hosts, resolver
}

// servingDatabaseURL is the connection the serving pool uses. In a container the
// bootstrap derives the runtime login connection (BANSOKO_RUNTIME_DATABASE_URL)
// from the schema-owner BANSOKO_DATABASE_URL; a process started without the
// bootstrap (native development, test harnesses) serves with BANSOKO_DATABASE_URL.
// Production startup still refuses an owner or superuser serving login.
func servingDatabaseURL(getenv func(string) string) string {
	if runtime := strings.TrimSpace(getenv("BANSOKO_RUNTIME_DATABASE_URL")); runtime != "" {
		return runtime
	}
	return getenv("BANSOKO_DATABASE_URL")
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// ApplicationDataDirs returns the absolute directories that hold this
// instance's own state: the deployment-owned config workspace (configuration
// and secrets, the bootstrap workspace), the embedded-update workspace.
//
// The file manager takes these as its refusal set, so an owner browsing the
// container can never read or delete the application out from under itself.
// Which container paths those are is the deployment's configuration, resolved
// here at the composition root rather than hardcoded in the browser. Empty
// entries are dropped: an unconfigured directory denies nothing.
func ApplicationDataDirs(getenv func(string) string) []string {
	candidates := []string{
		firstNonEmpty(getenv("BANSOKO_CONFIG_DIR"), bootprepare.DefaultConfigDir),
		deploymentidentity.UpdatesRoot(getenv),
	}
	dirs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate != "" {
			dirs = append(dirs, candidate)
		}
	}
	return dirs
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// parseFixtureHosts parses BANSOKO_TEST_FIXTURE_HOSTS ("host=ip,host2=ip2")
// strictly. The setting is an explicit security exception, so malformed
// entries fail startup instead of silently changing the requested fixture.
func parseFixtureHosts(raw string) (map[string]net.IP, error) {
	hosts := map[string]net.IP{}
	if strings.TrimSpace(raw) == "" {
		return hosts, nil
	}
	for _, pair := range strings.Split(raw, ",") {
		host, ipText, ok := strings.Cut(strings.TrimSpace(pair), "=")
		host = normalizeFixtureHost(host)
		ipText = strings.TrimSpace(ipText)
		ip := net.ParseIP(ipText)
		if !ok || host == "" || net.ParseIP(host) != nil || ip == nil {
			return nil, fmt.Errorf("%w: %q", ErrFixtureHostsConfig, pair)
		}
		hosts[host] = ip
	}
	return hosts, nil
}

func normalizeFixtureHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}
