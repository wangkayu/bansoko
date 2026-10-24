package application

import (
	"bansoko/internal/workitems"
	"context"
	"testing"
)

// The committed batch of a prepared plan survives recovery of its sealed graph.
func TestPGPreparedPlanCommittedBatchRecoveryPreservesOperations(t *testing.T) {
	s, c := preparedPlanPGFixture(t)
	ctx := context.Background()
	if _, err := s.PostProcessingPlanHandler().Handle(ctx, c); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM file_operations WHERE postprocessing_plan_id=$1`, c.Owner.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("fixture did not commit operations")
	}
	before := planGraphSnapshot(t, s.db)
	out, ok, err := s.confirmTypedPlanCommit(ctx, c)
	if err != nil || !ok {
		t.Fatalf("complete graph confirmation: %v ok=%v", err, ok)
	}
	if out.OwnerResult.State != workitems.StateSucceeded || out.WriteOwner != nil || out.PostTransition != nil {
		t.Fatal("confirmation manufactured writer")
	}
	if after := planGraphSnapshot(t, s.db); after != before {
		t.Fatal("confirmation changed committed operations or graph")
	}
}
