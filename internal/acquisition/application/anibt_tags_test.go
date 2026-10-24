package application

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"bansoko/internal/acquisition/domain"
)

func TestHTTPAniBTRSSFetcherAttachesStructuredTags(t *testing.T) {
	payload, err := os.ReadFile("../domain/testdata/anibt-rss-1.0.xml")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	items, err := (HTTPAniBTRSSFetcher{Endpoint: server.URL, AllowPrivateHosts: []string{"127.0.0.1"}}).FetchAnime(context.Background(), "517057", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d", len(items))
	}
	for _, item := range items {
		if item.AniBTTags == nil {
			t.Fatalf("AniBT item %q has no tag set", item.Title)
		}
	}
	if want := []string{"resolution:720p", "subtitle:NONE", "format:MP4", "codec:AVC", "source:WEB-DL"}; !reflect.DeepEqual(items[2].AniBTTags.Tags, want) {
		t.Fatalf("tags = %#v, want %#v", items[2].AniBTTags.Tags, want)
	}
	if items[3].AniBTTags.Diagnostic != domain.AniBTTagDiagnosticTagsAbsent || len(items[3].AniBTTags.Tags) != 0 {
		t.Fatalf("tag-less item = %#v", items[3].AniBTTags)
	}
}

// A legacy (pre-namespace) AniBT feed still parses; its releases carry an
// empty tag set and a diagnostic instead of failing the refresh.
func TestHTTPAniBTRSSFetcherLegacyFeedYieldsEmptyTags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss><channel><item><guid>rel-1</guid><title>[Group] Show - 01</title><link>https://anibt.test/release/rel-1</link><enclosure url="https://anibt.test/api/torrent/rel-1.torrent" length="123"/><torrent xmlns="https://anibt.moe/xmlns/0.1/"><infohash>abc</infohash></torrent></item></channel></rss>`))
	}))
	defer server.Close()
	items, err := (HTTPAniBTRSSFetcher{Endpoint: server.URL, AllowPrivateHosts: []string{"127.0.0.1"}}).FetchAnime(context.Background(), "543360", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].AniBTTags == nil || len(items[0].AniBTTags.Tags) != 0 || items[0].AniBTTags.Diagnostic != domain.AniBTTagDiagnosticNamespaceLegacy {
		t.Fatalf("legacy item = %#v", items)
	}
}
