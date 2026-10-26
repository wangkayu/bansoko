package httpapi

import (
	ownershipapplication "bansoko/internal/anime/ownership/application"
	"bansoko/internal/transport/httpapi/generated"
)

func mapDiscoverSubgroupsInput(value generated.DiscoverSubgroupsInput) ownershipapplication.DiscoverSubgroupsInput {
	return ownershipapplication.DiscoverSubgroupsInput{ExpectedVersion: int64(value.ExpectedVersion)}
}

func mapDiscoverSubgroupsResult(value ownershipapplication.DiscoverSubgroupsResult) (generated.DiscoverSubgroupsResult, error) {
	subgroups, err := mapDiscoverSubgroupsOwner(value.Subgroups)
	if err != nil {
		return generated.DiscoverSubgroupsResult{}, err
	}
	result := generated.DiscoverSubgroupsResult{
		ManagedAnimeId: generated.ID(value.ManagedAnimeID),
		Outcome:        generated.DiscoverSubgroupsOutcome(value.Outcome),
		Subgroups:      subgroups,
	}
	if value.Reason != nil {
		reason := *value.Reason
		result.Reason = &reason
	}
	return result, nil
}

func mapDiscoverSubgroupsOwner(value ownershipapplication.DiscoverSubgroupsOwnerResult) (generated.SubgroupsOwnerResult, error) {
	result := generated.SubgroupsOwnerResult{
		ManagedAnimeId:     generated.ID(value.ManagedAnimeID),
		Subgroups:          mapDiscoverSubgroupsSection(value.Subgroups),
		EffectiveSelection: make([]generated.ProviderGroupSelection, 0, len(value.EffectiveSelection)),
		RssSources:         make([]generated.RSSSource, 0, len(value.RSSources)),
		Version:            generated.Version(value.Version),
	}
	for _, selection := range value.EffectiveSelection {
		result.EffectiveSelection = append(result.EffectiveSelection, generated.ProviderGroupSelection{
			DisplayName:       selection.DisplayName,
			EffectiveScore:    selection.EffectiveScore,
			EffectiveSelected: selection.EffectiveSelected,
			GroupRef:          selection.GroupRef,
			Id:                generated.ID(selection.ID),
			ManualSelected:    cloneHTTPBool(selection.ManualSelected),
			Provider:          generated.ProviderKind(selection.Provider),
			ReasonCode:        cloneStringPtr(selection.ReasonCode),
			ScoreBreakdown:    ownershipapplication.SubgroupScoreBreakdownToGenerated(selection.ScoreBreakdown),
			SelectionMode:     generated.SelectionMode(selection.SelectionMode),
			SubscriptionId:    generated.ID(selection.SubscriptionID),
			Version:           generated.Version(selection.Version),
		})
	}
	for _, source := range value.RSSources {
		result.RssSources = append(result.RssSources, mapDiscoverSubgroupsRSSSource(source))
	}
	return result, nil
}

func mapDiscoverSubgroupsSection(value ownershipapplication.SubgroupsSection) generated.SubgroupsSection {
	result := generated.SubgroupsSection{
		Entries: make([]generated.SubgroupScoreEntry, 0, len(value.Entries)),
		MaxRss:  value.MaxRss,
	}
	for _, entry := range value.Entries {
		result.Entries = append(result.Entries, generated.SubgroupScoreEntry{
			DisplayName:       entry.DisplayName,
			EffectiveSelected: entry.EffectiveSelected,
			GroupRef:          entry.GroupRef,
			ManualSelected:    cloneHTTPBool(entry.ManualSelected),
			Provider:          generated.ProviderKind(entry.Provider),
			Score:             entry.Score,
			SelectionMode:     generated.SelectionMode(entry.SelectionMode),
		})
	}
	if value.NameKeywords != nil {
		result.NameKeywords = &generated.ProviderNameKeywordRules{
			Anibt: mapDiscoverNameKeywordRules(value.NameKeywords.Anibt),
			Mikan: mapDiscoverNameKeywordRules(value.NameKeywords.Mikan),
		}
	}
	if value.ProviderDefaults != nil {
		result.ProviderDefaults = &generated.ProviderGroupDefaults{
			Anibt: mapDiscoverProviderGroupDefault(value.ProviderDefaults.Anibt),
			Mikan: mapDiscoverProviderGroupDefault(value.ProviderDefaults.Mikan),
		}
	}
	return result
}

func mapDiscoverNameKeywordRules(value *[]ownershipapplication.NameKeywordRule) *[]generated.NameKeywordRule {
	if value == nil {
		return nil
	}
	result := make([]generated.NameKeywordRule, 0, len(*value))
	for _, rule := range *value {
		result = append(result, generated.NameKeywordRule{Keyword: rule.Keyword, Score: rule.Score})
	}
	return &result
}

func mapDiscoverProviderGroupDefault(value *ownershipapplication.ProviderGroupDefault) *generated.ProviderGroupDefault {
	if value == nil {
		return nil
	}
	return &generated.ProviderGroupDefault{
		Groups:       append([]string(nil), value.Groups...),
		Mode:         generated.ProviderGroupDefaultMode(value.Mode),
		Score:        value.Score,
		PinnedScores: ownershipapplication.PinnedGroupScoresToGenerated(value.PinnedScores),
	}
}

func mapDiscoverSubgroupsRSSSource(value ownershipapplication.DiscoverRSSSource) generated.RSSSource {
	result := generated.RSSSource{
		GroupName:             value.GroupName,
		HttpFeedConnectionRef: cloneHTTPString(value.HttpFeedConnectionRef),
		Id:                    generated.ID(value.ID),
		LocatorDisplay:        value.LocatorDisplay,
		NetworkRouteRef:       cloneHTTPString(value.NetworkRouteRef),
		EpisodeOffset:         value.EpisodeOffset,
		SourceKind:            generated.SourceKind(value.SourceKind),
		SubscriptionId:        generated.ID(value.SubscriptionID),
		Title:                 value.Title,
		Version:               generated.Version(value.Version),
	}
	if value.AnibtAuxMode != nil {
		mode := generated.RSSSourceAnibtAuxMode(*value.AnibtAuxMode)
		result.AnibtAuxMode = &mode
	}
	if value.ProviderProvenance != nil {
		result.ProviderProvenance = &generated.ProviderProvenance{
			DiscoverySnapshotVersion: value.ProviderProvenance.DiscoverySnapshotVersion,
			Provider:                 generated.ProviderKind(value.ProviderProvenance.Provider),
			ProviderEntryRef:         value.ProviderProvenance.ProviderEntryRef,
			ProviderGroupRef:         value.ProviderProvenance.ProviderGroupRef,
		}
	}
	return result
}

func cloneHTTPBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneHTTPString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
