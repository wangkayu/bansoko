package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

// webhookTargetBody builds a valid webhook target create body; tests mutate the
// config or secret to probe the closed adapter-config codec.
func webhookTargetBody(name string) map[string]any {
	return map[string]any{
		"name": name, "plugin_kind": "notification", "adapter_kind": "webhook",
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
}

// TestPluginTargetConfigSmugglingIsRejectedOverHTTP proves a request cannot push
// an undeclared or secret-bearing key into storage, a snapshot or a read
// response: the closed codec rejects the document with a typed 4xx instead of
// persisting it.
func TestPluginTargetConfigSmugglingIsRejectedOverHTTP(t *testing.T) {
	rig := newTestRig(t, "development", true, withObservabilityContractDeps(t))

	cases := []struct {
		name       string
		adapter    string
		config     map[string]any
		wantDetail string
	}{
		{
			name: "telegram carries bot_token", adapter: "telegram",
			config:     map[string]any{"adapter_kind": "telegram", "chat_id": "1", "bot_token": "123:ABC"},
			wantDetail: "bot_token",
		},
		{
			name: "webhook carries endpoint_url", adapter: "webhook",
			config:     map[string]any{"adapter_kind": "webhook", "auth_kind": "none", "endpoint_url": "https://example.test/hook"},
			wantDetail: "endpoint_url",
		},
		{
			name: "webhook carries auth_value", adapter: "webhook",
			config:     map[string]any{"adapter_kind": "webhook", "auth_kind": "bearer", "auth_value": "secret-token"},
			wantDetail: "auth_value",
		},
		{
			name: "unknown future field", adapter: "telegram",
			config:     map[string]any{"adapter_kind": "telegram", "chat_id": "1", "retry_policy": "aggressive"},
			wantDetail: "retry_policy",
		},
		{
			name: "inner adapter kind mismatch", adapter: "telegram",
			config:     map[string]any{"adapter_kind": "webhook", "chat_id": "1"},
			wantDetail: "config.adapter_kind",
		},
		{
			name: "missing inner adapter kind", adapter: "telegram",
			config:     map[string]any{"chat_id": "1"},
			wantDetail: "config.adapter_kind",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body := webhookTargetBody("smuggle-" + testCase.name)
			body["adapter_kind"] = testCase.adapter
			body["config"] = testCase.config
			recorder, response := rig.do(t, http.MethodPost, "/api/v1/plugin-targets", body,
				map[string]string{"Idempotency-Key": "smuggle-" + testCase.name})
			if recorder.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422; body=%s", recorder.Code, response)
			}
			if !bytes.Contains(response, []byte(testCase.wantDetail)) {
				t.Fatalf("response must name the offending field %q: %s", testCase.wantDetail, response)
			}
			for _, leaked := range []string{"123:ABC", "secret-token"} {
				if bytes.Contains(response, []byte(leaked)) {
					t.Fatalf("response echoed a secret value %q: %s", leaked, response)
				}
			}
			// Nothing was persisted: the smuggling attempt leaves no target row
			// and no secret row behind.
			var targets, secrets int
			if err := rig.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM plugin_target_configs`).Scan(&targets); err != nil {
				t.Fatal(err)
			}
			if err := rig.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM secret_values`).Scan(&secrets); err != nil {
				t.Fatal(err)
			}
			if targets != 0 || secrets != 0 {
				t.Fatalf("a rejected config persisted state: targets=%d secrets=%d", targets, secrets)
			}
		})
	}
}

