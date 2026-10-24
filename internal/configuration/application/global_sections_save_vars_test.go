package application

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/configuration/domain"
	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/transport/httpapi/generated"
)

// A naming-rules save must refuse a template that reads a vars key the
// deployment has not loaded, with the typed code rather than the generic
// section-value code. The service has no database, so reaching the write path
// would panic: returning the typed refusal proves the refusal comes from save
// validation, before anything is persisted.
func TestPutGlobalSectionRefusesUnknownNamingVarsBeforeWriting(t *testing.T) {
	activateNamingVars(t, namingvars.Vars{"anime": {"ep": "07"}})
	service := &Service{}
	input := namingRulesSectionInput(t, "${anime.title_base}/Season ${vars.anime.season}")
	if _, err := service.PutGlobalSection(context.Background(), animedomain.SectionNamingRules, 0, input); err == nil {
		t.Fatal("a template reading an unloaded vars key must be refused")
	} else {
		var validation *apperrors.Validation
		if !errors.As(err, &validation) || validation.Code != naming.ErrCodeNamingVarsUnknown {
			t.Fatalf("save error = %v, want typed %s", err, naming.ErrCodeNamingVarsUnknown)
		}
	}
}

// uninitializedVarsChildEnv marks the child run of
// TestPutGlobalSectionFailsClosedWithoutLoadedNamingVars. The Vars snapshot is
// process-wide, so the only order-independent way to observe the uninitialized
// state is a process in which nothing initialized it.
const uninitializedVarsChildEnv = "BANSOKO_CONFIG_UNINITIALIZED_VARS_CHILD"

// A process without a loaded snapshot cannot judge a template, so the save fails
// closed with the same typed code instead of writing an unverifiable template.
func TestPutGlobalSectionFailsClosedWithoutLoadedNamingVars(t *testing.T) {
	if os.Getenv(uninitializedVarsChildEnv) != "1" {
		// -test.run selects this test only, so the child never activates a snapshot.
		child := exec.Command(os.Args[0], "-test.run=^TestPutGlobalSectionFailsClosedWithoutLoadedNamingVars$")
		child.Env = append(os.Environ(), uninitializedVarsChildEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("uninitialized child run failed: %v\n%s", err, output)
		}
		return
	}
	// This template needs no vars at all: the refusal is about the missing
	// snapshot, not about what the template references.
	service := &Service{}
	input := namingRulesSectionInput(t, "${anime.title_base}.mkv")
	if _, err := service.PutGlobalSection(context.Background(), animedomain.SectionNamingRules, 0, input); err == nil {
		t.Fatal("a save without a loaded vars snapshot must fail closed")
	} else {
		var validation *apperrors.Validation
		if !errors.As(err, &validation) || validation.Code != naming.ErrCodeNamingVarsUnknown {
			t.Fatalf("save error = %v, want the fail-closed %s", err, naming.ErrCodeNamingVarsUnknown)
		}
	}
}

// An empty unknown video filename template is refused at save like an empty tv
// or movie one, before anything is written.
func TestPutGlobalSectionRefusesEmptyUnknownTemplate(t *testing.T) {
	activateNamingVars(t, namingvars.Vars{})
	service := &Service{}
	input := namingRulesSectionInputWithUnknown(t, "${anime.title_base}.mkv", "")
	if _, err := service.PutGlobalSection(context.Background(), animedomain.SectionNamingRules, 0, input); err == nil {
		t.Fatal("an empty unknown template must be refused")
	} else {
		var validation *apperrors.Validation
		if !errors.As(err, &validation) || validation.Code != naming.ErrCodeNamingTargetInvalid {
			t.Fatalf("save error = %v, want typed %s", err, naming.ErrCodeNamingTargetInvalid)
		}
	}
}

func namingRulesSectionInput(t *testing.T, videoTemplate string) domain.GlobalSectionInput {
	t.Helper()
	return namingRulesSectionInputWithUnknown(t, videoTemplate, "${sanitize(file.name)}")
}

func namingRulesSectionInputWithUnknown(t *testing.T, videoTemplate, unknownTemplate string) domain.GlobalSectionInput {
	t.Helper()
	rules := generated.NamingRules{
		Language: generated.NamingRulesLanguage(naming.Language),
		Tv:       generated.NamingVariant{DirectoryTemplate: "${anime.title_base}", VideoFilenameTemplate: videoTemplate},
		Movie:    generated.NamingVariant{DirectoryTemplate: "${anime.title_base}", VideoFilenameTemplate: "${anime.title_base}.mkv"},
		Unknown:  generated.NamingVariant{DirectoryTemplate: "${sanitize(anime.title)}/extras", VideoFilenameTemplate: unknownTemplate},
	}
	raw, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return domain.GlobalSectionInput{SchemaVersion: animedomain.SectionSchemaVersions[animedomain.SectionNamingRules], Value: value}
}

func activateNamingVars(t *testing.T, vars namingvars.Vars) {
	t.Helper()
	if err := namingvars.SetActive(vars, "sha256:configuration-save-vars"); err != nil {
		t.Fatal(err)
	}
}
