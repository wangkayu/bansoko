package application

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"bansoko/internal/acquisition/domain"
	"bansoko/internal/acquisition/torrent"
	configurationdomain "bansoko/internal/configuration/domain"
	providerdomain "bansoko/internal/provider/domain"
	subscriptiondomain "bansoko/internal/subscription/domain"
)

type fakeTorrentReader struct {
	metadata torrent.Metadata
	lastURL  string
	calls    int
}

func (r *fakeTorrentReader) Fetch(_ context.Context, sourceURL string) (torrent.Metadata, error) {
	r.lastURL = sourceURL
	r.calls++
	return r.metadata, nil
}

type fakeTorrentPayloadReader struct {
	fakeTorrentReader
	payload []byte
}

func (r *fakeTorrentPayloadReader) FetchPayload(ctx context.Context, sourceURL string) (TorrentPayload, error) {
	metadata, err := r.Fetch(ctx, sourceURL)
	if err != nil {
		return TorrentPayload{}, err
	}
	return TorrentPayload{Bytes: append([]byte(nil), r.payload...), Metadata: metadata}, nil
}

// failingTorrentPayloadReader always fails, and counts how many times it was
// asked, so the "one bounded request per unique locator" contract can be
// asserted on the failure path too.
type failingTorrentPayloadReader struct {
	err   error
	calls int
}

func (r *failingTorrentPayloadReader) Fetch(context.Context, string) (torrent.Metadata, error) {
	r.calls++
	return torrent.Metadata{}, r.err
}

func (r *failingTorrentPayloadReader) FetchPayload(context.Context, string) (TorrentPayload, error) {
	r.calls++
	return TorrentPayload{}, r.err
}

type fixtureConnectionSource struct {
	connections []configurationdomain.Connection
}

func (s fixtureConnectionSource) ListConnections(context.Context) ([]configurationdomain.Connection, error) {
	return s.connections, nil
}

func TestHTTPRSSFetcherRejectsDNSResolutionToPrivateAddress(t *testing.T) {
	dialed := false
	transport := &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			dialed = true
			return nil, nil
		},
	}
	fetcher := HTTPRSSFetcher{
		Client: &http.Client{Transport: transport},
		Resolver: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		},
	}

	_, err := fetcher.Fetch(context.Background(), subscriptiondomain.RSSLocator{URL: "https://feed.example.test/feed.xml"})
	if err == nil || !strings.Contains(err.Error(), "private or local") {
		t.Fatalf("DNS private address was not rejected: %v", err)
	}
	if dialed {
		t.Fatal("private RSS address was dialed")
	}
}

func TestHTTPRSSFetcherForwardsExplicitHTTPProxy(t *testing.T) {
	var proxyRequests int
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests++
		if r.URL.Scheme != "http" || r.URL.Host == "" {
			t.Fatalf("proxy did not receive an absolute HTTP request: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>feed</title></channel></rss>`))
	}))
	defer proxyServer.Close()
	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	resolverCalls := 0
	fetcher := HTTPRSSFetcher{
		Client: &http.Client{Transport: transport},
		Resolver: func(context.Context, string) ([]net.IP, error) {
			resolverCalls++
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		},
	}

	_, err = fetcher.Fetch(context.Background(), subscriptiondomain.RSSLocator{URL: "http://feed.example.test/feed.xml"})
	if err != nil {
		t.Fatalf("explicit route feed should succeed through the proxy: %v", err)
	}
	if proxyRequests != 1 {
		t.Fatalf("explicit HTTP proxy received %d request(s), want 1", proxyRequests)
	}
	// The request target is still SSRF-validated to a public address before
	// routing, so the resolver runs once even on the proxied path.
	if resolverCalls != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolverCalls)
	}
}

