package httpapi

import (
	animeapplication "bansoko/internal/anime/application"
	ownershipapplication "bansoko/internal/anime/ownership/application"
	"bansoko/internal/transport/httpapi/generated"
)

func mapSubgroupsPatchInput(value generated.SubgroupsPatch) ownershipapplication.SubgroupsPatchInput {
	result := ownershipapplication.SubgroupsPatchInput{
		ExpectedVersion: int64(value.ExpectedVersion),
		Subgroups:       mapSubgroupsSection(value.Subgroups),
	}
	if value.GroupActions != nil {
		result.GroupActions = make([]ownershipapplication.GroupAction, len(value.GroupActions))
	}
	for i, action := range value.GroupActions {
		result.GroupActions[i] = ownershipapplication.GroupAction{
			Action:   ownershipapplication.GroupActionType(action.Action),
			GroupRef: action.GroupRef,
			Provider: string(action.Provider),
		}
	}
	return result
}

func mapSubgroupsSection(value generated.SubgroupsSection) ownershipapplication.SubgroupsSection {
	result := ownershipapplication.SubgroupsSection{MaxRss: value.MaxRss}
	if value.Entries != nil {
		result.Entries = make([]ownershipapplication.SubgroupScoreEntry, len(value.Entries))
	}
	for i, entry := range value.Entries {
		result.Entries[i] = ownershipapplication.SubgroupScoreEntry{
			CountInLimit:      entry.CountInLimit,
			DisplayName:       entry.DisplayName,
			EffectiveSelected: entry.EffectiveSelected,
			GroupRef:          entry.GroupRef,
			ManualSelected:    cloneBoolPtr(entry.ManualSelected),
			Provider:          string(entry.Provider),
			Score:             entry.Score,
			SelectionMode:     string(entry.SelectionMode),
		}
	}
	if value.NameKeywords != nil {
		result.NameKeywords = &ownershipapplication.ProviderNameKeywordRules{
			Anibt: mapNameKeywordRules(value.NameKeywords.Anibt),
			Mikan: mapNameKeywordRules(value.NameKeywords.Mikan),
		}
	}
	if value.ProviderDefaults != nil {
		result.ProviderDefaults = &ownershipapplication.ProviderGroupDefaults{
			Anibt: mapProviderGroupDefault(value.ProviderDefaults.Anibt),
			Mikan: mapProviderGroupDefault(value.ProviderDefaults.Mikan),
		}
	}
	return result
}

func mapNameKeywordRules(value *[]generated.NameKeywordRule) *[]ownershipapplication.NameKeywordRule {
	if value == nil {
		return nil
	}
	result := make([]ownershipapplication.NameKeywordRule, len(*value))
	for i, rule := range *value {
		result[i] = ownershipapplication.NameKeywordRule{Keyword: rule.Keyword, Score: rule.Score}
	}
	return &result
}

func mapProviderGroupDefault(value *generated.ProviderGroupDefault) *ownershipapplication.ProviderGroupDefault {
	if value == nil {
		return nil
	}
	return &ownershipapplication.ProviderGroupDefault{
		Groups:       cloneStrings(value.Groups),
		Mode:         string(value.Mode),
		Score:        value.Score,
		PinnedScores: ownershipapplication.PinnedGroupScoresFromGenerated(value.PinnedScores),
	}
}

func mapSubgroupsOwnerResult(value ownershipapplication.SubgroupsOwnerResult) generated.SubgroupsOwnerResult {
	result := generated.SubgroupsOwnerResult{
		ManagedAnimeId: value.ManagedAnimeID,
		Subgroups:      mapApplicationSubgroupsSection(value.Subgroups),
		Version:        generated.Version(value.Version),
	}
	if value.EffectiveSelection != nil {
		result.EffectiveSelection = make([]generated.ProviderGroupSelection, len(value.EffectiveSelection))
	}
	if value.RSSSources != nil {
		result.RssSources = make([]generated.RSSSource, len(value.RSSSources))
	}
	for i, selection := range value.EffectiveSelection {
		result.EffectiveSelection[i] = generated.ProviderGroupSelection{
			Available:              &selection.Available,
			AvailabilityGeneration: &selection.AvailabilityGeneration,
			CountInLimit:           selection.CountInLimit,
			DisplayName:            selection.DisplayName,
			EffectiveScore:         selection.EffectiveScore,
			EffectiveSelected:      selection.EffectiveSelected,
			GroupRef:               selection.GroupRef,
			Id:                     selection.ID,
			ManualSelected:         cloneBoolPtr(selection.ManualSelected),
			Provider:               generated.ProviderKind(selection.Provider),
			ReasonCode:             cloneStringPtr(selection.ReasonCode),
			ScoreBreakdown:         ownershipapplication.SubgroupScoreBreakdownToGenerated(selection.ScoreBreakdown),
			SelectionMode:          generated.SelectionMode(selection.SelectionMode),
			SubscriptionId:         selection.SubscriptionID,
			Version:                generated.Version(selection.Version),
		}
	}
	for i, source := range value.RSSSources {
		result.RssSources[i] = mapApplicationRSSSource(source)
	}
	return result
}

