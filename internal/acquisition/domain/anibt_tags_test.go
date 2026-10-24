package domain

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"
)

func TestExtractAniBTTagsCurrentNamespace(t *testing.T) {
	payload, err := os.ReadFile("testdata/anibt-rss-1.0.xml")
	if err != nil {
		t.Fatal(err)
	}
	items, err := ParseFeed(payload)
	if err != nil {
		t.Fatalf("ParseFeed: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	cases := []struct {
		name       string
		tags       []string
		diagnostic string
	}{
		{"multi-value language and customTag", []string{"resolution:1080p", "language:CHS", "language:CHT", "subtitle:INTERNAL", "format:MKV", "codec:HEVC", "bitdepth:10bit", "source:WEBRip", "audio:AAC"}, ""},
		{"second release", []string{"resolution:1080p", "language:CHS", "language:CHT", "subtitle:INTERNAL", "format:MKV", "codec:AV1", "tag:BDRip", "audio:OPUS"}, ""},
		{"subtitle NONE has no language; duplicate customTag folds", []string{"resolution:720p", "subtitle:NONE", "format:MP4", "codec:AVC", "source:WEB-DL"}, ""},
		{"current namespace without tag fields", []string{}, AniBTTagDiagnosticTagsAbsent},
	}
	for index, tc := range cases {
		set := ExtractAniBTTags(items[index].Extensions)
		if set.Schema != AniBTTagSchemaV1 {
			t.Fatalf("%s: schema = %q", tc.name, set.Schema)
		}
		if !reflect.DeepEqual(set.Tags, tc.tags) {
			t.Fatalf("%s: tags = %#v, want %#v", tc.name, set.Tags, tc.tags)
		}
		if set.Diagnostic != tc.diagnostic {
			t.Fatalf("%s: diagnostic = %q, want %q", tc.name, set.Diagnostic, tc.diagnostic)
		}
	}
	// Episode/version/group fields are never tags.
	for _, tag := range ExtractAniBTTags(items[0].Extensions).Tags {
		if dimension, _, _ := SplitTag(tag); !slices.Contains(AniBTTagDimensions, dimension) {
			t.Fatalf("unexpected dimension in %q", tag)
		}
	}
}

func TestExtractAniBTTagsLegacyAndAbsentNamespace(t *testing.T) {
	legacy := `<?xml version="1.0"?><rss version="2.0"><channel><item>
<title>[Group] Legacy - 01</title><link>https://anibt.moe/r/1</link><guid>legacy-1</guid>
<torrent xmlns="https://anibt.moe/xmlns/0.1/"><link>https://anibt.moe/t/1.torrent</link><infoHash>abc</infoHash></torrent>
</item></channel></rss>`
	items, err := ParseFeed([]byte(legacy))
	if err != nil {
		t.Fatal(err)
	}
	set := ExtractAniBTTags(items[0].Extensions)
	if set.Schema != "" || len(set.Tags) != 0 || set.Diagnostic != AniBTTagDiagnosticNamespaceLegacy {
		t.Fatalf("legacy set = %#v", set)
	}
	plain := `<?xml version="1.0"?><rss version="2.0"><channel><item>
<title>[Group] Plain - 01</title><guid>plain-1</guid><enclosure url="https://example.test/1.torrent" type="application/x-bittorrent"/>
</item></channel></rss>`
	items, err = ParseFeed([]byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	set = ExtractAniBTTags(items[0].Extensions)
	if set.Schema != "" || set.Tags == nil || len(set.Tags) != 0 || set.Diagnostic != AniBTTagDiagnosticNamespaceAbsent {
		t.Fatalf("absent set = %#v", set)
	}
}

func TestExtractAniBTTagsDropsOversizedValues(t *testing.T) {
	long := make([]byte, MaxAniBTTagValueBytes+1)
	for i := range long {
		long[i] = 'x'
	}
	set := ExtractAniBTTags([]FeedExtensionField{
		{Space: AniBTNamespaceV1, Local: "customTag", Value: string(long)},
		{Space: AniBTNamespaceV1, Local: "resolution", Value: " 1080p "},
		{Space: AniBTNamespaceV1, Local: "episode", Value: "9"},
	})
	if !reflect.DeepEqual(set.Tags, []string{"resolution:1080p"}) || set.Diagnostic != AniBTTagDiagnosticValueDropped {
		t.Fatalf("set = %#v", set)
	}
}

func TestClassifyAniBTCustomTagUsesTableCaseInsensitively(t *testing.T) {
	cases := map[string]string{
		"HEVC": "codec", " hevc ": "codec", "AV1": "codec", "10BIT": "bitdepth", "8bit": "bitdepth",
		"flac": "audio", "Opus": "audio", "web-dl": "source", "WEBRip": "source",
		"cr": "platform", "Baha": "platform", "B-Global": "platform", "bilibili": "platform", "ABEMA": "platform", "NF": "platform", "CTHP": "platform",
		"PGS": "subformat", "BDRip": "tag", "x265": "tag",
	}
	for value, want := range cases {
		if got := ClassifyAniBTCustomTag(value); got != want {
			t.Fatalf("ClassifyAniBTCustomTag(%q) = %q, want %q", value, got, want)
		}
	}
	set := ExtractAniBTTags([]FeedExtensionField{
		{Space: AniBTNamespaceV1, Local: "customTag", Value: "hevc"},
		{Space: AniBTNamespaceV1, Local: "customTag", Value: "HEVC"},
		{Space: AniBTNamespaceV1, Local: "customTag", Value: "Pinyin"},
	})
	if !reflect.DeepEqual(set.Tags, []string{"codec:hevc", "tag:Pinyin"}) {
		t.Fatalf("tags = %#v", set.Tags)
	}
}

func TestStoredAniBTTagsReclassifiesLegacyCustomTagsOnRead(t *testing.T) {
	var stored StoredAniBTTags
	if err := json.Unmarshal([]byte(`{"schema":"anibt-rss-1.0","tags":["language:CHS","tag:HEVC","tag:BDRip","codec:AVC","tag:baha"]}`), &stored); err != nil {
		t.Fatal(err)
	}
	want := []string{"language:CHS", "codec:HEVC", "tag:BDRip", "codec:AVC", "platform:baha"}
	if !reflect.DeepEqual(stored.Tags, want) || stored.Schema != AniBTTagSchemaV1 {
		t.Fatalf("stored = %#v", stored)
	}
	_, anibt, err := DecodeSourceFactTagSets([]byte(`{"anibt":{"tags":["tag:10bit"]}}`))
	if err != nil || anibt == nil || !reflect.DeepEqual(anibt.Tags, []string{"bitdepth:10bit"}) {
		t.Fatalf("decoded = %#v, %v", anibt, err)
	}
}
