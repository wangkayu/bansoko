package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
)

// The default template is the global naming: switching the default writes its
// body into the naming_rules section, a stale library version is refused, and
// a generic naming_rules save shows up as the default body and bumps the
// library version.
func TestNamingTemplateLibraryDefaultIsTheGlobalNaming(t *testing.T) {
	db := openConnectionTestDB(t)
	ctx := context.Background()
	activateNamingVars(t, namingvars.Vars{})
	svc := NewService(db, newConnectionTestSecretStore(t, db))

	seed, err := svc.GetNamingTemplateLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if seed.Version != 1 || seed.DefaultKey != "default" || len(seed.Templates) != 1 {
		t.Fatalf("seed library = %+v", seed)
	}
	defaultBody := animedomain.NamingRulesFromDomain(naming.DefaultNamingRules())
	clean := defaultBody
	clean.Tv.VideoFilenameTemplate = "${sanitize(anime.title)} - E${pad(ep.display, 2)}.${file.ext}"
	cleanRaw, err := json.Marshal(clean)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := svc.PutNamingTemplateLibrary(ctx, 1, configurationdomain.NamingTemplateLibraryInput{
		DefaultKey: "clean",
		Templates: []configurationdomain.NamingTemplate{
			seed.Templates[0],
			{Key: "clean", Name: "Clean", Rules: cleanRaw},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Version != 2 || saved.DefaultKey != "clean" || len(saved.Templates) != 2 {
		t.Fatalf("saved library = %+v", saved)
	}
	var section []byte
	if err := db.QueryRowContext(ctx, `SELECT value FROM global_config_sections WHERE section_name='naming_rules'`).Scan(&section); err != nil {
		t.Fatalf("global naming not written: %v", err)
	}
	var stored animedomain.NamingRules
	if err := json.Unmarshal(section, &stored); err != nil || stored.Tv.VideoFilenameTemplate != clean.Tv.VideoFilenameTemplate {
		t.Fatalf("global naming = %s (%v)", section, err)
	}

	if _, err := svc.PutNamingTemplateLibrary(ctx, 1, configurationdomain.NamingTemplateLibraryInput{DefaultKey: "clean", Templates: saved.Templates}); err == nil {
		t.Fatal("a stale library version must be refused")
	} else {
		var conflict *apperrors.VersionConflict
		if !errors.As(err, &conflict) {
			t.Fatalf("stale save error = %v", err)
		}
	}

	views, err := svc.ListGlobalSections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var sectionVersion int64
	for _, view := range views {
		if view.SectionName == animedomain.SectionNamingRules {
			sectionVersion = view.Version
		}
	}
	generic := clean
	generic.SubtitleSuffixTemplate = ""
	genericRaw, _ := json.Marshal(generic)
	var genericValue map[string]any
	_ = json.Unmarshal(genericRaw, &genericValue)
	if _, err := svc.PutGlobalSection(ctx, animedomain.SectionNamingRules, sectionVersion, configurationdomain.GlobalSectionInput{SchemaVersion: animedomain.SectionSchemaVersions[animedomain.SectionNamingRules], Value: genericValue}); err != nil {
		t.Fatal(err)
	}
	after, err := svc.GetNamingTemplateLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != 3 {
		t.Fatalf("library version after a section save = %d, want 3", after.Version)
	}
	for _, item := range after.Templates {
		if item.Key != "clean" {
			continue
		}
		var body animedomain.NamingRules
		if err := json.Unmarshal(item.Rules, &body); err != nil || body.SubtitleSuffixTemplate != "" {
			t.Fatalf("default body does not follow the section: %s (%v)", item.Rules, err)
		}
	}
}