func mapApplicationSubgroupsSection(value ownershipapplication.SubgroupsSection) generated.SubgroupsSection {
	result := generated.SubgroupsSection{MaxRss: value.MaxRss}
	if value.Entries != nil {
		result.Entries = make([]generated.SubgroupScoreEntry, len(value.Entries))
	}
	for i, entry := range value.Entries {
		result.Entries[i] = generated.SubgroupScoreEntry{
			CountInLimit: entry.CountInLimit, DisplayName: entry.DisplayName,
			EffectiveSelected: entry.EffectiveSelected, GroupRef: entry.GroupRef,
			ManualSelected: cloneBoolPtr(entry.ManualSelected), Provider: generated.ProviderKind(entry.Provider),
			Score: entry.Score, SelectionMode: generated.SelectionMode(entry.SelectionMode),
		}
	}
	if value.NameKeywords != nil {
		result.NameKeywords = &generated.ProviderNameKeywordRules{Anibt: mapApplicationNameKeywordRules(value.NameKeywords.Anibt), Mikan: mapApplicationNameKeywordRules(value.NameKeywords.Mikan)}
	}
	if value.ProviderDefaults != nil {
		result.ProviderDefaults = &generated.ProviderGroupDefaults{Anibt: mapApplicationProviderGroupDefault(value.ProviderDefaults.Anibt), Mikan: mapApplicationProviderGroupDefault(value.ProviderDefaults.Mikan)}
	}
	return result
}

func mapApplicationNameKeywordRules(value *[]ownershipapplication.NameKeywordRule) *[]generated.NameKeywordRule {
	if value == nil {
		return nil
	}
	result := make([]generated.NameKeywordRule, len(*value))
	for i, rule := range *value {
		result[i] = generated.NameKeywordRule{Keyword: rule.Keyword, Score: rule.Score}
	}
	return &result
}

func mapApplicationProviderGroupDefault(value *ownershipapplication.ProviderGroupDefault) *generated.ProviderGroupDefault {
	if value == nil {
		return nil
	}
	return &generated.ProviderGroupDefault{Groups: cloneStrings(value.Groups), Mode: generated.ProviderGroupDefaultMode(value.Mode), Score: value.Score, PinnedScores: ownershipapplication.PinnedGroupScoresToGenerated(value.PinnedScores)}
}

func mapApplicationRSSSource(value animeapplication.RSSSource) generated.RSSSource {
	result := generated.RSSSource{
		GroupName: value.GroupName, Id: value.ID, LocatorDisplay: value.LocatorDisplay,
		SourceKind: generated.SourceKind(value.SourceKind), SubscriptionId: value.SubscriptionID,
		Title: value.Title, Version: generated.Version(value.Version),
		EpisodeOffset: value.EpisodeOffset, HttpFeedConnectionRef: cloneStringPtr(value.HttpFeedConnectionRef),
		NetworkRouteRef: cloneStringPtr(value.NetworkRouteRef),
	}
	if value.AnibtAuxMode != nil {
		mode := generated.RSSSourceAnibtAuxMode(*value.AnibtAuxMode)
		result.AnibtAuxMode = &mode
	}
	if value.ProviderProvenance != nil {
		result.ProviderProvenance = &generated.ProviderProvenance{
			DiscoverySnapshotVersion: value.ProviderProvenance.DiscoverySnapshotVersion,
			Provider:                 generated.ProviderKind(value.ProviderProvenance.Provider), ProviderEntryRef: value.ProviderProvenance.ProviderEntryRef,
			ProviderGroupRef: value.ProviderProvenance.ProviderGroupRef,
		}
	}
	return result
}

func cloneBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
func cloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
