package application

import (
	"encoding/json"
	"fmt"

	"bansoko/internal/selection/domain"
)

// DiscoveryPolicyFromSubgroupsSection builds the discovery policy that one
// already-resolved subgroups section implies, without touching the database.
// raw is the section's canonical JSON (the value a draft or subscription
// stores) and version identifies its source. ReadDiscoveryPolicy uses it for a
// stored section, so a preview ranks groups by the same rules the runtime will.
func DiscoveryPolicyFromSubgroupsSection(raw []byte, version int64) (domain.DiscoveryPolicy, error) {
	var section persistedSubgroupsSection
	if err := json.Unmarshal(raw, &section); err != nil {
		return domain.DiscoveryPolicy{}, fmt.Errorf("decode subgroups section: %w", err)
	}
	policy := defaultDiscoveryPolicy()
	policy.MaxRSS = section.MaxRss
	rules, err := providerPoliciesFromSection(section, version)
	if err != nil {
		return domain.DiscoveryPolicy{}, fmt.Errorf("decode subgroup entries: %w", err)
	}
	defaultRules, err := providerDefaultRules(section, version)
	if err != nil {
		return domain.DiscoveryPolicy{}, fmt.Errorf("decode provider group defaults: %w", err)
	}
	policy.AnimeRules = mergeProviderPolicies(defaultRules, rules)
	policy.ProviderDefaults = providerDefaultsFromSection(section, version)
	policy.NameKeywords = nameKeywordsFromSection(section)
	return policy, nil
}
