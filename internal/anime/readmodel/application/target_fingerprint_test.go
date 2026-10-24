package application

import (
	"database/sql"
	"testing"
)

func TestDecodeTargetFingerprintFallsBackToExpectedSize(t *testing.T) {
	expected := sql.NullInt64{Int64: 52, Valid: true}
	legacy := decodeTargetFingerprint([]byte(`{"algorithm":"sha256","value":"ab"}`), expected)
	if legacy == nil || legacy.SizeBytes != 52 || legacy.Value != "ab" {
		t.Fatalf("pre-186 fingerprint = %+v, want size 52 from expected_size_bytes", legacy)
	}
	stored := decodeTargetFingerprint([]byte(`{"algorithm":"sha256","value":"ab","size_bytes":0}`), expected)
	if stored == nil || stored.SizeBytes != 0 {
		t.Fatalf("a stored size_bytes must win, even 0: %+v", stored)
	}
	if got := decodeTargetFingerprint([]byte(`{"algorithm":"sha256","value":"ab"}`), sql.NullInt64{}); got == nil || got.SizeBytes != 0 {
		t.Fatalf("no expected size leaves 0: %+v", got)
	}
}
