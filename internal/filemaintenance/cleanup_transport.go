package filemaintenance

import (
	"context"

	"bansoko/internal/transport/httpapi/generated"
)

func (s *CleanupService) ListReplacementDeletions(ctx context.Context, animeID string, limit, offset int) ([]generated.ReplacementDeletionView, error) {
	views, err := s.ListReplacementDeletionsApplication(ctx, animeID, limit, offset)
	if err != nil {
		return nil, err
	}
	if views == nil {
		return nil, nil
	}
	mapped := make([]generated.ReplacementDeletionView, len(views))
	for index, value := range views {
		mapped[index] = replacementDeletionViewToGenerated(value)
	}
	return mapped, nil
}

func replacementDeletionViewToGenerated(value ReplacementDeletionView) generated.ReplacementDeletionView {
	return generated.ReplacementDeletionView{
		CreatedAt:         value.CreatedAt,
		FailureCode:       cloneStringPointer(value.FailureCode),
		Id:                value.ID,
		ManagedAnimeId:    value.ManagedAnimeID,
		PlacementId:       value.PlacementID,
		Reason:            cloneStringPointer(value.Reason),
		ReplacementRef:    value.ReplacementRef,
		AdmissionMode:     generated.ReplacementDeletionViewAdmissionMode(value.AdmissionMode),
		SourceFingerprint: value.SourceFingerprint,
		SourcePath:        value.SourcePath,
		State:             generated.ReplacementDeletionViewState(value.State),
		UpdatedAt:         cloneTimePointer(value.UpdatedAt),
		Version:           value.Version,
	}
}
