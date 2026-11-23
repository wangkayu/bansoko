package filemaintenance

import (
	"sort"

	"bansoko/internal/qualityprofile"
)

// FrozenReplacementComparison preserves the actual evaluated input for a new
// decision. Old pools without this field cannot be promoted to typed approval.
type FrozenReplacementComparison struct {
	Profile         Profile      `json:"profile"`
	ExistingMissing bool         `json:"existing_missing"`
	Placement       PlacementRef `json:"placement"`
	Candidate       Facts        `json:"candidate"`
	Existing        Facts        `json:"existing"`
	Dimensions      []string     `json:"dimensions"`
	Verdict         string       `json:"verdict"`
}

// approvalDimension maps a comparison item to the category the typed
// approval evidence (ComparisonV1.compared_dimensions) names. Items without a
// category (anibt.subtitle, anibt.format, rt.codec) are not named there; the
// decision's own steps keep them.
var approvalDimension = map[string]string{
	qualityprofile.KeySubgroupScore:   "group",
	qualityprofile.KeyAniBTLanguage:   "languages",
	qualityprofile.KeyRtSubLang:       "languages",
	qualityprofile.KeyAniBTResolution: "resolution",
	qualityprofile.KeyRtResolution:    "resolution",
	qualityprofile.KeyRtRevision:      "revision",
	qualityprofile.KeyRtSource:        "source",
	qualityprofile.KeyFileSize:        "size",
}

// comparedDimensions is the sorted category set of configured comparison items.
func comparedDimensions(profile Profile) []string {
	dimensions := []string{}
	for _, item := range profile.Items {
		if category, ok := approvalDimension[item.Key]; ok {
			dimensions = append(dimensions, category)
		}
	}
	sort.Strings(dimensions)
	return compactDimensions(dimensions)
}

// freezeReplacementComparisons freezes, for every loser of a replace pool,
// the evaluated input the typed approval later reads. profile is the
// mode-adjusted profile the pools were evaluated with.
func freezeReplacementComparisons(pools []PoolEvidence, candidate Facts, placements []PlacementFacts, profile Profile) {
	dimensions := comparedDimensions(profile)
	byID := map[string]PlacementFacts{}
	for _, placement := range placements {
		byID[placement.PlacementID] = placement
	}
	for index := range pools {
		pool := &pools[index]
		if pool.Verdict != "replace" {
			continue
		}
		for _, loser := range pool.Losers {
			existing, ok := byID[loser.PlacementID]
			if !ok {
				continue
			}
			// A loser without release facts (a scan-managed file no release
			// claimed) is frozen with no values: any candidate beats it.
			if existing.Version != loser.Version || existing.Fingerprint != loser.Fingerprint || !compareWithMember(candidate, existing, profile).Better {
				continue
			}
			pool.Comparisons = append(pool.Comparisons, FrozenReplacementComparison{Placement: loser, Candidate: candidate, Existing: comparableFacts(existing), Dimensions: dimensions, Verdict: "replace", Profile: profile, ExistingMissing: !existing.FactsOK})
		}
	}
}

func compactDimensions(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}
