package httpapi

import (
	filemaintenance "bansoko/internal/filemaintenance"
	"bansoko/internal/transport/httpapi/generated"
)

func mapPlacementView(value filemaintenance.PlacementView) generated.PlacementView {
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

func mapPlacementViews(values []filemaintenance.PlacementView) []generated.PlacementView {
	if values == nil {
		return nil
	}
	mapped := make([]generated.PlacementView, len(values))
	for index, value := range values {
		mapped[index] = mapPlacementView(value)
	}
	return mapped
}

func mapPlacementCoverageView(value filemaintenance.PlacementCoverageView) generated.PlacementCoverageView {
	return generated.PlacementCoverageView{
		CoverageOrdinal: value.CoverageOrdinal,
		EvidenceRef:     value.EvidenceRef,
		FactsVersion:    value.FactsVersion,
		ObservationKey:  value.ObservationKey,
	}
}

func mapPlacementCoverageViews(values []filemaintenance.PlacementCoverageView) []generated.PlacementCoverageView {
	if values == nil {
		return nil
	}
	mapped := make([]generated.PlacementCoverageView, len(values))
	for index, value := range values {
		mapped[index] = mapPlacementCoverageView(value)
	}
	return mapped
}

func mapReplacementPlacementRef(value filemaintenance.ReplacementPlacementRef) generated.ReplacementPlacementRef {
	return generated.ReplacementPlacementRef{Fingerprint: value.Fingerprint, PlacementId: value.PlacementID, Version: value.Version}
}

func mapReplacementPoolEvidence(value filemaintenance.ReplacementPoolEvidence) generated.ReplacementPoolEvidence {
	mapped := generated.ReplacementPoolEvidence{
		PoolKey: value.PoolKey,
		Reason:  cloneStringPtr(value.Reason),
		Verdict: generated.ReplacementPoolEvidenceVerdict(value.Verdict),
	}
	if value.Winner != nil {
		winner := mapReplacementPlacementRef(*value.Winner)
		mapped.Winner = &winner
	}
	if value.Losers != nil {
		losers := make([]generated.ReplacementPlacementRef, len(*value.Losers))
		for index, loser := range *value.Losers {
			losers[index] = mapReplacementPlacementRef(loser)
		}
		mapped.Losers = &losers
	}
	return mapped
}

func mapReplacementDecisionView(value filemaintenance.ReplacementDecisionView) generated.ReplacementDecisionView {
	mapped := generated.ReplacementDecisionView{
		AdmissionMode:        generated.ReplacementDecisionViewAdmissionMode(value.AdmissionMode),
		CandidatePlacementId: cloneStringPtr(value.CandidatePlacementID),
		CandidateReleaseId:   value.CandidateReleaseID,
		CoverageKey:          value.CoverageKey,
		CreatedAt:            value.CreatedAt,
		Id:                   value.ID,
		InputDigest:          cloneStringPtr(value.InputDigest),
		ManagedAnimeId:       value.ManagedAnimeID,
		PolicyVersion:        value.PolicyVersion,
		ProfileVersion:       value.ProfileVersion,
		SnapshotGeneration:   cloneIntPtr(value.SnapshotGeneration),
		StaleReason:          cloneStringPtr(value.StaleReason),
		State:                generated.ReplacementDecisionViewState(value.State),
		SubscriptionVersion:  cloneIntPtr(value.SubscriptionVersion),
		Verdict:              generated.ReplacementDecisionViewVerdict(value.Verdict),
		Version:              value.Version,
	}
	if value.Pools != nil {
		pools := make([]generated.ReplacementPoolEvidence, len(*value.Pools))
		for index, pool := range *value.Pools {
			pools[index] = mapReplacementPoolEvidence(pool)
		}
		mapped.Pools = &pools
	}
	return mapped
}

func mapReplacementDecisionViews(values []filemaintenance.ReplacementDecisionView) []generated.ReplacementDecisionView {
	if values == nil {
		return nil
	}
	mapped := make([]generated.ReplacementDecisionView, len(values))
	for index, value := range values {
		mapped[index] = mapReplacementDecisionView(value)
	}
	return mapped
}

func mapOffsetMigrationPlanView(value filemaintenance.OffsetMigrationPlanView) generated.OffsetMigrationPlanView {
	return generated.OffsetMigrationPlanView{
		CreatedAt:      value.CreatedAt,
		FailureReason:  cloneStringPtr(value.FailureReason),
		Id:             value.ID,
		ManagedAnimeId: cloneStringPtr(value.ManagedAnimeID),
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

func mapOffsetMigrationPlanViews(values []filemaintenance.OffsetMigrationPlanView) []generated.OffsetMigrationPlanView {
	if values == nil {
		return nil
	}
	mapped := make([]generated.OffsetMigrationPlanView, len(values))
	for index, value := range values {
		mapped[index] = mapOffsetMigrationPlanView(value)
	}
	return mapped
}

func mapOffsetMigrationGenerationView(value filemaintenance.OffsetMigrationGenerationView) generated.OffsetMigrationGenerationView {
	return generated.OffsetMigrationGenerationView{
		AppliedCount:    cloneIntPtr(value.AppliedCount),
		BlockedCount:    cloneIntPtr(value.BlockedCount),
		CompletedAt:     cloneTimePointer(value.CompletedAt),
		CreatedAt:       value.CreatedAt,
		Id:              value.ID,
		ManagedAnimeId:  value.ManagedAnimeID,
		NewOffset:       value.NewOffset,
		OldOffset:       value.OldOffset,
		PlannedCount:    cloneIntPtr(value.PlannedCount),
		PolicyVersion:   value.PolicyVersion,
		Reason:          cloneStringPtr(value.Reason),
		ReasonCode:      cloneStringPtr(value.ReasonCode),
		ScopeKind:       generated.OffsetMigrationGenerationViewScopeKind(value.ScopeKind),
		ScopeOwnerId:    value.ScopeOwnerID,
		State:           generated.OffsetMigrationGenerationViewState(value.State),
		SupersededCount: cloneIntPtr(value.SupersededCount),
		UpdatedAt:       cloneTimePointer(value.UpdatedAt),
		Version:         value.Version,
	}
}

func mapOffsetMigrationGenerationViews(values []filemaintenance.OffsetMigrationGenerationView) []generated.OffsetMigrationGenerationView {
	if values == nil {
		return nil
	}
	mapped := make([]generated.OffsetMigrationGenerationView, len(values))
	for index, value := range values {
		mapped[index] = mapOffsetMigrationGenerationView(value)
	}
	return mapped
}
