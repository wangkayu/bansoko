package application

import (
	"encoding/json"
	"testing"

	"bansoko/internal/templatecatalog/notification"
)

func TestBusinessDeliverySourceFacts(t *testing.T) {
	for _, tc := range []struct{ provider, site, page, special string }{
		{"anibt", "AniBT", "https://anibt.net/release/123", "anibt.link"},
		{"mikan", "Mikan", "https://mikanani.me/Home/Episode/abc", "mikan.link"},
		{"nyaa", "Nyaa", "https://nyaa.si/view/123", ""},
		{"jackett", "Sample Tracker", "https://tracker.example.test/details/123", ""},
		{"custom", "", "https://feed.example.test/posts/123", ""},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			evidence, _ := json.Marshal(map[string]any{"provider": tc.provider, "details_url": tc.page, "download_url": "https://cdn.example.test/123.torrent", "source_site": map[string]string{"id": "sample", "name": "Sample Tracker", "type": "private"}})
			facts := map[string]any{}
			applyBusinessDeliverySourceFacts(facts, "download_submitted", evidence)
			if facts["release.src"] != tc.provider || notificationFactString(facts, "release.site") != tc.site || facts["release.link"] != tc.page || facts["download.link"] != "https://cdn.example.test/123.torrent" {
				t.Fatalf("facts=%v", facts)
			}
			for _, key := range []string{"anibt.link", "mikan.link"} {
				want := ""
				if key == tc.special {
					want = tc.page
				}
				if notificationFactString(facts, key) != want {
					t.Fatalf("%s=%v", key, facts[key])
				}
			}
		})
	}
}

func TestBusinessDeliverySourceUnknownAndUnsafe(t *testing.T) {
	for _, raw := range []string{
		`{`, `{}`, `{"provider":"custom","details_url":"https://example.test/page","download_url":123}`, `{"provider":"provider_group"}`, `{"provider":"custom","download_url":"https://downloads.example.test/file.torrent?passkey=secret"}`,
		`{"provider":"anibt","details_url":"https://anibt.net/release/123?auth=secret","download_url":"https://downloads.example.test/file.torrent?signature=secret"}`,
		`{"provider":"custom","details_url":"https://user@tracker.example.test/details/1"}`,
	} {
		facts := map[string]any{"release.src": "stale", "release.link": "https://stale.example.test", "anibt.link": "https://anibt.net/release/1"}
		applyBusinessDeliverySourceFacts(facts, "download_failed", []byte(raw))
		for _, key := range []string{"release.link", "download.link", "anibt.link", "mikan.link"} {
			if facts[key] != nil {
				t.Fatalf("unsafe evidence=%s key=%s value=%v", raw, key, facts[key])
			}
		}
	}
	// A download proxy and a GUID are not page evidence. Missing tracker attribution
	// never changes the channel to the proxy host or manufactures a site name.
	facts := map[string]any{}
	applyBusinessDeliverySourceFacts(facts, "release_discovered", []byte(`{"provider":"jackett","guid":"123","download_url":"https://proxy.example.test/123.torrent"}`))
	if facts["release.site"] != nil || facts["release.link"] != nil || facts["release.src"] != "jackett" {
		t.Fatal(facts)
	}
	if notification.LinkValue(facts, "download.link") == "" {
		t.Fatal("independent download link missing")
	}
}

func TestBusinessDeliverySourceEventScope(t *testing.T) {
	for _, trigger := range []string{"subscription_changed", "file_displaced", "replacement_deletion_failed"} {
		facts := map[string]any{}
		applyBusinessDeliverySourceFacts(facts, trigger, []byte(`{"provider":"anibt","details_url":"https://anibt.net/release/123"}`))
		if len(facts) != 0 {
			t.Fatalf("%s gained ambiguous source facts: %v", trigger, facts)
		}
	}
}
