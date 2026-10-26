package application

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

// Opt-in live coverage of Nyaa's RSS keyword search: the public site's answer
// must still match the declared feed adapter. Live rows are logged for
// diagnosis only and never become fixtures. BANSOKO_NYAA_ENDPOINT may name a
// reachable mirror or proxy front for hosts that cannot reach nyaa.si directly.

func TestNyaaRealSeedSearch(t *testing.T) {
	if os.Getenv("BANSOKO_PROVIDER_OPTIN") != "1" {
		t.Skip("opt-in provider test: set BANSOKO_PROVIDER_OPTIN=1 to hit the public Nyaa endpoint")
	}
	endpoint := os.Getenv("BANSOKO_NYAA_ENDPOINT")
	if endpoint == "" {
		endpoint = "https://nyaa.si"
	}

	searcher := HTTPNyaaSearcher{Endpoint: endpoint, Client: &http.Client{Timeout: 45 * time.Second}}
	result, err := searcher.SearchNyaaSeeds(context.Background(), "conan 1080p")
	if err != nil {
		t.Fatalf("SearchNyaaSeeds failed: %v", err)
	}
	if len(result.Items) == 0 {
		t.Fatal("the search RSS parsed to zero usable items: the live layout changed")
	}
	if result.SourceItemCount > result.Window {
		t.Fatalf("live answer exceeded the window: %d > %d", result.SourceItemCount, result.Window)
	}
	for _, item := range result.Items {
		if item.TorrentInfoHash == "" || item.TorrentURL == "" {
			t.Fatalf("a usable item lost its identity: %+v", item)
		}
	}
	t.Logf("Seed search returned %d usable of %d source items, parser %s",
		len(result.Items), result.SourceItemCount, result.ParserVersion)
	for i, item := range result.Items {
		if i >= 3 {
			break
		}
		t.Logf("  [%d] %s size=%d hash=%s", i, item.Title, item.TorrentSizeBytes, item.TorrentInfoHash)
	}
}
