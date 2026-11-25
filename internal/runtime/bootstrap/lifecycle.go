package bootstrap

import (
	"context"
	"io"
	"os"
	"runtime"
	"strings"

	"bansoko/internal/runtime/config"
	updateapplication "bansoko/internal/update/application"
)

// nativeAnchorEnv carries the installed binary across verified overlay handoffs.
const nativeAnchorEnv = "BANSOKO_NATIVE_ANCHOR_EXECUTABLE"

// lifecycle.go exposes the container PID1 runtime as the native single-binary
// lifecycle: `bansoko start` is the same unified bootstrap flow (deployment
// environment preparation, launch planning, embedded-update handoff, steady
// exec) running without container paths or privilege management. Container
// and native start share this flow so both deployments keep identical launch
// guarantees.

// Start runs the unified bootstrap flow for kind ("server" or "migrate"). It
// replaces the process image on success, exactly like the container PID1
// entrypoint, but performs no container privilege management: the process
// belongs to the invoking operator account.
func Start(kind string) int {
	if kind == "server" {
		anchor, err := os.Executable()
		if err == nil {
			err = os.Setenv(nativeAnchorEnv, anchor)
		}
		if err != nil {
			logf("resolve native anchor: %v", err)
			return 1
		}
		if err := applyNativeDefaults(runtime.GOOS, os.Getenv, os.Setenv); err != nil {
			logf("native startup defaults: %v", err)
			return 1
		}
	}
	return run(kind)
}

func applyNativeDefaults(goos string, getenv func(string) string, setenv func(string, string) error) error {
	mode := "manual"
	if goos == "linux" {
		mode = "embedded"
	}
	defaults := [][2]string{{"BANSOKO_ENVIRONMENT", "production"}, {"BANSOKO_UPDATE_MODE", mode}}
	for _, pair := range defaults {
		if strings.TrimSpace(getenv(pair[0])) == "" {
			if err := setenv(pair[0], pair[1]); err != nil {
				return err
			}
		}
	}
	if config.UpdateModeFromEnv(getenv) == "embedded" && strings.TrimSpace(getenv(updateapplication.EnvLane)) == "" {
		return setenv(updateapplication.EnvLane, updateapplication.LaneStable)
	}
	return nil
}

// ResetPassword promotes the `reset-password` subcommand into the
// shared operator reset flow without booting the full PID1 runtime.
func ResetPassword(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runResetPassword(context.Background(), args, stdin, stdout, stderr)
}

// Recover dispatches the top-level `recover` family (status/rollback/evidence
// ...) against the updates root under the native configuration directory.
func Recover(args []string, stdout, stderr io.Writer) int {
	return runRecoverCommand(context.Background(), args, stdout, stderr)
}
