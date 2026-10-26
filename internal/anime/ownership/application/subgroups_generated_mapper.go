package application

import (
	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/transport/httpapi/generated"
)

func selectionGenerationValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func subgroupsSectionToGenerated(value SubgroupsSection) generated.SubgroupsSection {
	result := generated.SubgroupsSection{MaxRss: value.MaxRss}
	if value.Entries != nil {
		result.Entries = make([]generated.SubgroupScoreEntry, len(value.Entries))
	}
	for i, entry := range value.Entries {
		result.Entries[i] = generated.SubgroupScoreEntry{
			DisplayName:       entry.DisplayName,
			EffectiveSelected: entry.EffectiveSelected, GroupRef: entry.GroupRef,
			ManualSelected: cloneBoolValue(entry.ManualSelected), Provider: generated.ProviderKind(entry.Provider),
			Score: entry.Score, SelectionMode: generated.SelectionMode(entry.SelectionMode),
		}
	}
	if value.NameKeywords != nil {
		result.NameKeywords = &generated.ProviderNameKeywordRules{
			Anibt: nameKeywordRulesToGenerated(value.NameKeywords.Anibt),
			Mikan: nameKeywordRulesToGenerated(value.NameKeywords.Mikan),
		}
	}
	if value.ProviderDefaults != nil {
		result.ProviderDefaults = &generated.ProviderGroupDefaults{
			Anibt: providerGroupDefaultToGenerated(value.ProviderDefaults.Anibt),
			Mikan: providerGroupDefaultToGenerated(value.ProviderDefaults.Mikan),
		}
	}
	return result
}

func nameKeywordRulesToGenerated(value *[]NameKeywordRule) *[]generated.NameKeywordRule {
	if value == nil {
		return nil
	}
	result := make([]generated.NameKeywordRule, len(*value))
	for i, rule := range *value {
		result[i] = generated.NameKeywordRule{Keyword: rule.Keyword, Score: rule.Score}
	}
	return &result
}

func providerGroupDefaultToGenerated(value *ProviderGroupDefault) *generated.ProviderGroupDefault {
	if value == nil {
		return nil
	}
	return &generated.ProviderGroupDefault{Groups: animeapplication.CloneStringSlice(value.Groups), Mode: generated.ProviderGroupDefaultMode(value.Mode), Score: value.Score, PinnedScores: PinnedGroupScoresToGenerated(value.PinnedScores)}
}

// PinnedGroupScoresToGenerated is the one public projection of a site's
// pinned scores; nil (no pin field) stays absent.
func PinnedGroupScoresToGenerated(value []PinnedGroupScore) *[]generated.PinnedGroupScore {
	if value == nil {
		return nil
	}
	result := make([]generated.PinnedGroupScore, len(value))
	for i, pin := range value {
		result[i] = generated.PinnedGroupScore{GroupRef: pin.GroupRef, Score: pin.Score}
	}
	return &result
}

// PinnedGroupScoresFromGenerated is the inverse of
// PinnedGroupScoresToGenerated.
func PinnedGroupScoresFromGenerated(value *[]generated.PinnedGroupScore) []PinnedGroupScore {
	if value == nil {
		return nil
	}
	result := make([]PinnedGroupScore, len(*value))
	for i, pin := range *value {
		result[i] = PinnedGroupScore{GroupRef: pin.GroupRef, Score: pin.Score}
	}
	return result
}

