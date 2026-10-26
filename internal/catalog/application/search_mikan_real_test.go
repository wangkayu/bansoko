package application

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

// Opt-in live coverage of Mikan's two search endpoints, the same contract the
// AniBT real torrent test pins: the public site's answer must still match the
// shipped readers. Live rows are logged for diagnosis only and never become
// fixtures; a layout change that turns a parse silently empty fails here.

func TestMikanRealProgramSearch(t *testing.T) {
	if os.Getenv("BANSOKO_PROVIDER_OPTIN") != "1" {
		t.Skip("opt-in provider test: set BANSOKO_PROVIDER_OPTIN=1 to hit the public Mikan endpoint")
	}

	searcher := HTTPMikanSearcher{Endpoint: "https://mikanani.me", Client: &http.Client{Timeout: 30 * time.Second}}
	result, err := searcher.SearchMikanPrograms(context.Background(), "conan")
	if err != nil {
		t.Fatalf("SearchMikanPrograms failed: %v", err)
	}
	if len(result.Entries) == 0 {
		t.Fatal("the recommendation block parsed to zero entries: the live layout changed")
	}
	t.Logf("Program search returned %d entries, contract %s", len(result.Entries), result.ContractVersion)
	for i, entry := range result.Entries {
		if i >= 3 {
			break
		}
		t.Logf("  [%d] %s (ref %s, bgm %s)", i, entry.Title, entry.ProviderEntryRef, entry.BGMID)
	}
}

func TestMikanRealSeedSearch(t *testing.T) {
	if os.Getenv("BANSOKO_PROVIDER_OPTIN") != "1" {
		t.Skip("opt-in provider test: set BANSOKO_PROVIDER_OPTIN=1 to hit the public Mikan endpoint")
	}

	searcher := HTTPMikanSearcher{Endpoint: "https://mikanani.me", Client: &http.Client{Timeout: 30 * time.Second}}
	result, err := searcher.SearchMikanSeeds(context.Background(), "conan")
	if err != nil {
		t.Fatalf("SearchMikanSeeds failed: %v", err)
	}
	if len(result.Items) == 0 {
		t.Fatal("the search RSS parsed to zero usable items: the live layout changed")
	}
	if result.SourceItemCount > result.Window {
		t.Fatalf("live answer exceeded the window: %d > %d", result.SourceItemCount, result.Window)
	}
	if result.ParserVersion == "" || result.Query != "conan" {
		t.Fatalf("result evidence = %+v", result)
	}
	t.Logf("Seed search returned %d usable of %d source items, parser %s",
		len(result.Items), result.SourceItemCount, result.ParserVersion)
	for i, item := range result.Items {
		if i >= 3 {
			break
		}
		t.Logf("  [%d] %s", i, item.Title)
		t.Logf("      TorrentURL: %s", item.TorrentURL)
		t.Logf("      InfoHash: %s", item.TorrentInfoHash)
	}
}
