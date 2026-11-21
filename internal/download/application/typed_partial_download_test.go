package application

import (
	"context"
	"encoding/json"
	"testing"

	"bansoko/internal/workitems"
)

func TestOneShotPartialWriteRequiresTheCurrentPollAndUnsentStart(t *testing.T) {
	ctx := context.Background()
	db := openDownloadDB(t)
	const task = "task-qb-partial"
	seedDownloadTask(t, db, task, "downloading", `{"external_id":"tag:bansoko-qb:release-qb-test"}`)
	seedOneShotMarker(t, db, "marker-"+task, task)
	if _, err := ownerTestDB(t).ExecContext(ctx, `UPDATE download_tasks SET frozen_input=frozen_input||'{"partial_download":{"paths":["Pack/E01.mkv"],"file_count":2}}' WHERE id=$1`, task); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	claim := createClaimedOneShotQBSyncItem(t, db, store, task)
	var ordinal int
	if err := db.QueryRowContext(ctx, `SELECT next_poll_ordinal FROM download_tasks WHERE id=$1`, task).Scan(&ordinal); err != nil {
		t.Fatal(err)
	}
	const session = "partial-poll"
	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT download_start_sync($1,$2,$3,$4,$5)`, task, claim.ID, claim.FencingToken, ordinal, session).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var gate stageReply
	if err := json.Unmarshal(raw, &gate); err != nil || !gate.Granted {
		t.Fatalf("start poll: %s, %v", raw, err)
	}
	authorize := func(token int64, session string, start bool) (stageReply, error) {
		var raw []byte
		var reply stageReply
		if err := db.QueryRowContext(ctx, `SELECT download_authorize_partial_write($1,$2,$3,$4,$5,$6)`, task, claim.ID, token, session, ordinal, start).Scan(&raw); err != nil {
			return reply, err
		}
		return reply, json.Unmarshal(raw, &reply)
	}
	if _, err := authorize(claim.FencingToken+1, session, false); err == nil {
		t.Fatal("stale claim could write priorities")
	}
	if _, err := authorize(claim.FencingToken, "another-poll", true); err == nil {
		t.Fatal("a different poll could start")
	}
	if reply, err := authorize(claim.FencingToken, session, false); err != nil || !reply.Granted {
		t.Fatalf("authorize priorities: %+v, %v", reply, err)
	}
	owner := ownerTestDB(t)
	if _, err := owner.ExecContext(ctx, `INSERT INTO acquisition_cancel_requests(acquisition_id,idempotency_key,request_hash,result_version,accepted_at) VALUES($1,'cancel-fixture','sha256:'||repeat('a',64),1,clock_timestamp())`, "acq-"+task); err != nil {
		t.Fatal(err)
	}
	if reply, err := authorize(claim.FencingToken, session, true); err != nil || reply.Granted {
		t.Fatalf("authorize cancelled start: %+v, %v", reply, err)
	}
	if _, err := owner.ExecContext(ctx, `DELETE FROM acquisition_cancel_requests WHERE acquisition_id=$1`, "acq-"+task); err != nil {
		t.Fatal(err)
	}
	if reply, err := authorize(claim.FencingToken, session, true); err != nil || !reply.Granted {
		t.Fatalf("authorize start: %+v, %v", reply, err)
	}
	var sent bool
	if err := db.QueryRowContext(ctx, `SELECT (frozen_input->>'partial_start_sent')::boolean FROM download_tasks WHERE id=$1`, task).Scan(&sent); err != nil || !sent {
		t.Fatalf("durable start marker: %v, %v", sent, err)
	}
	if _, err := authorize(claim.FencingToken, session, true); err == nil {
		t.Fatal("start could be sent twice")
	}
}