func TestHTTPAniBTRSSFetcherMapsProviderFacts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rss/anime.xml" || r.URL.Query().Get("bgmId") != "543360" || r.URL.Query().Get("groupSlug") != "sub-group" {
			t.Fatalf("unexpected AniBT request: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprint(w, `<?xml version="1.0"?><rss><channel><item><guid>rel-1</guid><title>[Group] Show - 01</title><link>https://anibt.test/release/rel-1</link><pubDate>Sat, 11 Jul 2026 03:00:00 +0000</pubDate><enclosure url="https://anibt.test/api/torrent/rel-1.torrent" length="123"/><torrent><infohash>abc</infohash><magneturi>magnet:?xt=urn:btih:abc</magneturi><filename>show.mkv</filename></torrent></item></channel></rss>`)
	}))
	defer server.Close()

	items, err := (HTTPAniBTRSSFetcher{Endpoint: server.URL, AllowPrivateHosts: []string{"127.0.0.1"}}).FetchAnime(context.Background(), "543360", "sub-group")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Provider != "anibt" || items[0].Enclosure == "" {
		t.Fatalf("unexpected AniBT feed item: %+v", items)
	}
	facts := make(map[string]bool)
	for _, fact := range items[0].SourceFacts {
		facts[fact.FieldName] = true
	}
	for _, field := range []string{"anibt.release_id", "anibt.torrent_url", "anibt.info_hash", "anibt.filename", "anibt.size_bytes", "anibt.group_slug"} {
		if !facts[field] {
			t.Fatalf("missing provider fact %q: %+v", field, items[0].SourceFacts)
		}
	}
	// The feed's magnet is ignored: no magnet fact, no magnet download.
	if facts["anibt.magnet_uri"] || strings.HasPrefix(items[0].DownloadURL(), "magnet:") {
		t.Fatalf("magnet leaked from the AniBT item: %+v", items[0])
	}
}

func TestHTTPMikanRSSFetcherUsesExactSubgroupAndMapsProviderFacts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/RSS/Bangumi" || r.URL.Query().Get("bangumiId") != "3706" || r.URL.Query().Get("subgroupid") != "42" {
			t.Fatalf("unexpected Mikan request: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprint(w, `<?xml version="1.0"?><rss><channel><item><guid>mikan-rel-1</guid><title>[Mikan Group] Show - 01</title><link>https://mikan.test/Home/Episode/mikan-rel-1</link><pubDate>Sat, 11 Jul 2026 03:00:00 +0000</pubDate><enclosure url="https://mikan.test/Download/mikan-rel-1.torrent" type="application/x-bittorrent"/></item></channel></rss>`)
	}))
	defer server.Close()

	items, err := (HTTPMikanRSSFetcher{Endpoint: server.URL, AllowPrivateHosts: []string{"127.0.0.1"}}).FetchAnime(context.Background(), "3706", "42")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Provider != "mikan" || items[0].Enclosure == "" {
		t.Fatalf("unexpected Mikan feed item: %+v", items)
	}
	if items[0].DownloadURL() != items[0].Enclosure {
		t.Fatalf("Mikan download URL should use the torrent enclosure: %+v", items[0])
	}
	facts := make(map[string]string)
	for _, fact := range items[0].SourceFacts {
		facts[fact.FieldName] = fact.RawValue
	}
	if facts["mikan.bangumi_id"] != "3706" || facts["mikan.subgroup_id"] != "42" {
		t.Fatalf("Mikan provider facts are incomplete: %+v", items[0].SourceFacts)
	}
}

