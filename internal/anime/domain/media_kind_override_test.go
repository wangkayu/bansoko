package domain

import (
	"testing"

	"bansoko/internal/transport/httpapi/generated"
)

// The owner media-kind pin is validated with the same complete-owner-value
// contract as title_override, but with the exact persisted vocabulary: the
// column is CHECKed against tv|movie, so validation must accept exactly the
// bytes the column can store and reject everything else (blank, other case,
// surrounding whitespace, an unknown kind).
func TestValidateDraftMediaKindOverride(t *testing.T) {
	tv := generated.AnimeMetadataDraftMediaKindOverrideTv
	movie := generated.AnimeMetadataDraftMediaKindOverrideMovie
	blank := generated.AnimeMetadataDraftMediaKindOverride("")
	upper := generated.AnimeMetadataDraftMediaKindOverride("TV")
	padded := generated.AnimeMetadataDraftMediaKindOverride(" movie")

	cases := []struct {
		name         string
		override     *generated.AnimeMetadataDraftMediaKindOverride
		wantViolated bool
	}{
		{name: "unset", override: nil, wantViolated: false},
		{name: "tv", override: &tv, wantViolated: false},
		{name: "movie", override: &movie, wantViolated: false},
		{name: "blank", override: &blank, wantViolated: true},
		{name: "wrong case", override: &upper, wantViolated: true},
		{name: "padded", override: &padded, wantViolated: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entryRef := "entry-1"
			draft := domainValue[AnimeDraft](generated.AnimeDraft{
				Origin:         generated.SubscriptionOriginAnibt,
				OriginEntryRef: &entryRef,
				Title:          "Show",
				Metadata:       generated.AnimeMetadataDraft{MediaKindOverride: tc.override},
				RssSources:     []generated.RSSSourceDraft{},
			})
			violated := false
			for _, violation := range ValidateDraft(draft) {
				if violation.Field == "metadata.media_kind_override" {
					violated = true
				}
			}
			if violated != tc.wantViolated {
				t.Fatalf("metadata.media_kind_override violation = %v, want %v", violated, tc.wantViolated)
			}
			if tc.override != nil && draft.Metadata.MediaKindOverride != nil && *draft.Metadata.MediaKindOverride != string(*tc.override) {
				t.Fatalf("decoded override = %q, want %q", *draft.Metadata.MediaKindOverride, string(*tc.override))
			}
		})
	}
}

// The owner PATCH carries the same value: nil clears the pin, and only the exact
// persisted vocabulary is accepted.
func TestValidateMetadataPatchMediaKindOverride(t *testing.T) {
	tv := "tv"
	movie := "movie"
	blank := ""
	upper := "TV"
	padded := "movie "

	cases := []struct {
		name         string
		override     *string
		wantViolated bool
	}{
		{name: "null clears", override: nil, wantViolated: false},
		{name: "tv", override: &tv, wantViolated: false},
		{name: "movie", override: &movie, wantViolated: false},
		{name: "blank", override: &blank, wantViolated: true},
		{name: "wrong case", override: &upper, wantViolated: true},
		{name: "padded", override: &padded, wantViolated: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			violated := false
			for _, violation := range ValidateMetadataPatch(nil, nil, nil, tc.override, nil) {
				if violation.Field == "media_kind_override" {
					violated = true
				}
			}
			if violated != tc.wantViolated {
				t.Fatalf("media_kind_override violation = %v, want %v", violated, tc.wantViolated)
			}
		})
	}
}
