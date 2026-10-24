package naming

import (
	"strings"

	"bansoko/internal/templatefacts"
)

const subtitleModeResolverKey = "release.subtitle_mode"

// subtitleModeValue is the atomic release.subtitle_mode output.
type subtitleModeValue struct {
	SubtitleMode string
}

func (v subtitleModeValue) String() string { return v.SubtitleMode }

// subtitleModeResolver classifies the whole release as embedded, external,
// mixed, or unknown from the release's parsed subtitle flags (rt.sub_flag,
// the flat reltitle sub_flag projection soft / hard / ex): soft or hard
// alone embedded, ex alone external, ex beside either mixed, no flag value
// unknown. A standalone subtitle ArtifactFile proves nothing.
type subtitleModeResolver struct{}

func (subtitleModeResolver) Definition() ResolverDefinition {
	return ResolverDefinition{
		Key:          subtitleModeResolverKey,
		OutputType:   "string",
		Dependencies: nil,
		Nullable:     false,
	}
}

func (subtitleModeResolver) Resolve(input ResolverInput) ResolverResult {
	result := ResolverResult{Key: subtitleModeResolverKey, State: ResolverStateValue}
	flags, _ := input.Facts.Rt.Get("SubFlag")
	values := templatefacts.SplitValues(flags)
	embedded, external := false, false
	for _, value := range values {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "soft", "hard":
			embedded = true
		case "ex":
			external = true
		}
	}
	mode := "unknown"
	switch {
	case embedded && external:
		mode = "mixed"
	case embedded:
		mode = "embedded"
	case external:
		mode = "external"
	}
	if len(values) > 0 {
		result.Evidence = append(result.Evidence, ResolverEvidence{
			ResolverKey: subtitleModeResolverKey,
			RuleID:      "subtitle_mode_from_rt_sub_flag",
			InputPath:   "Rt.SubFlag",
			Captures:    map[string]string{"flags": templatefacts.JoinValues(values)},
		})
	}
	result.Value = subtitleModeValue{SubtitleMode: mode}
	return result
}
