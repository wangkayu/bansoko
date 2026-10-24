package transport

import (
	"encoding/json"
	"fmt"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/transport/httpapi/generated"
)

func decode[T any](value any) (T, error) {
	var result T
	raw, err := json.Marshal(value)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, err
	}
	return result, nil
}

func Draft(value generated.AnimeDraft) (animedomain.AnimeDraft, error) {
	return decode[animedomain.AnimeDraft](value)
}

// presentSection copies one rules section; an absent section (a draft that has
// not been loaded yet) stays absent rather than becoming a JSON null.
func presentSection[T interface{ MarshalJSON() ([]byte, error) }](name string, value *T) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := (*value).MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("marshal %s rules: %w", name, err)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	return append(json.RawMessage(nil), raw...), nil
}

// RulesSections maps wire rules sections; every section is optional on the
// wire (a draft before load_business_preset) and stays absent when left out.
func RulesSections(value generated.RulesSections) (animedomain.RulesSections, error) {
	var result animedomain.RulesSections
	var err error
	if result.Parser, err = presentSection("parser", value.Parser); err != nil {
		return animedomain.RulesSections{}, err
	}
	if result.ReleaseFilter, err = presentSection("release filter", value.ReleaseFilter); err != nil {
		return animedomain.RulesSections{}, err
	}
	if result.FileFilter, err = presentSection("file filter", value.FileFilter); err != nil {
		return animedomain.RulesSections{}, err
	}
	if result.Naming, err = presentSection("naming", value.Naming); err != nil {
		return animedomain.RulesSections{}, err
	}
	if result.FileEffect, err = presentSection("file effect", value.FileEffect); err != nil {
		return animedomain.RulesSections{}, err
	}
	if result.TagFilters, err = presentSection("tag filter", value.TagFilters); err != nil {
		return animedomain.RulesSections{}, err
	}
	return result, nil
}

// TagFilterRulesToHTTP maps the effective tag_filters section.
func TagFilterRulesToHTTP(value animedomain.TagFilterRules) generated.TagFilterRules {
	row := func(value animedomain.TagFilterRow) generated.TagFilterRow {
		return generated.TagFilterRow{Require: append([]string{}, value.Require...), Exclude: append([]string{}, value.Exclude...)}
	}
	return generated.TagFilterRules{Anibt: row(value.Anibt), Title: row(value.Title)}
}

// sectionToHTTP maps one stored rules section; an absent section stays nil.
func sectionToHTTP[T any, P interface {
	*T
	UnmarshalJSON([]byte) error
}](name string, raw json.RawMessage) (*T, error) {
	if animedomain.SectionAbsent(raw) {
		return nil, nil
	}
	var value T
	if err := P(&value).UnmarshalJSON(raw); err != nil {
		return nil, fmt.Errorf("unmarshal %s rules: %w", name, err)
	}
	return &value, nil
}

func RulesSectionsToHTTP(value animedomain.RulesSections) (generated.RulesSections, error) {
	var result generated.RulesSections
	var err error
	if result.Parser, err = sectionToHTTP[generated.ParserRulesOverride]("parser", value.Parser); err != nil {
		return generated.RulesSections{}, err
	}
	if result.ReleaseFilter, err = sectionToHTTP[generated.ReleaseFilterRulesOverride]("release filter", value.ReleaseFilter); err != nil {
		return generated.RulesSections{}, err
	}
	if result.FileFilter, err = sectionToHTTP[generated.FileFilterRulesOverride]("file filter", value.FileFilter); err != nil {
		return generated.RulesSections{}, err
	}
	if result.Naming, err = sectionToHTTP[generated.NamingRulesOverride]("naming", value.Naming); err != nil {
		return generated.RulesSections{}, err
	}
	if result.FileEffect, err = sectionToHTTP[generated.FileEffectRulesOverride]("file effect", value.FileEffect); err != nil {
		return generated.RulesSections{}, err
	}
	tagFilters := value.TagFilters
	if !animedomain.SectionAbsent(tagFilters) {
		tagFilters = animedomain.NormalizeTagFiltersOverrideJSON(tagFilters)
	}
	if result.TagFilters, err = sectionToHTTP[generated.TagFilterRulesOverride]("tag filter", tagFilters); err != nil {
		return generated.RulesSections{}, err
	}
	return result, nil
}

func BGM(value *generated.NullableBGMBinding) (*animedomain.NullableBGMBinding, error) {
	if value == nil {
		return nil, nil
	}
	result, err := decode[animedomain.NullableBGMBinding](value)
	return &result, err
}

func TMDB(value *generated.NullableTMDBBinding) (*animedomain.NullableTMDBBinding, error) {
	if value == nil {
		return nil, nil
	}
	result, err := decode[animedomain.NullableTMDBBinding](value)
	return &result, err
}

func MetadataHold(value animedomain.MetadataHold) (generated.MetadataHold, error) {
	return decode[generated.MetadataHold](value)
}

func MetadataHoldFromHTTP(value generated.MetadataHold) (animedomain.MetadataHold, error) {
	return decode[animedomain.MetadataHold](value)
}

func EffectiveRules(value animedomain.EffectiveRules) (generated.EffectiveRules, error) {
	return generated.EffectiveRules{
		FileEffect: generated.FileEffectRules{
			Operation:       generated.FileEffectRulesOperation(value.FileEffect.Operation),
			CollisionPolicy: generated.CollisionPolicy(value.FileEffect.CollisionPolicy),
		},
		FileFilter: generated.FileFilterRules{
			MustContain: value.FileFilter.MustContain,
			NotIn:       value.FileFilter.NotIn,
		},
		Naming: generated.NamingRules{
			Language:               generated.NamingRulesLanguage(value.Naming.Language),
			Movie:                  generated.NamingVariant{DirectoryTemplate: value.Naming.Movie.DirectoryTemplate, VideoFilenameTemplate: value.Naming.Movie.VideoFilenameTemplate},
			Preset:                 cloneString(value.Naming.Preset),
			SubtitleSuffixTemplate: value.Naming.SubtitleSuffixTemplate,
			Tv:                     generated.NamingVariant{DirectoryTemplate: value.Naming.Tv.DirectoryTemplate, VideoFilenameTemplate: value.Naming.Tv.VideoFilenameTemplate},
		},
		Parser: generated.ParserRules{
			EpisodePatterns: cloneStrings(value.Parser.EpisodePatterns),
		},
		ReleaseFilter: generated.ReleaseFilterRules{
			MustContain: value.ReleaseFilter.MustContain,
			NotIn:       value.ReleaseFilter.NotIn,
		},
	}, nil
}

func NamingRules(value generated.NamingRules) (animedomain.NamingRules, error) {
	return decode[animedomain.NamingRules](value)
}
func ParserRules(value generated.ParserRules) (animedomain.ParserRules, error) {
	return decode[animedomain.ParserRules](value)
}
func SubgroupsSection(value generated.SubgroupsSection) (animedomain.SubgroupsSection, error) {
	return decode[animedomain.SubgroupsSection](value)
}
func cloneStrings(value []string) []string {
	if value == nil {
		return nil
	}
	cloned := make([]string, len(value))
	copy(cloned, value)
	return cloned
}

func cloneStringSlice(value *[]string) *[]string {
	if value == nil {
		return nil
	}
	result := cloneStrings(*value)
	return &result
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
