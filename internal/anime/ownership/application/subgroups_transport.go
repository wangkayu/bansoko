package application

import (
	"context"

	"bansoko/internal/transport/httpapi/generated"
)

// PatchAnimeSubgroups is the generated-contract compatibility facade.
func (s *Service) PatchAnimeSubgroups(ctx context.Context, managedAnimeID string, patch generated.SubgroupsPatch) (generated.SubgroupsOwnerResult, error) {
	input := SubgroupsPatchInput{
		ExpectedVersion: int64(patch.ExpectedVersion),
		Subgroups:       subgroupsSectionFromGenerated(patch.Subgroups),
	}
	if patch.GroupActions != nil {
		input.GroupActions = make([]GroupAction, len(patch.GroupActions))
	}
	for i, action := range patch.GroupActions {
		input.GroupActions[i] = GroupAction{Action: GroupActionType(action.Action), GroupRef: action.GroupRef, Provider: string(action.Provider)}
	}
	result, err := s.PatchAnimeSubgroupsApplication(ctx, managedAnimeID, input)
	if err != nil {
		return generated.SubgroupsOwnerResult{}, err
	}
	return subgroupsOwnerToGenerated(result), nil
}
