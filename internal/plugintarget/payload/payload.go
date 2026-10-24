// Package payload owns the frozen notification delivery payload codec.
//
// One delivery persists exactly one canonical PluginDeliveryPayload —
// {"schema_version", "trigger_kind", "facts"}. The digest recorded on the
// delivery row is taken over those canonical bytes and the dispatcher sends the
// same frozen bytes, so a writer, a reader and a verifier all agree on one
// representation. Everything that is request identity rather than delivery
// content (a test request's expected_version, for example) belongs in the
// idempotency record, not here.
package payload

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	plugintargetport "bansoko/internal/plugintarget/port"
)

// SchemaVersion is the canonical schema version persisted with every delivery.
const SchemaVersion = 1

// Status values reported by the read boundary. They mirror the contract's
// PluginDeliveryPayloadStatus enum.
const (
	StatusAvailable   = "available"
	StatusUnavailable = "unavailable"
	StatusInvalid     = "invalid"
)

// TriggerKinds are the delivery triggers a canonical payload may carry,
// derived from the port's single registry. They were a second literal copy of
// the same seven values, so adding a trigger in the registry alone would have
// produced deliveries the codec rejects.
var TriggerKinds = func() map[string]bool {
	out := make(map[string]bool, len(plugintargetport.Triggers))
	for _, trigger := range plugintargetport.Triggers {
		out[trigger] = true
	}
	return out
}()

type canonical struct {
	SchemaVersion int             `json:"schema_version"`
	TriggerKind   string          `json:"trigger_kind"`
	Facts         json.RawMessage `json:"facts"`
}

// Encode returns the canonical bytes for one delivery payload. facts may be
// nil, which is persisted as an empty object: a test delivery has no owner
// facts and must not invent any. The returned bytes are exactly what the
// delivery row stores and what payload_digest is computed over.
func Encode(triggerKind string, facts map[string]any) ([]byte, error) {
	if !TriggerKinds[triggerKind] {
		return nil, fmt.Errorf("plugin delivery payload trigger %q is not supported", triggerKind)
	}
	if facts == nil {
		facts = map[string]any{}
	}
	factsJSON, err := json.Marshal(facts)
	if err != nil {
		return nil, fmt.Errorf("encode plugin delivery facts: %w", err)
	}
	raw, err := json.Marshal(canonical{SchemaVersion: SchemaVersion, TriggerKind: triggerKind, Facts: factsJSON})
	if err != nil {
		return nil, fmt.Errorf("encode plugin delivery payload: %w", err)
	}
	canonicalBytes, ok := Canonicalize(raw)
	if !ok {
		return nil, errors.New("plugin delivery payload does not canonicalise")
	}
	return canonicalBytes, nil
}

// Normalize projects one stored payload onto the read boundary. An empty or
// null payload reports unavailable; anything that is not the canonical shape
// reports invalid with the payload withheld, so a reader never re-serialises
// legacy bytes into a fabricated payload.
func Normalize(raw []byte) (json.RawMessage, string) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, StatusUnavailable
	}
	canonicalBytes, ok := Canonicalize(trimmed)
	if !ok {
		return nil, StatusInvalid
	}
	return canonicalBytes, StatusAvailable
}

// Canonicalize validates the canonical payload shape and returns its canonical
// bytes. The shape is closed on purpose: exactly the three contract keys, a
// schema version at or above the current one, an allowlisted trigger and an
// object of facts. Key order inside facts is normalised, so the digest of a
// payload survives a JSONB round trip.
func Canonicalize(raw []byte) (json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, false
	}
	if len(object) != 3 {
		return nil, false
	}
	for _, key := range []string{"schema_version", "trigger_kind", "facts"} {
		if _, ok := object[key]; !ok {
			return nil, false
		}
	}
	version, ok := object["schema_version"].(json.Number)
	if !ok {
		return nil, false
	}
	parsed, err := strconv.Atoi(version.String())
	if err != nil || parsed < SchemaVersion {
		return nil, false
	}
	triggerKind, ok := object["trigger_kind"].(string)
	if !ok || !TriggerKinds[triggerKind] {
		return nil, false
	}
	facts, ok := object["facts"].(map[string]any)
	if !ok {
		return nil, false
	}
	factsJSON, err := json.Marshal(facts)
	if err != nil {
		return nil, false
	}
	out, err := json.Marshal(canonical{SchemaVersion: parsed, TriggerKind: triggerKind, Facts: factsJSON})
	if err != nil {
		return nil, false
	}
	return out, true
}

// ProjectRow rewrites one delivery row projection: payload becomes the
// normalised payload (or null) and payload_status records which case applied.
// Every read surface projects rows through this function, so the dedicated
// endpoints and the global record union can never disagree about a payload.
func ProjectRow(row []byte) ([]byte, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(row, &object); err != nil {
		return nil, fmt.Errorf("decode plugin delivery row: %w", err)
	}
	if object == nil {
		return nil, errors.New("decode plugin delivery row: expected a JSON object")
	}
	normalized, status := Normalize(object["payload"])
	if normalized == nil {
		object["payload"] = json.RawMessage("null")
	} else {
		object["payload"] = normalized
	}
	object["payload_status"] = json.RawMessage(strconv.Quote(status))
	return json.Marshal(object)
}
