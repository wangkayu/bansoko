package migrations

import (
	"bansoko/internal/runtime/database"
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestDownloadRecoveryFinishAfterSubmitWallDoesNotAdvance(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, owner, "recovery-deadline")
	p := validQueued(t, "recovery-deadline", "recovery-deadline-key", "recovery-deadline")
	var input map[string]any
	if err := json.Unmarshal([]byte(p.InputCanonical), &input); err != nil {
		t.Fatal(err)
	}
	limits := input["limits"].(map[string]any)
	limits["submit"].(map[string]any)["wall_seconds"] = 3
	limits["submit"].(map[string]any)["attempt_timeout_seconds"] = 1

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
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_submit_attempt($1,$2,1,'deadline-submit')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_dispatch($1,$2,1,'deadline-submit','deadline-permit',$3,'connection',$3,'{"key":"deadline"}','{"root":"/downloads"}','{"hash":"a"}')`, p.TaskID, p.SubmitWorkID, acqDigest("a")).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET recovery_blocked=true,recovery_reason='effect_unknown',lease_until=clock_timestamp()-INTERVAL '1 second' WHERE id=$1`, p.SubmitWorkID); err != nil {
		t.Fatal(err)
	}
	var token int64
	if err := runtimeDB.QueryRowContext(ctx, `SELECT fencing_token FROM work_item_claim_recovery_observation(ARRAY['qb_submit'],3600)`).Scan(&token); err != nil {
		t.Fatal(err)
	}
	if token != 2 {
		t.Fatalf("token=%d", token)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_submit_lookup($1,$2,2,'deadline-permit','deadline-lookup')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var grant struct {
		Granted bool `json:"granted"`
	}
	if err := json.Unmarshal(raw, &grant); err != nil {
		t.Fatal(err)
	}
	if !grant.Granted {
		t.Fatalf("no lookup grant: %s", raw)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var expired bool
		if err := runtimeDB.QueryRowContext(ctx, `SELECT clock_timestamp()>=submit_wall_until FROM download_tasks WHERE id=$1`, p.TaskID).Scan(&expired); err != nil {
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
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_record_submission_receipt($1,'deadline-permit','deadline-lookup',$2,'confirmed','{"kind":"channel_closed"}','{"kind":"same_key_found"}','{"found":true}',(SELECT submit_lookup_ticket->>'ticket_id' FROM download_tasks WHERE submit_permit_id='deadline-permit'),$3,2,1)`, p.AcquisitionID, acqDigest("a"), p.SubmitWorkID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	_, err := runtimeDB.ExecContext(ctx, `SELECT download_finish_submission($1,$2,2,'deadline-lookup','deadline-permit','downloading','{"external_id":"deadline-object"}',NULL,'forbidden-deadline-sync')`, p.TaskID, p.SubmitWorkID)
	if err != nil && sqlState(err) != "23514" {
		t.Fatal(err)
	}
	var state, effect string
	var syncCount int
	if err := runtimeDB.QueryRowContext(ctx, `SELECT t.state,t.submit_effect_class,(SELECT count(*) FROM work_item_history WHERE id='forbidden-deadline-sync') FROM download_tasks t WHERE t.id=$1`, p.TaskID).Scan(&state, &effect, &syncCount); err != nil {
		t.Fatal(err)
	}
	if state == "downloading" || syncCount != 0 || effect != "confirmed" {
		t.Fatalf("late recovery state=%s effect=%s sync=%d", state, effect, syncCount)
	}
}
