package httpapi

import (
	animedomain "bansoko/internal/anime/domain"
	animetransport "bansoko/internal/anime/transport"
	"bansoko/internal/transport/httpapi/generated"
)

// mapAnimeDraft projects a transport AnimeDraft into the domain draft for the
// RSSListView read model (current_input), the direct subscription command and
// the one-shot admission. It is the same canonical mapping candidate save uses,
// so the draft's frozen business snapshot and digest always survive: a
// hand-written copy here once dropped them, and every current-input preview
// was refused with BUSINESS_SNAPSHOT_REQUIRED.
func mapAnimeDraft(value generated.AnimeDraft) (animedomain.AnimeDraft, error) {
	return animetransport.Draft(value)
}

// mapMetadataHold projects the metadata hold every read model carries. missing is
// a required, non-nullable array in the schema: an empty hold must serialize as
// [] rather than null.
func mapMetadataHold(value animedomain.MetadataHold) generated.MetadataHold {
	missing := make([]generated.MetadataHoldMissing, 0, len(value.Missing))
	for _, item := range value.Missing {
		missing = append(missing, generated.MetadataHoldMissing(item))
	}
	return generated.MetadataHold{Held: value.Held, Missing: missing}
}
