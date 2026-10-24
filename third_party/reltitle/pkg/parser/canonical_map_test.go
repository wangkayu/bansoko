package parser

import (
	"reflect"
	"testing"

	"github.com/wangkayu/reltitle/pkg/config"
)

// Aliases that differ only by case collide once lower-cased. The result must not
// depend on map iteration order.
func TestBuildTypeCanonicalMapIsDeterministic(t *testing.T) {
	cfg := &config.Config{}
	cfg.DictCanonicalValues.Canonical = map[string]any{
		"mappings": map[string]any{
			"source_type_aliases": map[string]any{
				"WEB": "WEBRip",
				"Web": "WEB-DL",
				"web": "WEB",
			},
		},
	}

	want := buildTypeCanonicalMap(cfg, "source_type")
	// Keys are applied in sorted order ("WEB" < "Web" < "web"), so the last one wins.
	if got := want["web"]; got != "WEB" {
		t.Fatalf(`map["web"]=%q, want "WEB" (last colliding alias in key order)`, got)
	}
	for i := 0; i < 100; i++ {
		if got := buildTypeCanonicalMap(cfg, "source_type"); !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: map differs between runs: %v vs %v", i, got, want)
		}
	}
}
