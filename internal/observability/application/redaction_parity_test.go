package application

import (
	"strings"
	"testing"

	"bansoko/internal/redact"
)

func TestRedactionParityAcrossObservabilitySinks(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		secret string
	}{
		{"credential assignment", `token=topsecret`, "topsecret"},
		{"bare bearer", `bearer abc123`, "abc123"},
		{"cookie", `cookie=session-secret`, "session-secret"},
		{"passkey", `passkey=pass-secret`, "pass-secret"},
		{"dsn", `dsn=postgres://db-user:db-secret@db.example/app`, "db-secret"},
		{"magnet", `magnet:?xt=urn:btih:magnet-secret`, "magnet-secret"},
		{"URL", `https://user:url-secret@example.test/path`, "url-secret"},
		{"private path", `/private/library/private-file`, "private-file"},
		{"parent path", `../private-parent/file`, "private-parent"},
		{"panic", `panic: panic-secret`, "panic-secret"},
		{"payload dump", `payload: {"secret":"dump-secret"}`, "dump-secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			write := sanitizeFreeText(tc.input, maxErrorLength)
			runtime := safeRuntimeMessage(tc.input)
			read := sanitizeDiagnosticLastError(tc.input)
			// The validator stays reject-only for unsanitized text; the write
			// path masks the fragment first, so its output must then pass.
			if err := validateSafeText("last_error", tc.input, maxErrorLength, true); err == nil {
				t.Error("write validation accepted hostile text")
			} else if !strings.Contains(err.Error(), "contains a secret, URL, or private path") {
				t.Errorf("write validation error = %q", err)
			}
			if err := validateSafeText("last_error", write, maxErrorLength, true); err != nil {
				t.Errorf("masked write %q still rejected: %v", write, err)
			}
			if !strings.Contains(write, "<redacted>") {
				t.Errorf("write sanitizer = %q, want the redaction marker", write)
			}
			if runtime != "<redacted runtime message>" {
				t.Errorf("Runtime Log = %q, want mask marker", runtime)
			}
			if read != redact.Redacted {
				t.Errorf("diagnostics read = %q, want %q", read, redact.Redacted)
			}
			for sink, value := range map[string]string{"write": write, "runtime": runtime, "read": read} {
				if strings.Contains(value, tc.secret) {
					t.Errorf("%s sink leaked %q: %q", sink, tc.secret, value)
				}
			}
		})
	}
}

func TestRedactionPolicyCoversEverySinkMarker(t *testing.T) {
	for _, value := range []string{"token=hidden", "bearer hidden", "cookie=hidden", "passkey=hidden", "dsn=hidden", "magnet:?xt=urn:btih:hidden", "https://user:hidden@example.test/", "/private/hidden", "../hidden", "panic: hidden", `payload {"secret":"hidden"}`} {
		if _, matched := redact.Apply(value, 4096, redact.Reject, ""); !matched {
			t.Errorf("shared policy missed %q", value)
		}
	}
}
