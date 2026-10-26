package application

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	catalogdomain "bansoko/internal/catalog/domain"
)

// The fixtures are hand-written placeholder rows: placeholder titles and
// group names, and infohashes that do not belong to any real torrent.

func nyaaItemXML(id int, title, hash, size string) string {
	return fmt.Sprintf(`<item>
  <title>%s</title>
  <link>https://nyaa.si/download/%d.torrent</link>
  <guid isPermaLink="true">https://nyaa.si/view/%d</guid>
  <pubDate>Mon, 19 Oct 2026 11:28:03 -0000</pubDate>
  <nyaa:seeders>12</nyaa:seeders>
  <nyaa:leechers>1</nyaa:leechers>
  <nyaa:infoHash>%s</nyaa:infoHash>
  <nyaa:categoryId>1_2</nyaa:categoryId>
  <nyaa:size>%s</nyaa:size>
</item>`, title, id, id, hash, size)
}

func nyaaFeedXML(items ...string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<rss xmlns:atom="http://www.w3.org/2005/Atom" xmlns:nyaa="https://nyaa.si/xmlns/nyaa" version="2.0">
<channel>
  <title>Nyaa - "keyword" - Torrent File RSS</title>
  <link>https://nyaa.si/</link>
` + strings.Join(items, "\n") + `
</channel></rss>`
}

func nyaaHash(n int) string { return fmt.Sprintf("%040x", n) }

func nyaaServer(t *testing.T, status int, body string, capture func(*http.Request)) *httptest.Server {
	t.Helper()
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			capture(r)
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(mock.Close)
	return mock
}

func TestNyaaSeedSearchNormalizesRSSItems(t *testing.T) {
	var seen *http.Request
	mock := nyaaServer(t, 200, nyaaFeedXML(
		nyaaItemXML(1001, "[示例字幕组] 示例作品 - 03 [1080p]", nyaaHash(1), "1.5 GiB"),
		nyaaItemXML(1002, "[SampleGroup] Sample Work - 04 (WEB 1080p)", nyaaHash(2), "700.0 MiB"),
	), func(r *http.Request) { seen = r.Clone(context.Background()) })

	result, err := (HTTPNyaaSearcher{Endpoint: mock.URL, Client: mock.Client()}).SearchNyaaSeeds(context.Background(), "  示例作品 & 03+  ")
	if err != nil {
		t.Fatal(err)
	}
	q := seen.URL.Query()
	if seen.URL.Path != "/" || q.Get("page") != "rss" || q.Get("c") != "1_0" || q.Get("f") != "0" || q.Get("q") != "示例作品 & 03+" || len(q) != 4 {
		t.Fatalf("request = %s", seen.URL.String())
	}
	if len(result.Items) != 2 || result.SourceItemCount != 2 || result.Window != 75 || result.ParserVersion != nyaaSearchContractVersion || result.Query != "示例作品 & 03+" {
		t.Fatalf("result = %+v", result)
	}
	first := result.Items[0]
	if first.Provider != "nyaa" || first.TorrentURL != "https://nyaa.si/download/1001.torrent" || first.TorrentInfoHash != nyaaHash(1) || first.GUID != "https://nyaa.si/view/1001" {
		t.Fatalf("first = %+v", first)
	}
	if first.TorrentSizeBytes != 1610612736 || result.Items[1].TorrentSizeBytes != 734003200 {
		t.Fatalf("sizes = %d, %d", first.TorrentSizeBytes, result.Items[1].TorrentSizeBytes)
	}
	if first.Published.IsZero() {
		t.Fatal("published time was dropped")
	}
	seeds, err := mapFeedItemsToSnapshotSeeds(result.Items)
	if err != nil {
		t.Fatalf("search items must map to snapshot seeds: %v", err)
	}
	if len(seeds) != 2 || seeds[0].InfoHash == nil || *seeds[0].InfoHash != nyaaHash(1) || seeds[0].SizeBytes == nil || *seeds[0].SizeBytes != 1610612736 {
		t.Fatalf("seeds = %+v", seeds)
	}
	// The snapshot write accepts nyaa as a search provider (full persistence is
	// covered by the PG snapshot constraint test).
	write := catalogdomain.ProviderFeedSnapshotWrite{Provider: "nyaa", ProviderEntryRef: "search-scope:" + strings.Repeat("a", 64), ProviderGroupRef: "search-query:" + strings.Repeat("b", 64), SnapshotKind: catalogdomain.FeedSnapshotKindSearch, SearchParserVersion: result.ParserVersion}
	if err := write.Validate(); err != nil && strings.Contains(err.Error(), "provider") && !strings.Contains(err.Error(), "timestamps") && !strings.Contains(err.Error(), "hash") {
		t.Fatalf("nyaa search snapshot provider rejected: %v", err)
	}
}

func TestNyaaSeedSearchEmptyChannelIsEmptySuccess(t *testing.T) {
	mock := nyaaServer(t, 200, nyaaFeedXML(), nil)
	result, err := (HTTPNyaaSearcher{Endpoint: mock.URL, Client: mock.Client()}).SearchNyaaSeeds(context.Background(), "示例作品")
	if err != nil {
		t.Fatalf("an empty channel is a valid empty search: %v", err)
	}
	if len(result.Items) != 0 || result.SourceItemCount != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestNyaaSeedSearchSkipsRowsWithoutIdentityAndFailsClosedWhenNoneUsable(t *testing.T) {
	missingHash := nyaaItemXML(2001, "[示例字幕组] 缺哈希 - 01", nyaaHash(1), "1.0 GiB")
	missingHash = strings.Replace(missingHash, "<nyaa:infoHash>"+nyaaHash(1)+"</nyaa:infoHash>", "", 1)
	notTorrent := strings.Replace(nyaaItemXML(2002, "[示例字幕组] 磁力 - 01", nyaaHash(2), "1.0 GiB"),
		"https://nyaa.si/download/2002.torrent", "magnet:?xt=urn:btih:"+nyaaHash(2), 1)

	mixed := nyaaServer(t, 200, nyaaFeedXML(nyaaItemXML(2003, "[示例字幕组] 可用 - 02", nyaaHash(3), "1.0 GiB"), missingHash, notTorrent), nil)
	result, err := (HTTPNyaaSearcher{Endpoint: mixed.URL, Client: mixed.Client()}).SearchNyaaSeeds(context.Background(), "示例作品")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.SourceItemCount != 3 || len(result.Parse.Skipped) != 2 {
		t.Fatalf("usable=%d source=%d skipped=%d", len(result.Items), result.SourceItemCount, len(result.Parse.Skipped))
	}

	none := nyaaServer(t, 200, nyaaFeedXML(missingHash, notTorrent), nil)
	_, err = (HTTPNyaaSearcher{Endpoint: none.URL, Client: none.Client()}).SearchNyaaSeeds(context.Background(), "示例作品")
	var failure *acquisitiondomain.FeedFailure
	if !errors.As(err, &failure) {
		t.Fatalf("a page of only unusable rows must be a typed feed failure, got %v", err)
	}
}

func TestNyaaSeedSearchRefusesNonDownloadLinks(t *testing.T) {
	for name, link := range map[string]string{
		"magnet":     "magnet:?xt=urn:btih:" + nyaaHash(5),
		"view page":  "https://nyaa.si/view/3001",
		"plain page": "https://nyaa.si/",
	} {
		t.Run(name, func(t *testing.T) {
			item := strings.Replace(nyaaItemXML(3001, "[示例字幕组] 示例作品 - 01", nyaaHash(5), "1.0 GiB"), "https://nyaa.si/download/3001.torrent", link, 1)
			mock := nyaaServer(t, 200, nyaaFeedXML(item), nil)
			_, err := (HTTPNyaaSearcher{Endpoint: mock.URL, Client: mock.Client()}).SearchNyaaSeeds(context.Background(), "示例作品")
			if err == nil {
				t.Fatal("a link that is not a .torrent download must not become a seed")
			}
		})
	}
}

func TestNyaaSeedSearchSizeParsing(t *testing.T) {
	cases := map[string]int64{
		"1.0 GiB":   1073741824,
		"512.5 MiB": 537395200,
		"3 KiB":     3072,
		"2.0 TiB":   2199023255552,
	}
	for text, want := range cases {
		mock := nyaaServer(t, 200, nyaaFeedXML(nyaaItemXML(4001, "[示例字幕组] 示例作品 - 01", nyaaHash(7), text)), nil)
		result, err := (HTTPNyaaSearcher{Endpoint: mock.URL, Client: mock.Client()}).SearchNyaaSeeds(context.Background(), "示例作品")
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		if got := result.Items[0].TorrentSizeBytes; got != want {
			t.Fatalf("%s = %d, want %d", text, got, want)
		}
	}
	// An unreadable size leaves the size unknown instead of inventing one.
	mock := nyaaServer(t, 200, nyaaFeedXML(nyaaItemXML(4002, "[示例字幕组] 示例作品 - 01", nyaaHash(8), "many")), nil)
	result, err := (HTTPNyaaSearcher{Endpoint: mock.URL, Client: mock.Client()}).SearchNyaaSeeds(context.Background(), "示例作品")
	if err != nil || result.Items[0].TorrentSizeBytes != 0 {
		t.Fatalf("unreadable size: %+v %v", result.Items, err)
	}
}

func TestNyaaSeedSearchRefusesOverWindowAnswer(t *testing.T) {
	items := make([]string, 0, 76)
	for i := 0; i < 76; i++ {
		items = append(items, nyaaItemXML(5000+i, fmt.Sprintf("[示例字幕组] 示例作品 - %02d", i), nyaaHash(100+i), "1.0 GiB"))
	}
	mock := nyaaServer(t, 200, nyaaFeedXML(items...), nil)
	_, err := (HTTPNyaaSearcher{Endpoint: mock.URL, Client: mock.Client()}).SearchNyaaSeeds(context.Background(), "示例作品")
	if !errors.Is(err, ErrProviderUpstream) {
		t.Fatalf("an over-window answer is not the shape the site promises: %v", err)
	}
	exact := nyaaServer(t, 200, nyaaFeedXML(items[:75]...), nil)
	result, err := (HTTPNyaaSearcher{Endpoint: exact.URL, Client: exact.Client()}).SearchNyaaSeeds(context.Background(), "示例作品")
	if err != nil || len(result.Items) != 75 {
		t.Fatalf("a full window is valid: %d %v", len(result.Items), err)
	}
}

func TestNyaaSearchUpstreamFailuresAreTyped(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		rate   bool
		format bool
	}{
		{"rate limited", 429, "slow down", true, false},
		{"unavailable", 503, "busy", true, false},
		{"server error", 500, "boom", false, false},
		{"html challenge page", 200, "<!DOCTYPE html><html><body>Just a moment...</body></html>", false, true},
		{"not xml", 200, "plain text", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := nyaaServer(t, tc.status, tc.body, nil)
			_, err := (HTTPNyaaSearcher{Endpoint: mock.URL, Client: mock.Client()}).SearchNyaaSeeds(context.Background(), "示例作品")
			var limited *ProviderRateLimited
			var failure *acquisitiondomain.FeedFailure
			switch {
			case tc.format && !errors.As(err, &failure):
				t.Fatalf("a changed site format is a typed feed failure, got %v", err)
			case tc.format:
			case tc.rate && !errors.As(err, &limited):
				t.Fatalf("want ProviderRateLimited, got %v", err)
			case !tc.rate && !errors.Is(err, ErrProviderUpstream):
				t.Fatalf("want ErrProviderUpstream, got %v", err)
			}
		})
	}
}

func TestNyaaSeedSearchRejectsBlankQueryAndBadEndpoint(t *testing.T) {
	if _, err := (HTTPNyaaSearcher{Endpoint: "https://nyaa.si"}).SearchNyaaSeeds(context.Background(), "   "); !errors.Is(err, ErrSearchQueryInvalid) {
		t.Fatalf("blank query: %v", err)
	}
	for _, endpoint := range []string{"", "ftp://nyaa.si", "https://user:pw@nyaa.si", "nyaa.si"} {
		if _, err := (HTTPNyaaSearcher{Endpoint: endpoint}).SearchNyaaSeeds(context.Background(), "示例作品"); err == nil {
			t.Fatalf("endpoint %q must be refused", endpoint)
		}
	}
}

func TestSearchProviderSetKeepsNyaaOutOfCatalogDiscovery(t *testing.T) {
	if err := validateSearchProvider("nyaa"); err != nil {
		t.Fatal(err)
	}
	if err := validateCatalogProvider("nyaa"); err == nil {
		t.Fatal("nyaa is a search source only; catalog and discovery must still refuse it")
	}
	feed := catalogdomain.ProviderFeedSnapshotWrite{Provider: "nyaa", ProviderEntryRef: "entry-1", ProviderGroupRef: "group-1"}
	if err := feed.Validate(); err == nil {
		t.Fatal("a provider feed snapshot must refuse nyaa")
	}
}
