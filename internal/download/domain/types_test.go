package domain

import "testing"

// Pure enum/type contract tests: download/domain is the pure logic layer of
// the owner state machine and the effect snapshots. Covers the constant
// completeness of TaskState/CancellationState/OperationKind/OperationPhase,
// the stability of the state strings (DB and JSON depend on these literals),
// and the structure semantics of the frozen path snapshot.

func TestTaskStateConstantsAreStable(t *testing.T) {
	expected := map[TaskState]string{
		TaskQueued:      "queued",
		TaskSubmitted:   "submitted",
		TaskDownloading: "downloading",
		TaskCompleted:   "completed",
		TaskFailed:      "failed",
		TaskCanceled:    "canceled",
	}
	if len(expected) != 6 {
		t.Fatalf("task state count = %d, want 6", len(expected))
	}
	for state, want := range expected {
		if string(state) != want {
			t.Errorf("TaskState(%q) != %q", state, want)
		}
	}
}

func TestCancellationStatesAreStable(t *testing.T) {
	if string(CancellationRequested) != "requested" || string(CancellationSucceeded) != "succeeded" ||
		string(CancellationNoOp) != "no_op" || string(CancellationFailed) != "failed" {
		t.Fatal("cancellation state literals drifted")
	}
	if string(CancellationOutcomeCanceled) != "canceled" || string(CancellationOutcomeAlreadyCanceled) != "already_canceled" ||
		string(CancellationOutcomeAlreadyCompleted) != "already_completed" || string(CancellationOutcomeTaskChanged) != "task_changed" ||
		string(CancellationOutcomeProviderFailed) != "provider_failed" {
		t.Fatal("cancellation outcome literals drifted")
	}
}

func TestRetryAndProviderErrorClassesAreStable(t *testing.T) {
	if string(RetryRequested) != "requested" || string(RetrySucceeded) != "succeeded" ||
		string(RetryFailed) != "failed" || string(RetrySuperseded) != "superseded" {
		t.Fatal("retry state literals drifted")
	}
	classes := []ProviderErrorClass{ProviderErrorUnknown, ProviderErrorRetryable, ProviderErrorAmbiguous, ProviderErrorTerminal}
	want := []string{"unknown", "retryable", "ambiguous", "terminal"}
	for i, c := range classes {
		if string(c) != want[i] {
			t.Fatalf("provider error class %q != %q", c, want[i])
		}
	}
}

func TestOperationKindAndPhaseAreStable(t *testing.T) {
	kinds := []OperationKind{OperationSubmit, OperationRetry, OperationCancel}
	wantKinds := []string{"submit", "retry", "cancel"}
	for i, k := range kinds {
		if string(k) != wantKinds[i] {
			t.Fatalf("operation kind %q != %q", k, wantKinds[i])
		}
	}
	phases := []OperationPhase{OperationReserved, OperationExecuting, OperationRecovery}
	wantPhases := []string{"reserved", "executing", "recovery"}
	for i, p := range phases {
		if string(p) != wantPhases[i] {
			t.Fatalf("operation phase %q != %q", p, wantPhases[i])
		}
	}
}

func TestVerificationResultIsStable(t *testing.T) {
	if string(VerificationVerified) != "verified" || string(VerificationWithConflicts) != "verified_with_conflicts" ||
		string(VerificationFailed) != "failed" {
		t.Fatal("verification result literals drifted")
	}
}

func TestPathSnapshotFreezesOnlyRoots(t *testing.T) {
	snapshot := PathSnapshot{RemoteRoot: "/downloads", ApplicationRoot: "/library"}
	if snapshot.RemoteRoot != "/downloads" || snapshot.ApplicationRoot != "/library" {
		t.Fatalf("path snapshot = %+v", snapshot)
	}
	// The snapshot deliberately carries no credentials or connection URL.
	if snapshot.RemoteRoot == "" || snapshot.ApplicationRoot == "" {
		t.Fatal("path snapshot roots must be non-empty for a real effect")
	}
}
