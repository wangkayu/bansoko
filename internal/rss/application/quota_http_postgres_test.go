package application

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	configuration "bansoko/internal/configuration/domain"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

func TestRSSHTTPRateLimitRefundsAttempts(t *testing.T) {
	for _, provider := range []string{"custom", "mikan"} {
		t.Run(provider, func(t *testing.T) {
			db := openRSSDatabase(t)
			ctx := t.Context()
			sourceID, subID := seedRSSOwner(t, db, false, true)
			var status, hits atomic.Int32
			status.Store(http.StatusTooManyRequests)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Retry-After", "23")
				w.WriteHeader(int(status.Load()))
			}))
			defer upstream.Close()
			if provider == "mikan" {
				if _, err := db.ExecContext(ctx, `UPDATE rss_sources SET source_kind='provider_group',provider='mikan',provider_entry_ref='123',provider_group_ref='456',discovery_snapshot_version='v1' WHERE id=$1`, sourceID); err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(ctx, `INSERT INTO provider_group_selections(id,subscription_id,provider,group_ref,display_name,selection_mode,effective_selected,effective_score) VALUES ('http-quota',$1,'mikan','456','Group','auto',true,50)`, subID); err != nil {
					t.Fatal(err)
				}
			} else if _, err := db.ExecContext(ctx, `UPDATE rss_sources SET locator='http://custom.provider.test/rss.xml',locator_display='http://custom.provider.test/rss.xml',http_feed_connection_ref='custom-feed' WHERE id=$1`, sourceID); err != nil {
				t.Fatal(err)
			}
			proxyURL, err := url.Parse(upstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.Proxy = http.ProxyURL(proxyURL)
			t.Cleanup(transport.CloseIdleConnections)
			fetcher := ConfiguredSourceFetcher{
				AllowPrivateHosts: []string{"127.0.0.1"},
				Connections: sourceFetcherConnections{connections: []configuration.Connection{
					{Kind: configuration.ConnectionMikan, Endpoint: upstream.URL, State: configuration.ConnectionConfigured},
					{ID: "custom-feed", Kind: configuration.ConnectionHTTPFeed, State: configuration.ConnectionConfigured, NetworkRouteID: "route-custom"},
				}},
				ClientResolver: sourceFetcherResolver{client: &http.Client{Transport: transport}},
				Resolver: func(_ context.Context, host string) ([]net.IP, error) {
					if host == "127.0.0.1" {
						return []net.IP{net.ParseIP(host)}, nil
					}
					return []net.IP{net.ParseIP("203.0.113.10")}, nil
				},
			}
			store, err := workitems.NewStore(db, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			service, err := newRSSService(t, db, store, fetcher)
			if err != nil {
				t.Fatal(err)
			}
			owner := workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}
			schemaOwner := ownerTestDB(t)
			var version int64
			if err := db.QueryRowContext(ctx, `SELECT version FROM rss_source_runtime WHERE rss_source_id=$1`, sourceID).Scan(&version); err != nil {
				t.Fatal(err)
			}
			claim := createRSSClaim(t, db, store, owner, workitems.KindRSSRefresh, version)
			for i := range 4 {
				outcome, err := service.Handle(ctx, claim)
				if err != nil || outcome.Kind != worker.OutcomeDeferRSS || outcome.ReasonCode != "provider_rate_limited" || outcome.RetryAfter != 23*time.Second || outcome.WriteOwner != nil {
					t.Fatalf("quota outcome=%+v err=%v", outcome, err)
				}
				if err := store.DeferRSSQuota(ctx, claim, outcome.ReasonCode, outcome.RetryAfter); err != nil {
					t.Fatal(err)
				}
				var attempt, failures, circuits int
				var unchanged, waiting bool
				err = db.QueryRowContext(ctx, `SELECT w.attempt,s.consecutive_failures,s.version=$2,w.available_at>CURRENT_TIMESTAMP,(SELECT count(*) FROM rss_provider_circuits) FROM work_items w CROSS JOIN rss_source_runtime s WHERE w.id=$1 AND s.rss_source_id=$3`, claim.ID, version, sourceID).Scan(&attempt, &failures, &unchanged, &waiting, &circuits)
				if err != nil || attempt != 0 || failures != 0 || circuits != 0 || !unchanged || !waiting || hits.Load() != int32(i+1) {
					t.Fatalf("refund: attempt=%d failures=%d circuits=%d unchanged=%t waiting=%t hits=%d err=%v", attempt, failures, circuits, unchanged, waiting, hits.Load(), err)
				}
				if _, err := schemaOwner.ExecContext(ctx, `UPDATE work_items SET available_at=CURRENT_TIMESTAMP WHERE id=$1`, claim.ID); err != nil {
					t.Fatal(err)
				}
				next, err := store.ClaimDue(ctx, "rss-http-test", []string{workitems.KindRSSRefresh}, time.Minute)
				if err != nil || next.ID != claim.ID || next.Attempt != 1 {
					t.Fatalf("reclaim=%+v err=%v", next, err)
				}
				claim = next
			}
			status.Store(http.StatusServiceUnavailable)
			for _, failure := range []string{"HTTP 503", "network"} {
				if failure == "network" {
					upstream.Close()
				}
				outcome, err := service.Handle(ctx, claim)
				if err != nil || outcome.Kind != worker.OutcomeRetry || outcome.RetryAfter != 5*time.Minute || outcome.WriteOwner == nil {
					t.Fatalf("%s: outcome=%+v err=%v", failure, outcome, err)
				}
			}
			if hits.Load() != 5 {
				t.Fatalf("upstream hits=%d", hits.Load())
			}
		})
	}
}
