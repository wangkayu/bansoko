package reltitletags

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	reltitleconfigs "github.com/wangkayu/reltitle/configs"

	acquisitiondomain "bansoko/internal/acquisition/domain"
)

func TestExtractProjectsNonEpisodeCanonicalAttributes(t *testing.T) {
	if err := Ready(); err != nil {
		t.Fatalf("embedded reltitle must load: %v", err)
	}
	cases := []struct {
		title string
		want  []string
	}{
		{"[SampleGroup] Sample Title - 03 [1080p][CHS].mp4", []string{"resolution:1080p", "container:MP4", "sub_lang:SC"}},
		{"[Group] Show [1080p][720p][CHS][CHT] - 03", []string{"resolution:1080p", "resolution:720p", "sub_lang:SC", "sub_lang:TC"}},
		{"[Group] Show 1-12 [1080p][Baha][WEBRIP]", []string{"resolution:1080p", "source_type:WEBRip", "platform:BAHA"}},
	}
	for _, tc := range cases {
		got := Extract(tc.title)
		if got.Diagnostic != "" || got.Schema != Schema || got.Implementation != ImplementationVersion || !strings.HasPrefix(got.ConfigDigest, "lexicon:sha256:") {
			t.Fatalf("%q: unexpected envelope %+v", tc.title, got)
		}
		if !reflect.DeepEqual(got.Tags, tc.want) {
			t.Fatalf("%q: tags = %v, want %v", tc.title, got.Tags, tc.want)
		}
		for _, tag := range got.Tags {
			dimension, _, _ := acquisitiondomain.SplitTag(tag)
			if !IsDimension(dimension) {
				t.Fatalf("%q: tag %q is outside the title dimensions", tc.title, tag)
			}
		}
	}
}

func TestExtractNeverCarriesEpisodeIdentity(t *testing.T) {
	got := Extract("[Group] Show S02E05 v2 [1080p]")
	for _, tag := range got.Tags {
		dimension, _, _ := acquisitiondomain.SplitTag(tag)
		switch dimension {
		case "episode", "season", "title", "group", "version", "episode_range":
			t.Fatalf("episode/identity attribute leaked: %v", got.Tags)
		}
	}
	if want := []string{"title:Show", "group:Group", "version:v2"}; !reflect.DeepEqual(got.ValueTags, want) {
		t.Fatalf("value tags = %v, want %v", got.ValueTags, want)
	}
}

func TestExtractIsDeterministicAndBounded(t *testing.T) {
	title := "[Group] Show - 07 [WebRip 1080p HEVC-10bit AAC][简繁内封字幕]"
	first, second := Extract(title), Extract(title)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("not deterministic: %+v vs %+v", first, second)
	}
	if got := Extract(strings.Repeat("a", MaxTitleBytes+1)); got.Diagnostic != acquisitiondomain.TitleTagDiagnosticParseFailed || len(got.Tags) != 0 {
		t.Fatalf("oversized title: %+v", got)
	}
	if got := Extract("   "); got.Diagnostic != "" || len(got.Tags) != 0 {
		t.Fatalf("blank title: %+v", got)
	}
}

func TestVocabularyComesFromCanonicalDictionary(t *testing.T) {
	vocabulary := Vocabulary()
	if len(vocabulary) != len(Dimensions) {
		t.Fatalf("vocabulary dimensions = %d", len(vocabulary))
	}
	for index, entry := range vocabulary {
		if entry.Dimension != Dimensions[index] || len(entry.Values) == 0 {
			t.Fatalf("vocabulary[%d] = %+v", index, entry)
		}
	}
	if value, ok := CanonicalValue("source_type", "webrip"); !ok || value != "WEBRip" {
		t.Fatalf("CanonicalValue(source_type, webrip) = %q, %v", value, ok)
	}
	if _, ok := CanonicalValue("resolution", "1081p"); ok {
		t.Fatal("unknown value accepted")
	}
}

