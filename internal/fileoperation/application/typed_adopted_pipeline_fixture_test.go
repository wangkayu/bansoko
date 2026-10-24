package application

import (
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"os"
	"testing"
)

func assertTypedAdoptedPipelinePlanning(t *testing.T) {
	t.Helper()
	// Path-level verification: same path + same size is accepted
	// weak identity, so a same-size target with other bytes is adopted like a
	// matching one; only a size difference refuses the occupied target.
	t.Run("same_size_different_content", func(t *testing.T) {
		db, s, c, _, observed := preparedObservedBatch(t)
		if err := os.WriteFile(observed, make([]byte, 14), 0600); err != nil {
			t.Fatal(err)
		}
		p, err := s.loadPlan(context.Background(), c.Owner.ID)
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := s.captureArtifactBatch(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.buildArtifactBatch(context.Background(), fresh, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := s.commitTypedPlan(context.Background(), c, fresh); err != nil {
			t.Fatal(err)
		}
		var kind string
		var code sql.NullString
		var operations int
		if err := db.QueryRow(`SELECT plan_kind,plan_reason_code,(SELECT count(*) FROM file_operations) FROM acquisition_files f JOIN postprocessing_plans p ON p.acquisition_id=f.acquisition_id WHERE p.id=$1`, c.Owner.ID).Scan(&kind, &code, &operations); err != nil {
			t.Fatal(err)
		}
		if kind != "adopt" || code.Valid || operations != 0 {
			t.Fatalf("same-size target not adopted: %s/%v operations=%d", kind, code, operations)
		}
		assertCommittedReentry(t, s, "artifact-file-test", nil, "confirmed", "")
	})
	t.Run("different_size", func(t *testing.T) {
		db, s, c, _, observed := preparedObservedBatch(t)
		if err := os.WriteFile(observed, make([]byte, 13), 0600); err != nil {
			t.Fatal(err)
		}
		p, err := s.loadPlan(context.Background(), c.Owner.ID)
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := s.captureArtifactBatch(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.buildArtifactBatch(context.Background(), fresh, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := s.commitTypedPlan(context.Background(), c, fresh); err != nil {
			t.Fatal(err)
		}
		var kind, code string
		if err := db.QueryRow(`SELECT plan_kind,plan_reason_code FROM acquisition_files f JOIN postprocessing_plans p ON p.acquisition_id=f.acquisition_id WHERE p.id=$1`, c.Owner.ID).Scan(&kind, &code); err != nil {
			t.Fatal(err)
		}
		if kind != "failed" || code != "TARGET_OCCUPIED" {
			t.Fatalf("different-size target adopted: %s/%s", kind, code)
		}
		assertCommittedReentry(t, s, "artifact-file-test", nil, "confirmed", "")
	})
	t.Run("real_matching_content", func(t *testing.T) {
		db, s, c, b, _ := preparedObservedBatch(t)
		if _, err := s.commitTypedPlan(context.Background(), c, b); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM file_operations`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("adoption fabricated operation")
		}
		before := planGraphSnapshot(t, db)
		out, err := s.Handle(context.Background(), workitems.Claim{Kind: workitems.KindFileExecute, Owner: workitems.OwnerRef{Type: workitems.OwnerFileOperation, ID: "unadmitted"}})
		if err == nil && out.WriteOwner != nil {
			t.Fatal("unadmitted effect")
		}
		if planGraphSnapshot(t, db) != before {
			t.Fatal("unadmitted effect changed graph")
		}
		assertCommittedReentry(t, s, "artifact-file-test", nil, "confirmed", "")
	})
}
