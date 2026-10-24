package filemaintenance

import (
	"context"
	"time"

	"bansoko/internal/transport/httpapi/generated"
)

func (s *OffsetMigrationService) ListMigrations(ctx context.Context, animeID string, policyVersion *int64, limit, offset int) ([]generated.OffsetMigrationPlanView, error) {
	views, err := s.ListMigrationsApplication(ctx, animeID, policyVersion, limit, offset)
	if err != nil {
		return nil, err
	}
	if views == nil {
		return nil, nil
	}
	mapped := make([]generated.OffsetMigrationPlanView, len(views))
	for index, value := range views {
		mapped[index] = offsetMigrationPlanViewToGenerated(value)
	}
	return mapped, nil
}

func (s *OffsetMigrationService) ListGenerations(ctx context.Context, animeID string, limit, offset int) ([]generated.OffsetMigrationGenerationView, error) {
	views, err := s.ListGenerationsApplication(ctx, animeID, limit, offset)
	if err != nil {
		return nil, err
	}
	if views == nil {
		return nil, nil
	}
	mapped := make([]generated.OffsetMigrationGenerationView, len(views))
	for index, value := range views {
		mapped[index] = offsetMigrationGenerationViewToGenerated(value)
	}
	return mapped, nil
}

func offsetMigrationPlanViewToGenerated(value OffsetMigrationPlanView) generated.OffsetMigrationPlanView {
	return generated.OffsetMigrationPlanView{
		CreatedAt:      value.CreatedAt,
		FailureReason:  cloneStringPointer(value.FailureReason),
		Id:             value.ID,
		ManagedAnimeId: cloneStringPointer(value.ManagedAnimeID),
		NewEpisode:     value.NewEpisode,
		NewTargetPath:  value.NewTargetPath,
		OldTargetPath:  value.OldTargetPath,
		PlacementId:    value.PlacementID,
		PolicyVersion:  value.PolicyVersion,
		RawEpisode:     value.RawEpisode,
		State:          generated.OffsetMigrationPlanViewState(value.State),
		UpdatedAt:      cloneTimePointer(value.UpdatedAt),
		Version:        value.Version,
	}
}

func offsetMigrationGenerationViewToGenerated(value OffsetMigrationGenerationView) generated.OffsetMigrationGenerationView {
	return generated.OffsetMigrationGenerationView{
		AppliedCount:    cloneIntPointer(value.AppliedCount),
		BlockedCount:    cloneIntPointer(value.BlockedCount),
		CompletedAt:     cloneTimePointer(value.CompletedAt),
		CreatedAt:       value.CreatedAt,
		Id:              value.ID,
		ManagedAnimeId:  value.ManagedAnimeID,
		NewOffset:       value.NewOffset,
		OldOffset:       value.OldOffset,
		PlannedCount:    cloneIntPointer(value.PlannedCount),
		PolicyVersion:   value.PolicyVersion,
		Reason:          cloneStringPointer(value.Reason),
		ReasonCode:      cloneStringPointer(value.ReasonCode),
		ScopeKind:       generated.OffsetMigrationGenerationViewScopeKind(value.ScopeKind),
		ScopeOwnerId:    value.ScopeOwnerID,
		State:           generated.OffsetMigrationGenerationViewState(value.State),
		SupersededCount: cloneIntPointer(value.SupersededCount),
		UpdatedAt:       cloneTimePointer(value.UpdatedAt),
		Version:         value.Version,
	}
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
