package application

import (
	"testing"

	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/selection/domain"
)

func rankedKeys(t *testing.T, kind providerdomain.Kind, raw string, ids ...string) map[string]domain.GroupSelectionOutcome {
	t.Helper()
	policy, err := DiscoveryPolicyFromSubgroupsSection([]byte(raw), 3)
	if err != nil {
		t.Fatalf("DiscoveryPolicyFromSubgroupsSection: %v", err)
	}
	groups := make([]domain.ObservedGroup, 0, len(ids))
	for _, id := range ids {
		groups = append(groups, domain.ObservedGroup{ExternalID: id, Name: "Group " + id, Available: true})
	}
	decisions, err := domain.RankObservedGroups(kind, groups, policy)
	if err != nil {
		t.Fatalf("RankObservedGroups: %v", err)
	}
	outcomes := map[string]domain.GroupSelectionOutcome{}
	for _, decision := range decisions {
		outcomes[decision.Group.Ref.ExternalID] = decision.Outcome
	}
	return outcomes
}

// A section with no entries is the common not-yet-configured draft: every
// observed group is ranked by the product default and capped by max_rss.
func TestDiscoveryPolicyFromSubgroupsSectionEmptyEntriesUsesProductDefault(t *testing.T) {
	got := rankedKeys(t, providerdomain.ProviderMikan, `{"entries":[],"max_rss":2}`, "1", "2", "3")
	selected := 0
	for _, outcome := range got {
		if outcome == domain.GroupSelected {
			selected++
		}
	}
	if selected != 2 {
		t.Fatalf("max_rss=2 must select exactly two of three observed groups, got %+v", got)
	}
}

// An explicit manual entry overrides the default, exactly as in the stored form.
func TestDiscoveryPolicyFromSubgroupsSectionManualEntryOverridesDefault(t *testing.T) {
	raw := `{"entries":[{"provider":"mikan","group_ref":"1","display_name":"G1","selection_mode":"manual","manual_selected":false,"score":0,"count_in_limit":false,"effective_selected":false}],"max_rss":5}`
	got := rankedKeys(t, providerdomain.ProviderMikan, raw, "1", "2")
	if got["1"] != domain.GroupNotSelected {
		t.Fatalf("manual-off group must not be selected: %+v", got)
	}
	if got["2"] != domain.GroupSelected {
		t.Fatalf("the unconfigured group still takes the default: %+v", got)
	}
}

// A whitelist default flips the baseline: only the listed groups are ranked.
func TestDiscoveryPolicyFromSubgroupsSectionWhitelistDefaultSelectsOnlyListedGroups(t *testing.T) {
	raw := `{"entries":[],"max_rss":5,"provider_defaults":{"mikan":{"groups":["7"],"mode":"whitelist","score":70}}}`
	got := rankedKeys(t, providerdomain.ProviderMikan, raw, "7", "8")
	if got["7"] != domain.GroupSelected || got["8"] != domain.GroupNotSelected {
		t.Fatalf("whitelist must select only group 7: %+v", got)
	}
}

func TestDiscoveryPolicyFromSubgroupsSectionRejectsMalformedJSON(t *testing.T) {
	if _, err := DiscoveryPolicyFromSubgroupsSection([]byte(`{"entries":`), 1); err == nil {
		t.Fatal("malformed section JSON must be an error, not a silent default policy")
	}
}
