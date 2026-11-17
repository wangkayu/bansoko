package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/qualityprofile"
	"strings"
	"testing"
)

func TestReplacementNativeEvidencePreservesComparedValues(t *testing.T) {
	profile := qualityprofile.Profile{Version: qualityprofile.Version, Items: []qualityprofile.Item{{Key: qualityprofile.KeyRtSubLang, Ranks: [][]string{{"SC", "TC"}, {"SC"}}}}}
	candidate := replacementFactsSnapshot{SourceDigest: strings.Repeat("a", 64), Values: map[string][]string{qualityprofile.KeyRtSubLang: {"SC", "TC"}}, Subgroup: &qualityprofile.SubgroupSource{Provider: "mikan", GroupRef: "g1"}}
	existing := replacementFactsSnapshot{SourceDigest: strings.Repeat("b", 64), Values: map[string][]string{qualityprofile.KeyRtSubLang: {"SC"}}}
	comparison := replacementComparisonSnapshot{Profile: profile, Candidate: candidate, Existing: existing}
	value, err := comparison.nativeEvidence()
	if err != nil {
		t.Fatal(err)
	}
	if got := value.Object["candidate"].Object["values"].Object["values"].Object[qualityprofile.KeyRtSubLang].Array; len(got) != 2 {
		t.Fatalf("multi-value evidence lost: %s", protocol.Canonical(value))
	}
	comparison.Existing = candidate
	if _, err := comparison.nativeEvidence(); err == nil {
		t.Fatal("a tie authorized replacement")
	}
	comparison.ExistingMissing = true
	if _, err := comparison.nativeEvidence(); err != nil {
		t.Fatalf("scan-managed incumbent cannot be replaced: %v", err)
	}
	comparison.Candidate.SourceDigest = ""
	if _, err := comparison.nativeEvidence(); err == nil {
		t.Fatal("untraceable candidate cache authorized replacement")
	}
}

func TestReplacementNativeEvidencePreservesManualGroupScore(t *testing.T) {
	a, b := int64(0), int64(-1)
	comparison := replacementComparisonSnapshot{
		Profile:   qualityprofile.Profile{Version: qualityprofile.Version, Items: []qualityprofile.Item{{Key: qualityprofile.KeySubgroupScore, Ranks: [][]string{}}}},
		Candidate: replacementFactsSnapshot{SourceDigest: strings.Repeat("a", 64), Values: map[string][]string{}, Subgroup: &qualityprofile.SubgroupSource{Provider: "mikan", GroupRef: "a"}, Score: &a},
		Existing:  replacementFactsSnapshot{SourceDigest: strings.Repeat("b", 64), Values: map[string][]string{}, Subgroup: &qualityprofile.SubgroupSource{Provider: "mikan", GroupRef: "b"}, Score: &b},
	}
	if _, err := comparison.nativeEvidence(); err != nil {
		t.Fatal(err)
	}
}
