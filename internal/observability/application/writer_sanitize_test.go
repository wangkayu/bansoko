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
		SubscriptionID: "sub-1", ManagedAnimeID: "anime-1", Kind: "download_changed", Outcome: "info",
		ReasonCode: "download_submitted", SubjectType: "download_task", SubjectID: "task-1", SubjectVersion: 2,
		MessageCode: observabilityport.MessageDownloadSubmitted,
	}
}

// sanitizedName sanitizes record and encodes its params as the writer does,
// returning the stored name param.
func sanitizedName(t *testing.T, record observabilityport.ActivityRecord) (observabilityport.ActivityRecord, string) {
	t.Helper()
	got := sanitizeActivity(record)
	if err := validateActivity(got); err != nil {
		t.Fatalf("sanitized activity still rejected: %v", err)
	}
	encoded, err := encodeMessageParams(got.MessageCode, got.MessageParams)
	if err != nil {
		t.Fatalf("sanitized params rejected: %v", err)
	}
	var params map[string]string
	if err := json.Unmarshal(encoded, &params); err != nil {
		t.Fatal(err)
	}
	return got, params[observabilityport.ParamName]
}

// TestSanitizeActivityMessageParams pins the write-side policy for upstream
// text in string params: ordinary slashes survive, and a URL, absolute path or
// credential fragment is masked in place so the owner transaction still
// commits.
func TestSanitizeActivityMessageParams(t *testing.T) {
	cases := []struct {
		name, text, want, secret string
	}{
		{"real AniBT title is unchanged", realAniBTTitle, realAniBTTitle, ""},
		{"http URL is masked", "[Group] Show - 01 http://tracker.example/announce?passkey=abc [1080p]", "[Group] Show - 01 <redacted> [1080p]", "tracker.example"},
		{"home path is masked", "[Group] Show /home/alice/Downloads/show.mkv - 01", "[Group] Show <redacted> - 01", "alice"},
		{"token is masked", "[Group] Show token=0123456789abcdef - 01", "[Group] Show <redacted> - 01", "0123456789abcdef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := sanitizeBaseRecord()
			record.MessageParams = map[string]any{observabilityport.ParamName: tc.text}
			_, got := sanitizedName(t, record)
			if got != tc.want {
				t.Fatalf("sanitized name=%q, want %q", got, tc.want)
			}
			if tc.secret != "" && strings.Contains(got, tc.secret) {
				t.Fatalf("secret %q survived: %q", tc.secret, got)
			}
		})
	}
}

func TestSanitizeActivityBoundsAndControls(t *testing.T) {
	record := sanitizeBaseRecord()
	record.MessageParams = map[string]any{observabilityport.ParamName: strings.Repeat("番", 400)} // 1200 bytes, over the 256-byte bound
	record.GroupRef = "Group\r\nName"
	got, name := sanitizedName(t, record)
	if len(name) > observabilityport.MaxMessageParamBytes || !strings.HasPrefix(name, "番") || strings.ContainsRune(name, '\uFFFD') {
		t.Fatalf("name not bounded on a rune boundary: %d bytes", len(name))
	}
	if got.GroupRef != "Group  Name" {
		t.Fatalf("group ref controls not folded: %q", got.GroupRef)
	}

	// Control characters (NUL included) fold before the bound check.
	record.MessageParams = map[string]any{observabilityport.ParamName: "a\x00b"}
	if _, name := sanitizedName(t, record); strings.ContainsRune(name, 0) {
		t.Fatalf("NUL survived sanitizing: %q", name)
	}
	// Whitespace-only text is an absent optional param.
	record.MessageParams = map[string]any{observabilityport.ParamName: "   "}
	if _, name := sanitizedName(t, record); name != "" {
		t.Fatalf("blank name must be omitted, got %q", name)
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
