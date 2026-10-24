package migrations

import (
	"bansoko/internal/runtime/database"
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestDownloadIssuedSubmitWallClosesWork(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, owner, "issued-submit-wall")
	p := validQueued(t, "issued-submit-wall", "issued-submit-wall-key", "issued-submit-wall")
	var input map[string]any
	if err := json.Unmarshal([]byte(p.InputCanonical), &input); err != nil {
		t.Fatal(err)
	}
	limits := input["limits"].(map[string]any)
	limits["download_wait_seconds"] = 3
	limits["stall_seconds"] = 1
	limits["sync"].(map[string]any)["max_polls"] = 2
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
	work := p.SubmitWorkID
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=1,lease_until=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, work); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var grant struct {
		Granted bool   `json:"granted"`
		Reason  string `json:"reason"`
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_submit_attempt($1,$2,1,'issued-submit-wall-session')`, p.TaskID, work).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &grant); err != nil {
		t.Fatal(err)
	}
	if !grant.Granted {
		t.Fatalf("initial poll not granted: %s", raw)
	}
	var before, after string
	const snapshot = `SELECT jsonb_build_array(submit_attempts_started,submit_attempt_index,submit_attempt_ticket,budget_clock_at)::text FROM download_tasks WHERE id=$1`
	if err := runtimeDB.QueryRowContext(ctx, snapshot, p.TaskID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_submit_attempt($1,$2,1,'issued-submit-wall-session')`, p.TaskID, work).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var repeated struct {
		Granted bool `json:"granted"`
	}
	if err := json.Unmarshal(raw, &repeated); err != nil {
		t.Fatal(err)
	}
	if repeated.Granted {
		t.Fatal("same token regranted poll")
	}
	if err := runtimeDB.QueryRowContext(ctx, snapshot, p.TaskID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("same token changed counters or deadline")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var expired bool
		if err := runtimeDB.QueryRowContext(ctx, `SELECT clock_timestamp()>=wait_until FROM download_tasks WHERE id=$1`, p.TaskID).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("wall did not expire")
		}
		time.Sleep(50 * time.Millisecond)
	}
	tx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE download_tasks SET submit_attempt_ticket=jsonb_set(submit_attempt_ticket,'{state}','"superseded"'),submit_attempt_index=jsonb_set(submit_attempt_index,'{0,state}','"superseded"') WHERE id=$1`, p.TaskID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	_, err = tx.ExecContext(ctx, `WITH proposed AS (SELECT jsonb_set(jsonb_set(submit_attempt_ticket,'{ordinal}',to_jsonb(submit_attempts_started+1)),'{state}','"issued"') ticket FROM download_tasks WHERE id=$1) UPDATE download_tasks SET submit_attempts_started=submit_attempts_started+1,submit_attempt_ticket=proposed.ticket,submit_attempt_index=submit_attempt_index||jsonb_build_array(proposed.ticket) FROM proposed WHERE id=$1`, p.TaskID)
	tx.Rollback()
	if sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "DOWNLOAD_TASK_GRANT_DEADLINE" {
		t.Fatalf("new submit grant after wall with backdated ticket accepted: %v", err)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_submit_attempt($1,$2,1,'issued-submit-wall-session')`, p.TaskID, work).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	grant.Granted = false
	grant.Reason = ""
	if err := json.Unmarshal(raw, &grant); err != nil {
		t.Fatal(err)
	}
	var taskState, workState, ticketState string
	if err := runtimeDB.QueryRowContext(ctx, `SELECT t.state,w.state,t.submit_attempt_ticket->>'state' FROM download_tasks t JOIN work_item_history w ON w.id=$2 WHERE t.id=$1`, p.TaskID, work).Scan(&taskState, &workState, &ticketState); err != nil {
		t.Fatal(err)
	}
	if grant.Granted || grant.Reason != "SUBMIT_BUDGET_EXHAUSTED" || taskState != "failed" || workState != "failed" || ticketState != "superseded" {
		t.Fatalf("issued wall task=%s work=%s ticket=%s result=%s", taskState, workState, ticketState, raw)
	}
}
