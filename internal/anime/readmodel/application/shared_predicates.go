package application

import (
	"strings"

	animedomain "bansoko/internal/anime/domain"
)

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
