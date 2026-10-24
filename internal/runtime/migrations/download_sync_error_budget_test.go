package migrations

import (
	"bansoko/internal/runtime/database"
	"context"
	"encoding/json"
	"testing"
)

func TestDownloadSyncErrorBudgetExhaustion(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, owner, "sync-errors")
	p := validQueued(t, "sync-errors", "sync-errors-key", "sync-errors")
	var input map[string]any
	if err := json.Unmarshal([]byte(p.InputCanonical), &input); err != nil {
		t.Fatal(err)
	}
	sync := input["limits"].(map[string]any)["sync"].(map[string]any)
	sync["max_consecutive_errors"] = 1
	sync["max_total_errors"] = 1
	p.InputCanonical = canonicalJSON(t, input)
	p.AdmissionDigest = acqDomainDigest("acquisition-input:v1", []byte(p.InputCanonical))
	if _, err := callQueuedAccept(t, owner, p); err != nil {
		t.Fatal(err)
	}
	password := randomTestPassword(t)
	if err := database.ProvisionRuntimeRole(ctx, owner, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
		t.Fatal(err)
	}
	runtimeDB := openRuntimeConnection(t, password)
	defer runtimeDB.Close()
	var work string
	if err := runtimeDB.QueryRowContext(ctx, `SELECT work_item_create('error-sync','download_task',$1,'qb_sync',$2,5,'{"poll_ordinal":1}',1,0::double precision,7::smallint)`, p.TaskID, p.TaskIdempotency).Scan(&work); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=1,lease_until=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, work); err != nil {
		t.Fatal(err)
	}
	for _, probe := range []struct{ name, patch string }{
		{"cancel requested", `cancel_requested=true`},
		{"recovery blocked", `recovery_blocked=true,recovery_reason='effect_unknown'`},
		{"expired lease", `lease_until=clock_timestamp()-INTERVAL '1 second'`},
		{"wrong owner version", `expected_owner_version=expected_owner_version+1`},
	} {
		tx, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE work_items SET `+probe.patch+` WHERE id=$1`, work); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		_, err = tx.ExecContext(ctx, `WITH proposed AS (SELECT jsonb_build_object('work_item_id',$2::text,'token',1,'ordinal',1,'executor_session','probe','started_at',statement_timestamp(),'attempt_until',statement_timestamp()+INTERVAL '1 second','state','issued') AS ticket) UPDATE download_tasks SET polls_started=1,next_poll_ordinal=2,active_poll_ticket=proposed.ticket FROM proposed WHERE id=$1`, p.TaskID, work)
		tx.Rollback()
		if sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "DOWNLOAD_TASK_GRANT_WORK_IDENTITY" {
			t.Fatalf("new sync grant accepted %s: %v", probe.name, err)
		}
	}
	if _, err := owner.ExecContext(ctx, `WITH proposed AS (SELECT jsonb_build_object('work_item_id',$2::text,'token',1,'ordinal',1,'executor_session','future-probe','started_at',statement_timestamp()+INTERVAL '10 seconds','attempt_until',statement_timestamp()+INTERVAL '11 seconds','state','issued') AS ticket) UPDATE download_tasks SET polls_started=1,next_poll_ordinal=2,active_poll_ticket=proposed.ticket FROM proposed WHERE id=$1`, p.TaskID, work); sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "DOWNLOAD_TASK_GRANT_FUTURE_TIME" {
		t.Fatalf("new sync grant with future started_at accepted: %v", err)
	}
	for _, rootState := range []string{"failed", "succeeded"} {
		rootTx, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = rootTx.ExecContext(ctx, `UPDATE acquisitions SET state=$2,terminal_at=statement_timestamp(),terminal_schema='acquisition-terminal:v1',terminal_receipt='{}',detail_until=statement_timestamp()+detail_retention_seconds*INTERVAL '1 second',receipt_until=greatest(receipt_until,statement_timestamp()+receipt_after_terminal_seconds*INTERVAL '1 second') WHERE id=$1`, p.AcquisitionID, rootState); err != nil {
			rootTx.Rollback()
			t.Fatal(err)
		}
		_, err = rootTx.ExecContext(ctx, `WITH proposed AS (SELECT jsonb_build_object('work_item_id',$2::text,'token',1,'ordinal',1,'executor_session','root-probe','started_at',statement_timestamp(),'attempt_until',statement_timestamp()+INTERVAL '1 second','state','issued') AS ticket) UPDATE download_tasks SET polls_started=1,next_poll_ordinal=2,active_poll_ticket=proposed.ticket FROM proposed WHERE id=$1`, p.TaskID, work)
		rootTx.Rollback()
		if sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "DOWNLOAD_TASK_ROOT_GRANT" {
			t.Fatalf("new sync grant on %s root accepted: %v", rootState, err)
		}
	}
	scheduledTx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = scheduledTx.ExecContext(ctx, `UPDATE work_items SET available_at=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, work); err != nil {
		scheduledTx.Rollback()
		t.Fatal(err)
	}
	_, err = scheduledTx.ExecContext(ctx, `SELECT download_start_sync($1,$2,1,1,'not-scheduled-yet')`, p.TaskID, work)
	scheduledTx.Rollback()
	if err != nil {
		t.Fatalf("valid claim rejected because available_at changed: %v", err)
	}
	ordinalTx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ordinalTx.ExecContext(ctx, `UPDATE work_items SET input_snapshot=jsonb_set(input_snapshot,'{poll_ordinal}','99') WHERE id=$1`, work); err != nil {
		ordinalTx.Rollback()
		t.Fatal(err)
	}
	_, err = ordinalTx.ExecContext(ctx, `SELECT download_start_sync($1,$2,1,1,'wrong-work-ordinal')`, p.TaskID, work)
	ordinalTx.Rollback()
	if sqlState(err) != "23514" {
		t.Fatalf("sync Start granted ticket for wrong frozen work ordinal: %v", err)
	}
	var raw []byte
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_sync($1,$2,1,1,'error-session')`, p.TaskID, work).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET active_poll_ticket=jsonb_set(active_poll_ticket,'{executor_session}','"changed-session"') WHERE id=$1`, p.TaskID); sqlState(err) != "23514" {
		t.Fatalf("issued sync ticket identity rewrite accepted: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET active_poll_ticket=NULL WHERE id=$1`, p.TaskID); sqlState(err) != "23514" {
		t.Fatalf("issued sync ticket deletion accepted: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET polls_started=polls_started+1,next_poll_ordinal=next_poll_ordinal+1,active_poll_ticket=jsonb_set(jsonb_set(active_poll_ticket,'{ordinal}','2'),'{executor_session}','"replacement"') WHERE id=$1`, p.TaskID); sqlState(err) != "23514" {
		t.Fatalf("issued sync ticket replaced without closure: %v", err)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_finish_sync($1,$2,1,'error-session',1,'downloading',NULL,'provider_unavailable','temporary',true,NULL,NULL,NULL,'forbidden-next',NULL,NULL,NULL)`, p.TaskID, work).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var state, code string
	var consecutive, total, count int
	if err := runtimeDB.QueryRowContext(ctx, `SELECT state,failure_code,consecutive_errors,total_errors,(SELECT count(*) FROM work_item_history WHERE id='forbidden-next') FROM download_tasks WHERE id=$1`, p.TaskID).Scan(&state, &code, &consecutive, &total, &count); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || code != "SYNC_ERROR_BUDGET_EXHAUSTED" || consecutive != 1 || total != 1 || count != 0 {
		t.Fatalf("error budget state=%s code=%s errors=%d/%d next=%d", state, code, consecutive, total, count)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET active_poll_ticket=jsonb_set(active_poll_ticket,'{state}','"issued"') WHERE id=$1`, p.TaskID); sqlState(err) != "23514" {
		t.Fatalf("completed sync ticket reopened: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET polls_started=polls_started+2,next_poll_ordinal=next_poll_ordinal+2,active_poll_ticket=jsonb_set(jsonb_set(active_poll_ticket,'{ordinal}',to_jsonb(polls_started+2)),'{state}','"issued"') WHERE id=$1`, p.TaskID); sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "DOWNLOAD_TASK_GRANT_STEP" {
		t.Fatalf("sync grant skipped an ordinal: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET polls_started=polls_started+1,next_poll_ordinal=next_poll_ordinal+1,active_poll_ticket=jsonb_set(active_poll_ticket,'{ordinal}',to_jsonb(polls_started+1)) WHERE id=$1`, p.TaskID); sqlState(err) != "23514" {
		t.Fatalf("sync count advanced without issued grant: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET polls_started=polls_started+1,next_poll_ordinal=next_poll_ordinal+1,active_poll_ticket=jsonb_set(jsonb_set(jsonb_set(active_poll_ticket,'{ordinal}',to_jsonb(polls_started+1)),'{state}','"issued"'),'{attempt_until}',to_jsonb(LEAST(wait_until,(active_poll_ticket->>'started_at')::timestamptz+(sync_attempt_timeout_seconds+1)*INTERVAL '1 second'))) WHERE id=$1`, p.TaskID); sqlState(err) != "23514" {
		t.Fatalf("sync ticket exceeds frozen attempt timeout: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET polls_started=polls_started+1,next_poll_ordinal=next_poll_ordinal+1,active_poll_ticket=jsonb_set(jsonb_set(jsonb_set(active_poll_ticket,'{ordinal}',to_jsonb(polls_started+1)),'{state}','"issued"'),'{work_item_id}','"missing-sync-work"') WHERE id=$1`, p.TaskID); sqlState(err) != "23514" {
		t.Fatalf("sync ticket references nonexistent work: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET polls_started=polls_started+1,next_poll_ordinal=next_poll_ordinal+1,active_poll_ticket=jsonb_set(jsonb_set(active_poll_ticket,'{ordinal}',to_jsonb(polls_started+1)),'{state}','"issued"') WHERE id=$1`, p.TaskID); sqlState(err) != "23514" {
		t.Fatalf("new poll granted against terminal work: %v", err)
	}
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `WITH o AS (DELETE FROM work_item_outcomes WHERE id=$2 RETURNING *)
INSERT INTO work_items(id,owner_type,owner_id,kind,state,available_at,lease_until,fencing_token,idempotency_key,attempt,max_attempts,input_snapshot,expected_owner_version,last_error_code,last_error_reason,retention_days,created_at,updated_at,cancel_requested,managed_anime_id,owner_lifecycle_epoch,external_effect_admitted_at,recovery_blocked,recovery_reason,acquisition_id)
SELECT id,owner_type,owner_id,kind,'leased',available_at,clock_timestamp()+INTERVAL '1 hour',fencing_token,idempotency_key,attempt,max_attempts,input_snapshot-'sync_ticket',(SELECT version FROM download_tasks WHERE id=$1),last_error_code,last_error_reason,retention_days,created_at,updated_at,cancel_requested,managed_anime_id,owner_lifecycle_epoch,external_effect_admitted_at,recovery_blocked,recovery_reason,acquisition_id FROM o`, p.TaskID, work); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE download_tasks SET polls_started=polls_started+1,next_poll_ordinal=next_poll_ordinal+1,active_poll_ticket=jsonb_set(jsonb_set(active_poll_ticket,'{ordinal}',to_jsonb(polls_started+1)),'{state}','"issued"') WHERE id=$1`, p.TaskID); sqlState(err) != "23514" {
		t.Fatalf("terminal Task granted new poll despite valid work claim: %v", err)
	}
}
