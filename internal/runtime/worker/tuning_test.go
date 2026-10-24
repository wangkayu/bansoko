package worker

import (
	"testing"
	"time"
)

func TestScaleStorageAttemptAppliesFloorAndCap(t *testing.T) {
	const gib = int64(1) << 30
	if got := ScaleStorageAttempt(300*time.Second, gib, 1); got != 403*time.Second {
		t.Fatalf("1 GiB: %v", got)
	}
	if got := ScaleStorageAttempt(300*time.Second, 1<<50, 6); got != MaxStorageAttemptTimeout {
		t.Fatalf("cap: %v", got)
	}
	if got := ScaleStorageAttempt(120*time.Second, -1, 0); got != 120*time.Second {
		t.Fatalf("degenerate inputs: %v", got)
	}
}

func TestStorageHandlerDeadlinesExceedScaledAttemptCap(t *testing.T) {
	for _, p := range DefaultPolicies() {
		if p.AttemptTimeout > 0 && p.Deadline <= MaxStorageAttemptTimeout {
			t.Fatalf("%s handler deadline %v does not exceed the scaled attempt cap", p.Kind, p.Deadline)
		}
	}
}