// TestPluginTargetConfigIsCanonicalOverHTTP proves the request's key order and
// whitespace do not reach storage: the persisted config is the canonical
// allowlisted document, reads serve it, and no secret value crosses any read
// surface.
func TestPluginTargetConfigIsCanonicalOverHTTP(t *testing.T) {
	rig := newTestRig(t, "development", true, withObservabilityContractDeps(t))
	ctx := t.Context()

	body := webhookTargetBody("canonical-config-target")
	// Deliberately shuffled key order plus a header name that bearer auth drops.
	body["config"] = map[string]any{"auth_kind": "header", "adapter_kind": "webhook", "auth_header_name": "X-Hook-Token"}
	recorder, response := rig.do(t, http.MethodPost, "/api/v1/plugin-targets", body, map[string]string{"Idempotency-Key": "canonical-config-target"})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d; body=%s", recorder.Code, response)
	}
	var created struct {
		ID           string `json:"id"`
		ConfigStatus string `json:"config_status"`
	}
	if err := json.Unmarshal(response, &created); err != nil {
		t.Fatal(err)
	}
	if created.ConfigStatus != "available" {
		t.Fatalf("config_status = %q, want available", created.ConfigStatus)
	}

	var stored string
	if err := rig.db.QueryRowContext(ctx, `SELECT config::text FROM plugin_target_configs WHERE id = $1`, created.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	// JSONB text is key-sorted by length; compare the logical document plus the
	// exact key set so a smuggled key could not hide behind ordering.
	var storedObject map[string]any
	if err := json.Unmarshal([]byte(stored), &storedObject); err != nil {
		t.Fatal(err)
	}
	if len(storedObject) != 3 || storedObject["adapter_kind"] != "webhook" || storedObject["auth_kind"] != "header" || storedObject["auth_header_name"] != "X-Hook-Token" {
		t.Fatalf("stored config is not the canonical allowlisted document: %s", stored)
	}

	recorder, listBody := rig.do(t, http.MethodGet, "/api/v1/plugin-targets", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d; body=%s", recorder.Code, listBody)
	}
	validateAgainstOpenAPI(t, "PluginTargetPage", listBody)
	for _, forbidden := range []string{"secret_ref", "endpoint_url", "https://example.test/hook"} {
		if bytes.Contains(listBody, []byte(forbidden)) {
			t.Fatalf("target read leaked %q: %s", forbidden, listBody)
		}
	}

	// The secret still travels through its own channel and is reported only as
	// presence.
	var hasSecret bool
	if err := rig.db.QueryRowContext(ctx, `SELECT secret_ref IS NOT NULL FROM plugin_target_configs WHERE id = $1`, created.ID).Scan(&hasSecret); err != nil {
		t.Fatal(err)
	}
	if !hasSecret {
		t.Fatal("the secret channel must still store a secret reference")
	}
	if !bytes.Contains(listBody, []byte(`"has_secret":true`)) {
		t.Fatalf("the read must report secret presence: %s", listBody)
	}
}

