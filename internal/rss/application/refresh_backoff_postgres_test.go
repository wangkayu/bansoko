package application

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/runtime/worker"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/workitems"
)

// TestRefreshBackoffAndProviderCircuit proves repeated fetch failures back off
// 5/10/20/40 minutes per source, trip the provider circuit at the threshold
// (with a provider-scoped diagnostic), and one success resets both.
func TestRefreshBackoffAndProviderCircuit(t *testing.T) {
	db := openRSSDatabase(t)
	ctx := context.Background()
	sourceID, subscriptionID := seedRSSOwner(t, db, false, true)
	if _, err := db.ExecContext(ctx, `
UPDATE rss_sources SET source_kind = 'provider_group', provider = 'anibt', provider_entry_ref = '123',
       provider_group_ref = 'alpha', discovery_snapshot_version = 'v1'
WHERE id = $1`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO provider_group_selections(id, subscription_id, provider, group_ref, display_name, selection_mode, manual_selected, effective_selected, effective_score)
VALUES ('selection-backoff', $1, 'anibt', 'alpha', 'Group', 'auto', NULL, TRUE, 50)`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &rssFakeSourceFetcher{err: errors.New("upstream down")}
	service, err := newRSSService(t, db, store, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	service.SetDiagnosticRecorder(recorder, nil)
	owner := workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}
	// The runtime role cannot delete WorkItems; test cleanup uses the owner.
	schemaOwner := ownerTestDB(t)
	runtime := func() (version int64, failures int) {
		if err := db.QueryRowContext(ctx, `SELECT version, consecutive_failures FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).Scan(&version, &failures); err != nil {
			t.Fatal(err)
		}
		return version, failures
	}
	circuit := func() (failures int, paused bool) {
		err := db.QueryRowContext(ctx, `SELECT consecutive_failures, COALESCE(paused_until > CURRENT_TIMESTAMP + INTERVAL '50 minutes', FALSE) FROM rss_provider_circuits WHERE provider = 'anibt'`).Scan(&failures, &paused)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false
		}
		if err != nil {
			t.Fatal(err)
		}
		return failures, paused
	}
	pauseEpisodeOpen := func() bool {
		var open bool
		err := db.QueryRowContext(ctx, `SELECT succeeded_after_at IS NULL FROM diagnostic_episodes WHERE error_code = 'rss_provider_paused' AND scope_kind = 'provider' AND scope_id = 'anibt'`).Scan(&open)
		if errors.Is(err, sql.ErrNoRows) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		return open
	}

	wantDelays := []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute, 80 * time.Minute}
	for i, want := range wantDelays {
		version, _ := runtime()
		claim := createRSSClaim(t, db, store, owner, workitems.KindRSSRefresh, version)
		outcome, err := service.Handle(ctx, claim)
		if err != nil || outcome.Kind != worker.OutcomeRetry || outcome.RetryAfter != want {
			t.Fatalf("failure %d: outcome=%+v err=%v, want retry after %s", i+1, outcome, err, want)
		}
		if _, err := store.Retry(ctx, claim, outcome.ReasonCode, outcome.Reason, outcome.RetryAfter, outcome.WriteOwner); err != nil {
			t.Fatal(err)
		}
		outcome.AfterCommit(ctx)
		if _, err := schemaOwner.ExecContext(ctx, `WITH o AS (DELETE FROM work_item_outcomes WHERE kind = 'rss_refresh') DELETE FROM work_items WHERE kind = 'rss_refresh'`); err != nil {
			t.Fatal(err)
		}
		_, failures := runtime()
		circuitFailures, paused := circuit()
		tripped := i+1 >= subscriptiondomain.ProviderCircuitThreshold
		if failures != i+1 || circuitFailures != i+1 || paused != tripped || pauseEpisodeOpen() != tripped {
			t.Fatalf("after failure %d: source=%d circuit=%d paused=%t episode=%t", i+1, failures, circuitFailures, paused, pauseEpisodeOpen())
		}
	}

	fetcher.err = nil
	fetcher.items = []acquisitiondomain.FeedItem{}
	version, _ := runtime()
	claim := createRSSClaim(t, db, store, owner, workitems.KindRSSRefresh, version)
	outcome, err := service.Handle(ctx, claim)
	if err != nil || outcome.Kind != worker.OutcomeFinish {
		t.Fatalf("recovery outcome=%+v err=%v", outcome, err)
	}
	if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	outcome.AfterCommit(ctx)
	_, failures := runtime()
	circuitFailures, paused := circuit()
	if failures != 0 || circuitFailures != 0 || paused || pauseEpisodeOpen() {
		t.Fatalf("success must reset backoff and circuit: source=%d circuit=%d paused=%t episode=%t", failures, circuitFailures, paused, pauseEpisodeOpen())
	}
}
