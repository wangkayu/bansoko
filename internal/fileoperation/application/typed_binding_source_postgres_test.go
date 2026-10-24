package application

import (
	"context"
	"os"
	"testing"
)

func TestTypedExistingMissingBindingSourceIsImmutableProof(t *testing.T) {
	db, f := openFileOperationDBWithPolicy(t, bindingManifestFixture(t, map[string]any{"file_ref": "subtitle", "relative_path": "source.chs.ass", "exact_size": 8, "media_kind": "subtitle"}))
	if err := os.WriteFile(f.sourcePath+".ass", []byte("subtitle"), 0600); err != nil {
		t.Fatal(err)
	}
	addArtifactFile(t, db, f, "source-missing-sub", "subtitle", "source.chs.ass", f.sourcePath+".ass", "subtitle", 8)
	if _, err := ownerTestDB(t).Exec(`UPDATE artifact_files SET subtitle_binding_state='missing',subtitle_binding_snapshot='{"version":"subtitle-binding-1","diagnostics":[{"code":"SOURCE_MISSING","message":"frozen source decision"}]}' WHERE id='source-missing-sub'`); err != nil {
		t.Fatal(err)
	}
	s := newFixtureService(t, db, f)
	prepareFixture(t, db, s, f.artifactID)
	assertCommittedReentry(t, s, f.artifactID, nil, "confirmed", "")
	if _, err := ownerTestDB(t).Exec(`UPDATE artifact_files SET subtitle_binding_snapshot=jsonb_set(subtitle_binding_snapshot,'{diagnostics}','[]') WHERE id='source-missing-sub'`); err != nil {
		t.Fatal(err)
	}
	before := planGraphSnapshot(t, db)
	var plan, root string
	if err := db.QueryRow(`SELECT id,acquisition_id FROM postprocessing_plans WHERE artifact_id=$1`, f.artifactID).Scan(&plan, &root); err != nil {
		t.Fatal(err)
	}
	useRuntimePlanObserver(t, s)
	out, err := s.ObserveCommittedPlan(context.Background(), CommittedPlanObservationRequest{PlanID: plan, ArtifactID: f.artifactID, AcquisitionID: root})
	if err != nil || out.Status != "blocked" || out.ReasonCode != "PLAN_RECOVERY_GRAPH_INCONSISTENT" {
		t.Fatalf("source tamper proof=%+v %v", out, err)
	}
	if planGraphSnapshot(t, db) != before {
		t.Fatal("source proof repaired snapshot")
	}
}
