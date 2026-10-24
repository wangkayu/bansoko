package application

import "testing"

func TestPlacementFingerprintSizeReadsRecordedSize(t *testing.T) {
	const hex = "3e47a27b2b57c149c9f7cb24d6fc514b853a7f58688a7fd7bab7adc815f76509"
	if got, ok := placementFingerprintSize(`{"value": "` + hex + `", "algorithm": "sha256", "size_bytes": 52}`); !ok || got != 52 {
		t.Fatalf("size document = %d, %v", got, ok)
	}
	for _, stored := range []string{`{"value": "` + hex + `", "algorithm": "sha256"}`, "sha256:" + hex, hex, `{"size_bytes": -1}`, `{`} {
		if _, ok := placementFingerprintSize(stored); ok {
			t.Errorf("placementFingerprintSize(%q) accepted a fingerprint without a recorded size", stored)
		}
	}
}
