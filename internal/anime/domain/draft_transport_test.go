package domain

import (
	"encoding/json"
	"testing"

	"bansoko/internal/transport/httpapi/generated"
)

func domainValue[T any](value any) T {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var result T
	if err := json.Unmarshal(raw, &result); err != nil {
		panic(err)
	}
	return result
}

func TestValidateDraftRejectsUnknownRawSectionFields(t *testing.T) {
	sections := generated.RulesSections{
		Parser:        &generated.ParserRulesOverride{},
		ReleaseFilter: &generated.ReleaseFilterRulesOverride{},
		FileFilter:    &generated.FileFilterRulesOverride{},
		Naming:        &generated.NamingRulesOverride{},
		FileEffect:    &generated.FileEffectRulesOverride{},
	}
	for name, target := range map[string]func([]byte) error{
		"parser":         sections.Parser.UnmarshalJSON,
		"release_filter": sections.ReleaseFilter.UnmarshalJSON,
		"file_filter":    sections.FileFilter.UnmarshalJSON,
		"naming":         sections.Naming.UnmarshalJSON,
		"file_effect":    sections.FileEffect.UnmarshalJSON,
	} {
		raw := []byte(`{"mode":"replace","unexpected_` + name + `":true}`)
		if err := target(raw); err != nil {
			t.Fatalf("store raw %s section: %v", name, err)
		}
	}
	draft := domainValue[AnimeDraft](generated.AnimeDraft{
		Origin:        generated.SubscriptionOriginCustom,
		Title:         "Unknown field",
		InstanceLabel: stringPtr("unknown-field"),
		Metadata:      generated.AnimeMetadataDraft{},
		Subgroups:     generated.SubgroupsSection{Entries: []generated.SubgroupScoreEntry{}},
		Rules:         generated.RulesDraft{Sections: sections},
		RssSources: []generated.RSSSourceDraft{{
			SourceKind: generated.SourceKindCustomRss, Locator: "https://feeds.example.test/rss",
			GroupName: "Custom", Title: "Unknown field",
		}},
	})
	violations := ValidateDraft(draft)
	for _, violation := range violations {
		if violation.Field == "rules.parser" && violation.Code == "OVERRIDE_MODE_INVALID" {
			return
		}
	}
	t.Fatalf("unknown raw section field was silently discarded: %+v", violations)
}

func TestValidateDraftAllowsCustomRSSTransportReferences(t *testing.T) {
	connection := "feed-1"
	route := "route-1"
	violations := ValidateDraft(domainValue[AnimeDraft](generated.AnimeDraft{
		Origin:        generated.SubscriptionOriginCustom,
		Title:         "Custom Show",
		InstanceLabel: stringPtr("custom-instance"),
		RssSources: []generated.RSSSourceDraft{{
			SourceKind: generated.SourceKindCustomRss, Locator: "https://feeds.example.test/rss",
			GroupName: "Custom", Title: "Custom Show",
			HttpFeedConnectionRef: &connection, NetworkRouteRef: &route,
		}},
	}))
	for _, violation := range violations {
		if violation.Field == "rss_sources[0].http_feed_connection_ref" || violation.Field == "rss_sources[0].network_route_ref" {
			t.Fatalf("valid custom transport was rejected: %+v", violations)
		}
	}
}

func TestValidateDraftRejectsProviderRSSTransportReferences(t *testing.T) {
	connection := "feed-1"
	violations := ValidateDraft(domainValue[AnimeDraft](generated.AnimeDraft{
		Origin: generated.SubscriptionOriginAnibt,
		Title:  "Provider Show",
		RssSources: []generated.RSSSourceDraft{{
			SourceKind: generated.SourceKindProviderGroup, Locator: "https://feeds.example.test/rss",
			GroupName: "Group", Title: "Provider Show",
			HttpFeedConnectionRef: &connection,
		}},
	}))
	for _, violation := range violations {
		if violation.Field == "rss_sources[0].http_feed_connection_ref" {
			return
		}
	}
	t.Fatalf("provider source transport reference was accepted: %+v", violations)
}

