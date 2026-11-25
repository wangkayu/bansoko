package filemaintenance

import (
	"strings"
	"testing"

	"bansoko/internal/postprocessing/naming"
	providerdomain "bansoko/internal/provider/domain"
)

// contextWithoutOptionalMediaTags is a placed-file naming context as the
// maintenance render builds it for a file whose frozen evidence carries a
// resolution and a group but no video codec and no language, which is what the
// inspected fixture media facts in the real-chain suites look like.
func contextWithoutOptionalMediaTags(t *testing.T) naming.NamingContext {
	t.Helper()
	ctx := naming.NamingContext{}
	ctx.Anime.ID = "anime-1"
	ctx.Anime.Title = "Recon Real Show"
	ctx.Anime.TMDB.MediaKind = "tv"
	ctx.Anime.TMDB.Season = 1
	// The shipped TV directory names ${anime.year} as well, and that fact is the
	// bound work's provider observation — the same 2025 the real-chain fixture
	// records in metadata_work_titles before the release is accepted, which the
	// file effect freezes into its binding (binding_facts.tmdb_year) and this
	// render restores from. A placed file whose frozen evidence carries no year
	// is an unobserved binding, which the default rules refuse by name.
	year := 2025
	ctx.Anime.TMDB.Year = &year
	ctx.Derived.EffectiveTitle = "Recon Real Show"
	ctx.Source.Kind = "custom_rss"
	ctx.Source.GroupName = "RealGroup"
	ctx.Release.OriginalTitle = "[RealGroup] Recon Real Show - 01 [1080p]"
	ctx.Release.FrozenEpisode = &naming.FrozenEpisodeNamingFacts{Raw: "01", Number: "01", Display: "01"}
	ctx.File = naming.FileNamingFacts{
		OriginalName: "Recon Real Show - 01.mkv",
		Basename:     "Recon Real Show - 01",
		Extension:    "mkv",
		SizeBytes:    42,
		Kind:         "video",
	}
	ctx.File.Media.Resolution = "1080p"
	resolved, err := naming.BuildResolvedContext(ctx)
	if err != nil {
		t.Fatalf("resolve naming context: %v", err)
	}
	return resolved.Context
}

