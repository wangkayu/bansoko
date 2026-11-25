// Package configdir resolves the deployment configuration directory (the
// directory that holds secrets.json and, by default, the embedded-update
// workspace) for both container and native single-binary deployments.
//
// Resolution precedence:
//
//  1. the --config-dir CLI flag,
//  2. BANSOKO_CONFIG_DIR,
//  3. /config, but only when it already exists and is writable (this keeps
//     container behavior unchanged),
//  4. the OS user configuration directory (os.UserConfigDir) + "bansoko".
//
// The fourth level lets a native binary run without any environment setup,
// while the writability probe on /config makes sure a host that happens to
// have an unrelated /config directory is not picked silently.
package configdir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvConfigDir is the environment override for the configuration directory.
const EnvConfigDir = "BANSOKO_CONFIG_DIR"

// ContainerConfigDir is the container configuration directory kept as the
// third precedence level.
const ContainerConfigDir = "/config"

// FlagName is the CLI flag every lifecycle subcommand accepts.
const FlagName = "config-dir"

// Resolve returns the effective configuration directory. flagValue is the
// value of the --config-dir flag ("" when the flag was not given). The result
// is never empty; on exotic platforms without a user configuration directory
// the container path is the final fallback so behavior stays deterministic.
func Resolve(flagValue string, getenv func(string) string) string {
	if value := strings.TrimSpace(flagValue); value != "" {
		return value
	}
	if getenv != nil {
		if value := strings.TrimSpace(getenv(EnvConfigDir)); value != "" {
			return value
		}
	}
	if WritableDir(ContainerConfigDir) {
		return ContainerConfigDir
	}
	if dir, err := os.UserConfigDir(); err == nil && strings.TrimSpace(dir) != "" {
		return filepath.Join(dir, "bansoko")
	}
	return ContainerConfigDir
}

// FlagValue extracts the --config-dir value from args, tolerating both
// "--config-dir value" and "--config-dir=value" spellings. It returns ""
// when the flag is absent. Used to resolve the configuration directory before
// full flag parsing (which needs the resolved directory itself, e.g. to load
// stored secrets).
func FlagValue(args []string) string {
	for i, arg := range args {
		if arg == "--"+FlagName {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if strings.HasPrefix(arg, "--"+FlagName+"=") {
			return strings.TrimPrefix(arg, "--"+FlagName+"=")
		}
	}
	return ""
}

// WritableDir reports whether path is an existing directory the process can
// create files in. The probe writes and removes a temporary file because
// permission bits alone do not cover read-only mounts.
func WritableDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	probe, err := os.CreateTemp(path, ".bansoko-write-probe-*")
	if err != nil {
		return false
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return true
}

// EnsureDir creates the configuration directory when missing so native
// first-run flows can persist generated secrets. Existing directories keep
// their permissions.
func EnsureDir(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("config directory path is empty")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create config directory %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat config directory %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("config directory %s is not a directory", path)
	}
	return nil
}
