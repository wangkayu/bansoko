package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	configurationpersistence "bansoko/internal/configuration/adapter/persistence"
	configurationapplication "bansoko/internal/configuration/application"
	observabilityapplication "bansoko/internal/observability/application"
	plugintargetapplication "bansoko/internal/plugintarget/application"
	deliverypayload "bansoko/internal/plugintarget/payload"
	"bansoko/internal/workitems"
)

// These tests run the real HTTP handlers over a disposable PostgreSQL and
// validate the responses against api/openapi.yaml. They exist because the
// records endpoint serves a raw JSON union: whatever the read projection
// selects reaches the client verbatim, so a storage-only column
// (plugin_deliveries.secret_ref) or a column added later by a migration
// would leak without any mapper change.

// withObservabilityContractDeps wires the observability read service and the
// plugin target service onto the shared rig database.
func withObservabilityContractDeps(t *testing.T) rigOption {
	t.Helper()
	return func(deps *Dependencies) {
		observability, err := observabilityapplication.NewService(deps.DB)
		if err != nil {
			t.Fatalf("new observability service: %v", err)
		}
		deps.Observability = observability

		workStore, err := workitems.NewStore(deps.DB, 0, nil)
		if err != nil {
			t.Fatalf("new work item store: %v", err)
		}
		secrets, err := configurationapplication.NewDatabaseSecretStore(
			deps.DB, []byte("test-secret-encryption-key-0123456789abcdef"), configurationpersistence.NewRepository(deps.DB))
		if err != nil {
			t.Fatalf("new secret store: %v", err)
		}
		pluginTargets, err := plugintargetapplication.NewService(deps.DB, secrets, workStore)
		if err != nil {
			t.Fatalf("new plugin target service: %v", err)
		}
		deps.PluginTargets = pluginTargets
	}
}

