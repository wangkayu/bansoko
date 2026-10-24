package migrations

import (
	"bansoko/internal/runtime/database"
	"context"
	"encoding/json"
	"testing"
)

func TestDownloadLastSubmitAttemptReentryPreservesLiveTicket(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, owner, "submit-last")
	p := validQueued(t, "submit-last", "submit-last-key", "submit-last")
	var input map[string]any
	if err := json.Unmarshal([]byte(p.InputCanonical), &input); err != nil {
		t.Fatal(err)
	}
	input["limits"].(map[string]any)["submit"].(map[string]any)["max_attempts"] = 1
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
	var before, after string
	const snapshot = `SELECT jsonb_build_array(to_jsonb(t),to_jsonb(w),a.facts_revision)::text FROM download_tasks t JOIN work_item_history w ON w.id=$2 JOIN acquisitions a ON a.id=t.acquisition_id WHERE t.id=$1`
	if err := runtimeDB.QueryRowContext(ctx, snapshot, p.TaskID, p.SubmitWorkID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_submit_attempt($1,$2,1,'last-session')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := runtimeDB.QueryRowContext(ctx, snapshot, p.TaskID, p.SubmitWorkID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("valid last-attempt reentry changed task/work/facts: %s", raw)
	}
}
