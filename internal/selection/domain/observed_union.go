package domain

import (
	"fmt"
	"sort"

	providerdomain "bansoko/internal/provider/domain"
)

// ObservedGroup is one provider group that takes part in selection, in the
// transport-neutral form shared by the discovery dispatcher and the draft
// preview. Display names are scoring evidence, never identity keys.
type ObservedGroup struct {
	ExternalID string
	Name       string
	// Available is whether the provider currently lists the group. An
	// unavailable group is reported as such and never selected.
	Available bool
	// Manual, when set, is an explicit per-group override that wins over every
	// policy layer: true selects the group (always), false blocks it (never).
	Manual       *bool
	CountInLimit bool
}

// RankObservedGroups ranks the whole observed union under one policy and is the
// single place that turns "groups the provider lists" into selection decisions.
// Groups without an explicit policy resolve through the provider default and its
// name-keyword adjustments, so a draft that has not configured any subgroup still
// gets the selection the runtime would make once it is subscribed.
func RankObservedGroups(kind providerdomain.Kind, groups []ObservedGroup, policy DiscoveryPolicy) ([]GroupSelectionDecision, error) {
	ordered := make([]ObservedGroup, len(groups))
	copy(ordered, groups)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ExternalID < ordered[j].ExternalID })
	for i := 1; i < len(ordered); i++ {
		if ordered[i].ExternalID == ordered[i-1].ExternalID {
			return nil, fmt.Errorf("provider group %q is observed more than once", ordered[i].ExternalID)
		}
	}
	defaultPolicy, ok := policy.ProviderDefaults[kind]
	if !ok {
		defaultPolicy = ProviderDefaultPolicy{Mode: Ranked, Score: 50, Version: 1}
	}
	effective := make([]EffectiveProviderGroup, 0, len(ordered))
	for _, group := range ordered {
		ref, err := providerdomain.NewGroupRef(kind, group.ExternalID)
		if err != nil {
			return nil, err
		}
		resolved, err := ResolveProviderGroupPolicyWithNameKeywords(ref, "", group.Name, group.Available, policy.AnimeRules, defaultPolicy, policy.NameKeywords[kind])
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", ref.Key(), err)
		}
		if group.Manual != nil {
			resolved.CountInLimit = group.CountInLimit
			override := EntryOverrideNever
			if *group.Manual {
				resolved.Mode = Always
				override = EntryOverrideAlways
			} else {
				resolved.Mode = Never
				resolved.CountInLimit = false
			}
			if resolved.Breakdown != nil {
				resolved.Breakdown = resolved.Breakdown.withOverride(override)
			}
		}
		effective = append(effective, resolved)
	}
	return SelectProviderGroups(effective, policy.MaxRSS)
}