func subgroupsOwnerFromGenerated(value generated.SubgroupsOwnerResult) SubgroupsOwnerResult {
	result := SubgroupsOwnerResult{ManagedAnimeID: string(value.ManagedAnimeId), Subgroups: subgroupsSectionFromGenerated(value.Subgroups), Version: int64(value.Version)}
	if value.EffectiveSelection != nil {
		result.EffectiveSelection = make([]ProviderGroupSelection, len(value.EffectiveSelection))
	}
	if value.RssSources != nil {
		result.RSSSources = make([]RSSSource, len(value.RssSources))
	}
	for i, selection := range value.EffectiveSelection {
		result.EffectiveSelection[i] = ProviderGroupSelection{
			DisplayName:    selection.DisplayName,
			EffectiveScore: selection.EffectiveScore, EffectiveSelected: selection.EffectiveSelected,
			GroupRef: selection.GroupRef, ID: string(selection.Id), ManualSelected: cloneBoolValue(selection.ManualSelected),
			Provider: string(selection.Provider), ReasonCode: cloneStringValue(selection.ReasonCode),
			ScoreBreakdown: SubgroupScoreBreakdownFromGenerated(selection.ScoreBreakdown),
			SelectionMode:  string(selection.SelectionMode),
			SubscriptionID: string(selection.SubscriptionId), Version: int64(selection.Version),
			Available:              selection.Available != nil && *selection.Available,
			AvailabilityGeneration: selectionGenerationValue(selection.AvailabilityGeneration),
		}
	}
	for i, source := range value.RssSources {
		result.RSSSources[i] = rssSourceFromGenerated(source)
	}
	return result
}

func subgroupsOwnerToGenerated(value SubgroupsOwnerResult) generated.SubgroupsOwnerResult {
	result := generated.SubgroupsOwnerResult{
		ManagedAnimeId: generated.ID(value.ManagedAnimeID),
		Subgroups:      subgroupsSectionToGenerated(value.Subgroups),
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
			DisplayName:    selection.DisplayName,
			EffectiveScore: selection.EffectiveScore, EffectiveSelected: selection.EffectiveSelected,
			GroupRef: selection.GroupRef, Id: generated.ID(selection.ID), ManualSelected: cloneBoolValue(selection.ManualSelected),
			Provider: generated.ProviderKind(selection.Provider), ReasonCode: cloneStringValue(selection.ReasonCode),
			ScoreBreakdown: SubgroupScoreBreakdownToGenerated(selection.ScoreBreakdown),
			SelectionMode:  generated.SelectionMode(selection.SelectionMode),
			SubscriptionId: generated.ID(selection.SubscriptionID), Version: generated.Version(selection.Version),
			Available: &selection.Available, AvailabilityGeneration: &selection.AvailabilityGeneration,
		}
	}
	for i, source := range value.RSSSources {
		result.RssSources[i] = rssSourceToGenerated(source)
	}
	return result
}

