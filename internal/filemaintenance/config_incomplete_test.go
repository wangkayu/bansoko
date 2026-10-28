package filemaintenance

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
)

func completeAnimeConfig() currentAnimeConfig {
	return currentAnimeConfig{
		bgmWorkID: "bgm-1", tmdbWorkID: "tmdb-1", tmdbBindingKind: "tv",
		tmdbSeason: sql.NullInt64{Int64: 2, Valid: true}, ownerSeason: sql.NullInt64{Int64: 3, Valid: true},
		ownerYear: sql.NullInt64{Int64: 2021, Valid: true},
		titles:    namingvars.AnimeEvidence{BgmTitle: "bgm title", TmdbTitleCN: "tmdb 标题"},
	}
}

func TestCurrentAnimeConfigCheckReasons(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*currentAnimeConfig)
		want   string
	}{
		{name: "complete", mutate: func(*currentAnimeConfig) {}},
		{name: "no bgm binding", mutate: func(c *currentAnimeConfig) { c.bgmWorkID = "  " }, want: "config_incomplete:binding_missing:bgm"},
		{name: "no tmdb binding", mutate: func(c *currentAnimeConfig) { c.tmdbWorkID = "" }, want: "config_incomplete:binding_missing:tmdb"},
		{name: "both missing reports bgm first", mutate: func(c *currentAnimeConfig) { c.bgmWorkID, c.tmdbWorkID = "", "" }, want: "config_incomplete:binding_missing:bgm"},
		{name: "unset owner kind is tv", mutate: func(c *currentAnimeConfig) { c.mediaKindOverride = sql.NullString{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := completeAnimeConfig()
			tc.mutate(&config)
			if got := RenderExclusionReason(config.check()); got != tc.want {
				t.Fatalf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCurrentAnimeConfigApplyReadsOwnerValues(t *testing.T) {
	var facts naming.AnimeNamingFacts
	facts.Season = new(int) // a stale value is replaced, not kept
	completeAnimeConfig().apply(&facts)
	if facts.Season == nil || *facts.Season != 3 || facts.Year == nil || *facts.Year != 2021 {
		t.Fatalf("owner season/year = %v/%v, want 3/2021", facts.Season, facts.Year)
	}
	if facts.TMDB.MediaKind != "tv" || facts.TMDB.Season != 2 || !facts.TMDB.SeasonPresent || facts.TMDB.SeasonBound {
		t.Fatalf("tmdb facts = %+v", facts.TMDB)
	}
	if facts.BGM.WorkID != "bgm-1" || facts.TMDB.WorkID != "tmdb-1" || facts.BGM.Title != "bgm title" || facts.TMDB.TitleCN != "tmdb 标题" {
		t.Fatalf("binding facts = %+v / %+v", facts.BGM, facts.TMDB)
	}

	movie := completeAnimeConfig()
	movie.mediaKindOverride = sql.NullString{String: "movie", Valid: true}
	movie.tmdbSeason = sql.NullInt64{Int64: 0, Valid: true}
	movie.apply(&facts)
	if facts.TMDB.MediaKind != "movie" || !facts.TMDB.SeasonBound {
		t.Fatalf("movie tmdb facts = %+v", facts.TMDB)
	}
}

func TestCurrentAnimeConfigEmptyOwnerValuesRenderEmpty(t *testing.T) {
	config := completeAnimeConfig()
	config.ownerSeason, config.ownerYear = sql.NullInt64{}, sql.NullInt64{}
	rules := naming.NamingRules{
		Language: naming.Language,
		TV: naming.NamingVariant{
			DirectoryTemplate:     "/library/${anime.season_folder ? anime.season_folder : 'no-season'}/${anime.year ? anime.year : 'no-year'}",
			VideoFilenameTemplate: "episode.mkv",
		},
		Movie: naming.NamingVariant{DirectoryTemplate: "/library/movie", VideoFilenameTemplate: "episode.mkv"},
	}
	facts := naming.NamingContext{
		File:    naming.FileNamingFacts{OriginalName: "a.mkv", Basename: "a", Extension: "mkv", Kind: "video"},
		Release: naming.ReleaseNamingFacts{FrozenEpisode: &naming.FrozenEpisodeNamingFacts{Raw: "1", Number: "1", Display: "1", Source: "file"}},
	}
	config.apply(&facts.Anime)
	vars, err := naming.WithVars(facts, map[string]map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	resolved := naming.DefaultRegistry().ResolveAll(naming.ResolverInput{Facts: vars})
	got, err := renderResolvedTarget("a.mkv", rules, resolved.Context, nil, naming.CurrentConfigRenderer(""))
	if err != nil {
		t.Fatal(err)
	}
	if want := "/library/no-season/no-year/episode.mkv"; got != want {
		t.Fatalf("target = %q, want %q", got, want)
	}
}

func TestRenderResolvedTargetUndefinedVariableIsConfigIncomplete(t *testing.T) {
	rules := naming.NamingRules{
		Language: naming.Language,
		TV:       naming.NamingVariant{DirectoryTemplate: "/library/${mikan.groupid}", VideoFilenameTemplate: "episode.mkv"},
		Movie:    naming.NamingVariant{DirectoryTemplate: "/library/movie", VideoFilenameTemplate: "episode.mkv"},
	}
	facts := naming.NamingContext{
		File:    naming.FileNamingFacts{OriginalName: "a.mkv", Basename: "a", Extension: "mkv", Kind: "video"},
		Release: naming.ReleaseNamingFacts{FrozenEpisode: &naming.FrozenEpisodeNamingFacts{Raw: "1", Number: "1", Display: "1", Source: "file"}},
	}
	completeAnimeConfig().apply(&facts.Anime)
	facts, err := naming.WithVars(facts, map[string]map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	resolved := naming.DefaultRegistry().ResolveAll(naming.ResolverInput{Facts: facts})
	_, err = renderResolvedTarget("a.mkv", rules, resolved.Context, nil, naming.CurrentConfigRenderer(""))
	var incomplete *ConfigIncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("error = %v, want ConfigIncompleteError", err)
	}
	if got, want := RenderExclusionReason(err), "config_incomplete:template_variable_undefined:mikan.groupid"; got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
}

func TestExclusionReasonCodes(t *testing.T) {
	if got := RenderExclusionReason(fmt.Errorf("wrapped: %w", ErrPlacementExcluded)); got != ReasonPlacementExcluded {
		t.Fatalf("excluded reason = %q", got)
	}
	if got := RenderExclusionReason(errors.New("boom")); got != "" {
		t.Fatalf("plain error reason = %q, want none", got)
	}
	for _, reason := range []string{
		"config_incomplete:binding_missing:bgm", "config_incomplete:binding_missing:tmdb",
		"config_incomplete:template_variable_undefined:vars.a,vars.b",
	} {
		if !isConfigIncompleteReason(reason) || normalizeAlignmentExcludeReasonCode(reason) != reason {
			t.Fatalf("%q is not kept as an alignment exclusion reason", reason)
		}
	}
	for _, reason := range []string{"config_incomplete:", "config_incomplete:other", "config_incomplete:template_variable_undefined:"} {
		if isConfigIncompleteReason(reason) || normalizeAlignmentExcludeReasonCode(reason) != "unknown" {
			t.Fatalf("%q must normalize to unknown", reason)
		}
	}
	if normalizeAlignmentExcludeReasonCode(ReasonPlacementExcluded) != ReasonPlacementExcluded {
		t.Fatal("placement_excluded must be kept")
	}
}

func TestCurrentConfigRendererKeepsOwnerSeasonVersions(t *testing.T) {
	for recorded, want := range map[string]string{
		"": "naming-render-4", "naming-render-1": "naming-render-4", "naming-render-3": "naming-render-4",
		"naming-render-4": "naming-render-4", "naming-render-5": "naming-render-5", naming.RendererVersion: naming.RendererVersion,
	} {
		if got := naming.CurrentConfigRenderer(recorded); got != want {
			t.Fatalf("CurrentConfigRenderer(%q) = %q, want %q", recorded, got, want)
		}
	}
}
