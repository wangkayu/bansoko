package facts

import (
	"reflect"
	"testing"

	"bansoko/internal/qualityprofile"
)

const compareSourceFacts = `{"identity_kind":"guid","identity_value":"g","anibt":{"schema":"anibt-rss-1.0","tags":["language:CHS","language:CHT","resolution:1080p","format:MKV","subtitle:INTERNAL"]},"title_tags":{"schema":"reltitle-attrs-v1","implementation":"reltitle@x","tags":["resolution:1080p","sub_lang:SC","sub_lang:TC","sub_flag:soft","sub_flag:ex","source_type:WEBRip","video_codec:HEVC"],"value_tags":["title:Show","group:Grp"]}}`

// Each namespace freezes only its own values (multi-valued fields split on
// "+"), release.* from the title parse, and the subgroup source only for a
// provider-group source.
func TestCompareValuesFreezesEachNamespace(t *testing.T) {
	candidate := CandidateFromCoverage("[Grp] Show - 03v2 [1080p][CHS]", testRules(), []string{"tv:s1:e3"})
	if candidate.Revision != "v2" {
		t.Fatalf("revision = %q, want v2", candidate.Revision)
	}
	raw := CompareValues(candidate, CompareSource{SourceFacts: []byte(compareSourceFacts), SourceKind: "provider_group", Provider: "anibt", ProviderGroupRef: "g-1"})
	values := qualityprofile.ParseValues(raw)
	want := map[string][]string{
		qualityprofile.KeyReleaseLang:       {"chs"},
		qualityprofile.KeyReleaseResolution: {"1080p"},
		qualityprofile.KeyReleaseRevision:   {"v2"},
		qualityprofile.KeyAniBTLanguage:     {"CHS", "CHT"},
		qualityprofile.KeyAniBTResolution:   {"1080p"},
		qualityprofile.KeyAniBTFormat:       {"MKV"},
		qualityprofile.KeyAniBTSubtitle:     {"INTERNAL"},
		qualityprofile.KeyRtResolution:      {"1080p"},
		qualityprofile.KeyRtSubLang:         {"SC", "TC"},
		qualityprofile.KeyRtSubFlag:         {"ex", "soft"},
		qualityprofile.KeyRtSource:          {"WEBRip"},
		qualityprofile.KeyRtCodec:           {"HEVC"},
	}
	if !reflect.DeepEqual(values.Values, want) {
		t.Fatalf("values = %#v\nwant %#v", values.Values, want)
	}
	if values.Subgroup == nil || *values.Subgroup != (qualityprofile.SubgroupSource{Provider: "anibt", GroupRef: "g-1"}) {
		t.Fatalf("subgroup = %#v", values.Subgroup)
	}
}

func TestCompareValuesWithoutSourceFacts(t *testing.T) {
	candidate := CandidateFromCoverage("[Grp] Show - 03 [720p]", testRules(), []string{"tv:s1:e3"})
	values := qualityprofile.ParseValues(CompareValues(candidate, CompareSource{SourceKind: "custom_rss"}))
	if values.Subgroup != nil {
		t.Fatalf("custom RSS without a source id has no subgroup source: %#v", values.Subgroup)
	}
	if !reflect.DeepEqual(values.Values, map[string][]string{qualityprofile.KeyReleaseResolution: {"720p"}}) {
		t.Fatalf("values = %#v", values.Values)
	}
	broken := qualityprofile.ParseValues(CompareValues(candidate, CompareSource{SourceFacts: []byte(`{"anibt":"broken"}`), SourceKind: "provider_group"}))
	if broken.Subgroup != nil || len(broken.Values) != 1 {
		t.Fatalf("unreadable facts / incomplete identity = %#v", broken)
	}
}

// A custom RSS source is its own group identity: (custom, rss source id).
func TestCompareValuesCustomSourceIsItsOwnGroup(t *testing.T) {
	candidate := CandidateFromCoverage("[Grp] Show - 03 [720p]", testRules(), []string{"tv:s1:e3"})
	values := qualityprofile.ParseValues(CompareValues(candidate, CompareSource{RSSSourceID: "src-1", SourceKind: "custom_rss"}))
	if values.Subgroup == nil || *values.Subgroup != (qualityprofile.SubgroupSource{Provider: qualityprofile.CustomSubgroupProvider, GroupRef: "src-1"}) {
		t.Fatalf("custom subgroup = %#v", values.Subgroup)
	}
	provider := qualityprofile.ParseValues(CompareValues(candidate, CompareSource{RSSSourceID: "src-2", SourceKind: "provider_group", Provider: "mikan", ProviderGroupRef: "583"}))
	if provider.Subgroup == nil || provider.Subgroup.Key() != "mikan:583" {
		t.Fatalf("provider subgroup keeps (provider, group ref): %#v", provider.Subgroup)
	}
}
