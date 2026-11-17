package facts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/qualityprofile"
	"bansoko/internal/templatefacts"
)

// CompareSource names the frozen release tags and its stable source identity.
// No title, file-name parse, measured media or size enters the comparison.
type CompareSource struct {
	SourceFacts      []byte
	RSSSourceID      string
	SourceKind       string
	Provider         string
	ProviderGroupRef string
}

const (
	sourceKindProviderGroup = "provider_group"
	sourceKindCustomRSS     = "custom_rss"
)

// CompareValues is a pure versioned projection of captured release evidence.
// Missing tags contribute no value; corrupt evidence is not silently discarded.
func CompareValues(source CompareSource) (json.RawMessage, error) {
	title, anibt, err := acquisitiondomain.DecodeSourceFactTagSets(source.SourceFacts)
	if err != nil {
		return nil, err
	}
	values := qualityprofile.NewValues()
	if source.SourceKind == sourceKindProviderGroup && strings.TrimSpace(source.Provider) != "" && strings.TrimSpace(source.ProviderGroupRef) != "" {
		values.Subgroup = &qualityprofile.SubgroupSource{Provider: strings.TrimSpace(source.Provider), GroupRef: strings.TrimSpace(source.ProviderGroupRef)}
	}
	if source.SourceKind == sourceKindCustomRSS && strings.TrimSpace(source.RSSSourceID) != "" {
		values.Subgroup = &qualityprofile.SubgroupSource{Provider: qualityprofile.CustomSubgroupProvider, GroupRef: strings.TrimSpace(source.RSSSourceID)}
	}
	evidence, err := json.Marshal(struct {
		Title    *acquisitiondomain.TitleTagSet `json:"title_tags"`
		AniBT    *acquisitiondomain.AniBTTagSet `json:"anibt"`
		Subgroup *qualityprofile.SubgroupSource `json:"subgroup"`
	}{title, anibt, values.Subgroup})
	if err != nil {
		return nil, fmt.Errorf("encode comparison source: %w", err)
	}
	digest := sha256.Sum256(evidence)
	values.SourceDigest = hex.EncodeToString(digest[:])
	projected := templatefacts.FromTagSets(title, anibt)
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
		qualityprofile.KeyRtRevision:      "Version",
	} {
		if value, ok := projected.Reltitle.Get(field); ok {
			values.SetJoined(key, value)
		}
	}
	return values.JSON(), nil
}
