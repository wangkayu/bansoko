package workitems

import (
	"context"
	"testing"
	"time"

	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/disposabledb"
)

func TestReclaimRetainsRunningAcquisitionWithoutSealedPlan(t *testing.T) {
	for _, queued := range []bool{false, true} {
		name := "no_plan"
		if queued {
			name = "queued_plan"
		}
		t.Run(name, func(t *testing.T) {
			db := openTestDB(t)
			const marker = "retained-running"
			createOneShotChain(t, db, marker, []string{"retained.mkv"})
			owner := disposabledb.Owner(t)
			if queued {
				if _, err := owner.Exec(`INSERT INTO postprocessing_plans(id,artifact_id,acquisition_id,artifact_version,state,input_digest) SELECT 'retained-queued','art-retained-running',id,1,'queued',admission_digest FROM acquisitions WHERE id='acq-retained-running'`); err != nil {
					t.Fatal(err)
				}
			}
			insertOneShotMarker(t, db, marker, OneShotStateRetentionPending, oneShotRetentionAt(time.Now().Add(-time.Hour)))
			before := acquisitionfixture.Snapshot(t, owner)
			service, err := NewOneShotRetentionService(db, nil)
			if err != nil {
				t.Fatal(err)
			}
			for n := 0; n < 2; n++ {
				got, err := service.reclaimOne(context.Background(), marker)
				if err != nil || got {
					t.Fatalf("reclaim=%v err=%v", got, err)
				}
				if before != acquisitionfixture.Snapshot(t, owner) {
					t.Fatal("reclaim changed retained running graph")
				}
			}
		})
	}
}
