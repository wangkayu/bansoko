package migrations

import (
	"bansoko/internal/runtime/database"
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestDownloadIssuedSyncWallClosesWork(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, owner, "issued-wall")
	p := validQueued(t, "issued-wall", "issued-wall-key", "issued-wall")
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
	var work string
	if err := runtimeDB.QueryRowContext(ctx, `SELECT work_item_create('issued-wall-sync','download_task',$1,'qb_sync',$2,5,'{"poll_ordinal":1}',1,0::double precision,7::smallint)`, p.TaskID, p.TaskIdempotency).Scan(&work); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=1,lease_until=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, work); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET input_snapshot=jsonb_set(input_snapshot,'{sync_ticket}','{}') WHERE id=$1`, work); sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "SYNC_WORK_TICKET_BINDING" {
		t.Fatalf("unbound initial sync ticket accepted: %v", err)
	}
	var raw []byte
	var grant struct {
		Granted bool   `json:"granted"`
		Reason  string `json:"reason"`
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_sync($1,$2,1,1,'issued-wall-session')`, p.TaskID, work).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &grant); err != nil {
		t.Fatal(err)
	}
	if !grant.Granted {
		t.Fatalf("initial poll not granted: %s", raw)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET input_snapshot=input_snapshot-'sync_ticket' WHERE id=$1`, work); sqlState(err) != "23514" {
		t.Fatalf("persisted sync receipt identity removed: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET input_snapshot=jsonb_set(input_snapshot,'{sync_ticket,executor_session}','"rewritten"') WHERE id=$1`, work); sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "SYNC_WORK_TICKET_IMMUTABLE" {
		t.Fatalf("persisted sync receipt identity rewritten: %v", err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET input_snapshot=jsonb_set(input_snapshot,'{poll_ordinal}','99') WHERE id=$1`, work); sqlState(err) != "23514" {
		t.Fatalf("frozen work poll ordinal changed after grant: %v", err)
	}
	for _, patch := range []string{`kind='qb_submit'`, `owner_type='acquisition'`, `owner_id='changed-owner'`, `acquisition_id=NULL`} {
		if _, err := owner.ExecContext(ctx, `UPDATE work_items SET `+patch+` WHERE id=$1`, work); sqlState(err) != "23514" || !ownerImmutableMessage(adoptAcceptPGMessage(err)) {
			t.Fatalf("sync work identity changed (%s): %v", patch, err)
		}
	}
	kindTx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = kindTx.ExecContext(ctx, `DELETE FROM work_items WHERE owner_type='download_task' AND owner_id=$1 AND kind='qb_submit'`, p.TaskID); err != nil {
		kindTx.Rollback()
		t.Fatal(err)
	}
	_, err = kindTx.ExecContext(ctx, `UPDATE work_items SET kind='qb_submit' WHERE id=$1`, work)
	kindTx.Rollback()
	if sqlState(err) != "23514" {
		t.Fatalf("work with persisted sync ticket changed kind: %v", err)
	}
	mergeTx, err := runtimeDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var merged string
	err = mergeTx.QueryRowContext(ctx, `SELECT work_item_merge('merge-issued-probe','download_task',$1,'qb_sync',$2,5,'{"poll_ordinal":1}',1,0::double precision,7::smallint)`, p.TaskID, p.TaskIdempotency).Scan(&merged)
	if err == nil {
		var preserved bool
		err = mergeTx.QueryRowContext(ctx, `SELECT input_snapshot->'sync_ticket'=(SELECT active_poll_ticket FROM download_tasks WHERE id=$2) AND input_snapshot->>'poll_ordinal'='1' FROM work_item_history WHERE id=$1`, merged, p.TaskID).Scan(&preserved)
		if err == nil && !preserved {
			t.Fatal("merge did not preserve frozen ticket and ordinal")
		}
	}
	mergeTx.Rollback()
	if err != nil {
		t.Fatalf("runtime merge of issued sync work must preserve original ticket: %v", err)
	}
	if _, err := runtimeDB.ExecContext(ctx, `SELECT work_item_merge('merge-conflict-probe','download_task',$1,'qb_sync',$2,5,'{"poll_ordinal":99}',1,0::double precision,7::smallint)`, p.TaskID, p.TaskIdempotency); sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "DOWNLOAD_WORK_MERGE_TICKET_CONFLICT" {
		t.Fatalf("merge accepted conflicting frozen ordinal: %v", err)
	}
	dueTx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dueTx.Rollback()
	if _, err = dueTx.ExecContext(ctx, `UPDATE download_tasks SET active_poll_ticket=jsonb_set(active_poll_ticket,'{state}','"superseded"') WHERE id=$1`, p.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err = dueTx.ExecContext(ctx, `SELECT public.work_item_close(id,'failed') FROM work_items
WHERE id=$1`, work); err != nil {
		t.Fatal(err)
	}
	if _, err = dueTx.ExecContext(ctx, `INSERT INTO work_items(id,owner_type,owner_id,acquisition_id,kind,idempotency_key,max_attempts,input_snapshot,expected_owner_version,state,fencing_token,lease_until) SELECT 'early-poll-probe',owner_type,owner_id,acquisition_id,kind,idempotency_key,max_attempts,'{"poll_ordinal":2}',expected_owner_version,'leased',1,clock_timestamp()+INTERVAL '1 hour' FROM work_items WHERE id=$1`, work); err != nil {
		t.Fatal(err)
	}
	_, err = dueTx.ExecContext(ctx, `UPDATE download_tasks SET polls_started=2,next_poll_ordinal=3,active_poll_ticket=jsonb_build_object('work_item_id','early-poll-probe','token',1,'ordinal',2,'executor_session','early','started_at',statement_timestamp(),'attempt_until',least(wait_until,statement_timestamp()+INTERVAL '1 second'),'state','issued') WHERE id=$1`, p.TaskID)
	dueTx.Rollback()
	if sqlState(err) != "23514" {
		t.Fatalf("new sync grant before frozen poll interval accepted: %v", err)
	}
	clockTx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = clockTx.ExecContext(ctx, `UPDATE download_tasks SET budget_clock_at=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, p.TaskID)
	clockTx.Rollback()
	if err != nil {
		t.Fatalf("historical budget observation high-water rejected: %v", err)
	}
	regrantTx, err := owner.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = regrantTx.ExecContext(ctx, `UPDATE download_tasks SET active_poll_ticket=jsonb_set(active_poll_ticket,'{state}','"superseded"') WHERE id=$1`, p.TaskID); err != nil {
		regrantTx.Rollback()
		t.Fatal(err)
	}
	_, err = regrantTx.ExecContext(ctx, `UPDATE download_tasks SET polls_started=polls_started+1,next_poll_ordinal=next_poll_ordinal+1,active_poll_ticket=jsonb_set(jsonb_set(active_poll_ticket,'{ordinal}',to_jsonb(polls_started+1)),'{state}','"issued"') WHERE id=$1`, p.TaskID)
	regrantTx.Rollback()
	if sqlState(err) != "23514" {
		t.Fatalf("same sync work/token granted another ordinal: %v", err)
	}
	var before, after string
	const snapshot = `SELECT jsonb_build_array(polls_started,next_poll_ordinal,active_poll_ticket,budget_clock_at)::text FROM download_tasks WHERE id=$1`
	if err := runtimeDB.QueryRowContext(ctx, snapshot, p.TaskID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_sync($1,$2,1,1,'issued-wall-session')`, p.TaskID, work).Scan(&raw); err != nil {
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
	if _, err = tx.ExecContext(ctx, `UPDATE download_tasks SET active_poll_ticket=jsonb_set(active_poll_ticket,'{state}','"superseded"') WHERE id=$1`, p.TaskID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE download_tasks SET polls_started=polls_started+1,next_poll_ordinal=next_poll_ordinal+1,active_poll_ticket=jsonb_set(jsonb_set(active_poll_ticket,'{ordinal}',to_jsonb(polls_started+1)),'{state}','"issued"') WHERE id=$1`, p.TaskID)
	tx.Rollback()
	if sqlState(err) != "23514" {
		t.Fatalf("new sync grant after wall with backdated ticket accepted: %v", err)
	}
	if _, err := runtimeDB.ExecContext(ctx, `SELECT download_finish_sync($1,$2,1,'issued-wall-session',1,'completed','{"external_id":"late-object"}',NULL,NULL,false,NULL,NULL,'uploading','late-inspect','late-artifact','{"files":[]}','{"files":[]}')`, p.TaskID, work); sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "SYNC_FINISH_IDENTITY" {
		t.Fatalf("late Finish accepted success: %v", err)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_sync($1,$2,1,1,'issued-wall-session')`, p.TaskID, work).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	grant.Granted = false
	grant.Reason = ""
	if err := json.Unmarshal(raw, &grant); err != nil {
		t.Fatal(err)
	}
	var taskState, workState, ticketState string
	if err := runtimeDB.QueryRowContext(ctx, `SELECT t.state,w.state,t.active_poll_ticket->>'state' FROM download_tasks t JOIN work_item_history w ON w.id=$2 WHERE t.id=$1`, p.TaskID, work).Scan(&taskState, &workState, &ticketState); err != nil {
		t.Fatal(err)
	}
	if grant.Granted || grant.Reason != "SYNC_BUDGET_EXHAUSTED" || taskState != "failed" || workState != "failed" || ticketState != "superseded" {
		t.Fatalf("issued wall task=%s work=%s ticket=%s result=%s", taskState, workState, ticketState, raw)
	}
}
