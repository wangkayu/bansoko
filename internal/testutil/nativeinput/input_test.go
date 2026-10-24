package nativeinput

import (
	"encoding/json"
	"strings"
	"testing"
)

// The single fixture schema validates against its registry schema (Canonical
// fails the test otherwise) and carries the flattened single-offset shape.
func TestCanonicalBuildsTheFlattenedSchema(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	var input struct {
		SchemaVersion int `json:"schema_version"`
		Config        struct {
			Sections []struct {
				SectionKey    string                     `json:"section_key"`
				SchemaVersion string                     `json:"schema_version"`
				Value         map[string]json.RawMessage `json:"value"`
			} `json:"sections"`
		} `json:"config"`
		Media  map[string]json.RawMessage `json:"media"`
		Source map[string]json.RawMessage `json:"source"`
	}
	if err := json.Unmarshal([]byte(Canonical(t, digest, digest)), &input); err != nil {
		t.Fatal(err)
	}
	if input.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", input.SchemaVersion)
	}
	if _, ok := input.Media["source_offset"]; ok {
		t.Fatal("media must not carry source_offset")
	}
	for _, key := range []string{"torrent_size", "torrent_file_count"} {
		if _, ok := input.Source[key]; !ok {
			t.Fatalf("source must carry %s", key)
		}
	}
	for _, section := range input.Config.Sections {
		if section.SectionKey == "parser" && (section.SchemaVersion != "parser-section-v1" || len(section.Value) != 1 || section.Value["episode_patterns"] == nil) {
			t.Fatalf("parser section = %+v, want parser-section-v1 with episode_patterns only", section)
		}
	}
}
