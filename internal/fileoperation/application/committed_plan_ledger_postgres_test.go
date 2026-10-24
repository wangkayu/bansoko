package application

import (
	"bansoko/internal/workitems"
	"context"
	"errors"
	"testing"
)

func TestCommittedPlanMalformedLedgerBlocksWithoutConsumption(t *testing.T) {
	db, f := openFileOperationDB(t)
	s := newFixtureService(t, db, f)
	seedOneShotMarkerFixture(t, db, "ledger-marker")
	ref := workitems.OneShotRef{MarkerID: "ledger-marker", CorrelationID: "corr-ledger-marker", AdmissionEpoch: 1}
	if err := prepareOneShotFixture(t, db, s, f.artifactID, ref); err != nil {
		t.Fatal(err)
	}
	var plan, root string
	if err := db.QueryRow(`SELECT id,acquisition_id FROM postprocessing_plans WHERE artifact_id=$1`, f.artifactID).Scan(&plan, &root); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE one_shot_acquisitions SET active_effect_leases='[{}]',active_effect_count=1 WHERE id=$1`, ref.MarkerID); err != nil {
		t.Fatal(err)
	}
	before := planGraphSnapshot(t, db)
	useRuntimePlanObserver(t, s)
	out, err := s.ObserveCommittedPlan(context.Background(), CommittedPlanObservationRequest{PlanID: plan, ArtifactID: f.artifactID, AcquisitionID: root, OneShot: &ref})
	if out.Status != "blocked" || out.ReasonCode != "PLAN_RECOVERY_GRAPH_INCONSISTENT" || !errors.Is(err, workitems.ErrInvalidOneShotLedger) {
		t.Fatalf("%+v %v", out, err)
	}
	if planGraphSnapshot(t, db) != before {
		t.Fatal("ledger consumed or repaired")
	}
}
