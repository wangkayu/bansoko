package domain

import (
	"bansoko/internal/postprocessing/naming"
)

// stringPtrValue dereferences an optional transport string; nil becomes "".
func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// stringPtrWhen returns a pointer only for a non-empty value, so the optional
// transport field stays omitted for the engine default.
func stringPtrWhen(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// NamingRulesToDomain maps the transport naming rules to the naming engine
// shape. The transport and engine shapes stay independent.
func NamingRulesToDomain(value NamingRules) naming.NamingRules {
	return naming.NamingRules{
		TV: naming.NamingVariant{
			DirectoryTemplate:     value.Tv.DirectoryTemplate,
			VideoFilenameTemplate: value.Tv.VideoFilenameTemplate,
		},
		Movie: naming.NamingVariant{
			DirectoryTemplate:     value.Movie.DirectoryTemplate,
			VideoFilenameTemplate: value.Movie.VideoFilenameTemplate,
		},
		Unknown:                naming.NamingVariant{DirectoryTemplate: value.Unknown.DirectoryTemplate, VideoFilenameTemplate: value.Unknown.VideoFilenameTemplate},
		SubtitleSuffixTemplate: value.SubtitleSuffixTemplate,
		Preset:                 stringPtrValue(value.Preset),
		Language:               string(value.Language),
	}
}

// NamingRulesFromDomain maps engine naming rules back to the transport shape.
func NamingRulesFromDomain(value naming.NamingRules) NamingRules {
	return NamingRules{
		Tv: NamingVariant{
			DirectoryTemplate:     value.TV.DirectoryTemplate,
			VideoFilenameTemplate: value.TV.VideoFilenameTemplate,
		},
		Movie: NamingVariant{
			DirectoryTemplate:     value.Movie.DirectoryTemplate,
			VideoFilenameTemplate: value.Movie.VideoFilenameTemplate,
		},
		Unknown:                NamingVariant{DirectoryTemplate: value.Unknown.DirectoryTemplate, VideoFilenameTemplate: value.Unknown.VideoFilenameTemplate},
		SubtitleSuffixTemplate: value.SubtitleSuffixTemplate,
		Preset:                 stringPtrWhen(value.Preset),
		Language:               namingLanguageOrCurrent(value.Language),
	}
}

// namingLanguageOrCurrent keeps the language a rules value was written in; an
// unset language is the current one.
func namingLanguageOrCurrent(language string) NamingRulesLanguage {
	if language == "" {
		return NamingRulesLanguage(naming.Language)
	}
	return NamingRulesLanguage(language)
}
