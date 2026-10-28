package filemaintenance

import (
	"encoding/json"
	"testing"

	"bansoko/internal/library/domain"
)

// The invocation attempt keeps nanosecond file timestamps exact: a float64
// round trip would shift a birth time and fail the adapter's identity check
// on filesystems without file handles (overlayfs, SMB, FUSE).
func TestNormalizeAttemptJSONKeepsNanosecondTimestamps(t *testing.T) {
	const generation int64 = 1793152508172414825
	const change int64 = 1793152508228415509
	raw := json.RawMessage(`{"z":1,  "capture_observation":{"identity":{"generation_nanos":1793152508172414825},"change_nanos":1793152508228415509}}`)
	normalized := normalizeAttemptJSON(raw)
	var attempt struct {
		CaptureObservation domain.FileObservation `json:"capture_observation"`
	}
	if err := json.Unmarshal(normalized, &attempt); err != nil {
		t.Fatal(err)
	}
	if got := attempt.CaptureObservation.Identity.GenerationNanos; got != generation {
		t.Fatalf("generation = %d, want %d (normalized %s)", got, generation, normalized)
	}
	if got := attempt.CaptureObservation.ChangeNanos; got != change {
		t.Fatalf("change = %d, want %d (normalized %s)", got, change, normalized)
	}
	if string(normalized) != `{"capture_observation":{"change_nanos":1793152508228415509,"identity":{"generation_nanos":1793152508172414825}},"z":1}` {
		t.Fatalf("normalized = %s", normalized)
	}
}
