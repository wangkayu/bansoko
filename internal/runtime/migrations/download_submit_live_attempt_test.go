package migrations

import (
	"bansoko/internal/runtime/database"
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestDownloadNewTokenDoesNotReplaceLiveSubmitAttempt(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, owner, "submit-last")
	p := validQueued(t, "submit-last", "submit-last-key", "submit-last")
	var input map[string]any
	if err := json.Unmarshal([]byte(p.InputCanonical), &input); err != nil {
		t.Fatal(err)
	}
	input["limits"].(map[string]any)["submit"].(map[string]any)["max_attempts"] = 2
	input["limits"].(map[string]any)["submit"].(map[string]any)["attempt_timeout_seconds"] = 2
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
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=1,lease_until=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, p.SubmitWorkID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET submit_attempts_started=submit_attempts_started+2 WHERE id=$1`, p.TaskID); sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "DOWNLOAD_TASK_GRANT_STEP" {
		t.Fatalf("submit grant skipped an ordinal: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET submit_attempts_started=submit_attempts_started+1,submit_attempt_ticket=jsonb_build_object('state','completed') WHERE id=$1`, p.TaskID); sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "DOWNLOAD_TASK_GRANT_REQUIRES_ISSUED" {
		t.Fatalf("submit count advanced without issued grant: %v", err)
	}
	for _, probe := range []struct{ name, patch string }{
		{"expired lease", `lease_until=clock_timestamp()-INTERVAL '1 second'`},
		{"wrong owner version", `expected_owner_version=expected_owner_version+1`},
		{"cancel requested", `cancel_requested=true`},
		{"recovery blocked", `recovery_blocked=true,recovery_reason='effect_unknown'`},
	} {
		tx, err := owner.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE work_items SET `+probe.patch+` WHERE id=$1`, p.SubmitWorkID); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		_, err = tx.ExecContext(ctx, `WITH proposed AS (SELECT jsonb_build_object('work_item_id',$2::text,'token',1,'ordinal',1,'executor_session','probe','started_at',statement_timestamp(),'attempt_until',statement_timestamp()+INTERVAL '1 second','state','issued') AS ticket) UPDATE download_tasks SET submit_attempts_started=1,submit_attempt_ticket=proposed.ticket,submit_attempt_index=jsonb_build_array(proposed.ticket) FROM proposed WHERE id=$1`, p.TaskID, p.SubmitWorkID)
		tx.Rollback()
		if sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "DOWNLOAD_TASK_GRANT_WORK_IDENTITY" {
			t.Fatalf("new grant accepted %s: %v", probe.name, err)
		}
	}
	if _, err := owner.ExecContext(ctx, `WITH proposed AS (SELECT jsonb_build_object('work_item_id',$2::text,'token',1,'ordinal',1,'executor_session','future-probe','started_at',statement_timestamp()+INTERVAL '10 seconds','attempt_until',statement_timestamp()+INTERVAL '11 seconds','state','issued') AS ticket) UPDATE download_tasks SET submit_attempts_started=1,submit_attempt_ticket=proposed.ticket,submit_attempt_index=jsonb_build_array(proposed.ticket) FROM proposed WHERE id=$1`, p.TaskID, p.SubmitWorkID); sqlState(err) != "23514" {
		t.Fatalf("new submit grant with future started_at accepted: %v", err)
	}
	rootTx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rootTx.ExecContext(ctx, `UPDATE acquisitions SET state='failed',terminal_at=statement_timestamp(),terminal_schema='acquisition-terminal:v1',terminal_receipt='{}',detail_until=statement_timestamp()+detail_retention_seconds*INTERVAL '1 second',receipt_until=greatest(receipt_until,statement_timestamp()+receipt_after_terminal_seconds*INTERVAL '1 second') WHERE id=$1`, p.AcquisitionID); err != nil {
		rootTx.Rollback()
		t.Fatal(err)
	}
	_, err = rootTx.ExecContext(ctx, `WITH proposed AS (SELECT jsonb_build_object('work_item_id',$2::text,'token',1,'ordinal',1,'executor_session','root-probe','started_at',statement_timestamp(),'attempt_until',statement_timestamp()+INTERVAL '1 second','state','issued') AS ticket) UPDATE download_tasks SET submit_attempts_started=1,submit_attempt_ticket=proposed.ticket,submit_attempt_index=jsonb_build_array(proposed.ticket) FROM proposed WHERE id=$1`, p.TaskID, p.SubmitWorkID)
	rootTx.Rollback()
	if sqlState(err) != "23514" {
		t.Fatalf("new submit grant on terminal root accepted: %v", err)
	}
	insertTx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = insertTx.ExecContext(ctx, `CREATE TEMP TABLE insert_task_probe AS SELECT * FROM download_tasks WHERE id=$1`, p.TaskID); err != nil {
		insertTx.Rollback()
		t.Fatal(err)
	}
	if _, err = insertTx.ExecContext(ctx, `DELETE FROM work_items WHERE owner_type='download_task' AND owner_id=$1`, p.TaskID); err != nil {
		insertTx.Rollback()
		t.Fatal(err)
	}
	if _, err = insertTx.ExecContext(ctx, `DELETE FROM download_tasks WHERE id=$1`, p.TaskID); err != nil {
		insertTx.Rollback()
		t.Fatal(err)
	}
	_, err = insertTx.ExecContext(ctx, `WITH proposed AS (SELECT jsonb_build_object('work_item_id','missing-insert-work','token',1,'ordinal',1,'executor_session','insert-probe','started_at',statement_timestamp(),'attempt_until',statement_timestamp()+INTERVAL '1 second','state','issued') AS ticket) INSERT INTO download_tasks(id,release_id,acquisition_id,frozen_input,frozen_policy,state,idempotency_key,submit_attempts_started,submit_attempt_ticket,submit_attempt_index) SELECT id,release_id,acquisition_id,frozen_input,frozen_policy,'queued',idempotency_key,1,proposed.ticket,jsonb_build_array(proposed.ticket) FROM insert_task_probe CROSS JOIN proposed`)
	insertTx.Rollback()
	if sqlState(err) != "23514" {
		t.Fatalf("Task INSERT accepted pre-granted ticket with nonexistent work: %v", err)
	}
	var raw []byte
	var grant struct {
		Granted bool `json:"granted"`
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_submit_attempt($1,$2,1,'last-session')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &grant); err != nil {
		t.Fatal(err)
	}
	if !grant.Granted {
		t.Fatalf("no initial grant: %s", raw)
	}
	for _, patch := range []string{`kind='qb_sync'`, `owner_type='acquisition'`, `owner_id='changed-owner'`, `acquisition_id=NULL`} {
		if _, err := owner.ExecContext(ctx, `UPDATE work_items SET `+patch+` WHERE id=$1`, p.SubmitWorkID); sqlState(err) != "23514" || !ownerImmutableMessage(adoptAcceptPGMessage(err)) {
			t.Fatalf("submit work identity changed (%s): %v", patch, err)
		}
	}
	regrantTx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = regrantTx.ExecContext(ctx, `UPDATE download_tasks SET submit_attempt_ticket=jsonb_set(submit_attempt_ticket,'{state}','"superseded"'),submit_attempt_index=jsonb_set(submit_attempt_index,'{0,state}','"superseded"') WHERE id=$1`, p.TaskID); err != nil {
		regrantTx.Rollback()
		t.Fatal(err)
	}
	_, err = regrantTx.ExecContext(ctx, `WITH proposed AS (SELECT jsonb_set(jsonb_set(submit_attempt_ticket,'{ordinal}',to_jsonb(submit_attempts_started+1)),'{state}','"issued"') ticket FROM download_tasks WHERE id=$1) UPDATE download_tasks SET submit_attempts_started=submit_attempts_started+1,submit_attempt_ticket=proposed.ticket,submit_attempt_index=submit_attempt_index||jsonb_build_array(proposed.ticket) FROM proposed WHERE id=$1`, p.TaskID)
	regrantTx.Rollback()
	if sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "DOWNLOAD_TASK_TOKEN_REGRANT" {
		t.Fatalf("same submit work/token granted another ordinal: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET fencing_token=2 WHERE id=$1`, p.SubmitWorkID); err != nil {
		t.Fatal(err)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_submit_attempt($1,$2,2,'next-session')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	grant.Granted = false
	if err := json.Unmarshal(raw, &grant); err != nil {
		t.Fatal(err)
	}
	if grant.Granted {
		t.Fatalf("new token replaced an unexpired issued attempt: %s", raw)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var expired bool
		if err := runtimeDB.QueryRowContext(ctx, `SELECT clock_timestamp()>=(submit_attempt_ticket->>'attempt_until')::timestamptz FROM download_tasks WHERE id=$1`, p.TaskID).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("attempt did not expire")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_submit_attempt($1,$2,2,'next-session')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var issued int
	if err := runtimeDB.QueryRowContext(ctx, `SELECT count(*) FROM download_tasks t,jsonb_array_elements(t.submit_attempt_index) ticket WHERE t.id=$1 AND ticket->>'state'='issued'`, p.TaskID).Scan(&issued); err != nil {
		t.Fatal(err)
	}
	if issued != 1 {
		t.Fatalf("expired attempt not closed before successor grant: issued=%d result=%s", issued, raw)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET submit_attempt_index=jsonb_set(submit_attempt_index,'{0,state}','"issued"') WHERE id=$1`, p.TaskID); sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "SUBMIT_ATTEMPT_STATE_REGRESSION" {
		t.Fatalf("guard accepted two issued submit tickets: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET submit_attempt_ticket=jsonb_set(submit_attempt_ticket,'{executor_session}','"mismatched"') WHERE id=$1`, p.TaskID); sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "SUBMIT_ATTEMPT_INDEX_CONSISTENCY" {
		t.Fatalf("active/index mismatch accepted: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET submit_attempt_ticket=jsonb_set(submit_attempt_ticket,'{state}','"superseded"'),submit_attempt_index=jsonb_set(jsonb_set(submit_attempt_index,'{1,state}','"superseded"'),'{0,state}','"issued"') WHERE id=$1`, p.TaskID); sqlState(err) != "23514" {
		t.Fatalf("historical issued ticket without active issued slot accepted: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET submit_attempt_index=jsonb_set(submit_attempt_index,'{0,executor_session}','"rewritten-history"') WHERE id=$1`, p.TaskID); sqlState(err) != "23514" {
		t.Fatalf("historical granted ticket identity rewrite accepted: %v", err)
	}
	for _, query := range []string{`UPDATE download_tasks SET submit_attempt_ticket=NULL WHERE id=$1`, `UPDATE download_tasks SET submit_attempt_index='[]' WHERE id=$1`} {
		if _, err := owner.ExecContext(ctx, query, p.TaskID); sqlState(err) != "23514" {
			t.Fatalf("submit ticket deletion accepted: %v", err)
		}
	}
}

// ownerImmutableMessage accepts the ticket guard's message and the generic
// owner fence's: a patch that moves the row off the download_task owner type
// is rejected by the fence, which the ticket guard no longer sees.
func ownerImmutableMessage(message string) bool {
	return message == "DOWNLOAD_WORK_TICKET_OWNER_IMMUTABLE" || message == "WORK_ITEM_OWNER_IMMUTABLE"
}
