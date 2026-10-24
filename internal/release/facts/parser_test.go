package facts

import (
	"encoding/json"
	"testing"

	"bansoko/internal/attrrules"
)

func testRules() attrrules.Rules {
	return attrrules.Rules{Version: 1, Resolution: []string{"2160p", "1440p", "1080p", "720p", "480p"}, Quality: []string{"remux", "web-dl", "webrip", "bluray", "bdrip", "hdtv"}, Language: []string{"chs", "cht", "sc", "tc", "jpn", "eng"}, SpecialMarkers: []string{"special", "ncop", "nced"}, GroupBrackets: []string{"[", "]"}}
}

// The lazy profile refresh must project the frozen plan coverage, not re-parse
// the stored title. A title that would parse to another episode cannot move the
// coverage.
func TestRecomputeCurrentProfileProjectsFrozenCoverage(t *testing.T) {
	existing := ReleaseCandidateFacts{
		ReleaseID:        "release-1",
		ReleaseVersion:   4,
		ParserVersion:    "release-facts-v1@sha256:old",
		InputFingerprint: InputFingerprint("[Group] Show - 01 [720p]", nil, false),
		RawEvidence:      []byte(`{"title":"[Group] Show - 99 [1080p]"}`),
		Facts:            []byte(`{"resolution":"720p"}`),
		CoverageKeys:     []string{"tv:s1:e1"},
	}
	coverage := []string{"tv:s1:e12"}
	updated, changed, err := RecomputeCurrentProfile(existing, testRules(), &coverage, CompareSource{})
	if err != nil {
		t.Fatal(err)
	}
	if !changed || updated.ReleaseVersion != existing.ReleaseVersion {
		t.Fatalf("recompute must be pure and report changed: changed=%v version=%d", changed, updated.ReleaseVersion)
	}
	if updated.Resolution != "1080p" || updated.ParserVersion == existing.ParserVersion {
		t.Fatalf("current attr profile was not applied: %+v", updated)
	}
	if len(updated.CoverageKeys) != 1 || updated.CoverageKeys[0] != "tv:s1:e12" {
		t.Fatalf("coverage = %v, want the frozen plan key tv:s1:e12", updated.CoverageKeys)
	}
	if updated.InputFingerprint != InputFingerprint("[Group] Show - 99 [1080p]", nil, false) {
		t.Fatal("input fingerprint must remain the title/input identity")
	}
	if string(existing.Facts) != `{"resolution":"720p"}` {
		t.Fatal("recompute must not mutate the old facts row")
	}
}

// An empty frozen plan coverage stays unresolved; the title is never allowed
// to rescue it into a guessed scalar episode.
func TestRecomputeCurrentProfileKeepsEmptyCoverageUnresolved(t *testing.T) {
	existing := ReleaseCandidateFacts{
		ReleaseID: "release-1", ReleaseVersion: 1,
		RawEvidence:  []byte(`{"title":"[Group] Show - 09 [1080p]"}`),
		CoverageKeys: []string{CoverageUnresolved},
	}
	coverage := []string{}
	updated, changed, err := RecomputeCurrentProfile(existing, testRules(), &coverage, CompareSource{})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("recompute must report the profile refresh")
	}
	if len(updated.CoverageKeys) != 1 || updated.CoverageKeys[0] != CoverageUnresolved {
		t.Fatalf("coverage = %v, want [%s]", updated.CoverageKeys, CoverageUnresolved)
	}
}

// No frozen plan means the lazy writer is a no-op: it must never
// synthesize coverage from the title.
func TestRecomputeCurrentProfileNoOpWithoutFrozenPlan(t *testing.T) {
	existing := ReleaseCandidateFacts{
		ReleaseID: "release-1", ReleaseVersion: 3,
		ParserVersion: "release-facts-v1@sha256:old",
		RawEvidence:   []byte(`{"title":"[Group] Show - 01 [1080p]"}`),
		CoverageKeys:  []string{"tv:s1:e1"},
	}
	updated, changed, err := RecomputeCurrentProfile(existing, testRules(), nil, CompareSource{})
	if err != nil || changed {
		t.Fatalf("missing frozen plan must be a no-op: changed=%v err=%v", changed, err)
	}
	if updated.ParserVersion != existing.ParserVersion || updated.Facts != nil && string(updated.Facts) != string(existing.Facts) {
		t.Fatalf("no-op must not change the row: %+v", updated)
	}
}

func TestRecomputeCurrentProfileNoOpWithoutTitle(t *testing.T) {
	coverage := []string{"tv:s1:e12"}
	for _, raw := range []json.RawMessage{nil, []byte(`{}`), []byte(`{"title":"  "}`), []byte(`not-json`)} {
		updated, changed, err := RecomputeCurrentProfile(ReleaseCandidateFacts{RawEvidence: raw}, testRules(), &coverage, CompareSource{})
		if err != nil || changed || updated.ParserVersion != "" {
			t.Fatalf("missing title must be a no-op: raw=%s changed=%v err=%v", raw, changed, err)
		}
	}
}

func TestInputFingerprintIsDeterministic(t *testing.T) {
	first := InputFingerprint("Show - 01", nil, false)
	second := InputFingerprint("Show - 01", nil, false)
	third := InputFingerprint("Show - 01", nil, true)
	if first != second {
		t.Fatal("fingerprint must be deterministic")
	}
	if first == third {
		t.Fatal("movie flag must change the fingerprint")
	}
}
