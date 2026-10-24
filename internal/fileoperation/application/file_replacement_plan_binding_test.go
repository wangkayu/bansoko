package application

import (
	"testing"
)

// TestOverwritePlanAbsentTargetIsEnsurePresent: overwrite only authorizes
// replacing an existing file, so an absent target (no overwrite execution
// snapshot) plans as an ordinary block/ensure_present operation.
func TestOverwritePlanAbsentTargetIsEnsurePresent(t *testing.T) {
	plan := operationPlan{collisionPolicy: "overwrite", intent: "ensure_present", targetPath: "/target"}
	if got := downgradeAbsentOverwrite(plan); got.collisionPolicy != "block" || got.intent != "ensure_present" {
		t.Fatalf("absent overwrite target plan = %q/%q, want block/ensure_present", got.collisionPolicy, got.intent)
	}
}
