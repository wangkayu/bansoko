// Package config owns the closed adapter-config codec.
//
// An adapter config is one JSON document that names its own adapter_kind and,
// for webhook, its authentication *kind*. It is dispatched on
// (config_schema_version, adapter_kind), decoded with DisallowUnknownFields and
// re-emitted as canonical, allowlisted JSON, so a request cannot smuggle a
// secret-bearing key (bot_token, endpoint_url, auth_value, or any field the
// contract does not declare) into storage, a frozen delivery snapshot, or a
// read response. Secret material only ever travels through the secret
// mutation/value path, never through this document.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// SchemaVersion is the only adapter config schema version this build knows.
const SchemaVersion = 1

// Status values reported by read surfaces. They mirror the contract's
// PluginAdapterConfigStatus enum.
const (
	StatusAvailable   = "available"
	StatusUnavailable = "unavailable"
	StatusInvalid     = "invalid"
)

// Adapter kinds the codec can dispatch.
var adapterKinds = map[string]bool{"telegram": true, "feishu": true, "webhook": true}

type telegramConfig struct {
	AdapterKind string `json:"adapter_kind"`
	ChatID      string `json:"chat_id"`
}

type feishuConfig struct {
	AdapterKind string `json:"adapter_kind"`
}

type webhookConfig struct {
	AdapterKind    string  `json:"adapter_kind"`
	AuthKind       string  `json:"auth_kind"`
	AuthHeaderName *string `json:"auth_header_name,omitempty"`
}

// Encode validates one adapter config against its declared schema version and
// adapter kind and returns the canonical bytes. The input must be a single JSON
// document: a decoding error, an undeclared field, a mismatched inner
// adapter_kind, or an out-of-domain value is rejected instead of being
// persisted. The returned bytes are exactly what a caller stores and what a
// delivery snapshot freezes.
func Encode(adapterKind string, configSchemaVersion int, raw []byte) (json.RawMessage, error) {
	canonical, err := encodeValidated(adapterKind, configSchemaVersion, raw)
	if err != nil {
		return nil, err
	}
	// Encoding must be a fixed point: a canonical document re-decodes to the
	// same bytes, so a stored config, a frozen snapshot and a read response can
	// never disagree about key order or shape.
	reencoded, err := encodeValidated(adapterKind, configSchemaVersion, canonical)
	if err != nil || !bytes.Equal(reencoded, canonical) {
		return nil, errors.New("config does not canonicalise")
	}
	return canonical, nil
}

// encodeValidated performs one decode-and-re-encode pass; Encode wraps it with
// the fixed-point check.
func encodeValidated(adapterKind string, configSchemaVersion int, raw []byte) (json.RawMessage, error) {
	if configSchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("config_schema_version %d is not supported", configSchemaVersion)
	}
	if !adapterKinds[adapterKind] {
		return nil, fmt.Errorf("adapter_kind %q is not supported", adapterKind)
	}
	var canonical json.RawMessage
	switch adapterKind {
	case "telegram":
		var decoded telegramConfig
		if err := decodeClosed(raw, &decoded); err != nil {
			return nil, err
		}
		if err := checkKind("telegram", decoded.AdapterKind); err != nil {
			return nil, err
		}
		if strings.TrimSpace(decoded.ChatID) == "" || len(decoded.ChatID) > 256 {
			return nil, errors.New("config.chat_id is required and bounded to 1..256 characters")
		}
		encoded, err := json.Marshal(decoded)
		if err != nil {
			return nil, fmt.Errorf("encode telegram adapter config: %w", err)
		}
		canonical = encoded
	case "feishu":
		var decoded feishuConfig
		if err := decodeClosed(raw, &decoded); err != nil {
			return nil, err
		}
		if err := checkKind("feishu", decoded.AdapterKind); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(decoded)
		if err != nil {
			return nil, fmt.Errorf("encode feishu adapter config: %w", err)
		}
		canonical = encoded
	case "webhook":
		var decoded webhookConfig
		if err := decodeClosed(raw, &decoded); err != nil {
			return nil, err
		}
		if err := checkKind("webhook", decoded.AdapterKind); err != nil {
			return nil, err
		}
		switch decoded.AuthKind {
		case "none", "bearer":
			if decoded.AuthHeaderName != nil && strings.TrimSpace(*decoded.AuthHeaderName) != "" {
				return nil, errors.New("config.auth_header_name is only valid for header auth")
			}
			decoded.AuthHeaderName = nil
		case "header":
			if decoded.AuthHeaderName == nil || !SafeAuthHeaderName(*decoded.AuthHeaderName) {
				return nil, errors.New("config.auth_header_name must be a safe HTTP header name")
			}
		default:
			return nil, errors.New("config.auth_kind must be none, bearer, or header")
		}
		encoded, err := json.Marshal(decoded)
		if err != nil {
			return nil, fmt.Errorf("encode webhook adapter config: %w", err)
		}
		canonical = encoded
	}
	return canonical, nil
}

