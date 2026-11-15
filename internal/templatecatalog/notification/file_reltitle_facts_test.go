package notification

import (
	"encoding/json"
	"reflect"
	"testing"

	"bansoko/internal/templatefacts"
)

// A file operation preparation freezes the complete reltitle tag set of the
// original download file name; the producer projects that block and never
// parses the rendered library name.
func TestFileReltitleFactsReadsTheFrozenBlock(t *testing.T) {
	render := []byte(`{"renderer_version":"fileoperation-render-v1","file_title_tags":{
		"schema":"reltitle-attrs-v1","implementation":"reltitle@v0.1.0","config_digest":"lexicon:sha256:1",
		"tags":["resolution:2160p","video_codec:HEVC","content_rating:UNCENSORED"],
		"value_tags":["title:进击的巨人","group:文件组","version:v2"]}}`)
	facts := FileReltitleFacts(render, "frt", Text)
	want := map[string]any{
		"frt.title": "进击的巨人", "frt.group": "文件组", "frt.rev": "v2", "frt.res": "2160p",
		"frt.vc": "HEVC", "frt.uncensored": true,
		"frt.source": nil, "frt.ac": nil, "frt.lang": nil, "frt.subflag": nil,
	}
	if len(facts) != len(templatefacts.ReltitleFields) {
		t.Fatalf("facts = %d, want %d", len(facts), len(templatefacts.ReltitleFields))
	}
	encoded, _ := json.Marshal(facts)
	var got map[string]any
	_ = json.Unmarshal(encoded, &got)
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %#v, want %#v (facts %s)", key, got[key], value, encoded)
		}
	}
	if value, ok := got["frt.uncensored"]; !ok || value != true {
		t.Fatalf("an uncensored file must read true, got %#v", value)
	}
}

// A block written before this feature, a side with no file operation, an
// explicit null and a malformed block all read null everywhere: nothing is
// reparsed and no other namespace stands in.
func TestFileReltitleFactsWithoutABlockIsEmpty(t *testing.T) {
	for _, render := range []string{
		``,
		`{}`,
		`{"renderer_version":"fileoperation-render-v1"}`,
		`{"file_title_tags":null}`,
		`{"file_title_tags":"broken"}`,
		`{"file_title_tags":{"tags":["resolution:1080p",7]}}`,
	} {
		facts := FileReltitleFacts([]byte(render), "frt", Text)
		if len(facts) != len(templatefacts.ReltitleFields) {
			t.Fatalf("%s: facts = %d", render, len(facts))
		}
		for key, value := range facts {
			if value != nil {
				t.Fatalf("%s: %s = %#v, want null", render, key, value)
			}
		}
	}
}

// A successful parse without the marker answers false; a diagnostic withholds
// the yes/no answer but keeps the tags it froze.
func TestFileReltitleFactsUncensoredStates(t *testing.T) {
	clean := []byte(`{"file_title_tags":{"schema":"reltitle-attrs-v1","implementation":"r","tags":["resolution:1080p"]}}`)
	if got := FileReltitleFacts(clean, "frt", Text)["frt.uncensored"]; got != false {
		t.Fatalf("clean parse uncensored = %#v, want false", got)
	}
	diagnostic := []byte(`{"file_title_tags":{"schema":"reltitle-attrs-v1","implementation":"r","tags":["resolution:1080p","content_rating:UNCENSORED"],"diagnostic":"reltitle_parse_failed"}}`)
	facts := FileReltitleFacts(diagnostic, "frt", Text)
	if facts["frt.uncensored"] != nil {
		t.Fatalf("a diagnostic must withhold the yes/no answer: %#v", facts)
	}
	if facts["frt.res"] != "1080p" {
		t.Fatalf("a diagnostic keeps the frozen tags: %#v", facts)
	}
	// An old schema contributes no ValueTags and no yes/no answer.
	old := []byte(`{"file_title_tags":{"schema":"reltitle-attrs-v0","tags":["resolution:720p","content_rating:UNCENSORED"],"value_tags":["title:ignored"]}}`)
	facts = FileReltitleFacts(old, "frt", Text)
	if facts["frt.title"] != nil || facts["frt.uncensored"] != nil || facts["frt.res"] != "720p" {
		t.Fatalf("old schema facts = %#v", facts)
	}
}

