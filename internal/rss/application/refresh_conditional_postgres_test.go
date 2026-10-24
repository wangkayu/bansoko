package application

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	acquisitionapplication "bansoko/internal/acquisition/application"
	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/runtime/worker"
	subscriptiondomain "bansoko/internal/subscription/domain"
	"bansoko/internal/workitems"
)

// httpFixtureSourceFetcher drives the real shared HTTP feed fetcher (with
// torrent metadata enrichment) against a local fixture server.
type httpFixtureSourceFetcher struct{ feedURL string }

// The fixture is reached through an allowlisted host alias: the RSS locator
// rejects literal loopback addresses before any request.
const rssFixtureHost = "rss-fixture.test"

func rssFixtureResolver(context.Context, string) ([]net.IP, error) {
	return []net.IP{net.ParseIP("127.0.0.1")}, nil
}

func (f httpFixtureSourceFetcher) FetchSource(ctx context.Context, _ SourceLocator) ([]acquisitiondomain.FeedItem, error) {
	return (acquisitionapplication.HTTPRSSFetcher{
		AllowPrivateHosts: []string{rssFixtureHost},
		Resolver:          rssFixtureResolver,
		TorrentReader:     acquisitionapplication.HTTPTorrentMetadataReader{AllowPrivateHosts: []string{rssFixtureHost}, Resolver: rssFixtureResolver},
	}).Fetch(ctx, subscriptiondomain.RSSLocator{URL: f.feedURL})
}

// TestRefreshConditionalRequestAndTorrentReuse proves the RSS refresh sends the
// stored validators, records a 304 as a refresh without release work, and never
// downloads the .torrent of an already-known release again even when the feed
// is re-read in full.
func TestRefreshConditionalRequestAndTorrentReuse(t *testing.T) {
	db := openRSSDatabase(t)
	ctx := context.Background()
	sourceID, _ := seedRSSOwner(t, db, false, true)
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	var feedRequests, torrentRequests, conditionalRequests atomic.Int32
	etag := `"feed-v1"`
	honourValidators := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rss.xml":
			feedRequests.Add(1)
			if r.Header.Get("If-None-Match") != "" {
				conditionalRequests.Add(1)
			}
			if honourValidators && r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", etag)
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>feed</title><item><guid>guid-conditional-01</guid><title>[Group] Test Show - 01 1080p</title><enclosure url="http://` + r.Host + `/show-01.torrent" type="application/x-bittorrent"/></item></channel></rss>`))
		case "/show-01.torrent":
			torrentRequests.Add(1)
			_, _ = w.Write([]byte("d4:infod4:name15:show-01.torrent6:lengthi1eee"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	port := server.URL[strings.LastIndex(server.URL, ":")+1:]
	service, err := newRSSService(t, db, store, httpFixtureSourceFetcher{feedURL: "http://" + rssFixtureHost + ":" + port + "/rss.xml"})
	if err != nil {
		t.Fatal(err)
	}
	// The runtime role cannot delete WorkItems; test cleanup uses the owner.
	schemaOwner := ownerTestDB(t)
	runtimeVersion := func() int64 {
		var version int64
		if err := db.QueryRowContext(ctx, `SELECT version FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).Scan(&version); err != nil {
			t.Fatal(err)
		}
		return version
	}
	refresh := func() worker.Outcome {
		t.Helper()
		if _, err := schemaOwner.ExecContext(ctx, `WITH o AS (DELETE FROM work_item_outcomes WHERE kind = 'rss_refresh') DELETE FROM work_items WHERE kind = 'rss_refresh'`); err != nil {
			t.Fatal(err)
		}
		claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, runtimeVersion())
		outcome, err := service.Handle(ctx, claim)
		if err != nil || outcome.Kind != worker.OutcomeFinish {
			t.Fatalf("refresh outcome=%+v err=%v", outcome, err)
		}
		if err := store.FinishWithResult(ctx, claim, outcome.TerminalState, outcome.OwnerResult, outcome.WriteOwner); err != nil {
			t.Fatal(err)
		}
		return outcome
	}
	release := func() (string, int64) {
		var id string
		var version int64
		if err := db.QueryRowContext(ctx, `SELECT id, version FROM releases WHERE rss_source_id = $1`, sourceID).Scan(&id, &version); err != nil {
			t.Fatal(err)
		}
		return id, version
	}

	first := refresh()
	if first.OwnerResult.ReasonCode != "refresh_succeeded" || torrentRequests.Load() != 1 || conditionalRequests.Load() != 0 {
		t.Fatalf("first refresh: reason=%s torrent=%d conditional=%d", first.OwnerResult.ReasonCode, torrentRequests.Load(), conditionalRequests.Load())
	}
	var storedETag string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(etag, '') FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).Scan(&storedETag); err != nil {
		t.Fatal(err)
	}
	if storedETag != etag {
		t.Fatalf("stored etag = %q, want %q", storedETag, etag)
	}
	releaseID, releaseVersion := release()

	var refreshedBefore string
	if err := db.QueryRowContext(ctx, `SELECT last_refresh_at::text FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).Scan(&refreshedBefore); err != nil {
		t.Fatal(err)
	}
	notModified := refresh()
	if notModified.OwnerResult.ReasonCode != "refresh_not_modified" || conditionalRequests.Load() != 1 || torrentRequests.Load() != 1 {
		t.Fatalf("304 refresh: reason=%s conditional=%d torrent=%d", notModified.OwnerResult.ReasonCode, conditionalRequests.Load(), torrentRequests.Load())
	}
	var refreshedAfter, health, keptETag string
	if err := db.QueryRowContext(ctx, `SELECT last_refresh_at::text, health, COALESCE(etag, '') FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).Scan(&refreshedAfter, &health, &keptETag); err != nil {
		t.Fatal(err)
	}
	if refreshedAfter == refreshedBefore || health != "ready" || keptETag != etag {
		t.Fatalf("304 must only re-stamp the refresh: before=%s after=%s health=%s etag=%q", refreshedBefore, refreshedAfter, health, keptETag)
	}
	if id, version := release(); id != releaseID || version != releaseVersion {
		t.Fatalf("304 touched the release: %s v%d -> %s v%d", releaseID, releaseVersion, id, version)
	}

	// The upstream ignores validators and serves the full feed again: the
	// known release must reuse its stored torrent instead of downloading it.
	honourValidators = false
	full := refresh()
	if full.OwnerResult.ReasonCode != "refresh_succeeded" || feedRequests.Load() != 3 || torrentRequests.Load() != 1 {
		t.Fatalf("full re-read: reason=%s feed=%d torrent=%d", full.OwnerResult.ReasonCode, feedRequests.Load(), torrentRequests.Load())
	}
	if id, version := release(); id != releaseID || version != releaseVersion {
		t.Fatalf("reused torrent metadata changed the release: %s v%d -> %s v%d", releaseID, releaseVersion, id, version)
	}
}
