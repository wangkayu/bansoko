package varspreflight

import (
	"reflect"
	"testing"
)

func TestScanNamingRulesJSON(t *testing.T) {
	raw := []byte(`{
		"language":"js-naming-1",
		"tv":{"directory_template":"/lib/${anime.title}","video_filename_template":"${vars.anime.ep}.${file.ext}"},
		"movie":{"directory_template":"/lib/${anime.title}","video_filename_template":"${anime.title}.${file.ext}"},
		"unknown":{"directory_template":"/lib/${anime.title}","video_filename_template":"${context.vars.x.y}.${file.ext}"},
		"subtitle_suffix_template":"${\"vars.literal\"}"
	}`)
	want := []Hit{
		{Location: "global_config_sections:naming_rules", Field: "tv.video_filename_template"},
		{Location: "global_config_sections:naming_rules", Field: "unknown.video_filename_template"},
	}
	got, err := ScanNamingRulesJSON("global_config_sections:naming_rules", raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ScanNamingRulesJSON = %+v, want %+v", got, want)
	}
}

func TestScanFrozenInputChecksBothNamingLocations(t *testing.T) {
	raw := []byte(`{
		"config":{"sections":[
			{"section_key":"naming_rules","value":{"tv":{"directory_template":"${context[\"vars\"].anime.dir}"}}},
			{"section_key":"other","value":{"tv":{"directory_template":"${vars.ignored.dir}"}}}
		]},
		"delivery":{"naming":{"templates":{"movie":{"video_filename_template":"${vars.media.name}"}}}}
	}`)
	got, err := scanFrozenInput("acquisitions:test", raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []Hit{
		{Location: "acquisitions:test:config.sections.naming_rules", Field: "tv.directory_template"},
		{Location: "acquisitions:test:delivery.naming.templates", Field: "movie.video_filename_template"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scanFrozenInput = %+v, want %+v", got, want)
	}
}

func TestScanNamingRulesJSONFailsClosed(t *testing.T) {
	for _, raw := range []string{
		`{`,
		`{"tv":{"video_filename_template":"${("}}`,
	} {
		if hits, err := ScanNamingRulesJSON("broken", []byte(raw)); err == nil || hits != nil {
			t.Fatalf("ScanNamingRulesJSON(%q) = %+v, %v; want no hits and an error", raw, hits, err)
		}
	}
}

func TestScanNamingTemplateLibraryJSON(t *testing.T) {
	raw := []byte(`{
		"default_key":"default",
		"templates":[
			{"key":"default","name":"Default"},
			{"key":"custom-clean","name":"Clean","rules":{"tv":{"directory_template":"/lib/${anime.title}","video_filename_template":"${anime.title}.${file.ext}"}}},
			{"key":"custom-vars","name":"Legacy","rules":{"tv":{"directory_template":"/lib/${vars[\"a\"][\"b\"]}","video_filename_template":"${anime.title}.${file.ext}"}}}
		]
	}`)
	want := []Hit{{Location: "naming_template_library:custom-vars", Field: "tv.directory_template"}}
	got, err := ScanNamingTemplateLibraryJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ScanNamingTemplateLibraryJSON = %+v, want %+v", got, want)
	}
}

func TestScanCandidateDraftJSON(t *testing.T) {
	// Draft without business body is ignored safely.
	got, err := ScanCandidateDraftJSON("anime_config_candidates:c0", []byte(`{"title":"No Business"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("draft without business = %+v, want none", got)
	}
	raw := []byte(`{"business":{"sections":{"naming_rules":{"movie":{"directory_template":"/lib/${(() => { const { vars: v } = context; return v.a.b; })()}","video_filename_template":"${anime.title}.${file.ext}"}}}}}`)
	want := []Hit{{Location: "anime_config_candidates:c1", Field: "movie.directory_template"}}
	got, err = ScanCandidateDraftJSON("anime_config_candidates:c1", raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ScanCandidateDraftJSON = %+v, want %+v", got, want)
	}
}

func TestScanNotificationTemplateSetJSON(t *testing.T) {
	raw := []byte(`{
		"events":{
			"file_imported":{"mode":"custom","title_template":"{{vars.anime.ep}}","body_template":"literal vars.anime.ep and {{default(vars.a.b, 'ok')}}"},
			"download_completed":{"mode":"system_default","title_template":"{{vars.ignored.key}}","body_template":"{{vars.ignored.key}}"},
			"file_failed":{"mode":"custom","title_template":"{{anime.title}}","body_template":"{{default(ep.number, 'vars.literal')}}"}
		}
	}`)
	want := []Hit{
		{Location: "plugin_target_configs:t1", Field: "events.file_imported.body_template"},
		{Location: "plugin_target_configs:t1", Field: "events.file_imported.title_template"},
	}
	got, err := ScanNotificationTemplateSetJSON("plugin_target_configs:t1", raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ScanNotificationTemplateSetJSON = %+v, want %+v", got, want)
	}
}

func TestScanNotificationTemplateSetJSONFailsClosed(t *testing.T) {
	raw := []byte(`{"events":{"file_imported":{"mode":"custom","title_template":"{{vars.anime.ep","body_template":""}}}}`)
	if hits, err := ScanNotificationTemplateSetJSON("plugin_target_configs:broken", raw); err == nil || hits != nil {
		t.Fatalf("hits = %+v, err = %v; want no hits and an error", hits, err)
	}
}
