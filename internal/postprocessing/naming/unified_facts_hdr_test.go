package naming

import (
	"strings"
	"testing"

	"bansoko/internal/templatefacts"
)

// TestUnifiedFactsCarriesHDRIntoNamingContext pins media.hdr ← MediaFacts.HDR:
// the probed column vocabulary (hdr10 / hlg / dovi, or a comma-joined list)
// reaches the naming facts under the info root, and an unset value (SDR, a
// file the tool cannot read) is empty, never a rendered word.
func TestUnifiedFactsCarriesHDRIntoNamingContext(t *testing.T) {
	ctx := NamingContext{
		Anime: AnimeNamingFacts{Title: "Show"},
		File:  FileNamingFacts{OriginalName: "a.mkv", Basename: "a", Extension: "mkv", Kind: "video"},
	}
	unset := UnifiedFacts(ctx)
	if value := templatefacts.FlatValues(templatefacts.ScenarioNaming, &unset)["media.hdr"]; value != nil {
		t.Fatalf("unset HDR fact value = %#v, want nil", value)
	}

	ctx.File.Media.HDR = "hdr10,dovi"
	set := UnifiedFacts(ctx)
	facts := templatefacts.FlatValues(templatefacts.ScenarioNaming, &set)
	if value := facts["media.hdr"]; value != "hdr10,dovi" {
		t.Fatalf("media.hdr = %#v, want \"hdr10,dovi\"", value)
	}

	// The object shape keeps the info root present with the value and lets
	// the unnamed value read empty.
	if info, ok := facts["media.hdr"]; !ok || strings.ContainsAny(info.(string), " ") {
		t.Fatalf("media.hdr = %#v, want the one normalized token (no spaces)", info)
	}
}
