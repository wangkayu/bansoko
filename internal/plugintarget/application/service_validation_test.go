package application

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"bansoko/internal/apperrors"
)

func telegramConfig(t *testing.T, chatID string) json.RawMessage {
	t.Helper()
	return json.RawMessage(`{"adapter_kind":"telegram","chat_id":"` + chatID + `"}`)
}

func webhookConfig(t *testing.T, authKind string) json.RawMessage {
	t.Helper()
	return json.RawMessage(`{"adapter_kind":"webhook","auth_kind":"` + authKind + `"}`)
}

func defaultTemplateSet(t *testing.T) json.RawMessage {
	t.Helper()
	return json.RawMessage(`{"language_version":"` + templateLanguageVersion + `","catalog_version":"` + templateCatalogVersion + `","events":{"download_submitted":{"mode":"system_default"},"file_operation_completed":{"mode":"system_default"},"file_operation_failed":{"mode":"system_default"}}}`)
}

func validCreateInput(t *testing.T) TargetInput {
	t.Helper()
	return TargetInput{
		AdapterKind:         AdapterTelegram,
		Config:              telegramConfig(t, "12345"),
		ConfigSchemaVersion: 1,
		Name:                "My Target",
		PluginKind:          PluginKindNotification,
		Secret:              SecretInput{Mode: "none"},
		TemplateSet:         defaultTemplateSet(t),
		TriggerKinds:        []TriggerKind{TriggerDownloadSubmitted, TriggerFileOperationCompleted, TriggerFileOperationFailed},
	}
}

func TestValidateTargetCommonValid(t *testing.T) {
	input := validCreateInput(t)
	if err := validateTargetCommon(input.PluginKind, input.AdapterKind, input.ConfigSchemaVersion, input.Config, input.TemplateSet, input.TriggerKinds, input.Name); err != nil {
		t.Fatalf("expected valid input to pass, got %v", err)
	}
}

func TestValidateTargetCommonRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*TargetInput)
		want   string
	}{
		{"wrong plugin kind", func(in *TargetInput) { in.PluginKind = "other" }, "plugin_kind"},
		{"empty name", func(in *TargetInput) { in.Name = "  " }, "name"},
		{"zero config schema version", func(in *TargetInput) { in.ConfigSchemaVersion = 0 }, "config_schema_version"},
		{"telegram missing chat_id", func(in *TargetInput) { in.Config = telegramConfig(t, "") }, "chat_id"},
		{"webhook missing auth_kind", func(in *TargetInput) {
			in.AdapterKind = AdapterWebhook
			in.Config = webhookConfig(t, "")
		}, "auth_kind"},
		{"unknown adapter kind", func(in *TargetInput) { in.AdapterKind = "sms" }, "adapter_kind"},
		{"bad language version", func(in *TargetInput) { in.TemplateSet = replaceTemplateField(t, "language_version", "other") }, "language_version"},
		{"bad catalog version", func(in *TargetInput) { in.TemplateSet = replaceTemplateField(t, "catalog_version", "other") }, "catalog_version"},
		{"unknown trigger kind", func(in *TargetInput) { in.TriggerKinds = []TriggerKind{"other"} }, "not allowlisted"},
		{"duplicate trigger kinds", func(in *TargetInput) {
			in.TriggerKinds = []TriggerKind{TriggerDownloadSubmitted, TriggerDownloadSubmitted}
		}, "unique"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := validCreateInput(t)
			tc.mutate(&input)
			err := validateTargetCommon(input.PluginKind, input.AdapterKind, input.ConfigSchemaVersion, input.Config, input.TemplateSet, input.TriggerKinds, input.Name)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			var validation *apperrors.Validation
			if !errors.As(err, &validation) {
				t.Fatalf("expected apperrors.Validation, got %T: %v", err, err)
			}
			if !strings.Contains(validation.Error()+" "+detailMessages(validation), tc.want) {
				t.Fatalf("expected error to mention %q, got code=%s details=%s", tc.want, validation.Code, detailMessages(validation))
			}
		})
	}
}

func TestValidateEventTemplateCustom(t *testing.T) {
	event := json.RawMessage(`{"mode":"custom","title_template":"title","body_template":"body"}`)
	if err := validateEventTemplate("template_set.events.download_submitted", event); err != nil {
		t.Fatalf("expected valid custom template, got %v", err)
	}
	empty := json.RawMessage(`{"mode":"custom","title_template":"  ","body_template":"body"}`)
	if err := validateEventTemplate("template_set.events.download_submitted", empty); err == nil {
		t.Fatal("expected empty custom title template to fail")
	}
}

