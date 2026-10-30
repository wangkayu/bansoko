package application

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/jackett"
)

const jackettTestAPIKey = "fixture-jackett-key-not-a-real-secret"

func jackettTestBody() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><title>Jackett</title>
<item><title>[示例字幕组] 示例作品 - 03 [1080p]</title><guid>rel_sample</guid>
<link>https://tracker.example.test/download/rel_sample.torrent?passkey=tracker-secret</link>
<pubDate>Thu, 22 Oct 2026 02:00:47 +0800</pubDate>
<enclosure url="https://tracker.example.test/download/rel_sample.torrent?passkey=tracker-secret" length="2048" type="application/x-bittorrent" />
<torznab:attr name="size" value="2048" /><torznab:attr name="infohash" value="fab1cfcf83daaffeb8a3cb7bb1e0329125d93b3e" />
<jackettindexer id="example-indexer">Example Tracker</jackettindexer><type>private</type>
</item></channel></rss>`
}

func TestFetchJackettSearchUsesFixedAggregateAndPersistsSourceAttribution(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != jackett.AggregateTorznabPath {
			t.Errorf("path = %q", r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("t") != "search" || query.Get("q") != "示例作品 03" || query.Get("limit") != "100" {
			t.Errorf("unexpected aggregate query: %s", r.URL.RawQuery)
		}
		if query.Get("apikey") != jackettTestAPIKey {
			t.Error("saved API key was not injected")
		}
		if query.Get("indexer") != "" {
			t.Error("request selected an indexer instead of the fixed all aggregate")
		}
		_, _ = io.WriteString(w, jackettTestBody())
	}))
	defer upstream.Close()

	store := &searchTestStore{}
	service := NewProviderService(nil, nil)
	service.SetFeedSnapshotStore(store)
	application := SeedSearchApplication{
		Service: service,
		Endpoints: CatalogSearchEndpointsFunc(func(context.Context, string) (CatalogSearchEndpoint, error) {
			return CatalogSearchEndpoint{Endpoint: upstream.URL, Client: upstream.Client(), APIKey: jackettTestAPIKey}, nil
		}),
		Now:               func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) },
		AllowPrivateHosts: []string{"127.0.0.1"},
	}
	preview, err := application.Fetch(context.Background(), FetchSearchSnapshotInput{Provider: ProviderJackett, Query: "  示例作品   03 "})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(store.written) != 1 || len(store.written[0].Seeds) != 1 {
		t.Fatalf("snapshot writes = %+v", store.written)
	}
	seed := store.written[0].Seeds[0]
	if seed.SourceSite == nil || seed.SourceSite.ID != "example-indexer" || seed.SourceSite.Name != "Example Tracker" || seed.SourceSite.Type != "private" {
		t.Fatalf("source attribution = %+v", seed.SourceSite)
	}
	if seed.Locator != "https://tracker.example.test/download/rel_sample.torrent?passkey=tracker-secret" {
		t.Fatalf("internal locator = %q", seed.Locator)
	}
	if strings.Contains(seed.LocatorDisplay, "passkey") || strings.Contains(seed.LocatorDisplay, "tracker-secret") {
		t.Fatalf("display locator exposes tracker credentials: %q", seed.LocatorDisplay)
	}
	if len(preview.Seeds) != 1 || preview.Seeds[0].SourceSite == nil || preview.Seeds[0].SourceSite.Name != "Example Tracker" {
		t.Fatalf("preview source = %+v", preview.Seeds)
	}
	if preview.Seeds[0].GroupName != "示例字幕组" {
		t.Fatalf("source site replaced the title-parsed group: %+v", preview.Seeds[0])
	}
	if strings.Contains(preview.Seeds[0].LocatorDisplay, "tracker-secret") {
		t.Fatal("preview exposes the tracker credential")
	}
	wantScope := SearchScopeRef(ProviderJackett, upstream.URL, "示例作品 03")
	if preview.ScopeRef != wantScope || store.written[0].ProviderEntryRef != wantScope {
		t.Fatal("snapshot scope is not derived from the credential-free endpoint and normalized query")
	}
	if store.written[0].NormalizedKind() != catalogdomain.FeedSnapshotKindSearch {
		t.Fatal("Jackett was not stored as a search-only snapshot")
	}
}

func TestJackettSearchSkipsRowsWithoutUsableSourceOrTorrentURL(t *testing.T) {
	payload := []byte(`<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>
<item><title>Missing source</title><link>https://tracker.example.test/a.torrent</link><pubDate>Thu, 22 Oct 2026 02:00:47 +0800</pubDate></item>
<item><title>Magnet only</title><link>magnet:?xt=urn:btih:fab1cfcf83daaffeb8a3cb7bb1e0329125d93b3e</link><pubDate>Thu, 22 Oct 2026 02:00:47 +0800</pubDate><jackettindexer id="example">Example</jackettindexer><type>public</type></item>
</channel></rss>`)
	_, err := parseJackettSearch(payload)
	if err == nil {
		t.Fatal("a page with no usable HTTP torrent result was accepted")
	}
}

func TestJackettSearchRedirectDoesNotLeakCredentialURL(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if referer := r.Header.Get("Referer"); referer != "" {
			t.Errorf("redirect forwarded Referer: %q", referer)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("redirect forwarded ambient credentials")
		}
		_, _ = io.WriteString(w, jackettTestBody())
	}))
	defer destination.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/results", http.StatusFound)
	}))
	defer upstream.Close()

	searcher := HTTPJackettSearcher{Endpoint: upstream.URL, APIKey: jackettTestAPIKey, Client: upstream.Client()}
	result, err := searcher.SearchJackettSeeds(context.Background(), "示例作品")
	if err != nil {
		t.Fatalf("redirected search: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("redirected result count = %d", len(result.Items))
	}
}

func TestJackettSearchErrorDoesNotEchoAPIKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<error code="100" description="request failed apikey=`+jackettTestAPIKey+`"/>`)
	}))
	defer upstream.Close()
	searcher := HTTPJackettSearcher{Endpoint: upstream.URL, APIKey: jackettTestAPIKey, Client: upstream.Client()}
	_, err := searcher.SearchJackettSeeds(context.Background(), "示例作品")
	if err == nil {
		t.Fatal("Torznab error was accepted")
	}
	if strings.Contains(err.Error(), jackettTestAPIKey) {
		t.Fatalf("error exposes API key: %v", err)
	}
}
