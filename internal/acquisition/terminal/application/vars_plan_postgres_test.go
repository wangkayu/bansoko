package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	fileapp "bansoko/internal/fileoperation/application"
	filesystem "bansoko/internal/library/adapter/filesystem"
	"bansoko/internal/library/domain"
	"bansoko/internal/testutil/mediafactsfixture"
	"bansoko/internal/workitems"
)

type forbiddenVarsFileExecutor struct{ t *testing.T }

func (e forbiddenVarsFileExecutor) PreparePublication(context.Context, domain.FileOperation, string) (domain.PublicationPreparation, error) {
	e.t.Fatal("prepare prepared a file effect")
	return domain.PublicationPreparation{}, nil
}

// The submit stage is the actual runtime producer. Only the already committed planning graph is
// arranged by the owner; the real runtime prepare stage consumes that same root record.
func checkAcceptedVarsPrepare(t *testing.T, ctx context.Context, owner, runtime *sql.DB, root, recordBefore string) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "ep01.mkv")
	content := make([]byte, 1000)
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	fingerprint, _ := json.Marshal(map[string]any{"algorithm": "sha256", "value": hex.EncodeToString(sum[:]), "size_bytes": 1000})
	var placement, rootDir string
	if err := owner.QueryRowContext(ctx, `SELECT input->'placement'->>'mode',COALESCE(input->'delivery'->'route'->'root'->>'value','') FROM acquisitions WHERE id=$1`, root).Scan(&placement, &rootDir); err != nil {
		t.Fatal(err)
	}
	if placement == "observe_for_instance" {
		var title string
		if err := owner.QueryRowContext(ctx, `SELECT CASE WHEN input->'media'->'title_override'->>'state'='known' THEN input->'media'->'title_override'->>'value' ELSE input->'media'->'title'->>'value' END FROM acquisitions WHERE id=$1`, root).Scan(&title); err != nil {
			t.Fatal(err)
		}
		// The handoff release title carries episode 01: a template needing the
		// episode never renders an empty one (E00) for the existing file.
		target := filepath.Join(rootDir, title, "Season 01", title+" - E01@G.mkv")
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var task string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM download_tasks WHERE acquisition_id=$1`, root).Scan(&task); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE download_tasks SET state='completed' WHERE id=$1`, task); err != nil {
		t.Fatal(err)
	}
	artifact, plan := "vars-artifact-"+root, "vars-plan-"+root
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts(id,download_task_id,acquisition_id,inventory_snapshot,fingerprint_snapshot,state,verified_at) VALUES($1,$2,$3,'{}','{}','verified',CURRENT_TIMESTAMP)`, artifact, task, root); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifact_files(id,artifact_id,acquisition_id,file_ref,relative_path,source_path,media_kind,size_bytes,fingerprint,subtitle_binding_state) SELECT 'vars-file-'||$1,$2,$1,file_ref,relative_path,$3,media_kind,exact_size,$4::jsonb,'not_applicable' FROM acquisition_files WHERE acquisition_id=$1`, root, artifact, source, fingerprint); err != nil {
		t.Fatal(err)
	}
	if placement == "observe_for_instance" {
		// The handoff Release is seeded by SQL, so the test freezes what its
		// admission and inspection write: the media fact snapshot and one
		// row per video, recognised as the title's episode 01.
		var releaseID, releaseTitle string
		if err := tx.QueryRowContext(ctx, `SELECT r.id, r.raw_facts->>'title' FROM download_tasks t JOIN releases r ON r.id=t.release_id WHERE t.id=$1`, task).Scan(&releaseID, &releaseTitle); err != nil {
			t.Fatal(err)
		}
		rows, err := tx.QueryContext(ctx, `SELECT file_ref, exact_size, relative_path FROM acquisition_files WHERE acquisition_id=$1 AND media_kind='video' ORDER BY ordinal`, root)
		if err != nil {
			t.Fatal(err)
		}
		var videos []mediafactsfixture.Video
		for rows.Next() {
			var video mediafactsfixture.Video
			if err := rows.Scan(&video.FileRef, &video.Size, &video.Path); err != nil {
				t.Fatal(err)
			}
			video.Name = releaseTitle + ".mkv"
			videos = append(videos, video)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		mediafactsfixture.Freeze(t, tx, releaseID, releaseTitle, videos...)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO postprocessing_plans(id,artifact_id,acquisition_id,artifact_version,input_digest,state) SELECT $1,$2,id,1,admission_digest,'queued' FROM acquisitions WHERE id=$3`, plan, artifact, root); err != nil {
		t.Fatal(err)
	}
	store := &workitems.Store{DB: runtime, RetentionDays: workitems.DefaultRetentionDays}
	if _, err := store.Create(ctx, tx, workitems.WorkItemSpec{Owner: workitems.OwnerRef{Type: workitems.OwnerPostProcessingPlan, ID: plan}, Kind: workitems.KindPostProcessPlan, IdempotencyKey: "plan:" + plan, InputSnapshot: []byte(`{}`), MaxAttempts: 3, ExpectedOwnerVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	service, err := fileapp.NewService(runtime, forbiddenVarsFileExecutor{t}, filesystem.OSFileObserver{})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimDue(ctx, "vars-t05", []string{workitems.KindPostProcessPlan}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PostProcessingPlanHandler().Handle(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if placement == "observe_for_instance" {
		var items string
		if err := owner.QueryRowContext(ctx, `SELECT jsonb_agg(to_jsonb(i))::text FROM acquisition_files i JOIN postprocessing_plans p ON p.acquisition_id=i.acquisition_id WHERE p.id=$1`, plan).Scan(&items); err != nil {
			t.Fatal(err)
		}
		t.Logf("handoff Plan items: %s", items)
		var paths string
		if err := owner.QueryRowContext(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('target',target_path,'source',source_path))::text,'[]') FROM file_operations WHERE postprocessing_plan_id=$1`, plan).Scan(&paths); err != nil {
			t.Fatal(err)
		}
		t.Logf("handoff target paths: %s", paths)
		var kind string
		if err := owner.QueryRowContext(ctx, `SELECT i.plan_kind FROM acquisition_files i JOIN postprocessing_plans p ON p.acquisition_id=i.acquisition_id WHERE p.id=$1`, plan).Scan(&kind); err != nil || kind != "adopt" {
			t.Fatalf("historical Vars scenario expected adopt, got %s: %v", kind, err)
		}
	}
	var state, after string
	if err := owner.QueryRowContext(ctx, `SELECT p.state,e.vars_evaluation_canonical_utf8 FROM postprocessing_plans p JOIN acquisitions e ON e.id=p.acquisition_id WHERE p.id=$1`, plan).Scan(&state, &after); err != nil || state != "succeeded" || after != recordBefore {
		t.Fatalf("prepare original evidence %s %v", state, err)
	}
	out, err := service.ObserveCommittedPlan(ctx, fileapp.CommittedPlanObservationRequest{PlanID: plan, ArtifactID: artifact, AcquisitionID: root})
	if err != nil || out.Status != "confirmed" {
		t.Fatalf("T05 independent proof %+v %v", out, err)
	}
}
