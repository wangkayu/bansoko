package application

import (
	"bansoko/internal/workitems"
	"context"
	"testing"
)

func TestCommittedPlanObservationChildCorruption(t *testing.T) {
	for _, tc := range []struct{ name, mutation, detail string }{
		{"missing_marker", `input_snapshot=input_snapshot-'one_shot'`, "CHILD_ONE_SHOT_MISSING"},
		{"wrong_file", `input_snapshot=jsonb_set(input_snapshot,'{artifact_file_id}','"wrong-file"')`, "CHILD_WORK_SNAPSHOT_INVALID"},
		{"compound", `input_snapshot=jsonb_set(input_snapshot-'one_shot','{artifact_file_id}','"wrong-file"')`, "CHILD_WORK_SNAPSHOT_INVALID"},
		{"malformed", `input_snapshot=jsonb_set(input_snapshot,'{one_shot}','{"marker_id":"obs-marker"}')`, "CHILD_ONE_SHOT_INVALID"},
		{"conflict", `input_snapshot=jsonb_set(input_snapshot,'{one_shot,correlation_id}','"wrong-correlation"')`, "CHILD_ONE_SHOT_MISMATCH"},
		{"extra_marker_key", `input_snapshot=jsonb_set(input_snapshot,'{one_shot,extra}','true')`, "CHILD_ONE_SHOT_INVALID"},
		{"fractional_epoch", `input_snapshot=jsonb_set(input_snapshot,'{one_shot,admission_epoch}','1.5')`, "CHILD_ONE_SHOT_INVALID"},
		{"empty_correlation", `input_snapshot=jsonb_set(input_snapshot,'{one_shot,correlation_id}','""')`, "CHILD_ONE_SHOT_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, f := openFileOperationDB(t)
			s := newFixtureService(t, db, f)
			seedOneShotMarkerFixture(t, db, "obs-marker")
			ref := workitems.OneShotRef{MarkerID: "obs-marker", CorrelationID: "corr-obs-marker", AdmissionEpoch: 1}
			if err := prepareOneShotFixture(t, db, s, f.artifactID, ref); err != nil {
				t.Fatal(err)
			}
			var plan, root string
			if err := db.QueryRow(`SELECT id,acquisition_id FROM postprocessing_plans WHERE artifact_id=$1`, f.artifactID).Scan(&plan, &root); err != nil {
				t.Fatal(err)
			}
			req := CommittedPlanObservationRequest{PlanID: plan, ArtifactID: f.artifactID, AcquisitionID: root, OneShot: &ref}
			if _, err := workItemFixtureDB(t).Exec(`UPDATE work_items SET state='leased',lease_until=CURRENT_TIMESTAMP+interval '1 minute' WHERE owner_type='file_operation' AND owner_id IN (SELECT id FROM file_operations WHERE artifact_id=$1)`, f.artifactID); err != nil {
				t.Fatal(err)
			}
			useRuntimePlanObserver(t, s)
			before := planGraphSnapshot(t, db)
			out, err := s.ObserveCommittedPlan(context.Background(), req)
			if err != nil || out.Status != "confirmed" {
				t.Fatalf("normal %+v %v", out, err)
			}
			if planGraphSnapshot(t, db) != before {
				t.Fatal("normal reentry wrote graph")
			}
			// The child work may still be live or already closed.
			mutated := int64(0)
			for _, table := range []string{"work_items", "work_item_outcomes"} {
				res, err := workItemFixtureDB(t).Exec(`UPDATE `+table+` SET `+tc.mutation+` WHERE owner_type='file_operation' AND owner_id IN (SELECT id FROM file_operations WHERE artifact_id=$1)`, f.artifactID)
				if err != nil {
					t.Fatal(err)
				}
				n, _ := res.RowsAffected()
				mutated += n
			}
			if mutated == 0 {
				t.Fatal("no child work to corrupt")
			}
			before = planGraphSnapshot(t, db)
			for repeat := 0; repeat < 2; repeat++ {
				out, err = s.ObserveCommittedPlan(context.Background(), req)
				if err != nil || out.Status != "blocked" || out.ReasonCode != "PLAN_RECOVERY_GRAPH_INCONSISTENT" || out.DetailCode != tc.detail {
					t.Fatalf("damaged %+v %v", out, err)
				}
			}
			if planGraphSnapshot(t, db) != before {
				t.Fatal("blocked repaired graph")
			}
		})
	}
}
