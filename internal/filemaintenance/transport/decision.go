package transport

import (
	filemaintenance "bansoko/internal/filemaintenance"
	"bansoko/internal/transport/httpapi/generated"
)

func ReplacementEvaluateInput(value generated.ReplacementEvaluateInput) filemaintenance.EvaluateRequest {
	return filemaintenance.EvaluateRequest{
		AnimeID:          string(value.ManagedAnimeId),
		CandidateRelease: string(value.CandidateReleaseId),
	}
}

// ReplacementDecisionViews maps the per-key decisions of one evaluation.
func ReplacementDecisionViews(values []filemaintenance.ReplacementDecisionView) []generated.ReplacementDecisionView {
	mapped := make([]generated.ReplacementDecisionView, len(values))
	for index, value := range values {
		mapped[index] = ReplacementDecisionView(value)
	}
	return mapped
}

func ReplacementDecisionView(value filemaintenance.ReplacementDecisionView) generated.ReplacementDecisionView {
	mapped := generated.ReplacementDecisionView{
		AdmissionMode:        generated.ReplacementDecisionViewAdmissionMode(value.AdmissionMode),
		CandidatePlacementId: cloneDecisionString(value.CandidatePlacementID),
		CandidateReleaseId:   value.CandidateReleaseID,
		CoverageKey:          value.CoverageKey,
		CreatedAt:            value.CreatedAt,
		Id:                   value.ID,
		InputDigest:          cloneDecisionString(value.InputDigest),
		ManagedAnimeId:       value.ManagedAnimeID,
		PolicyVersion:        value.PolicyVersion,
		ProfileVersion:       value.ProfileVersion,
		SnapshotGeneration:   cloneDecisionInt(value.SnapshotGeneration),
		StaleReason:          cloneDecisionString(value.StaleReason),
		State:                generated.ReplacementDecisionViewState(value.State),
		SubscriptionVersion:  cloneDecisionInt(value.SubscriptionVersion),
		Verdict:              generated.ReplacementDecisionViewVerdict(value.Verdict),
		Version:              value.Version,
	}
	if value.Pools != nil {
		pools := make([]generated.ReplacementPoolEvidence, len(*value.Pools))
		for index, pool := range *value.Pools {
			mappedPool := generated.ReplacementPoolEvidence{
				PoolKey: pool.PoolKey,
				Reason:  cloneDecisionString(pool.Reason),
				Verdict: generated.ReplacementPoolEvidenceVerdict(pool.Verdict),
			}
			if pool.Winner != nil {
				winner := generated.ReplacementPlacementRef{
					Fingerprint: pool.Winner.Fingerprint,
					PlacementId: pool.Winner.PlacementID,
					Version:     pool.Winner.Version,
				}
				mappedPool.Winner = &winner
			}
			if pool.Losers != nil {
				losers := make([]generated.ReplacementPlacementRef, len(*pool.Losers))
				for loserIndex, loser := range *pool.Losers {
					losers[loserIndex] = generated.ReplacementPlacementRef{
						Fingerprint: loser.Fingerprint,
						PlacementId: loser.PlacementID,
						Version:     loser.Version,
					}
				}
				mappedPool.Losers = &losers
			}
			pools[index] = mappedPool
		}
		mapped.Pools = &pools
	}
	return mapped
}

func cloneDecisionString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneDecisionInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
