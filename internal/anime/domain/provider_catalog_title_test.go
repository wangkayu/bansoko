package domain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The provider catalog title is what the subscription freezes as
// managed_anime.provider_title. The rule accepts exactly what the column can
// hold: the trimmed title, non-empty, valid UTF-8 and at most 512 characters.
// Every other observed title is nil (no provider title), never a repaired value.
func TestProviderCatalogTitle(t *testing.T) {
	long512 := strings.Repeat("猫", 512)
	long513 := strings.Repeat("猫", 513)
	tests := []struct {
		name  string
		title string
		want  *string
	}{
		{name: "plain title is trimmed", title: "  转生史莱姆  ", want: ptr("转生史莱姆")},
		{name: "inner whitespace is kept", title: " 转生 史莱姆 ", want: ptr("转生 史莱姆")},
		{name: "tab and newline are trimmed", title: "\t\n[示例字幕组] 转生史莱姆\r\n", want: ptr("[示例字幕组] 转生史莱姆")},
		{name: "empty is nil", title: "", want: nil},
		{name: "blank is nil", title: " \t\r\n ", want: nil},
		{name: "unicode blank is nil", title: "\u00a0\u2003", want: nil},
		{name: "512 characters is the bound", title: long512, want: ptr(long512)},
		{name: "513 characters is nil", title: long513, want: nil},
		{name: "pages of whitespace around a long title still count", title: " " + long513 + " ", want: nil},
		{name: "invalid utf8 is nil", title: string([]byte{0xff}), want: nil},
		{name: "invalid utf8 inside a title is nil", title: "转生\xff史莱姆", want: nil},
		{name: "invalid utf8 only whitespace is nil", title: " \xff ", want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ProviderCatalogTitle(tc.title)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("ProviderCatalogTitle(%q) = %q, want nil", tc.title, *got)
			case tc.want != nil && got == nil:
				t.Fatalf("ProviderCatalogTitle(%q) = nil, want %q", tc.title, *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("ProviderCatalogTitle(%q) = %q, want %q", tc.title, *got, *tc.want)
			}
		})
	}
}

// The bound is characters, not bytes: 512 three-byte characters are accepted,
// so a byte-length check would wrongly reject ordinary CJK titles.
func TestProviderCatalogTitleBoundCountsCharacters(t *testing.T) {
	title := strings.Repeat("猫", 512)
	if len(title) <= 512 {
		t.Fatalf("test title is not multi-byte: %d bytes", len(title))
	}
	got := ProviderCatalogTitle(title)
	if got == nil || utf8.RuneCountInString(*got) != 512 {
		t.Fatalf("ProviderCatalogTitle(512 runes) = %v, want 512 runes", got)
	}
}

func ptr(value string) *string { return &value }
