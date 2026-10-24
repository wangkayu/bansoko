package filemaintenance

import (
	"context"

	"bansoko/internal/transport/httpapi/generated"
)

func (s *AlignmentService) ListAlignments(ctx context.Context, animeID string, limit, offset int) ([]generated.AlignmentOperationView, error) {
	views, err := s.ListAlignmentsApplication(ctx, animeID, limit, offset)
	if err != nil {
		return nil, err
	}
	if views == nil {
		return nil, nil
	}
	mapped := make([]generated.AlignmentOperationView, len(views))
	for index, value := range views {
		mapped[index] = alignmentOperationViewToGenerated(value)
	}
	return mapped, nil
}

func alignmentOperationViewToGenerated(value AlignmentOperationView) generated.AlignmentOperationView {
	return generated.AlignmentOperationView{
		CompletedAt:       cloneTimePointer(value.CompletedAt),
		CreatedAt:         value.CreatedAt,
		FailureCode:       cloneStringPointer(value.FailureCode),
		Id:                value.ID,
		ManagedAnimeId:    value.ManagedAnimeID,
		ObservationRef:    value.ObservationRef,
		PolicyVersion:     cloneIntPointer(value.PolicyVersion),
		Reason:            cloneStringPointer(value.Reason),
		ScanGeneration:    value.ScanGeneration,
		SourceFingerprint: value.SourceFingerprint,
		SourcePath:        value.SourcePath,
		State:             generated.AlignmentOperationViewState(value.State),
		TargetPath:        value.TargetPath,
		UpdatedAt:         cloneTimePointer(value.UpdatedAt),
		Version:           value.Version,
	}
}