// Provider sources report item skips exactly like custom sources: the detailed
// result keeps each reason, and a feed whose every item is skipped fails with
// the shared summary instead of reading as an empty source.
func TestProviderRSSFetchersReportSkipReasons(t *testing.T) {
	const good = `<item><title>[G] Show - 01</title><enclosure url="https://mikan.test/Download/20261019/084c615f82f1e9577ce3bf115069cf3c59111065.torrent" type="application/x-bittorrent"/></item>`
	const magnet = `<item><title>[G] Show - 02</title><enclosure url="magnet:?xt=urn:btih:abc" type="application/x-bittorrent"/></item>`
	body := good + magnet
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprint(w, `<?xml version="1.0"?><rss><channel>`+body+`</channel></rss>`)
	}))
	defer server.Close()
	fetcher := HTTPMikanRSSFetcher{Endpoint: server.URL, AllowPrivateHosts: []string{"127.0.0.1"}}

	result, err := fetcher.FetchAnimeDetailed(context.Background(), "3706", "42")
	if err != nil {
		t.Fatal(err)
	}
	if result.Parse.Site == nil || result.Parse.Site.ID != domain.FeedSiteMikan {
		t.Fatalf("provider feed must be parsed by its site adapter: %+v", result.Parse.Site)
	}
	if len(result.Items) != 1 || len(result.Parse.Skipped) != 1 || result.Parse.Skipped[0].Reason != domain.FeedSkipMagnetOnly {
		t.Fatalf("provider skip reasons were not reported: items=%d skipped=%+v", len(result.Items), result.Parse.Skipped)
	}

	body = magnet
	_, err = fetcher.FetchAnime(context.Background(), "3706", "42")
	var failure *domain.FeedFailure
	if !errors.As(err, &failure) || failure.Code != domain.FeedFailureNoUsableItems || !strings.Contains(err.Error(), "magnet_only=1") {
		t.Fatalf("all-skipped provider feed must fail with the summary, got %v", err)
	}
}

func TestProviderRSSFetchersAcceptValidEmptyFeed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprint(w, `<?xml version="1.0"?><rss><channel/></rss>`)
	}))
	defer server.Close()

	anibtItems, err := (HTTPAniBTRSSFetcher{Endpoint: server.URL, AllowPrivateHosts: []string{"127.0.0.1"}}).FetchAnime(context.Background(), "543360", "sub-group")
	if err != nil || len(anibtItems) != 0 {
		t.Fatalf("valid empty AniBT feed should be a zero-result: items=%+v err=%v", anibtItems, err)
	}
	mikanItems, err := (HTTPMikanRSSFetcher{Endpoint: server.URL, AllowPrivateHosts: []string{"127.0.0.1"}}).FetchAnime(context.Background(), "3706", "42")
	if err != nil || len(mikanItems) != 0 {
		t.Fatalf("valid empty Mikan feed should be a zero-result: items=%+v err=%v", mikanItems, err)
	}
}

func TestHTTPRSSFetcherDetailedResultPreservesItemSkipsForPreview(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprint(w, `<rss><channel>
<item><title>missing download</title></item>
<item><title>usable item</title><enclosure url="https://example.test/usable.torrent"/></item>
</channel></rss>`)
	}))
	defer server.Close()
	serverAddress := server.Listener.Addr().String()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, serverAddress)
	}

	fetcher := HTTPRSSFetcher{Client: &http.Client{Transport: transport}}
	result, err := fetcher.FetchDetailed(context.Background(), subscriptiondomain.RSSLocator{URL: "http://203.0.113.10/feed.xml"})
	if err != nil || len(result.Items) != 1 || len(result.Parse.Skipped) != 1 || result.Parse.Skipped[0].Reason != "download_url_missing" {
		t.Fatalf("detailed RSS result did not preserve compatibility diagnostics: %+v err=%v", result, err)
	}
	if len(result.Parse.Warnings) != 1 || result.Parse.Warnings[0] != "items_skipped" {
		t.Fatalf("detailed RSS warnings are unstable: %+v", result.Parse)
	}
}

