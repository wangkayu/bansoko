package domain

import (
	"net/url"
	"strings"
	"testing"
)

func TestLocatorDisplayExposesOnlyCredentialURLOrigin(t *testing.T) {
	locators := []string{
		"https://user:secret@feeds.example.com/rss/private-passkey?access_token=query-secret&foo=visible#fragment",
		"https://feeds.example.com/rss/private-passkey?X-Amz-Signature=unknown-key-secret",
	}
	for _, locator := range locators {
		display := LocatorDisplay(locator)
		if display != "https://feeds.example.com" {
			t.Fatalf("locator display = %q, want safe origin", display)
		}
		parsed, err := url.Parse(display)
		if err != nil {
			t.Fatalf("parse redacted display: %v", err)
		}
		if parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			t.Fatalf("locator display retained protected URL components: %s", display)
		}
		for _, secret := range []string{"secret", "passkey", "visible", "Signature"} {
			if strings.Contains(display, secret) {
				t.Fatalf("locator display leaked %q: %s", secret, display)
			}
		}
	}
}

func TestSanitizeLocatorDisplayIsIdempotent(t *testing.T) {
	for _, display := range []string{
		"https://user:pass@example.test/private-passkey?foo=secret#fragment",
		invalidLocatorDisplay,
		opaqueLocatorDisplay,
	} {
		once := SanitizeLocatorDisplay(display)
		if twice := SanitizeLocatorDisplay(once); twice != once {
			t.Fatalf("sanitize display is not idempotent: once=%q twice=%q", once, twice)
		}
	}
}

func TestLocatorDisplayFailsClosedForOpaqueLocators(t *testing.T) {
	tests := []struct {
		name    string
		locator string
		want    string
	}{
		{name: "malformed", locator: "opaque://feed/%zz/DO_NOT_DISCLOSE_SECRET_MARKER", want: invalidLocatorDisplay},
		{name: "parseable", locator: "opaque://feed/DO_NOT_DISCLOSE_SECRET_MARKER", want: opaqueLocatorDisplay},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			display := LocatorDisplay(tc.locator)
			if display != tc.want {
				t.Fatalf("opaque locator display = %q, want fixed fallback %q", display, tc.want)
			}
			if strings.Contains(display, tc.locator) || strings.Contains(display, "DO_NOT_DISCLOSE_SECRET_MARKER") {
				t.Fatalf("opaque locator display leaked protected input: %q", display)
			}
			if len(display) > 64 {
				t.Fatalf("opaque locator fallback must stay bounded, got %d bytes", len(display))
			}
		})
	}
}

func TestValidateMetadataPatchTotalEpisodes(t *testing.T) {
	zero := 0
	positive := 12
	negative := -1

	tests := []struct {
		name          string
		totalEpisodes *int
		wantViolation bool
	}{
		{name: "nil", totalEpisodes: nil, wantViolation: false},
		{name: "zero", totalEpisodes: &zero, wantViolation: false},
		{name: "positive", totalEpisodes: &positive, wantViolation: false},
		{name: "negative", totalEpisodes: &negative, wantViolation: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			violations := ValidateMetadataPatch(nil, nil, nil, nil, tc.totalEpisodes)
			found := false
			for _, v := range violations {
				if v.Field == "total_episodes" {
					found = true
				}
			}
			if found != tc.wantViolation {
				t.Fatalf("total_episodes validation: want violation=%v, got violations=%v", tc.wantViolation, violations)
			}
		})
	}
}
