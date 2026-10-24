package application

import (
	"context"
	"testing"
	"time"

	"bansoko/internal/workitems"
)

// Exercise the actual runtime recovery claim against sealed one-shot observation.
// Owner fault injection expires a child lease; it never supplies the claim result.
func TestCommittedPlanObservationRecoveryClaimOrders(t *testing.T) {
	db, f := openFileOperationDB(t)
	s := newFixtureService(t, db, f)
	seedOneShotMarkerFixture(t, db, "recovery-competition-marker")
	ref := workitems.OneShotRef{MarkerID: "recovery-competition-marker", CorrelationID: "corr-recovery-competition-marker", AdmissionEpoch: 1}
	if err := prepareOneShotFixture(t, db, s, f.artifactID, ref); err != nil {
		t.Fatal(err)
	}
	var plan, root string
	if err := db.QueryRow(`SELECT id,acquisition_id FROM postprocessing_plans WHERE artifact_id=$1`, f.artifactID).Scan(&plan, &root); err != nil {
		t.Fatal(err)
	}
	useRuntimePlanObserver(t, s)
	store, err := workitems.NewStore(s.db, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := CommittedPlanObservationRequest{PlanID: plan, ArtifactID: f.artifactID, AcquisitionID: root, OneShot: &ref}
	owner := workItemFixtureDB(t)
	for _, claimFirst := range []bool{true, false} {
		for round := 0; round < 50; round++ {
			if _, err := owner.Exec(`UPDATE work_items SET state='leased',lease_until=statement_timestamp()-interval '1 second',recovery_blocked=true WHERE owner_type='file_operation' AND owner_id IN (SELECT id FROM file_operations WHERE artifact_id=$1)`, f.artifactID); err != nil {
				t.Fatal(err)
			}
			claimed := make(chan error, 1)
			start := make(chan struct{})
			go func() {
				<-start
				c, e := store.ClaimRecovery(context.Background(), []string{workitems.KindFileExecute}, time.Minute)
				if e == nil && c.Owner.Type != "file_operation" {
					e = workitems.ErrInvalidSpec
				}
				claimed <- e
			}()
			if claimFirst {
				close(start)
			}
			observed, e := s.ObserveCommittedPlan(context.Background(), req)
			if e != nil || observed.Status != "confirmed" {
				t.Fatalf("order=%v round=%d observation=%+v err=%v", claimFirst, round, observed, e)
			}
			if !claimFirst {
				close(start)
			}
			if e := <-claimed; e != nil {
				t.Fatalf("order=%v round=%d runtime ClaimRecovery: %v", claimFirst, round, e)
			}
			observed, e = s.ObserveCommittedPlan(context.Background(), req)
			if e != nil || observed.Status != "confirmed" {
				t.Fatalf("post-claim order=%v round=%d observation=%+v err=%v", claimFirst, round, observed, e)
			}
		}
	}
}
