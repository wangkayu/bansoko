package filemaintenance

import (
	"context"

	"bansoko/internal/transport/httpapi/generated"
)

func (s *CleanupService) ListCleanups(ctx context.Context, animeID string, limit, offset int) ([]generated.CleanupOperationView, error) {
	views, err := s.ListCleanupsApplication(ctx, animeID, limit, offset)
	if err != nil {
		return nil, err
	}
	if views == nil {
		return nil, nil
	}
	mapped := make([]generated.CleanupOperationView, len(views))
	for index, value := range views {
		mapped[index] = cleanupOperationViewToGenerated(value)
	}
	return mapped, nil
}

func cleanupOperationViewToGenerated(value CleanupOperationView) generated.CleanupOperationView {
	return generated.CleanupOperationView{
		CreatedAt:         value.CreatedAt,
		FailureCode:       cloneStringPointer(value.FailureCode),
		Id:                value.ID,
		ManagedAnimeId:    value.ManagedAnimeID,
		PlacementId:       value.PlacementID,
		Reason:            cloneStringPointer(value.Reason),
		ReplacementRef:    value.ReplacementRef,
		RetentionMode:     generated.CleanupOperationViewRetentionMode(value.RetentionMode),
		SourceFingerprint: value.SourceFingerprint,
		SourcePath:        value.SourcePath,
		State:             generated.CleanupOperationViewState(value.State),
		UpdatedAt:         cloneTimePointer(value.UpdatedAt),
		Version:           value.Version,
	}
}
