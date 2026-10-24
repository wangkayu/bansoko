package application

import (
	"bansoko/internal/workitems"
	"context"
	"errors"
	"testing"
)

func TestCommittedPlanContextAndMarkerGates(t *testing.T) {
	for _, tc := range []string{"ordinary", "other", "missing", "reclaiming", "cleanup_blocked", "epoch"} {
		t.Run(tc, func(t *testing.T) {
			db, f := openFileOperationDB(t)
			s := newFixtureService(t, db, f)
			seedOneShotMarkerFixture(t, db, "context-marker")
			ref := workitems.OneShotRef{MarkerID: "context-marker", CorrelationID: "corr-context-marker", AdmissionEpoch: 1}
			if err := prepareOneShotFixture(t, db, s, f.artifactID, ref); err != nil {
				t.Fatal(err)
			}
			var plan, root string
			db.QueryRow(`SELECT id,acquisition_id FROM postprocessing_plans WHERE artifact_id=$1`, f.artifactID).Scan(&plan, &root)
			req := CommittedPlanObservationRequest{PlanID: plan, ArtifactID: f.artifactID, AcquisitionID: root, OneShot: &ref}
			want := workitems.ErrOneShotReclaiming
			switch tc {
			case "ordinary":
				req.OneShot = nil
				want = workitems.ErrInvalidOneShotClaim
			case "other":
				other := ref
				other.MarkerID = "other-marker"
				req.OneShot = &other
				want = workitems.ErrInvalidSpec
			case "missing":
				if _, e := db.Exec(`DELETE FROM one_shot_acquisitions WHERE id='context-marker'`); e != nil {
					t.Fatal(e)
				}
				want = workitems.ErrOneShotMarkerMissing
			case "epoch":
				setOneShotMarkerEpochForPrepareFixture(t, db, ref.MarkerID, 2)
			default:
				setOneShotMarkerStateForPrepareFixture(t, db, ref.MarkerID, tc)
			}
			before := planGraphSnapshot(t, db)
			useRuntimePlanObserver(t, s)
			out, err := s.ObserveCommittedPlan(context.Background(), req)
			if out.Status != "blocked" || !errors.Is(err, want) {
				t.Fatalf("%+v %v want %v", out, err, want)
			}
			if planGraphSnapshot(t, db) != before {
				t.Fatal("gate changed graph")
			}
		})
	}
}
