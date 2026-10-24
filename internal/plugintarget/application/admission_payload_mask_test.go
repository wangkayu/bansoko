package application

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMaskPayloadURLsKeepsAdmissionValid proves an upstream title carrying a
// URL is masked in the admission payload rather than rejecting the ledger row
// (which would roll back the media owner transaction).
func TestMaskPayloadURLsKeepsAdmissionValid(t *testing.T) {
	raw := json.RawMessage(`{"release.title":"[Group] Show - 01 http://tracker.example/a?passkey=x","anime.title":"【我推的孩子】/Oshi no Ko","list":["see https://x.test/y"]}`)
	masked := maskPayloadURLs(raw)
	if strings.Contains(string(masked), "://") || strings.Contains(string(masked), "tracker.example") {
		t.Fatalf("URL survived: %s", masked)
	}
	if !strings.Contains(string(masked), `"anime.title":"【我推的孩子】/Oshi no Ko"`) || !strings.Contains(string(masked), "[Group] Show - 01 <redacted>") {
		t.Fatalf("payload not masked in place: %s", masked)
	}
	var payload any
	if err := json.Unmarshal(masked, &payload); err != nil || unsafeBusinessPayload(payload, "") {
		t.Fatalf("masked payload still unsafe: %s err=%v", masked, err)
	}
	clean := json.RawMessage(`{"b":"x", "a":"y"}`)
	if got := maskPayloadURLs(clean); string(got) != string(clean) {
		t.Fatalf("clean payload must stay byte-for-byte: %s", got)
	}
	// A URL-like key is code-owned and still rejected by validation.
	key := maskPayloadURLs(json.RawMessage(`{"callback_url":"x"}`))
	_ = json.Unmarshal(key, &payload)
	if !unsafeBusinessPayload(payload, "") {
		t.Fatal("URL key must still be rejected")
	}
}