// TestMissingTemplateFactsIgnoresOptionalMediaTags is the regression for the
// qb-real-chain failure: the shipped filename template reads the codec and
// language tags only inside a conditional, so a placed file whose evidence
// carries neither must still re-render. Blocking it left the alignment planner
// with no executable candidate, so `TestReconciliationRealChain` never saw the
// placement converge on the target the file effect had rendered.
func TestMissingTemplateFactsIgnoresOptionalMediaTags(t *testing.T) {
	engine := naming.NewEngine()
	// The required-fact refusal is naming-render-1 semantics, replayed for a
	// placement that recorded no renderer version.
	legacy, err := engine.ForRenderer("")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := engine.CompileRules(naming.DefaultNamingRules())
	if err != nil {
		t.Fatalf("compile default rules: %v", err)
	}
	resolved := contextWithoutOptionalMediaTags(t)
	// From naming-render-2 on an empty required fact rendered empty at
	// placement too, so only the frozen-evidence guard remains.
	if got := missingTemplateFacts(engine, []string{"media.video_codec"}, nil, resolved); len(got) != 0 {
		t.Fatalf("naming-render-2 replay must not refuse an empty fact by itself, got %v", got)
	}
	if got := missingTemplateFacts(engine, nil, map[string]string{"File.Media.VideoCodec": "HEVC"}, resolved); strings.Join(got, ",") != "File.Media.VideoCodec" {
		t.Fatalf("naming-render-2 replay keeps the frozen-evidence guard, got %v", got)
	}
	if resolved.Parsed.Episode.Display == "" {
		t.Fatalf("fixture must resolve the episode the default template requires")
	}
	if missing := missingTemplateFacts(legacy, compiled.TV.RequiredFacts(), nil, resolved); len(missing) != 0 {
		t.Fatalf("default rules must not require the optional media tags, got missing=%v", missing)
	}

	// The guard still refuses a fact the template reads where an empty value
	// changes the name.
	strict := naming.DefaultNamingRules()
	strict.TV.VideoFilenameTemplate = "${media.video_codec}.${file.ext}"
	strictCompiled, err := engine.CompileRules(strict)
	if err != nil {
		t.Fatalf("compile strict rules: %v", err)
	}
	missing := missingTemplateFacts(legacy, strictCompiled.TV.RequiredFacts(), nil, resolved)
	if strings.Join(missing, ",") != "media.video_codec" {
		t.Fatalf("missing = %v, want [media.video_codec]", missing)
	}

	// A fact the file effect recorded as non-empty must not disappear from the
	// maintenance render even when the template only reads it conditionally:
	// the effect rendered it, so an empty value now is lost evidence, not the
	// template's own handling.
	resolved.File.Media.VideoCodec = ""
	if got := missingTemplateFacts(legacy, compiled.TV.RequiredFacts(), map[string]string{"File.Media.VideoCodec": "HEVC"}, resolved); strings.Join(got, ",") != "File.Media.VideoCodec" {
		t.Fatalf("a recorded fact that went missing must block, got %v", got)
	}
	// A fact the effect never had stays absent without blocking: the shipped
	// template omits its tag, exactly as the effect did.
	if got := missingTemplateFacts(legacy, compiled.TV.RequiredFacts(), map[string]string{"File.Media.VideoCodec": ""}, resolved); len(got) != 0 {
		t.Fatalf("an optional fact the effect never had must not block, got %v", got)
	}

	// A required non-media fact behaves the same way. The shipped TV filename
	// names the episode through the ep.s00e00 shorthand outside any conditional
	// or fallback, so a frozen identity without an episode number (the
	// composite renders empty) is refused instead of rendering an episode-less
	// name.
	episode := resolved
	episode.Parsed.Episode.Display, episode.Parsed.Episode.Raw = "", ""
	if got := missingTemplateFacts(legacy, compiled.TV.RequiredFacts(), nil, episode); strings.Join(got, ",") != "ep.number" {
		t.Fatalf("missing = %v, want [ep.number]", got)
	}
}

// TestMissingTemplateFactsGuardsTheSourceGroupIdentity: the source group
// identity is frozen evidence on the same footing as Source.GroupName. An effect
// that rendered it must not re-render without it, so a maintenance render whose
// lineage no longer supplies the identity refuses instead of renaming the file
// away from what acquisition committed.
func TestMissingTemplateFactsGuardsTheSourceGroupIdentity(t *testing.T) {
	engine := naming.NewEngine()
	legacy := engine
	compiled, err := engine.CompileRules(naming.DefaultNamingRules())
	if err != nil {
		t.Fatalf("compile default rules: %v", err)
	}
	grouped := contextWithoutOptionalMediaTags(t)
	grouped.Source.Kind = "provider_group"
	grouped.Source.Group = providerdomain.GroupRef{Provider: providerdomain.ProviderAniBT, ExternalID: "samplegroup"}
	recorded := naming.ReplayFactValues(grouped)
	if recorded["Source.Group"] != "anibt:samplegroup" || recorded["Source.GroupName"] != "RealGroup" {
		t.Fatalf("the effect must freeze the group identity, got %q / %q", recorded["Source.Group"], recorded["Source.GroupName"])
	}
	if got := missingTemplateFacts(legacy, compiled.TV.RequiredFacts(), recorded, grouped); len(got) != 0 {
		t.Fatalf("the frozen evidence must satisfy its own render, got %v", got)
	}
	// The lineage no longer carries the provider identity: what the effect
	// rendered cannot be reproduced, so the render is refused.
	grouped.Source.Group = providerdomain.GroupRef{}
	if got := missingTemplateFacts(legacy, compiled.TV.RequiredFacts(), recorded, grouped); strings.Join(got, ",") != "Source.Group" {
		t.Fatalf("lost group identity must block, got %v", got)
	}
}
