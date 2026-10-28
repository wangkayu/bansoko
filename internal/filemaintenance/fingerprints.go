package filemaintenance

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"bansoko/internal/library/domain"
)

// frozenFingerprintValue unwraps a frozen fingerprint envelope
// ({"algorithm","value"}) to its value; a bare value is returned as-is.
func frozenFingerprintValue(frozen string) (string, error) {
	trimmed := strings.TrimSpace(frozen)
	if !strings.HasPrefix(trimmed, "{") {
		return trimmed, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &fields); err != nil || fields == nil {
		return "", fmt.Errorf("decode source fingerprint: %w", err)
	}
	var algorithm, value string
	if err := json.Unmarshal(fields["algorithm"], &algorithm); err != nil {
		return "", errors.New("source fingerprint algorithm is invalid")
	}
	if err := json.Unmarshal(fields["value"], &value); err != nil {
		return "", errors.New("source fingerprint value is invalid")
	}
	switch algorithm {
	case "sha256":
		if !isHexFingerprint(value) {
			return "", errors.New("source fingerprint value is invalid")
		}
		return value, nil
	case "metadata":
		if _, ok := domain.ParseMetadataFingerprint(value); !ok {
			return "", errors.New("source metadata fingerprint is invalid")
		}
		return value, nil
	default:
		return "", errors.New("source fingerprint algorithm must be sha256 or metadata")
	}
}

func isHexFingerprint(value string) bool {
	if len(value) != 64 || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// fingerprintsEqual compares two frozen/observed representations after
// unwrapping their typed envelopes. Metadata uses size+inode (device excluded);
// SHA-256 keeps the case-insensitive digest comparison.
func fingerprintsEqual(left, right string) bool {
	leftValue, leftErr := frozenFingerprintValue(left)
	rightValue, rightErr := frozenFingerprintValue(right)
	if leftErr != nil || rightErr != nil {
		return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
	}
	if domain.IsMetadataFingerprint(leftValue) || domain.IsMetadataFingerprint(rightValue) {
		// Path-level identity (size, inode when both know it); a metadata
		// fingerprint never equals a content digest.
		return domain.MetadataFingerprintsMatch(leftValue, rightValue)
	}
	return strings.EqualFold(leftValue, rightValue)
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
