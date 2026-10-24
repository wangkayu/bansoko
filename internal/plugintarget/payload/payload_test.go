package payload

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// digest mirrors the delivery writer's payload digest so the recompute
// invariant can be asserted without importing the application package.
func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestEncodeProducesTheCanonicalContractShape(t *testing.T) {
	encoded, err := Encode("download_submitted", map[string]any{"anime.title": "Show"})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"schema_version":1,"trigger_kind":"download_submitted","facts":{"anime.title":"Show"}}` {
		t.Fatalf("canonical payload = %s", encoded)
	}
	// A nil fact map is an empty object, never null and never a fabricated fact.
	empty, err := Encode("file_operation_failed", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(empty) != `{"schema_version":1,"trigger_kind":"file_operation_failed","facts":{}}` {
		t.Fatalf("empty facts payload = %s", empty)
	}
	if _, err := Encode("unknown_trigger", nil); err == nil {
		t.Fatal("an unsupported trigger must not encode")
	}
}

func TestEncodeIsDeterministicAndRoundTripStable(t *testing.T) {
	facts := map[string]any{"b": 2, "a": map[string]any{"y": 1, "x": "v"}, "c": []any{"1", true}}
	first, err := Encode("download_submitted", facts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Encode("download_submitted", facts)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("encode is not deterministic:\n%s\n%s", first, second)
	}
	// The digest must survive the storage round trip: canonicalising the bytes a
	// reader gets back reproduces exactly the bytes that were hashed.
	normalized, status := Normalize(first)
	if status != StatusAvailable {
		t.Fatalf("status = %q, want available", status)
	}
	if string(normalized) != string(first) || digest(normalized) != digest(first) {
		t.Fatalf("round trip changed the payload:\n%s\n%s", first, normalized)
	}
	// A JSONB round trip reorders object keys by length; canonicalising the
	// reordered form must still produce the hashed bytes.
	reordered := json.RawMessage(`{"facts":{"a":{"y":1,"x":"v"},"b":2,"c":["1",true]},"trigger_kind":"download_submitted","schema_version":1}`)
	canonicalized, ok := Canonicalize(reordered)
	if !ok {
		t.Fatal("a reordered but equivalent payload must canonicalise")
	}
	if digest(canonicalized) != digest(first) {
		t.Fatalf("digest changed across key reordering:\n%s\n%s", canonicalized, first)
	}
}

func TestNormalizeRejectsLegacyAndForeignShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"absent payload", ``, StatusUnavailable},
		{"json null payload", `null`, StatusUnavailable},
		{"legacy test request body", `{"expected_version":1,"trigger_kind":"download_submitted"}`, StatusInvalid},
		{"legacy facts map", `{"anime.title":"Show","download.task_name":"t"}`, StatusInvalid},
		{"missing facts", `{"schema_version":1,"trigger_kind":"download_submitted"}`, StatusInvalid},
		{"null facts", `{"schema_version":1,"trigger_kind":"download_submitted","facts":null}`, StatusInvalid},
		{"undeclared extra key", `{"schema_version":1,"trigger_kind":"download_submitted","facts":{},"expected_version":1}`, StatusInvalid},
		{"zero schema version", `{"schema_version":0,"trigger_kind":"download_submitted","facts":{}}`, StatusInvalid},
		{"unknown trigger", `{"schema_version":1,"trigger_kind":"something_else","facts":{}}`, StatusInvalid},
		{"non object payload", `["not","an","object"]`, StatusInvalid},
		{"canonical payload", `{"schema_version":1,"trigger_kind":"download_submitted","facts":{}}`, StatusAvailable},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			normalized, status := Normalize([]byte(testCase.raw))
			if status != testCase.want {
				t.Fatalf("status = %q, want %q", status, testCase.want)
			}
			if status == StatusAvailable && normalized == nil {
				t.Fatal("an available payload must be returned")
			}
			if status != StatusAvailable && normalized != nil {
				t.Fatalf("a withheld payload must be nil, got %s", normalized)
			}
		})
	}
	// A version above the current one is still canonical and is preserved.
	normalized, status := Normalize([]byte(`{"schema_version":2,"trigger_kind":"download_submitted","facts":{}}`))
	if status != StatusAvailable || !strings.Contains(string(normalized), `"schema_version":2`) {
		t.Fatalf("a newer schema version must survive: %q %s", status, normalized)
	}
}

func TestProjectRowReportsStatusAndWithholdsMalformedPayloads(t *testing.T) {
	row := []byte(`{"id":"delivery-1","payload":{"expected_version":1,"trigger_kind":"download_submitted"},"payload_digest":"sha256:x"}`)
	projected, err := ProjectRow(row)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(projected, &object); err != nil {
		t.Fatal(err)
	}
	if string(object["payload"]) != "null" {
		t.Fatalf("a malformed payload must be withheld, got %s", object["payload"])
	}
	if string(object["payload_status"]) != `"invalid"` {
		t.Fatalf("payload_status = %s, want invalid", object["payload_status"])
	}
	if string(object["payload_digest"]) != `"sha256:x"` {
		t.Fatalf("the frozen digest column must survive projection: %s", projected)
	}

	canonicalRow := []byte(`{"id":"delivery-2","payload":{"schema_version":1,"trigger_kind":"download_submitted","facts":{}}}`)
	projected, err = ProjectRow(canonicalRow)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(projected, &object); err != nil {
		t.Fatal(err)
	}
	if string(object["payload_status"]) != `"available"` {
		t.Fatalf("payload_status = %s, want available", object["payload_status"])
	}
	if string(object["payload"]) != `{"schema_version":1,"trigger_kind":"download_submitted","facts":{}}` {
		t.Fatalf("canonical payload = %s", object["payload"])
	}
}