// The taker side uses its own prefix: both sides can be present, and either
// side can be absent without affecting the other.
func TestFileReltitleFactsUsesThePrefix(t *testing.T) {
	render := []byte(`{"file_title_tags":{"schema":"reltitle-attrs-v1","tags":["resolution:2160p"],"value_tags":["group:文件组B"]}}`)
	facts := FileReltitleFacts(render, "taker.frt", Text)
	if facts["taker.frt.group"] != "文件组B" || facts["taker.frt.res"] != "2160p" {
		t.Fatalf("taker facts = %#v", facts)
	}
	for _, key := range FileReltitleFactKeys("taker.frt") {
		if len(key) < len("taker.frt.") || key[:len("taker.frt.")] != "taker.frt." {
			t.Fatalf("key %q does not carry the taker prefix", key)
		}
	}
	if got := FileReltitleFacts(nil, "taker.frt", Text)["taker.frt.group"]; got != nil {
		t.Fatalf("an absent taker block = %#v, want null", got)
	}
}

// The displaced producer reads the placement's own copy of the block, which
// is the same projection as the render snapshot's member; an empty, null or
// malformed copy reads null everywhere.
func TestFileReltitleFactsFromTitleTagsReadsAPlacementCopy(t *testing.T) {
	block := []byte(`{"schema":"reltitle-attrs-v1","implementation":"reltitle@v0.1.0","tags":["resolution:1080p","content_rating:UNCENSORED"],"value_tags":["title:进击的巨人","group:文件组"]}`)
	render := []byte(`{"renderer_version":"fileoperation-render-v1","file_title_tags":` + string(block) + `}`)
	if got, want := FileReltitleFactsFromTitleTags(block, "frt", Text), FileReltitleFacts(render, "frt", Text); !reflect.DeepEqual(got, want) {
		t.Fatalf("placement copy = %#v, render member = %#v", got, want)
	}
	facts := FileReltitleFactsFromTitleTags(block, "taker.frt", Text)
	if facts["taker.frt.title"] != "进击的巨人" || facts["taker.frt.group"] != "文件组" || facts["taker.frt.res"] != "1080p" || facts["taker.frt.uncensored"] != true {
		t.Fatalf("placement copy facts = %#v", facts)
	}
	for _, empty := range [][]byte{nil, []byte(`null`), []byte(`"broken"`), []byte(`[]`), []byte(`{"tags":["resolution:1080p",7]}`)} {
		for key, value := range FileReltitleFactsFromTitleTags(empty, "frt", Text) {
			if value != nil {
				t.Fatalf("%s: %s = %#v, want null", empty, key, value)
			}
		}
	}
}

// frt is advertised only to the triggers whose producer freezes the file's own
// parse; taker.frt only to the one-to-one displaced event. A reference from any
// other trigger is rejected by Compile, including inside showIf arguments.
func TestFileReltitleFactsAreScopedToTheirTriggers(t *testing.T) {
	for _, trigger := range []string{TriggerFileOperationCompleted, TriggerFileOperationFailed, TriggerFileDisplaced} {
		if errors, known := Compile(trigger, `{{frt.group}}`, `{{frt.res}}`); !known || len(errors) != 0 {
			t.Fatalf("%s: frt must compile, errors %+v", trigger, errors)
		}
	}
	if errors, _ := Compile(TriggerFileOperationCompleted, `{{showIf(frt.group, 'x')}}`, ""); len(errors) != 0 {
		t.Fatalf("showIf(frt.group) must compile for file_operation_completed: %+v", errors)
	}
	if errors, _ := Compile(TriggerFileDisplaced, `{{taker.frt.group}}`, ""); len(errors) != 0 {
		t.Fatalf("taker.frt must compile for file_displaced: %+v", errors)
	}
	for _, trigger := range []string{TriggerDownloadSubmitted, TriggerReleaseDiscovered, TriggerDownloadFailed,
		TriggerSubscriptionChanged, TriggerUnknownFilesImported, TriggerReplacementDeletionFailed, TriggerReplacementApplied} {
		if errors, _ := Compile(trigger, `{{frt.group}}`, ""); len(errors) == 0 {
			t.Fatalf("%s must reject frt.group", trigger)
		}
		if errors, _ := Compile(trigger, `{{showIf(frt.group, 'x')}}`, ""); len(errors) == 0 {
			t.Fatalf("%s must reject frt.group inside showIf", trigger)
		}
	}
	for _, trigger := range []string{TriggerFileOperationCompleted, TriggerFileOperationFailed} {
		if errors, _ := Compile(trigger, `{{taker.frt.group}}`, ""); len(errors) == 0 {
			t.Fatalf("%s must reject taker.frt: only file_displaced names a taker", trigger)
		}
	}
}
