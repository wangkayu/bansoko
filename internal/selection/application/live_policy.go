package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/selection/domain"
)

// SitePolicy is the live site-level subgroup policy: each provider's
// provider_defaults (list mode, default score, group list, pinned scores) and
// name_keywords, read from the Global subgroups section. It is the one
// explicit exception to one-time input: it applies to every existing
// instance immediately, while the instance keeps its own entries and max_rss.
// The Global section's own entries and max_rss are only the defaults a new
// draft copies and never take part here.
type SitePolicy struct {
	ProviderDefaults *persistedProviderGroupDefaults
	NameKeywords     *persistedProviderNameKeywordRules
	Version          int64
}

// DefaultSitePolicy is the site policy before the Global subgroups section is
// first saved: every provider ranks every group at the code default score
// (defaultDiscoveryPolicy), with no list and no keyword.
func DefaultSitePolicy() SitePolicy { return SitePolicy{} }

// ReadSitePolicy reads the live site policy. An absent Global row
// deliberately yields DefaultSitePolicy; no other Global section is read.
func ReadSitePolicy(ctx context.Context, q queryRower) (SitePolicy, error) {
	var raw []byte
	var version int64
	err := q.QueryRowContext(ctx, `
SELECT value, version
FROM global_config_sections
WHERE section_name = 'subgroups'`).Scan(&raw, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultSitePolicy(), nil
	}
	if err != nil {
		return SitePolicy{}, fmt.Errorf("read global subgroups site policy: %w", err)
	}
	policy, err := SitePolicyFromSubgroupsSection(raw)
	if err != nil {
		return SitePolicy{}, err
	}
	policy.Version = version
	return policy, nil
}

// SitePolicyFromSubgroupsSection takes the site parts of one subgroups
// section value (provider_defaults and name_keywords).
func SitePolicyFromSubgroupsSection(raw []byte) (SitePolicy, error) {
	var section persistedSubgroupsSection
	if err := json.Unmarshal(raw, &section); err != nil {
		return SitePolicy{}, fmt.Errorf("decode global subgroups site policy: %w", err)
	}
	return SitePolicy{ProviderDefaults: section.ProviderDefaults, NameKeywords: section.NameKeywords}, nil
}

// SitePolicyChanged reports whether two Global subgroups section values differ
// in their site parts. A change to entries or max_rss alone (the new-draft
// defaults) is not a site policy change.
func SitePolicyChanged(previous, next []byte) (bool, error) {
	if len(previous) == 0 {
		previous = []byte(`{}`)
	}
	before, err := SitePolicyFromSubgroupsSection(previous)
	if err != nil {
		return false, err
	}
	after, err := SitePolicyFromSubgroupsSection(next)
	if err != nil {
		return false, err
	}
	normalize := func(policy SitePolicy) (string, error) {
		raw, err := json.Marshal(struct {
			Defaults map[providerdomain.Kind]domain.ProviderDefaultPolicy
		}{policy.providerDefaults()})
		return string(raw), err
	}
	left, err := normalize(before)
	if err != nil {
		return false, err
	}
	right, err := normalize(after)
	if err != nil {
		return false, err
	}
	return left != right, nil
}

