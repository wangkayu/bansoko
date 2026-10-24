package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEncodeProducesCanonicalAllowlistedConfig(t *testing.T) {
	cases := []struct {
		name        string
		adapterKind string
		raw         string
		want        string
	}{
		{
			name: "telegram", adapterKind: "telegram",
			raw:  `{"adapter_kind":"telegram","chat_id":"12345"}`,
			want: `{"adapter_kind":"telegram","chat_id":"12345"}`,
		},
		{
			name: "telegram key order normalised", adapterKind: "telegram",
			raw:  `{"chat_id":"12345","adapter_kind":"telegram"}`,
			want: `{"adapter_kind":"telegram","chat_id":"12345"}`,
		},
		{
			name: "feishu", adapterKind: "feishu",
			raw:  `{"adapter_kind":"feishu"}`,
			want: `{"adapter_kind":"feishu"}`,
		},
		{
			name: "webhook none", adapterKind: "webhook",
			raw:  `{"adapter_kind":"webhook","auth_kind":"none"}`,
			want: `{"adapter_kind":"webhook","auth_kind":"none"}`,
		},
		{
			name: "webhook header keeps the header name", adapterKind: "webhook",
			raw:  `{"auth_header_name":"X-Hook-Token","auth_kind":"header","adapter_kind":"webhook"}`,
			want: `{"adapter_kind":"webhook","auth_kind":"header","auth_header_name":"X-Hook-Token"}`,
		},
		{
			name: "webhook bearer drops a stray header name", adapterKind: "webhook",
			raw:  `{"adapter_kind":"webhook","auth_kind":"bearer","auth_header_name":null}`,
			want: `{"adapter_kind":"webhook","auth_kind":"bearer"}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			encoded, err := Encode(testCase.adapterKind, SchemaVersion, []byte(testCase.raw))
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if string(encoded) != testCase.want {
				t.Fatalf("canonical config = %s, want %s", encoded, testCase.want)
			}
			// Canonical bytes are a fixed point: re-encoding yields the same
			// document, so a stored config and a read response agree byte for
			// byte regardless of the request's key order or whitespace.
			again, err := Encode(testCase.adapterKind, SchemaVersion, encoded)
			if err != nil || string(again) != string(encoded) {
				t.Fatalf("canonicalisation is not stable: %s vs %s (%v)", again, encoded, err)
			}
		})
	}
}

func TestEncodeRejectsSmuggledAndMismatchedConfigs(t *testing.T) {
	cases := []struct {
		name        string
		adapterKind string
		version     int
		raw         string
	}{
		{"telegram with bot_token", "telegram", SchemaVersion, `{"adapter_kind":"telegram","chat_id":"1","bot_token":"123:ABC"}`},
		{"webhook with endpoint_url", "webhook", SchemaVersion, `{"adapter_kind":"webhook","auth_kind":"none","endpoint_url":"https://example.test/hook"}`},
		{"webhook with auth_value", "webhook", SchemaVersion, `{"adapter_kind":"webhook","auth_kind":"bearer","auth_value":"secret-token"}`},
		{"undeclared future field", "telegram", SchemaVersion, `{"adapter_kind":"telegram","chat_id":"1","retry_policy":"aggressive"}`},
		{"inner kind mismatch", "telegram", SchemaVersion, `{"adapter_kind":"webhook","chat_id":"1"}`},
		{"inner kind mismatch feishu", "feishu", SchemaVersion, `{"adapter_kind":"telegram"}`},
		{"missing inner kind", "telegram", SchemaVersion, `{"chat_id":"1"}`},
		{"missing chat_id", "telegram", SchemaVersion, `{"adapter_kind":"telegram"}`},
		{"empty chat_id", "telegram", SchemaVersion, `{"adapter_kind":"telegram","chat_id":"   "}`},
		{"overlong chat_id", "telegram", SchemaVersion, `{"adapter_kind":"telegram","chat_id":"` + strings.Repeat("a", 257) + `"}`},
		{"unsupported auth kind", "webhook", SchemaVersion, `{"adapter_kind":"webhook","auth_kind":"basic"}`},
		{"header auth without header name", "webhook", SchemaVersion, `{"adapter_kind":"webhook","auth_kind":"header"}`},
		{"header name reserved", "webhook", SchemaVersion, `{"adapter_kind":"webhook","auth_kind":"header","auth_header_name":"Host"}`},
		{"header name unsafe", "webhook", SchemaVersion, `{"adapter_kind":"webhook","auth_kind":"header","auth_header_name":"X Hook"}`},
		{"header name on none auth", "webhook", SchemaVersion, `{"adapter_kind":"webhook","auth_kind":"none","auth_header_name":"X-Hook"}`},
		{"unsupported version", "telegram", 2, `{"adapter_kind":"telegram","chat_id":"1"}`},
		{"array document", "telegram", SchemaVersion, `["telegram"]`},
		{"second document", "telegram", SchemaVersion, `{"adapter_kind":"telegram","chat_id":"1"}{"adapter_kind":"feishu"}`},
		{"not even an object", "webhook", SchemaVersion, `"webhook"`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := Encode(testCase.adapterKind, testCase.version, []byte(testCase.raw)); err == nil {
				t.Fatalf("config %s must be rejected", testCase.raw)
			}
		})
	}
	if _, err := Encode("mastodon", SchemaVersion, []byte(`{"adapter_kind":"mastodon"}`)); err == nil {
		t.Fatal("an unknown adapter kind must be rejected")
	}
}

func TestNormalizeAndProjectRowWithholdUnprovableConfigs(t *testing.T) {
	// A legacy telegram config carrying a smuggled bot token is not provable and
	// must be withheld rather than returned.
	normalized, status := Normalize("telegram", SchemaVersion, []byte(`{"adapter_kind":"telegram","chat_id":"1","bot_token":"123:ABC"}`))
	if status != StatusInvalid || normalized != nil {
		t.Fatalf("smuggled config: status=%q payload=%s", status, normalized)
	}
	if normalized, status := Normalize("webhook", SchemaVersion, nil); status != StatusUnavailable || normalized != nil {
		t.Fatalf("absent config: status=%q payload=%s", status, normalized)
	}
	if normalized, status := Normalize("webhook", SchemaVersion, []byte(`{"adapter_kind":"webhook","auth_kind":"none"}`)); status != StatusAvailable || normalized == nil {
		t.Fatalf("canonical config: status=%q payload=%s", status, normalized)
	}

	targetRow := []byte(`{"id":"target-1","adapter_kind":"telegram","config_schema_version":1,"config":{"adapter_kind":"telegram","chat_id":"1","bot_token":"123:ABC"}}`)
	projected, err := ProjectRow(targetRow, "config")
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(projected, &object); err != nil {
		t.Fatal(err)
	}
	if string(object["config"]) != "null" || string(object["config_status"]) != `"invalid"` {
		t.Fatalf("target projection = %s", projected)
	}
	if strings.Contains(string(projected), "bot_token") || strings.Contains(string(projected), "123:ABC") {
		t.Fatalf("target projection leaked the smuggled field: %s", projected)
	}

	deliveryRow := []byte(`{"id":"d-1","adapter_kind":"webhook","config_schema_version":1,"adapter_config_snapshot":{"adapter_kind":"webhook","auth_kind":"none"}}`)
	projected, err = ProjectRow(deliveryRow, "adapter_config_snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(projected, &object); err != nil {
		t.Fatal(err)
	}
	if string(object["config_status"]) != `"available"` {
		t.Fatalf("delivery projection = %s", projected)
	}
	if string(object["adapter_config_snapshot"]) != `{"adapter_kind":"webhook","auth_kind":"none"}` {
		t.Fatalf("delivery snapshot = %s", object["adapter_config_snapshot"])
	}
}
