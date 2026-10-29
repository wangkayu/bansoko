package domain

import (
	"encoding/json"
	"testing"

	"bansoko/internal/namingvars"
)

func TestVarsInputV1AcceptsTheBuiltFactObject(t *testing.T) {
	input := namingvars.BuildInput(namingvars.ReleaseEvidence{
		AnimeEvidence: namingvars.AnimeEvidence{AnimeID: "a1", AnimeTitle: "Show"},
		ReleaseTitle:  "Show - 01", Files: []namingvars.File{{Name: "a.mkv", Path: "a.mkv", Size: 3}},
	})
	known := map[string]any{"release_title": map[string]any{"state": "known", "value": "Show - 01", "reason_code": nil}, "files": []any{map[string]any{"name": "a.mkv", "path": "a.mkv", "size": 3}}, "facts": input.Facts}
	raw, err := json.Marshal(known)
	if err != nil {
		t.Fatal(err)
	}
	v, err := ParseStrict(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate("VarsInputV1", v); err != nil {
		t.Fatal(err)
	}
	// Inputs frozen before rt.version was introduced remain valid unchanged.
	delete(v.Object["facts"].Object["rt"].Object, "version")
	if err := Validate("VarsInputV1", v); err != nil {
		t.Fatalf("old input without rt.version rejected: %v", err)
	}
	// A release of several hundred files is described in full.
	file := v.Object["files"].Array[0]
	files := make([]Value, 619)
	for index := range files {
		files[index] = file
	}
	v.Object["files"] = Value{Kind: KindArray, Array: files}
	if err := Validate("VarsInputV1", v); err != nil {
		t.Fatalf("619-file input rejected: %v", err)
	}
	// The object is closed: an unknown member is refused.
	v.Object["facts"].Object["anime"].Object["kind"] = Value{Kind: KindString, String: "tv"}
	if err := Validate("VarsInputV1", v); err == nil {
		t.Fatal("unknown fact accepted")
	}
}