// providerDefaults is the typed per-provider site policy, with the code
// default for a provider the section does not configure.
func (p SitePolicy) providerDefaults() map[providerdomain.Kind]domain.ProviderDefaultPolicy {
	defaults := defaultDiscoveryPolicy().ProviderDefaults
	keywords := nameKeywordsFromSection(persistedSubgroupsSection{NameKeywords: p.NameKeywords})
	version := p.Version
	if version < 1 {
		version = 1
	}
	for provider, policy := range defaults {
		policy.ListMode = domain.SiteBlacklist
		policy.Version = version
		policy.NameKeywords = keywords[provider]
		defaults[provider] = policy
	}
	if p.ProviderDefaults == nil {
		return defaults
	}
	for provider, configured := range map[providerdomain.Kind]*persistedProviderGroupDefault{
		providerdomain.ProviderAniBT: p.ProviderDefaults.Anibt,
		providerdomain.ProviderMikan: p.ProviderDefaults.Mikan,
	} {
		if configured == nil {
			continue
		}
		policy := defaults[provider]
		policy.Score = configured.Score
		policy.ListMode = domain.SiteBlacklist
		policy.Mode = domain.Ranked
		if configured.Mode == persistedProviderDefaultWhitelist {
			policy.ListMode = domain.SiteWhitelist
			policy.Mode = domain.Never
		}
		policy.Listed = make([]string, 0, len(configured.Groups))
		for _, group := range configured.Groups {
			if trimmed := strings.TrimSpace(group); trimmed != "" {
				policy.Listed = append(policy.Listed, trimmed)
			}
		}
		policy.Pinned = nil
		if len(configured.PinnedScores) > 0 {
			policy.Pinned = make(map[string]int, len(configured.PinnedScores))
			for _, pin := range configured.PinnedScores {
				if key := strings.TrimSpace(pin.GroupRef); key != "" {
					policy.Pinned[key] = pin.Score
				}
			}
		}
		defaults[provider] = policy
	}
	return defaults
}

// LiveDiscoveryPolicy composes the runtime discovery policy from its two
// owners, explicitly: the instance's (or draft's) own subgroups section gives
// max_rss and the per-anime manual entries, and site gives every provider's
// list, default score and name keywords. Nothing else is read from either
// side: an instance-stored provider_defaults/name_keywords copy is ignored,
// and an automatic entry contributes no rule, because its stored score is the
// projection of the last decision, not an input (the product has no per-group
// score override). raw is the canonical section JSON; version is its owner
// version.
func LiveDiscoveryPolicy(raw []byte, version int64, site SitePolicy) (domain.DiscoveryPolicy, error) {
	var section persistedSubgroupsSection
	if err := json.Unmarshal(raw, &section); err != nil {
		return domain.DiscoveryPolicy{}, fmt.Errorf("decode subgroups section: %w", err)
	}
	policy := defaultDiscoveryPolicy()
	policy.MaxRSS = section.MaxRss
	rules, err := manualPoliciesFromSection(section, version)
	if err != nil {
		return domain.DiscoveryPolicy{}, fmt.Errorf("decode subgroup entries: %w", err)
	}
	policy.AnimeRules = rules
	policy.ProviderDefaults = site.providerDefaults()
	policy.NameKeywords = nameKeywordsFromSection(persistedSubgroupsSection{NameKeywords: site.NameKeywords})
	return policy, nil
}

// ReadLiveDiscoveryPolicy reads the site policy through q and composes it with
// one already-resolved subgroups section (a draft's value, or an owner's
// unsaved one).
func ReadLiveDiscoveryPolicy(ctx context.Context, q queryRower, raw []byte, version int64) (domain.DiscoveryPolicy, error) {
	site, err := ReadSitePolicy(ctx, q)
	if err != nil {
		return domain.DiscoveryPolicy{}, err
	}
	return LiveDiscoveryPolicy(raw, version, site)
}

// manualPoliciesFromSection maps only the per-anime manual intents: a manual
// include is always, a manual exclude is never.
func manualPoliciesFromSection(section persistedSubgroupsSection, version int64) ([]domain.ProviderGroupPolicy, error) {
	policies := make([]domain.ProviderGroupPolicy, 0, len(section.Entries))
	for _, entry := range section.Entries {
		if entry.SelectionMode != persistedSubgroupSelectionManual {
			continue
		}
		ref, err := providerdomain.NewGroupRef(providerdomain.Kind(entry.Provider), entry.GroupRef)
		if err != nil {
			return nil, err
		}
		policy := domain.ProviderGroupPolicy{
			Target:  domain.PolicyTarget{Kind: domain.TargetProviderGroup, ProviderRef: &ref},
			Version: version,
		}
		if entry.ManualSelected != nil && *entry.ManualSelected {
			policy.Mode = domain.Always
			policy.CountInLimit = entry.CountInLimit
		} else {
			policy.Mode = domain.Never
		}
		if err := domain.ValidateProviderGroupPolicy(policy); err != nil {
			return nil, err
		}
		policies = append(policies, policy)
	}
	return policies, nil
}
