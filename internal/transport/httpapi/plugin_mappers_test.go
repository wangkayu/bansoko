package httpapi

import (
	"encoding/json"
	"testing"

	plugintargetapplication "bansoko/internal/plugintarget/application"
	"bansoko/internal/transport/httpapi/generated"
)

func TestPluginTargetMappersPreserveUnionJSON(t *testing.T) {
	var input generated.PluginTargetCreate
	if err := json.Unmarshal([]byte(`{"adapter_kind":"telegram","config":{"adapter_kind":"telegram","chat_id":"42"},"config_schema_version":1,"enabled":true,"name":"target","plugin_kind":"notification","secret":{"mode":"set","value":{"adapter_kind":"telegram","bot_token":"token"}},"template_set":{"language_version":"bansoko-template-1","catalog_version":"notification-catalog-v1","events":{"download_submitted":{"mode":"system_default"},"file_operation_completed":{"mode":"system_default"},"file_operation_failed":{"mode":"system_default"}}},"trigger_kinds":["download_submitted"]}`), &input); err != nil {
		t.Fatal(err)
	}
	neutral, err := mapPluginTargetInput(input)
	if err != nil {
		t.Fatal(err)
	}
	if neutral.AdapterKind != plugintargetapplication.AdapterTelegram || string(neutral.Config) != `{"adapter_kind":"telegram","chat_id":"42"}` {
		t.Fatalf("unexpected neutral target input: %+v", neutral)
	}
	if neutral.Secret.Mode != "set" || string(neutral.Secret.Value) != `{"adapter_kind":"telegram","bot_token":"token"}` {
		t.Fatalf("secret union was not preserved: %+v", neutral.Secret)
	}
	mapped, err := mapPluginTargetConfig(plugintargetapplication.TargetConfig{
		AdapterKind:         plugintargetapplication.AdapterTelegram,
		Config:              neutral.Config,
		ConfigSchemaVersion: 1,
		Enabled:             true,
		ID:                  "target-id",
		Name:                "target",
		PluginKind:          plugintargetapplication.PluginKindNotification,
		TemplateSet:         neutral.TemplateSet,
		TriggerKinds: []plugintargetapplication.TriggerKind{
			plugintargetapplication.TriggerDownloadSubmitted,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	configJSON, err := json.Marshal(mapped.Config)
	if err != nil {
		t.Fatal(err)
	}
	if string(configJSON) != string(neutral.Config) {
		t.Fatalf("config JSON changed: got %s want %s", configJSON, neutral.Config)
	}
}

func TestPluginDeliveryMappersDecodeRawProjection(t *testing.T) {
	raw := plugintargetapplication.Delivery{Raw: json.RawMessage(`{"id":"delivery-id","adapter_kind":"telegram","adapter_config_snapshot":{"adapter_kind":"telegram","chat_id":"42"},"trigger_kind":"download_submitted","state":"pending","delivery_kind":"test","payload":{"schema_version":1,"trigger_kind":"download_submitted","facts":{}},"payload_status":"available","source_owner_type":"plugin_target","source_owner_id":"target-id","source_owner_version":1,"source_result_key":"test","plugin_target_config_id":"target-id","plugin_target_config_version":1,"idempotency_key":"effect-key","payload_digest":"sha256:x","payload_schema_version":1,"title_template_snapshot":"title","body_template_snapshot":"body","target_display":"telegram:42","template_language_version":"bansoko-template-1","template_catalog_version":"notification-catalog-v1","attempt_count":0,"retention_until":"2026-01-01T00:00:00Z","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","version":1}`)}
	mapped, err := mapPluginDelivery(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(mapped.Id) != "delivery-id" || mapped.State != generated.PluginDeliveryState("pending") {
		t.Fatalf("unexpected delivery mapping: %+v", mapped)
	}
}