// TestLegacyAdapterConfigIsWithheldOverHTTP proves a row the migration could not
// prove canonical never returns its bytes: the target, the delivery and the
// global union all withhold the config and report it invalid.
func TestLegacyAdapterConfigIsWithheldOverHTTP(t *testing.T) {
	rig := newTestRig(t, "development", true, withObservabilityContractDeps(t))
	ctx := t.Context()

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := rig.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed legacy config fixture: %v", err)
		}
	}
	exec(`INSERT INTO plugin_target_configs(
			id, name, plugin_kind, adapter_kind, enabled, trigger_kinds, config_schema_version,
			config, config_status, template_set)
		VALUES ('target-legacy-config', 'Legacy Config Target', 'notification', 'telegram', FALSE,
			'["download_submitted"]'::jsonb, 1,
			'{"adapter_kind":"telegram","chat_id":"1","bot_token":"123:legacy-secret"}'::jsonb, 'invalid',
			'{"language_version":"bansoko-template-1","catalog_version":"notification-catalog-v1","events":{"download_submitted":{"mode":"system_default"},"file_operation_completed":{"mode":"system_default"},"file_operation_failed":{"mode":"system_default"}}}'::jsonb)`)
	exec(`INSERT INTO plugin_deliveries(
			id, plugin_target_config_id, plugin_target_config_version, adapter_kind, adapter_config_snapshot,
			config_status, config_schema_version, delivery_kind, trigger_kind, source_owner_type,
			source_owner_id, source_owner_version, source_result_key, state, idempotency_key,
			payload_schema_version, payload, payload_digest, template_language_version, template_catalog_version,
			title_template_snapshot, body_template_snapshot, target_display, attempt_count,
			created_at, updated_at, retention_until)
		VALUES ('delivery-legacy-config', 'target-legacy-config', 1, 'telegram',
			'{"adapter_kind":"telegram","chat_id":"1","bot_token":"123:legacy-secret"}'::jsonb, 'invalid', 1,
			'business', 'download_submitted', 'download_task', 'task-legacy-config', 1, 'legacy-config',
			'unknown', 'legacy-config-key', 1,
			'{"schema_version":1,"trigger_kind":"download_submitted","facts":{}}'::jsonb,
			'sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff',
			'bansoko-template-1', 'notification-catalog-v1', 'title', 'body', 'Legacy Target', 1,
			CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP + INTERVAL '30 days')`)

	targetRecorder, targetBody := rig.do(t, http.MethodGet, "/api/v1/plugin-targets", nil, nil)
	if targetRecorder.Code != http.StatusOK {
		t.Fatalf("target list status = %d; body=%s", targetRecorder.Code, targetBody)
	}
	validateAgainstOpenAPI(t, "PluginTargetPage", targetBody)
	assertLegacyConfigWithheld(t, targetBody, "config", "target-legacy-config")

	deliveryRecorder, deliveryBody := rig.do(t, http.MethodGet, "/api/v1/plugin-deliveries?limit=20", nil, nil)
	if deliveryRecorder.Code != http.StatusOK {
		t.Fatalf("delivery list status = %d; body=%s", deliveryRecorder.Code, deliveryBody)
	}
	validateAgainstOpenAPI(t, "PluginDeliveryList", deliveryBody)
	assertLegacyConfigWithheld(t, deliveryBody, "adapter_config_snapshot", "delivery-legacy-config")

	recordsRecorder, recordsBody := rig.do(t, http.MethodGet, "/api/v1/observability/records?kind=plugin&limit=20", nil, nil)
	if recordsRecorder.Code != http.StatusOK {
		t.Fatalf("records status = %d; body=%s", recordsRecorder.Code, recordsBody)
	}
	validateAgainstOpenAPI(t, "ObservabilityRecordPage", recordsBody)
	assertLegacyConfigWithheld(t, recordsBody, "adapter_config_snapshot", "delivery-legacy-config")

	for _, body := range [][]byte{targetBody, deliveryBody, recordsBody} {
		for _, forbidden := range []string{"bot_token", "123:legacy-secret"} {
			if bytes.Contains(body, []byte(forbidden)) {
				t.Fatalf("legacy adapter config leaked %q: %s", forbidden, body)
			}
		}
	}
}

// assertLegacyConfigWithheld checks the named row reports config_status invalid
// with a null config field, on whichever envelope the response uses.
func assertLegacyConfigWithheld(t *testing.T, body []byte, configField, rowID string) {
	t.Helper()
	if !bytes.Contains(body, []byte(rowID)) {
		t.Fatalf("row %s is missing from the response: %s", rowID, body)
	}
	var envelope struct {
		Targets []map[string]json.RawMessage `json:"targets"`
		Items   []map[string]json.RawMessage `json:"items"`
		Records []struct {
			Delivery map[string]json.RawMessage `json:"delivery"`
		} `json:"records"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode response: %v (%s)", err, body)
	}
	rows := append([]map[string]json.RawMessage{}, envelope.Targets...)
	rows = append(rows, envelope.Items...)
	for _, record := range envelope.Records {
		if record.Delivery != nil {
			rows = append(rows, record.Delivery)
		}
	}
	for _, row := range rows {
		if string(row["id"]) != `"`+rowID+`"` {
			continue
		}
		if string(row[configField]) != "null" {
			t.Fatalf("%s must be withheld for %s, got %s", configField, rowID, row[configField])
		}
		if string(row["config_status"]) != `"invalid"` {
			t.Fatalf("config_status = %s, want invalid", row["config_status"])
		}
		return
	}
	t.Fatalf("row %s not found among the projected rows: %s", rowID, body)
}
