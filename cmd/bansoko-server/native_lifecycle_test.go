package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bansoko/internal/mediaprobe"
	"bansoko/internal/runtime/configdir"
	"bansoko/internal/runtime/migrations"
)

func TestRunVersionReportsBuildAndSchemaIdentity(t *testing.T) {
	var out bytes.Buffer
	if code := runVersion(&out); code != 0 {
		t.Fatalf("runVersion exit code = %d", code)
	}
	text := out.String()
	for _, want := range []string{"bansoko ", "commit", "target schema version: " + migrations.TargetVersion(), "embedded frontend: ", "platform: "} {
		if !strings.Contains(text, want) {
			t.Fatalf("version output %q lacks %q", text, want)
		}
	}
}

func TestDoctorConfigDirReportsWritableExistingDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BANSOKO_CONFIG_DIR", dir)
	result := doctorConfigDir(nil)
	if result.verdict != doctorOK {
		t.Fatalf("existing writable dir verdict = %s (%s)", result.verdict, result.detail)
	}
}

func TestDoctorConfigDirReportsCreatableMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bansoko")
	t.Setenv("BANSOKO_CONFIG_DIR", dir)
	result := doctorConfigDir(nil)
	if result.verdict != doctorOK {
		t.Fatalf("creatable missing dir verdict = %s (%s)", result.verdict, result.detail)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("doctor must stay read-only; %s was created", dir)
	}
}

func TestDoctorConfigDirHonoursFlagPrecedence(t *testing.T) {
	flagDir := t.TempDir()
	envDir := t.TempDir()
	t.Setenv("BANSOKO_CONFIG_DIR", envDir)
	result := doctorConfigDir([]string{"--config-dir", flagDir})
	if result.verdict != doctorOK || !strings.Contains(result.detail, flagDir) {
		t.Fatalf("flag precedence result = %+v", result)
	}
}

func TestDoctorPostgresFailsWithoutOwnerDSN(t *testing.T) {
	t.Setenv("BANSOKO_DATABASE_URL", "")
	result := doctorPostgres(t.Context())
	if result.verdict != doctorFail || !strings.Contains(result.detail, "BANSOKO_DATABASE_URL") {
		t.Fatalf("missing DSN result = %+v", result)
	}
}

func TestDoctorMediaInfoWithEmptyPath(t *testing.T) {
	t.Setenv("PATH", "")
	result := doctorMediaInfo(t.Context(), mediaprobe.SelfCheck)
	if result.verdict != doctorOK || !strings.Contains(result.detail, "MediaInfoLib") {
		t.Fatalf("MediaInfo check = %+v", result)
	}
	failed := doctorMediaInfo(t.Context(), func(context.Context) (string, error) { return "", errors.New("probe failed") })
	if failed.verdict != doctorFail || failed.detail != "probe failed" {
		t.Fatalf("failed MediaInfo check = %+v", failed)
	}
}

func TestDoctorMissingPostgresTools(t *testing.T) {
	for _, tc := range []struct {
		goos, mode string
		want       doctorVerdict
	}{
		{"linux", "", doctorFail},
		{"linux", "embedded", doctorFail},
		{"linux", "manual", doctorWarn},
		{"darwin", "", doctorWarn},
	} {
		t.Run(tc.goos+"/"+tc.mode, func(t *testing.T) {
			t.Setenv("BANSOKO_UPDATE_MODE", tc.mode)
			result := doctorPostgresTools(tc.goos, nil)
			if result.verdict != tc.want || !strings.Contains(result.detail, "postgresql-client-17") {
				t.Fatalf("PG tools check = %+v, want %s with installation guidance", result, tc.want)
			}
		})
	}
}
func TestDoctorVerdictsPropagateToExitCode(t *testing.T) {
	results := []doctorResult{
		{name: "a", verdict: doctorOK, detail: "fine"},
		{name: "b", verdict: doctorWarn, detail: "careful"},
	}
	code := 0
	for _, result := range results {
		if result.verdict == doctorFail {
			code = 1
		}
	}
	if code != 0 {
		t.Fatalf("warnings must not fail doctor")
	}
}

func TestExportConfigDirFlag(t *testing.T) {
	dir := t.TempDir()
	for _, command := range []string{"start", "serve", "migrate", "doctor", "update", "reset-password", "recover"} {
		t.Run(command, func(t *testing.T) {
			for _, flags := range [][]string{{"--config-dir", dir}, {"--config-dir=" + dir}} {
				t.Setenv(configdir.EnvConfigDir, t.TempDir())
				argv, err := exportConfigDirFlag(append([]string{command, "--help"}, flags...))
				if err != nil {
					t.Fatal(err)
				}
				mode, args := runtimeCommand(argv)
				if mode != command || len(args) != 1 || args[0] != "--help" {
					t.Fatalf("dispatch = %q %q", mode, args)
				}
				if got := os.Getenv(configdir.EnvConfigDir); got != dir {
					t.Fatalf("exported config dir = %q, want %q", got, dir)
				}
			}
		})
	}
}

func TestExportConfigDirFlagRejectsMissingValue(t *testing.T) {
	for _, args := range [][]string{{"start", "--config-dir"}, {"serve", "--config-dir", "--help"}, {"update", "--config-dir="}} {
		if _, err := exportConfigDirFlag(args); err == nil {
			t.Fatalf("accepted malformed arguments %q", args)
		}
	}
}
