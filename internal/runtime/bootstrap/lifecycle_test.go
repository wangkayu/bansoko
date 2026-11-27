package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bansoko/internal/runtime/bootsecrets"
	"bansoko/internal/runtime/deploymentidentity"
)

func TestDeploymentIdentityEnvMarksNativeAnchorWithoutImageDigest(t *testing.T) {
	// No BANSOKO_BASE_IMAGE_DIGEST: a native base is anchored by kind instead
	// of a fabricated digest.
	env := deploymentIdentityEnv(deploymentidentity.SourceBase, "", "", "")
	if env[deploymentidentity.EnvAnchorKind] != deploymentidentity.AnchorNative {
		t.Fatalf("native anchor = %q, want %q", env[deploymentidentity.EnvAnchorKind], deploymentidentity.AnchorNative)
	}
}

func TestDeploymentIdentityEnvDoesNotInferDockerFromDigest(t *testing.T) {
	t.Setenv(deploymentidentity.EnvBaseImageDigest, "sha256:"+strings.Repeat("ab", 32))
	env := deploymentIdentityEnv(deploymentidentity.SourceBase, "", "", "")
	if _, docker := readImageAnchor(); !docker && env[deploymentidentity.EnvAnchorKind] != deploymentidentity.AnchorNative {
		t.Fatalf("native anchor = %q despite no image executable", env[deploymentidentity.EnvAnchorKind])
	}
}

func TestNativeAnchoredBaseIdentityValidates(t *testing.T) {
	identity := deploymentidentity.Identity{
		SelectedSource: deploymentidentity.SourceBase,
		Anchor:         deploymentidentity.AnchorNative,
	}
	if err := identity.Validate(nil); err != nil {
		t.Fatalf("native base identity rejected: %v", err)
	}
}

func TestBaseServerBinaryPrefersContainerPath(t *testing.T) {
	if _, err := os.Stat(baseServerPath); err == nil {
		t.Skip("container layout present; host-native resolution is not exercised here")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if got := baseServerBinary(); got != exe {
		t.Fatalf("baseServerBinary = %q, want the running binary %q outside a container layout", got, exe)
	}
}

func TestBaseAssetDirOnlyReportsExistingDirs(t *testing.T) {
	if got := baseAssetDir(filepath.Join(t.TempDir(), "absent")); got != "" {
		t.Fatalf("missing asset dir = %q, want empty (embedded fallback)", got)
	}
	dir := t.TempDir()
	if got := baseAssetDir(dir); got != dir {
		t.Fatalf("existing asset dir = %q, want %q", got, dir)
	}
	if _, err := os.Stat(baseSchemaDir); err == nil {
		t.Skip("container layout present; native asset resolution is not exercised here")
	}
	if got := baseAssetDir(baseSchemaDir); got != "" {
		t.Fatalf("container schema dir = %q on a host without it, want empty", got)
	}
}

func TestPrepareDeploymentEnvironmentCreatesMissingConfigDir(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "absent")
	env := newEnvRecorder(map[string]string{
		bootsecrets.EnvDatabaseURL: "postgres://owner:pw@db/bansoko",
		bootsecrets.EnvConfigDir:   configDir,
	})
	if err := prepareDeploymentEnvironment(env.get, env.setenv, discardLog); err != nil {
		t.Fatalf("a native first run must create its config dir: %v", err)
	}
	info, err := os.Stat(configDir)
	if err != nil || !info.IsDir() {
		t.Fatalf("config dir was not created: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("config dir mode = %o, want 0700", info.Mode().Perm())
	}
	if _, err := os.Stat(bootsecrets.FilePath(configDir)); err != nil {
		t.Fatalf("secrets file was not generated: %v", err)
	}
}

func TestApplyNativeDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, goos, environment, mode, lane string
		overrides                           map[string]string
	}{
		{name: "Linux", goos: "linux", environment: "production", mode: "embedded", lane: "stable"},
		{name: "macOS", goos: "darwin", environment: "production", mode: "manual"},
		{name: "explicit manual", goos: "linux", environment: "test", mode: "manual", overrides: map[string]string{"BANSOKO_ENVIRONMENT": "test", "BANSOKO_UPDATE_MODE": "manual"}},
		{name: "explicit test lane", goos: "linux", environment: "production", mode: "embedded", lane: "test", overrides: map[string]string{"BANSOKO_UPDATE_LANE": "test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newEnvRecorder(tc.overrides)
			if err := applyNativeDefaults(tc.goos, env.get, env.setenv); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{"BANSOKO_ENVIRONMENT": tc.environment, "BANSOKO_UPDATE_MODE": tc.mode, "BANSOKO_UPDATE_LANE": tc.lane} {
				if got := env.get(key); got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
		})
	}
}
