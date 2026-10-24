package migrations

import (
	"bansoko/internal/runtime/database"
	"context"
	"encoding/json"
	"testing"
)

func TestDownloadRuntimeSubmitAttemptBudget(t *testing.T) {
	fixtureDB := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, fixtureDB, "budget")
	params := validQueued(t, "budget", "budget-key", "budget")
	if _, err := callQueuedAccept(t, fixtureDB, params); err != nil {
		t.Fatal(err)
	}
	if _, err := fixtureDB.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=1,lease_until=CURRENT_TIMESTAMP+INTERVAL '1 hour' WHERE id=$1`, params.SubmitWorkID); err != nil {
		t.Fatal(err)
	}
	password := randomTestPassword(t)
	if err := database.ProvisionRuntimeRole(ctx, fixtureDB, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
		t.Fatal(err)
	}
	runtimeDB := openRuntimeConnection(t, password)
	defer runtimeDB.Close()
	for i := 0; i < 2; i++ {
		var raw []byte
		if err := runtimeDB.QueryRowContext(ctx, `SELECT public.download_start_submit_attempt($1,$2,1,'session')`, params.TaskID, params.SubmitWorkID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var result struct {
			Granted bool `json:"granted"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		if result.Granted != (i == 0) {
			t.Fatalf("attempt %d: %s", i, raw)
		}
	}
	var started int
	if err := runtimeDB.QueryRowContext(ctx, `SELECT submit_attempts_started FROM download_tasks WHERE id=$1`, params.TaskID).Scan(&started); err != nil {
		t.Fatal(err)
	}
	if started != 1 {
		t.Fatalf("attempt count=%d", started)
	}
	var dispatchRaw []byte
	if err := runtimeDB.QueryRowContext(ctx, `SELECT public.download_start_dispatch($1,$2,1,'session','permit',$3,'connection',$3,'{"operation_key":"dt-budget"}'::jsonb,'{"remote_root":"/downloads"}'::jsonb,'{"infohash_v1":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}'::jsonb)`, params.TaskID, params.SubmitWorkID, acqDigest("a")).Scan(&dispatchRaw); err != nil {
		t.Fatalf("runtime dispatch must bind the accepted task and its submit WorkItem: %v", err)
	}
	var dispatch struct {
		Granted bool `json:"granted"`
	}
	if err := json.Unmarshal(dispatchRaw, &dispatch); err != nil {
		t.Fatal(err)
	}
	if !dispatch.Granted {
		t.Fatalf("first dispatch must grant: %s", dispatchRaw)
	}
	// Reentry must never recover the same once-only dispatch grant.
	if err := runtimeDB.QueryRowContext(ctx, `SELECT public.download_start_dispatch($1,$2,1,'session','permit',$3,'connection',$3,'{"operation_key":"dt-budget"}'::jsonb,'{"remote_root":"/downloads"}'::jsonb,'{"infohash_v1":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}'::jsonb)`, params.TaskID, params.SubmitWorkID, acqDigest("a")).Scan(&dispatchRaw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(dispatchRaw, &dispatch); err != nil {
		t.Fatal(err)
	}
	if dispatch.Granted {
		t.Fatalf("duplicate dispatch granted: %s", dispatchRaw)
	}
	// Ordinary dispatch receipts must validate the original work/token identity.
	if _, err := runtimeDB.ExecContext(ctx, `SELECT download_record_submission_receipt($1,'permit','session',$2,'unknown',NULL,NULL,'{"found":false}',NULL,'wrong-work',99,NULL)`, params.AcquisitionID, acqDigest("a")); sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "SUBMIT_RECEIPT_IDENTITY" {
		t.Fatalf("ordinary receipt accepted wrong work/token: %v", err)
	}
	if _, err := runtimeDB.ExecContext(ctx, `SELECT download_record_submission_receipt($1,'permit',NULL,$2,'unknown',NULL,NULL,'{"found":false}',NULL,$3,1,NULL)`, params.AcquisitionID, acqDigest("a"), params.SubmitWorkID); sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "SUBMIT_RECEIPT_SESSION" {
		t.Fatalf("ordinary receipt must reject absent session: %v", err)
	}
	var lookupRaw []byte
	if err := runtimeDB.QueryRowContext(ctx, `SELECT public.download_start_submit_lookup($1,$2,1,'permit','lookup-session')`, params.TaskID, params.SubmitWorkID).Scan(&lookupRaw); err != nil {
		t.Fatal(err)
	}
	var lookup struct {
		Granted bool `json:"granted"`
	}
	if err := json.Unmarshal(lookupRaw, &lookup); err != nil {
		t.Fatal(err)
	}
	if !lookup.Granted {
		t.Fatalf("first lookup must grant: %s", lookupRaw)
	}
	var lookupCount int
	if err := runtimeDB.QueryRowContext(ctx, `SELECT submit_lookup_count FROM download_tasks WHERE submit_permit_id='permit'`).Scan(&lookupCount); err != nil {
		t.Fatal(err)
	}
	if lookupCount != 1 {
		t.Fatalf("lookup_count=%d", lookupCount)
	}
	// Reading the same active lookup identity must not grant another HTTP call.
	if err := runtimeDB.QueryRowContext(ctx, `SELECT public.download_start_submit_lookup($1,$2,1,'permit','lookup-session')`, params.TaskID, params.SubmitWorkID).Scan(&lookupRaw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lookupRaw, &lookup); err != nil {
		t.Fatal(err)
	}
	if lookup.Granted {
		t.Fatalf("same active lookup identity regranted: %s", lookupRaw)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT submit_lookup_count FROM download_tasks WHERE submit_permit_id='permit'`).Scan(&lookupCount); err != nil {
		t.Fatal(err)
	}
	if lookupCount != 1 {
		t.Fatalf("duplicate lookup count=%d, want 1", lookupCount)
	}
	var workRoot string
	if err := runtimeDB.QueryRowContext(ctx, `SELECT acquisition_id FROM work_item_history WHERE id=$1`, params.SubmitWorkID).Scan(&workRoot); err != nil {
		t.Fatal(err)
	}
	if workRoot != params.AcquisitionID {
		t.Fatalf("submit root=%s", workRoot)
	}
	// Constraint probes use owner; runtime cannot perform direct DML.
	if _, err := fixtureDB.ExecContext(ctx, `UPDATE work_items SET acquisition_id=NULL WHERE id=$1`, params.SubmitWorkID); err == nil {
		t.Fatal("task WorkItem NULL root unexpectedly accepted")
	}
	var successor string
	if err := runtimeDB.QueryRowContext(ctx, `SELECT public.work_item_create('sync','download_task',$1,'qb_sync',$2,3,'{"trigger":"qb_sync_continuation"}'::jsonb,1,0::double precision,7::smallint)`, params.TaskID, params.TaskIdempotency).Scan(&successor); err != nil {
		t.Fatal(err)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT acquisition_id FROM work_item_history WHERE id=$1`, successor).Scan(&workRoot); err != nil {
		t.Fatal(err)
	}
	if workRoot != params.AcquisitionID {
		t.Fatalf("successor root=%s", workRoot)
	}
	// The fold keeps only the current ticket on the owner row, so a replayed
	// receipt of the same ticket is a no-op that moves neither the budget count
	// nor the reconciliation clock.
	var beforeCount int
	var beforeUntil string
	if err := runtimeDB.QueryRowContext(ctx, `SELECT submit_lookup_count,submit_reconcile_until::text FROM download_tasks WHERE submit_permit_id='permit'`).Scan(&beforeCount, &beforeUntil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		var receipt []byte
		if err := runtimeDB.QueryRowContext(ctx, `SELECT public.download_record_submission_receipt($1,'permit','lookup-session',$2,'unknown',NULL,NULL,'{"found":false}'::jsonb,(SELECT submit_lookup_ticket->>'ticket_id' FROM download_tasks WHERE submit_permit_id='permit'),$3,1,1)`, params.AcquisitionID, acqDigest("a"), params.SubmitWorkID).Scan(&receipt); err != nil {
			t.Fatal(err)
		}
		var result struct {
			Recorded bool `json:"recorded"`
		}
		if err := json.Unmarshal(receipt, &result); err != nil {
			t.Fatal(err)
		}
		if result.Recorded != (i == 0) {
			t.Fatalf("receipt %d: %s", i, receipt)
		}
	}
	var afterCount int
	var afterUntil, appliedDigest string
	if err := runtimeDB.QueryRowContext(ctx, `SELECT submit_lookup_count,submit_reconcile_until::text,submit_lookup_ticket->>'result_digest' FROM download_tasks WHERE submit_permit_id='permit'`).Scan(&afterCount, &afterUntil, &appliedDigest); err != nil {
		t.Fatal(err)
	}
	if afterCount != beforeCount || afterUntil != beforeUntil || appliedDigest == "" {
		t.Fatalf("receipt application count=%d->%d until=%s->%s digest=%q", beforeCount, afterCount, beforeUntil, afterUntil, appliedDigest)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT public.download_start_submit_lookup($1,$2,1,'permit','lookup-session-next')`, params.TaskID, params.SubmitWorkID).Scan(&lookupRaw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lookupRaw, &lookup); err != nil {
		t.Fatal(err)
	}
	if !lookup.Granted {
		t.Fatalf("completed previous ticket must permit next lookup: %s", lookupRaw)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT submit_lookup_count,submit_reconcile_until::text FROM download_tasks WHERE submit_permit_id='permit'`).Scan(&lookupCount, &afterUntil); err != nil {
		t.Fatal(err)
	}
	if lookupCount != 2 || afterUntil != beforeUntil {
		t.Fatalf("next lookup count/clock=%d/%s", lookupCount, afterUntil)
	}
	// Conflicting completed receipts preserve the current ticket's original
	// result instead of overwriting it.
	var applied []byte
	if err := runtimeDB.QueryRowContext(ctx, `SELECT public.download_record_submission_receipt($1,'permit','lookup-session-next',$2,'unknown',NULL,NULL,'{"found":false}'::jsonb,(SELECT submit_lookup_ticket->>'ticket_id' FROM download_tasks WHERE submit_permit_id='permit'),$3,1,2)`, params.AcquisitionID, acqDigest("a"), params.SubmitWorkID).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	var originalDigest, preservedDigest string
	if err := runtimeDB.QueryRowContext(ctx, `SELECT submit_lookup_ticket->>'result_digest' FROM download_tasks WHERE submit_permit_id='permit'`).Scan(&originalDigest); err != nil {
		t.Fatal(err)
	}
	var conflictRaw []byte
	if err := runtimeDB.QueryRowContext(ctx, `SELECT public.download_record_submission_receipt($1,'permit','lookup-session-next',$2,'unknown',NULL,NULL,'{"found":false,"changed":true}'::jsonb,(SELECT submit_lookup_ticket->>'ticket_id' FROM download_tasks WHERE submit_permit_id='permit'),$3,1,2)`, params.AcquisitionID, acqDigest("a"), params.SubmitWorkID).Scan(&conflictRaw); err != nil {
		t.Fatal(err)
	}
	var conflict struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(conflictRaw, &conflict); err != nil {
		t.Fatal(err)
	}
	if conflict.Reason != "SUBMIT_RECEIPT_CONFLICT" {
		t.Fatalf("conflict result: %s", conflictRaw)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT submit_lookup_ticket->>'result_digest' FROM download_tasks WHERE submit_permit_id='permit'`).Scan(&preservedDigest); err != nil {
		t.Fatal(err)
	}
	if originalDigest != preservedDigest {
		t.Fatal("conflict overwrote completed result")
	}
	// The runtime role holds no INSERT/UPDATE privilege on the proof columns,
	// so the lookup log cannot be rewritten directly.
	if _, err := runtimeDB.ExecContext(ctx, `UPDATE download_tasks SET submit_lookup_ticket=jsonb_set(submit_lookup_ticket,'{state}','"superseded"') WHERE submit_permit_id='permit'`); sqlState(err) != "42501" {
		t.Fatalf("runtime lookup-log write: %v", err)
	}
	if _, err := runtimeDB.ExecContext(ctx, `SELECT public.download_record_submission_receipt($1,'permit','lookup-session',$2,'unknown',NULL,NULL,'{}','unknown-ticket',$3,1,1)`, params.AcquisitionID, acqDigest("a"), params.SubmitWorkID); sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "SUBMIT_RECEIPT_LOOKUP_TICKET" {
		t.Fatalf("unknown ticket: %v", err)
	}
	// A real historical ticket does not authorize another identity.
	for _, probe := range []struct {
		session, work  string
		token, ordinal int
	}{
		{"wrong-session", params.SubmitWorkID, 1, 1},
		{"lookup-session", "wrong-work", 1, 1},
		{"lookup-session", params.SubmitWorkID, 2, 1},
		{"lookup-session", params.SubmitWorkID, 1, 2},
	} {
		if _, err := runtimeDB.ExecContext(ctx, `SELECT download_record_submission_receipt($1,'permit',$2,$3,'unknown',NULL,NULL,'{}',(SELECT submit_lookup_ticket->>'ticket_id' FROM download_tasks WHERE submit_permit_id='permit'),$4,$5,$6)`, params.AcquisitionID, probe.session, acqDigest("a"), probe.work, probe.token, probe.ordinal); sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "SUBMIT_RECEIPT_LOOKUP_TICKET" {
			t.Fatalf("ticket identity mismatch: %v", err)
		}
	}
	var maxLookups int
	if err := runtimeDB.QueryRowContext(ctx, `SELECT submit_max_lookups FROM download_tasks WHERE submit_permit_id='permit'`).Scan(&maxLookups); err != nil {
		t.Fatal(err)
	}
	for ordinal := 2; ordinal <= maxLookups; ordinal++ {
		session := "lookup-capacity"
		if ordinal == 2 {
			session = "lookup-session-next"
		}
		var receipt []byte
		if err := runtimeDB.QueryRowContext(ctx, `SELECT download_record_submission_receipt($1,'permit',$2,$3,'unknown',NULL,NULL,'{"found":false}',(SELECT submit_lookup_ticket->>'ticket_id' FROM download_tasks WHERE submit_permit_id='permit'),$4,1,$5)`, params.AcquisitionID, session, acqDigest("a"), params.SubmitWorkID, ordinal).Scan(&receipt); err != nil {
			t.Fatal(err)
		}
		if err := runtimeDB.QueryRowContext(ctx, `SELECT public.download_start_submit_lookup($1,$2,1,'permit','lookup-capacity')`, params.TaskID, params.SubmitWorkID).Scan(&lookupRaw); err != nil {
			t.Fatal(err)
		}
		var grant struct {
			Granted bool   `json:"granted"`
			Reason  string `json:"reason"`
		}
		if err := json.Unmarshal(lookupRaw, &grant); err != nil {
			t.Fatal(err)
		}
		if grant.Granted != (ordinal < maxLookups) {
			t.Fatalf("lookup limit %d/%d: %s", ordinal, maxLookups, lookupRaw)
		}
		if ordinal == maxLookups && grant.Reason != "SUBMIT_RECONCILE_EXHAUSTED" {
			t.Fatalf("lookup limit reason: %s", lookupRaw)
		}
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT submit_lookup_count FROM download_tasks WHERE submit_permit_id='permit'`).Scan(&lookupCount); err != nil {
		t.Fatal(err)
	}
	if lookupCount != maxLookups {
		t.Fatalf("lookup capacity=%d want %d", lookupCount, maxLookups)
	}
	// Count exhaustion (proved above) already blocked the work item. The slot keeps
	// its unknown window immutable, so the wall half cannot be replayed by moving
	// reconcile_until backwards; the block must persist for the read side.
	var blocked bool
	var reason string
	if err := runtimeDB.QueryRowContext(ctx, `SELECT recovery_blocked,recovery_reason FROM work_item_history WHERE id=$1`, params.SubmitWorkID).Scan(&blocked, &reason); err != nil {
		t.Fatal(err)
	}
	if !blocked || reason != "SUBMIT_RECONCILE_EXHAUSTED" {
		t.Fatalf("exhaustion blocked=%v reason=%s", blocked, reason)
	}
	for _, probe := range []struct{ spec, effect, ticket any }{
		{nil, "unknown", nil},
		{acqDigest("a"), nil, nil},
		{acqDigest("a"), "unknown", "unexpected-ticket"},
	} {
		if _, err := runtimeDB.ExecContext(ctx, `SELECT download_record_submission_receipt($1,'permit','session',$2,$3,NULL,NULL,'{"found":false}',$4,$5,1,NULL)`, params.AcquisitionID, probe.spec, probe.effect, probe.ticket, params.SubmitWorkID); sqlState(err) != "23514" || adoptAcceptPGMessage(err) != "SUBMIT_RECEIPT_IDENTITY" {
			t.Fatalf("ordinary receipt identity spec=%v effect=%v ticket=%v: %v", probe.spec, probe.effect, probe.ticket, err)
		}
	}
}
