package migrations

import (
	"bansoko/internal/persistence/txscope"
	plugintargetport "bansoko/internal/plugintarget/port"
	"context"
	"errors"
	"testing"
	"time"

	download "bansoko/internal/download/application"
	"bansoko/internal/download/domain"
	"bansoko/internal/runtime/database"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

type typedRecoveryProvider struct {
	calls int
	found bool
}

func (p *typedRecoveryProvider) LookupByOperationKey(context.Context, string) (domain.ProviderTask, bool, error) {
	p.calls++
	return domain.ProviderTask{ID: "object-a", State: domain.TaskDownloading}, p.found, nil
}
func (*typedRecoveryProvider) Sync(context.Context, string) (domain.ProviderTask, error) {
	panic("submit recovery must not sync")
}
func (*typedRecoveryProvider) ClassifyProviderError(error) domain.ProviderErrorClass {
	return domain.ProviderErrorUnknown
}

type failingRecoveryAdmission struct{}

func (failingRecoveryAdmission) EnqueueBusinessDelivery(context.Context, txscope.Tx, plugintargetport.BusinessDeliveryRequest) error {
	return errors.New("injected notification admission failure")
}

func TestDownloadTypedSubmitRecoveryHandler(t *testing.T) {
	owner := installedDB(t)
	ctx := context.Background()
	seedAcceptableRelease(t, owner, "typed-recovery")
	p := validQueued(t, "typed-recovery", "typed-recovery-key", "typed-recovery")
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
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_submit_attempt($1,$2,1,'recovery-submit')`, p.TaskID, p.SubmitWorkID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := runtimeDB.QueryRowContext(ctx, `SELECT download_start_dispatch($1,$2,1,'recovery-submit','recovery-permit',$3,'connection',$3,'{"key":"recovery"}','{"remote_root":"/downloads","application_root":"/downloads","path_mappings":[]}','{"hash":"a"}')`, p.TaskID, p.SubmitWorkID, acqDigest("a")).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET recovery_blocked=true,recovery_reason='effect_unknown',lease_until=clock_timestamp()-INTERVAL '1 second' WHERE id=$1`, p.SubmitWorkID); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(runtimeDB, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimRecovery(ctx, []string{workitems.KindQBSubmit}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	provider := &typedRecoveryProvider{}
	service, err := download.NewService(runtimeDB, store, provider, typedSyncConfig{})
	if err != nil {
		t.Fatal(err)
	}
	observer := service.SubmitHandler().(worker.RecoveryObserver)
	first, err := observer.ObserveRecovery(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if first.Disposition != workitems.DispositionAmbiguous {
		t.Fatalf("not found disposition=%s", first.Disposition)
	}
	provider.found = true
	service.SetBusinessDeliveryEnqueuer(failingRecoveryAdmission{})
	if _, err := observer.ObserveRecovery(ctx, claim); err == nil {
		t.Fatal("notification failure did not roll back the typed submit recovery")
	}
	service.SetBusinessDeliveryEnqueuer(nil)
	if _, err := owner.ExecContext(ctx, `UPDATE work_items SET lease_until=clock_timestamp()-INTERVAL '1 second' WHERE id=$1`, claim.ID); err != nil {
		t.Fatal(err)
	}
	claim, err = store.ClaimRecovery(ctx, []string{workitems.KindQBSubmit}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var originalSession string
	var savedCorrelation []byte
	if err := runtimeDB.QueryRowContext(ctx, `SELECT t.submit_lookup_ticket->>'executor_session',t.submit_observation->'correlation' FROM download_tasks t WHERE t.submit_permit_id='recovery-permit' AND t.submit_lookup_ticket->>'result_kind'='found'`).Scan(&originalSession, &savedCorrelation); err != nil {
		t.Fatal(err)
	}
	for _, probe := range []struct {
		session     string
		correlation []byte
	}{
		{"wrong-session", savedCorrelation},
		{originalSession, []byte(`{"external_id":"wrong-object","state":"downloading"}`)},
	} {
		_, err := runtimeDB.ExecContext(ctx, `SELECT download_finish_submission($1,$2,$3,$4,'recovery-permit','downloading',$5,NULL,'forbidden-adoption-sync')`, p.TaskID, claim.ID, claim.FencingToken, probe.session, probe.correlation)
		if sqlState(err) != "23514" {
			t.Fatalf("forged evidence adoption accepted: %v", err)
		}
	}
	second, err := observer.ObserveRecovery(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if second.Kind != worker.OutcomeCommitted {
		t.Fatalf("found outcome=%s", second.Kind)
	}
	if _, err := runtimeDB.ExecContext(ctx, `SELECT download_finish_submission($1,$2,$3,$4,'recovery-permit','downloading',$5,NULL,'repeat-adoption-sync')`, p.TaskID, claim.ID, claim.FencingToken, originalSession, savedCorrelation); err != nil {
		t.Fatalf("committed adoption receipt repeat rejected: %v", err)
	}
	for _, probe := range []struct {
		session     string
		correlation []byte
	}{
		{"wrong-session", savedCorrelation},
		{originalSession, []byte(`{"external_id":"wrong-object","state":"downloading"}`)},
	} {
		_, err := runtimeDB.ExecContext(ctx, `SELECT download_finish_submission($1,$2,$3,$4,'recovery-permit','downloading',$5,NULL,'forged-repeat-sync')`, p.TaskID, claim.ID, claim.FencingToken, probe.session, probe.correlation)
		if sqlState(err) != "23514" {
			t.Fatalf("forged completed adoption accepted: %v", err)
		}
	}
	var state, workState, effect string
	var lookups, syncCount int
	if err := runtimeDB.QueryRowContext(ctx, `SELECT t.state,w.state,t.submit_effect_class,t.submit_lookup_count,(SELECT count(*) FROM work_item_history wi WHERE wi.owner_id=t.id AND wi.kind='qb_sync') FROM download_tasks t JOIN work_item_history w ON w.id=$2 WHERE t.id=$1`, p.TaskID, claim.ID).Scan(&state, &workState, &effect, &lookups, &syncCount); err != nil {
		t.Fatal(err)
	}
	if state != "downloading" || workState != "succeeded" || effect != "confirmed" || lookups != 2 || syncCount != 1 || provider.calls != 2 {
		t.Fatalf("task=%s work=%s effect=%s lookup=%d sync=%d calls=%d", state, workState, effect, lookups, syncCount, provider.calls)
	}
}
