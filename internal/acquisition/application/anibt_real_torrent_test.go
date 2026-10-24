package application

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestAniBTRealTorrentDownload(t *testing.T) {
	if os.Getenv("BANSOKO_PROVIDER_OPTIN") != "1" {
		t.Skip("opt-in provider test: set BANSOKO_PROVIDER_OPTIN=1 to hit the public AniBT endpoint")
	}

	fetcher := HTTPAniBTRSSFetcher{
		Endpoint: "https://anibt.net",
		Client:   &http.Client{Timeout: 30 * time.Second},
		TorrentReader: HTTPTorrentMetadataReader{
			Client:  &http.Client{Timeout: 30 * time.Second},
			MaxSize: 20 << 20,
		},
	}

	items, err := fetcher.FetchAnime(context.Background(), "583729", "nix-raws")
	if err != nil {
		t.Fatalf("FetchAnime failed: %v", err)
	}

	if len(items) == 0 {
		t.Fatal("No items returned")
	}

	t.Logf("Fetched %d items", len(items))
	for i, item := range items[:3] {
		t.Logf("  [%d] %s", i, item.Title)
		t.Logf("      TorrentURL: %s", item.TorrentURL)
		t.Logf("      TorrentInfoHash: %s", item.TorrentInfoHash)
		t.Logf("      TorrentMetadataURL: %s", item.TorrentMetadataURL())
		if item.TorrentPayload != nil {
			t.Logf("      Payload: %d bytes", len(item.TorrentPayload))
		}
	}
}
