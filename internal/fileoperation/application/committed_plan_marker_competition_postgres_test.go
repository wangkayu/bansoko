package application

import (
	"context"
	"errors"
	"testing"

	"bansoko/internal/workitems"
)

// The writer's committed epoch and its open transaction are observed in both
// orders. A held writer never makes observation lock or repair the marker.
func TestCommittedPlanObservationMarkerEpochOrders(t *testing.T) {
	db, f := openFileOperationDB(t)
	s := newFixtureService(t, db, f)
	const marker = "epoch-competition"
	seedOneShotMarkerFixture(t, db, marker)
	ref := workitems.OneShotRef{MarkerID: marker, CorrelationID: "corr-" + marker, AdmissionEpoch: 1}
	if err := prepareOneShotFixture(t, db, s, f.artifactID, ref); err != nil {
		t.Fatal(err)
	}
	var plan, root string
	if err := db.QueryRow(`SELECT id,acquisition_id FROM postprocessing_plans WHERE artifact_id=$1`, f.artifactID).Scan(&plan, &root); err != nil {
		t.Fatal(err)
	}
	owner := workItemFixtureDB(t)
	useRuntimePlanObserver(t, s)
	req := CommittedPlanObservationRequest{PlanID: plan, ArtifactID: f.artifactID, AcquisitionID: root, OneShot: &ref}
	for _, commitFirst := range []bool{false, true} {
		for round := 0; round < 50; round++ {
			if _, err := owner.Exec(`UPDATE one_shot_acquisitions SET admission_epoch=1 WHERE id=$1`, marker); err != nil {
				t.Fatal(err)
			}
			tx, err := owner.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`UPDATE one_shot_acquisitions SET admission_epoch=2 WHERE id=$1`, marker); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if commitFirst {
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			observed, err := s.ObserveCommittedPlan(context.Background(), req)
			if commitFirst {
				if observed.Status != "blocked" || !errors.Is(err, workitems.ErrOneShotReclaiming) {
					t.Fatalf("round=%d %+v %v", round, observed, err)
				}
			} else {
				if err != nil || observed.Status != "confirmed" {
					tx.Rollback()
					t.Fatalf("round=%d %+v %v", round, observed, err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			observed, err = s.ObserveCommittedPlan(context.Background(), req)
			if observed.Status != "blocked" || !errors.Is(err, workitems.ErrOneShotReclaiming) {
				t.Fatalf("post-commit round=%d %+v %v", round, observed, err)
			}
		}
	}
}
