package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"bansoko/internal/persistence/txscope"
	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/selection/domain"
)

type Service struct{ db *sql.DB }

func NewService(db *sql.DB) *Service { return &Service{db: db} }

// ReadDiscoveryPolicy composes one subscription's live discovery policy: its
// own stored subgroups section (max_rss and per-anime manual entries; a
// subscription without one fails closed) and the live Global site policy
// (provider_defaults and name_keywords; an absent Global row is the code
// default). See LiveDiscoveryPolicy.
func (s *Service) ReadDiscoveryPolicy(ctx context.Context, managedAnimeID string) (domain.DiscoveryPolicy, error) {
	if s == nil || s.db == nil {
		return domain.DiscoveryPolicy{}, errors.New("selection database is nil")
	}
	return readDiscoveryPolicy(ctx, s.db, managedAnimeID)
}

// ReadDiscoveryPolicyInTx reads the same policy through the caller's transaction.
// Global section writes use this method so a policy recomputation observes the
// newly written section before the transaction commits.
func (s *Service) ReadDiscoveryPolicyInTx(ctx context.Context, scope txscope.Tx, managedAnimeID string) (domain.DiscoveryPolicy, error) {
	if s == nil || scope.SQLTx() == nil {
		return domain.DiscoveryPolicy{}, errors.New("selection transaction is nil")
	}
	return readDiscoveryPolicy(ctx, scope.SQLTx(), managedAnimeID)
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readDiscoveryPolicy(ctx context.Context, q queryRower, managedAnimeID string) (domain.DiscoveryPolicy, error) {
	if strings.TrimSpace(managedAnimeID) == "" {
		return domain.DiscoveryPolicy{}, errors.New("discovery policy requires a subscription")
	}
	var raw []byte
	var version int64
	err := q.QueryRowContext(ctx, `
SELECT o.value, owner.subgroups_version
FROM anime_config_overrides AS o
JOIN anime_business_configs AS owner
  ON owner.managed_anime_id = o.managed_anime_id
WHERE o.managed_anime_id = $1
  AND o.owner_type = 'subgroups'
  AND o.section_name = 'subgroups'`, managedAnimeID).Scan(&raw, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DiscoveryPolicy{}, fmt.Errorf("subscription %s has no stored subgroups section", managedAnimeID)
	}
	if err != nil {
		return domain.DiscoveryPolicy{}, fmt.Errorf("read anime subgroups section: %w", err)
	}
	if len(raw) == 0 {
		return domain.DiscoveryPolicy{}, fmt.Errorf("subscription %s has no stored subgroups section", managedAnimeID)
	}
	site, err := ReadSitePolicy(ctx, q)
	if err != nil {
		return domain.DiscoveryPolicy{}, err
	}
	policy, err := LiveDiscoveryPolicy(raw, version, site)
	if err != nil {
		return domain.DiscoveryPolicy{}, fmt.Errorf("decode anime subgroups section: %w", err)
	}
	return policy, nil
}

func defaultDiscoveryPolicy() domain.DiscoveryPolicy {
	return domain.DiscoveryPolicy{
		MaxRSS: 3,
		ProviderDefaults: map[providerdomain.Kind]domain.ProviderDefaultPolicy{
			providerdomain.ProviderAniBT: {Mode: domain.Ranked, Score: 50, Version: 1},
			providerdomain.ProviderMikan: {Mode: domain.Ranked, Score: 50, Version: 1},
		},
		NameKeywords: map[providerdomain.Kind][]domain.NameKeywordRule{
			providerdomain.ProviderAniBT: nil,
			providerdomain.ProviderMikan: nil,
		},
	}
}

func providerPoliciesFromSection(section persistedSubgroupsSection, version int64) ([]domain.ProviderGroupPolicy, error) {
	policies := make([]domain.ProviderGroupPolicy, 0, len(section.Entries))
	for _, entry := range section.Entries {
		ref, err := providerdomain.NewGroupRef(providerdomain.Kind(entry.Provider), entry.GroupRef)
		if err != nil {
			return nil, err
		}
		policy := domain.ProviderGroupPolicy{
			Target:  domain.PolicyTarget{Kind: domain.TargetProviderGroup, ProviderRef: &ref},
			Version: version,
		}
		if entry.SelectionMode == persistedSubgroupSelectionAuto {
			policy.Mode = domain.Ranked
			score := entry.Score
			policy.Score = &score
		} else if entry.ManualSelected != nil && *entry.ManualSelected {
			policy.Mode = domain.Always
			policy.Score = nil
		} else {
			policy.Mode = domain.Never
			policy.Score = nil
		}
		if err := domain.ValidateProviderGroupPolicy(policy); err != nil {
			return nil, err
		}
		policies = append(policies, policy)
	}
	return policies, nil
}

func providerDefaultRules(section persistedSubgroupsSection, version int64) ([]domain.ProviderGroupPolicy, error) {
	if section.ProviderDefaults == nil {
		return nil, nil
	}
	rules := make([]domain.ProviderGroupPolicy, 0)
	for provider, configured := range map[providerdomain.Kind]*persistedProviderGroupDefault{
		providerdomain.ProviderAniBT: section.ProviderDefaults.Anibt,
		providerdomain.ProviderMikan: section.ProviderDefaults.Mikan,
	} {
		if configured == nil {
			continue
		}
		for _, group := range configured.Groups {
			ref, err := providerdomain.NewGroupRef(provider, strings.TrimSpace(group))
			if err != nil {
				return nil, err
			}
			rule := domain.ProviderGroupPolicy{Target: domain.PolicyTarget{Kind: domain.TargetProviderGroup, ProviderRef: &ref}, Version: version}
			if configured.Mode == persistedProviderDefaultWhitelist {
				rule.Mode = domain.Ranked
				score := configured.Score
				rule.Score = &score
			} else {
				rule.Mode = domain.Never
			}
			if err := domain.ValidateProviderGroupPolicy(rule); err != nil {
				return nil, err
			}
			rules = append(rules, rule)
		}
	}
	return rules, nil
}

func providerDefaultsFromSection(section persistedSubgroupsSection, version int64) map[providerdomain.Kind]domain.ProviderDefaultPolicy {
	defaults := defaultDiscoveryPolicy().ProviderDefaults
	nameKeywords := nameKeywordsFromSection(section)
	for provider, rules := range nameKeywords {
		defaultPolicy := defaults[provider]
		defaultPolicy.NameKeywords = rules
		defaults[provider] = defaultPolicy
	}
	if section.ProviderDefaults == nil {
		return defaults
	}
	for provider, configured := range map[providerdomain.Kind]*persistedProviderGroupDefault{
		providerdomain.ProviderAniBT: section.ProviderDefaults.Anibt,
		providerdomain.ProviderMikan: section.ProviderDefaults.Mikan,
	} {
		if configured == nil {
			continue
		}
		mode := domain.Ranked
		if configured.Mode == persistedProviderDefaultWhitelist {
			mode = domain.Never
		}
		defaults[provider] = domain.ProviderDefaultPolicy{
			Mode: mode, Score: configured.Score, Version: version,
			NameKeywords: nameKeywords[provider],
		}
	}
	return defaults
}

func nameKeywordsFromSection(section persistedSubgroupsSection) map[providerdomain.Kind][]domain.NameKeywordRule {
	result := map[providerdomain.Kind][]domain.NameKeywordRule{
		providerdomain.ProviderAniBT: nil,
		providerdomain.ProviderMikan: nil,
	}
	if section.NameKeywords == nil {
		return result
	}
	result[providerdomain.ProviderAniBT] = nameKeywordRulesFromPersisted(section.NameKeywords.Anibt)
	result[providerdomain.ProviderMikan] = nameKeywordRulesFromPersisted(section.NameKeywords.Mikan)
	return result
}

func nameKeywordRulesFromPersisted(rules *[]persistedNameKeywordRule) []domain.NameKeywordRule {
	if rules == nil || len(*rules) == 0 {
		return nil
	}
	result := make([]domain.NameKeywordRule, 0, len(*rules))
	for _, rule := range *rules {
		result = append(result, domain.NameKeywordRule{Keyword: rule.Keyword, Score: rule.Score})
	}
	return result
}

func mergeProviderPolicies(base, overrides []domain.ProviderGroupPolicy) []domain.ProviderGroupPolicy {
	result := append([]domain.ProviderGroupPolicy(nil), base...)
	index := make(map[string]int, len(result))
	for i, rule := range result {
		index[providerPolicyKey(rule)] = i
	}
	for _, rule := range overrides {
		key := providerPolicyKey(rule)
		if i, ok := index[key]; ok {
			result[i] = rule
		} else {
			index[key] = len(result)
			result = append(result, rule)
		}
	}
	return result
}

func providerPolicyKey(rule domain.ProviderGroupPolicy) string {
	if rule.Target.ProviderRef != nil {
		return string(rule.Target.Kind) + ":" + rule.Target.ProviderRef.Key()
	}
	return string(rule.Target.Kind) + ":" + rule.Target.CanonicalKey
}
