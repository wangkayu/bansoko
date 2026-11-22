package application

import (
	animedomain "bansoko/internal/anime/domain"
	animetransport "bansoko/internal/anime/transport"
	"bansoko/internal/transport/httpapi/generated"
)

func domainDraft(value generated.AnimeDraft) (animedomain.AnimeDraft, error) {
	return animetransport.Draft(value)
}
