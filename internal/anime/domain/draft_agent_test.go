package domain

import (
	"testing"

	"bansoko/internal/transport/httpapi/generated"
)

// validAgentSections returns rules + subgroups sections that pass validation,
// so agent draft tests can focus on origin-specific rules.
func validAgentSections(t *testing.T) (generated.RulesDraft, generated.SubgroupsSection) {
	t.Helper()
	sections := completeRulesForTest(t)
	rules := generated.RulesDraft{Sections: sections}
	subgroups := generated.SubgroupsSection{MaxRss: 100, Entries: []generated.SubgroupScoreEntry{}}
	return rules, subgroups
}

func TestValidateDraftAgentOrigin(t *testing.T) {
	rules, subgroups := validAgentSections(t)

	tests := []struct {
		name       string
		draft      generated.AnimeDraft
		wantFields []string // empty means expect zero violations
	}{
		{
			name: "agent with label and zero RSS sources is valid",
			draft: generated.AnimeDraft{
				Origin:        generated.SubscriptionOriginOneshot,
				Title:         "Agent Show",
				InstanceLabel: stringPtr("agent-run-1"),
				RssSources:    []generated.RSSSourceDraft{},
				Rules:         rules,
				Subgroups:     subgroups,
			},
			wantFields: nil,
		},
		{
			name: "agent without instance_label is invalid",
			draft: generated.AnimeDraft{
				Origin:     generated.SubscriptionOriginOneshot,
				Title:      "Agent Show",
				RssSources: []generated.RSSSourceDraft{},
				Rules:      rules,
				Subgroups:  subgroups,
			},
			wantFields: []string{"instance_label"},
		},
		{
			name: "agent with one custom_rss source is valid",
			draft: generated.AnimeDraft{
				Origin:        generated.SubscriptionOriginOneshot,
				Title:         "Agent Show",
				InstanceLabel: stringPtr("agent-run-1"),
				RssSources: []generated.RSSSourceDraft{{
					SourceKind: generated.SourceKindCustomRss, Locator: "https://feeds.example.test/rss",
					GroupName: "Group", Title: "Agent Show",
				}},
				Rules:     rules,
				Subgroups: subgroups,
			},
			wantFields: nil,
		},
		{
			name: "agent with provider_group source is invalid",
			draft: generated.AnimeDraft{
				Origin:        generated.SubscriptionOriginOneshot,
				Title:         "Agent Show",
				InstanceLabel: stringPtr("agent-run-1"),
				RssSources: []generated.RSSSourceDraft{{
					SourceKind: generated.SourceKindProviderGroup, Locator: "https://feeds.example.test/rss",
					GroupName: "Group", Title: "Agent Show",
				}},
				Rules:     rules,
				Subgroups: subgroups,
			},
			wantFields: []string{"rss_sources[0].source_kind"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			violations := ValidateDraft(domainValue[AnimeDraft](tc.draft))
			if len(tc.wantFields) == 0 {
				for _, v := range violations {
					t.Errorf("unexpected violation: %+v", v)
				}
				return
			}
			found := make(map[string]bool)
			for _, v := range violations {
				found[v.Field] = true
			}
			for _, want := range tc.wantFields {
				if !found[want] {
					t.Errorf("expected violation on field %q, got: %+v", want, violations)
				}
			}
		})
	}
}