// TestUnknownHTTP500PersistsDiagnosticEpisodeOverPostgres drives writeError
// through the common HTTP middleware with the real PostgreSQL observability
// recorder, then reads the committed episode through both DB and HTTP.
func TestUnknownHTTP500PersistsDiagnosticEpisodeOverPostgres(t *testing.T) {
	var observability *observabilityapplication.Service
	rig := newTestRig(t, "development", true, func(deps *Dependencies) {
		var err error
		observability, err = observabilityapplication.NewService(deps.DB)
		if err != nil {
			t.Fatalf("new observability service: %v", err)
		}
		deps.Observability = observability
		deps.DiagnosticRecorder = observability
	})
	if observability == nil {
		t.Fatal("observability recorder was not initialized")
	}

	const (
		secretValue = "super-secret-token"
		privatePath = "/srv/app/private/data"
		dsnValue    = "postgres://db-user:db-password@db.internal:5432/mikan"
		firstID     = "http-unknown-pg-1"
		secondID    = "http-unknown-pg-2"
	)

	server, err := NewServer(Dependencies{
		DB: rig.db, Access: rig.access, Observability: observability,
		DiagnosticRecorder: observability, Environment: "development", SkipAuth: true,
	})
	if err != nil {
		t.Fatalf("new HTTP test server: %v", err)
	}
	handler := server.withCommonMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/test/unknown":
			writeError(w, r, fmt.Errorf(
				"unknown application fault request_id=%s token=%s path=%s dsn=%s",
				RequestIDFrom(r.Context()), secretValue, privatePath, dsnValue,
			))
		case "/test/known-503":
			writeError(w, r, unavailable("DEPENDENCY_UNAVAILABLE", "The dependency is unavailable."))
		default:
			http.NotFound(w, r)
		}
	}))

	doRequest := func(path, requestID string) (*httptest.ResponseRecorder, []byte) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("X-Request-ID", requestID)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response, response.Body.Bytes()
	}
	assertGeneric500 := func(response *httptest.ResponseRecorder, body []byte, requestID string) {
		t.Helper()
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500; body=%s", response.Code, body)
		}
		if response.Header().Get("X-Request-ID") != requestID {
			t.Fatalf("response request ID = %q, want %q", response.Header().Get("X-Request-ID"), requestID)
		}
		var errorBody struct {
			Code      string  `json:"code"`
			Message   string  `json:"message"`
			RequestID *string `json:"request_id"`
		}
		if err := json.Unmarshal(body, &errorBody); err != nil {
			t.Fatalf("decode generic error: %v; body=%s", err, body)
		}
		if errorBody.Code != "INTERNAL" || errorBody.Message != "The request failed." ||
			errorBody.RequestID == nil || *errorBody.RequestID != requestID {
			t.Fatalf("generic error = %+v, want INTERNAL with request ID %q", errorBody, requestID)
		}
		for _, forbidden := range []string{secretValue, privatePath, dsnValue, "db-password"} {
			if bytes.Contains(body, []byte(forbidden)) {
				t.Fatalf("generic error leaked %q: %s", forbidden, body)
			}
		}
	}

	firstResponse, firstBody := doRequest("/test/unknown", firstID)
	assertGeneric500(firstResponse, firstBody, firstID)
	secondResponse, secondBody := doRequest("/test/unknown", secondID)
	assertGeneric500(secondResponse, secondBody, secondID)

	ctx := t.Context()
	readEpisode := func() (int, int) {
		t.Helper()
		var episodeCount, occurrenceCount int
		if err := rig.db.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(MAX(occurrence_count), 0)
FROM diagnostic_episodes
WHERE kind = 'internal' AND target_id = 'http.5xx'`).Scan(&episodeCount, &occurrenceCount); err != nil {
			t.Fatalf("read HTTP diagnostic episode: %v", err)
		}
		return episodeCount, occurrenceCount
	}
	episodeCount, occurrenceCount := readEpisode()
	if episodeCount != 1 || occurrenceCount != 2 {
		t.Fatalf("HTTP diagnostic rows before typed 503 = %d, occurrence_count = %d; want 1/2", episodeCount, occurrenceCount)
	}

	knownResponse, knownBody := doRequest("/test/known-503", "http-known-pg-503")
	if knownResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("known typed 503 status = %d, want 503; body=%s", knownResponse.Code, knownBody)
	}
	if knownResponse.Header().Get("X-Request-ID") != "http-known-pg-503" {
		t.Fatalf("known 503 request ID = %q, want exact ID", knownResponse.Header().Get("X-Request-ID"))
	}
	episodeCount, occurrenceCount = readEpisode()
	if episodeCount != 1 || occurrenceCount != 2 {
		t.Fatalf("HTTP diagnostic rows after typed 503 = %d, occurrence_count = %d; want unchanged 1/2", episodeCount, occurrenceCount)
	}

	diagnosticsResponse, diagnosticsBody := rig.do(t, http.MethodGet,
		"/api/v1/observability/diagnostics?limit=20", nil, nil)
	if diagnosticsResponse.Code != http.StatusOK {
		t.Fatalf("diagnostics status = %d; body=%s", diagnosticsResponse.Code, diagnosticsBody)
	}
	validateAgainstOpenAPI(t, "DiagnosticEpisodePage", diagnosticsBody)
	var page struct {
		Episodes []json.RawMessage `json:"episodes"`
		Internal *struct {
			OccurrenceCount int     `json:"occurrence_count"`
			LastRequestID   *string `json:"last_request_id"`
		} `json:"internal"`
	}
	if err := json.Unmarshal(diagnosticsBody, &page); err != nil {
		t.Fatalf("decode diagnostics page: %v; body=%s", err, diagnosticsBody)
	}
	if len(page.Episodes) != 0 {
		t.Fatalf("an internal error must not be listed as an episode: %s", diagnosticsBody)
	}
	if page.Internal == nil || page.Internal.OccurrenceCount != 2 || page.Internal.LastRequestID == nil || *page.Internal.LastRequestID != secondID {
		t.Fatalf("internal summary = %+v, want two occurrences and the latest request id: %s", page.Internal, diagnosticsBody)
	}
	for _, forbidden := range []string{firstID, secretValue, privatePath, dsnValue, "db-password"} {
		if bytes.Contains(diagnosticsBody, []byte(forbidden)) {
			t.Fatalf("diagnostics response leaked %q: %s", forbidden, diagnosticsBody)
		}
	}
}

// TestPluginDeliveryNeverExposesSecretRefOverPostgres seeds a real delivery row
// with a secret reference and proves neither the global observability union nor
// the dedicated delivery endpoints return the reference, its value, or any
// field the contract does not declare.
func TestPluginDeliveryNeverExposesSecretRefOverPostgres(t *testing.T) {
	rig := newTestRig(t, "development", true, withObservabilityContractDeps(t))
	ctx := t.Context()

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := rig.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed plugin fixture: %v", err)
		}
	}
	// The storage-only reference is the leak vector: it is a real column on the
	// delivery row, so it must never appear in any response. Its value is an
	// opaque identifier; the encrypted secret itself is a different row and was
	// never selected.
	const secretRef = "secret-ref-plugin-contract"
	exec(`INSERT INTO secret_values(ref, purpose, nonce, ciphertext)
		VALUES ($1, 'plugin-adapter', decode(repeat('01', 12), 'hex'), decode(repeat('02', 32), 'hex'))`, secretRef)
	exec(`INSERT INTO plugin_target_configs(id, name, plugin_kind, adapter_kind, enabled, trigger_kinds, config_schema_version, config, template_set)
		VALUES ('target-contract', 'Contract Target', 'notification', 'webhook', FALSE,
			'["download_submitted"]'::jsonb, 1, '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb, '{}'::jsonb)`)
	exec(`INSERT INTO plugin_deliveries(
			id, plugin_target_config_id, plugin_target_config_version, adapter_kind, adapter_config_snapshot,
			secret_ref, delivery_kind, trigger_kind, source_owner_type, source_owner_id, source_owner_version,
			source_result_key, managed_anime_id, state, idempotency_key, payload_schema_version, payload,
			payload_digest, template_language_version, template_catalog_version, title_template_snapshot,
			body_template_snapshot, target_display, attempt_count, last_error, created_at, updated_at,
			retention_until)
		VALUES ('delivery-contract', 'target-contract', 1, 'webhook', '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb,
			$1, 'business', 'download_submitted', 'download_task', 'task-contract', 1,
			'result-contract', NULL, 'failed', 'idem-contract', 1,
			'{"schema_version":1,"trigger_kind":"download_submitted","facts":{}}'::jsonb,
			'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'bansoko-template-1', 'notification-catalog-v1', 'title', 'body', 'Contract Target', 1,
			'webhook delivery failed',
			CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '30 days')`, secretRef)

	assertNoSecret := func(t *testing.T, label string, body []byte) {
		t.Helper()
		for _, forbidden := range []string{secretRef, "secret_ref"} {
			if bytes.Contains(body, []byte(forbidden)) {
				t.Fatalf("%s leaked %q: %s", label, forbidden, body)
			}
		}
	}

	// Global union: only the plugin source is requested so the fixture row is
	// the only possible carrier.
	recordsRecorder, recordsBody := rig.do(t, http.MethodGet, "/api/v1/observability/records?kind=plugin&limit=20", nil, nil)
	if recordsRecorder.Code != http.StatusOK {
		t.Fatalf("records status = %d; body=%s", recordsRecorder.Code, recordsBody)
	}
	assertNoSecret(t, "observability records", recordsBody)
	validateAgainstOpenAPI(t, "ObservabilityRecordPage", recordsBody)
	if !bytes.Contains(recordsBody, []byte(`"delivery-contract"`)) {
		t.Fatalf("the plugin record must still be returned: %s", recordsBody)
	}

	// Dedicated list endpoint.
	listRecorder, listBody := rig.do(t, http.MethodGet, "/api/v1/plugin-deliveries?limit=20", nil, nil)
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("delivery list status = %d; body=%s", listRecorder.Code, listBody)
	}
	assertNoSecret(t, "plugin delivery list", listBody)
	validateAgainstOpenAPI(t, "PluginDeliveryList", listBody)

	// Dedicated detail endpoint.
	detailRecorder, detailBody := rig.do(t, http.MethodGet, "/api/v1/plugin-deliveries/delivery-contract", nil, nil)
	if detailRecorder.Code != http.StatusOK {
		t.Fatalf("delivery detail status = %d; body=%s", detailRecorder.Code, detailBody)
	}
	assertNoSecret(t, "plugin delivery detail", detailBody)
	validateAgainstOpenAPI(t, "PluginDeliveryDetail", detailBody)

	// The row really carries the reference, so the assertions above prove a
	// projection and not an empty fixture. The delivery itself must still be
	// returned, otherwise the absence assertions would pass on an empty body.
	var storedRef string
	if err := rig.db.QueryRowContext(ctx, `SELECT COALESCE(secret_ref, '') FROM plugin_deliveries WHERE id = 'delivery-contract'`).Scan(&storedRef); err != nil {
		t.Fatal(err)
	}
	if storedRef != secretRef {
		t.Fatalf("fixture did not store the secret reference, got %q", storedRef)
	}
}

