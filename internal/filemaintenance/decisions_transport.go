package filemaintenance

import (
	"context"

	"bansoko/internal/transport/httpapi/generated"
)

func (s *ReplacementDecisionService) EvaluateServer(ctx context.Context, input EvaluateRequest) ([]generated.ReplacementDecisionView, error) {
	views, err := s.EvaluateApplication(ctx, input)
	if err != nil {
		return nil, err
	}
	return replacementDecisionViewsToGenerated(views), nil
}

func (s *ReplacementDecisionService) ListDecisions(ctx context.Context, animeID string, limit, offset int) ([]generated.ReplacementDecisionView, error) {
	views, err := s.ListDecisionsApplication(ctx, animeID, limit, offset)
	if err != nil {
		return nil, err
	}
	return replacementDecisionViewsToGenerated(views), nil
}

func replacementDecisionViewToGenerated(value ReplacementDecisionView) generated.ReplacementDecisionView {
	mapped := generated.ReplacementDecisionView{
		AdmissionMode:        generated.ReplacementDecisionViewAdmissionMode(value.AdmissionMode),
		CandidatePlacementId: cloneStringPointer(value.CandidatePlacementID),
		CandidateReleaseId:   value.CandidateReleaseID,
		CoverageKey:          value.CoverageKey,
		CreatedAt:            value.CreatedAt,
		Id:                   value.ID,
		InputDigest:          cloneStringPointer(value.InputDigest),
		ManagedAnimeId:       value.ManagedAnimeID,
		PolicyVersion:        value.PolicyVersion,
		ProfileVersion:       value.ProfileVersion,
		SnapshotGeneration:   cloneIntPointer(value.SnapshotGeneration),
		StaleReason:          cloneStringPointer(value.StaleReason),
		State:                generated.ReplacementDecisionViewState(value.State),
		SubscriptionVersion:  cloneIntPointer(value.SubscriptionVersion),
		Verdict:              generated.ReplacementDecisionViewVerdict(value.Verdict),
		Version:              value.Version,
	}
	if value.Pools != nil {
		pools := make([]generated.ReplacementPoolEvidence, 0, len(*value.Pools))
		for _, pool := range *value.Pools {
			mappedPool := generated.ReplacementPoolEvidence{
				PoolKey: pool.PoolKey,
				Reason:  cloneStringPointer(pool.Reason),
				Verdict: generated.ReplacementPoolEvidenceVerdict(pool.Verdict),
			}
			if pool.Winner != nil {
				winner := generated.ReplacementPlacementRef{Fingerprint: pool.Winner.Fingerprint, PlacementId: pool.Winner.PlacementID, Version: pool.Winner.Version}
				mappedPool.Winner = &winner
			}
			if pool.Losers != nil {
				losers := make([]generated.ReplacementPlacementRef, len(*pool.Losers))
				for index, loser := range *pool.Losers {
					losers[index] = generated.ReplacementPlacementRef{Fingerprint: loser.Fingerprint, PlacementId: loser.PlacementID, Version: loser.Version}
				}
				mappedPool.Losers = &losers
			}
			if len(pool.Steps) > 0 {
				steps := make([]generated.ReplacementCompareStep, len(pool.Steps))
				for index, step := range pool.Steps {
					steps[index] = generated.ReplacementCompareStep{
						Key:             step.Key,
						Result:          generated.ReplacementCompareStepResult(step.Result),
						Candidate:       optionalString(step.Candidate),
						Current:         optionalString(step.Current),
						CandidateListed: optionalTrue(step.CandidateListed),
						CurrentListed:   optionalTrue(step.CurrentListed),
					}
				}
				mappedPool.Steps = &steps
			}
			pools = append(pools, mappedPool)
		}
		mapped.Pools = &pools
	}
	return mapped
}

func replacementDecisionViewsToGenerated(values []ReplacementDecisionView) []generated.ReplacementDecisionView {
	if values == nil {
		return nil
	}
	mapped := make([]generated.ReplacementDecisionView, len(values))
	for index, value := range values {
		mapped[index] = replacementDecisionViewToGenerated(value)
	}
	return mapped
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalTrue(value bool) *bool {
	if !value {
		return nil
	}
	return &value
}
