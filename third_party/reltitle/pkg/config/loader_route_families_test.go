package config

import "testing"

func TestLoadAll_LoadsRouteFamilies(t *testing.T) {
	cfg, err := LoadAll("../../configs")
	if err != nil {
		t.Fatalf("LoadAll failed: %v", err)
	}
	if len(cfg.RouteFamilies.Families) == 0 {
		t.Fatalf("expected route families to be loaded")
	}
	foundRawMedia := false
	for _, fam := range cfg.RouteFamilies.Families {
		if fam.ID != "raw_media" {
			continue
		}
		foundRawMedia = true
		if !fam.Select.StrictTemplateFilter {
			t.Fatalf("raw_media strict_template_filter=false, want true")
		}
		if len(fam.Select.AllowedRouteIDs) == 0 {
			t.Fatalf("raw_media allowed_route_ids empty")
		}
	}
	if !foundRawMedia {
		t.Fatalf("raw_media family not found")
	}
}
