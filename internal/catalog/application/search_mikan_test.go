package application

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	catalogdomain "bansoko/internal/catalog/domain"
)

// Mikan's two search surfaces are read by one versioned parser pair. The
// fixtures below are placeholder titles, placeholder groups and placeholder
// entry refs: no real upstream sample is checked into the repository.

// mikanProgramSearchHTML is the shape Home/Search serves: a recommendation block
// of program links plus the torrent rows this parser deliberately ignores.
func mikanProgramSearchHTML() string {
	return `<!DOCTYPE html>
<html><body>
<div class="search-result-recommend">
  <a href="/Home/Bangumi/1001" title="示例作品 第一季"><img data-src="/images/1001.jpg" /></a>
  <a href="/Home/Bangumi/1002" title="示例作品 第二季"><img src="/images/1002.jpg" /></a>
  <a href="/Home/Bangumi/1001" title="示例作品 第一季"><img data-src="/images/1001.jpg" /></a>
</div>
<div class="js-search-results-row">
  <a href="/Home/Bangumi/9999" title="其他区域作品">不得作为检索结果</a>
  <span>torrent row that this parser never reads</span>
  <a href="/Download/2026/example-hash.torrent">torrent</a>
  <a href="magnet:?xt=urn:btih:0000000000000000000000000000000000000000">magnet</a>
</div>
</body></html>`
}

// mikanSearchRSS is one RSS/Search answer: 3 items, one dated, one undated and
// one with no .torrent enclosure.
func mikanSearchRSS() string {
	return `<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0" xmlns:torrent="https://mikanani.me/xmlns/torrent">
<channel>
  <title>Mikan Project - 示例作品</title>
  <item>
    <title>[示例字幕组] 示例作品 - 03 [1080p][简体内嵌]</title>
    <guid isPermaLink="false">https://mikanani.me/Home/Episode/example-guid-1</guid>
    <link>https://mikanani.me/Home/Episode/example-guid-1</link>
    <enclosure url="https://mikanani.me/Download/20261023/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.torrent" length="1024" type="application/x-bittorrent" />
    <torrent:pubDate>2026-10-23T02:00:47+08:00</torrent:pubDate>
    <torrent:contentLength>1024</torrent:contentLength>
  </item>
  <item>
    <title>[示例字幕组] 示例作品 - 04 [1080p][简体内嵌]</title>
    <guid isPermaLink="false">https://mikanani.me/Home/Episode/example-guid-2</guid>
    <link>https://mikanani.me/Home/Episode/example-guid-2</link>
    <enclosure url="https://mikanani.me/Download/20261030/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.torrent" length="2048" type="application/x-bittorrent" />
  </item>
  <item>
    <title>[示例字幕组] 示例作品 - 05 无下载</title>
    <guid isPermaLink="false">https://mikanani.me/Home/Episode/example-guid-3</guid>
    <link>https://mikanani.me/Home/Episode/example-guid-3</link>
    <torrent:pubDate>2026-10-30T02:00:47+08:00</torrent:pubDate>
  </item>
</channel>
</rss>`
}

func TestMikanProgramSearchReadsOnlyTheRecommendationBlock(t *testing.T) {
	var gotPath, gotQuery string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query().Get("searchstr")
		io.WriteString(w, mikanProgramSearchHTML())
	}))
	defer mock.Close()

	result, err := (HTTPMikanSearcher{Endpoint: mock.URL, Client: mock.Client()}).SearchMikanPrograms(context.Background(), "  示例作品  ")
	if err != nil {
		t.Fatalf("program search: %v", err)
	}
	if gotPath != "/Home/Search" || gotQuery != "示例作品" {
		t.Fatalf("upstream = %s?searchstr=%s", gotPath, gotQuery)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("entries = %d, want 2 after de-duplication (%+v)", len(result.Entries), result.Entries)
	}
	if result.Entries[0].ProviderEntryRef != "1001" || result.Entries[0].Title != "示例作品 第一季" {
		t.Fatalf("first entry = %+v", result.Entries[0])
	}
	if cover := result.Entries[0].SourceHint["cover"]; cover != "/images/1001.jpg" {
		t.Fatalf("cover = %v", cover)
	}
	// The torrent rows on the same page are never read as programs.
	for _, entry := range result.Entries {
		if strings.Contains(entry.ProviderEntryRef, "Download") {
			t.Fatalf("a torrent row was read as a program: %+v", entry)
		}
	}
	if result.ContractVersion != mikanSearchContractVersion {
		t.Fatalf("contract version = %s", result.ContractVersion)
	}
}

