package transport

import (
	"encoding/json"
	"testing"
	"time"

	"bansoko/internal/release/facts"
)

func TestViewPreservesHTTPShape(t *testing.T) {
	created := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	raw := map[string]any{"title": "Show - 01"}
	size := int64(123)
	value := View(facts.View{
		ReleaseID:        "release-1",
		ReleaseVersion:   2,
		ParserVersion:    "candidate-facts-v1",
		InputFingerprint: "fingerprint",
		RawEvidence:      &raw,
		Facts:            map[string]any{"episode": "1"},
		CoverageKeys:     []string{"tv:e1"},
		Resolution:       stringPtr("1080p"),
		Revision:         stringPtr("v2"),
		Source:           stringPtr("rss"),
		Language:         stringPtr("sc"),
		Group:            stringPtr("Group"),
		SizeBytes:        &size,
		CreatedAt:        &created,
	})

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got["release_id"] != "release-1" || got["release_version"] != float64(2) || got["size_bytes"] != float64(123) {
		t.Fatalf("mapped JSON = %s", encoded)
	}
	if got["coverage_keys"].([]any)[0] != "tv:e1" {
		t.Fatalf("coverage_keys = %v", got["coverage_keys"])
	}
}

func stringPtr(value string) *string { return &value }
