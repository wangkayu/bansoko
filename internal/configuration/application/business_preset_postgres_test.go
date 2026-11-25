package application

import (
	"context"
	"testing"

	animedomain "bansoko/internal/anime/domain"
)

// A fresh install persists no Global sections and no file-effect default. The
// preset must still be one complete business snapshot built from the code
// defaults, with provenance 0 marking every absent value; a new draft loads it
// before anything was ever saved.
func TestReadBusinessPresetOnFreshInstall(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	service := NewService(db, newConnectionTestSecretStore(t, db))
	if _, err := db.ExecContext(ctx, `DELETE FROM global_config_sections WHERE section_name = 'file_effect_policy'`); err != nil {
		t.Fatal(err)
	}

	preset, err := service.ReadBusinessPreset(ctx)
	if err != nil {
		t.Fatalf("fresh-install business preset: %v", err)
	}
	if preset.UnclassifiedRoute != "skip" || preset.FileEffectVersion != 0 {
		t.Fatalf("absent file-effect default = route %q version %d, want the code default skip at provenance 0", preset.UnclassifiedRoute, preset.FileEffectVersion)
	}
	for _, name := range animedomain.EffectiveSectionOrder {
		if len(preset.Sections[name]) == 0 {
			t.Fatalf("preset section %q missing", name)
		}
	}
	if preset.SchemaVersion != animedomain.BusinessSnapshotSchemaVersion {
		t.Fatalf("preset schema = %q, want %q", preset.SchemaVersion, animedomain.BusinessSnapshotSchemaVersion)
	}
	if _, err := preset.Digest(); err != nil {
		t.Fatalf("preset must be a complete, digestible business snapshot: %v", err)
	}

	// A persisted default is used as-is once it exists.
	if _, err := db.ExecContext(ctx, `INSERT INTO global_config_sections(section_name,schema_version,value,version,updated_at) VALUES('file_effect_policy','file-effect-policy-v1','{"unclassified_route":"route"}'::jsonb,3,CURRENT_TIMESTAMP) ON CONFLICT (section_name) DO UPDATE SET value=EXCLUDED.value, version=3, updated_at=CURRENT_TIMESTAMP`); err != nil {
		t.Fatal(err)
	}
	preset, err = service.ReadBusinessPreset(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if preset.UnclassifiedRoute != "route" || preset.FileEffectVersion != 3 {
		t.Fatalf("persisted file-effect default = route %q version %d, want route at 3", preset.UnclassifiedRoute, preset.FileEffectVersion)
	}
}