func rssSourceToGenerated(value RSSSource) generated.RSSSource {
	result := generated.RSSSource{
		GroupName: value.GroupName, Id: generated.ID(value.ID), LocatorDisplay: value.LocatorDisplay,
		EpisodeOffset: value.EpisodeOffset, SourceKind: generated.SourceKind(value.SourceKind),
		SubscriptionId: generated.ID(value.SubscriptionID), Title: value.Title, Version: generated.Version(value.Version),
		HttpFeedConnectionRef: cloneStringValue(value.HttpFeedConnectionRef), NetworkRouteRef: cloneStringValue(value.NetworkRouteRef),
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
func subgroupsSectionFromGenerated(value generated.SubgroupsSection) SubgroupsSection {
	result := SubgroupsSection{MaxRss: value.MaxRss}
	if value.Entries != nil {
		result.Entries = make([]SubgroupScoreEntry, len(value.Entries))
	}
	for i, entry := range value.Entries {
		result.Entries[i] = SubgroupScoreEntry{
			DisplayName:       entry.DisplayName,
			EffectiveSelected: entry.EffectiveSelected, GroupRef: entry.GroupRef,
			ManualSelected: cloneBoolValue(entry.ManualSelected), Provider: string(entry.Provider),
			Score: entry.Score, SelectionMode: string(entry.SelectionMode),
		}
	}
	if value.NameKeywords != nil {
		result.NameKeywords = &ProviderNameKeywordRules{Anibt: nameKeywordRulesFromGenerated(value.NameKeywords.Anibt), Mikan: nameKeywordRulesFromGenerated(value.NameKeywords.Mikan)}
	}
	if value.ProviderDefaults != nil {
		result.ProviderDefaults = &ProviderGroupDefaults{Anibt: providerGroupDefaultFromGenerated(value.ProviderDefaults.Anibt), Mikan: providerGroupDefaultFromGenerated(value.ProviderDefaults.Mikan)}
	}
	return result
}

func nameKeywordRulesFromGenerated(value *[]generated.NameKeywordRule) *[]NameKeywordRule {
	if value == nil {
		return nil
	}
	result := make([]NameKeywordRule, len(*value))
	for i, rule := range *value {
		result[i] = NameKeywordRule{Keyword: rule.Keyword, Score: rule.Score}
	}
	return &result
}

func providerGroupDefaultFromGenerated(value *generated.ProviderGroupDefault) *ProviderGroupDefault {
	if value == nil {
		return nil
	}
	return &ProviderGroupDefault{Groups: animeapplication.CloneStringSlice(value.Groups), Mode: string(value.Mode), Score: value.Score, PinnedScores: PinnedGroupScoresFromGenerated(value.PinnedScores)}
}

func rssSourceFromGenerated(value generated.RSSSource) RSSSource {
	result := RSSSource{
		GroupName: value.GroupName, ID: string(value.Id), LocatorDisplay: value.LocatorDisplay,
		SourceKind: animedomain.SourceKind(value.SourceKind), SubscriptionID: string(value.SubscriptionId), Title: value.Title,
		Version: int64(value.Version), EpisodeOffset: value.EpisodeOffset,
		HttpFeedConnectionRef: cloneStringValue(value.HttpFeedConnectionRef), NetworkRouteRef: cloneStringValue(value.NetworkRouteRef),
	}
	if value.AnibtAuxMode != nil {
		mode := animedomain.RSSSourceDraftAnibtAuxMode(*value.AnibtAuxMode)
		result.AnibtAuxMode = &mode
	}
	if value.ProviderProvenance != nil {
		result.ProviderProvenance = &animedomain.ProviderProvenance{
			DiscoverySnapshotVersion: value.ProviderProvenance.DiscoverySnapshotVersion,
			Provider:                 animedomain.ProviderKind(value.ProviderProvenance.Provider), ProviderEntryRef: value.ProviderProvenance.ProviderEntryRef,
			ProviderGroupRef: value.ProviderProvenance.ProviderGroupRef,
		}
	}
	return result
}

func cloneBoolValue(value *bool) *bool {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneStringValue(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

// SubgroupScoreBreakdownToGenerated is the one public projection of a score
// breakdown; every transport that lists a group selection uses it.
func SubgroupScoreBreakdownToGenerated(value *SubgroupScoreBreakdown) *generated.SubgroupScoreBreakdown {
	if value == nil {
		return nil
	}
	result := &generated.SubgroupScoreBreakdown{
		SiteMode: generated.SubgroupScoreBreakdownSiteMode(value.SiteMode), SiteDefaultScore: value.SiteDefaultScore,
		ListEffect:    generated.SubgroupScoreBreakdownListEffect(value.ListEffect),
		EntryOverride: generated.SubgroupScoreBreakdownEntryOverride(value.EntryOverride),
		Keywords:      make([]generated.NameKeywordRule, len(value.Keywords)),
		FinalScore:    cloneIntValue(value.FinalScore),
		PinnedScore:   cloneIntValue(value.PinnedScore),
	}
	for i, keyword := range value.Keywords {
		result.Keywords[i] = generated.NameKeywordRule{Keyword: keyword.Keyword, Score: keyword.Score}
	}
	return result
}

// SubgroupScoreBreakdownFromGenerated is the inverse of
// SubgroupScoreBreakdownToGenerated.
func SubgroupScoreBreakdownFromGenerated(value *generated.SubgroupScoreBreakdown) *SubgroupScoreBreakdown {
	if value == nil {
		return nil
	}
	result := &SubgroupScoreBreakdown{
		SiteMode: string(value.SiteMode), SiteDefaultScore: value.SiteDefaultScore,
		ListEffect: string(value.ListEffect), EntryOverride: string(value.EntryOverride),
		Keywords: make([]NameKeywordRule, len(value.Keywords)), FinalScore: cloneIntValue(value.FinalScore),
		PinnedScore: cloneIntValue(value.PinnedScore),
	}
	for i, keyword := range value.Keywords {
		result.Keywords[i] = NameKeywordRule{Keyword: keyword.Keyword, Score: keyword.Score}
	}
	return result
}

func cloneIntValue(value *int) *int {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
