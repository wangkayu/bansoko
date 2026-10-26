package application

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// AniBT publishes its torrent search as anonymous Torznab. These tests pin the
// four things a snapshot depends on: the exact keyword query shape, the
// protocol-error and rate-limit refusals, attribute normalization (infohash and
// size survive; nothing is guessed), and the window semantics.

func torznabItemXML(title, guid, pubDate, infohash, size string) string {
	attrs := ""
	if size != "" {
		attrs += `<torznab:attr name="size" value="` + size + `" />`
	}
	if infohash != "" {
		attrs += `<torznab:attr name="infohash" value="` + infohash + `" />`
	}
	return `<item><title>` + title + `</title><guid isPermaLink="false">` + guid + `</guid>` +
		`<link>https://anibt.example.test/api/torrent/` + guid + `.torrent</link>` +
		`<pubDate>` + pubDate + `</pubDate>` +
		`<enclosure url="https://anibt.example.test/api/torrent/` + guid + `.torrent" length="` + size + `" type="application/x-bittorrent" />` +
		attrs + `</item>`
}

func torznabDocument(items ...string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
<channel><title>AniBT</title><newznab:response offset="0" total="` + strings.Join([]string{}, "") + `" />` +
		strings.Join(items, "") + `</channel></rss>`
}

// The query travels as one whole parameter and the request carries only the
// documented keyword function: no cat, no movie/tvsearch, no source filter.
func TestAniBTTorznabSearchSendsOneKeywordQuery(t *testing.T) {
	var gotQuery, gotRaw string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/torznab/api" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotQuery = r.URL.Query().Get("q")
		gotRaw = r.URL.Query().Get("t") + "|" + r.URL.Query().Get("limit") + "|" + r.URL.Query().Get("cat")
		io.WriteString(w, torznabDocument(torznabItemXML(
			"[示例字幕组] 示例作品 - 03 [1080p]", "rel_sample_1", "Fri, 23 Oct 2026 02:00:47 +0800",
			"fab1cfcf83daaffeb8a3cb7bb1e0329125d93b3e", "752064075")))
	}))
	defer mock.Close()

	// Leading/trailing and repeated whitespace is normalized; the remaining text,
	// including CJK and punctuation, is sent verbatim.
	result, err := (HTTPAniBTTorznabSearcher{Endpoint: mock.URL, Client: mock.Client()}).SearchAniBTKeywords(context.Background(), "  示例字幕组   示例作品 - 03  ")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if gotQuery != "示例字幕组 示例作品 - 03" {
		t.Fatalf("upstream q = %q", gotQuery)
	}
	if gotRaw != "search|100|" {
		t.Fatalf("upstream function/limit/cat = %q, want search|100| (no cat)", gotRaw)
	}
	if result.Query != "示例字幕组 示例作品 - 03" {
		t.Fatalf("result query = %q", result.Query)
	}
	if result.Window != 100 || result.SourceItemCount != 1 || len(result.Items) != 1 {
		t.Fatalf("window=%d source=%d items=%d", result.Window, result.SourceItemCount, len(result.Items))
	}
	item := result.Items[0]
	if item.TorrentInfoHash != "fab1cfcf83daaffeb8a3cb7bb1e0329125d93b3e" {
		t.Fatalf("infohash = %q", item.TorrentInfoHash)
	}
	if item.TorrentSizeBytes != 752064075 {
		t.Fatalf("size = %d", item.TorrentSizeBytes)
	}
	if item.DownloadURL() != "https://anibt.example.test/api/torrent/rel_sample_1.torrent" {
		t.Fatalf("download url = %q", item.DownloadURL())
	}
	if item.GUID != "rel_sample_1" || item.Published.IsZero() {
		t.Fatalf("guid=%q published=%v", item.GUID, item.Published)
	}
}

// A Torznab protocol error arrives inside an HTTP 200. It must never be read as
// a successful empty result.
func TestAniBTTorznabErrorFailsClosed(t *testing.T) {
	cases := map[string]string{
		"generic error": `<?xml version="1.0" encoding="UTF-8"?><error code="201" description="Incorrect parameter"/>`,
		"no items":      `<?xml version="1.0" encoding="UTF-8"?><error code="100" description="Unsupported function"/>`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				io.WriteString(w, body)
			}))
			defer mock.Close()
			_, err := (HTTPAniBTTorznabSearcher{Endpoint: mock.URL, Client: mock.Client()}).SearchAniBTKeywords(context.Background(), "示例作品")
			if !errors.Is(err, ErrTorznabError) {
				t.Fatalf("error = %v, want ErrTorznabError", err)
			}
			var protocol *TorznabError
			if !errors.As(err, &protocol) || protocol.Code == "" {
				t.Fatalf("typed torznab error missing its code: %v", err)
			}
		})
	}
}

// 429/503 carry Retry-After in both documented forms, and the wait travels with
// the typed error so a caller answers with the upstream's own number.
func TestAniBTTorznabRateLimitCarriesRetryAfter(t *testing.T) {
	for name, header := range map[string]string{"seconds": "42", "date": ""} {
		t.Run(name, func(t *testing.T) {
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", firstNonEmpty(header, time.Now().UTC().Add(30*time.Second).Format(http.TimeFormat)))
				w.WriteHeader(http.StatusTooManyRequests)
				io.WriteString(w, "slow down")
			}))
			defer mock.Close()
			_, err := (HTTPAniBTTorznabSearcher{Endpoint: mock.URL, Client: mock.Client()}).SearchAniBTKeywords(context.Background(), "x")
			var limited *ProviderRateLimited
			if !errors.As(err, &limited) {
				t.Fatalf("error = %v, want a typed rate limit", err)
			}
			if !errors.Is(err, ErrProviderRateLimited) {
				t.Fatalf("error = %v, want ErrProviderRateLimited", err)
			}
			if limited.RetryAfter <= 0 {
				t.Fatalf("Retry-After %q produced no wait", header)
			}
		})
	}
}

// A malformed body, a wrong root element and an oversized window are upstream
// failures, never a successful empty search.
func TestAniBTTorznabMalformedResponsesFailClosed(t *testing.T) {
	many := make([]string, 0, torznabSearchWindow+1)
	for index := 0; index <= torznabSearchWindow; index++ {
		many = append(many, torznabItemXML("[组] 作品 - 01", "rel_"+strings.Repeat("a", 4), "Fri, 23 Oct 2026 02:00:47 +0800", "", ""))
	}
	cases := map[string]string{
		"not xml":        "<html>bad gateway</html>",
		"wrong root":     `<?xml version="1.0"?><feed><entry/></feed>`,
		"too many items": torznabDocument(many...),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, body)
			}))
			defer mock.Close()
			_, err := (HTTPAniBTTorznabSearcher{Endpoint: mock.URL, Client: mock.Client()}).SearchAniBTKeywords(context.Background(), "x")
			if err == nil {
				t.Fatal("malformed upstream response produced a successful search")
			}
		})
	}
}

// Items without a title, a published time or a download URL are skipped with a
// reason; a page where every row is unusable is a typed feed failure rather than
// a zero-result. A row with a malformed infohash is unusable too: the snapshot
// must never store an identity it cannot verify.
func TestAniBTTorznabUnusableItemsAreReported(t *testing.T) {
	usable := torznabItemXML("[组] 作品 - 01 [1080p]", "rel_ok", "Fri, 23 Oct 2026 02:00:47 +0800", "fab1cfcf83daaffeb8a3cb7bb1e0329125d93b3e", "1000")
	cases := map[string]struct {
		items    []string
		wantSkip int
		wantErr  bool
	}{
		"partial": {items: []string{
			usable,
			`<item><title></title><guid>rel_bad</guid></item>`,
			`<item><title>[组] 作品 - 02</title><guid>rel_bad2</guid></item>`,
		}, wantSkip: 2},
		"all unusable": {items: []string{
			`<item><title></title><guid>rel_bad</guid></item>`,
			`<item><title>[组] 作品 - 02</title><guid>rel_bad2</guid></item>`,
		}, wantSkip: 2, wantErr: true},
		"bad infohash": {items: []string{
			torznabItemXML("[组] 作品 - 03", "rel_badhash", "Fri, 23 Oct 2026 02:00:47 +0800", "not-a-hash", "1000"),
		}, wantSkip: 1, wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, torznabDocument(tc.items...))
			}))
			defer mock.Close()
			result, err := (HTTPAniBTTorznabSearcher{Endpoint: mock.URL, Client: mock.Client()}).SearchAniBTKeywords(context.Background(), "x")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected a failure, got %d items", len(result.Items))
				}
				return
			}
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if len(result.Parse.Skipped) != tc.wantSkip {
				t.Fatalf("skipped = %d, want %d (%+v)", len(result.Parse.Skipped), tc.wantSkip, result.Parse.Skipped)
			}
			if len(result.Items) != 1 {
				t.Fatalf("usable items = %d, want 1", len(result.Items))
			}
		})
	}
}

// An empty but well-formed window is a real zero-result, not a failure.
func TestAniBTTorznabEmptyWindowIsZeroResult(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, torznabDocument())
	}))
	defer mock.Close()
	result, err := (HTTPAniBTTorznabSearcher{Endpoint: mock.URL, Client: mock.Client()}).SearchAniBTKeywords(context.Background(), "no-such-title")
	if err != nil {
		t.Fatalf("empty window: %v", err)
	}
	if len(result.Items) != 0 || result.SourceItemCount != 0 {
		t.Fatalf("items=%d source=%d", len(result.Items), result.SourceItemCount)
	}
}

// Retry-After parsing accepts both documented forms and never invents a wait.
func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	if got := parseRetryAfter("120", now); got != 2*time.Minute {
		t.Fatalf("seconds form = %v", got)
	}
	if got := parseRetryAfter(now.Add(45*time.Second).Format(http.TimeFormat), now); got <= 0 || got > time.Minute {
		t.Fatalf("date form = %v", got)
	}
	for _, unusable := range []string{"", "soon", "-5", "999999999", now.Add(-time.Minute).Format(http.TimeFormat)} {
		if got := parseRetryAfter(unusable, now); got != 0 {
			t.Fatalf("Retry-After %q produced %v, want no wait", unusable, got)
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
