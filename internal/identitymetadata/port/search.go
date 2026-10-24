package port

import (
	"context"

	"bansoko/internal/identitymetadata/domain"
)

// CandidateSearcher searches external metadata providers without applying a
// result. Application owners decide when a complete binding may be persisted.
type CandidateSearcher interface {
	Search(context.Context, string, string, int) ([]domain.SearchCandidate, error)
}
