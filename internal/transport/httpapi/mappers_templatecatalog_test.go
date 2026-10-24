package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	templatecatalogapplication "bansoko/internal/templatecatalog/application"
	"bansoko/internal/transport/httpapi/generated"
)

func TestMapNotificationTemplateCatalogPreservesContractAndIsolation(t *testing.T) {
	catalog := templatecatalogapplication.NotificationTemplateCatalog{
		CatalogVersion: templatecatalogapplication.NotificationTemplateCatalogCatalogVersionNotificationCatalogV1,
		DefaultTemplate: templatecatalogapplication.NotificationEventTemplateCustom{
			BodyTemplate:  "body",
			Mode:          templatecatalogapplication.NotificationEventTemplateCustomModeCustom,
			TitleTemplate: "title",
		},
		Functions:       []templatecatalogapplication.TemplateFunction{{Arguments: []string{"value"}, Description: "desc", Name: "upper"}},
		LanguageVersion: templatecatalogapplication.NotificationTemplateCatalogLanguageVersionBansokoTemplate1,
		Sample: templatecatalogapplication.TemplateSampleFacts{
			Values: map[string]interface{}{"nested": map[string]interface{}{"value": "redacted"}},
		},
		TriggerKind: templatecatalogapplication.NotificationTriggerKindDownloadSubmitted,
		Variables: []templatecatalogapplication.TemplateVariable{{
			Description: stringPointer("description"),
			Label:       "label",
			Name:        "anime.title",
			Nullable:    true,
			SampleValue: nil,
			Source:      "anime",
			Type:        templatecatalogapplication.TemplateVariableTypeString,
		}},
	}

	mapped := mapNotificationTemplateCatalog(catalog)
	if mapped.CatalogVersion != "notification-catalog-v1" || mapped.LanguageVersion != "bansoko-template-1" || mapped.TriggerKind != "download_submitted" {
		t.Fatalf("mapped versions/trigger = %#v", mapped)
	}
	if mapped.Variables[0].SampleValue != nil || !mapped.Variables[0].Nullable {
		t.Fatalf("nullable variable = %#v", mapped.Variables[0])
	}
	mapped.Functions[0].Arguments[0] = "changed"
	mapped.Sample.Values["nested"].(map[string]interface{})["value"] = "changed"
	if catalog.Functions[0].Arguments[0] != "value" {
		t.Fatal("function arguments alias application input")
	}
	if catalog.Sample.Values["nested"].(map[string]interface{})["value"] != "redacted" {
		t.Fatal("sample values alias application input")
	}

	body, err := json.Marshal(mapped)
	if err != nil {
		t.Fatalf("marshal mapped catalog: %v", err)
	}
	text := string(body)
	for _, field := range []string{`"catalog_version":"notification-catalog-v1"`, `"language_version":"bansoko-template-1"`, `"trigger_kind":"download_submitted"`, `"sample_value":null`} {
		if !strings.Contains(text, field) {
			t.Errorf("mapped JSON missing %s: %s", field, text)
		}
	}
}

func TestMapNotificationTemplatePreviewPreservesErrorsAndWarnings(t *testing.T) {
	field := templatecatalogapplication.TemplateSourceErrorFieldTitleTemplate
	preview := templatecatalogapplication.NotificationTemplatePreview{
		Body:            "body",
		CatalogVersion:  templatecatalogapplication.NotificationTemplatePreviewCatalogVersionNotificationCatalogV1,
		Errors:          []templatecatalogapplication.TemplateSourceError{{Code: "invalid_expression", End: 12, Field: &field, Message: "bad", Start: 2}},
		LanguageVersion: templatecatalogapplication.NotificationTemplatePreviewLanguageVersionBansokoTemplate1,
		Title:           "title",
		Valid:           false,
		Warnings:        []templatecatalogapplication.TemplateWarning{{Code: "null_variable", Message: "null", Variable: stringPointer("anime.title_override")}},
	}

	mapped := mapNotificationTemplatePreview(preview)
	if mapped.Errors[0].Field == nil || *mapped.Errors[0].Field != "title_template" {
		t.Fatalf("error field = %#v", mapped.Errors[0].Field)
	}
	if mapped.Errors[0].Start != 2 || mapped.Errors[0].End != 12 {
		t.Fatalf("error span = [%d,%d]", mapped.Errors[0].Start, mapped.Errors[0].End)
	}
	if mapped.Warnings[0].Variable == nil || *mapped.Warnings[0].Variable != "anime.title_override" {
		t.Fatalf("warning variable = %#v", mapped.Warnings[0].Variable)
	}
}