// A provider-origin draft must not carry RSS sources at all: provider_group
// sources are created by the subgroups selection after discovery, so feeding a
// provider RSS URL directly into a provider draft would bypass discovery (D4).
func TestValidateDraftRejectsProviderOriginRSSSources(t *testing.T) {
	tests := []struct {
		name   string
		origin generated.SubscriptionOrigin
	}{
		{name: "anibt", origin: generated.SubscriptionOriginAnibt},
		{name: "mikan", origin: generated.SubscriptionOriginMikan},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			violations := ValidateDraft(domainValue[AnimeDraft](generated.AnimeDraft{
				Origin: tc.origin,
				Title:  "Provider Show",
				RssSources: []generated.RSSSourceDraft{{
					SourceKind: generated.SourceKindProviderGroup, Locator: "https://feeds.example.test/rss",
					GroupName: "Group", Title: "Provider Show",
				}},
			}))
			found := false
			for _, violation := range violations {
				if violation.Field == "rss_sources" && violation.Code == "DRAFT_INVALID" {
					found = true
				}
			}
			if !found {
				t.Fatalf("provider draft with RSS sources was accepted: %+v", violations)
			}
		})
	}
}

func stringPtr(value string) *string { return &value }

func TestValidateDraftAniBTModeRules(t *testing.T) {
	mode := generated.RSSSourceDraftAnibtAuxModeOverride
	anibt := generated.ProviderKindAnibt
	provenance := &generated.ProviderProvenance{
		Provider: anibt, ProviderEntryRef: "entry-1", ProviderGroupRef: "group-1", DiscoverySnapshotVersion: "snap-1",
	}
	base := generated.AnimeDraft{
		Origin: generated.SubscriptionOriginAnibt,
		Title:  "AniBT Show",
		RssSources: []generated.RSSSourceDraft{{
			SourceKind: generated.SourceKindProviderGroup, Locator: "https://feeds.example.test/rss",
			GroupName: "Group", Title: "AniBT Show",
			ProviderProvenance: provenance,
		}},
	}
	// A valid AniBT provider source accepts an aux mode.
	valid := base
	valid.RssSources[0].AnibtAuxMode = &mode
	for _, violation := range ValidateDraft(domainValue[AnimeDraft](valid)) {
		if violation.Field == "rss_sources[0].anibt_aux_mode" {
			t.Fatalf("valid anibt aux mode was rejected: %+v", ValidateDraft(domainValue[AnimeDraft](valid)))
		}
	}
	// A non-AniBT provider source rejects the mode.
	nonAnibt := base
	nonAnibt.RssSources[0].ProviderProvenance = &generated.ProviderProvenance{
		Provider: generated.ProviderKindMikan, ProviderEntryRef: "e", ProviderGroupRef: "g", DiscoverySnapshotVersion: "s",
	}
	nonAnibt.RssSources[0].AnibtAuxMode = &mode
	if !hasDraftViolation(nonAnibt, "rss_sources[0].anibt_aux_mode") {
		t.Fatalf("anibt aux mode on a non-AniBT source was accepted")
	}
	// An invalid enum value is rejected even for AniBT sources.
	invalidMode := generated.RSSSourceDraftAnibtAuxMode("banana")
	invalid := base
	invalid.RssSources[0].AnibtAuxMode = &invalidMode
	if !hasDraftViolation(invalid, "rss_sources[0].anibt_aux_mode") {
		t.Fatalf("invalid anibt aux mode enum was accepted")
	}
}

func hasDraftViolation(draft generated.AnimeDraft, field string) bool {
	for _, violation := range ValidateDraft(domainValue[AnimeDraft](draft)) {
		if violation.Field == field {
			return true
		}
	}
	return false
}
