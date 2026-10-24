package application

import (
	"context"
	"testing"
)

func TestCommittedPlanMissingInitialWorkBlocked(t *testing.T) {
	s, c := preparedPlanPGFixture(t)
	if _, err := s.PostProcessingPlanHandler().Handle(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	var artifact, root string
	if err := s.db.QueryRow(`SELECT artifact_id,acquisition_id FROM postprocessing_plans WHERE id=$1`, c.Owner.ID).Scan(&artifact, &root); err != nil {
		t.Fatal(err)
	}
	owner := ownerTestDB(t)
	if _, err := owner.Exec(`DELETE FROM work_items WHERE owner_type='file_operation' AND owner_id IN (SELECT id FROM file_operations WHERE postprocessing_plan_id=$1)`, c.Owner.ID); err != nil {
		t.Fatal(err)
	}
	before := planGraphSnapshot(t, owner)
	useRuntimePlanObserver(t, s)
	out, err := s.ObserveCommittedPlan(context.Background(), CommittedPlanObservationRequest{PlanID: c.Owner.ID, ArtifactID: artifact, AcquisitionID: root})
	if err != nil || out.Status != "blocked" || out.DetailCode != "CHILD_WORK_MISSING" {
		t.Fatalf("%+v %v", out, err)
	}
	if planGraphSnapshot(t, owner) != before {
		t.Fatal("missing work repaired")
	}
	if _, ok, err := s.confirmTypedPlanCommit(context.Background(), c); err != nil || ok {
		t.Fatalf("unknown proof accepted missing work=%v %v", ok, err)
	}
}
