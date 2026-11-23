package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateDraftEpisodeSpan(t *testing.T) {
	entryRef := "entry-1"
	for _, span := range []int{0, 1, 12} {
		draft := AnimeDraft{
			Title: "Show", Origin: SubscriptionOriginMikan, OriginEntryRef: &entryRef,
			ProviderGroupOffsets: []ProviderGroupOffsetDraft{{ProviderGroupRef: "group-1", EpisodeSpan: span}},
		}
		for _, violation := range ValidateDraft(draft) {
			if strings.HasSuffix(violation.Field, ".episode_span") {
				t.Fatalf("provider span %d rejected: %+v", span, violation)
			}
		}
	}
	for _, span := range []int{-1, 13} {
		draft := AnimeDraft{
			Title: "Show", Origin: SubscriptionOriginMikan, OriginEntryRef: &entryRef,
			ProviderGroupOffsets: []ProviderGroupOffsetDraft{{ProviderGroupRef: "group-1", EpisodeSpan: span}},
		}
		found := false
		for _, violation := range ValidateDraft(draft) {
			if violation.Field == "provider_group_offsets[0].episode_span" && violation.Code == "DRAFT_INVALID" {
				found = true
			}
		}
		if !found {
			t.Fatalf("provider span %d was not rejected: %+v", span, ValidateDraft(draft))
		}
	}

	label := "Custom"
	for _, span := range []int{-1, 13} {
		draft := AnimeDraft{
			Title: "Show", Origin: SubscriptionOriginCustom, InstanceLabel: &label,
			RssSources: []RSSSourceDraft{{
				Locator: "custom://feed", SourceKind: SourceKindCustomRss, EpisodeSpan: span,
			}},
		}
		found := false
		for _, violation := range ValidateDraft(draft) {
			if violation.Field == "rss_sources[0].episode_span" && violation.Code == "DRAFT_INVALID" {
				found = true
			}
		}
		if !found {
			t.Fatalf("RSS source span %d was not rejected: %+v", span, ValidateDraft(draft))
		}
	}
}

func TestDefaultEpisodeSpanStaysAbsentFromDraftJSON(t *testing.T) {
	for _, value := range []any{
		ProviderGroupOffsetDraft{ProviderGroupRef: "group-1"},
		RSSSourceDraft{Locator: "custom://feed", SourceKind: SourceKindCustomRss},
	} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "episode_span") {
			t.Fatalf("default span changed legacy draft JSON: %s", raw)
		}
	}
}
