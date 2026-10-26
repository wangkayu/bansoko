package templatefacts

import (
	"reflect"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
)

func TestReltitleValuesProjectTitleTagSet(t *testing.T) {
	set := &acquisitiondomain.TitleTagSet{
		Schema:    acquisitiondomain.TitleTagSchema,
		Tags:      []string{"resolution:1080p", "resolution:720p", "source_type:WEBRip", "video_codec:HEVC", "audio_codec:AAC", "container:MKV", "sub_lang:TC", "sub_lang:SC", "sub_flag:hard"},
		ValueTags: []string{"title:进击的巨人", "group:示例字幕组", "version:v2", "content_rating:UNCENSORED"},
	}
	got := ReltitleValues(set)
	want := Values{"Title": "进击的巨人", "Subgroup": "示例字幕组", "Version": "v2", "Resolution": "1080p+720p", "Source": "WEBRip", "Codec": "HEVC", "SubLang": "SC+TC", "SubFlag": "hard", "ContentRating": "UNCENSORED"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReltitleValues = %#v, want %#v", got, want)
	}
	// Whitelisted but default-off outputs never become fields.
	for _, key := range []string{"Container", "AudioCodec"} {
		if _, ok := got.Get(key); ok {
			t.Fatalf("unexposed reltitle output %s projected", key)
		}
	}
}

func TestReltitleUnknownSchemaHasNoValueTags(t *testing.T) {
	// A set of an unknown schema is not trusted for ValueTags: even a stray value tag is not read.
	set := &acquisitiondomain.TitleTagSet{Schema: "reltitle-attrs-v0", Tags: []string{"resolution:1080p"}, ValueTags: []string{"title:Leaked", "version:v2"}}
	got := ReltitleValues(set)
	if !reflect.DeepEqual(got, Values{"Resolution": "1080p"}) {
		t.Fatalf("unknown-schema projection = %#v", got)
	}
	if got := ReltitleValues(nil); len(got) != 0 {
		t.Fatalf("release without title_tags projected %#v", got)
	}
}

func TestAniBTTagValuesKeepAniBTSpellingAndJoinDeterministically(t *testing.T) {
	first := AniBTTagValues(&acquisitiondomain.AniBTTagSet{Schema: acquisitiondomain.AniBTTagSchemaV1, Tags: []string{"resolution:1080p", "language:CHT", "language:CHS", "subtitle:INTERNAL", "format:MKV", "tag:WEBRip", "tag:HEVC", "tag:10bit", "tag:AAC"}})
	second := AniBTTagValues(&acquisitiondomain.AniBTTagSet{Tags: []string{"audio:AAC", "bitdepth:10bit", "codec:HEVC", "source:WEBRip", "format:MKV", "subtitle:INTERNAL", "language:CHS", "language:CHT", "resolution:1080p"}})
	want := Values{"Resolution": "1080p", "Subtitle": "INTERNAL", "Language": "CHS+CHT", "Format": "MKV", "Tag": "10bit+AAC+HEVC+WEBRip"}
	if !reflect.DeepEqual(first, want) || !reflect.DeepEqual(second, want) {
		t.Fatalf("AniBT values = %#v / %#v, want %#v", first, second, want)
	}
}

func TestNonAniBTReleaseHasNoAniBTValues(t *testing.T) {
	facts, err := FromSourceFacts([]byte(`{"group_name":"Test Group","identity_kind":"guid","identity_value":"g","title_tags":{"schema":"reltitle-attrs-v1","implementation":"reltitle@x","tags":["sub_lang:JPSC"],"value_tags":["title:Show"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.AniBTTags) != 0 {
		t.Fatalf("non-AniBT release has AniBT values %#v", facts.AniBTTags)
	}
	object := Object(AniBTTagFields, facts.AniBTTags)
	for _, field := range AniBTTagFields {
		if value, ok := object[field.Key]; !ok || value != nil {
			t.Fatalf("missing AniBT field %s = %#v, want null", field.Key, value)
		}
	}
	// No implied value: JPSC stays JPSC.
	if got := facts.Reltitle["SubLang"]; got != "JPSC" {
		t.Fatalf("SubLang = %q", got)
	}
}

func TestNamespacesNeverCrossFill(t *testing.T) {
	facts := FromTagSets(
		&acquisitiondomain.TitleTagSet{Schema: acquisitiondomain.TitleTagSchema, Tags: []string{}, ValueTags: []string{}},
		&acquisitiondomain.AniBTTagSet{Tags: []string{"resolution:1080p", "language:CHS"}},
	)
	if len(facts.Reltitle) != 0 {
		t.Fatalf("AniBT values leaked into Rt: %#v", facts.Reltitle)
	}
	facts = FromTagSets(&acquisitiondomain.TitleTagSet{Schema: acquisitiondomain.TitleTagSchema, Tags: []string{"resolution:720p", "sub_lang:SC"}}, &acquisitiondomain.AniBTTagSet{Tags: []string{}})
	if len(facts.AniBTTags) != 0 {
		t.Fatalf("reltitle values leaked into AniBTTags: %#v", facts.AniBTTags)
	}
}

func TestFromSourceFactsDecodesFrozenBlocks(t *testing.T) {
	facts, err := FromSourceFacts([]byte(`{"identity_kind":"guid","identity_value":"g","anibt":{"schema":"anibt-rss-1.0","tags":["language:CHS","tag:HEVC"]},"title_tags":{"schema":"reltitle-attrs-v1","implementation":"reltitle@x","tags":["resolution:1080p"],"value_tags":["title:Show","group:Grp"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(facts.Reltitle, Values{"Title": "Show", "Subgroup": "Grp", "Resolution": "1080p"}) || !reflect.DeepEqual(facts.AniBTTags, Values{"Language": "CHS", "Tag": "HEVC"}) {
		t.Fatalf("facts = %#v", facts)
	}
	for _, raw := range []string{"", "null", "{}"} {
		facts, err := FromSourceFacts([]byte(raw))
		if err != nil || len(facts.Reltitle) != 0 || len(facts.AniBTTags) != 0 {
			t.Fatalf("FromSourceFacts(%q) = %#v, %v", raw, facts, err)
		}
	}
	if _, err := FromSourceFacts([]byte(`{"anibt":"broken"}`)); err == nil {
		t.Fatal("invalid source facts decoded")
	}
}

func TestJoinValues(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"  "}, ""},
		{[]string{"SC"}, "SC"},
		{[]string{"tc", "SC", "TC"}, "SC+tc"},
		{[]string{"b", "A", "c"}, "A+b+c"},
	}
	for _, tc := range cases {
		if got := JoinValues(tc.in); got != tc.want {
			t.Fatalf("JoinValues(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFieldKeysAreTemplateIdentifiers(t *testing.T) {
	for _, fields := range [][]Field{ReltitleFields, AniBTTagFields} {
		seen := map[string]bool{}
		for _, field := range fields {
			if field.Key == "" || seen[field.Key] || field.Dimension == "" || field.SampleValue == "" {
				t.Fatalf("bad field %+v", field)
			}
			seen[field.Key] = true
		}
	}
	if len(ReltitleFields) != 9 || len(AniBTTagFields) != 5 {
		t.Fatalf("field counts = %d/%d", len(ReltitleFields), len(AniBTTagFields))
	}
}
