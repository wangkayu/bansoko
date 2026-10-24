package application

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The one binding rule is the filename prefix; the retired episode and
// unique-video branches no longer exist, so the wire carries basename_match.
func TestTypedBindingProductionBranches(t *testing.T) {
	for _, tc := range []struct{ name, path, method, confidence string }{{"basename", "source.ass", "basename_match", "1"}, {"multi-part marker", "source.chs&cht.ass", "basename_match", "1"}} {
		t.Run(tc.name, func(t *testing.T) {
			db, f := openFileOperationDBWithPolicy(t, func(p map[string]any) {
				videoName := "source.mkv"
				p["fixture_manifest"] = []map[string]any{
					{"file_ref": "primary", "ordinal": 0, "relative_path": videoName, "exact_size": 14, "media_kind": "video", "identity_evidence": map[string]any{}},
					{"file_ref": "sub", "ordinal": 1, "relative_path": tc.path, "exact_size": 8, "media_kind": "subtitle", "identity_evidence": map[string]any{}},
				}
			})
			path := filepath.Join(t.TempDir(), tc.path)
			if err := os.WriteFile(path, []byte("subtitle"), 0600); err != nil {
				t.Fatal(err)
			}
			addArtifactFile(t, db, f, "branch-sub", "sub", tc.path, path, "subtitle", 8)
			s := newFixtureService(t, db, f)
			prepareFixture(t, db, s, f.artifactID)
			var method, number, wire, kind string
			if err := db.QueryRow(`SELECT f.subtitle_binding_snapshot->>'method',f.subtitle_binding_snapshot->>'confidence',t.render_snapshot->'binding'->>'confidence',jsonb_typeof(f.subtitle_binding_snapshot->'confidence') FROM artifact_files f JOIN acquisition_files t ON t.acquisition_id=f.acquisition_id AND t.file_ref=f.file_ref WHERE f.id='branch-sub'`).Scan(&method, &number, &wire, &kind); err != nil {
				t.Fatal(err)
			}
			if method != tc.method || number != tc.confidence || wire != tc.confidence || kind != "number" {
				t.Fatalf("binding=%s source=%s/%s wire=%s", method, number, kind, wire)
			}
			var mismatch int
			if err := db.QueryRow(`SELECT count(*) FROM file_operations o JOIN artifact_files f ON f.id=o.artifact_file_id WHERE f.id='branch-sub' AND (f.subtitle_binding_snapshot->>'method' IS DISTINCT FROM o.frozen_render_snapshot->'binding'->>'method' OR f.subtitle_binding_snapshot->'confidence' IS DISTINCT FROM o.frozen_render_snapshot->'binding'->'confidence' OR f.subtitle_binding_snapshot->'locked'->>'video_operation_id' IS DISTINCT FROM o.bound_video_operation_id)`).Scan(&mismatch); err != nil {
				t.Fatal(err)
			}
			if mismatch != 0 {
				t.Fatal("source/Operation mismatch")
			}
			var proof []byte
			if err := db.QueryRowContext(context.Background(), `SELECT postprocessing_plan_commit_state(p.id,w.id,w.fencing_token) FROM postprocessing_plans p JOIN work_item_history w ON w.owner_type='postprocessing_plan' AND w.owner_id=p.id AND w.kind='postprocess_plan' WHERE p.artifact_id=$1`, f.artifactID).Scan(&proof); err != nil || len(proof) == 0 {
				t.Fatalf("proof: %s %v", proof, err)
			}
		})
	}
}
