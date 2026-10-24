package application

import (
	"strings"
	"testing"

	catalogdomain "bansoko/internal/catalog/domain"
)

// Synthetic credential: never a real tracker key.
const testTrackerKey = "0123456789abcdef0123456789abcdef"

func TestSeedLocatorDisplayRedactsPathKeys(t *testing.T) {
	for raw, want := range map[string]string{
		"https://t.example.test/torrents/download/29577." + testTrackerKey:                       "https://t.example.test/torrents/download/29577.REDACTED",
		"https://pt.example.test/download.php?id=1&passkey=" + testTrackerKey:                    "https://pt.example.test/download.php",
		"https://mikanani.me/Download/20261019/084c615f82f1e9577ce3bf115069cf3c59111065.torrent": "https://mikanani.me/Download/20261019/084c615f82f1e9577ce3bf115069cf3c59111065.torrent",
	} {
		if got := safeFeedLocatorDisplay(raw); got != want {
			t.Fatalf("safeFeedLocatorDisplay(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Snapshot rows stored before path redaction existed are redacted when read:
// the seed display and the stored torrent error reason.
func TestStoredSnapshotSeedIsRedactedOnRead(t *testing.T) {
	reason := `fetch torrent metadata: Get "https://t.example.test/torrents/download/7.` + testTrackerKey + `": EOF`
	result := mapProviderFeedSnapshot(catalogdomain.ProviderFeedSnapshot{Seeds: []catalogdomain.ProviderFeedSnapshotSeed{{
		StableSeedKey: "7", Locator: "https://t.example.test/torrents/download/7." + testTrackerKey,
		LocatorDisplay: "https://t.example.test/torrents/download/7." + testTrackerKey, Title: "Show - 01",
		TorrentState: catalogdomain.TorrentAcquisitionUnavailable, TorrentErrorReason: &reason,
	}}})
	seed := result.Seeds[0]
	if strings.Contains(seed.LocatorDisplay, testTrackerKey) || seed.TorrentErrorReason == nil || strings.Contains(*seed.TorrentErrorReason, testTrackerKey) {
		t.Fatalf("seed read keeps a key: display=%q reason=%v", seed.LocatorDisplay, seed.TorrentErrorReason)
	}
}

// Business messages built from upstream errors (feed fetch, seed expansion)
// leave through safeProviderReason; the key is redacted before the bound.
func TestProviderReasonRedactsURLs(t *testing.T) {
	got := safeProviderReason(`fetch RSS source: Get "https://pt.example.test/torrentrss.php?passkey=` + testTrackerKey + `": timeout`)
	if strings.Contains(got, testTrackerKey) || !strings.Contains(got, "passkey=REDACTED") {
		t.Fatalf("safeProviderReason = %q", got)
	}
	long := strings.Repeat("x", 500) + " https://t.example.test/torrents/download/7." + testTrackerKey
	if got := safeProviderReason(long); strings.Contains(got, testTrackerKey[:8]) || len(got) > 512 {
		t.Fatalf("bounded reason keeps part of a key: %q", got)
	}
}
