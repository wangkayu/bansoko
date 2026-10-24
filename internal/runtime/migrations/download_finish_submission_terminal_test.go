package migrations

import (
	"bansoko/internal/runtime/database"
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestDownloadFinishSubmissionPreservesTerminalConclusion(t *testing.T) {
	for _, terminal := range []string{"completed", "failed"} {
		for _, expired := range []bool{false, true} {
			name := terminal + "/valid"
			if expired {
				name = terminal + "/expired"
			}
			t.Run(name, func(t *testing.T) {
				owner := installedDB(t)
				ctx := context.Background()
				seedAcceptableRelease(t, owner, "submit-terminal-race")
				p := validQueued(t, "submit-terminal-race", "submit-terminal-race-key", "submit-terminal-race")
				if expired {
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
				}
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
				if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_submit_attempt($1,$2,1,'race-session')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_dispatch($1,$2,1,'race-session','race-permit',$3,'connection',$3,'{"key":"race"}','{"root":"/downloads"}','{"hash":"a"}')`, p.TaskID, p.SubmitWorkID, acqDigest("a")).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT download_record_submission_receipt($1,'race-permit','race-session',$2,'confirmed','{"kind":"channel_closed"}','{"kind":"same_key_found"}','{"found":true}',NULL,$3,1,NULL)`, p.AcquisitionID, acqDigest("a"), p.SubmitWorkID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if _, err := owner.ExecContext(ctx, `UPDATE download_tasks SET state=$2,failure_code=CASE WHEN $2='failed' THEN 'download_stalled' END,failure_reason=CASE WHEN $2='failed' THEN 'terminal race' END,version=version+1 WHERE id=$1`, p.TaskID, terminal); err != nil {
					t.Fatal(err)
				}
				if _, err := owner.ExecContext(ctx, `UPDATE work_items SET expected_owner_version=(SELECT version FROM download_tasks WHERE id=$1) WHERE id=$2`, p.TaskID, p.SubmitWorkID); err != nil {
					t.Fatal(err)
				}
				var before, after string
				const snapshot = `SELECT (to_jsonb(t)-'submit_attempt_ticket'-'submit_attempt_index')::text FROM download_tasks t WHERE id=$1`
				if err := runtimeDB.QueryRowContext(ctx, snapshot, p.TaskID).Scan(&before); err != nil {
					t.Fatal(err)
				}
				if expired {
					deadline := time.Now().Add(10 * time.Second)
					for {
						var done bool
						if err := runtimeDB.QueryRowContext(ctx, `SELECT clock_timestamp()>=(submit_attempt_ticket->>'attempt_until')::timestamptz FROM download_tasks WHERE id=$1`, p.TaskID).Scan(&done); err != nil {
							t.Fatal(err)
						}
						if done {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal("ticket did not expire")
						}
						time.Sleep(50 * time.Millisecond)
					}
				}
				_, err := runtimeDB.ExecContext(ctx, `SELECT download_finish_submission($1,$2,1,'race-session','race-permit','downloading','{"external_id":"object-a"}',NULL,'forbidden-race-sync')`, p.TaskID, p.SubmitWorkID)
				if err != nil && sqlState(err) != "23514" {
					t.Fatal(err)
				}
				if err := runtimeDB.QueryRowContext(ctx, snapshot, p.TaskID).Scan(&after); err != nil {
					t.Fatal(err)
				}
				if before != after {
					t.Fatal("late FinishSubmission rewrote committed terminal Task conclusion")
				}
				var workState, ticketState, indexState string
				var successors int
				if err := runtimeDB.QueryRowContext(ctx, `SELECT w.state,t.submit_attempt_ticket->>'state',t.submit_attempt_index->0->>'state',(SELECT count(*) FROM work_item_history WHERE id='forbidden-race-sync') FROM download_tasks t JOIN work_item_history w ON w.id=$2 WHERE t.id=$1`, p.TaskID, p.SubmitWorkID).Scan(&workState, &ticketState, &indexState, &successors); err != nil {
					t.Fatal(err)
				}
				want := "failed"
				if terminal == "completed" {
					want = "succeeded"
				}
				if workState != want || ticketState != "superseded" || indexState != "superseded" || successors != 0 {
					t.Fatalf("terminal work=%s ticket=%s index=%s next=%d", workState, ticketState, indexState, successors)
				}
				const repeatSnapshot = `SELECT jsonb_build_array(to_jsonb(t),to_jsonb(w),a.facts_revision)::text FROM download_tasks t JOIN work_item_history w ON w.id=$2 JOIN acquisitions a ON a.id=t.acquisition_id WHERE t.id=$1`
				if err := runtimeDB.QueryRowContext(ctx, repeatSnapshot, p.TaskID, p.SubmitWorkID).Scan(&before); err != nil {
					t.Fatal(err)
				}
				if _, err := runtimeDB.ExecContext(ctx, `SELECT download_finish_submission($1,$2,1,'wrong-session','race-permit','downloading','{"external_id":"object-a"}',NULL,'forbidden-race-sync')`, p.TaskID, p.SubmitWorkID); sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "SUBMIT_RECEIPT_SESSION" {
					t.Fatalf("terminal duplicate wrong session: %v", err)
				}
				if _, err := runtimeDB.ExecContext(ctx, `SELECT download_finish_submission($1,$2,1,'race-session','race-permit','downloading','{"external_id":"object-a"}',NULL,'forbidden-race-sync')`, p.TaskID, p.SubmitWorkID); err != nil {
					t.Fatalf("legitimate terminal closure receipt rejected on repeat: %v", err)
				}
				if err := runtimeDB.QueryRowContext(ctx, repeatSnapshot, p.TaskID, p.SubmitWorkID).Scan(&after); err != nil {
					t.Fatal(err)
				}
				if before != after {
					t.Fatal("repeat terminal receipt changed task/work/facts")
				}
			})
		}
	}
}
