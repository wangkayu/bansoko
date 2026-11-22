package application

import (
	"context"

	"bansoko/internal/transport/httpapi/generated"
)

// ReevaluateAnimeReleases is the generated-contract compatibility facade.
func (s *Service) ReevaluateAnimeReleases(ctx context.Context, managedAnimeID string) (generated.ReleaseReevaluationResult, error) {
	result, err := s.ReevaluateAnimeReleasesApplication(ctx, managedAnimeID)
	if err != nil {
		return generated.ReleaseReevaluationResult{}, err
	}
	return generated.ReleaseReevaluationResult{
		Accepted:       generated.ReleaseReevaluationResultAccepted(result.Accepted),
		ManagedAnimeId: generated.ID(result.ManagedAnimeID),
		ScheduledCount: result.ScheduledCount,
	}, nil
}
