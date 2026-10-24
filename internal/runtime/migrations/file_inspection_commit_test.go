package migrations

import (
	"bansoko/internal/workitems"
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestFileInspectionAtomicCommit(t *testing.T) {
	owner, db, p := progressProbe(t, "inspect-commit", nil, nil)
	ctx := context.Background()
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET state='completed' WHERE id=$1`, p.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `INSERT INTO artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,state) VALUES('commit-artifact',$1,$2,'{}','{}','pending')`, p.TaskID, p.AcquisitionID); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRowContext(ctx, `SELECT public.work_item_create('commit-work','artifact','commit-artifact','artifact_inspect','commit-audit',3,'{}',1,0::double precision,7::smallint)`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	store := &workitems.Store{DB: db, RetentionDays: 7}
	c, err := store.ClaimDue(ctx, "commit-audit", []string{workitems.KindArtifactInspect}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var manifest []map[string]any
	if err := json.Unmarshal([]byte(p.ManifestFiles), &manifest); err != nil {
		t.Fatal(err)
	}
	files := make([]map[string]any, 0, len(manifest))
	for _, f := range manifest {
		files = append(files, map[string]any{"id": "commit-file-" + f["file_ref"].(string), "file_ref": f["file_ref"], "relative_path": f["relative_path"], "source_path": "/downloads/" + f["relative_path"].(string), "size_bytes": f["exact_size"], "media_kind": f["media_kind"], "fingerprint": map[string]any{"algorithm": "sha256", "value": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}, "facts": map[string]any{}})
	}
	body, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	inventory, fingerprints := inspectionSnapshotFixture(t, files)
	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT public.artifact_commit_inspection($1,$2,$3,$4,$5,$6::jsonb,$9::jsonb,$10::jsonb,$7::jsonb,'commit-plan','commit-plan-work',$8,'','[]')`, c.Owner.ID, c.ID, c.FencingToken, p.AdmissionDigest, p.ManifestDigest, p.ManifestFiles, body, p.AdmissionDigest, inventory, fingerprints).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var state, work string
	var plans, planWork int
	if err := db.QueryRowContext(ctx, `SELECT a.state,w.state,(SELECT count(*) FROM postprocessing_plans WHERE acquisition_id=a.acquisition_id),(SELECT count(*) FROM work_item_history WHERE owner_type='postprocessing_plan' AND owner_id='commit-plan') FROM artifacts a JOIN work_item_history w ON w.owner_type='artifact' AND w.owner_id=a.id WHERE w.id=$1`, c.ID).Scan(&state, &work, &plans, &planWork); err != nil {
		t.Fatal(err)
	}
	if state != "verified" || work != "succeeded" || plans != 1 || planWork != 1 {
		t.Fatalf("state=%s work=%s plans=%d planWork=%d", state, work, plans, planWork)
	}
	var planRoot string
	if err := db.QueryRowContext(ctx, `SELECT acquisition_id FROM work_item_history WHERE owner_type='postprocessing_plan' AND owner_id='commit-plan'`).Scan(&planRoot); err != nil {
		t.Fatal(err)
	}
	if planRoot != p.AcquisitionID {
		t.Fatalf("plan work root=%q want %q", planRoot, p.AcquisitionID)
	}
	planClaim, err := store.ClaimDue(ctx, "plan-claim", []string{workitems.KindPostProcessPlan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if planClaim.Owner.ID != "commit-plan" {
		t.Fatalf("plan claim=%+v", planClaim)
	}
}
