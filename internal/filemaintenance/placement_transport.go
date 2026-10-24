package filemaintenance

import (
	"context"

	"bansoko/internal/transport/httpapi/generated"
)

func (s *PlacementService) ListPlacements(ctx context.Context, animeID string, limit, offset int) ([]generated.PlacementView, error) {
	views, err := s.ListPlacementsApplication(ctx, animeID, limit, offset)
	if err != nil {
		return nil, err
	}
	return placementViewsToGenerated(views), nil
}

func (s *PlacementService) GetCoverage(ctx context.Context, placementID string) ([]generated.PlacementCoverageView, error) {
	views, err := s.GetCoverageApplication(ctx, placementID)
	if err != nil {
		return nil, err
	}
	return placementCoverageViewsToGenerated(views), nil
}

func placementViewToGenerated(value PlacementView) generated.PlacementView {
	return generated.PlacementView{
		CreatedAt:          value.CreatedAt,
		FactsVersion:       value.FactsVersion,
		Id:                 value.ID,
		ManagedAnimeId:     value.ManagedAnimeID,
		PhysicalPath:       value.PhysicalPath,
		SourceEvidenceType: generated.PlacementViewSourceEvidenceType(value.SourceEvidenceType),
		SourceOwnerRef:     value.SourceOwnerRef,
		State:              generated.PlacementViewState(value.State),
		TargetFingerprint:  value.TargetFingerprint,
		VerificationState:  generated.PlacementViewVerificationState(value.VerificationState),
		Version:            value.Version,
	}
}

func placementViewsToGenerated(values []PlacementView) []generated.PlacementView {
	if values == nil {
		return nil
	}
	mapped := make([]generated.PlacementView, len(values))
	for index, value := range values {
		mapped[index] = placementViewToGenerated(value)
	}
	return mapped
}

func placementCoverageViewToGenerated(value PlacementCoverageView) generated.PlacementCoverageView {
	return generated.PlacementCoverageView{
		CoverageOrdinal: value.CoverageOrdinal,
		EvidenceRef:     value.EvidenceRef,
		FactsVersion:    value.FactsVersion,
		ObservationKey:  value.ObservationKey,
	}
}

func placementCoverageViewsToGenerated(values []PlacementCoverageView) []generated.PlacementCoverageView {
	if values == nil {
		return nil
	}
	mapped := make([]generated.PlacementCoverageView, len(values))
	for index, value := range values {
		mapped[index] = placementCoverageViewToGenerated(value)
	}
	return mapped
}
