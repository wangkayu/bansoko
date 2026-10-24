package application

import (
	"encoding/json"
	"strings"
	"testing"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/redact"
)

const realAniBTTitle = "[7³ACG] 【我推的孩子】/Oshi no Ko S01 [01-11][1080p][简繁英字幕]"

func sanitizeBaseRecord() observabilityport.ActivityRecord {
	return observabilityport.ActivityRecord{
		SubscriptionID: "sub-1", ManagedAnimeID: "anime-1", Kind: "release_processed", Outcome: "info",
		ReasonCode: "submitted", SubjectType: "release", SubjectID: "release-1", SubjectVersion: 2,
		Title: "Anime", Summary: "已提交下载",
	}
}

// TestSanitizeActivityTitles pins the write-side policy for upstream titles:
// ordinary slashes survive, and a URL, absolute path or credential fragment is
// masked in place so the owner transaction still commits.
func TestSanitizeActivityTitles(t *testing.T) {
	cases := []struct {
		name, title, want, secret string
	}{
		{"real AniBT title is unchanged", realAniBTTitle, realAniBTTitle, ""},
		{"http URL is masked", "[Group] Show - 01 http://tracker.example/announce?passkey=abc [1080p]", "[Group] Show - 01 <redacted> [1080p]", "tracker.example"},
		{"home path is masked", "[Group] Show /home/alice/Downloads/show.mkv - 01", "[Group] Show <redacted> - 01", "alice"},
		{"token is masked", "[Group] Show token=0123456789abcdef - 01", "[Group] Show <redacted> - 01", "0123456789abcdef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := sanitizeBaseRecord()
			record.Title = tc.title
			record.Summary = tc.title
			got := sanitizeActivity(record)
			if got.Title != tc.want || got.Summary != tc.want {
				t.Fatalf("sanitized title=%q summary=%q, want %q", got.Title, got.Summary, tc.want)
			}
			if tc.secret != "" && strings.Contains(got.Title, tc.secret) {
				t.Fatalf("secret %q survived: %q", tc.secret, got.Title)
			}
			if err := validateActivity(got); err != nil {
				t.Fatalf("sanitized activity still rejected: %v", err)
			}
		})
	}
}

func TestSanitizeActivityBoundsAndControls(t *testing.T) {
	record := sanitizeBaseRecord()
	record.Title = strings.Repeat("番", 400) // 1200 bytes, over the 512-byte bound
	record.GroupRef = "Group\r\nName"
	got := sanitizeActivity(record)
	if len(got.Title) > maxActivityTitleLength || !strings.HasPrefix(got.Title, "番") || strings.ContainsRune(got.Title, '�') {
		t.Fatalf("title not bounded on a rune boundary: %d bytes", len(got.Title))
	}
	if got.GroupRef != "Group  Name" {
		t.Fatalf("group ref controls not folded: %q", got.GroupRef)
	}
	if err := validateActivity(got); err != nil {
		t.Fatalf("bounded activity rejected: %v", err)
	}
}

func TestSanitizeActivityDetails(t *testing.T) {
	record := sanitizeBaseRecord()
	record.Details = json.RawMessage(`{"verdict":"submitted","source":"open /home/alice/x.mkv failed","nested":[{"url_text":"see https://example.test/a"}]}`)
	got := sanitizeActivity(record)
	if strings.Contains(string(got.Details), "alice") || strings.Contains(string(got.Details), "example.test") {
		t.Fatalf("detail fragments survived: %s", got.Details)
	}
	if !strings.Contains(string(got.Details), redact.Redacted) || !strings.Contains(string(got.Details), `"verdict":"submitted"`) {
		t.Fatalf("details not masked in place: %s", got.Details)
	}
	if err := validateActivity(got); err != nil {
		t.Fatalf("masked details rejected: %v", err)
	}

	clean := sanitizeBaseRecord()
	clean.Details = json.RawMessage(`{"b":"x", "a":"y"}`)
	if got := sanitizeActivity(clean); string(got.Details) != string(clean.Details) {
		t.Fatalf("clean details must stay byte-for-byte: %s", got.Details)
	}

	// Detail keys are produced by code; a sensitive key stays a rejection.
	key := sanitizeBaseRecord()
	key.Details = json.RawMessage(`{"access_token":"opaque"}`)
	if err := validateActivity(sanitizeActivity(key)); err == nil {
		t.Fatal("sensitive detail key must still be rejected")
	}
}

func TestSanitizeFreeTextReMasksAfterByteCut(t *testing.T) {
	// "/database" is not a known root; cut at "/data" it would become one.
	value := strings.Repeat("a", 10) + " /database"
	got := sanitizeFreeText(value, len(value)-4)
	if _, matched := redact.Apply(got, 4096, redact.Reject, ""); matched {
		t.Fatalf("byte cut exposed a sensitive fragment: %q", got)
	}
}
