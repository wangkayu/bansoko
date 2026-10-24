package domain

import (
	"strings"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
)

// A release's stored identity key never keeps a tracker URL: RSS refresh and
// one-shot admission (whose item falls back to the seed's torrent URL) both
// store this form. Synthetic key.
func TestPublicStableItemKeyDigestsURLIdentities(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"
	torrentURL := "https://t.example.test/torrents/download/7." + key
	for name, item := range map[string]acquisitiondomain.FeedItem{
		"enclosure fallback (one-shot seed)": {Title: "Show - 01", Enclosure: torrentURL, TorrentURL: torrentURL},
		"guid that is a URL":                 {Title: "Show - 01", GUID: "https://pt.example.test/details.php?id=1&passkey=" + key},
		"link fallback":                      {Title: "Show - 01", Link: torrentURL},
	} {
		t.Run(name, func(t *testing.T) {
			kind, value := PublicStableItemKey(item)
			if strings.Contains(value, key) || !strings.HasPrefix(value, "sha256:") || kind == "guid" {
				t.Fatalf("PublicStableItemKey = %s %q", kind, value)
			}
			if _, again := PublicStableItemKey(item); again != value {
				t.Fatal("the digest must be stable for dedupe")
			}
		})
	}
	if kind, value := PublicStableItemKey(acquisitiondomain.FeedItem{GUID: "29577", TorrentURL: torrentURL}); kind != "guid" || value != "29577" {
		t.Fatalf("a plain GUID is kept: %s %q", kind, value)
	}
}