func TestProviderRSSFetcherEnrichesTorrentFileTreeThroughSharedReader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprint(w, `<?xml version="1.0"?><rss><channel><item><guid>mikan-tree</guid><title>[Group] Show - 01</title><link>https://mikan.test/release/mikan-tree</link><enclosure url="https://mikan.test/Download/mikan-tree.torrent"/></item></channel></rss>`)
	}))
	defer server.Close()
	reader := &fakeTorrentReader{metadata: torrent.Metadata{
		InfoHashV1: "0123456789012345678901234567890123456789", TorrentName: "Show", TorrentVersion: "v1", TotalSize: 12,
		Files: []torrent.File{{Path: "Show/Show.mkv", Name: "Show.mkv", SizeBytes: 12, MediaKind: "video"}},
	}}
	items, err := (HTTPMikanRSSFetcher{Endpoint: server.URL, AllowPrivateHosts: []string{"127.0.0.1"}, TorrentReader: reader}).FetchAnime(context.Background(), "3706", "42")
	if err != nil {
		t.Fatal(err)
	}
	if reader.lastURL != "https://mikan.test/Download/mikan-tree.torrent" || len(items) != 1 || len(items[0].ReleaseFiles) != 1 {
		t.Fatalf("torrent reader was not used: url=%q items=%+v", reader.lastURL, items)
	}
	if items[0].ReleaseFiles[0].SourcePath != "Show/Show.mkv" || items[0].ReleaseFiles[0].SourceHash != reader.metadata.InfoHashV1 {
		t.Fatalf("unexpected enriched file facts: %+v", items[0].ReleaseFiles)
	}
}

func TestEnrichFeedItemsSkipsNonTorrentDownloadSources(t *testing.T) {
	reader := &fakeTorrentReader{metadata: torrent.Metadata{TorrentName: "unexpected"}}
	items := []domain.FeedItem{
		{Title: "HTML enclosure", Enclosure: "https://example.test/release/1", EnclosureType: "text/html"},
		{Title: "Octet stream enclosure", Enclosure: "https://example.test/release/2", EnclosureType: "application/octet-stream"},
		{Title: "Release link", Link: "https://example.test/release/3"},
	}
	_, err := enrichFeedItemsWithTorrentMetadata(context.Background(), items, reader)
	if err != nil {
		t.Fatal(err)
	}
	if reader.calls != 0 {
		t.Fatalf("non-torrent sources must not invoke torrent reader: calls=%d url=%q", reader.calls, reader.lastURL)
	}
}

func TestEnrichFeedItemsRetainsVerifiedTorrentPayload(t *testing.T) {
	payload := []byte("d4:infod4:name8:show.mkv6:lengthi12eee")
	reader := &fakeTorrentPayloadReader{
		fakeTorrentReader: fakeTorrentReader{metadata: torrent.Metadata{
			InfoHashV1:  "0123456789012345678901234567890123456789",
			TorrentName: "show.mkv", TorrentVersion: "v1", TotalSize: 12,
			Files: []torrent.File{{Path: "show.mkv", Name: "show.mkv", SizeBytes: 12, MediaKind: "video"}},
		}},
		payload: payload,
	}
	result, err := enrichFeedItemsWithTorrentMetadata(context.Background(), []domain.FeedItem{{
		Title: "Show - 01", TorrentURL: "https://example.test/files/show.torrent",
	}}, reader)
	if err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 || string(result[0].TorrentPayload) != string(payload) {
		t.Fatalf("verified torrent payload was not retained: calls=%d payload=%q", reader.calls, result[0].TorrentPayload)
	}
	if result[0].TorrentInfoHashV1 != reader.metadata.InfoHashV1 || result[0].TorrentVersion != "v1" {
		t.Fatalf("verified torrent metadata was not retained: %+v", result[0])
	}
}

func TestEnrichFeedItemsUsesExplicitTorrentURLBeforeGenericEnclosure(t *testing.T) {
	reader := &fakeTorrentReader{metadata: torrent.Metadata{TorrentName: "Show"}}
	items := []domain.FeedItem{{
		Title:         "Explicit torrent URL",
		Enclosure:     "https://example.test/download",
		EnclosureType: "text/html",
		TorrentURL:    "https://example.test/files/show.torrent",
	}}
	result, err := enrichFeedItemsWithTorrentMetadata(context.Background(), items, reader)
	if err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 || reader.lastURL != items[0].TorrentURL {
		t.Fatalf("explicit torrent URL was not used: calls=%d url=%q", reader.calls, reader.lastURL)
	}
	if result[0].TorrentFilename != "Show" {
		t.Fatalf("torrent metadata was not applied: %+v", result[0])
	}
}

