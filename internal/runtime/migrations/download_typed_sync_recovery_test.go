package migrations

import (
	download "bansoko/internal/download/application"
	"bansoko/internal/runtime/database"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
	"context"
	"testing"
	"time"
)

func TestDownloadTypedSyncRecoveryHandler(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "interrupted", true: "cancelled"}[cancel], func(t *testing.T) {
			ctx := context.Background()
			owner := installedDB(t)
			seedAcceptableRelease(t, owner, "sync-recovery")
			p := validQueued(t, "sync-recovery", "sync-recovery-key", "sync-recovery")
			if _, err := callQueuedAccept(t, owner, p); err != nil {
				t.Fatal(err)
			}
			password := randomTestPassword(t)
			if err := database.ProvisionRuntimeRole(ctx, owner, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
				t.Fatal(err)
			}
			db := openRuntimeConnection(t, password)
			defer db.Close()
			var work string
			var raw []byte
			if err := db.QueryRowContext(ctx, `SELECT work_item_create('recovery-sync','download_task',$1,'qb_sync',$2,5,'{"poll_ordinal":1}',1,0::double precision,7::smallint)`, p.TaskID, p.TaskIdempotency).Scan(&work); err != nil {
				t.Fatal(err)
			}
			if _, err := owner.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=1,lease_until=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, work); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRowContext(ctx, `SELECT download_start_sync($1,$2,1,1,'original-poll')`, p.TaskID, work).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if _, err := owner.ExecContext(ctx, `UPDATE work_items SET recovery_blocked=true,recovery_reason=CASE WHEN $2 THEN 'cancel_requested' ELSE 'effect_unknown' END,cancel_requested=$2,lease_until=clock_timestamp()-INTERVAL '1 second' WHERE id=$1`, work, cancel); err != nil {
				t.Fatal(err)
			}
			store, err := workitems.NewStore(db, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := store.ClaimRecovery(ctx, []string{workitems.KindQBSync}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			provider := &typedRecoveryProvider{}
			service, err := download.NewService(db, store, provider, typedSyncConfig{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.SyncHandler().(worker.RecoveryObserver).ObserveRecovery(ctx, claim)
			if err != nil {
				t.Fatal(err)
			}
			if result.Kind != worker.OutcomeCommitted {
				t.Fatalf("outcome=%s", result.Kind)
			}
			var count, errors int
			var state, ticket string
			if err := db.QueryRowContext(ctx, `SELECT w.state,t.active_poll_ticket->>'state',t.total_errors,(SELECT count(*) FROM work_items n WHERE n.owner_id=t.id AND n.kind='qb_sync' AND n.state='available') FROM work_item_history w JOIN download_tasks t ON t.id=w.owner_id WHERE w.id=$1`, claim.ID).Scan(&state, &ticket, &errors, &count); err != nil {
				t.Fatal(err)
			}
			want := 1
			if cancel {
				want = 0
			}
			if state != "failed" || ticket != "superseded" || count != want || errors != want || provider.calls != 0 {
				t.Fatalf("work=%s ticket=%s errors=%d successor=%d calls=%d", state, ticket, errors, count, provider.calls)
			}
		})
	}
}