// A missing recommendation block is a schema mismatch. A program link elsewhere
// on the page is never promoted into a search result as a fallback.
func TestMikanProgramSearchStructureChangeFailsClosed(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<html><body><div class="js-search-results-row"><a href="/Home/Bangumi/1001" title="页内其他作品">其他区域</a></div></body></html>`)
	}))
	defer mock.Close()
	_, err := (HTTPMikanSearcher{Endpoint: mock.URL, Client: mock.Client()}).SearchMikanPrograms(context.Background(), "示例作品")
	var readError *catalogdomain.ReadError
	if !errors.As(err, &readError) || readError.Code != catalogdomain.ReadFailureSchemaMismatch {
		t.Fatalf("error = %v, want a provider schema mismatch", err)
	}
}

func TestMikanSeedSearchNormalizesRSSItems(t *testing.T) {
	var gotPath, gotQuery string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query().Get("searchstr")
		io.WriteString(w, mikanSearchRSS())
	}))
	defer mock.Close()

	result, err := (HTTPMikanSearcher{Endpoint: mock.URL, Client: mock.Client()}).SearchMikanSeeds(context.Background(), "示例字幕组  示例作品")
	if err != nil {
		t.Fatalf("seed search: %v", err)
	}
	if gotPath != "/RSS/Search" || gotQuery != "示例字幕组 示例作品" {
		t.Fatalf("upstream = %s?searchstr=%s", gotPath, gotQuery)
	}
	if len(result.Items) != 1 {
		t.Fatalf("usable items = %d, want 1 (%+v)", len(result.Items), result.Parse.Skipped)
	}
	item := result.Items[0]
	if item.Provider != "mikan" {
		t.Fatalf("provider = %q", item.Provider)
	}
	if item.Published.IsZero() || !strings.Contains(item.DownloadURL(), ".torrent") {
		t.Fatalf("item = %+v", item)
	}
	if result.ParserVersion != mikanSearchContractVersion || result.Window != mikanSearchWindow {
		t.Fatalf("parser=%s window=%d", result.ParserVersion, result.Window)
	}
	// The undated row and the row without a .torrent enclosure are reported, not
	// silently dropped.
	reasons := map[string]bool{}
	for _, skipped := range result.Parse.Skipped {
		reasons[skipped.Reason] = true
	}
	// The undated row is reported by this reader; the row without a .torrent
	// enclosure is reported by the declared Mikan adapter.
	if !reasons["published_missing"] || !reasons["site_format_mismatch"] {
		t.Fatalf("skip reasons = %v, want published_missing and site_format_mismatch", reasons)
	}
}

// A search answer whose every row is unusable is the shared typed feed failure,
// not a successful empty search.
func TestMikanSeedSearchAllUnusableFailsClosed(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><rss version="2.0"><channel><title>Mikan</title>
<item><title>[示例字幕组] 无下载</title><guid>g1</guid><link>https://mikanani.me/Home/Episode/g1</link><torrent:pubDate>2026-10-30T02:00:47+08:00</torrent:pubDate></item>
</channel></rss>`)
	}))
	defer mock.Close()
	_, err := (HTTPMikanSearcher{Endpoint: mock.URL, Client: mock.Client()}).SearchMikanSeeds(context.Background(), "示例作品")
	var failure *acquisitiondomain.FeedFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want a typed feed failure", err)
	}
}

// The 100-item bound applies to the upstream answer, not just the usable rows:
// invalid rows cannot hide an over-window response from the fail-closed check.
func TestMikanSeedSearchRefusesOverWindowSourceAnswer(t *testing.T) {
	valid := `<item><title>[示例字幕组] 示例作品 - 03</title><guid>valid</guid><link>https://mikanani.me/Home/Episode/valid</link><enclosure url="https://mikanani.me/Download/20261023/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.torrent" length="1024" type="application/x-bittorrent" /><torrent:pubDate>2026-10-23T02:00:47+08:00</torrent:pubDate></item>`
	undated := `<item><title>[示例字幕组] 示例作品 - 04</title><guid>undated</guid><link>https://mikanani.me/Home/Episode/undated</link><enclosure url="https://mikanani.me/Download/20261023/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.torrent" length="1024" type="application/x-bittorrent" /></item>`
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<rss version="2.0" xmlns:torrent="https://mikanani.me/xmlns/torrent"><channel>`+strings.Repeat(valid, 100)+undated+`</channel></rss>`)
	}))
	defer mock.Close()
	_, err := (HTTPMikanSearcher{Endpoint: mock.URL, Client: mock.Client()}).SearchMikanSeeds(context.Background(), "示例作品")
	if !errors.Is(err, ErrProviderUpstream) || !strings.Contains(err.Error(), "101 items") {
		t.Fatalf("over-window source answer: %v", err)
	}
}

// A malformed body and a non-2xx status are upstream failures.
func TestMikanSearchUpstreamFailuresAreTyped(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"unreadable rss":  {status: http.StatusOK, body: "<html>not a feed</html>"},
		"upstream 500":    {status: http.StatusInternalServerError, body: "boom"},
		"unreadable html": {status: http.StatusOK, body: "\x00\x01"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer mock.Close()
			searcher := HTTPMikanSearcher{Endpoint: mock.URL, Client: mock.Client()}
			if _, err := searcher.SearchMikanSeeds(context.Background(), "x"); err == nil {
				t.Fatal("seed search accepted a malformed upstream answer")
			}
			if _, err := searcher.SearchMikanPrograms(context.Background(), "x"); err == nil {
				t.Fatal("program search accepted a malformed upstream answer")
			}
		})
	}
}

// mikanBangumiRef accepts only a normalized positive bangumi id path.
func TestMikanBangumiRef(t *testing.T) {
	for href, want := range map[string]string{
		"/Home/Bangumi/1001":                  "1001",
		"/Home/Bangumi/1001/":                 "1001",
		"/Home/Bangumi/1001?x=1":              "1001",
		"https://mikanani.me/Home/Bangumi/42": "42",
	} {
		if got, ok := mikanBangumiRef(href); !ok || got != want {
			t.Fatalf("href %q = %q,%v want %q", href, got, ok, want)
		}
	}
	for _, href := range []string{"/Home/Bangumi/0", "/Home/Bangumi/abc", "/Home/Bangumi/01001", "/Home/Episode/1", ""} {
		if got, ok := mikanBangumiRef(href); ok {
			t.Fatalf("href %q was accepted as %q", href, got)
		}
	}
}
