package facts

import (
	"encoding/json"
	"strings"

	"bansoko/internal/qualityprofile"
	"bansoko/internal/templatefacts"
)

// CompareSource is the frozen release input of the replacement comparison
// values beyond the title: the Release source_facts document and the RSS
// source it arrived through (its id, kind and provider group identity).
type CompareSource struct {
	SourceFacts      []byte
	RSSSourceID      string
	SourceKind       string
	Provider         string
	ProviderGroupRef string
}

// RSS source kinds: a provider group carries (provider, group ref); a custom
// RSS source is its own group identity.
const (
	sourceKindProviderGroup = "provider_group"
	sourceKindCustomRSS     = "custom_rss"
)

// CompareValues freezes the replacement comparison values one release
// contributes: release.* from the title parse (the candidate), anibt.* and
// rt.* from the frozen source facts (each namespace its own values, no
// cross-namespace fallback; multi-valued fields split on "+"), and the
// subgroup source: (provider, group ref) of a provider-group RSS source, or
// (custom, rss source id) of a custom RSS source. The subgroup source is the
// stable group identity per_group pools by and subgroup.score looks up. An
// unreadable source facts document contributes no anibt/rt values.
func CompareValues(candidate Candidate, source CompareSource) json.RawMessage {
	values := qualityprofile.NewValues()
	values.Set(qualityprofile.KeyReleaseLang, candidate.Language)
	values.Set(qualityprofile.KeyReleaseResolution, candidate.Resolution)
	values.Set(qualityprofile.KeyReleaseRevision, candidate.Revision)
	if len(source.SourceFacts) > 0 {
		if projected, err := templatefacts.FromSourceFacts(source.SourceFacts); err == nil {
			for key, field := range map[string]string{
				qualityprofile.KeyAniBTResolution: "Resolution",
				qualityprofile.KeyAniBTLanguage:   "Language",
				qualityprofile.KeyAniBTSubtitle:   "Subtitle",
				qualityprofile.KeyAniBTFormat:     "Format",
			} {
				if value, ok := projected.AniBTTags.Get(field); ok {
					values.SetJoined(key, value)
				}
			}
			for key, field := range map[string]string{
				qualityprofile.KeyRtResolution:    "Resolution",
				qualityprofile.KeyRtSubLang:       "SubLang",
				qualityprofile.KeyRtSubFlag:       "SubFlag",
				qualityprofile.KeyRtSource:        "Source",
				qualityprofile.KeyRtCodec:         "Codec",
				qualityprofile.KeyRtContentRating: "ContentRating",
			} {
				if value, ok := projected.Reltitle.Get(field); ok {
					values.SetJoined(key, value)
				}
			}
		}
	}
	if source.SourceKind == sourceKindProviderGroup && strings.TrimSpace(source.Provider) != "" && strings.TrimSpace(source.ProviderGroupRef) != "" {
		values.Subgroup = &qualityprofile.SubgroupSource{Provider: strings.TrimSpace(source.Provider), GroupRef: strings.TrimSpace(source.ProviderGroupRef)}
	}
	if source.SourceKind == sourceKindCustomRSS && strings.TrimSpace(source.RSSSourceID) != "" {
		values.Subgroup = &qualityprofile.SubgroupSource{Provider: qualityprofile.CustomSubgroupProvider, GroupRef: strings.TrimSpace(source.RSSSourceID)}
	}
	return values.JSON()
}
