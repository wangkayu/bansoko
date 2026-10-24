package application

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"bansoko/internal/acquisition/domain"
)

func TestFeedExchangeConditionalRequestAndTorrentReuse(t *testing.T) {
	var torrentRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rss/anime.xml":
			if r.Header.Get("If-None-Match") == `"v1"` && r.Header.Get("If-Modified-Since") == "Sat, 10 Oct 2026 00:00:00 GMT" {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Last-Modified", "Sat, 10 Oct 2026 00:00:00 GMT")
			_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><item><guid>g-1</guid><title>Show 01</title><enclosure url="http://` + r.Host + `/1.torrent" type="application/x-bittorrent"/></item></channel></rss>`))
		case "/1.torrent":
			torrentRequests.Add(1)
			_, _ = w.Write([]byte("d4:infod4:name15:show-01.torrent6:lengthi1eee"))
		}
	}))
	defer server.Close()
	fetcher := HTTPAniBTRSSFetcher{Endpoint: server.URL, AllowPrivateHosts: []string{"127.0.0.1"}, TorrentReader: HTTPTorrentMetadataReader{AllowPrivateHosts: []string{"127.0.0.1"}}}

	// Without an exchange the fetch is unconditional and enriches every item.
	if _, err := fetcher.FetchAnime(context.Background(), "1", "g"); err != nil || torrentRequests.Load() != 1 {
		t.Fatalf("plain fetch err=%v torrent=%d", err, torrentRequests.Load())
	}

	first := &FeedExchange{ReuseTorrentMetadata: func(item domain.FeedItem) (domain.FeedItem, bool) { return item, true }}
	items, err := fetcher.FetchAnime(WithFeedExchange(context.Background(), first), "1", "g")
	if err != nil || len(items) != 1 {
		t.Fatalf("exchange fetch items=%d err=%v", len(items), err)
	}
	if torrentRequests.Load() != 1 {
		t.Fatalf("reuse hook did not prevent the torrent download: %d", torrentRequests.Load())
	}
	if first.ResponseETag != `"v1"` || first.ResponseLastModified == "" {
		t.Fatalf("response validators not recorded: %+v", first)
	}

	second := &FeedExchange{ETag: first.ResponseETag, LastModified: first.ResponseLastModified}
	if _, err := fetcher.FetchAnime(WithFeedExchange(context.Background(), second), "1", "g"); !errors.Is(err, ErrFeedNotModified) {
		t.Fatalf("conditional fetch err=%v, want ErrFeedNotModified", err)
	}
}
