package application

import (
	selectiondomain "bansoko/internal/selection/domain"
)

// SubgroupScoreBreakdown is how the group-selection decision scored one group:
// the live site default and list effect, every matched name keyword and the
// per-anime manual override. It is copied from the decision, never computed
// here.
type SubgroupScoreBreakdown struct {
	SiteMode         string
	SiteDefaultScore int
	ListEffect       string
	Keywords         []NameKeywordRule
	EntryOverride    string
	PinnedScore      *int
	FinalScore       *int
}

// SubgroupScoreBreakdownFromDomain copies one decision's breakdown; nil stays
// nil.
func SubgroupScoreBreakdownFromDomain(value *selectiondomain.ScoreBreakdown) *SubgroupScoreBreakdown {
	if value == nil {
		return nil
	}
	result := &SubgroupScoreBreakdown{
		SiteMode: string(value.SiteMode), SiteDefaultScore: value.SiteDefaultScore,
		ListEffect: string(value.ListEffect), EntryOverride: string(value.EntryOverride),
		Keywords: make([]NameKeywordRule, len(value.Keywords)),
	}
	for i, keyword := range value.Keywords {
		result.Keywords[i] = NameKeywordRule{Keyword: keyword.Keyword, Score: keyword.Score}
	}
	if value.FinalScore != nil {
		score := *value.FinalScore
		result.FinalScore = &score
	}
	result.PinnedScore = cloneIntValue(value.PinnedScore)
	return result
}

func cloneSubgroupScoreBreakdown(value *SubgroupScoreBreakdown) *SubgroupScoreBreakdown {
	if value == nil {
		return nil
	}
	result := *value
	result.Keywords = append([]NameKeywordRule{}, value.Keywords...)
	if value.FinalScore != nil {
		score := *value.FinalScore
		result.FinalScore = &score
	}
	result.PinnedScore = cloneIntValue(value.PinnedScore)
	return &result
}
