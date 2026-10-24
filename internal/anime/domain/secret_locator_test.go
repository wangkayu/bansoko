package domain

import (
	"testing"

	"bansoko/internal/redact"
)

func secretSource(kind SourceKind, locator string, display *string) RSSSourceDraft {
	return RSSSourceDraft{SourceKind: kind, Locator: locator, LocatorDisplay: display, GroupName: "g", Title: "t"}
}

func displayOf(value string) *string { return &value }

func TestResolveSecretLocatorsKeepsTheSameSourceOnly(t *testing.T) {
	const (
		first  = "https://a.example.test/rss?passkey=one"
		second = "https://b.example.test/rss?passkey=two"
	)
	current := AnimeDraft{RssSources: []RSSSourceDraft{
		secretSource(SourceKindCustomRss, first, nil),
		secretSource(SourceKindCustomRss, second, nil),
	}}
	placeholder := redact.SecretPlaceholder

	cases := []struct {
		name    string
		sources []RSSSourceDraft
		want    []string
	}{
		{
			name: "display picks the stored source",
			sources: []RSSSourceDraft{
				secretSource(SourceKindCustomRss, placeholder, displayOf("https://b.example.test")),
				secretSource(SourceKindCustomRss, placeholder, displayOf("https://a.example.test")),
			},
			want: []string{second, first},
		},
		{
			name:    "no display with two stored sources is ambiguous",
			sources: []RSSSourceDraft{secretSource(SourceKindCustomRss, placeholder, nil)},
			want:    []string{placeholder},
		},
		{
			name:    "a display naming another host matches nothing",
			sources: []RSSSourceDraft{secretSource(SourceKindCustomRss, placeholder, displayOf("https://c.example.test"))},
			want:    []string{placeholder},
		},
		{
			name: "two placeholders claiming one stored source both stay",
			sources: []RSSSourceDraft{
				secretSource(SourceKindCustomRss, placeholder, displayOf("https://a.example.test")),
				secretSource(SourceKindCustomRss, placeholder, displayOf("https://a.example.test")),
			},
			want: []string{placeholder, placeholder},
		},
		{
			name: "real locators pass untouched",
			sources: []RSSSourceDraft{
				secretSource(SourceKindCustomRss, "https://new.example.test/rss", nil),
				secretSource(SourceKindCustomRss, placeholder, displayOf("https://a.example.test")),
			},
			want: []string{"https://new.example.test/rss", first},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			incoming := AnimeDraft{RssSources: tc.sources}
			resolved := ResolveSecretLocators(incoming, current)
			for index, want := range tc.want {
				if got := resolved.RssSources[index].Locator; got != want {
					t.Fatalf("source %d locator = %q, want %q", index, got, want)
				}
			}
			if tc.sources[0].Locator != incoming.RssSources[0].Locator {
				t.Fatal("the input draft must not be modified")
			}
		})
	}

	single := AnimeDraft{RssSources: []RSSSourceDraft{secretSource(SourceKindCustomRss, first, nil)}}
	resolved := ResolveSecretLocators(AnimeDraft{RssSources: []RSSSourceDraft{secretSource(SourceKindCustomRss, placeholder, nil)}}, single)
	if resolved.RssSources[0].Locator != first {
		t.Fatalf("one stored source of the same kind is the same source: %q", resolved.RssSources[0].Locator)
	}
	if DraftHasSecretPlaceholder(resolved) {
		t.Fatal("a resolved draft has no placeholder left")
	}
}
