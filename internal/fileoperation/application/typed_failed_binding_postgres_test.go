package application

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTypedMissingBindingPersistsWithoutOperation(t *testing.T) {
	db, f := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		p["fixture_manifest"] = []map[string]any{{"file_ref": "primary", "ordinal": 0, "relative_path": "source.mkv", "exact_size": 14, "media_kind": "video", "identity_evidence": map[string]any{}}, {"file_ref": "sub", "ordinal": 1, "relative_path": "unrelated.ass", "exact_size": 8, "media_kind": "subtitle", "identity_evidence": map[string]any{}}}
		p["effective_sections"].(map[string]any)["file_rules"] = map[string]any{"must_contain": `[.]ass$`, "not_in": ""}
	})
	path := filepath.Join(t.TempDir(), "unrelated.ass")
	if err := os.WriteFile(path, []byte("subtitle"), 0600); err != nil {
		t.Fatal(err)
	}
	addArtifactFile(t, db, f, "missing-sub", "sub", "unrelated.ass", path, "subtitle", 8)
	s := newFixtureService(t, db, f)
	prepareFixture(t, db, s, f.artifactID)
	var state, version string
	if err := db.QueryRow(`SELECT subtitle_binding_state,subtitle_binding_snapshot->>'version' FROM artifact_files WHERE id='missing-sub'`).Scan(&state, &version); err != nil {
		t.Fatal(err)
	}
	if state != "missing" || version != bindingSnapshotVersion {
		t.Fatalf("missing binding %s/%s", state, version)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM file_operations WHERE artifact_id=$1`, f.artifactID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed binding Operations=%d", count)
	}
	var proof []byte
	if err := db.QueryRow(`SELECT postprocessing_plan_commit_state(p.id,w.id,w.fencing_token) FROM postprocessing_plans p JOIN work_item_history w ON w.owner_type='postprocessing_plan' AND w.owner_id=p.id AND w.kind='postprocess_plan' WHERE p.artifact_id=$1`, f.artifactID).Scan(&proof); err != nil || len(proof) == 0 {
		t.Fatalf("proof %s %v", proof, err)
	}
}
