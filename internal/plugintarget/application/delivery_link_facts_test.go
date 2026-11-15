package application

import (
	"strings"
	"testing"

	"bansoko/internal/templatecatalog/notification"
)

func TestBusinessDeliveryLinkFactsDeriveProviderAndDownloadURLs(t *testing.T) {
	for _, tc := range []struct {
		name         string
		provider     string
		internalJSON string
		wantKey      string
		wantURL      string
	}{
		{
			name: "AniBT release page", provider: "anibt",
			internalJSON: `{"link":"https://anibt.net/release/123","download_url":"https://cdn.example.test/123.torrent"}`,
			wantKey:      notification.AniBTLinkVariable, wantURL: "https://anibt.net/release/123",
		},
		{
			name: "AniBT numeric GUID fallback", provider: "anibt",
			internalJSON: `{"guid":"123"}`,
			wantKey:      notification.AniBTLinkVariable, wantURL: "https://anibt.net/release/123",
		},
		{
			name: "Mikan release page", provider: "mikan",
			internalJSON: `{"link":"https://mikanani.me/Home/Episode/ABCDEF"}`,
			wantKey:      notification.MikanLinkVariable, wantURL: "https://mikanani.me/Home/Episode/ABCDEF",
		},
		{
			name: "Mikan info hash fallback", provider: "mikan",
			internalJSON: `{"torrent_info_hash_v1":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`,
			wantKey:      notification.MikanLinkVariable, wantURL: "https://mikanani.me/Home/Episode/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facts := map[string]any{"release.id": "release-1"}
			applyBusinessDeliveryLinkFacts(facts, tc.provider, []byte(tc.internalJSON))
			if got := facts[tc.wantKey]; got != tc.wantURL {
				t.Fatalf("%s = %#v, want %q", tc.wantKey, got, tc.wantURL)
			}
		})
	}
}

func TestBusinessDeliveryDownloadLinkSkipsCredentials(t *testing.T) {
	facts := map[string]any{}
	applyBusinessDeliveryLinkFacts(facts, "custom", []byte(`{
		"download_url":"https://downloads.example.test/1.torrent?passkey=secret",
		"torrent_url":"https://user:pass@downloads.example.test/2.torrent",
		"enclosure":"https://downloads.example.test/3.torrent?token=secret"
	}`))
	if _, exists := facts[notification.DownloadLinkVariable]; exists {
		t.Fatalf("credentialed download URL was exported: %#v", facts[notification.DownloadLinkVariable])
	}

	applyBusinessDeliveryLinkFacts(facts, "custom", []byte(`{
		"download_url":"https://downloads.example.test/1.torrent?token=secret",
		"torrent_url":"https://downloads.example.test/2.torrent"
	}`))
	if got := facts[notification.DownloadLinkVariable]; got != "https://downloads.example.test/2.torrent" {
		t.Fatalf("safe fallback = %#v", got)
	}
}

func TestBusinessDeliveryLinkFactsFailClosedOnMalformedSnapshot(t *testing.T) {
	facts := map[string]any{"release.src": "anibt"}
	applyBusinessDeliveryLinkFacts(facts, "", []byte(`{"link":`))
	for _, key := range []string{notification.AniBTLinkVariable, notification.MikanLinkVariable, notification.DownloadLinkVariable} {
		if value, exists := facts[key]; exists {
			t.Fatalf("malformed snapshot produced %s=%#v", key, value)
		}
	}
}

func TestBusinessDeliveryLinkFactsNeverExposeCredentialNames(t *testing.T) {
	facts := map[string]any{}
	applyBusinessDeliveryLinkFacts(facts, "anibt", []byte(`{
		"link":"https://anibt.net/release/123?auth=secret",
		"download_url":"https://downloads.example.test/1.torrent?signature=secret"
	}`))
	encoded := strings.Join([]string{
		notificationFactString(facts, notification.AniBTLinkVariable),
		notificationFactString(facts, notification.DownloadLinkVariable),
	}, "\n")
	if strings.Contains(encoded, "secret") || strings.Contains(encoded, "auth=") || strings.Contains(encoded, "signature=") {
		t.Fatalf("credential material escaped into derived links: %q", encoded)
	}
}
