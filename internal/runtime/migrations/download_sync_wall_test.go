package migrations

import (
	"bansoko/internal/runtime/database"
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestDownloadSyncWallExhaustionClosesWork(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, owner, "sync-wall")
	p := validQueued(t, "sync-wall", "sync-wall-key", "sync-wall")
	var input map[string]any
	if err := json.Unmarshal([]byte(p.InputCanonical), &input); err != nil {
		t.Fatal(err)
	}
	limits := input["limits"].(map[string]any)
	limits["download_wait_seconds"] = 1
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
	if err := runtimeDB.QueryRowContext(ctx, `SELECT work_item_create('wall-sync','download_task',$1,'qb_sync',$2,5,'{"poll_ordinal":1}',1,0::double precision,7::smallint)`, p.TaskID, p.TaskIdempotency).Scan(&work); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=1,lease_until=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, work); err != nil {
		t.Fatal(err)
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
	var raw []byte
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_sync($1,$2,1,1,'wall-session')`, p.TaskID, work).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var grant struct {
		Granted bool `json:"granted"`
	}
	if err := json.Unmarshal(raw, &grant); err != nil {
		t.Fatal(err)
	}
	if grant.Granted {
		t.Fatal("expired wall granted observation")
	}
	var taskState, workState string
	if err := runtimeDB.QueryRowContext(ctx, `SELECT t.state,w.state FROM download_tasks t JOIN work_item_history w ON w.id=$2 WHERE t.id=$1`, p.TaskID, work).Scan(&taskState, &workState); err != nil {
		t.Fatal(err)
	}
	if taskState != "failed" || workState != "failed" {
		t.Fatalf("wall exhaustion left task=%s work=%s result=%s", taskState, workState, raw)
	}
	// The exhaustion wakes settlement (the explicit wake and the closed work
	// item's exit at commit each advance facts_revision).
	var code string
	var facts int64
	var settleCount, polls int
	if err := runtimeDB.QueryRowContext(ctx, `SELECT t.failure_code,t.polls_started,a.facts_revision,(SELECT count(*) FROM work_items WHERE acquisition_id=a.id AND kind='acquisition_settle' AND state IN ('available','leased')) FROM download_tasks t JOIN acquisitions a ON a.id=t.acquisition_id WHERE t.id=$1`, p.TaskID).Scan(&code, &polls, &facts, &settleCount); err != nil {
		t.Fatal(err)
	}
	if code != "SYNC_BUDGET_EXHAUSTED" || polls != 0 || facts == 0 || settleCount != 1 {
		t.Fatalf("exhaustion code=%s polls=%d facts=%d settle=%d", code, polls, facts, settleCount)
	}
}
