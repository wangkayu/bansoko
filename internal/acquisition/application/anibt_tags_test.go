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

// The provider and the structured tags are feed facts: the refresh's pre-fetch
// hook already sees them, so it can judge an item by the AniBT tag row before
// its .torrent is fetched. The source facts, which carry the torrent's
// identity, are still built after the fetch.
func TestHTTPAniBTRSSFetcherTagsItemsBeforeTheTorrentFetch(t *testing.T) {
	payload, err := os.ReadFile("../domain/testdata/anibt-rss-1.0.xml")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	var seen []domain.FeedItem
	exchange := &FeedExchange{ReuseTorrentMetadata: func(item domain.FeedItem) (domain.FeedItem, bool) {
		seen = append(seen, item)
		return item, true
	}}
	fetcher := HTTPAniBTRSSFetcher{Endpoint: server.URL, AllowPrivateHosts: []string{"127.0.0.1"}, TorrentReader: HTTPTorrentMetadataReader{AllowPrivateHosts: []string{"127.0.0.1"}}}
	items, err := fetcher.FetchAnime(WithFeedExchange(context.Background(), exchange), "517057", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 || len(seen) != len(items) {
		t.Fatalf("pre-fetch hook saw %d items, fetch returned %d", len(seen), len(items))
	}
	for index, item := range seen {
		if item.Provider != "anibt" || item.AniBTTags == nil {
			t.Fatalf("pre-fetch item %q provider=%q tags=%v, want the AniBT feed facts", item.Title, item.Provider, item.AniBTTags)
		}
		if !reflect.DeepEqual(item.AniBTTags, items[index].AniBTTags) {
			t.Fatalf("pre-fetch tags %v differ from the fetched item's %v", item.AniBTTags, items[index].AniBTTags)
		}
		if len(items[index].SourceFacts) == 0 {
			t.Fatalf("fetched item %q lost its source facts", items[index].Title)
		}
	}
}
