package application

import (
	"bansoko/internal/catalog/domain"
	"context"
)

func (s *Service) SetCustomSnapshotApplication(app CustomSnapshotApplication) {
	if app.FreshWindow == nil {
		app.FreshWindow = s.previewFreshWindow
	}
	s.customSnapshots = &app
}
func (s *Service) FetchCustomSnapshot(ctx context.Context, id string, revision, version int64) (domain.ProviderRSSPreviewResult, error) {
	if s == nil || s.customSnapshots == nil {
		return (CustomSnapshotApplication{}).Fetch(ctx, id, revision, version)
	}
	return s.customSnapshots.Fetch(ctx, id, revision, version)
}

// FetchOwnerCustomSnapshot is the pending custom owner's test-bench fetch.
func (s *Service) FetchOwnerCustomSnapshot(ctx context.Context, managedAnimeID string) (domain.ProviderRSSPreviewResult, error) {
	if s == nil || s.customSnapshots == nil {
		return (CustomSnapshotApplication{}).FetchOwner(ctx, managedAnimeID)
	}
	return s.customSnapshots.FetchOwner(ctx, managedAnimeID)
}
