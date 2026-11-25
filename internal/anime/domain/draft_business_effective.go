package domain

import (
	"errors"
)

// ResolveDraftSectionsFromBusiness resolves a draft's complete effective
// sections the way saving the draft freezes them: the draft's own subgroups
// section and its five rules sections, each a complete value copied from Global
// when the draft was created and edited since. Global is never consulted. The
// draft must carry its business snapshot (the frozen policy axes); a draft
// without one fails closed.
func ResolveDraftSectionsFromBusiness(draft AnimeDraft) (ResolvedSections, error) {
	if draft.Business == nil {
		return ResolvedSections{}, errors.New("draft carries no business snapshot")
	}
	return ResolveDraftEffectiveSections(draft)
}
