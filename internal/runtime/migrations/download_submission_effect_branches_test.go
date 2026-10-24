package migrations

import (
	"bansoko/internal/runtime/database"
	"context"
	"encoding/json"
	"testing"
)

func TestDownloadSubmissionEffectBranches(t *testing.T) {
	for _, effect := range []string{"unknown", "no_external_effect", "confirmed", "confirmed_failed"} {
		t.Run(effect, func(t *testing.T) {
			owner := installedDB(t)
			ctx := context.Background()
			seedAcceptableRelease(t, owner, "effects")
			p := validQueued(t, "effects", "effects-key", "effects")
			if _, err := callQueuedAccept(t, owner, p); err != nil {
				t.Fatal(err)
			}
			if _, err := owner.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=1,lease_until=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, p.SubmitWorkID); err != nil {
				t.Fatal(err)
			}
			password := randomTestPassword(t)
			if err := database.ProvisionRuntimeRole(ctx, owner, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
				t.Fatal(err)
			}
			runtimeDB := openRuntimeConnection(t, password)
			defer runtimeDB.Close()
			var raw []byte
			if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_submit_attempt($1,$2,1,'effect-session')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_dispatch($1,$2,1,'effect-session','effect-permit',$3,'connection',$3,'{"key":"effects"}','{"root":"/downloads"}','{"hash":"a"}')`, p.TaskID, p.SubmitWorkID, acqDigest("a")).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var closed, resolution any
			if effect == "confirmed_failed" {
				if err := runtimeDB.QueryRowContext(ctx, `SELECT download_record_submission_receipt($1,'effect-permit','effect-session',$2,'confirmed','{"kind":"channel_closed"}','{"kind":"same_key_found"}','{"found":true}',NULL,$3,1,NULL)`, p.AcquisitionID, acqDigest("a"), p.SubmitWorkID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT download_finish_submission($1,$2,1,'effect-session','effect-permit','failed','{"external_id":"object-a","state":"failed"}',NULL,'failed-next')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
					t.Fatalf("confirmed failed provider result cannot finish: %v", err)
				}
				var taskState, workState, effectClass, externalID, code string
				var nextCount int
				if err := runtimeDB.QueryRowContext(ctx, `SELECT t.state,w.state,t.submit_effect_class,t.provider_correlation->>'external_id',t.failure_code,(SELECT count(*) FROM work_item_history WHERE id='failed-next') FROM download_tasks t JOIN work_item_history w ON w.id=$2 WHERE t.id=$1`, p.TaskID, p.SubmitWorkID).Scan(&taskState, &workState, &effectClass, &externalID, &code, &nextCount); err != nil {
					t.Fatal(err)
				}
				if taskState != "failed" || workState != "failed" || effectClass != "confirmed" || externalID != "object-a" || code != "qb_provider_failed" || nextCount != 0 {
					t.Fatalf("failed task=%s work=%s effect=%s external=%s code=%s next=%d", taskState, workState, effectClass, externalID, code, nextCount)
				}
				var before, after string
				const snapshotSQL = `SELECT jsonb_build_array(to_jsonb(t),to_jsonb(w),a.facts_revision)::text FROM download_tasks t JOIN work_item_history w ON w.id=$2 JOIN acquisitions a ON a.id=t.acquisition_id WHERE t.id=$1`
				if err := runtimeDB.QueryRowContext(ctx, snapshotSQL, p.TaskID, p.SubmitWorkID).Scan(&before); err != nil {
					t.Fatal(err)
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT download_finish_submission($1,$2,1,'effect-session','effect-permit','failed','{"external_id":"object-a","state":"failed"}',NULL,'failed-next')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
					t.Fatalf("duplicate confirmed failure receipt rejected: %v", err)
				}
				if err := runtimeDB.QueryRowContext(ctx, snapshotSQL, p.TaskID, p.SubmitWorkID).Scan(&after); err != nil {
					t.Fatal(err)
				}
				if before != after {
					t.Fatal("duplicate failed receipt changed persistent facts")
				}
				var duplicate struct {
					Applied bool `json:"applied"`
				}
				if err := json.Unmarshal(raw, &duplicate); err != nil {
					t.Fatal(err)
				}
				if duplicate.Applied {
					t.Fatalf("duplicate failed receipt reapplied: %s", raw)
				}
				var terminalVersion int64
				var lateWork string
				if err := runtimeDB.QueryRowContext(ctx, `SELECT version FROM download_tasks WHERE id=$1`, p.TaskID).Scan(&terminalVersion); err != nil {
					t.Fatal(err)
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT work_item_create('failed-terminal-sync','download_task',$1,'qb_sync',$2,5,'{"poll_ordinal":1}',$3,0::double precision,7::smallint)`, p.TaskID, p.TaskIdempotency, terminalVersion).Scan(&lateWork); err != nil {
					t.Fatal(err)
				}
				if _, err := owner.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=1,lease_until=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, lateWork); err != nil {
					t.Fatal(err)
				}
				if _, err := owner.ExecContext(ctx, `UPDATE work_items SET input_snapshot=input_snapshot-'poll_ordinal' WHERE id=$1`, lateWork); err != nil {
					t.Fatal(err)
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_sync($1,$2,1,1,'failed-terminal-session')`, p.TaskID, lateWork).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var lateState string
				if err := runtimeDB.QueryRowContext(ctx, `SELECT state FROM work_item_history WHERE id=$1`, lateWork).Scan(&lateState); err != nil {
					t.Fatal(err)
				}
				if lateState != "failed" {
					t.Fatalf("failed task late work=%s", lateState)
				}
				var lateSubmit string
				if err := runtimeDB.QueryRowContext(ctx, `SELECT work_item_create('failed-terminal-submit','download_task',$1,'qb_submit',$2,5,'{}',$3,0::double precision,7::smallint)`, p.TaskID, p.TaskIdempotency, terminalVersion).Scan(&lateSubmit); err != nil {
					t.Fatal(err)
				}
				if _, err := owner.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=1,lease_until=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, lateSubmit); err != nil {
					t.Fatal(err)
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_submit_attempt($1,$2,1,'failed-terminal-submit-session')`, p.TaskID, lateSubmit).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var submitGrant struct {
					Granted bool `json:"granted"`
				}
				if err := json.Unmarshal(raw, &submitGrant); err != nil {
					t.Fatal(err)
				}
				if submitGrant.Granted {
					t.Fatal("failed task granted a new submission attempt")
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT state FROM work_item_history WHERE id=$1`, lateSubmit).Scan(&lateState); err != nil {
					t.Fatal(err)
				}
				if lateState != "failed" {
					t.Fatalf("failed task late submit work=%s result=%s", lateState, raw)
				}
				var lateDispatch string
				if err := runtimeDB.QueryRowContext(ctx, `SELECT work_item_create('failed-terminal-dispatch','download_task',$1,'qb_submit',$2,5,'{}',$3,0::double precision,7::smallint)`, p.TaskID, p.TaskIdempotency, terminalVersion).Scan(&lateDispatch); err != nil {
					t.Fatal(err)
				}
				if _, err := owner.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=1,lease_until=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, lateDispatch); err != nil {
					t.Fatal(err)
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_dispatch($1,$2,1,'late-dispatch-session','late-dispatch-permit',$3,'connection',$3,'{}','{}','{}')`, p.TaskID, lateDispatch, acqDigest("a")).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var dispatchGrant struct {
					Granted bool `json:"granted"`
				}
				if err := json.Unmarshal(raw, &dispatchGrant); err != nil {
					t.Fatal(err)
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT state FROM work_item_history WHERE id=$1`, lateDispatch).Scan(&lateState); err != nil {
					t.Fatal(err)
				}
				if dispatchGrant.Granted || lateState != "failed" {
					t.Fatalf("terminal dispatch work=%s result=%s", lateState, raw)
				}
				return
			}
			if effect == "confirmed" {
				closed = `{"kind":"channel_closed"}`
				resolution = `{"kind":"same_key_found"}`
			}
			if effect == "no_external_effect" {
				closed = `{"kind":"channel_closed"}`
				resolution = `{"kind":"no_effect"}`
			}
			if effect == "unknown" {
				if _, err := owner.ExecContext(ctx, `UPDATE work_items SET lease_until=clock_timestamp()-INTERVAL '1 second' WHERE id=$1`, p.SubmitWorkID); err != nil {
					t.Fatal(err)
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT download_record_submission_receipt($1,'effect-permit','effect-session',$2,'unknown',NULL,NULL,'{"found":false}',NULL,$3,1,NULL)`, p.AcquisitionID, acqDigest("a"), p.SubmitWorkID).Scan(&raw); err != nil {
					t.Fatalf("late original dispatch receipt lost passive eligibility: %v", err)
				}
				if _, err := owner.ExecContext(ctx, `UPDATE work_items SET lease_until=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, p.SubmitWorkID); err != nil {
					t.Fatal(err)
				}
			}
			if err := runtimeDB.QueryRowContext(ctx, `SELECT download_record_submission_receipt($1,'effect-permit','effect-session',$2,$3,$4::jsonb,$5::jsonb,'{"found":false}',NULL,$6,1,NULL)`, p.AcquisitionID, acqDigest("a"), effect, closed, resolution, p.SubmitWorkID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if effect == "confirmed" {
				if err := runtimeDB.QueryRowContext(ctx, `SELECT download_record_submission_receipt($1,'effect-permit','effect-session',$2,'confirmed','{"kind":"channel_closed"}','{"kind":"same_key_found"}','{"found":true}',NULL,$3,1,NULL)`, p.AcquisitionID, acqDigest("a"), p.SubmitWorkID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT download_finish_submission($1,$2,1,'effect-session','effect-permit','completed','{"external_id":"object-a","files_complete":true,"files":[]}',NULL,'completed-inspect')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
					t.Fatalf("completed submission cannot create first inspect: %v", err)
				}
				var artifactCount, inspectCount, syncCount int
				var state, root, cause, verdict string
				if err := runtimeDB.QueryRowContext(ctx, `SELECT state FROM download_tasks WHERE id=$1`, p.TaskID).Scan(&state); err != nil {
					t.Fatal(err)
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT acquisition_id,causation_id FROM artifacts WHERE download_task_id=$1`, p.TaskID).Scan(&root, &cause); err != nil {
					t.Fatal(err)
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT verdict FROM releases WHERE id=$1`, p.SourceReleaseID).Scan(&verdict); err != nil {
					t.Fatal(err)
				}
				if state != "completed" || root != p.AcquisitionID || cause != p.TaskID || verdict != "submitted" {
					t.Fatalf("completion state=%s root=%s cause=%s verdict=%s", state, root, cause, verdict)
				}
				if _, err := runtimeDB.ExecContext(ctx, `SELECT download_finish_submission($1,$2,1,'wrong-session','wrong-permit','completed','{"external_id":"object-a"}',NULL,'wrong-inspect')`, p.TaskID, p.SubmitWorkID); sqlState(err) != "23514" {
					t.Fatalf("completed receipt accepted wrong session/permit: %v", err)
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT download_finish_submission($1,$2,1,'effect-session','effect-permit','completed','{"external_id":"object-a"}',NULL,'duplicate-inspect')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM artifacts WHERE download_task_id=$1),(SELECT count(*) FROM work_item_history WHERE owner_type='artifact' AND owner_id IN (SELECT id FROM artifacts WHERE download_task_id=$1) AND kind='artifact_inspect'),(SELECT count(*) FROM work_item_history WHERE owner_id=$1 AND kind='qb_sync')`, p.TaskID).Scan(&artifactCount, &inspectCount, &syncCount); err != nil {
					t.Fatal(err)
				}
				if artifactCount != 1 || inspectCount != 1 || syncCount != 0 {
					t.Fatalf("artifact=%d inspect=%d sync=%d", artifactCount, inspectCount, syncCount)
				}
				var terminalVersion int64
				if err := runtimeDB.QueryRowContext(ctx, `SELECT version FROM download_tasks WHERE id=$1`, p.TaskID).Scan(&terminalVersion); err != nil {
					t.Fatal(err)
				}
				var lateWork string
				if err := runtimeDB.QueryRowContext(ctx, `SELECT work_item_create('terminal-sync','download_task',$1,'qb_sync',$2,5,'{"poll_ordinal":1}',$3,0::double precision,7::smallint)`, p.TaskID, p.TaskIdempotency, terminalVersion).Scan(&lateWork); err != nil {
					t.Fatal(err)
				}
				if _, err := owner.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=1,lease_until=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, lateWork); err != nil {
					t.Fatal(err)
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_sync($1,$2,1,1,'terminal-session')`, p.TaskID, lateWork).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var terminalGrant struct {
					Granted bool `json:"granted"`
				}
				if err := json.Unmarshal(raw, &terminalGrant); err != nil {
					t.Fatal(err)
				}
				if terminalGrant.Granted {
					t.Fatal("completed task granted a new sync observation")
				}
				var lateState string
				if err := runtimeDB.QueryRowContext(ctx, `SELECT state FROM work_item_history WHERE id=$1`, lateWork).Scan(&lateState); err != nil {
					t.Fatal(err)
				}
				if lateState != "succeeded" {
					t.Fatalf("completed task late work=%s", lateState)
				}
				return
			}
			if err := runtimeDB.QueryRowContext(ctx, `SELECT download_finish_submission($1,$2,1,'effect-session','effect-permit',NULL,NULL,NULL,NULL)`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var result struct {
				Applied bool   `json:"applied"`
				Reason  string `json:"reason"`
			}
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			var taskState, workState string
			var syncCount int
			if err := runtimeDB.QueryRowContext(ctx, `SELECT t.state,w.state,(SELECT count(*) FROM work_item_history WHERE owner_id=t.id AND kind='qb_sync') FROM download_tasks t JOIN work_item_history w ON w.id=$2 WHERE t.id=$1`, p.TaskID, p.SubmitWorkID).Scan(&taskState, &workState, &syncCount); err != nil {
				t.Fatal(err)
			}
			if syncCount != 0 {
				t.Fatalf("unsafe first sync count=%d", syncCount)
			}
			if effect == "unknown" {
				if result.Applied || result.Reason != "SUBMIT_RECONCILE_REQUIRED" || workState != "leased" {
					t.Fatalf("unknown result=%s task=%s work=%s", raw, taskState, workState)
				}
			} else {
				if !result.Applied || taskState != "waiting_retry" || workState != "available" {
					t.Fatalf("no effect result=%s task=%s work=%s", raw, taskState, workState)
				}
				if _, err := owner.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=2,lease_until=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, p.SubmitWorkID); err != nil {
					t.Fatal(err)
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_submit_attempt($1,$2,2,'effect-session-next')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var grant struct {
					Granted bool `json:"granted"`
				}
				if err := json.Unmarshal(raw, &grant); err != nil {
					t.Fatal(err)
				}
				if !grant.Granted {
					t.Fatalf("proven no-effect cannot retry: %s", raw)
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_dispatch($1,$2,2,'effect-session-next','effect-permit-next',$3,'connection',$3,'{"key":"effects"}','{"root":"/downloads"}','{"hash":"a"}')`, p.TaskID, p.SubmitWorkID, acqDigest("a")).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, &grant); err != nil {
					t.Fatal(err)
				}
				if !grant.Granted {
					t.Fatalf("proven no-effect retry dispatch denied: %s", raw)
				}
			}
		})
	}
}
