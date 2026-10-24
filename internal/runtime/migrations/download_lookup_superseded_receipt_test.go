package migrations

import (
	"bansoko/internal/runtime/database"
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestDownloadLookupSupersededReceipt(t *testing.T) {
	for _, scenario := range []struct{ found, cancel bool }{{false, true}, {true, true}, {true, false}} {
		found, cancelledClaim := scenario.found, scenario.cancel
		t.Run(fmt.Sprintf("found_%v_cancel_%v", found, cancelledClaim), func(t *testing.T) {
			fixtureDB := installedDB(t)
			ctx := context.Background()
			seedAcceptableRelease(t, fixtureDB, "late")
			p := validQueued(t, "late", "late-key", "late")
			if _, err := callQueuedAccept(t, fixtureDB, p); err != nil {
				t.Fatal(err)
			}
			if _, err := fixtureDB.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=1,lease_until=CURRENT_TIMESTAMP+INTERVAL '1 hour' WHERE id=$1`, p.SubmitWorkID); err != nil {
				t.Fatal(err)
			}
			password := randomTestPassword(t)
			if err := database.ProvisionRuntimeRole(ctx, fixtureDB, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
				t.Fatal(err)
			}
			runtimeDB := openRuntimeConnection(t, password)
			defer runtimeDB.Close()
			var raw []byte
			if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_submit_attempt($1,$2,1,'submit')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_dispatch($1,$2,1,'submit','late-permit',$3,'connection',$3,'{"key":"late"}','{"root":"/downloads"}','{"hash":"a"}')`, p.TaskID, p.SubmitWorkID, acqDigest("a")).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var wrongWork string
			if err := runtimeDB.QueryRowContext(ctx, `SELECT work_item_create('lookup-wrong-kind','download_task',$1,'qb_sync',$2,5,'{"poll_ordinal":1}',1,0::double precision,7::smallint)`, p.TaskID, p.TaskIdempotency).Scan(&wrongWork); err != nil {
				t.Fatal(err)
			}
			if _, err := fixtureDB.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=1,lease_until=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, wrongWork); err != nil {
				t.Fatal(err)
			}
			_, kindErr := runtimeDB.ExecContext(ctx, `SELECT download_start_submit_lookup($1,$2,1,'late-permit','wrong-kind-session')`, p.TaskID, wrongWork)
			if sqlState(kindErr) != "23514" {
				t.Fatalf("submit lookup accepted qb_sync claim: %v", kindErr)
			}
			if _, err := fixtureDB.ExecContext(ctx, `DELETE FROM work_items WHERE id=$1`, wrongWork); err != nil {
				t.Fatal(err)
			}
			if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_submit_lookup($1,$2,1,'late-permit','lookup-old')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var ticketID, until string
			if err := runtimeDB.QueryRowContext(ctx, `SELECT submit_reconcile_until::text FROM download_tasks WHERE submit_permit_id='late-permit'`).Scan(&until); err != nil {
				t.Fatal(err)
			}
			if _, err := fixtureDB.ExecContext(ctx, `UPDATE work_items SET recovery_blocked=true,recovery_reason=CASE WHEN $2 THEN 'cancel_requested' ELSE 'effect_unknown' END,cancel_requested=$2,lease_until=clock_timestamp()-INTERVAL '1 second' WHERE id=$1`, p.SubmitWorkID, cancelledClaim); err != nil {
				t.Fatal(err)
			}
			var recoveredID string
			var recoveredToken int64
			if err := runtimeDB.QueryRowContext(ctx, `SELECT id,fencing_token FROM work_item_claim_recovery_observation(ARRAY['qb_submit'],3600)`).Scan(&recoveredID, &recoveredToken); err != nil {
				t.Fatal(err)
			}
			if recoveredID != p.SubmitWorkID || recoveredToken != 2 {
				t.Fatalf("recovery id=%s token=%d", recoveredID, recoveredToken)
			}
			if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_submit_lookup($1,$2,2,'late-permit','lookup-new')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			// The fold keeps only the current lookup ticket on the owner row, so the
			// receipt identity below is the ticket the second lookup just issued.
			if err := runtimeDB.QueryRowContext(ctx, `SELECT submit_lookup_ticket->>'ticket_id' FROM download_tasks WHERE submit_permit_id='late-permit'`).Scan(&ticketID); err != nil {
				t.Fatal(err)
			}
			var grant struct {
				Granted bool `json:"granted"`
			}
			if err := json.Unmarshal(raw, &grant); err != nil {
				t.Fatal(err)
			}
			if !grant.Granted {
				t.Fatalf("reclaim failed to supersede/grant: %s", raw)
			}
			if _, err := runtimeDB.ExecContext(ctx, `SELECT download_start_dispatch($1,$2,2,'lookup-new','forbidden-permit',$3,'connection',$3,'{"key":"late"}','{"root":"/downloads"}','{"hash":"a"}')`, p.TaskID, p.SubmitWorkID, acqDigest("a")); err == nil {
				t.Fatal("recovery claim granted new mutation")
			}
			effect := "unknown"
			var closed, resolution any
			if found {
				effect = "confirmed"
				closed = `{"kind":"channel_closed"}`
				resolution = `{"kind":"same_key_found"}`
			}
			observation, err := json.Marshal(map[string]bool{"found": found})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runtimeDB.ExecContext(ctx, `SELECT download_record_submission_receipt($1,'late-permit','wrong-session',$2,$3,$4::jsonb,$5::jsonb,$6::jsonb,$7,$8,2,2)`, p.AcquisitionID, acqDigest("a"), effect, closed, resolution, string(observation), ticketID, p.SubmitWorkID); sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "SUBMIT_RECEIPT_LOOKUP_TICKET" {
				t.Fatalf("wrong lookup receipt identity accepted: %v", err)
			}
			for i := 0; i < 2; i++ {
				if err := runtimeDB.QueryRowContext(ctx, `SELECT download_record_submission_receipt($1,'late-permit','lookup-new',$2,$3,$4::jsonb,$5::jsonb,$6::jsonb,$7,$8,2,2)`, p.AcquisitionID, acqDigest("a"), effect, closed, resolution, string(observation), ticketID, p.SubmitWorkID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var receipt struct {
					Recorded bool `json:"recorded"`
				}
				if err := json.Unmarshal(raw, &receipt); err != nil {
					t.Fatal(err)
				}
				if receipt.Recorded != (i == 0) {
					t.Fatalf("late receipt %d: %s", i, raw)
				}
			}
			if !found {
				var currentTicket string
				if err := runtimeDB.QueryRowContext(ctx, `SELECT submit_lookup_ticket->>'ticket_id' FROM download_tasks WHERE submit_permit_id='late-permit'`).Scan(&currentTicket); err != nil {
					t.Fatal(err)
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT download_record_submission_receipt($1,'late-permit','lookup-new',$2,'unknown',NULL,NULL,'{"error":"provider unavailable"}',$3,$4,2,2)`, p.AcquisitionID, acqDigest("a"), currentTicket, p.SubmitWorkID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
			}
			var newState, conclusion, afterUntil string
			var count int
			if err := runtimeDB.QueryRowContext(ctx, `SELECT submit_lookup_ticket->>'state',submit_effect_class,submit_lookup_count,submit_reconcile_until::text FROM download_tasks WHERE submit_permit_id='late-permit'`).Scan(&newState, &conclusion, &count, &afterUntil); err != nil {
				t.Fatal(err)
			}
			want := "unknown"
			if found {
				want = "confirmed"
			}
			// The per-ordinal history went with the fold: the owner row holds only the
			// current ticket, and a recorded receipt closes it.
			if newState != "completed" || conclusion != want || count != 2 || until != afterUntil {
				t.Fatalf("late result ticket=%s effect=%s count=%d until=%s", newState, conclusion, count, afterUntil)
			}
			if found {
				// The current recovery claim must consume confirmed passive evidence
				// without requiring the original mutation attempt's old token.
				for _, session := range []any{nil, "lookup-old", "wrong-session"} {
					_, err := runtimeDB.ExecContext(ctx, `SELECT download_finish_submission($1,$2,2,$3,'late-permit','downloading','{"external_id":"object-a"}',NULL,'late-sync')`, p.TaskID, p.SubmitWorkID, session)
					if err == nil {
						t.Fatalf("invalid recovery session %v accepted", session)
					}
				}
				if err := runtimeDB.QueryRowContext(ctx, `SELECT download_finish_submission($1,$2,2,'lookup-new','late-permit','downloading','{"external_id":"object-a"}',NULL,'late-sync')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
					t.Fatalf("recovery confirmation cannot continue to first sync: %v", err)
				}
				var syncCount int
				var taskState, reason, workState string
				var cancelled, blocked bool
				if err := runtimeDB.QueryRowContext(ctx, `SELECT t.state,w.state,w.cancel_requested,w.recovery_blocked,w.recovery_reason,(SELECT count(*) FROM work_item_history WHERE owner_id=t.id AND kind='qb_sync') FROM download_tasks t JOIN work_item_history w ON w.id=$2 WHERE t.id=$1`, p.TaskID, p.SubmitWorkID).Scan(&taskState, &workState, &cancelled, &blocked, &reason, &syncCount); err != nil {
					t.Fatal(err)
				}
				if cancelledClaim {
					if syncCount != 0 || taskState != "queued" || workState != "failed" || !cancelled || !blocked || reason != "cancel_requested" {
						t.Fatalf("cancelled recovery task=%s work=%s cancel=%v blocked=%v reason=%s sync=%d", taskState, workState, cancelled, blocked, reason, syncCount)
					}
					var before, after string
					const repeatSnapshot = `SELECT jsonb_build_array(to_jsonb(t),to_jsonb(w),a.facts_revision)::text FROM download_tasks t JOIN work_item_history w ON w.id=$2 JOIN acquisitions a ON a.id=t.acquisition_id WHERE t.id=$1`
					if err := runtimeDB.QueryRowContext(ctx, repeatSnapshot, p.TaskID, p.SubmitWorkID).Scan(&before); err != nil {
						t.Fatal(err)
					}
					if _, err := runtimeDB.ExecContext(ctx, `SELECT download_finish_submission($1,$2,2,'wrong-session','late-permit','downloading','{"external_id":"object-a"}',NULL,'late-sync')`, p.TaskID, p.SubmitWorkID); sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "SUBMIT_RECEIPT_SESSION" {
						t.Fatalf("cancelled repeat wrong session: %v", err)
					}
					if _, err := runtimeDB.ExecContext(ctx, `SELECT download_finish_submission($1,$2,2,'lookup-new','late-permit','downloading','{"external_id":"object-a"}',NULL,'late-sync')`, p.TaskID, p.SubmitWorkID); err != nil {
						t.Fatalf("cancelled confirmed receipt repeat rejected: %v", err)
					}
					if err := runtimeDB.QueryRowContext(ctx, repeatSnapshot, p.TaskID, p.SubmitWorkID).Scan(&after); err != nil {
						t.Fatal(err)
					}
					if before != after {
						t.Fatal("cancelled repeat changed task/work/evidence/facts")
					}
				} else if syncCount != 1 || taskState != "downloading" || workState != "succeeded" || cancelled {
					t.Fatalf("blocked recovery task=%s work=%s sync=%d", taskState, workState, syncCount)
				}
			}
		})
	}
}
