package application

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTypedCommitMultipleVideosAndSubtitlesDependencyOrder(t *testing.T) {
	db, f := openFileOperationDBWithPolicy(t, func(p map[string]any) {
		p["fixture_manifest"] = []map[string]any{
			{"file_ref": "primary", "ordinal": 0, "relative_path": "source.mkv", "exact_size": 14, "media_kind": "video", "identity_evidence": map[string]any{}},
			{"file_ref": "z-video2", "ordinal": 1, "relative_path": "Show - 02.mkv", "exact_size": 5, "media_kind": "video", "episode": "2", "identity_evidence": map[string]any{}},
			{"file_ref": "a-sub1", "ordinal": 2, "relative_path": "source.ass", "exact_size": 8, "media_kind": "subtitle", "identity_evidence": map[string]any{}},
			{"file_ref": "b-sub2", "ordinal": 3, "relative_path": "Show - 02.ass", "exact_size": 8, "media_kind": "subtitle", "identity_evidence": map[string]any{}},
		}
	})
	dir := t.TempDir()
	for _, x := range []struct{ id, ref, rel, kind, content string }{{"video2", "z-video2", "Show - 02.mkv", "video", "video"}, {"sub1", "a-sub1", "source.ass", "subtitle", "subtitle"}, {"sub2", "b-sub2", "Show - 02.ass", "subtitle", "subtitle"}} {
		path := filepath.Join(dir, filepath.Base(x.rel))
		if err := os.WriteFile(path, []byte(x.content), 0600); err != nil {
			t.Fatal(err)
		}
		addArtifactFile(t, db, f, x.id, x.ref, x.rel, path, x.kind, int64(len(x.content)))
	}
	s := newFixtureService(t, db, f)
	prepareFixture(t, db, s, f.artifactID)
	ops := loadOperations(t, db, f.artifactID)
	if len(ops) != 4 {
		t.Fatalf("operations=%d", len(ops))
	}
	for _, pair := range [][2]string{{"sub1", f.fileID}, {"sub2", "video2"}} {
		sub, video := operationForFile(t, ops, pair[0]), operationForFile(t, ops, pair[1])
		if sub.boundVideoOperationID.String != video.id {
			t.Fatalf("wrong predecessor: %+v", sub)
		}
	}
	var planID string
	if err := db.QueryRow(`SELECT id FROM postprocessing_plans WHERE artifact_id=$1`, f.artifactID).Scan(&planID); err != nil {
		t.Fatal(err)
	}
	var invalid int
	if err := db.QueryRow(`SELECT count(*) FROM file_operations c JOIN file_operations p ON p.id=c.bound_video_operation_id WHERE c.postprocessing_plan_id=$1 AND c.file_ref=p.file_ref`, planID).Scan(&invalid); err != nil {
		t.Fatal(err)
	}
	if invalid != 0 {
		t.Fatal("invalid topology")
	}
	var works int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM work_item_history WHERE kind='file_execute'`).Scan(&works); err != nil {
		t.Fatal(err)
	}
	if works != 2 {
		t.Fatalf("initial video works=%d", works)
	}
	var workID string
	var token int64
	if err := db.QueryRow(`SELECT id,fencing_token FROM work_item_history WHERE owner_type='postprocessing_plan' AND owner_id=$1 AND kind='postprocess_plan'`, planID).Scan(&workID, &token); err != nil {
		t.Fatal(err)
	}
	var proof []byte
	if err := db.QueryRow(`SELECT postprocessing_plan_commit_state($1,$2,$3)`, planID, workID, token).Scan(&proof); err != nil || len(proof) == 0 {
		t.Fatalf("normal proof: %s %v", proof, err)
	}
	for _, mutation := range []string{
		`subtitle_binding_snapshot=NULL`,
		`subtitle_binding_snapshot=jsonb_set(subtitle_binding_snapshot,'{method}','"unique_video"')`,
		`subtitle_binding_snapshot=jsonb_set(subtitle_binding_snapshot,'{confidence}','0.6')`,
		`subtitle_binding_snapshot=jsonb_set(subtitle_binding_snapshot,'{locked,basename}','"wrong"')`,
	} {
		tx, err := ownerTestDB(t).BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE artifact_files SET ` + mutation + ` WHERE id='sub2'`); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.QueryRow(`SELECT postprocessing_plan_commit_state($1,$2,$3)`, planID, workID, token).Scan(&proof); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if len(proof) != 0 {
			tx.Rollback()
			t.Fatalf("proof accepted %s", mutation)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
	}
}