// TestObservabilityUnionMembersSatisfyContractOverPostgres seeds one row per
// union source and validates the whole page against api/openapi.yaml. Every
// member is additionalProperties:false, so this fails if a source starts
// projecting a storage-only column again — the defect class that let
// secret_ref reach the client through to_jsonb(plugin_deliveries.*).
func TestObservabilityUnionMembersSatisfyContractOverPostgres(t *testing.T) {
	rig := newTestRig(t, "development", true, withObservabilityContractDeps(t))
	ctx := t.Context()

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := rig.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed union fixture: %v", err)
		}
	}
	exec(`INSERT INTO routine_activities(
			id, kind, scope_kind, scope_id, managed_anime_id, outcome, last_executed_at,
			last_result_code, summary, details)
		VALUES ('routine-contract', 'rss_refresh', 'subscription', 'subscription-contract', NULL,
			'success', CURRENT_TIMESTAMP, 'success', 'routine contract fixture', '{"item_count":1}'::jsonb)`)
	exec(`INSERT INTO managed_anime(id, title) VALUES ('anime-contract', 'Contract Show')`)
	exec(`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
		VALUES ('subscription-contract', 'anime-contract', 'custom', 'Contract', 'active')`)
	exec(`INSERT INTO subscription_log_entries(
			id, subscription_id, managed_anime_id, kind, outcome, occurred_at, message_code, message_params,
			dedupe_key, retention_until)
		VALUES ('business-contract', 'subscription-contract', 'anime-contract', 'release_processed',
			'info', CURRENT_TIMESTAMP, 'release_accepted', '{"group_name":"Contract Group"}'::jsonb,
			'business-contract:1', CURRENT_TIMESTAMP + INTERVAL '30 days')`)
	exec(`INSERT INTO automation_dispatch_runs(
			id, kind, trigger, state, target_count, enqueued_count, merged_count, skipped_count,
			dispatch_error_count, started_at, finished_at, retention_until)
		VALUES ('automation-contract', 'rss_refresh', 'scheduled', 'completed', 1, 1, 0, 0, 0,
			CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '30 days')`)
	exec(`INSERT INTO plugin_target_configs(id, name, plugin_kind, adapter_kind, enabled, trigger_kinds, config_schema_version, config, template_set)
		VALUES ('target-contract-union', 'Union Target', 'notification', 'webhook', FALSE,
			'["download_submitted"]'::jsonb, 1, '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb, '{}'::jsonb)`)
	exec(`INSERT INTO plugin_deliveries(
			id, plugin_target_config_id, plugin_target_config_version, adapter_kind, adapter_config_snapshot,
			delivery_kind, trigger_kind, source_owner_type, source_owner_id, source_owner_version,
			source_result_key, state, idempotency_key, payload_schema_version, payload, payload_digest,
			template_language_version, template_catalog_version, title_template_snapshot,
			body_template_snapshot, target_display, attempt_count, delivered_at, created_at, updated_at, retention_until)
		VALUES ('delivery-contract-union', 'target-contract-union', 1, 'webhook', '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb,
			'business', 'download_submitted', 'download_task', 'task-union', 1, 'result-union',
			'delivered', 'idem-union', 1,
			'{"schema_version":1,"trigger_kind":"download_submitted","facts":{}}'::jsonb,
			'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			'bansoko-template-1', 'notification-catalog-v1', 'title', 'body', 'Union Target', 1,
			CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '30 days')`)

	recorder, body := rig.do(t, http.MethodGet, "/api/v1/observability/records?limit=50", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("records status = %d; body=%s", recorder.Code, body)
	}
	if bytes.Contains(body, []byte("secret_ref")) {
		t.Fatalf("union response leaked a storage-only column: %s", body)
	}
	validateAgainstOpenAPI(t, "ObservabilityRecordPage", body)

	var page struct {
		Records []struct {
			RecordType string `json:"record_type"`
		} `json:"records"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("decode records page: %v (%s)", err, body)
	}
	kinds := map[string]bool{}
	for _, record := range page.Records {
		kinds[record.RecordType] = true
	}
	for _, kind := range []string{"routine", "business", "automation", "plugin"} {
		if !kinds[kind] {
			t.Fatalf("union page is missing the %s member: %s", kind, body)
		}
	}
}

// TestPluginDeliveryPayloadIsCanonicalAndFrozenOverPostgres drives the real
// writer through the HTTP API and then reads the same delivery back through
// both surfaces. The persisted payload must be the canonical
// PluginDeliveryPayload, payload_digest must be the digest of exactly those
// bytes, and the dedicated and global responses must agree.
func TestPluginDeliveryPayloadIsCanonicalAndFrozenOverPostgres(t *testing.T) {
	rig := newTestRig(t, "development", true, withObservabilityContractDeps(t))
	ctx := t.Context()

	targetBody := map[string]any{
		"name": "Frozen Payload Contract Target", "plugin_kind": "notification", "adapter_kind": "webhook",
		"enabled": false, "trigger_kinds": []string{"download_submitted"}, "config_schema_version": 1,
		"config": map[string]any{"adapter_kind": "webhook", "auth_kind": "none"},
		"template_set": map[string]any{
			"language_version": "bansoko-template-1", "catalog_version": "notification-catalog-v1",
			"events": map[string]any{
				"download_submitted":       map[string]any{"mode": "system_default"},
				"file_operation_completed": map[string]any{"mode": "system_default"},
				"file_operation_failed":    map[string]any{"mode": "system_default"},
			},
		},
		"secret": map[string]any{"mode": "set", "value": map[string]any{"adapter_kind": "webhook", "endpoint_url": "https://example.test/hook"}},
	}
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/plugin-targets", targetBody, map[string]string{"Idempotency-Key": "frozen-payload-target"})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create target status = %d; body=%s", recorder.Code, body)
	}
	var target struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}
	if err := json.Unmarshal(body, &target); err != nil {
		t.Fatal(err)
	}

	recorder, body = rig.do(t, http.MethodPost, "/api/v1/plugin-targets/"+target.ID+"/test",
		map[string]any{"expected_version": target.Version, "trigger_kind": "download_submitted"},
		map[string]string{"Idempotency-Key": "frozen-payload-test-1"})
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("test delivery status = %d; body=%s", recorder.Code, body)
	}
	var accepted struct {
		DeliveryID string `json:"delivery_id"`
	}
	if err := json.Unmarshal(body, &accepted); err != nil {
		t.Fatal(err)
	}

	// The dedicated detail read.
	detailRecorder, detailBody := rig.do(t, http.MethodGet, "/api/v1/plugin-deliveries/"+accepted.DeliveryID, nil, nil)
	if detailRecorder.Code != http.StatusOK {
		t.Fatalf("delivery detail status = %d; body=%s", detailRecorder.Code, detailBody)
	}
	validateAgainstOpenAPI(t, "PluginDeliveryDetail", detailBody)

	var detail struct {
		Delivery struct {
			Payload       json.RawMessage `json:"payload"`
			PayloadStatus string          `json:"payload_status"`
			PayloadDigest string          `json:"payload_digest"`
			TriggerKind   string          `json:"trigger_kind"`
		} `json:"delivery"`
	}
	if err := json.Unmarshal(detailBody, &detail); err != nil {
		t.Fatal(err)
	}
	assertCanonicalFrozenPayload(t, detail.Delivery.Payload, detail.Delivery.PayloadStatus, detail.Delivery.PayloadDigest, detail.Delivery.TriggerKind)

	// The global union must serve the same frozen payload.
	recordsRecorder, recordsBody := rig.do(t, http.MethodGet, "/api/v1/observability/records?kind=plugin&limit=20", nil, nil)
	if recordsRecorder.Code != http.StatusOK {
		t.Fatalf("records status = %d; body=%s", recordsRecorder.Code, recordsBody)
	}
	validateAgainstOpenAPI(t, "ObservabilityRecordPage", recordsBody)
	var page struct {
		Records []struct {
			Delivery struct {
				ID            string          `json:"id"`
				Payload       json.RawMessage `json:"payload"`
				PayloadStatus string          `json:"payload_status"`
				PayloadDigest string          `json:"payload_digest"`
				TriggerKind   string          `json:"trigger_kind"`
			} `json:"delivery"`
		} `json:"records"`
	}
	if err := json.Unmarshal(recordsBody, &page); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range page.Records {
		if record.Delivery.ID != accepted.DeliveryID {
			continue
		}
		found = true
		assertCanonicalFrozenPayload(t, record.Delivery.Payload, record.Delivery.PayloadStatus, record.Delivery.PayloadDigest, record.Delivery.TriggerKind)
		if string(record.Delivery.Payload) != string(detail.Delivery.Payload) {
			t.Fatalf("global and dedicated payloads disagree:\n%s\n%s", record.Delivery.Payload, detail.Delivery.Payload)
		}
	}
	if !found {
		t.Fatalf("the delivery is missing from the global union: %s", recordsBody)
	}

	// The test request identity lives in the idempotency record, not in the
	// delivery payload.
	var requestBody string
	if err := rig.db.QueryRowContext(ctx, `
SELECT canonical_request_body::text FROM idempotency_records
WHERE kind = 'plugin_test' AND delivery_id = $1`, accepted.DeliveryID).Scan(&requestBody); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(requestBody, "expected_version") {
		t.Fatalf("the idempotency record must keep the request identity: %s", requestBody)
	}
	if bytes.Contains(detail.Delivery.Payload, []byte("expected_version")) {
		t.Fatalf("the delivery payload must not carry request identity: %s", detail.Delivery.Payload)
	}
}

// TestLegacyPluginDeliveryPayloadFailsClosedOverPostgres proves a row written
// before the canonical payload shape is withheld on both read surfaces: the
// payload is null and its status is invalid, never a re-serialised or defaulted
// object.
func TestLegacyPluginDeliveryPayloadFailsClosedOverPostgres(t *testing.T) {
	rig := newTestRig(t, "development", true, withObservabilityContractDeps(t))
	ctx := t.Context()

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := rig.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed legacy delivery: %v", err)
		}
	}
	exec(`INSERT INTO plugin_target_configs(id, name, plugin_kind, adapter_kind, enabled, trigger_kinds, config_schema_version, config, template_set)
		VALUES ('target-legacy-payload', 'Legacy Payload Target', 'notification', 'webhook', FALSE,
			'["download_submitted"]'::jsonb, 1, '{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb, '{}'::jsonb)`)
	// A legacy test delivery: the request body was stored as the payload.
	exec(`INSERT INTO plugin_deliveries(
			id, plugin_target_config_id, plugin_target_config_version, adapter_kind, adapter_config_snapshot,
			delivery_kind, trigger_kind, source_owner_type, source_owner_id, source_owner_version,
			source_result_key, test_request_idempotency_key, test_request_hash, state, idempotency_key,
			payload_schema_version, payload, payload_digest,
			template_language_version, template_catalog_version, title_template_snapshot,
			body_template_snapshot, target_display, attempt_count, created_at, updated_at, retention_until)
		VALUES ('delivery-legacy-test', 'target-legacy-payload', 1, 'webhook',
			'{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb,
			'test', 'download_submitted', 'plugin_target', 'target-legacy-payload', 1, 'legacy-test',
			'legacy-test-key', 'sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',
			'unknown', 'legacy-test-key', 1, '{"expected_version":1,"trigger_kind":"download_submitted"}'::jsonb,
			'sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',
			'bansoko-template-1', 'notification-catalog-v1', 'title', 'body', 'Legacy Target', 1,
			CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '30 days')`)
	// A legacy business delivery: the raw facts map was stored as the payload.
	exec(`INSERT INTO plugin_deliveries(
			id, plugin_target_config_id, plugin_target_config_version, adapter_kind, adapter_config_snapshot,
			delivery_kind, trigger_kind, source_owner_type, source_owner_id, source_owner_version,
			source_result_key, state, idempotency_key, payload_schema_version, payload, payload_digest,
			template_language_version, template_catalog_version, title_template_snapshot,
			body_template_snapshot, target_display, attempt_count, created_at, updated_at, retention_until)
		VALUES ('delivery-legacy-facts', 'target-legacy-payload', 1, 'webhook',
			'{"adapter_kind":"webhook","auth_kind":"none"}'::jsonb,
			'business', 'download_submitted', 'download_task', 'task-legacy', 1, 'legacy-facts',
			'unknown', 'legacy-facts-key', 1, '{"anime.title":"Legacy","download.task_name":"t"}'::jsonb,
			'sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd',
			'bansoko-template-1', 'notification-catalog-v1', 'title', 'body', 'Legacy Target', 1,
			CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '30 days')`)

	listRecorder, listBody := rig.do(t, http.MethodGet, "/api/v1/plugin-deliveries?limit=20", nil, nil)
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("delivery list status = %d; body=%s", listRecorder.Code, listBody)
	}
	validateAgainstOpenAPI(t, "PluginDeliveryList", listBody)
	assertLegacyPayloadWithheld(t, listBody, "delivery-legacy-test", `"expected_version"`)
	assertLegacyPayloadWithheld(t, listBody, "delivery-legacy-facts", `"anime.title"`)

	recordsRecorder, recordsBody := rig.do(t, http.MethodGet, "/api/v1/observability/records?kind=plugin&limit=20", nil, nil)
	if recordsRecorder.Code != http.StatusOK {
		t.Fatalf("records status = %d; body=%s", recordsRecorder.Code, recordsBody)
	}
	validateAgainstOpenAPI(t, "ObservabilityRecordPage", recordsBody)
	assertLegacyPayloadWithheld(t, recordsBody, "delivery-legacy-test", `"expected_version"`)

	// The frozen digest column is still served; only the unreadable payload is
	// withheld.
	if !bytes.Contains(listBody, []byte("sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")) {
		t.Fatalf("the frozen digest must survive the projection: %s", listBody)
	}
}

