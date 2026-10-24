package application

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTypedExactConfidenceProductionChain(t *testing.T) {
	db, f := openFileOperationDBWithPolicy(t, bindingManifestFixture(t, map[string]any{"file_ref": "subtitle", "relative_path": "source.ass", "exact_size": 8, "media_kind": "subtitle"}))
	path := filepath.Join(t.TempDir(), "source.ass")
	if err := os.WriteFile(path, []byte("subtitle"), 0600); err != nil {
		t.Fatal(err)
	}
	addArtifactFile(t, db, f, "exact-sub", "subtitle", "source.ass", path, "subtitle", 8)
	// Trusted owner source fixture before the prepare capture; confidence number is never converted to float.
	if _, err := db.Exec(`UPDATE artifact_files SET subtitle_binding_state='bound',bound_video_file_ref='primary',subtitle_binding_snapshot='{"version":"subtitle-binding-1","method":"basename_match","confidence":0.80000000000000001,"diagnostics":[]}' WHERE id='exact-sub'`); err != nil {
		t.Fatal(err)
	}
	s := newFixtureService(t, db, f)
	prepareFixture(t, db, s, f.artifactID)
	var source, operation, wire string
	if err := db.QueryRow(`SELECT f.subtitle_binding_snapshot->>'confidence',o.frozen_render_snapshot->'binding'->>'confidence',t.render_snapshot->'binding'->>'confidence' FROM artifact_files f JOIN file_operations o ON o.artifact_file_id=f.id JOIN acquisition_files t ON t.acquisition_id=f.acquisition_id AND t.file_ref=f.file_ref WHERE f.id='exact-sub'`).Scan(&source, &operation, &wire); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{source, operation, wire} {
		if value != "0.80000000000000001" {
			t.Fatalf("precision lost %s/%s/%s", source, operation, wire)
		}
	}
	assertCommittedReentry(t, s, f.artifactID, nil, "confirmed", "")
}
