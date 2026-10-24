package migrations

import "testing"

// A new artifact or plan takes the larger of its own attempt timeout
// (DDL DEFAULT / guard completion) and its accepted snapshot's bytes-scaled
// value; a snapshot below the policy number never lowers the row.
func TestAttemptTimeoutRaisedFromAcceptedSnapshot(t *testing.T) {
	for _, c := range []struct {
		tag                  string
		inspect, plan        int
		wantInspect, wantPln int
	}{
		{"budget-scaled", 4000, 3000, 4000, 3000},
		{"budget-floor", 90, 60, 300, 120},
	} {
		t.Run(c.tag, func(t *testing.T) {
			patch := map[string]any{
				"inspect": map[string]any{"max_attempts": 5, "attempt_timeout_seconds": c.inspect, "wall_seconds": 86400},
				"plan":    map[string]any{"max_attempts": 4, "attempt_timeout_seconds": c.plan, "wall_seconds": 86400},
			}
			owner, _, _, planID := seedBudgetOwners(t, c.tag, patch)
			if got := readArtifactBudget(t, owner, "plan-artifact").timeout; got != c.wantInspect {
				t.Fatalf("artifact timeout=%d want %d", got, c.wantInspect)
			}
			if got := readPlanBudget(t, owner, planID).timeout; got != c.wantPln {
				t.Fatalf("plan timeout=%d want %d", got, c.wantPln)
			}
		})
	}
}
