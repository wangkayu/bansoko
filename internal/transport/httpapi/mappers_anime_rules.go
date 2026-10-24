package httpapi

import (
	"fmt"

	ownershipapplication "bansoko/internal/anime/ownership/application"
	animetransport "bansoko/internal/anime/transport"
	"bansoko/internal/transport/httpapi/generated"
)

func mapRulesPatchInput(value generated.RulesPatch) (ownershipapplication.RulesPatchInput, error) {
	sections, err := animetransport.RulesSections(value.Sections)
	if err != nil {
		return ownershipapplication.RulesPatchInput{}, fmt.Errorf("map rules patch sections: %w", err)
	}
	return ownershipapplication.RulesPatchInput{
		ExpectedVersion: int64(value.ExpectedVersion),
		Sections:        sections,
	}, nil
}

func mapReleaseReevaluationResult(value ownershipapplication.ReleaseReevaluationResult) generated.ReleaseReevaluationResult {
	return generated.ReleaseReevaluationResult{
		Accepted:       generated.ReleaseReevaluationResultAccepted(value.Accepted),
		ManagedAnimeId: generated.ID(value.ManagedAnimeID),
		ScheduledCount: value.ScheduledCount,
	}
}
func mapRulesPatchResult(value ownershipapplication.RulesPatchResult) (generated.RulesPatchResult, error) {
	rules, err := mapRulesOwner(value.Rules)
	if err != nil {
		return generated.RulesPatchResult{}, err
	}
	return generated.RulesPatchResult{
		Reevaluation: generated.ReleaseReevaluationResult{
			Accepted:       generated.ReleaseReevaluationResultAccepted(value.Reevaluation.Accepted),
			ManagedAnimeId: generated.ID(value.Reevaluation.ManagedAnimeID),
			ScheduledCount: value.Reevaluation.ScheduledCount,
		},
		Rules: rules,
	}, nil
}

func mapRulesOwner(value ownershipapplication.RulesOwner) (generated.AnimeRules, error) {
	if value.ManagedAnimeID == "" {
		return generated.AnimeRules{}, fmt.Errorf("rules result has empty managed anime id")
	}
	sections, err := animetransport.RulesSectionsToHTTP(value.Sections)
	if err != nil {
		return generated.AnimeRules{}, fmt.Errorf("map rules sections: %w", err)
	}
	effective, err := animetransport.EffectiveRules(value.Effective)
	if err != nil {
		return generated.AnimeRules{}, fmt.Errorf("map effective rules: %w", err)
	}
	return generated.AnimeRules{
		ManagedAnimeId:      generated.ID(value.ManagedAnimeID),
		Sections:            sections,
		Effective:           effective,
		EffectiveTagFilters: animetransport.TagFilterRulesToHTTP(value.EffectiveTagFilters),
		HasAnibtSource:      value.HasAniBTSource,
		Version:             generated.Version(value.Version),
	}, nil
}
