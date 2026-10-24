package application

import (
	"encoding/json"
	"errors"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	configurationdomain "bansoko/internal/configuration/domain"
	"bansoko/internal/postprocessing/naming"
)

func defaultNamingTemplateRules(t *testing.T) json.RawMessage {
	t.Helper()
	value := animedomain.NamingRulesFromDomain(naming.DefaultNamingRules())
	preset := "anime_default"
	value.Preset = &preset
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func namingTemplateErrorCode(err error) string {
	var validation *apperrors.Validation
	if errors.As(err, &validation) {
		return validation.Code
	}
	return ""
}

// The library keeps the default body out of the stored document (it is the
// naming_rules section), stores the others, and drops the legacy preset field.
func TestNormalizeNamingTemplateLibrarySplitsTheDefaultBody(t *testing.T) {
	rules := defaultNamingTemplateRules(t)
	library, defaultRules, err := normalizeNamingTemplateLibrary(configurationdomain.NamingTemplateLibraryInput{
		DefaultKey: "b",
		Templates: []configurationdomain.NamingTemplate{
			{Key: "a", Name: " First ", Rules: rules},
			{Key: "b", Name: "Second", Rules: rules},
		},
	}, map[string]map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if library.DefaultKey != "b" || len(library.Templates) != 2 {
		t.Fatalf("library = %+v", library)
	}
	if library.Templates[0].Name != "First" || len(library.Templates[0].Rules) == 0 {
		t.Fatalf("non-default template = %+v", library.Templates[0])
	}
	if library.Templates[1].Rules != nil {
		t.Fatalf("default template body stored in the library: %s", library.Templates[1].Rules)
	}
	var decoded map[string]any
	if err := json.Unmarshal(defaultRules, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["preset"]; ok {
		t.Fatalf("preset kept on a template body: %s", defaultRules)
	}
}

func TestNormalizeNamingTemplateLibraryRefusals(t *testing.T) {
	rules := defaultNamingTemplateRules(t)
	invalid := json.RawMessage(`{"language":"js-naming-1","tv":{"directory_template":"relative","video_filename_template":""},"movie":{"directory_template":"","video_filename_template":""},"unknown":{"directory_template":"","video_filename_template":""},"subtitle_suffix_template":""}`)
	cases := []struct {
		name  string
		input configurationdomain.NamingTemplateLibraryInput
		code  string
	}{
		{"empty list", configurationdomain.NamingTemplateLibraryInput{DefaultKey: "a"}, errNamingTemplateListEmpty},
		{"default deleted", configurationdomain.NamingTemplateLibraryInput{DefaultKey: "gone", Templates: []configurationdomain.NamingTemplate{{Key: "a", Name: "A", Rules: rules}}}, errNamingTemplateDefaultMissing},
		{"bad key", configurationdomain.NamingTemplateLibraryInput{DefaultKey: "A", Templates: []configurationdomain.NamingTemplate{{Key: "A", Name: "A", Rules: rules}}}, errNamingTemplateKeyInvalid},
		{"duplicate key", configurationdomain.NamingTemplateLibraryInput{DefaultKey: "a", Templates: []configurationdomain.NamingTemplate{{Key: "a", Name: "A", Rules: rules}, {Key: "a", Name: "B", Rules: rules}}}, errNamingTemplateKeyDuplicate},
		{"blank name", configurationdomain.NamingTemplateLibraryInput{DefaultKey: "a", Templates: []configurationdomain.NamingTemplate{{Key: "a", Name: "  ", Rules: rules}}}, errNamingTemplateNameInvalid},
		{"duplicate name", configurationdomain.NamingTemplateLibraryInput{DefaultKey: "a", Templates: []configurationdomain.NamingTemplate{{Key: "a", Name: "A", Rules: rules}, {Key: "b", Name: "A ", Rules: rules}}}, errNamingTemplateNameDuplicate},
		{"invalid body", configurationdomain.NamingTemplateLibraryInput{DefaultKey: "a", Templates: []configurationdomain.NamingTemplate{{Key: "a", Name: "A", Rules: invalid}}}, naming.ErrCodeNamingTargetInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := normalizeNamingTemplateLibrary(tc.input, map[string]map[string]string{})
			if got := namingTemplateErrorCode(err); got != tc.code {
				t.Fatalf("code = %q (%v), want %q", got, err, tc.code)
			}
		})
	}
}
