package application

import (
	"encoding/json"
	"testing"

	"bansoko/internal/batchplan"
)

// Each release file shows its conclusion in the accepted plan; a release
// without a plan shows none.
func TestApplyPlanOutcomesFromTheFrozenPlan(t *testing.T) {
	plan := batchplan.Plan{Version: batchplan.Version, Files: []batchplan.File{
		{SourcePath: "Show/Show - 01.mkv", Outcome: batchplan.OutcomeCoveredExisting, ReasonCode: batchplan.ReasonCoveredExisting},
		{SourcePath: "Show/Show - 02.mkv", Outcome: batchplan.OutcomePlanned},
	}}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	files := []ReleaseFile{{Path: "Show/Show - 01.mkv"}, {Path: "Show/Show - 02.mkv"}, {Path: "Show/cover.jpg"}}
	applyPlanOutcomes(&files, raw)
	if files[0].Outcome == nil || *files[0].Outcome != "covered_existing" || files[0].OutcomeReason == nil || *files[0].OutcomeReason != "covered_existing" {
		t.Fatalf("covered file = %+v", files[0])
	}
	if files[1].Outcome == nil || *files[1].Outcome != "planned" || files[1].OutcomeReason != nil {
		t.Fatalf("planned file = %+v", files[1])
	}
	if files[2].Outcome != nil {
		t.Fatalf("a file the plan does not name has no outcome: %+v", files[2])
	}
	bare := []ReleaseFile{{Path: "Show/Show - 01.mkv"}}
	applyPlanOutcomes(&bare, nil)
	if bare[0].Outcome != nil {
		t.Fatal("no plan, no outcome")
	}
}
