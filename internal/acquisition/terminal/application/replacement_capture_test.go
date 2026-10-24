package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	library "bansoko/internal/library/domain"
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestReplacementProjectionStrictSchema(t *testing.T) {
	size := int64(8)
	now := time.Date(2026, 10, 9, 1, 2, 3, 0, time.UTC)
	observed := library.PathObservation{SizeBytes: size, Identity: library.FileIdentity{Device: 1, Inode: 2}, ObservedAt: now}
	object := replacementObject(filepath.Join(t.TempDir(), "target"), observed)
	comparison := map[string]any{"policy": map[string]any{"ref": "anime", "version": int64(1)}, "candidate_quality": replacementQuality(replacementFactsSnapshot{Resolution: "1080p", Size: &size}), "existing_quality": replacementQuality(replacementFactsSnapshot{Resolution: "720p", Size: &size}), "compared_dimensions": []string{"resolution", "size"}, "verdict": "replace", "evaluated_at": protocol.FormatTime(now)}
	v, err := valueOf(map[string]any{"placement": map[string]any{"ref": "placement", "version": int64(1)}, "expected_object": object, "comparison": comparison})
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.Validate("ReplacementV1", v); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureReplacement(context.Background(), nil, "release", "/target"); err == nil {
		t.Fatal("missing db accepted")
	}
	if _, err := (&ReplacementCapture{}).recheck(context.Background(), nil, "release", "anime"); err == nil {
		t.Fatal("zero capture accepted")
	}
}