func TestEnrichFeedItemsFetchesEachUniqueTorrentLocatorOnce(t *testing.T) {
	payload := []byte("d4:infod4:name8:show.mkv6:lengthi12eee")
	reader := &fakeTorrentPayloadReader{
		fakeTorrentReader: fakeTorrentReader{metadata: torrent.Metadata{
			InfoHashV1:  "0123456789012345678901234567890123456789",
			TorrentName: "show.mkv", TorrentVersion: "v1", TotalSize: 12,
			Files: []torrent.File{{Path: "show.mkv", Name: "show.mkv", SizeBytes: 12, MediaKind: "video"}},
		}},
		payload: payload,
	}
	// Three releases of one package share a single torrent locator; a fourth
	// points somewhere else. One refresh must cost exactly two requests.
	items := []domain.FeedItem{
		{Title: "Show - 01", TorrentURL: "https://example.test/files/show.torrent"},
		{Title: "Show - 01 [1080p]", TorrentURL: "https://example.test/files/show.torrent"},
		{Title: "Show - 01 [720p]", Enclosure: "https://example.test/files/show.torrent"},
		{Title: "Show - 02", TorrentURL: "https://example.test/files/show-02.torrent"},
	}
	result, err := enrichFeedItemsWithTorrentMetadata(context.Background(), items, reader)
	if err != nil {
		t.Fatal(err)
	}
	if reader.calls != 2 {
		t.Fatalf("unique locator requests = %d, want 2", reader.calls)
	}
	for index := range items {
		if string(result[index].TorrentPayload) != string(payload) || len(result[index].ReleaseFiles) != 1 {
			t.Fatalf("item %d did not receive the shared torrent facts: %+v", index, result[index].ReleaseFiles)
		}
	}
	// Each item owns its own payload bytes; mutating one must not corrupt another.
	result[0].TorrentPayload[0] = 'X'
	if result[1].TorrentPayload[0] == 'X' || result[2].TorrentPayload[0] == 'X' {
		t.Fatal("shared torrent payload bytes leaked between items")
	}
}

func TestEnrichFeedItemsRemembersRepeatedLocatorFailure(t *testing.T) {
	reader := &failingTorrentPayloadReader{err: errors.New("torrent host is unreachable")}
	items := []domain.FeedItem{
		{Title: "Show - 01", TorrentURL: "https://example.test/files/show.torrent"},
		{Title: "Show - 01 [1080p]", TorrentURL: "https://example.test/files/show.torrent"},
	}
	result, err := enrichFeedItemsWithTorrentMetadata(context.Background(), items, reader)
	if err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 {
		t.Fatalf("a repeated dead locator cost %d requests, want 1", reader.calls)
	}
	for index := range items {
		if result[index].TorrentErrorCode != "torrent_metadata_unavailable" {
			t.Fatalf("item %d lost its bounded acquisition reason: %q", index, result[index].TorrentErrorCode)
		}
		if len(result[index].ReleaseFiles) != 0 {
			t.Fatalf("item %d fabricated files for an unusable torrent", index)
		}
	}
}

