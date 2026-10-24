package main

import (
	"errors"
	"os"
	"os/exec"
	"testing"

	updateapp "bansoko/internal/update/application"
	embeddedupdate "bansoko/internal/update/journal"
)

// fixtureStopChildEnv selects the crash window the re-executed child runs.
const fixtureStopChildEnv = "BANSOKO_FIXTURE_STOP_CHILD_STEP"

func fixtureStopSeedSteps(target string) []embeddedupdate.Step {
	steps := []embeddedupdate.Step{
		embeddedupdate.StepAccepted,
		embeddedupdate.StepFetched,
		embeddedupdate.StepStaged,
		embeddedupdate.StepMaintenanceEntered,
		embeddedupdate.StepBackupVerified,
		embeddedupdate.StepPendingMigration,
	}
	if target == string(embeddedupdate.StepPromoted) {
		steps = append(steps, embeddedupdate.StepMigrationSucceeded)
	}
	return steps
}

// TestConsumeFixtureStopInjectionExitsNonZeroAtHandoffCheckpoints proves the
// consume-side crash windows use the real process exit: each handoff-family
// checkpoint the bootstrap consumer writes terminates PID1 with the fixture
// exit code, so a container restart policy (not an external poller) owns the
// replay. A child process is required because the injected crash is a real
// process termination.
func TestConsumeFixtureStopInjectionExitsNonZeroAtHandoffCheckpoints(t *testing.T) {
	for _, target := range []string{string(embeddedupdate.StepMigrationSucceeded), string(embeddedupdate.StepPromoted)} {
		t.Run(target, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestConsumeFixtureStopChild$", "-test.v")
			cmd.Env = append(os.Environ(),
				fixtureStopChildEnv+"="+target,
				"BANSOKO_ENVIRONMENT=test",
				updateapp.FixtureStopAfterEnv+"="+target,
				"BANSOKO_EMBEDDED_FIXTURE_RESTORE_FAILURE=0",
			)
			output, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("child exited with %v, want a non-zero termination; output:\n%s", err, output)
			}
			if exitErr.ExitCode() != updateapp.FixtureStopExitCode {
				t.Fatalf("child exit code = %d, want %d; output:\n%s",
					exitErr.ExitCode(), updateapp.FixtureStopExitCode, output)
			}
		})
	}
}

// TestConsumeFixtureStopChild is the re-executed helper of the test above. It
// is a no-op in the ordinary suite and only drives the consume flow when the
// parent selected a crash window.
func TestConsumeFixtureStopChild(t *testing.T) {
	target := os.Getenv(fixtureStopChildEnv)
	if target == "" {
		return
	}
	if os.Getenv(updateapp.FixtureStopAfterEnv) != target {
		t.Fatalf("child requires %s=%s", updateapp.FixtureStopAfterEnv, target)
	}

	root := t.TempDir()
	writeReleaseTree(t, root, testPrevRelease)
	writeReleaseTree(t, root, testTargetRelease)
	writePointer(t, root, testPrevRelease)
	record := seedConsumeJournal(t, root, "op-fixture-stop", nil, fixtureStopSeedSteps(target)...)

	outcome := drive(t, root, record, &fxRecorder{})
	t.Fatalf("consume flow survived the injected crash at %s with outcome %+v", target, outcome)
}

// TestConsumeFixtureStopInjectionIsInertInProduction drives the same handoff
// window with the injection armed in a production deployment: the flow must
// reach its normal terminal state instead of disappearing, proving production
// ignores the fixture knob entirely.
func TestConsumeFixtureStopInjectionIsInertInProduction(t *testing.T) {
	t.Setenv("BANSOKO_ENVIRONMENT", "production")
	t.Setenv(updateapp.FixtureStopAfterEnv, string(embeddedupdate.StepMigrationSucceeded))
	t.Setenv("BANSOKO_EMBEDDED_FIXTURE_RESTORE_FAILURE", "0")

	root := t.TempDir()
	writeReleaseTree(t, root, testPrevRelease)
	writeReleaseTree(t, root, testTargetRelease)
	writePointer(t, root, testPrevRelease)
	record := seedConsumeJournal(t, root, "op-fixture-stop-production", nil, fixtureStopSeedSteps("")...)

	outcome := drive(t, root, record, &fxRecorder{})
	if outcome.kind != outcomeExit || outcome.exitCode != 0 {
		t.Fatalf("outcome = %+v, want a clean exit", outcome)
	}
	final := reloadRecordForTest(t, root)
	if final.Step != embeddedupdate.StepSucceeded {
		t.Fatalf("final step %q, want succeeded (production must ignore the crash injection)", final.Step)
	}
}
