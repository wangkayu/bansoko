package notification

import (
	"encoding/json"
	"strings"
	"testing"

	"bansoko/internal/templatefacts"
)

// Every payload key a media-trigger producer writes is a registry fact path:
// payloads are stored under the registry paths with no translation layer.
func TestPayloadKeysAreRegistryPaths(t *testing.T) {
	for _, trigger := range []string{TriggerDownloadSubmitted, TriggerFileOperationCompleted, TriggerFileOperationFailed} {
		for _, key := range PayloadKeys(trigger) {
			fact, ok := templatefacts.Lookup(key)
			if !ok {
				t.Errorf("%s: payload key %s is not a registered fact", trigger, key)
				continue
			}
			if templatefacts.NotificationPayloadKeys[key] != fact.Path {
				t.Errorf("%s: payload key %s is not in the notification payload key set", trigger, key)
			}
		}
	}
}

func TestRenderMessageFormatsDecodedNumbersAndNestedFacts(t *testing.T) {
	facts := map[string]any{"file.size": float64(734003200), "anime": map[string]any{"title": "嵌套"}}
	message, ok := RenderMessage(TriggerFileOperationCompleted, "{{anime.title}}", "{{file.size}}|{{file.name}}", facts)
	if !ok || !message.Valid() {
		t.Fatalf("render failed: %+v", message)
	}
	if message.Title != "嵌套" || message.Body != "734003200|" {
		t.Fatalf("title=%q body=%q", message.Title, message.Body)
	}
	if len(message.Warnings) != 1 || message.Warnings[0].Variable != "file.name" {
		t.Fatalf("warnings=%+v", message.Warnings)
	}
	if got := FormatFact(json.Number("12")); got != "12" {
		t.Fatalf("json number=%q", got)
	}
}

func TestRenderMessageRejectsEmptyAndOversizedOutput(t *testing.T) {
	if message, _ := RenderMessage(TriggerDownloadSubmitted, "{{anime.title_override}}", " ", map[string]any{}); message.Valid() || message.Errors[0].Code != "rendered_empty" {
		t.Fatalf("empty output accepted: %+v", message.Errors)
	}
	long := strings.Repeat("长", 600)
	title := strings.Repeat("{{anime.title}}", 10)
	if message, _ := RenderMessage(TriggerDownloadSubmitted, title, "x", map[string]any{"anime.title": long}); message.Valid() || message.Errors[0].Code != "rendered_too_large" {
		t.Fatalf("oversized title accepted: %+v", message.Errors)
	}
}

func TestCompileReportsSourceSpans(t *testing.T) {
	errors, ok := Compile(TriggerDownloadSubmitted, "ok {{anime.nope}}", "{{lower()}}")
	if !ok || len(errors) != 2 {
		t.Fatalf("errors=%+v", errors)
	}
	if errors[0].Field != FieldTitle || errors[0].Start != 3 || errors[0].End != 17 {
		t.Fatalf("title span=%+v", errors[0])
	}
	if errors[1].Field != FieldBody || errors[1].Start != 0 || errors[1].End != 11 {
		t.Fatalf("body span=%+v", errors[1])
	}
	if _, ok := Compile("nope", "", ""); ok {
		t.Fatal("unknown trigger compiled")
	}
}

func TestRenderReadsValueNamespacesOnEveryTrigger(t *testing.T) {
	sourceFacts := []byte(`{"title_tags":{"schema":"reltitle-attrs-v1","implementation":"x","tags":["resolution:1080p","sub_lang:CHT","sub_lang:chs","sub_lang:CHS"],"value_tags":["title:进击的巨人"]},"anibt":{"tags":["language:CHT","language:CHS","format:MKV"]}}`)
	vars := map[string]any{"info": map[string]any{"tag": "WEB", "source_url": "https://x"}, "bad": "x"}
	facts := ValueFacts(vars, sourceFacts, func(v string) any { return v })
	for key, want := range map[string]any{"rt.title": "进击的巨人", "rt.resolution": "1080p", "rt.sub_lang": "chs+CHT", "rt.subgroup": nil, "anibt.language": "CHS+CHT", "anibt.format": "MKV", "anibt.tag": nil, "vars.info.tag": "WEB"} {
		if got, ok := facts[key]; !ok || got != want {
			t.Errorf("%s = %#v (%v); want %#v", key, got, ok, want)
		}
	}
	if _, ok := facts["vars.info.source_url"]; ok {
		t.Error("a vars key the payload safety check rejects must be left out")
	}
	for _, trigger := range Triggers {
		if trigger == TriggerSubscriptionChanged || trigger == TriggerReplacementDeletionFailed {
			// A subscription transition or a failed deletion concerns no release.
			continue
		}
		message, ok := RenderMessage(trigger, "{{vars.info.tag}} {{rt.sub_lang}}", "{{defaultIfEmpty(anibt.tag, '-')}} {{vars.info.missing}}", facts)
		if !ok || !message.Valid() || message.Title != "WEB chs+CHT" || message.Body != "- " {
			t.Fatalf("%s: %+v", trigger, message)
		}
	}
	if errors, _ := Compile(TriggerDownloadSubmitted, "{{vars.input.x}}", "{{vars.info}}"); len(errors) != 2 {
		t.Fatalf("vars.input and a bare group must not compile: %+v", errors)
	}
	if facts := ValueFacts(nil, []byte(`{"anibt":"broken"}`), func(v string) any { return v }); facts["anibt.format"] != nil || len(facts) != len(ValueFactKeys) {
		t.Fatalf("undecodable source facts = %#v", facts)
	}
}

func TestTitleFactsReadOnlyFrozenNativeTitles(t *testing.T) {
	identity := func(v string) any { return v }
	empty := TitleFacts([]byte(`{"schema_version":4,"media":{}}`), identity)
	for _, key := range TitleFactKeys {
		if value, ok := empty[key]; !ok || value != nil {
			t.Fatalf("%s = %v, want present null", key, value)
		}
	}
	facts := TitleFacts([]byte(`{"schema_version":5,"media":{"bgm_titles":{"state":"known","value":{"title":"B","title_cn":null}},"tmdb_titles":{"state":"known","value":{"title_cn":"T","original_title":"O"}}}}`), identity)
	if facts["bgm.title"] != "B" || facts["bgm.title_cn"] != nil || facts["tmdb.title_cn"] != "T" || facts["tmdb.original_title"] != "O" {
		t.Fatalf("facts = %+v", facts)
	}
}

// The TMDB work summary joins the title-family facts: it reads the frozen
// media.tmdb_titles.value.overview member (migration 047), and an input frozen
// before that member existed — like an observed-but-absent overview — reads
// null rather than a placeholder.
func TestTitleFactsReadsFrozenOverview(t *testing.T) {
	identity := func(v string) any { return v }
	facts := TitleFacts([]byte(`{"schema_version":5,"media":{"tmdb_titles":{"state":"known","value":{"title_cn":"弧之谜语","original_title":"Literary Arcana","overview":"作品的简介。","year":2024}}}}`), identity)
	if facts["tmdb.summary"] != "作品的简介。" {
		t.Fatalf("tmdb.summary = %#v, want the frozen overview", facts["tmdb.summary"])
	}
	absent := TitleFacts([]byte(`{"schema_version":5,"media":{"tmdb_titles":{"state":"known","value":{"title_cn":"弧之谜语","year":2024}}}}`), identity)
	if absent["tmdb.summary"] != nil {
		t.Fatalf("absent member = %#v, want null", absent["tmdb.summary"])
	}
}
