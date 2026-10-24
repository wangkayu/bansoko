package application

import (
	"context"
	"encoding/json"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/namingvars"
	varspersistence "bansoko/internal/namingvars/adapter/persistence"
)

// seedActivePrograms stands in for the server's startup initialization, which
// publishes one active Vars and one active attrrules program on a fresh install.
func seedActivePrograms(t *testing.T, ctx context.Context, service *Service) {
	t.Helper()
	source := `vars.actual = {title: input.release_title};`
	varsDigest, err := namingvars.SourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(source)
	if _, err := service.db.ExecContext(ctx, `INSERT INTO naming_vars_versions(fingerprint,content,active) VALUES($1,$2,TRUE)`, varsDigest, raw); err != nil {
		t.Fatal(err)
	}
	rules := attrrules.Rules{Version: 1, Resolution: []string{"1080p"}, Quality: []string{"web"}, Language: []string{"jpn"}, SpecialMarkers: []string{"special"}, GroupBrackets: []string{"[", "]"}}
	attrDigest, err := attrrules.Fingerprint(rules)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(rules)
	if _, err := service.db.ExecContext(ctx, `INSERT INTO attr_rule_versions(fingerprint,content,active) VALUES($1,$2,TRUE)`, attrDigest, raw); err != nil {
		t.Fatal(err)
	}
}

// A fresh install persists no Global sections and no file-effect default. The
// preset must still be one complete business snapshot built from the code
// defaults, with provenance 0 marking every absent value; a new draft loads it
// before anything was ever saved.
func TestReadBusinessPresetOnFreshInstall(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	service := NewService(db, newConnectionTestSecretStore(t, db))
	seedActivePrograms(t, ctx, service)
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
	if preset.Programs.Vars.Digest == "" || preset.Programs.AttrRules.Digest == "" {
		t.Fatalf("preset must reference the active programs exactly: %+v", preset.Programs)
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

// Activating a new Vars version is picked up by the very next business
// snapshot (what a subscription and an acceptance freeze), while a snapshot
// read earlier keeps its program reference and that program stays exactly
// loadable and evaluable: saving never rewrites or reinterprets history.
func TestBusinessPresetFollowsActivationAndEarlierReferencesStayExact(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	service := NewService(db, newConnectionTestSecretStore(t, db))
	seedActivePrograms(t, ctx, service)

	before, err := service.ReadBusinessPreset(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store := namingvars.NewStore(varspersistence.NewRepository(db))
	active, err := store.ActiveVersion(ctx)
	if err != nil || active.Digest != before.Programs.Vars.Digest {
		t.Fatalf("active version %q / %v, want the preset's program %q", active.Digest, err, before.Programs.Vars.Digest)
	}
	next := `vars.actual = {title: "changed " + input.release_title};`
	saved, err := store.SaveVersion(ctx, next, "", namingvars.OriginEditor, active.Revision)
	if err != nil {
		t.Fatal(err)
	}

	after, err := service.ReadBusinessPreset(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Programs.Vars.Digest != saved.Digest || after.Programs.Vars.Digest == before.Programs.Vars.Digest {
		t.Fatalf("a snapshot after activation references %q, want the new program %q", after.Programs.Vars.Digest, saved.Digest)
	}
	// The earlier reference still resolves to the exact earlier program.
	program, err := store.LoadProgram(ctx, before.Programs.Vars)
	if err != nil {
		t.Fatalf("earlier program reference no longer loads: %v", err)
	}
	evidence := namingvars.Input{ReleaseTitle: "Show - 01"}
	output, err := program.Evaluate(evidence, 0)
	if err != nil || output["actual"]["title"] != "Show - 01" {
		t.Fatalf("earlier program output = %v / %v, want its own result", output, err)
	}
	frozen, err := program.Freeze(evidence)
	if err != nil || frozen.Program != before.Programs.Vars || frozen.Output["actual"]["title"] != "Show - 01" {
		t.Fatalf("freezing under the earlier reference = %+v / %v", frozen, err)
	}
	// A rollback re-points new snapshots at the first program, not at a copy.
	if _, err := store.ActivateRevision(ctx, saved.Revision, active.Revision); err != nil {
		t.Fatal(err)
	}
	rolled, err := service.ReadBusinessPreset(ctx)
	if err != nil || rolled.Programs.Vars != before.Programs.Vars {
		t.Fatalf("rolled-back snapshot references %+v / %v, want %+v", rolled.Programs.Vars, err, before.Programs.Vars)
	}
}