func assertCanonicalFrozenPayload(t *testing.T, raw json.RawMessage, status, digest, triggerKind string) {
	t.Helper()
	if status != deliverypayload.StatusAvailable {
		t.Fatalf("payload_status = %q, want available", status)
	}
	canonical, ok := deliverypayload.Canonicalize(raw)
	if !ok {
		t.Fatalf("payload is not the canonical delivery payload: %s", raw)
	}
	sum := sha256.Sum256(canonical)
	if want := "sha256:" + hex.EncodeToString(sum[:]); digest != want {
		t.Fatalf("payload_digest = %s, want %s (digest of the canonical payload)", digest, want)
	}
	var decoded struct {
		SchemaVersion int             `json:"schema_version"`
		TriggerKind   string          `json:"trigger_kind"`
		Facts         json.RawMessage `json:"facts"`
	}
	if err := json.Unmarshal(canonical, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SchemaVersion != deliverypayload.SchemaVersion || decoded.TriggerKind != triggerKind {
		t.Fatalf("payload identity drifted: %s (row trigger %q)", canonical, triggerKind)
	}
	if string(decoded.Facts) != "{}" {
		t.Fatalf("a test delivery has no owner facts, got %s", decoded.Facts)
	}
}

func assertLegacyPayloadWithheld(t *testing.T, body []byte, deliveryID, forbidden string) {
	t.Helper()
	if !bytes.Contains(body, []byte(deliveryID)) {
		t.Fatalf("legacy delivery %s is missing from the response: %s", deliveryID, body)
	}
	var envelope struct {
		Records []struct {
			Delivery struct {
				ID            string          `json:"id"`
				Payload       json.RawMessage `json:"payload"`
				PayloadStatus string          `json:"payload_status"`
			} `json:"delivery"`
		} `json:"records"`
		Items []struct {
			ID            string          `json:"id"`
			Payload       json.RawMessage `json:"payload"`
			PayloadStatus string          `json:"payload_status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode response: %v (%s)", err, body)
	}
	type projection struct {
		payload json.RawMessage
		status  string
	}
	projections := map[string]projection{}
	for _, item := range envelope.Items {
		projections[item.ID] = projection{item.Payload, item.PayloadStatus}
	}
	for _, record := range envelope.Records {
		projections[record.Delivery.ID] = projection{record.Delivery.Payload, record.Delivery.PayloadStatus}
	}
	got, ok := projections[deliveryID]
	if !ok {
		t.Fatalf("delivery %s is missing from the projection: %s", deliveryID, body)
	}
	if got.status != deliverypayload.StatusInvalid {
		t.Fatalf("legacy payload status = %q, want invalid", got.status)
	}
	if string(got.payload) != "null" {
		t.Fatalf("legacy payload must be withheld, got %s", got.payload)
	}
	if bytes.Contains(body, []byte(forbidden)) {
		t.Fatalf("legacy payload bytes leaked: %s", body)
	}
}
