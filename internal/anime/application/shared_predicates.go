package application

import (
	"strings"

	animedomain "bansoko/internal/anime/domain"
)

// This file holds pure predicates over shared domain shapes used by more than
// one use-case bucket.

// RevisionSelectsProviderGroup reports whether the revision's subgroup selection covers a provider group.
func RevisionSelectsProviderGroup(section animedomain.SubgroupsSection, origin animedomain.SubscriptionOrigin, groupRef string) bool {
	_, groups := selectedProviderGroupEntries(section, origin)
	_, ok := groups[strings.TrimSpace(groupRef)]
	return ok
}

func selectedProviderGroupEntries(section animedomain.SubgroupsSection, origin animedomain.SubscriptionOrigin) ([]animedomain.SubgroupScoreEntry, map[string]animedomain.SubgroupScoreEntry) {
	ordered := make([]animedomain.SubgroupScoreEntry, 0, len(section.Entries))
	by := make(map[string]animedomain.SubgroupScoreEntry, len(section.Entries))
	for _, entry := range section.Entries {
		if entry.Provider != animedomain.ProviderKind(origin) || !entry.EffectiveSelected {
			continue
		}
		key := strings.TrimSpace(entry.GroupRef)
		if key == "" {
			continue
		}
		if _, ok := by[key]; ok {
			continue
		}
		by[key] = entry
		ordered = append(ordered, entry)
	}
	return ordered, by
}

// WorkspaceCollisionPolicy maps the stored collision policy to the projected file-operation policy.
func WorkspaceCollisionPolicy(value string) string {
	switch value {
	case "version":
		return "versioned"
	case "overwrite":
		return "overwrite"
	default:
		return "fail"
	}
}

func workspaceCollisionPolicy(value string) string { return WorkspaceCollisionPolicy(value) }
