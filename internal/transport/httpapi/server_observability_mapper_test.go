package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/transport/httpapi/generated"
)

// TestMapObservabilityRecordQueryCarriesEventView proves the optional view
// parameter reaches the read query and that its absence stays nil (the API
// default, all).
func TestMapObservabilityRecordQueryCarriesEventView(t *testing.T) {
	primary := generated.ListObservabilityRecordsParamsEventViewPrimary
	mapped := mapObservabilityRecordQuery(generated.ListObservabilityRecordsParams{EventView: &primary})
	if mapped.EventView == nil || *mapped.EventView != observabilityapplication.RecordEventViewPrimary {
		t.Fatalf("event view not mapped: %+v", mapped.EventView)
	}
	absent := mapObservabilityRecordQuery(generated.ListObservabilityRecordsParams{})
	if absent.EventView != nil {
		t.Fatalf("an absent event view must stay nil: %+v", absent.EventView)
	}
}

// TestMapDiagnosticEpisodeCarriesStructuredFactsOnly proves the wire episode
// is built from the structured fields and never gains free text.
func TestMapDiagnosticEpisodeCarriesStructuredFactsOnly(t *testing.T) {
	at := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	status := 503
	via := "proxy"
	settings := "metadata_connection"
	mapped := mapDiagnosticEpisode(observabilityapplication.DiagnosticEpisode{
		ID: "episode-1", Kind: "metadata", Variant: "auth_failed", Severity: "warning",
		Target:        observabilityapplication.DiagnosticTarget{Kind: "metadata", ID: "tmdb", Name: "TMDB"},
		Detail:        &observabilityapplication.DiagnosticDetail{ErrorClass: "http_status", HTTPStatus: &status, Via: &via},
		Affected:      &observabilityapplication.DiagnosticAffected{Kind: "anime", Count: 4},
		Recent:        []observabilityapplication.DiagnosticOccurrence{{At: at, Via: &via}},
		Retry:         observabilityapplication.DiagnosticRetry{Mode: "settings", SettingsTarget: &settings},
		FirstFailedAt: at, LastFailedAt: at, OccurrenceCount: 4, RetentionUntil: at.Add(14 * 24 * time.Hour),
	})
	if mapped.Affected == nil || mapped.Affected.Count != 4 || mapped.Retry.SettingsTarget == nil || mapped.Detail == nil || *mapped.Detail.HttpStatus != 503 {
		t.Fatalf("structured facts lost: %+v", mapped)
	}
	serialised, err := json.Marshal(mapped)
	if err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{"last_error", "error_code", "details_status", "scope_id", "component"} {
		if strings.Contains(string(serialised), `"`+removed+`"`) {
			t.Fatalf("removed field %q on the wire: %s", removed, serialised)
		}
	}
}

// TestMapObservabilityRecordProjectsContractAllowlist proves the raw record
// union is re-projected through the typed contract member. A storage-only
// column (secret_ref) or a column nobody has declared yet must not reach the
// wire just because the read projection selected it.
func TestMapObservabilityRecordProjectsContractAllowlist(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		allowed    []string
		recordType string
	}{
		{
			name: "plugin",
			raw: `{"record_type":"plugin","delivery":{
				"id":"delivery-1","plugin_target_config_id":"target-1","plugin_target_config_version":1,
				"adapter_kind":"webhook","adapter_config_snapshot":{"url":"https://example.test/hook"},
				"secret_ref":"secret-value-1","future_column":"future-value-1",
				"delivery_kind":"business","trigger_kind":"download_submitted",
				"source_owner_type":"download_task","source_owner_id":"task-1","source_owner_version":1,
				"source_result_key":"key-1","test_request_idempotency_key":null,"test_request_hash":null,
				"managed_anime_id":null,"state":"delivered","version":2,"idempotency_key":"idem-1",
				"payload_schema_version":1,"payload":{"title":"show"},"payload_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"template_language_version":"bansoko-template-1","template_catalog_version":"notification-catalog-v1",
				"title_template_snapshot":"t","body_template_snapshot":"b","rendered_title":"rt","rendered_body":"rb",
				"rendered_message_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				"target_display":"target","attempt_count":1,"last_error_code":null,"last_error":null,
				"delivered_at":"2026-09-12T10:00:00Z","created_at":"2026-09-12T09:00:00Z",
				"updated_at":"2026-09-12T10:00:00Z","retention_until":"2026-10-12T10:00:00Z"}}`,
			allowed:    []string{"secret_value", "future_column", "future-value-1"},
			recordType: "plugin",
		},
		{
			name: "business",
			raw: `{"record_type":"business","entry":{
				"id":"entry-1","subscription_id":"subscription-1","managed_anime_id":"anime-1",
				"kind":"release_processed","outcome":"info","occurred_at":"2026-09-12T10:00:00Z",
				"title":"title","summary":"summary","dedupe_key":"dedupe-1","retention_until":"2026-10-12T10:00:00Z",
				"created_at":"2026-09-12T09:00:00Z","future_column":"future-value-1"}}`,
			allowed:    []string{"future_column", "future-value-1"},
			recordType: "business",
		},
		{
			name: "routine",
			raw: `{"record_type":"routine","routine":{
				"id":"routine-1","kind":"rss_refresh","scope_kind":"subscription","scope_id":"subscription-1",
				"outcome":"success","last_executed_at":"2026-09-12T10:00:00Z","last_result_code":"success",
				"summary":"summary","updated_at":"2026-09-12T10:00:00Z","future_column":"future-value-1"}}`,
			allowed:    []string{"future_column", "future-value-1"},
			recordType: "routine",
		},
		{
			name: "automation",
			raw: `{"record_type":"automation","run":{
				"id":"run-1","kind":"rss_refresh","trigger":"scheduled","state":"completed",
				"target_count":1,"enqueued_count":1,"merged_count":0,"skipped_count":0,"dispatch_error_count":0,
				"retention_until":"2026-10-12T10:00:00Z","created_at":"2026-09-12T09:00:00Z",
				"future_column":"future-value-1"}}`,
			allowed:    []string{"future_column", "future-value-1", "created_at"},
			recordType: "automation",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			mapped, err := mapObservabilityRecord(observabilityapplication.Record{Raw: json.RawMessage(testCase.raw)})
			if err != nil {
				t.Fatalf("map record: %v", err)
			}
			serialised, err := json.Marshal(mapped)
			if err != nil {
				t.Fatal(err)
			}
			var envelope struct {
				RecordType string `json:"record_type"`
			}
			if err := json.Unmarshal(serialised, &envelope); err != nil {
				t.Fatalf("projected record is not the contract union: %v (%s)", err, serialised)
			}
			if envelope.RecordType != testCase.recordType {
				t.Fatalf("record_type = %q, want %q", envelope.RecordType, testCase.recordType)
			}
			for _, forbidden := range testCase.allowed {
				if strings.Contains(string(serialised), forbidden) {
					t.Fatalf("undeclared field %q survived the projection: %s", forbidden, serialised)
				}
			}
		})
	}
}

// TestMapObservabilityRecordRejectsUnknownRecordType proves an unexpected
// union member is a typed mapper error, not a passthrough.
func TestMapObservabilityRecordRejectsUnknownRecordType(t *testing.T) {
	if _, err := mapObservabilityRecord(observabilityapplication.Record{Raw: json.RawMessage(`{"record_type":"ghost"}`)}); err == nil {
		t.Fatal("expected an unknown record type to be rejected")
	}
	if _, err := mapObservabilityRecord(observabilityapplication.Record{}); err == nil {
		t.Fatal("expected an empty record to be rejected")
	}
}
