package migrations

import (
	"context"
	"testing"
	"time"

	download "bansoko/internal/download/application"
	"bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/runtime/database"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

type typedSyncProvider struct{ calls int }

func (p *typedSyncProvider) LookupByOperationKey(context.Context, string) (domain.ProviderTask, bool, error) {
	panic("sync must not lookup submit")
}
func (p *typedSyncProvider) Sync(context.Context, string) (domain.ProviderTask, error) {
	p.calls++
	return domain.ProviderTask{ID: "object-a", State: domain.TaskCompleted}, nil
}
func (*typedSyncProvider) ClassifyProviderError(error) domain.ProviderErrorClass {
	return domain.ProviderErrorUnknown
}

type typedSyncConfig struct{}

func (typedSyncConfig) GetQBittorrentRuntimeConfig(context.Context) (qbittorrent.RuntimeConfig, error) {
	return qbittorrent.RuntimeConfig{ConnectionID: "connection", SaveRoot: "/downloads"}, nil
}

func TestDownloadTypedSyncHandlerCommitsOnce(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, owner, "typed-sync-handler")
	p := validQueued(t, "typed-sync-handler", "typed-sync-handler-key", "typed-sync-handler")
	if _, err := callQueuedAccept(t, owner, p); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET state='leased',fencing_token=1,lease_until=clock_timestamp()+INTERVAL '1 hour' WHERE id=$1`, p.SubmitWorkID); err != nil {
		t.Fatal(err)
	}
	password := randomTestPassword(t)
	if err := database.ProvisionRuntimeRole(ctx, owner, database.RuntimeRoleConfig{Role: runtimeRoleTestName, Password: password}); err != nil {
		t.Fatal(err)
	}
	runtimeDB := openRuntimeConnection(t, password)
	defer runtimeDB.Close()
	var raw []byte
	for _, call := range []struct {
		sql  string
		args []any
	}{
		{`SELECT download_start_submit_attempt($1,$2,1,'handler-submit')`, []any{p.TaskID, p.SubmitWorkID}},
		{`SELECT download_start_dispatch($1,$2,1,'handler-submit','handler-permit',$3,'connection',$3,'{"key":"handler"}','{"root":"/downloads"}','{"hash":"a"}')`, []any{p.TaskID, p.SubmitWorkID, acqDigest("a")}},
		{`SELECT download_record_submission_receipt($1,'handler-permit','handler-submit',$2,'confirmed','{"kind":"channel_closed"}','{"kind":"same_key_found"}','{"found":true}',NULL,$3,1,NULL)`, []any{p.AcquisitionID, acqDigest("a"), p.SubmitWorkID}},
		{`SELECT download_finish_submission($1,$2,1,'handler-submit','handler-permit','downloading','{"external_id":"object-a"}',NULL,'handler-sync')`, []any{p.TaskID, p.SubmitWorkID}},
	} {
		if err := runtimeDB.QueryRowContext(ctx, call.sql, call.args...).Scan(&raw); err != nil {
			t.Fatal(err)
		}
	}
	// Move only the ClaimDue schedule for this test; no budget clock mutation.
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET available_at=clock_timestamp() WHERE id='handler-sync'`); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(runtimeDB, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	provider := &typedSyncProvider{}
	service, err := download.NewService(runtimeDB, store, provider, typedSyncConfig{})
	if err != nil {
		t.Fatal(err)
	}
	w := worker.NewWorker(store, "typed-sync-handler", time.Minute)
	if err := w.Register(workitems.KindQBSync, service.SyncHandler()); err != nil {
		t.Fatal(err)
	}
	handled, err := w.RunOnce(ctx)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	var state, workState string
	var polls, artifacts, inspect int
	if err := runtimeDB.QueryRowContext(ctx, `SELECT t.state,w.state,t.polls_started,(SELECT count(*) FROM artifacts WHERE download_task_id=t.id),(SELECT count(*) FROM work_item_history wi JOIN artifacts a ON wi.owner_id=a.id WHERE a.download_task_id=t.id AND wi.kind='artifact_inspect') FROM download_tasks t JOIN work_item_history w ON w.id='handler-sync' WHERE t.id=$1`, p.TaskID).Scan(&state, &workState, &polls, &artifacts, &inspect); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || workState != "succeeded" || polls != 1 || artifacts != 1 || inspect != 1 || provider.calls != 1 {
		t.Fatalf("state=%s work=%s polls=%d artifacts=%d inspect=%d calls=%d", state, workState, polls, artifacts, inspect, provider.calls)
	}
	// A terminal StartSync performs no provider I/O but must settle admission.
	if _, err := owner.ExecContext(ctx, `INSERT INTO one_shot_acquisitions(id,managed_anime_id,subscription_id,rss_source_id,release_id,download_task_id,correlation_id,request_idempotency_key,request_hash,result_snapshot,state) SELECT 'terminal-sync-marker',s.managed_anime_id,s.id,r.rss_source_id,r.id,$1,'terminal-corr','terminal-request',$2,'{}','running' FROM releases r JOIN rss_sources src ON src.id=r.rss_source_id JOIN subscriptions s ON s.id=src.subscription_id WHERE r.download_task_id=$1`, p.TaskID, acqDigest("a")); err != nil {
		t.Fatal(err)
	}
	ref := workitems.OneShotRef{MarkerID: "terminal-sync-marker", CorrelationID: "terminal-corr", AdmissionEpoch: 1}
	terminalInput, err := workitems.MergeOneShotInput([]byte(`{}`), ref)
	if err != nil {
		t.Fatal(err)
	}
	var terminalWork string
	if err := runtimeDB.QueryRowContext(ctx, `SELECT work_item_create('terminal-handler-sync','download_task',$1,'qb_sync',$2,5,$3,3,0::double precision,7::smallint)`, p.TaskID, p.TaskIdempotency, []byte(terminalInput)).Scan(&terminalWork); err != nil {
		t.Fatal(err)
	}
	terminalClaim, err := store.ClaimDue(ctx, "terminal-sync-handler", []string{workitems.KindQBSync}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitOneShotEffect(ctx, terminalClaim); err != nil {
		t.Fatal(err)
	}
	terminalOutcome, err := service.SyncHandler().Handle(ctx, terminalClaim)
	if err != nil {
		t.Fatal(err)
	}
	var admission int
	if err := runtimeDB.QueryRowContext(ctx, `SELECT active_effect_count FROM one_shot_acquisitions WHERE id='terminal-sync-marker'`).Scan(&admission); err != nil {
		t.Fatal(err)
	}
	if terminalOutcome.Kind != worker.OutcomeCommitted || admission != 0 || provider.calls != 1 {
		t.Fatalf("terminal sync outcome=%s admission=%d calls=%d", terminalOutcome.Kind, admission, provider.calls)
	}
}
