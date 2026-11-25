package persistence

import (
	"context"
	"reflect"
	"testing"

	"bansoko/internal/namingvars/preflight"
	"bansoko/internal/testutil/disposabledb"
)

func TestPreflightScanEditableTemplatesPostgres(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	ctx := context.Background()

	// Fresh schema has no editable Vars references.
	hits, err := preflight.ScanEditableTemplates(ctx, db)
	if err != nil {
		t.Fatalf("clean scan: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("clean schema hits = %+v, want none", hits)
	}

	// 1. Global naming_rules.
	if _, err := db.ExecContext(ctx, `INSERT INTO global_config_sections (section_name, schema_version, value, version)
VALUES ('naming_rules', 'naming-rules-v1', $1::jsonb, 1)
ON CONFLICT (section_name) DO UPDATE SET value = EXCLUDED.value`, `{
		"language":"js-naming-1",
		"preset":null,
		"tv":{"directory_template":"/lib/${anime.title}","video_filename_template":"${vars.anime.ep}.${file.ext}"},
		"movie":{"directory_template":"/lib/${anime.title}","video_filename_template":"${anime.title}.${file.ext}"},
		"unknown":{"directory_template":"/lib/${anime.title}","video_filename_template":"${anime.title}.${file.ext}"},
		"subtitle_suffix_template":""
	}`); err != nil {
		t.Fatalf("seed global naming_rules: %v", err)
	}

	// 2. Naming template library.
	if _, err := db.ExecContext(ctx, `UPDATE naming_template_library SET value = $1::jsonb WHERE singleton`, `{
		"default_key":"default",
		"templates":[
			{"key":"default","name":"Default"},
			{"key":"custom-vars","name":"Legacy Vars","rules":{
				"language":"js-naming-1",
				"tv":{"directory_template":"/lib/${context.vars.anime.ep}","video_filename_template":"${anime.title}.${file.ext}"},
				"movie":{"directory_template":"/lib/${anime.title}","video_filename_template":"${anime.title}.${file.ext}"},
				"unknown":{"directory_template":"/lib/${anime.title}","video_filename_template":"${anime.title}.${file.ext}"},
				"subtitle_suffix_template":""
			}}
		]
	}`); err != nil {
		t.Fatalf("seed naming_template_library: %v", err)
	}

	// 3. Per-anime override.
	if _, err := db.ExecContext(ctx, `INSERT INTO managed_anime (id, title)
VALUES ('anime-vars-1', 'Vars Show')`); err != nil {
		t.Fatalf("seed managed_anime: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO anime_config_overrides (managed_anime_id, owner_type, section_name, schema_version, value)
VALUES ('anime-vars-1', 'rules', 'naming_rules', 'naming-rules-v1', $1::jsonb)`, `{
		"language":"js-naming-1",
		"tv":{"directory_template":"/lib/${anime.title}","video_filename_template":"${anime.title}.${file.ext}"},
		"movie":{"directory_template":"/lib/${anime.title}","video_filename_template":"${vars[\"anime\"][\"ep\"]}.${file.ext}"},
		"unknown":{"directory_template":"/lib/${anime.title}","video_filename_template":"${anime.title}.${file.ext}"},
		"subtitle_suffix_template":""
	}`); err != nil {
		t.Fatalf("seed anime_config_overrides: %v", err)
	}

	// 4. Non-deleted notification target with custom Vars template (plus a soft-deleted one that must be ignored).
	// Disabled targets need no secret_ref and are still editable, so preflight scans them.
	if _, err := db.ExecContext(ctx, `INSERT INTO plugin_target_configs (id, name, plugin_kind, adapter_kind, enabled, trigger_kinds, config_schema_version, config, template_set)
VALUES
  ('target-active', 'Active', 'notification', 'webhook', false, '["file_operation_completed"]'::jsonb, 1, '{"adapter_kind":"webhook","url":"https://example.com/hook","auth_kind":"none"}'::jsonb, $1::jsonb),
  ('target-deleted', 'Deleted', 'notification', 'webhook', false, '["file_operation_completed"]'::jsonb, 1, '{"adapter_kind":"webhook","url":"https://example.com/hook","auth_kind":"none"}'::jsonb, $1::jsonb)`,
		`{"events":{"file_operation_completed":{"mode":"custom","title_template":"{{vars.anime.ep}}","body_template":"{{anime.title}}"}}}`); err != nil {
		t.Fatalf("seed plugin_target_configs: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE plugin_target_configs SET deleted_at = CURRENT_TIMESTAMP WHERE id = 'target-deleted'`); err != nil {
		t.Fatalf("soft delete target: %v", err)
	}

	hits, err = preflight.ScanEditableTemplates(ctx, db)
	if err != nil {
		t.Fatalf("scan populated: %v", err)
	}
	want := []preflight.Hit{
		{Location: "anime_config_overrides:anime-vars-1", Field: "movie.video_filename_template"},
		{Location: "global_config_sections:naming_rules", Field: "tv.video_filename_template"},
		{Location: "naming_template_library:custom-vars", Field: "tv.directory_template"},
		{Location: "plugin_target_configs:target-active", Field: "events.file_operation_completed.title_template"},
	}
	if !reflect.DeepEqual(hits, want) {
		t.Fatalf("hits = %+v, want %+v", hits, want)
	}
}
