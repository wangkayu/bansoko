package application

import (
	"bansoko/internal/workitems"
	"context"
	"testing"
)

// Both orders repeat against the same sealed graph; observer never locks or writes.
func TestCommittedPlanObservationChildLeaseOrders(t *testing.T) {
	db, f := openFileOperationDB(t)
	s := newFixtureService(t, db, f)
	seedOneShotMarkerFixture(t, db, "competition-marker")
	ref := workitems.OneShotRef{MarkerID: "competition-marker", CorrelationID: "corr-competition-marker", AdmissionEpoch: 1}
	if err := prepareOneShotFixture(t, db, s, f.artifactID, ref); err != nil {
		t.Fatal(err)
	}
	var plan, root string
	if err := db.QueryRow(`SELECT id,acquisition_id FROM postprocessing_plans WHERE artifact_id=$1`, f.artifactID).Scan(&plan, &root); err != nil {
		t.Fatal(err)
	}
	useRuntimePlanObserver(t, s)
	req := CommittedPlanObservationRequest{PlanID: plan, ArtifactID: f.artifactID, AcquisitionID: root, OneShot: &ref}
	owner := workItemFixtureDB(t)
	for _, first := range []bool{true, false} {
		for round := 0; round < 50; round++ {
			tx, err := owner.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(`UPDATE work_items SET state='leased',lease_until=CURRENT_TIMESTAMP+interval '1 minute',fencing_token=fencing_token+1 WHERE owner_type='file_operation' AND owner_id IN (SELECT id FROM file_operations WHERE artifact_id=$1)`, f.artifactID); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if first {
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			out, err := s.ObserveCommittedPlan(context.Background(), req)
			if err != nil || out.Status != "confirmed" {
				tx.Rollback()
				t.Fatalf("order=%v round=%d %+v %v", first, round, out, err)
			}
			if !first {
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
