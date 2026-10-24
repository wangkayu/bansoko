package httpapi

import (
	catalogapplication "bansoko/internal/catalog/application"
	catalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/transport/httpapi/generated"
)

// subgroupGroupCandidateList projects subgroup dictionary entries onto the
// contract. The provider key and the display name stay separate fields: the key
// is the identity the subgroup list stores, the name is display data that is
// never persisted. The layer each entry came from travels as source, so a
// client can tell the release's own snapshot from a group the user added.
func subgroupGroupCandidateList(entries []catalogdomain.GroupDictionaryEntry) generated.SubgroupGroupCandidateList {
	candidates := make([]generated.SubgroupGroupCandidate, 0, len(entries))
	for _, entry := range entries {
		candidates = append(candidates, generated.SubgroupGroupCandidate{
			Provider:    generated.ProviderKind(entry.Provider),
			GroupRef:    entry.GroupRef,
			DisplayName: entry.DisplayName,
			Source:      generated.SubgroupGroupCandidateSource(entry.Source),
			Available:   entry.Available,
		})
	}
	return generated.SubgroupGroupCandidateList{Candidates: candidates}
}

// subgroupGroupCandidatePage projects one page of the dictionary listing.
func subgroupGroupCandidatePage(page catalogapplication.GroupDictionaryPage) generated.SubgroupGroupCandidatePage {
	return generated.SubgroupGroupCandidatePage{
		Candidates: subgroupGroupCandidateList(page.Entries).Candidates,
		NextOffset: page.NextOffset,
		Total:      page.Total,
	}
}