// TestImplementationVersionMatchesModuleRequirement reads the version the root
// module requires and asserts the adapter reports it: an upgrade that forgets
// this constant fails here instead of shipping a stale implementation identity.
func TestImplementationVersionMatchesModuleRequirement(t *testing.T) {
	module, err := os.ReadFile(filepath.Join("..", "..", "..", "go.mod"))
	if err != nil {
		t.Fatalf("read root go.mod: %v", err)
	}
	version := requiredModuleVersion(string(module), "github.com/wangkayu/reltitle")
	if version == "" {
		t.Fatal("root go.mod does not require github.com/wangkayu/reltitle")
	}
	if want := "reltitle@" + version; ImplementationVersion != want {
		t.Fatalf("ImplementationVersion = %q, want %q", ImplementationVersion, want)
	}
}

// requiredModuleVersion returns the version a go.mod requires for module,
// from either a require block line or a single-line require directive.
func requiredModuleVersion(modFile, module string) string {
	for _, line := range strings.Split(modFile, "\n") {
		line = strings.TrimPrefix(strings.TrimSpace(line), "require ")
		if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == module {
			return fields[1]
		}
	}
	return ""
}

func TestExtractFreezesTitleGroupAndVersionAsValueTags(t *testing.T) {
	got := Extract("[SampleGroup] Sample Title - 03 [1080p][CHS].mp4")
	if got.Schema != acquisitiondomain.TitleTagSchema {
		t.Fatalf("schema = %q", got.Schema)
	}
	if want := []string{"title:Sample Title", "group:SampleGroup"}; !reflect.DeepEqual(got.ValueTags, want) {
		t.Fatalf("name tags = %v, want %v", got.ValueTags, want)
	}
	for _, tag := range got.Tags {
		if dimension, _, _ := acquisitiondomain.SplitTag(tag); dimension == "title" || dimension == "group" {
			t.Fatalf("name attribute leaked into filter tags: %v", got.Tags)
		}
	}
	for _, tag := range got.ValueTags {
		if dimension, _, _ := acquisitiondomain.SplitTag(tag); dimension != "title" && dimension != "group" && dimension != "version" {
			t.Fatalf("non-template attribute in value tags: %v", got.ValueTags)
		}
	}
	if blank := Extract("  "); len(blank.ValueTags) != 0 {
		t.Fatalf("blank title name tags = %v", blank.ValueTags)
	}
}

func TestSubtitleLanguagesReadsCompositeCodes(t *testing.T) {
	for tag, want := range map[string]string{"JPSC": "JPSC", "JPTC": "JPTC", "jpsc": "JPSC", "chs": "SC"} {
		if got := SubtitleLanguages(tag); len(got) != 1 || got[0] != want {
			t.Fatalf("SubtitleLanguages(%q) = %v, want [%s]", tag, got, want)
		}
	}
}

func TestExtractFreezesContentRatingAsValueTag(t *testing.T) {
	got := Extract("[Group] Show - 03 [1080p][无修正]")
	found := false
	for _, tag := range got.ValueTags {
		found = found || tag == "content_rating:UNCENSORED"
	}
	if !found {
		t.Fatalf("content_rating missing from value tags: %v", got.ValueTags)
	}
	for _, tag := range got.Tags {
		if strings.HasPrefix(tag, "content_rating:") {
			t.Fatalf("content_rating leaked into filter tags: %v", got.Tags)
		}
	}
}

// ValueVocabulary(content_rating) must stay within what the pinned lexicon
// can emit: every listed spelling appears as a content_rating normalized
// value there.
func TestContentRatingVocabularyMatchesLexicon(t *testing.T) {
	raw, err := fs.ReadFile(reltitleconfigs.FS, "lexicon.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range ValueVocabulary("content_rating") {
		pattern := regexp.MustCompile(`type: content_rating\s+normalized: "` + regexp.QuoteMeta(value) + `"`)
		if !pattern.Match(raw) {
			t.Fatalf("%s is not a content_rating value of the pinned lexicon", value)
		}
	}
	if ValueVocabulary("title") != nil {
		t.Fatal("only content_rating has a value vocabulary")
	}
}
