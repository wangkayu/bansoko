package domain

import (
	"fmt"
	"strings"

	selectiondomain "bansoko/internal/selection/domain"
)

// SubgroupsSectionWithEffectiveSelections returns the section with every
// entry's effective_selected recomputed by the single group-selection
// authority (selectiondomain.SelectProviderGroupsByKey).
//
// effective_selected is a projection, not an input: the client renders a
// predicted selection, while the runtime derives the real selection from
// selection_mode/manual_selected/score and max_rss. Persisting
// the projection through the same decision core is what keeps a saved revision
// and the owner provider-group rows from disagreeing with what the runtime
// selects (and therefore with what a saved-revision preview requires a feed
// snapshot identity for).
//
// Every entry of a persisted section is an observed group, so observation is
// not re-derived here. The group key is the provider-namespaced
// "<provider>:<group_ref>" form. The core is intentionally ref-shape agnostic:
// it decides selection, not identity syntax, so it can never reject a section
// the surrounding validation accepted.
func SubgroupsSectionWithEffectiveSelections(section SubgroupsSection) (SubgroupsSection, error) {
	result := section
	if section.Entries != nil {
		// Preserve a present-but-empty entry array: the section's JSON shape
		// distinguishes "no groups yet" ([]) from an absent list, and the
		// section validator rejects a null entries value.
		result.Entries = append([]SubgroupScoreEntry{}, section.Entries...)
	}
	decisions, err := subgroupsSectionDecisions(result, nil)
	if err != nil {
		return SubgroupsSection{}, err
	}
	for _, decision := range decisions {
		result.Entries[decision.Index].EffectiveSelected = decision.Outcome == selectiondomain.GroupSelected
	}
	return result, nil
}

// SubgroupSelectionDecision is one entry's selection outcome together with the
// reason the decision core gave for it.
type SubgroupSelectionDecision struct {
	Selected bool
	Reason   string
}

// SubgroupsSectionSelectionReasons returns the decision behind every entry's
// selection state, keyed by subgroupSelectionKey. The reason is the same one the
// decision core produced for effective_selected: it explains an unselected group
// (excluded by policy, outscored for the last slot, negative score) and names an
// unavailable one, so no caller has to re-derive the rules from the booleans it
// already has and no second implementation can disagree.
//
// unavailable names the groups the provider no longer lists. A completed
// section is observed by definition, but an owner read overlays persisted
// availability on it, and an unavailable group is never selected however high
// it scores — the reason must say so instead of claiming it won a slot.
//
// A group that is not in the section has no reason: the answer is about this
// section's groups, not about the groups it never listed.
func SubgroupsSectionSelectionReasons(section SubgroupsSection, unavailable map[string]struct{}) (map[string]SubgroupSelectionDecision, error) {
	decisions, err := subgroupsSectionDecisions(section, unavailable)
	if err != nil {
		return nil, err
	}
	result := make(map[string]SubgroupSelectionDecision, len(decisions))
	for _, decision := range decisions {
		result[decision.Key] = SubgroupSelectionDecision{
			Selected: decision.Outcome == selectiondomain.GroupSelected,
			Reason:   decision.Reason,
		}
	}
	return result, nil
}

// SubgroupsSectionForProvider returns the section restricted to one provider's
// entries. Selection is decided per provider — another provider's groups never
// compete for this one's max_rss — so a caller that explains one provider's
// decisions must ask about that provider's entries alone. A section that lists
// only the provider (the usual case) is returned as is.
func SubgroupsSectionForProvider(section SubgroupsSection, provider ProviderKind) SubgroupsSection {
	result := section
	entries := make([]SubgroupScoreEntry, 0, len(section.Entries))
	for _, entry := range section.Entries {
		if entry.Provider == provider {
			entries = append(entries, entry)
		}
	}
	result.Entries = entries
	return result
}

// SubgroupsSectionSelectionReasonsForKeys answers, for a set of already-known
// provider-scoped keys, what the decision core says about each. Each provider's
// entries are replayed alone, because selection is decided per provider: a
// section that carries both providers' groups would otherwise let one
// provider's entries consume the other's max_rss slots and manufacture
// max_rss_exhausted reasons the runtime never produced. A key the section does
// not list gets no decision, so a caller keeps no reason rather than a guessed
// one.
func SubgroupsSectionSelectionReasonsForKeys(section SubgroupsSection, keys []string, unavailable map[string]struct{}) (map[string]SubgroupSelectionDecision, error) {
	result := make(map[string]SubgroupSelectionDecision, len(keys))
	byProvider := make(map[string][]string, 2)
	for _, key := range keys {
		provider, _, _ := strings.Cut(key, ":")
		byProvider[provider] = append(byProvider[provider], key)
	}
	for provider, providerKeys := range byProvider {
		scoped, err := SubgroupsSectionSelectionReasons(SubgroupsSectionForProvider(section, ProviderKind(provider)), unavailable)
		if err != nil {
			return nil, err
		}
		for _, key := range providerKeys {
			if decision, ok := scoped[key]; ok {
				result[key] = decision
			}
		}
	}
	return result, nil
}

// subgroupsSectionDecisions runs the section through the single group-selection
// authority. An entry is an observed group unless the caller marks it
// unavailable. The group key is the provider-namespaced "<provider>:<group_ref>"
// form. The core is intentionally ref-shape agnostic: it decides selection, not
// identity syntax, so it can never reject a section the surrounding validation
// accepted.
func subgroupsSectionDecisions(section SubgroupsSection, unavailable map[string]struct{}) ([]selectiondomain.GroupSelectionResult, error) {
	if len(section.Entries) == 0 {
		return nil, nil
	}
	inputs := make([]selectiondomain.ProviderGroupSelectionInput, len(section.Entries))
	for i, entry := range section.Entries {
		key := subgroupSelectionKey(entry.Provider, entry.GroupRef)
		input := selectiondomain.ProviderGroupSelectionInput{Key: key, Observed: true}
		if _, gone := unavailable[key]; gone {
			input.Observed = false
		}
		switch entry.SelectionMode {
		case SelectionModeAuto:
			// validateSectionValue already bounds an auto score to 1..100.
			input.Mode = selectiondomain.Ranked
			input.Score = entry.Score
		case SelectionModeManual:
			if entry.ManualSelected != nil && *entry.ManualSelected {
				input.Mode = selectiondomain.Always
			} else {
				input.Mode = selectiondomain.Never
			}
		default:
			return nil, fmt.Errorf("unsupported subgroup selection mode %q", entry.SelectionMode)
		}
		inputs[i] = input
	}
	return selectiondomain.SelectProviderGroupsByKey(inputs, section.MaxRss)
}

func subgroupSelectionKey(provider ProviderKind, groupRef string) string {
	return string(provider) + ":" + strings.TrimSpace(groupRef)
}
