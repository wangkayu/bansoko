package application

import (
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"testing"
	"time"
)

func TestPGQBSyncRecoveryClosesIssuedPollWithoutProviderIdentityLookup(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-recovery", "downloading", `{"external_id":"tag:qb:release-qb-test"}`)
	if _, err := db.Exec(`UPDATE download_tasks SET frozen_input=frozen_input-'path_snapshot' WHERE id='task-recovery'`); err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{}
	service, store := newDownloadService(t, db, provider, &fakeRuntimeConfig{})
	claim := createClaimedQBWorkItem(t, db, store, "task-recovery", workitems.KindQBSync)
	ctx := context.Background()
	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT download_start_sync($1,$2,$3,1,'interrupted-sync')`, claim.Owner.ID, claim.ID, claim.FencingToken).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerTestDB(t).ExecContext(ctx, `UPDATE work_items SET recovery_blocked=true,recovery_reason='effect_unknown',lease_until=statement_timestamp()-INTERVAL '1 second' WHERE id=$1`, claim.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimRecovery(ctx, []string{workitems.KindQBSync}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	observer, ok := service.SyncHandler().(worker.RecoveryObserver)
	if !ok {
		t.Fatal("production registry handler lacks observer")
	}
	o, err := observer.ObserveRecovery(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if o.Kind != worker.OutcomeCommitted || provider.syncCalls != 0 || provider.lookupCalls != 0 || provider.fileSubmitCalls != 0 {
		t.Fatalf("recovery performed fresh observation: %+v provider=%+v", o, provider)
	}
	var workState, ticket string
	if err := db.QueryRowContext(ctx, `SELECT w.state,t.active_poll_ticket->>'state' FROM work_item_history w JOIN download_tasks t ON t.id=w.owner_id WHERE w.id=$1`, claim.ID).Scan(&workState, &ticket); err != nil {
		t.Fatal(err)
	}
	if workState != "failed" || ticket != "superseded" || countAvailableQBSyncItems(t, db, claim.Owner.ID) != 1 {
		t.Fatalf("unclosed poll work=%s ticket=%s", workState, ticket)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM download_tasks WHERE id='task-recovery'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "downloading" {
		t.Fatalf("owner rewritten: %s", state)
	}
}
