package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/transport/httpapi/generated"
)

// fakeObservabilityService lets the transport be exercised without a database.
type fakeObservabilityService struct {
	records     observabilityapplication.RecordPage
	diagnostics observabilityapplication.DiagnosticPage
	err         error
}

func (f fakeObservabilityService) ListRecords(context.Context, observabilityapplication.RecordQuery) (observabilityapplication.RecordPage, error) {
	return f.records, f.err
}

func (f fakeObservabilityService) ListDiagnostics(context.Context, observabilityapplication.DiagnosticQuery) (observabilityapplication.DiagnosticPage, error) {
	return f.diagnostics, f.err
}

// pluginRecordRaw is a contract-shaped plugin record carrying two fields the
// contract does not declare: the storage-only secret reference that used to be
// selected by `to_jsonb(plugin_deliveries.*)`, and an undeclared column a
// future migration could add.
const pluginRecordRaw = `{"record_type":"plugin","delivery":{
	"id":"delivery-1","plugin_target_config_id":"target-1","plugin_target_config_version":1,
	"adapter_kind":"webhook","adapter_config_snapshot":{"adapter_kind":"webhook","auth_kind":"none"},
	"secret_ref":"secret-ref-value","future_storage_column":"future-storage-value",
	"delivery_kind":"business","trigger_kind":"download_submitted",
	"source_owner_type":"download_task","source_owner_id":"task-1","source_owner_version":1,
	"source_result_key":"result-1","managed_anime_id":null,"state":"failed","version":2,
	"idempotency_key":"idem-1","payload_schema_version":1,
	"payload":{"schema_version":1,"trigger_kind":"download_submitted","facts":{}},
	"payload_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	"template_language_version":"bansoko-template-1","template_catalog_version":"notification-catalog-v1",
	"title_template_snapshot":"title","body_template_snapshot":"body",
	"target_display":"Target","attempt_count":1,
	"created_at":"2026-09-12T10:00:00Z","updated_at":"2026-09-12T10:00:00Z",
	"payload_status":"available","config_status":"available","retention_until":"2026-10-12T10:00:00Z"}}`

// TestObservabilityRecordsEndpointDropsUndeclaredFields proves the global
// records endpoint cannot serve a storage-only or undeclared field even when
// the read projection hands one to the transport. The record union is a raw
// JSON passthrough, so without the contract projection in mapObservabilityRecord
// the response body would contain secret_ref verbatim.
func TestObservabilityRecordsEndpointDropsUndeclaredFields(t *testing.T) {
	server := &Server{deps: Dependencies{Observability: fakeObservabilityService{
		records: observabilityapplication.RecordPage{
			Records: []observabilityapplication.Record{{Raw: json.RawMessage(pluginRecordRaw)}},
		},
	}}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/observability/records?kind=plugin", nil)
	server.ListObservabilityRecords(recorder, request, generated.ListObservabilityRecordsParams{})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.Bytes()
	for _, forbidden := range []string{"secret_ref", "secret-ref-value", "future_storage_column", "future-storage-value"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(string(body), `"delivery-1"`) {
		t.Fatalf("the plugin record itself must survive: %s", body)
	}
	validateAgainstOpenAPI(t, "ObservabilityRecordPage", body)
}

// TestObservabilityDiagnosticsEndpointSatisfiesContract proves the diagnostics
// response is additionalProperties:false compliant for an open episode with
// structured detail, a recovered one without, and the internal summary.
func TestObservabilityDiagnosticsEndpointSatisfiesContract(t *testing.T) {
	occurredAt := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	status := 503
	via := "proxy"
	host := "api.example.test"
	recoveredAt := occurredAt.Add(time.Hour)
	requestID := "request-1"
	server := &Server{deps: Dependencies{Observability: fakeObservabilityService{
		diagnostics: observabilityapplication.DiagnosticPage{
			Episodes: []observabilityapplication.DiagnosticEpisode{
				{
					ID: "episode-open", Kind: "metadata", Variant: "unreachable", Severity: "warning",
					Target:        observabilityapplication.DiagnosticTarget{Kind: "metadata", ID: "tmdb", Name: "TMDB", Host: &host},
					Detail:        &observabilityapplication.DiagnosticDetail{ErrorClass: "http_status", HTTPStatus: &status, Via: &via},
					Affected:      &observabilityapplication.DiagnosticAffected{Kind: "anime", Count: 3},
					Recent:        []observabilityapplication.DiagnosticOccurrence{{At: occurredAt, Via: &via, Host: &host}},
					FirstFailedAt: occurredAt, LastFailedAt: occurredAt, OccurrenceCount: 3,
					Retry:          observabilityapplication.DiagnosticRetry{Mode: "auto"},
					RetentionUntil: occurredAt.Add(14 * 24 * time.Hour),
				},
				{
					ID: "episode-recovered", Kind: "update", Variant: "unreachable", Severity: "warning",
					Target:        observabilityapplication.DiagnosticTarget{Kind: "update", ID: "update-index", Name: "Update"},
					FirstFailedAt: occurredAt, LastFailedAt: occurredAt, SucceededAfterAt: &recoveredAt, OccurrenceCount: 1,
					Retry:          observabilityapplication.DiagnosticRetry{Mode: "manual"},
					RetentionUntil: occurredAt.Add(14 * 24 * time.Hour),
				},
			},
			Internal: &observabilityapplication.DiagnosticInternal{OccurrenceCount: 2, LastFailedAt: occurredAt, LastRequestID: &requestID},
		},
	}}}
	recorder := httptest.NewRecorder()
	request := requestWithRuntimeLogger("diagnostic-contract", &panicRuntimeLogger{})
	server.ListObservabilityDiagnostics(recorder, request, generated.ListObservabilityDiagnosticsParams{})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.Bytes()
	for _, forbidden := range []string{"last_error", "error_code", "details_status"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("removed field %q is back in the response: %s", forbidden, body)
		}
	}
	validateAgainstOpenAPI(t, "DiagnosticEpisodePage", body)
}
