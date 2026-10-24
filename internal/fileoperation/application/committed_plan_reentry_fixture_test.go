package application

import (
	"bansoko/internal/workitems"
	"context"
	"testing"
)

func observeFixturePlan(s *Service, artifact string, ref *workitems.OneShotRef) (CommittedPlanObservation, error) {
	var plan, root string
	if err := s.db.QueryRow(`SELECT id,acquisition_id FROM postprocessing_plans WHERE artifact_id=$1`, artifact).Scan(&plan, &root); err != nil {
		return CommittedPlanObservation{Status: "observation_error"}, err
	}
	return s.ObserveCommittedPlan(context.Background(), CommittedPlanObservationRequest{PlanID: plan, ArtifactID: artifact, AcquisitionID: root, OneShot: ref})
}
func assertCommittedReentry(t *testing.T, dbService *Service, artifact string, ref *workitems.OneShotRef, status, detail string) {
	t.Helper()
	observer := *dbService
	useRuntimePlanObserver(t, &observer)
	var plan, root string
	if err := dbService.db.QueryRow(`SELECT id,acquisition_id FROM postprocessing_plans WHERE artifact_id=$1`, artifact).Scan(&plan, &root); err != nil {
		t.Fatal(err)
	}
	before := planGraphSnapshot(t, dbService.db)
	out, err := observer.ObserveCommittedPlan(context.Background(), CommittedPlanObservationRequest{PlanID: plan, ArtifactID: artifact, AcquisitionID: root, OneShot: ref})
	if err != nil || out.Status != status || out.DetailCode != detail {
		t.Fatalf("observation=%+v err=%v", out, err)
	}
	if status == "blocked" && out.ReasonCode != "PLAN_RECOVERY_GRAPH_INCONSISTENT" {
		t.Fatal(out)
	}
	if planGraphSnapshot(t, dbService.db) != before {
		t.Fatal("reentry changed graph")
	}
}
