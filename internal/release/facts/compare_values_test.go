package facts

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"bansoko/internal/qualityprofile"
)

const compareSourceFacts = `{"identity_kind":"guid","identity_value":"g","anibt":{"schema":"anibt-rss-1.0","tags":["language:CHS","language:CHT","resolution:1080p","format:MKV","subtitle:INTERNAL"]},"title_tags":{"schema":"reltitle-attrs-v1","implementation":"reltitle@x","tags":["resolution:1080p","sub_lang:SC","sub_lang:TC","sub_flag:soft","sub_flag:ex","source_type:WEBRip","video_codec:HEVC","content_rating:UNCENSORED","version:v2"]}}`

func projectedValues(t *testing.T, source CompareSource) qualityprofile.FrozenValues {
	t.Helper()
	raw, err := CompareValues(source)
	if err != nil {
		t.Fatal(err)
	}
	values, err := qualityprofile.ParseValues(raw)
	if err != nil {
		t.Fatal(err)
	}
	return values
}

func TestCompareValuesFreezesEachNamespace(t *testing.T) {
	values := projectedValues(t, CompareSource{SourceFacts: []byte(compareSourceFacts), SourceKind: "provider_group", Provider: "anibt", ProviderGroupRef: "g-1"})
	want := map[string][]string{
		qualityprofile.KeyAniBTLanguage:   {"CHS", "CHT"},
		qualityprofile.KeyAniBTResolution: {"1080p"},
		qualityprofile.KeyAniBTFormat:     {"MKV"},
		qualityprofile.KeyAniBTSubtitle:   {"INTERNAL"},
		qualityprofile.KeyRtResolution:    {"1080p"},
		qualityprofile.KeyRtSubLang:       {"SC", "TC"},
		qualityprofile.KeyRtSubFlag:       {"ex", "soft"},
		qualityprofile.KeyRtSource:        {"WEBRip"},
		qualityprofile.KeyRtCodec:         {"HEVC"},
		qualityprofile.KeyRtContentRating: {"UNCENSORED"},
		qualityprofile.KeyRtRevision:      {"v2"},
	}
	if !reflect.DeepEqual(values.Values, want) {
		t.Fatalf("values = %#v; want %#v", values.Values, want)
	}
	if values.Subgroup == nil || values.Subgroup.Key() != "anibt:g-1" || len(values.SourceDigest) != 64 {
		t.Fatalf("source identity = %#v", values)
	}
}

func TestCompareValuesWithoutSourceFacts(t *testing.T) {
	values := projectedValues(t, CompareSource{})
	if len(values.Values) != 0 || values.Subgroup != nil {
		t.Fatalf("missing source invented values: %#v", values)
	}
	if _, err := CompareValues(CompareSource{SourceFacts: []byte(`{"anibt":"broken"}`)}); err == nil {
		t.Fatal("corrupt source must be rejected")
	}
}

func TestCompareValuesCustomSourceIsItsOwnGroup(t *testing.T) {
	custom := projectedValues(t, CompareSource{RSSSourceID: "src-1", SourceKind: "custom_rss"})
	if custom.Subgroup == nil || custom.Subgroup.Key() != "custom:src-1" {
		t.Fatalf("custom group = %#v", custom)
	}
	provider := projectedValues(t, CompareSource{RSSSourceID: "src-2", SourceKind: "provider_group", Provider: "mikan", ProviderGroupRef: "583"})
	if provider.Subgroup == nil || provider.Subgroup.Key() != "mikan:583" {
		t.Fatalf("provider group = %#v", provider)
	}
}

func TestComparisonSourceDigestTracksEvidenceAndGroup(t *testing.T) {
	source := CompareSource{SourceFacts: []byte(compareSourceFacts), SourceKind: "provider_group", Provider: "anibt", ProviderGroupRef: "g-1"}
	first := projectedValues(t, source)
	for _, modified := range []CompareSource{
		{SourceFacts: []byte(strings.ReplaceAll(compareSourceFacts, "1080p", "720p")), SourceKind: source.SourceKind, Provider: source.Provider, ProviderGroupRef: source.ProviderGroupRef},
		{SourceFacts: source.SourceFacts, SourceKind: source.SourceKind, Provider: source.Provider, ProviderGroupRef: "g-2"},
		{SourceFacts: []byte(strings.ReplaceAll(compareSourceFacts, "reltitle@x", "reltitle@y")), SourceKind: source.SourceKind, Provider: source.Provider, ProviderGroupRef: source.ProviderGroupRef},
	} {
		if got := projectedValues(t, modified); got.SourceDigest == first.SourceDigest {
			t.Fatal("changed source reused digest")
		}
	}
	source.SourceFacts = []byte(strings.ReplaceAll(compareSourceFacts, "guid", "other-id-kind"))
	if got := projectedValues(t, source); got.SourceDigest != first.SourceDigest {
		t.Fatal("unused envelope metadata changed comparison digest")
	}
}

func TestRecomputeCacheRefreshesOnlyChangedEvidence(t *testing.T) {
	coverage := []string{"tv:e1"}
	source := CompareSource{SourceFacts: []byte(compareSourceFacts)}
	existing := ReleaseCandidateFacts{RawEvidence: []byte(`{"title":"Show - 99 [720p]"}`)}
	first, changed, err := RecomputeCurrentProfile(existing, &coverage, source)
	if err != nil || !changed {
		t.Fatalf("first projection = %v,%v", changed, err)
	}
	_, changed, err = RecomputeCurrentProfile(first, &coverage, source)
	if err != nil || changed {
		t.Fatalf("identical projection = %v,%v", changed, err)
	}
	var jsonb map[string]any
	if err := json.Unmarshal(first.CompareValues, &jsonb); err != nil {
		t.Fatal(err)
	}
	formatted, err := json.MarshalIndent(jsonb, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	fromDatabase := first
	fromDatabase.CompareValues = formatted
	if _, changed, err := RecomputeCurrentProfile(fromDatabase, &coverage, source); err != nil || changed {
		t.Fatalf("JSONB presentation changed evidence: changed=%v err=%v", changed, err)
	}
	source.SourceFacts = []byte(strings.ReplaceAll(compareSourceFacts, "1080p", "2160p"))
	second, changed, err := RecomputeCurrentProfile(first, &coverage, source)
	if err != nil || !changed || second.InputFingerprint == first.InputFingerprint {
		t.Fatalf("source invalidation = %v,%v", changed, err)
	}
	if !reflect.DeepEqual(second.CoverageKeys, coverage) {
		t.Fatalf("source refresh moved identity: %v", second.CoverageKeys)
	}
}
