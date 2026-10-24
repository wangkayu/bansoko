package migrations

import (
	"bansoko/internal/runtime/database"
	"context"
	"encoding/json"
	"testing"
)

func TestDownloadFinishSyncPreservesTerminalConclusion(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, owner, "finish-race")
	p := validQueued(t, "finish-race", "finish-race-key", "finish-race")
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
	if err := runtimeDB.QueryRowContext(ctx, `SELECT work_item_create('race-sync','download_task',$1,'qb_sync',$2,5,'{"poll_ordinal":1}',1,0::double precision,7::smallint)`, p.TaskID, p.TaskIdempotency).Scan(&work); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=1,lease_until=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, work); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_sync($1,$2,1,1,'race-session')`, p.TaskID, work).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var grant struct {
		Granted bool `json:"granted"`
	}
	if err := json.Unmarshal(raw, &grant); err != nil {
		t.Fatal(err)
	}
	if !grant.Granted {
		t.Fatalf("no initial grant: %s", raw)
	}
	// Arrange a committed terminal owner and a still-leased late work with its current version.
	if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET state='failed',failure_code='download_stalled',failure_reason='terminal race',version=version+1 WHERE id=$1`, p.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET expected_owner_version=(SELECT version FROM download_tasks WHERE id=$1) WHERE id=$2`, p.TaskID, work); err != nil {
		t.Fatal(err)
	}
	var before, after string
	const snapshot = `SELECT (to_jsonb(t)-'active_poll_ticket')::text FROM download_tasks t WHERE id=$1`
	if err := runtimeDB.QueryRowContext(ctx, snapshot, p.TaskID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	_, err := runtimeDB.ExecContext(ctx, `SELECT download_finish_sync($1,$2,1,'race-session',1,'downloading',NULL,NULL,NULL,false,NULL,NULL,'downloading','race-next',NULL,NULL,NULL)`, p.TaskID, work)
	if err != nil && sqlState(err) != "23514" {
		t.Fatal(err)
	}
	if err := runtimeDB.QueryRowContext(ctx, snapshot, p.TaskID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("late FinishSync rewrote committed terminal Task conclusion")
	}
	var workState, ticketState string
	if err := runtimeDB.QueryRowContext(ctx, `SELECT w.state,t.active_poll_ticket->>'state' FROM download_tasks t JOIN work_item_history w ON w.id=$2 WHERE t.id=$1`, p.TaskID, work).Scan(&workState, &ticketState); err != nil {
		t.Fatal(err)
	}
	if workState != "failed" || ticketState != "superseded" {
		t.Fatalf("late finish work=%s ticket=%s", workState, ticketState)
	}
	if _, err := runtimeDB.ExecContext(ctx, `SELECT download_finish_sync($1,$2,1,'wrong-session',99,'downloading',NULL,NULL,NULL,false,NULL,NULL,'downloading','wrong-next',NULL,NULL,NULL)`, p.TaskID, work); sqlState(err) != "23514" {
		t.Fatalf("terminal sync receipt accepted wrong session/ordinal: %v", err)
	}
}
