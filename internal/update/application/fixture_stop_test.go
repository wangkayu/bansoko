package application

import (
	"os"
	"strings"
	"testing"
)

// captureFixtureStop captures the injection banner and neutralises the process
// exit the fixture stop would otherwise perform, so the window can be observed
// without terminating the test process.
func captureFixtureStop(t *testing.T, step func()) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stderr-*")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	restore := fixtureStopExit
	os.Stderr = file
	fixtureStopExit = func(int) {}
	defer func() {
		os.Stderr = previous
		fixtureStopExit = restore
	}()
	step()
	os.Stderr = previous
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestFixtureInjectionBannersNameTheirWindow pins the two banners the real
// Docker matrix greps out of the app log: the staging window is detected by the
// pre-marker banner and a durable checkpoint window by the checkpoint banner.
// The names are a contract with scripts/ci/embedded-update-docker-fixture.sh, so
// renaming either line silently breaks that row's crash detection.
func TestFixtureInjectionBannersNameTheirWindow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arm     func(t *testing.T)
		trigger func()
		want    string
	}{
		{
			name: "before marker",
			arm: func(t *testing.T) {
				t.Setenv("BANSOKO_ENVIRONMENT", "test")
				t.Setenv(FixtureStopBeforeMarkerEnv, "1")
			},
			trigger: applyFixtureStopBeforeMarker,
			want:    "FIXTURE INJECTION: before marker",
		},
		{
			name: "durable checkpoint",
			arm: func(t *testing.T) {
				t.Setenv("BANSOKO_ENVIRONMENT", "test")
				t.Setenv(FixtureStopAfterEnv, "staged")
			},
			trigger: func() { ApplyFixtureStop("staged") },
			want:    `FIXTURE INJECTION: durable checkpoint "staged"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.arm(t)
			banner := captureFixtureStop(t, tc.trigger)
			if !strings.Contains(banner, tc.want) {
				t.Fatalf("banner %q does not contain %q", banner, tc.want)
			}
		})
	}
}

// TestFixtureInjectionIsInertInProduction proves neither window can fire outside
// a non-production deployment: the matrix's crash rows must be impossible to arm
// against a real instance.
func TestFixtureInjectionIsInertInProduction(t *testing.T) {
	t.Setenv("BANSOKO_ENVIRONMENT", "production")
	t.Setenv(FixtureStopBeforeMarkerEnv, "1")
	t.Setenv(FixtureStopAfterEnv, "staged")
	banner := captureFixtureStop(t, func() {
		applyFixtureStopBeforeMarker()
		ApplyFixtureStop("staged")
		ApplyFixtureStop("pending_backup")
	})
	if banner != "" {
		t.Fatalf("production emitted a fixture injection banner: %q", banner)
	}
}

// TestFixtureStopAfterOnlyMatchesItsExactStep keeps the window selector honest:
// a differently spelled step must not arm the injection.
func TestFixtureStopAfterOnlyMatchesItsExactStep(t *testing.T) {
	t.Setenv("BANSOKO_ENVIRONMENT", "test")
	t.Setenv(FixtureStopAfterEnv, "pending_backup")
	if !FixtureStopAfter("pending_backup") {
		t.Fatal("pending_backup window did not arm")
	}
	for _, other := range []string{"pending_migration", "pending_backup ", "staged", ""} {
		if FixtureStopAfter(other) {
			t.Fatalf("window armed for %q", other)
		}
	}
}
