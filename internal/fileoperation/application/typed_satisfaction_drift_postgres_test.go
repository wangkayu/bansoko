package application

import (
	"context"
	"testing"
)

func TestTypedObservedPreparedArtifactDriftRejectsCandidate(t *testing.T) {
	db, s, c, b, _ := preparedObservedBatch(t)
	ctx := context.Background()
	if _, err := ownerTestDB(t).Exec(`UPDATE artifacts SET version=version+1 WHERE id=$1`, b.capture.ArtifactID); err != nil {
		t.Fatal(err)
	}
	before := planGraphSnapshot(t, db)
	if _, err := s.commitTypedPlan(ctx, c, b); err == nil {
		t.Fatal("fixed Artifact qualification drift committed")
	}
	if planGraphSnapshot(t, db) != before {
		t.Fatal("fixed qualification drift partially wrote")
	}
	var items, results int
	if err := db.QueryRow(`SELECT count(*) FROM acquisition_files f JOIN postprocessing_plans p ON p.acquisition_id=f.acquisition_id WHERE p.id=$1 AND f.plan_kind IS NOT NULL`, c.Owner.ID).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM acquisition_files WHERE outcome IS NOT NULL`).Scan(&results); err != nil {
		t.Fatal(err)
	}
	if items != 0 || results != 0 {
		t.Fatal("stale observation published")
	}
}
