package application

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"

	"bansoko/internal/acquisition/domain"
	subscriptiondomain "bansoko/internal/subscription/domain"
)

// Synthetic credentials: never a real tracker key.
const testTrackerKey = "0123456789abcdef0123456789abcdef"

func privateResolver(context.Context, string) ([]net.IP, error) {
	return []net.IP{net.ParseIP("127.0.0.1")}, nil
}

// A fetch error names its request URL (net/http's *url.Error). The feed and
// torrent keys never survive into the error text that reaches stored reasons,
// logs and responses.
func TestFetchErrorsRedactTrackerKeys(t *testing.T) {
	fetcher := HTTPRSSFetcher{Client: &http.Client{Transport: &http.Transport{}}, Resolver: privateResolver}
	for _, feedURL := range []string{
		"https://pt.example.test/torrentrss.php?rows=10&passkey=" + testTrackerKey,
		"https://t.example.test/rss/12." + testTrackerKey,
	} {
		_, err := fetcher.Fetch(context.Background(), subscriptiondomain.RSSLocator{URL: feedURL})
		if err == nil || strings.Contains(err.Error(), testTrackerKey) || !strings.Contains(err.Error(), "REDACTED") {
			t.Fatalf("RSS fetch error must redact the key: %v", err)
		}
	}

	reader := HTTPTorrentMetadataReader{Client: &http.Client{Transport: &http.Transport{}}, Resolver: privateResolver}
	_, err := reader.FetchPayload(context.Background(), "https://t.example.test/torrents/download/7."+testTrackerKey)
	if err == nil || strings.Contains(err.Error(), testTrackerKey) || !errors.Is(err, ErrTorrentPayloadFetch) {
		t.Fatalf("torrent fetch error must redact the key and keep its class: %v", err)
	}
}

// The per-item torrent error is shown in the RSS list and stored with the
// seed: whatever the cause says, the key is gone.
func TestTorrentItemErrorReasonIsRedacted(t *testing.T) {
	item := domain.FeedItem{}
	markTorrentItemError(&item, errors.New(`fetch torrent metadata: Get "https://pt.example.test/download.php?id=1&passkey=`+testTrackerKey+`": EOF`))
	if strings.Contains(item.TorrentErrorReason, testTrackerKey) || !strings.Contains(item.TorrentErrorReason, "passkey=REDACTED") {
		t.Fatalf("reason = %q", item.TorrentErrorReason)
	}
}
