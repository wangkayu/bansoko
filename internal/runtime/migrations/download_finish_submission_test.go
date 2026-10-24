package migrations

import (
	"bansoko/internal/runtime/database"
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestDownloadRuntimeFinishSubmission(t *testing.T) {
	fixtureDB := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, fixtureDB, "finish")
	p := validQueued(t, "finish", "finish-key", "finish")
	if _, err := callQueuedAccept(t, fixtureDB, p); err != nil {
		t.Fatal(err)
	}
	if _, err := fixtureDB.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=1,lease_until=CURRENT_TIMESTAMP+INTERVAL '1 hour' WHERE id=$1`, p.SubmitWorkID); err != nil {
		t.Fatal(err)
	}
	password := randomTestPassword(t)
	if err := database.ProvisionRuntimeRole(ctx, fixtureDB, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
		t.Fatal(err)
	}
	runtimeDB := openRuntimeConnection(t, password)
	defer runtimeDB.Close()
	var raw []byte
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_submit_attempt($1,$2,1,'finish-session')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_dispatch($1,$2,1,'finish-session','finish-permit',$3,'connection',$3,'{"key":"finish"}','{"root":"/downloads"}','{"hash":"a"}')`, p.TaskID, p.SubmitWorkID, acqDigest("a")).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_record_submission_receipt($1,'finish-permit','finish-session',$2,'confirmed','{"kind":"channel_closed"}','{"kind":"same_key_found"}','{"found":true}',NULL,$3,1,NULL)`, p.AcquisitionID, acqDigest("a"), p.SubmitWorkID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	rollbackTx, err := runtimeDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rollbackTx.QueryRowContext(ctx, `SELECT download_finish_submission($1,$2,1,'finish-session','finish-permit','downloading','{"external_id":"object-a"}',NULL,'finish-sync')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
		rollbackTx.Rollback()
		t.Fatal(err)
	}
	if err := rollbackTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var leaked int
	if err := runtimeDB.QueryRowContext(ctx, `SELECT count(*) FROM work_item_history WHERE id='finish-sync'`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatalf("rolled back sync leaked: %d", leaked)
	}
	for i := 0; i < 2; i++ {
		if err := runtimeDB.QueryRowContext(ctx, `SELECT download_finish_submission($1,$2,1,'finish-session','finish-permit','downloading','{"external_id":"object-a"}',NULL,'finish-sync')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var result struct {
			Applied bool `json:"applied"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		if result.Applied != (i == 0) {
			t.Fatalf("finish %d: %s", i, raw)
		}
	}
	var state, root, verdict string
	var count int
	if err := runtimeDB.QueryRowContext(ctx, `SELECT state FROM work_item_history WHERE id=$1`, p.SubmitWorkID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" {
		t.Fatal(state)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT count(*),min(acquisition_id) FROM work_item_history WHERE owner_id=$1 AND kind='qb_sync'`, p.TaskID).Scan(&count, &root); err != nil {
		t.Fatal(err)
	}
	if count != 1 || root != p.AcquisitionID {
		t.Fatalf("sync count=%d root=%s", count, root)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT verdict FROM releases WHERE id=$1`, p.SourceReleaseID).Scan(&verdict); err != nil {
		t.Fatal(err)
	}
	if verdict != "submitted" {
		t.Fatal(verdict)
	}
	if _, err := fixtureDB.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=1,lease_until=CURRENT_TIMESTAMP+INTERVAL '1 hour' WHERE id='finish-sync'`); err != nil {
		t.Fatal(err)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_sync($1,'finish-sync',1,1,'sync-session')`, p.TaskID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_finish_sync($1,'finish-sync',1,'sync-session',1,'downloading',NULL,'provider_unavailable','temporary',true,NULL,NULL,NULL,'retry-sync',NULL,NULL,NULL)`, p.TaskID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var consecutive, total int
	if err := runtimeDB.QueryRowContext(ctx, `SELECT consecutive_errors,total_errors FROM download_tasks WHERE id=$1`, p.TaskID).Scan(&consecutive, &total); err != nil {
		t.Fatal(err)
	}
	if consecutive != 1 || total != 1 {
		t.Fatalf("errors %d/%d", consecutive, total)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		var due bool
		if err := runtimeDB.QueryRowContext(ctx, `SELECT clock_timestamp()>=(active_poll_ticket->>'started_at')::timestamptz+poll_interval_seconds*INTERVAL '1 second' FROM download_tasks WHERE id=$1`, p.TaskID).Scan(&due); err != nil {
			t.Fatal(err)
		}
		if due {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("poll did not become due within test deadline")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := fixtureDB.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=1,lease_until=CURRENT_TIMESTAMP+INTERVAL '1 hour' WHERE id='retry-sync'`); err != nil {
		t.Fatal(err)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_sync($1,'retry-sync',1,2,'sync-session')`, p.TaskID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var grant struct {
		Granted bool `json:"granted"`
	}
	if err := json.Unmarshal(raw, &grant); err != nil {
		t.Fatal(err)
	}
	if !grant.Granted {
		t.Fatalf("second poll not granted: %s", raw)
	}
	// Existing one-shot continuation gates reject stale admission epochs.
	if _, err := fixtureDB.ExecContext(ctx, `INSERT INTO one_shot_acquisitions(id,managed_anime_id,subscription_id,rss_source_id,release_id,download_task_id,correlation_id,request_idempotency_key,request_hash,result_snapshot,state,admission_epoch) VALUES('finish-marker','anime-finish','subscription-finish','rss-finish',$1,$2,'finish-corr','finish-marker-key',$3,'{}','running',2)`, p.SourceReleaseID, p.TaskID, acqDigest("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := fixtureDB.ExecContext(ctx, `UPDATE work_items SET input_snapshot=input_snapshot||'{"one_shot":{"marker_id":"finish-marker","correlation_id":"finish-corr","admission_epoch":1}}'::jsonb WHERE id='retry-sync'`); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeDB.ExecContext(ctx, `SELECT download_finish_sync($1,'retry-sync',1,'sync-session',2,'completed','{"external_id":"object-a"}','qb_download_completed','',false,NULL,NULL,'uploading','inspect-finish','artifact-finish','{"files":[]}','{"files":[]}')`, p.TaskID); err == nil {
		t.Fatal("stale one-shot admission epoch created artifact/inspect")
	}
	if _, err := fixtureDB.ExecContext(ctx, `UPDATE work_items SET input_snapshot=jsonb_set(input_snapshot,'{one_shot,admission_epoch}','2') WHERE id='retry-sync'`); err != nil {
		t.Fatal(err)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_finish_sync($1,'retry-sync',1,'sync-session',2,'completed','{"external_id":"object-a"}','qb_download_completed','',false,NULL,NULL,'uploading','inspect-finish','artifact-finish','{"files":[]}','{"files":[]}')`, p.TaskID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var inheritedEpoch int
	if err := runtimeDB.QueryRowContext(ctx, `SELECT (input_snapshot->'one_shot'->>'admission_epoch')::integer FROM work_item_history WHERE id='inspect-finish'`).Scan(&inheritedEpoch); err != nil {
		t.Fatal(err)
	}
	if inheritedEpoch != 2 {
		t.Fatalf("inspect epoch=%d", inheritedEpoch)
	}
	var artifactRoot, artifactCause string
	if err := runtimeDB.QueryRowContext(ctx, `SELECT acquisition_id,causation_id FROM artifacts WHERE id='artifact-finish'`).Scan(&artifactRoot, &artifactCause); err != nil {
		t.Fatal(err)
	}
	if artifactRoot != p.AcquisitionID || artifactCause != p.TaskID {
		t.Fatalf("artifact root=%s cause=%s", artifactRoot, artifactCause)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT count(*) FROM work_item_history WHERE owner_type='artifact' AND owner_id='artifact-finish' AND kind='artifact_inspect'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("inspect count=%d", count)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_finish_sync($1,'finish-sync',1,'sync-session',1,'downloading',NULL,'provider_unavailable','temporary',true,NULL,NULL,NULL,'duplicate-old-next',NULL,NULL,NULL)`, p.TaskID).Scan(&raw); err != nil {
		t.Fatalf("cross-round legal receipt lost: %v", err)
	}
	if _, err := runtimeDB.ExecContext(ctx, `SELECT download_finish_sync($1,'finish-sync',1,'wrong-session',1,'downloading',NULL,NULL,NULL,false,NULL,NULL,'downloading','wrong-next',NULL,NULL,NULL)`, p.TaskID); sqlState(err) != "23514" {
		t.Fatalf("cross-round wrong session accepted: %v", err)
	}
}