func TestValidateIdempotencyKey(t *testing.T) {
	if err := validateIdempotencyKey("short"); err == nil {
		t.Fatal("expected short key to fail")
	}
	if err := validateIdempotencyKey("a-valid-idempotency-key"); err != nil {
		t.Fatalf("expected valid key, got %v", err)
	}
	if err := validateIdempotencyKey(strings.Repeat("k", 129)); err == nil {
		t.Fatal("expected over-long key to fail")
	}
}

func TestHashTestRequestStable(t *testing.T) {
	body := canonicalTestBody(TriggerDownloadSubmitted, 3)
	first := hashTestRequest(body)
	second := hashTestRequest(canonicalTestBody(TriggerDownloadSubmitted, 3))
	if first != second {
		t.Fatalf("expected stable hash, got %s vs %s", first, second)
	}
	if !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("expected sha256 prefix, got %s", first)
	}
	if hashTestRequest(canonicalTestBody(TriggerFileOperationFailed, 3)) == first {
		t.Fatal("expected different trigger to produce a different hash")
	}
}

func TestSelectTemplateSystemDefault(t *testing.T) {
	title, body, err := selectTemplate(defaultTemplateSet(t), TriggerDownloadSubmitted)
	if err != nil {
		t.Fatalf("expected system default template, got %v", err)
	}
	if strings.TrimSpace(title) == "" || strings.TrimSpace(body) == "" {
		t.Fatalf("expected non-empty default template, got title=%q body=%q", title, body)
	}
}

func TestSelectTemplateCustom(t *testing.T) {
	templateSet := json.RawMessage(`{"language_version":"` + templateLanguageVersion + `","catalog_version":"` + templateCatalogVersion + `","events":{"download_submitted":{"mode":"system_default"},"file_operation_completed":{"mode":"custom","title_template":"custom title","body_template":"custom body"},"file_operation_failed":{"mode":"system_default"}}}`)
	title, body, err := selectTemplate(templateSet, TriggerFileOperationCompleted)
	if err != nil {
		t.Fatalf("expected custom template, got %v", err)
	}
	if title != "custom title" || body != "custom body" {
		t.Fatalf("expected custom template values, got title=%q body=%q", title, body)
	}
}

func replaceTemplateField(t *testing.T, field, value string) json.RawMessage {
	t.Helper()
	var template map[string]any
	if err := json.Unmarshal(defaultTemplateSet(t), &template); err != nil {
		t.Fatal(err)
	}
	template[field] = value
	raw, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func detailMessages(validation *apperrors.Validation) string {
	var builder strings.Builder
	for _, detail := range validation.Details {
		builder.WriteString(detail.Code)
		builder.WriteString(":")
		builder.WriteString(detail.Message)
		builder.WriteString(";")
	}
	return builder.String()
}

// Save compiles custom templates with the delivery renderer: an invalid one is a
// typed 422 carrying the same source spans preview reports.
func TestValidateTargetCommonRejectsUncompilableTemplate(t *testing.T) {
	input := validCreateInput(t)
	input.TemplateSet = json.RawMessage(`{"language_version":"` + templateLanguageVersion + `","catalog_version":"` + templateCatalogVersion + `","events":{"download_submitted":{"mode":"system_default"},"file_operation_completed":{"mode":"custom","title_template":"完成 {{anime.nope}}","body_template":"{{coalesce(release.title, out.name)}}"},"file_operation_failed":{"mode":"system_default"}}}`)
	err := validateTargetCommon(input.PluginKind, input.AdapterKind, input.ConfigSchemaVersion, input.Config, input.TemplateSet, input.TriggerKinds, input.Name)
	var validation *apperrors.Validation
	if !errors.As(err, &validation) || validation.Code != "TEMPLATE_INVALID" || len(validation.Details) != 1 {
		t.Fatalf("err=%v", err)
	}
	detail := validation.Details[0]
	if detail.Field == nil || *detail.Field != "template_set.events.file_operation_completed.title_template" || detail.Code != "invalid_expression" {
		t.Fatalf("detail=%+v field=%v", detail, detail.Field)
	}
	if detail.Start == nil || detail.End == nil || *detail.Start != len("完成 ") || *detail.End != len("完成 {{anime.nope}}") {
		t.Fatalf("span=%v..%v", detail.Start, detail.End)
	}
	// A variable from another trigger's catalog is unknown here as well.
	input.TemplateSet = json.RawMessage(strings.Replace(string(input.TemplateSet), "{{anime.nope}}", "{{download.task}}", 1))
	if err := validateTargetCommon(input.PluginKind, input.AdapterKind, input.ConfigSchemaVersion, input.Config, input.TemplateSet, input.TriggerKinds, input.Name); !errors.As(err, &validation) {
		t.Fatalf("cross-trigger variable accepted: %v", err)
	}
}
