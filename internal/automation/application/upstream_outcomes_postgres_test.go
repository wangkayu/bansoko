package application

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	configuration "bansoko/internal/configuration/domain"
	"bansoko/internal/metadata/matcher"
	observability "bansoko/internal/observability/application"
	provideradapter "bansoko/internal/provider/adapter"
	provider "bansoko/internal/provider/domain"
	"bansoko/internal/provider/outbound"
	outboundpersistence "bansoko/internal/provider/outbound/persistence"
	selection "bansoko/internal/selection/application"
	"bansoko/internal/testutil/storedsections"
	"bansoko/internal/workitems"
)

type readGroupsFunc func(context.Context, string) (provider.GroupReadResult, error)

func (f readGroupsFunc) ReadGroups(ctx context.Context, ref string) (provider.GroupReadResult, error) {
	return f(ctx, ref)
}

func TestScheduledMetadataMatchDefersRateLimit(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	insertMetadataSubscriptionFixture(t, db, "anime-meta-quota", "sub-meta-quota", "Quota Show")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer upstream.Close()
	recorder, err := observability.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	m := matcher.NewMatcher(nil, &matcher.BGMMatcher{Client: upstream.Client(), Endpoint: upstream.URL}, nil, nil)
	m.SetDiagnostics(recorder)
	dispatcher := &Dispatcher{DB: db, Recorder: recorder, MetadataMatcher: m}
	for range 2 {
		counts, err := dispatcher.dispatchMetadataMatch(ctx)
		if err != nil || counts.targetCount != 1 || counts.skipped != 1 || counts.errors != 0 || *counts.upstream != (provider.Outcomes{RateLimited: 1}) {
			t.Fatalf("unexpected metadata dispatch: %v upstream=%+v err=%v", counts, counts.upstream, err)
		}
	}
	var alerts int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM diagnostic_episodes WHERE kind = 'metadata'`).Scan(&alerts); err != nil || alerts != 0 {
		t.Fatalf("metadata quota produced %d alerts: %v", alerts, err)
	}
}

func TestScheduledDiscoverySummarizesUpstreamOutcomes(t *testing.T) {
	db := openDispatcherTestDB(t)
	ctx := context.Background()
	for i := 123; i <= 128; i++ {
		owner, ref := fmt.Sprintf("owner-%d", i), fmt.Sprint(i)
		if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime(id, title) VALUES ($1, $1)`, owner); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO anime_business_configs(managed_anime_id) VALUES ($1) ON CONFLICT (managed_anime_id) DO NOTHING`, owner); err != nil {
			t.Fatal(err)
		}
		origin := "anibt"
		if i == 126 {
			origin = "mikan"
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO subscriptions(id, managed_anime_id, origin, origin_entry_ref, state) VALUES ($1, $1, $3, $2, 'active')`, owner, ref, origin); err != nil {
			t.Fatal(err)
		}
		storedsections.Seed(t, db, owner)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Home/Bangumi/126" {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		ref := r.URL.Query().Get("bgmId")
		groups := `[]`
		if ref == "123" {
			groups = `[{"slug":"alpha","name":"Alpha"}]`
		}
		_, _ = fmt.Fprintf(w, `{"data":{"bgmId":%s,"groups":%s}}`, ref, groups)
	}))
	defer upstream.Close()
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := observability.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	deadline := now.Add(time.Minute)
	groupReader := dispatcherTestGroupReader{Endpoint: upstream.URL, Client: upstream.Client()}
	dispatcher := &Dispatcher{
		DB: db, WorkItems: store, Recorder: recorder, PolicyReader: selection.NewService(db),
		Connections: dispatcherTestConnections{
			{ID: "anibt-test", Kind: configuration.ConnectionAniBT, Endpoint: upstream.URL, State: configuration.ConnectionConfigured},
			{ID: "mikan-source-conn", Kind: configuration.ConnectionMikan, Endpoint: upstream.URL, State: configuration.ConnectionConfigured},
		},
		GroupReaders: map[string]GroupReader{"anibt": readGroupsFunc(func(ctx context.Context, ref string) (provider.GroupReadResult, error) {
			switch ref {
			case "125":
				return provider.GroupReadResult{}, &provider.ReadFailure{Status: provider.Status{State: provider.StateNotIndexed, ReasonCode: provider.ReasonAnimeNotFound}}
			case "127":
				return provider.GroupReadResult{}, &outbound.CooldownError{ReasonCode: outbound.ReasonBucketCooldown, CheckedAt: now, CooldownUntil: deadline}
			case "128":
				return provider.GroupReadResult{}, errors.New("bad response")
			default:
				return groupReader.ReadGroups(ctx, ref)
			}
		}), "mikan": provideradapter.MikanGroupReader{Endpoint: upstream.URL, Client: upstream.Client(), Now: func() time.Time { return now }}},
	}
	if _, err := db.ExecContext(ctx, `UPDATE automation_schedules SET enabled = (kind = 'group_discovery'), next_run_at = CASE WHEN kind = 'group_discovery' THEN CURRENT_TIMESTAMP - INTERVAL '1 minute' ELSE NULL END`); err != nil {
		t.Fatal(err)
	}
	if count, err := dispatcher.DispatchDue(ctx); err != nil || count != 1 {
		t.Fatalf("due group discovery: count=%d err=%v", count, err)
	}
	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	var runID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM automation_dispatch_runs WHERE kind = 'group_discovery'`).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	want := provider.Outcomes{Available: 1, Empty: 1, NotIndexed: 1, RateLimited: 1, Cooldown: 1, Error: 1}
	run, err := service.GetRun(ctx, runID)
	if err != nil || run.State != "partial" || run.DispatchErrorCount != 1 || !hasCompletedCount(run, 2) || run.UpstreamOutcomes == nil || *run.UpstreamOutcomes != want {
		t.Fatalf("dispatch run=%+v upstream=%v err=%v", run, run.UpstreamOutcomes, err)
	}
	var state, reason string
	var cooldown time.Time
	if err := db.QueryRowContext(ctx, `SELECT state,error_code,cooldown_until FROM provider_group_refresh_health WHERE provider='mikan' AND catalog_entry_ref='126'`).Scan(&state, &reason, &cooldown); err != nil || state != "rate_limited" || reason != "PROVIDER_RATE_LIMITED" || !cooldown.Equal(deadline) {
		t.Fatalf("Mikan refresh state=%q reason=%q cooldown=%v err=%v", state, reason, cooldown, err)
	}
	var routineErrors, routineCooldown int
	if err := db.QueryRowContext(ctx, `SELECT (details->>'error_count')::int, (details->'upstream_outcomes'->>'cooldown')::int FROM routine_activities WHERE kind = 'automation_dispatch' AND scope_id = 'group_discovery'`).Scan(&routineErrors, &routineCooldown); err != nil || routineErrors != 1 || routineCooldown != 1 {
		t.Fatalf("routine summary: errors=%d cooldown=%d err=%v", routineErrors, routineCooldown, err)
	}

	// Quota evidence is independent of the business outcomes, including a
	// newer in-memory deadline that has not yet been written to persistence.
	quotaStore := outboundpersistence.New(db)
	if err := quotaStore.Save(ctx, outbound.Health{Bucket: outbound.AniBTMetadata, CooldownUntil: deadline, LastLimitedAt: now, ReasonCode: outbound.ReasonRateLimited, Summary: "HTTP 429", Consecutive: 1, UpdatedAt: now, Version: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO provider_catalog_snapshots(id, provider, season, status, observed_at, contract_version, health_checked_at, version, upstream_state)
VALUES ('good', 'anibt', '2026-summer', 'fresh', $1, 'test', $1, 1, 'available'),
       ('down', 'anibt', '2026-summer', 'outage', $2, 'test', $2, 2, 'cooldown'),
       ('empty', 'mikan', '2026-summer', 'zero_result', $1, 'test', $1, 1, NULL),
       ('error', 'mikan', '2026-fall', 'outage', $2, 'test', $2, 1, 'error')`, now, deadline); err != nil {
		t.Fatal(err)
	}
	reader := outboundpersistence.HealthReader{Store: quotaStore, Live: func() []outbound.Health {
		return []outbound.Health{{Bucket: outbound.AniBTMetadata, CooldownUntil: deadline.Add(time.Minute), LastLimitedAt: now}}
	}}
	health, err := reader.Read(ctx)
	if err != nil || len(health.Quotas) != 1 || !health.Quotas[0].CooldownUntil.Equal(deadline.Add(time.Minute)) || len(health.Refreshes) != 4 {
		t.Fatalf("diagnostics=%+v err=%v", health, err)
	}
	groups, mikanGroups, catalog, mikan := health.Refreshes[0], health.Refreshes[1], health.Refreshes[2], health.Refreshes[3]
	want.RateLimited = 0
	if groups.Kind != "groups" || groups.Outcomes != want || groups.LastSuccessAt.IsZero() ||
		catalog.Kind != "catalog" || catalog.Outcomes != (provider.Outcomes{Cooldown: 1}) || !catalog.LastSuccessAt.Equal(now) ||
		mikanGroups.Kind != "groups" || mikanGroups.Provider != "mikan" || mikanGroups.Outcomes != (provider.Outcomes{RateLimited: 1}) || !mikanGroups.LastSuccessAt.IsZero() ||
		mikan.Provider != "mikan" || mikan.Outcomes != (provider.Outcomes{Empty: 1, Error: 1}) {
		t.Fatalf("business refreshes=%+v", health.Refreshes)
	}
}