func TestHTTPMikanRSSFetcherForwardsExplicitHTTPProxy(t *testing.T) {
	var proxyRequests int
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests++
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>feed</title></channel></rss>`))
	}))
	defer proxyServer.Close()

	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	_, err = (HTTPMikanRSSFetcher{
		Endpoint: "http://203.0.113.10",
		Client:   &http.Client{Transport: transport},
	}).FetchAnime(context.Background(), "3706", "42")
	if err != nil {
		t.Fatalf("explicit route feed should succeed through the proxy: %v", err)
	}
	if proxyRequests != 1 {
		t.Fatalf("explicit HTTP proxy received %d request(s), want 1", proxyRequests)
	}
}

// The preview fetcher is the list path, so it must cost exactly one request per
// group: the RSS itself. An item that carries a .torrent enclosure is the trap —
// with ParseTorrentMetadata off the enrichment reader stays nil, and no torrent
// request may leave the process no matter how many items the feed lists.
func TestPreviewProviderFeedFetcherIssuesOnlyTheRSSRequest(t *testing.T) {
	var rssRequests, torrentRequests atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".torrent") {
			torrentRequests.Add(1)
			http.NotFound(w, r)
			return
		}
		rssRequests.Add(1)
		_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><rss><channel>
			<item><guid>rel-1</guid><title>[Group] Show - 01</title><link>%[1]s/release/rel-1</link><enclosure url="%[1]s/Download/rel-1.torrent"/></item>
			<item><guid>rel-2</guid><title>[Group] Show - 02</title><link>%[1]s/release/rel-2</link><enclosure url="%[1]s/Download/rel-2.torrent"/></item>
		</channel></rss>`, server.URL)
	}))
	defer server.Close()

	items, err := (ConfiguredProviderFeedFetcher{
		Connections: fixtureConnectionSource{connections: []configurationdomain.Connection{{
			ID: "mikan-preview", Kind: configurationdomain.ConnectionMikan, Name: "preview",
			Endpoint: server.URL, State: configurationdomain.ConnectionConfigured,
		}}},
		// The composition root's preview fetcher sets exactly this.
		ParseTorrentMetadata: false,
		AllowPrivateHosts:    []string{"127.0.0.1"},
	}).FetchProviderFeed(context.Background(), providerdomain.FeedLocator{
		Provider: providerdomain.ProviderMikan, CatalogEntryRef: "3706",
		Group: providerdomain.GroupRef{Provider: providerdomain.ProviderMikan, ExternalID: "42"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rssRequests.Load() != 1 {
		t.Fatalf("RSS requests = %d, want exactly 1", rssRequests.Load())
	}
	if torrentRequests.Load() != 0 {
		t.Fatalf("preview fetched %d torrent(s), want none", torrentRequests.Load())
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want the 2 RSS entries", len(items))
	}
	// The RSS-native facts survive: title, link and enclosure are what the list
	// renders and what the snapshot publishes for a pending seed.
	for _, item := range items {
		if item.Title == "" || item.Enclosure == "" {
			t.Fatalf("preview item lost its RSS facts: %+v", item)
		}
		if len(item.ReleaseFiles) != 0 {
			t.Fatalf("an unexpanded preview item carries no file tree: %+v", item.ReleaseFiles)
		}
	}
}

func TestConfiguredProviderFeedFetcherPassesFixtureAllowlistToMikanAdapter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/RSS/Bangumi" || r.URL.Query().Get("bangumiId") != "3706" || r.URL.Query().Get("subgroupid") != "42" {
			t.Fatalf("unexpected Mikan request: %s", r.URL.String())
		}
		_, _ = fmt.Fprint(w, `<?xml version="1.0"?><rss><channel><item><guid>fixture-rel</guid><title>[Fixture] Show - 01</title><link>https://mikan.test/release/fixture-rel</link><enclosure url="https://mikan.test/Download/fixture-rel.torrent"/></item></channel></rss>`)
	}))
	defer server.Close()

	items, err := (ConfiguredProviderFeedFetcher{
		Connections: fixtureConnectionSource{connections: []configurationdomain.Connection{{
			ID: "mikan-fixture", Kind: configurationdomain.ConnectionMikan, Name: "fixture",
			Endpoint: server.URL, State: configurationdomain.ConnectionConfigured,
		}}},
		AllowPrivateHosts: []string{"127.0.0.1"},
	}).FetchProviderFeed(context.Background(), providerdomain.FeedLocator{
		Provider: providerdomain.ProviderMikan, CatalogEntryRef: "3706",
		Group: providerdomain.GroupRef{Provider: providerdomain.ProviderMikan, ExternalID: "42"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Provider != "mikan" {
		t.Fatalf("unexpected configured provider feed items: %+v", items)
	}
}
