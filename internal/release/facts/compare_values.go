package facts

import (
	"encoding/json"
	"strings"

	"bansoko/internal/qualityprofile"
	"bansoko/internal/templatefacts"
)

// CompareSource is the frozen release input of the replacement comparison
// values beyond the title: the Release source_facts document and the RSS
// source it arrived through.
type CompareSource struct {
	SourceFacts      []byte
	SourceKind       string
	Provider         string
	ProviderGroupRef string
}

// sourceKindProviderGroup is the RSS source kind that carries a provider
// group identity.
const sourceKindProviderGroup = "provider_group"

// CompareValues freezes the replacement comparison values one release
// contributes: release.* from the title parse (the candidate), anibt.* and
// rt.* from the frozen source facts (each namespace its own values, no
// cross-namespace fallback; multi-valued fields split on "+"), and the
// subgroup source of a provider-group RSS source. An unreadable source facts
// document contributes no anibt/rt values.
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
				qualityprofile.KeyRtResolution: "Resolution",
				qualityprofile.KeyRtSubLang:    "SubLang",
				qualityprofile.KeyRtSource:     "Source",
				qualityprofile.KeyRtCodec:      "Codec",
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
	return values.JSON()
}