func TestMapNamingTemplateCatalogPreservesNullablePresetShape(t *testing.T) {
	catalog := templatecatalogapplication.NamingTemplateCatalog{
		Fields: []templatecatalogapplication.NamingCatalogField{{
			Description: stringPointer("description"),
			Label:       nil,
			Nullable:    true,
			Path:        "Anime.Title",
			SampleValue: nil,
			Source:      "anime",
			Type:        templatecatalogapplication.NamingCatalogFieldTypeString,
		}},
		Functions: []templatecatalogapplication.NamingCatalogFunction{{
			Arguments:   []string{"value"},
			Description: "description",
			Example:     nil,
			Name:        "upper",
			Signature:   "upper(value) string",
		}},
		Samples: []templatecatalogapplication.NamingCatalogSample{{Description: nil, Label: "sample", Ref: "sample-tv"}},
	}

	mapped := mapNamingTemplateCatalog(catalog)
	if mapped.Fields[0].Label != nil || mapped.Fields[0].SampleValue != nil {
		t.Fatalf("nullable field pointers = %#v", mapped.Fields[0])
	}
	if mapped.Functions[0].Example != nil || mapped.Samples[0].Description != nil {
		t.Fatalf("optional pointers = %#v / %#v", mapped.Functions[0], mapped.Samples[0])
	}
	body, err := json.Marshal(mapped)
	if err != nil {
		t.Fatalf("marshal mapped naming catalog: %v", err)
	}
	text := string(body)
	for _, field := range []string{`"path":"Anime.Title"`, `"nullable":true`} {
		if !strings.Contains(text, field) {
			t.Errorf("mapped JSON missing %s: %s", field, text)
		}
	}
}

func TestMapNamingTemplateCatalogCarriesShorthands(t *testing.T) {
	// The catalog's shorthand list is the compiler's table; the wire shape must
	// carry every member and stay a non-null array when it is empty.
	mapped := mapNamingTemplateCatalog(templatecatalogapplication.NamingTemplateCatalog{
		Shorthands: []templatecatalogapplication.NamingCatalogShorthand{
			{Name: "s00e00", Expansion: `"S" + pad(anime.season, 2)`, Example: "S01E12"},
		},
	})
	if len(mapped.Shorthands) != 1 || mapped.Shorthands[0].Name != "s00e00" || mapped.Shorthands[0].Expansion != `"S" + pad(anime.season, 2)` || mapped.Shorthands[0].Example != "S01E12" {
		t.Fatalf("shorthands = %#v", mapped.Shorthands)
	}
	body, err := json.Marshal(mapped)
	if err != nil {
		t.Fatalf("marshal mapped naming catalog: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode mapped naming catalog: %v", err)
	}
	entries, ok := raw["shorthands"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("shorthand wire shape = %s", body)
	}
	entry, ok := entries[0].(map[string]any)
	if !ok || entry["name"] != "s00e00" || entry["expansion"] != `"S" + pad(anime.season, 2)` || entry["example"] != "S01E12" {
		t.Fatalf("shorthand wire entry = %#v", entries[0])
	}
	// An empty shorthand set must still serialize as a non-null array: the
	// schema requires the member, and the editor treats null as a load failure.
	emptyBody, err := json.Marshal(mapNamingTemplateCatalog(templatecatalogapplication.NamingTemplateCatalog{}))
	if err != nil {
		t.Fatalf("marshal empty naming catalog: %v", err)
	}
	if !strings.Contains(string(emptyBody), `"shorthands":[]`) {
		t.Fatalf("empty shorthand wire shape = %s, want an empty array", emptyBody)
	}
}

func TestMapNamingTemplateCatalogKeepsRequiredVarsKeysArray(t *testing.T) {
	// Regression: the mapper dropped vars_keys entirely, so the required array
	// serialized as null and the naming rules tab crashed on catalog.vars_keys.join.
	loadedAt := time.Date(2026, 9, 24, 1, 2, 3, 4, time.UTC)
	mapped := mapNamingTemplateCatalog(templatecatalogapplication.NamingTemplateCatalog{VarsLoadedAt: &loadedAt})
	if mapped.VarsKeys == nil {
		t.Fatal("empty var key set mapped to nil; schema requires a non-nullable array")
	}
	body, err := json.Marshal(mapped)
	if err != nil {
		t.Fatalf("marshal mapped naming catalog: %v", err)
	}
	if !strings.Contains(string(body), `"vars_keys":[]`) {
		t.Fatalf("vars_keys wire shape = %s, want an empty array", body)
	}
	if mapped.VarsLoadedAt == nil || !mapped.VarsLoadedAt.Equal(loadedAt) {
		t.Fatalf("vars_loaded_at = %v, want %v", mapped.VarsLoadedAt, loadedAt)
	}

	populated := mapNamingTemplateCatalog(templatecatalogapplication.NamingTemplateCatalog{VarsKeys: []string{"group", "season"}})
	if len(populated.VarsKeys) != 2 || populated.VarsKeys[0] != "group" || populated.VarsKeys[1] != "season" {
		t.Fatalf("var keys = %#v, want the loaded keys in order", populated.VarsKeys)
	}
}

func TestMapTemplatePreviewInputsPreservesValues(t *testing.T) {
	sampleRef := "sample-tv"
	namingInput := mapNamingTemplatePreviewInput(generated.NamingTemplatePreviewInput{
		SampleRef:      &sampleRef,
		TemplateKind:   generated.NamingTemplatePreviewInputTemplateKindSubtitleSuffix,
		TemplateSource: "source",
	})
	if namingInput.SampleRef == nil || *namingInput.SampleRef != sampleRef || namingInput.TemplateKind != "subtitle_suffix" || namingInput.TemplateSource != "source" {
		t.Fatalf("naming input = %#v", namingInput)
	}
}

func stringPointer(value string) *string { return &value }
