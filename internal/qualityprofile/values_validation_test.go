package qualityprofile

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseValuesRejectsIncompleteOrCorruptCache(t *testing.T) {
	valid := NewValues()
	valid.SourceDigest = strings.Repeat("a", 64)
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing digest", func(v map[string]any) { delete(v, "source_digest") }},
		{"bad digest", func(v map[string]any) { v["source_digest"] = "not-a-digest" }},
		{"missing values", func(v map[string]any) { delete(v, "values") }},
		{"retired key", func(v map[string]any) { v["values"] = map[string]any{"release.resolution": []string{"1080p"}} }},
		{"empty group", func(v map[string]any) { v["subgroup"] = map[string]string{"provider": "mikan", "group_ref": ""} }},
		{"empty item", func(v map[string]any) { v["values"] = map[string]any{KeyRtResolution: []string{""}} }},
		{"duplicate item", func(v map[string]any) { v["values"] = map[string]any{KeyRtSubLang: []string{"SC", "sc"}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			if err := json.Unmarshal(valid.JSON(), &body); err != nil {
				t.Fatal(err)
			}
			tc.mutate(body)
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseValues(raw); err == nil {
				t.Fatalf("accepted malformed cache: %s", raw)
			}
		})
	}
	if _, err := ParseValues(append(valid.JSON(), []byte(" {}")...)); err == nil {
		t.Fatal("accepted trailing JSON")
	}
	valid.Version = "compare-values-v1"
	if _, err := ParseValues(valid.JSON()); err == nil {
		t.Fatal("JSON promoted stale evidence version")
	}
}