// Canonicalize reports whether raw already is the canonical config for the
// given adapter kind and version, returning those bytes when it is.
func Canonicalize(adapterKind string, configSchemaVersion int, raw []byte) (json.RawMessage, bool) {
	canonical, err := Encode(adapterKind, configSchemaVersion, raw)
	if err != nil {
		return nil, false
	}
	return canonical, true
}

// Normalize projects one stored adapter config onto the read boundary. An empty
// or null config reports unavailable; a config that is not provably canonical
// reports invalid with the bytes withheld, so a legacy row can never carry an
// undeclared or secret-bearing field into a response.
func Normalize(adapterKind string, configSchemaVersion int, raw []byte) (json.RawMessage, string) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, StatusUnavailable
	}
	canonical, ok := Canonicalize(adapterKind, configSchemaVersion, trimmed)
	if !ok {
		return nil, StatusInvalid
	}
	return canonical, StatusAvailable
}

// ProjectRow rewrites one plugin target or delivery row projection: the config
// field becomes the validated canonical config (or null) and config_status
// records which case applied. Target rows carry config_schema_version; delivery
// rows carry the schema version their snapshot was frozen under.
func ProjectRow(row []byte, configField string) ([]byte, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(row, &object); err != nil {
		return nil, fmt.Errorf("decode plugin config row: %w", err)
	}
	if object == nil {
		return nil, errors.New("decode plugin config row: expected a JSON object")
	}
	// A row that cannot even name its adapter kind (or whose schema version is
	// not a number) cannot be projected: it is reported invalid with the bytes
	// withheld rather than failing the whole read.
	var adapterKind string
	if err := json.Unmarshal(object["adapter_kind"], &adapterKind); err != nil || adapterKind == "" {
		object[configField] = json.RawMessage("null")
		object["config_status"] = json.RawMessage(strconv.Quote(StatusInvalid))
		return json.Marshal(object)
	}
	schemaVersion := SchemaVersion
	if raw, ok := object["config_schema_version"]; ok {
		if err := json.Unmarshal(raw, &schemaVersion); err != nil {
			object[configField] = json.RawMessage("null")
			object["config_status"] = json.RawMessage(strconv.Quote(StatusInvalid))
			return json.Marshal(object)
		}
	}
	normalized, status := Normalize(adapterKind, schemaVersion, object[configField])
	if normalized == nil {
		object[configField] = json.RawMessage("null")
	} else {
		object[configField] = normalized
	}
	object["config_status"] = json.RawMessage(strconv.Quote(status))
	return json.Marshal(object)
}

func decodeClosed(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("config is not the declared shape: %w", err)
	}
	// A single JSON document only: trailing content would be silently dropped by
	// a plain Decode and could hide a second, unaudited object.
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return errors.New("config must be a single JSON document")
	}
	return nil
}

func checkKind(expected, actual string) error {
	if actual != expected {
		return fmt.Errorf("config.adapter_kind %q does not match adapter_kind %q", actual, expected)
	}
	return nil
}

// SafeAuthHeaderName reports whether a webhook auth header name may be used.
// Hop-by-hop and request-framing headers are refused so a config cannot rewrite
// the transport's own headers.
func SafeAuthHeaderName(value string) bool {
	if value == "" || forbiddenAuthHeaders[strings.ToLower(value)] || strings.HasPrefix(strings.ToLower(value), "proxy-") {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c))) {
			return false
		}
	}
	return true
}

var forbiddenAuthHeaders = map[string]bool{
	"host": true, "content-length": true, "cookie": true, "connection": true,
	"proxy-authorization": true, "proxy-authenticate": true, "transfer-encoding": true,
	"upgrade": true, "te": true, "trailer": true,
}
