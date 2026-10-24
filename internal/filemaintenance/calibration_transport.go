package filemaintenance

import (
	"context"

	"bansoko/internal/transport/httpapi/generated"
)

func (s *CalibrationService) ListConsumerCheckpoints(ctx context.Context, animeID string, limit, offset int) ([]generated.ReconciliationConsumerStateView, error) {
	views, err := s.ListConsumerCheckpointsApplication(ctx, animeID, limit, offset)
	if err != nil {
		return nil, err
	}
	if views == nil {
		return nil, nil
	}
	mapped := make([]generated.ReconciliationConsumerStateView, len(views))
	for index, value := range views {
		mapped[index] = reconciliationConsumerStateViewToGenerated(value)
	}
	return mapped, nil
}

func reconciliationConsumerStateViewToGenerated(value ReconciliationConsumerStateView) generated.ReconciliationConsumerStateView {
	return generated.ReconciliationConsumerStateView{
		Consumer:       generated.ReconciliationConsumerStateViewConsumer(value.Consumer),
		Generation:     value.Generation,
		ManagedAnimeId: generated.ID(value.ManagedAnimeID),
		Reason:         cloneStringPointer(value.Reason),
		ReasonCode:     cloneStringPointer(value.ReasonCode),
		State:          generated.ReconciliationConsumerStateViewState(value.State),
		UpdatedAt:      value.UpdatedAt,
		Version:        value.Version,
	}
}
