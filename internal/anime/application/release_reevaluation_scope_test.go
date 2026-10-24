package application

import (
	"strings"
	"testing"
)

// The Go form and the SQL form of the re-evaluation scope name the same
// excluded verdicts and the same submitted gate.
func TestReleaseReevaluationScopeFormsAgree(t *testing.T) {
	if !strings.Contains(ReleaseReevaluationScopeSQL, "r.download_task_id IS NULL") {
		t.Fatalf("scope SQL lost the submitted gate: %s", ReleaseReevaluationScopeSQL)
	}
	for verdict := range releaseReevaluationExcludedVerdicts {
		if !strings.Contains(ReleaseReevaluationScopeSQL, "'"+verdict+"'") {
			t.Fatalf("scope SQL does not exclude %q", verdict)
		}
		if ReleaseInReevaluationScope(verdict, false) {
			t.Fatalf("%q is in scope", verdict)
		}
	}
	if got := strings.Count(ReleaseReevaluationScopeSQL, "'"); got != 2*len(releaseReevaluationExcludedVerdicts) {
		t.Fatalf("scope SQL names %d quoted values, want %d", got/2, len(releaseReevaluationExcludedVerdicts))
	}
	if !ReleaseInReevaluationScope("filtered", false) || ReleaseInReevaluationScope("filtered", true) {
		t.Fatal("an unsubmitted release is in scope and a submitted one is not")
	}
}
