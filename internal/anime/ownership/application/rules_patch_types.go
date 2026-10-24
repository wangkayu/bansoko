package application

import animedomain "bansoko/internal/anime/domain"

// RulesPatchInput is the transport-neutral input to the rules owner patch transaction.
type RulesPatchInput struct {
	ExpectedVersion int64                     `json:"expected_version"`
	Sections        animedomain.RulesSections `json:"sections"`
}

// RulesPatchResult is the transport-neutral result of a rules owner patch.
type RulesPatchResult struct {
	Reevaluation RulesPatchReevaluation `json:"reevaluation"`
	Rules        RulesOwner             `json:"rules"`
}

// RulesOwner is the application read model returned after a rules patch.
type RulesOwner struct {
	ManagedAnimeID string                     `json:"managed_anime_id"`
	Sections       animedomain.RulesSections  `json:"sections"`
	Effective      animedomain.EffectiveRules `json:"effective"`
	// EffectiveTagFilters is the live-resolved tag_filters section.
	EffectiveTagFilters animedomain.TagFilterRules `json:"effective_tag_filters"`
	// HasAniBTSource reports whether the anime has at least one AniBT RSS
	// source, i.e. whether the AniBT tag row can ever apply.
	HasAniBTSource bool  `json:"has_anibt_source"`
	Version        int64 `json:"version"`
}

// RulesPatchReevaluation reports the release re-evaluation work admitted by a successful rules patch.
type RulesPatchReevaluation struct {
	Accepted       bool   `json:"accepted"`
	ManagedAnimeID string `json:"managed_anime_id"`
	ScheduledCount int    `json:"scheduled_count"`
}
