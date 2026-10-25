package filemaintenance

import (
	"encoding/json"
	"strings"

	"bansoko/internal/library/domain"
)

// metadataFingerprintValue identifies path-level identity mode and unwraps the
// placement/cleanup fingerprint document. It accepts the raw metadata form too
// because durable attempt observations store that form directly. The boolean
// reports the declared mode, not validity: malformed metadata must still use
// the metadata observer and fail comparison, never fall back to a content read.
// A SHA-256 envelope is deliberately not a metadata fingerprint.
func metadataFingerprintValue(frozen string) (string, bool) {
	trimmed := strings.TrimSpace(frozen)
	if domain.IsMetadataFingerprint(trimmed) {
		return trimmed, true
	}
	if !strings.HasPrefix(trimmed, "{") {
		return "", false
	}
	var envelope struct {
		Algorithm string `json:"algorithm"`
		Value     string `json:"value"`
	}
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil || envelope.Algorithm != "metadata" {
		return "", false
	}
	return envelope.Value, true
}
