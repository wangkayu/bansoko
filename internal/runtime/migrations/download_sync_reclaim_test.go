package migrations

import (
	"bansoko/internal/runtime/database"
	"context"
	"testing"
)

func TestDownloadSyncReclaimedPollClosesCrash(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, owner, "sync-crash")
	p := validQueued(t, "sync-crash", "sync-crash-key", "sync-crash")
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
	if err := runtimeDB.QueryRowContext(ctx, `SELECT work_item_create('crash-sync','download_task',$1,'qb_sync',$2,5,'{"poll_ordinal":1}',1,0::double precision,7::smallint)`, p.TaskID, p.TaskIdempotency).Scan(&work); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=1,lease_until=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, work); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_sync($1,$2,1,1,'sync-original')`, p.TaskID, work).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET fencing_token=2 WHERE id=$1`, work); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `INSERT INTO one_shot_acquisitions(id,managed_anime_id,subscription_id,rss_source_id,release_id,download_task_id,correlation_id,request_idempotency_key,request_hash,result_snapshot,state,admission_epoch) VALUES('crash-marker','anime-sync-crash','subscription-sync-crash','rss-sync-crash',$1,$2,'crash-corr','crash-marker-key',$3,'{}','running',2)`, p.SourceReleaseID, p.TaskID, acqDigest("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET input_snapshot=input_snapshot||'{"one_shot":{"marker_id":"crash-marker","correlation_id":"crash-corr","admission_epoch":1}}'::jsonb WHERE id=$1`, work); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeDB.ExecContext(ctx, `SELECT download_start_sync($1,$2,2,1,'sync-reclaimed')`, p.TaskID, work); err == nil {
		t.Fatal("crash successor bypassed stale one-shot epoch fence")
	}
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET input_snapshot=jsonb_set(input_snapshot,'{one_shot,admission_epoch}','2') WHERE id=$1`, work); err != nil {
		t.Fatal(err)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_sync($1,$2,2,1,'sync-reclaimed')`, p.TaskID, work).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var errors int
	var ticket string
	if err := runtimeDB.QueryRowContext(ctx, `SELECT total_errors,active_poll_ticket->>'state' FROM download_tasks WHERE id=$1`, p.TaskID).Scan(&errors, &ticket); err != nil {
		t.Fatal(err)
	}
	if errors != 1 || ticket != "superseded" {
		t.Fatalf("reclaimed poll did not close crash: errors=%d ticket=%s result=%s", errors, ticket, raw)
	}
	var successorEpoch int
	if err := runtimeDB.QueryRowContext(ctx, `SELECT (input_snapshot->'one_shot'->>'admission_epoch')::integer FROM work_items WHERE owner_id=$1 AND kind='qb_sync' AND state='available'`, p.TaskID).Scan(&successorEpoch); err != nil {
		t.Fatal(err)
	}
	if successorEpoch != 2 {
		t.Fatalf("successor epoch=%d", successorEpoch)
	}
	var consecutive, polls, ordinal, nextCount int
	var state string
	if err := runtimeDB.QueryRowContext(ctx, `SELECT consecutive_errors,polls_started,next_poll_ordinal FROM download_tasks WHERE id=$1`, p.TaskID).Scan(&consecutive, &polls, &ordinal); err != nil {
		t.Fatal(err)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT state FROM work_item_history WHERE id=$1`, work).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT count(*) FROM work_items WHERE owner_id=$1 AND kind='qb_sync' AND state='available' AND input_snapshot->>'poll_ordinal'='2'`, p.TaskID).Scan(&nextCount); err != nil {
		t.Fatal(err)
	}
	if consecutive != 1 || polls != 1 || ordinal != 2 || state != "failed" || nextCount != 1 {
		t.Fatalf("crash closure consecutive=%d polls=%d ordinal=%d state=%s next=%d", consecutive, polls, ordinal, state, nextCount)
	}
	// A replay cannot regain observation rights or count the crash again.
	if _, err := runtimeDB.ExecContext(ctx, `SELECT download_start_sync($1,$2,2,1,'sync-reclaimed')`, p.TaskID, work); err == nil {
		t.Fatal("terminal old poll regained grant")
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT total_errors FROM download_tasks WHERE id=$1`, p.TaskID).Scan(&errors); err != nil {
		t.Fatal(err)
	}
	if errors != 1 {
		t.Fatalf("crash counted twice: %d", errors)
	}
}
