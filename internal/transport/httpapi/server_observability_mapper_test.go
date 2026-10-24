package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/transport/httpapi/generated"
)

// TestMapDiagnosticEpisodeDetailsStatus proves the optional details payload is
// always accompanied by an honest bounded status: decoded details are
// available, absent details are unavailable, and stored details that no longer
// decode into the contract shape are reported invalid — never silently
// dropped, never serialised raw — with a bounded runtime event recorded.
func TestMapDiagnosticEpisodeDetailsStatus(t *testing.T) {
	occurredAt := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	base := observabilityapplication.DiagnosticEpisode{
		ID: "episode-1", Severity: "error", Component: "rss", Operation: "refresh",
		ErrorCode: "rss_refresh_failed", ScopeKind: "subscription", ScopeID: "subscription-1",
		FirstFailedAt: occurredAt, LastFailedAt: occurredAt, OccurrenceCount: 2,
		LastError: "refresh failed", RetentionUntil: occurredAt.Add(30 * 24 * time.Hour),
	}

	t.Run("absent details are unavailable", func(t *testing.T) {
		logger := &captureRuntimeLogger{}
		mapped := mapDiagnosticEpisode(requestWithRuntimeLogger("req-unavailable", logger).Context(), base)
		if mapped.DetailsStatus != generated.DiagnosticDetailsStatusUnavailable {
			t.Fatalf("details_status = %q, want unavailable", mapped.DetailsStatus)
		}
		if mapped.Details != nil {
			t.Fatalf("details must stay nil, got %+v", mapped.Details)
		}
		if len(logger.events) != 0 {
			t.Fatalf("absent details must not log, got %+v", logger.events)
		}
	})

	t.Run("decodable details are available", func(t *testing.T) {
		episode := base
		episode.Details = &observabilityapplication.DiagnosticDetails{
			Raw: json.RawMessage(`{"reason_code":"http_403","reason_detail":"provider rejected the request"}`),
		}
		logger := &captureRuntimeLogger{}
		mapped := mapDiagnosticEpisode(requestWithRuntimeLogger("req-available", logger).Context(), episode)
		if mapped.DetailsStatus != generated.DiagnosticDetailsStatusAvailable {
			t.Fatalf("details_status = %q, want available", mapped.DetailsStatus)
		}
		if mapped.Details == nil || mapped.Details.ReasonCode == nil || *mapped.Details.ReasonCode != "http_403" {
			t.Fatalf("details must survive decoding, got %+v", mapped.Details)
		}
		if len(logger.events) != 0 {
			t.Fatalf("decodable details must not log, got %+v", logger.events)
		}
	})

	t.Run("undecodable details are invalid and withheld", func(t *testing.T) {
		const rawDetails = `{"reason_code": 403, "safe_context": "not-an-object"}`
		episode := base
		episode.Details = &observabilityapplication.DiagnosticDetails{Raw: json.RawMessage(rawDetails)}
		logger := &captureRuntimeLogger{}
		mapped := mapDiagnosticEpisode(requestWithRuntimeLogger("req-invalid", logger).Context(), episode)
		if mapped.DetailsStatus != generated.DiagnosticDetailsStatusInvalid {
			t.Fatalf("details_status = %q, want invalid", mapped.DetailsStatus)
		}
		if mapped.Details != nil {
			t.Fatalf("invalid details must be withheld, got %+v", mapped.Details)
		}

		serialised, err := json.Marshal(mapped)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(serialised), "not-an-object") || strings.Contains(string(serialised), `"reason_code":403`) {
			t.Fatalf("raw details leaked into the contract response: %s", serialised)
		}

		if len(logger.events) != 1 {
			t.Fatalf("runtime events = %d, want 1", len(logger.events))
		}
		event := logger.events[0]
		if event.ReasonCode != "diagnostic_details_invalid" || event.ErrorClass != "diagnostic_details_decode_error" {
			t.Fatalf("runtime event = %+v, want bounded decode evidence", event)
		}
		if strings.Contains(event.Message, "not-an-object") {
			t.Fatalf("runtime event leaked raw details: %+v", event)
		}
	})
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
