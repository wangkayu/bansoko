package application

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/observability/outbound"
	quota "bansoko/internal/provider/outbound"
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
	const upstreamError = "upstream down at https://feeds.example.test/private-path?token=not-for-logs"
	fetcher := &rssFakeSourceFetcher{err: outbound.WithFailure(errors.New(upstreamError), outbound.Failure{
		Class: outbound.ClassHTTPStatus, Status: 503,
	})}
	service, err := newRSSService(t, db, store, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	logger := observabilityapplication.NewTeedRuntimeLogger(io.Discard)
	service.SetDiagnosticRecorder(recorder, logger)
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
		err := db.QueryRowContext(ctx, `SELECT succeeded_after_at IS NULL FROM diagnostic_episodes WHERE kind = 'site' AND variant = 'paused' AND target_id = 'anibt' ORDER BY first_failed_at DESC LIMIT 1`).Scan(&open)
		if errors.Is(err, sql.ErrNoRows) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		return open
	}

	// A shared pause must not consume even one of the three attempts or
	// mutate source health. Reclaim the same row past its retry deadline.
	initialVersion, _ := runtime()
	deferred := createRSSClaim(t, db, store, owner, workitems.KindRSSRefresh, initialVersion)
	for i := range 6 {
		wait := quota.CooldownError{RetryAfter: 12 * time.Second}
		fetcher.err = &wait
		wantCode := "provider_cooldown"
		if i == 0 {
			fetcher.err = &quota.ThrottleError{CooldownError: wait, StatusCode: http.StatusTooManyRequests}
			wantCode = "provider_rate_limited"
		}
		outcome, err := service.Handle(ctx, deferred)
		if err != nil || outcome.Kind != worker.OutcomeDeferRSS || outcome.ReasonCode != wantCode || outcome.RetryAfter != wait.RetryAfter || outcome.WriteOwner != nil {
			t.Fatalf("quota outcome=%+v err=%v", outcome, err)
		}
		if err := store.DeferRSSQuota(ctx, deferred, outcome.ReasonCode, outcome.RetryAfter); err != nil {
			t.Fatal(err)
		}
		if err := store.DeferRSSQuota(ctx, deferred, outcome.ReasonCode, outcome.RetryAfter); !errors.Is(err, workitems.ErrStaleClaim) {
			t.Fatalf("released claim may not refund again: %v", err)
		}
		var attempt int
		var state string
		var waiting bool
		if err := db.QueryRowContext(ctx, `SELECT attempt, state, available_at > CURRENT_TIMESTAMP FROM work_items WHERE id = $1`, deferred.ID).Scan(&attempt, &state, &waiting); err != nil || attempt != 0 || state != "available" || !waiting {
			t.Fatalf("quota transition: attempt=%d state=%s waiting=%t err=%v", attempt, state, waiting, err)
		}
		if _, err := schemaOwner.ExecContext(ctx, `UPDATE work_items SET available_at = CURRENT_TIMESTAMP WHERE id = $1`, deferred.ID); err != nil {
			t.Fatal(err)
		}
		next, err := store.ClaimDue(ctx, "rss-test-worker", []string{workitems.KindRSSRefresh}, time.Minute)
		if err != nil || next.ID != deferred.ID || next.Attempt != 1 || next.FencingToken <= deferred.FencingToken {
			t.Fatalf("reclaim after quota wait: %+v err=%v", next, err)
		}
		deferred = next
	}
	if version, failures := runtime(); version != initialVersion || failures != 0 {
		t.Fatalf("deferred source version=%d failures=%d", version, failures)
	}
	if failures, paused := circuit(); failures != 0 || paused || pauseEpisodeOpen() {
		t.Fatalf("deferred provider failures=%d paused=%t", failures, paused)
	}
	// A genuine 503 with Retry-After follows the ordinary failure path.
	fetcher.err = outbound.WithFailure(&quota.ThrottleError{StatusCode: http.StatusServiceUnavailable}, outbound.Failure{
		Class: outbound.ClassHTTPStatus, Status: http.StatusServiceUnavailable,
	})

	wantDelays := []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute, 80 * time.Minute}
	for i, want := range wantDelays {
		claim := deferred
		if i != 0 {
			fetcher.err = outbound.WithFailure(errors.New(upstreamError), outbound.Failure{Class: outbound.ClassHTTPStatus, Status: 503})
			version, _ := runtime()
			claim = createRSSClaim(t, db, store, owner, workitems.KindRSSRefresh, version)
		}
		outcome, err := service.Handle(ctx, claim)
		if err != nil || outcome.Kind != worker.OutcomeRetry || outcome.RetryAfter != want {
			t.Fatalf("failure %d: outcome=%+v err=%v, want retry after %s", i+1, outcome, err, want)
		}
		page, err := logger.RuntimeLogs(observabilityapplication.RuntimeLogQuery{WorkItemID: claim.ID})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Entries) != 1 {
			t.Fatalf("expected a single failed refresh result before retry writeback: %+v", page.Entries)
		}
		entry := page.Entries[0]
		if entry.Component != "rss" || entry.Stage != "read_feed" || entry.ReasonCode != "feed_fetch_failed" ||
			entry.Outcome != "failure" || entry.Retryable != nil || strings.Contains(entry.Message, "private-path") ||
			entry.Message != `RSS refresh for "Test Show" ("Test Group"): the feed request returned HTTP 503` ||
			entry.WorkItemID != claim.ID || entry.Attempt != claim.Attempt {
			t.Fatalf("incorrect or unsafe failed refresh result: %+v", entry)
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
