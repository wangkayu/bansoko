package naming

import (
	"strings"
	"testing"

	"bansoko/internal/templatefacts"
)

// fakeResolver is a test double for registry validation and ordering.
type fakeResolver struct {
	definition ResolverDefinition
}

func (f fakeResolver) Definition() ResolverDefinition { return f.definition }
func (fakeResolver) Resolve(ResolverInput) ResolverResult {
	return ResolverResult{State: ResolverStateNull}
}

func fakeDefinition(key string, dependencies ...string) ResolverDefinition {
	return ResolverDefinition{
		Key:          key,
		OutputType:   "string",
		Dependencies: dependencies,
		Nullable:     true,
	}
}

func TestRegistryValidateRejectsDuplicateKeys(t *testing.T) {
	registry := NewRegistry(
		fakeResolver{definition: fakeDefinition("a")},
		fakeResolver{definition: fakeDefinition("a")},
	)
	err := registry.Validate()
	if err == nil {
		t.Fatal("expected duplicate key error")
	}
	namingErr, ok := err.(*Error)
	if !ok || namingErr.Code != ErrCodeResolverCatalogInvalid {
		t.Fatalf("expected %s, got %v", ErrCodeResolverCatalogInvalid, err)
	}
}

func TestRegistryValidateRejectsUnknownDependency(t *testing.T) {
	registry := NewRegistry(fakeResolver{definition: fakeDefinition("a", "missing")})
	err := registry.Validate()
	if err == nil {
		t.Fatal("expected unknown dependency error")
	}
	if namingErr, ok := err.(*Error); !ok || namingErr.Code != ErrCodeResolverCatalogInvalid {
		t.Fatalf("expected %s, got %v", ErrCodeResolverCatalogInvalid, err)
	}
}

func TestRegistryValidateRejectsCycle(t *testing.T) {
	registry := NewRegistry(
		fakeResolver{definition: fakeDefinition("a", "b")},
		fakeResolver{definition: fakeDefinition("b", "a")},
	)
	err := registry.Validate()
	if err == nil {
		t.Fatal("expected dependency cycle error")
	}
	if namingErr, ok := err.(*Error); !ok || namingErr.Code != ErrCodeResolverCatalogInvalid {
		t.Fatalf("expected %s, got %v", ErrCodeResolverCatalogInvalid, err)
	}
}

func TestRegistryValidateRejectsIncompleteDefinition(t *testing.T) {
	definition := fakeDefinition("a")
	definition.Key = ""
	registry := NewRegistry(fakeResolver{definition: definition})
	if err := registry.Validate(); err == nil {
		t.Fatal("expected incomplete definition error")
	}
}

func TestRegistryTopologicalOrderIsStable(t *testing.T) {
	registry := NewRegistry(
		fakeResolver{definition: fakeDefinition("c", "a", "b")},
		fakeResolver{definition: fakeDefinition("b", "a")},
		fakeResolver{definition: fakeDefinition("a")},
	)
	if err := registry.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	order, err := registry.topologicalOrder()
	if err != nil {
		t.Fatalf("topological order: %v", err)
	}
	want := []string{"a", "b", "c"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for index, key := range want {
		if order[index] != key {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestDefaultRegistryCatalog(t *testing.T) {
	registry := DefaultRegistry()
	if err := registry.Validate(); err != nil {
		t.Fatalf("default registry invalid: %v", err)
	}
	expectations := map[string][]string{
		"parsed.episode":               nil,
		"parsed.language":              nil,
		"parsed.subtitle":              {"parsed.language"},
		"release.subtitle_mode":        nil,
		"derived.effective_title":      nil,
		"derived.season_folder":        nil,
		"derived.effective_media_kind": nil,
	}
	if len(registry.resolvers) != len(expectations) {
		t.Fatalf("default registry size = %d, want %d", len(registry.resolvers), len(expectations))
	}
	for key, expectedDependencies := range expectations {
		resolver, ok := registry.resolvers[key]
		if !ok {
			t.Fatalf("default registry misses resolver %q", key)
		}
		definition := resolver.Definition()
		if len(definition.Dependencies) != len(expectedDependencies) {
			t.Errorf("%s dependencies = %v, want %v", key, definition.Dependencies, expectedDependencies)
			continue
		}
		for index, dependency := range expectedDependencies {
			if definition.Dependencies[index] != dependency {
				t.Errorf("%s dependencies = %v, want %v", key, definition.Dependencies, expectedDependencies)
			}
		}
	}
}

func resolveSingle(t *testing.T, resolver Resolver, input ResolverInput) ResolverResult {
	t.Helper()
	result := resolver.Resolve(input)
	if result.Key != resolver.Definition().Key {
		t.Fatalf("result key = %q, want %q", result.Key, resolver.Definition().Key)
	}
	return result
}

func episodeInput(title string, frozen *FrozenEpisodeNamingFacts) ResolverInput {
	return ResolverInput{Facts: NamingContext{Release: ReleaseNamingFacts{OriginalTitle: title, FrozenEpisode: frozen}}}
}

func TestEpisodeResolverFrozenIdentityWins(t *testing.T) {
	// The frozen identity is the only episode authority: naming uses it even
	// when the title carries no episode.
	frozen := &FrozenEpisodeNamingFacts{Raw: "07", Number: "7", Offset: 0, Display: "7"}
	result := resolveSingle(t, episodeResolver{}, episodeInput("[Group] Show [1080p]", frozen))
	if result.State != ResolverStateValue {
		t.Fatalf("state = %q, want value", result.State)
	}
	value := result.Value.(episodeValue)
	if value.Display != "7" || value.Raw != "07" || value.Number != 7 {
		t.Fatalf("value = %+v, want 07/7/7", value)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].RuleID != "frozen_episode_identity" {
		t.Fatalf("evidence = %+v, want frozen_episode_identity", result.Evidence)
	}
}

func TestEpisodeResolverFrozenOffsetIsNotReapplied(t *testing.T) {
	frozen := &FrozenEpisodeNamingFacts{Raw: "12", Number: "15", Offset: 3, Display: "15"}
	result := resolveSingle(t, episodeResolver{}, episodeInput("[Group] Show - 12 [1080p]", frozen))
	value := result.Value.(episodeValue)
	if value.Number != 15 || value.Offset != 3 {
		t.Fatalf("number=%d offset=%d, want 15/3 (offset already frozen, never re-applied)", value.Number, value.Offset)
	}
}

// Naming never reads an episode from the release title: without a frozen
// identity parsed.episode stays null, however clearly the title numbers it.
func TestEpisodeResolverNeverParsesTheTitle(t *testing.T) {
	result := resolveSingle(t, episodeResolver{}, episodeInput("[Group] Show - 09 [1080p]", nil))
	if result.State != ResolverStateNull {
		t.Fatalf("state = %q value %v, want null", result.State, result.Value)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != "episode_identity_unresolved" {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
}

func TestEpisodeResolverSpecialKind(t *testing.T) {
	frozen := &FrozenEpisodeNamingFacts{Raw: "1", Number: "1", Display: "1"}
	for _, tc := range []struct {
		title string
		want  string
	}{
		{"Title NCOP 1", "ncop"},
		{"Title nced 2", "nced"},
		{"Title Special 3", "special"},
		{"Title 04", ""},
	} {
		result := resolveSingle(t, episodeResolver{}, episodeInput(tc.title, frozen))
		if got := result.Value.(episodeValue).SpecialKind; got != tc.want {
			t.Errorf("%q: special kind = %q, want %q", tc.title, got, tc.want)
		}
	}
}

func languageInput(title string) ResolverInput {
	return ResolverInput{Facts: NamingContext{Release: ReleaseNamingFacts{OriginalTitle: title}}}
}

func TestLanguageResolverPriority(t *testing.T) {
	cases := []struct {
		title     string
		code      string
		bcp47     string // the tags of languages, joined with ","
		languages []string
	}{
		{"[SampleGroup] Title - 01 [WebRip 1080p][简繁日内封]", "jpsc", "zh-Hans,zh-Hant", []string{"jpsc", "jptc"}},
		{"[SampleGroup] Title - 01 [WebRip 1080p][CHS&CHT]", "sc", "zh-Hans,zh-Hant", []string{"sc", "tc"}},
		{"[SampleGroup] Title - 01 [WebRip 1080p][CHS]", "sc", "zh-Hans", []string{"sc"}},
		{"[SampleGroup] Title - 01 [WebRip 1080p][CHT]", "tc", "zh-Hant", []string{"tc"}},
		{"[SampleGroup] Title - 01 [WebRip 1080p][JPN]", "jpn", "ja", []string{"jpn"}},
		{"[SampleGroup] Title - 01 [WebRip 1080p][ENG]", "eng", "en", []string{"eng"}},
		{"[SampleGroup] Title - 01 [WebRip 1080p][chs]", "sc", "zh-Hans", []string{"sc"}},
		{"[SampleGroup] Title - 01 [WebRip 1080p][cHs]", "sc", "zh-Hans", []string{"sc"}},
	}
	for _, tc := range cases {
		result := resolveSingle(t, languageResolver{}, languageInput(tc.title))
		if result.State != ResolverStateValue {
			t.Fatalf("%q: state = %q, diagnostics = %+v", tc.title, result.State, result.Diagnostics)
		}
		value := result.Value.(languageValue)
		if value.Code != tc.code {
			t.Errorf("%q: code = %q, want %q", tc.title, value.Code, tc.code)
		}
		if strings.Join(value.BCP47, ",") != tc.bcp47 {
			t.Errorf("%q: bcp47 = %v, want [%s]", tc.title, value.BCP47, tc.bcp47)
		}
		if strings.Join(value.Languages, ",") != strings.Join(tc.languages, ",") {
			t.Errorf("%q: languages = %v, want %v", tc.title, value.Languages, tc.languages)
		}
		if len(result.Evidence) != 1 || result.Evidence[0].MatchedSpan == nil {
			t.Errorf("%q: missing span evidence: %+v", tc.title, result.Evidence)
		}
	}
}

func TestLanguageResolverUnmatchedNeverDefaultsChinese(t *testing.T) {
	result := resolveSingle(t, languageResolver{}, languageInput("[SampleGroup] Title - 07 [WebRip 1080p HEVC-10bit AAC]"))
	if result.State != ResolverStateNull {
		t.Fatalf("state = %q, want null", result.State)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != "language_unmatched" {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
}

// TestLanguageResolverReadsOnlyTheReleaseTitle pins the release namespace
// boundary: release.lang is the release's own language and never the file
// name. A subtitle file whose suffix names a language keeps that in sub.*
// (file first, sub.lang_source=file); release.lang still reads the release
// title alone, and a release with no title stays null with its diagnostic.
func TestLanguageResolverReadsOnlyTheReleaseTitle(t *testing.T) {
	// A subtitle file whose own name says TC, inside a release titled in SC:
	// the two namespaces answer separately instead of the file name deciding
	// the release's language.
	input := subtitleInput("Show - 01.TC.ass", "Show - 01.TC", "ass", "subtitle")
	input.Facts.Release.OriginalTitle = "[SampleGroup] Show - 01 [WebRip 1080p][CHS]"
	result := resolveSingle(t, languageResolver{}, input)
	if result.State != ResolverStateValue {
		t.Fatalf("state = %q, diagnostics = %+v", result.State, result.Diagnostics)
	}
	value := result.Value.(languageValue)
	if value.Code != "sc" {
		t.Fatalf("release.lang = %q, want sc from the release title (never the file name)", value.Code)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].InputPath != "Release.OriginalTitle" {
		t.Fatalf("evidence = %+v, want the release title", result.Evidence)
	}

	// The same file with no release title has no release language: the file's
	// own TC never stands in for it.
	bare := subtitleInput("Show - 01.TC.ass", "Show - 01.TC", "ass", "subtitle")
	if got := resolveSingle(t, languageResolver{}, bare); got.State != ResolverStateNull {
		t.Fatalf("state = %q, want null (the file name is not release evidence)", got.State)
	}
}

func subtitleInput(originalName, basename, extension, kind string) ResolverInput {
	return ResolverInput{Facts: NamingContext{File: FileNamingFacts{
		OriginalName: originalName,
		Basename:     basename,
		Extension:    extension,
		Kind:         kind,
	}}}
}

func TestSubtitleResolverNonSubtitleIsSilentNull(t *testing.T) {
	result := resolveSingle(t, subtitleResolver{}, subtitleInput("Show - 01.mkv", "Show - 01", "mkv", "video"))
	if result.State != ResolverStateNull {
		t.Fatalf("state = %q, want null", result.State)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("expected no diagnostics, got %+v", result.Diagnostics)
	}
}

func TestSubtitleResolverUnknownFormat(t *testing.T) {
	result := resolveSingle(t, subtitleResolver{}, subtitleInput("Show - 01.txt", "Show - 01", "txt", "subtitle"))
	if result.State != ResolverStateNull {
		t.Fatalf("state = %q, want null", result.State)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != "subtitle_format_unknown" {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
}

func TestSubtitleResolverSuffixLanguagesAndFlags(t *testing.T) {
	result := resolveSingle(t, subtitleResolver{}, subtitleInput("Show - 01.cht_forced.srt", "Show - 01.cht_forced", "srt", "subtitle"))
	if result.State != ResolverStateValue {
		t.Fatalf("state = %q, diagnostics = %+v", result.State, result.Diagnostics)
	}
	value := result.Value.(subtitleValue)
	if value.Format != "srt" || value.OriginalSuffix != "cht_forced" {
		t.Errorf("unexpected subtitle value: %+v", value)
	}
	if len(value.Languages) != 1 || value.Languages[0] != "zh-Hant" || value.PrimaryLanguage != "zh-Hant" {
		t.Errorf("languages = %v primary = %q", value.Languages, value.PrimaryLanguage)
	}
	if !value.Flags.Forced || value.Flags.SDH || value.Flags.Commentary || value.Flags.Default {
		t.Errorf("flags = %+v", value.Flags)
	}
	// Suffix span evidence must cover the suffix inside File.OriginalName.
	var suffixEvidence *ResolverEvidence
	for index := range result.Evidence {
		if result.Evidence[index].RuleID == "subtitle_suffix" {
			suffixEvidence = &result.Evidence[index]
		}
	}
	if suffixEvidence == nil || suffixEvidence.MatchedSpan == nil {
		t.Fatalf("missing suffix span evidence: %+v", result.Evidence)
	}
	span := suffixEvidence.MatchedSpan
	if got := "Show - 01.cht_forced.srt"[span.Start:span.End]; got != "cht_forced" {
		t.Errorf("suffix span covers %q, want %q", got, "cht_forced")
	}
}

func TestSubtitleResolverUnknownTokenIsUnd(t *testing.T) {
	result := resolveSingle(t, subtitleResolver{}, subtitleInput("Show - 01.xyx.ass", "Show - 01.xyx", "ass", "subtitle"))
	if result.State != ResolverStateValue {
		t.Fatalf("state = %q", result.State)
	}
	value := result.Value.(subtitleValue)
	if len(value.Languages) != 1 || value.Languages[0] != "und" {
		t.Fatalf("languages = %v, want [und]", value.Languages)
	}
}

func TestSubtitleResolverMultiLanguageDedup(t *testing.T) {
	result := resolveSingle(t, subtitleResolver{}, subtitleInput("Show - 01.chs_cht_chs.ass", "Show - 01.chs_cht_chs", "ass", "subtitle"))
	value := result.Value.(subtitleValue)
	if len(value.Languages) != 2 || value.Languages[0] != "zh-Hans" || value.Languages[1] != "zh-Hant" {
		t.Fatalf("languages = %v, want [zh-Hans zh-Hant]", value.Languages)
	}
}

func TestSubtitleModeResolver(t *testing.T) {
	// The mode derives from the release's parsed subtitle flags (rt.sub_flag):
	// soft or hard alone embedded, ex alone external, ex beside soft/hard
	// mixed, no flag value unknown.
	cases := []struct {
		name  string
		rt    templatefacts.Values
		title string
		want  string
	}{
		{"soft alone", templatefacts.Values{"SubFlag": "soft"}, "[Group] Title - 01 [简繁日内封]", "embedded"},
		{"hard alone", templatefacts.Values{"SubFlag": "hard"}, "[Group] Title - 01 [内嵌简体]", "embedded"},
		{"ex alone", templatefacts.Values{"SubFlag": "ex"}, "[Group] Title - 01 [简繁外挂]", "external"},
		{"soft and ex", templatefacts.Values{"SubFlag": "soft+ex"}, "[Group] Title - 01 [内封简繁][外挂英日]", "mixed"},
		{"hard and ex", templatefacts.Values{"SubFlag": "hard+ex"}, "[Group] Title - 01 [内嵌][外挂]", "mixed"},
		{"no flags", templatefacts.Values{}, "[Group] Title - 01 [CHS]", "unknown"},
		{"no rt namespace", nil, "[Group] Title - 01 [内封]", "unknown"},
	}
	for _, tc := range cases {
		ctx := NamingContext{Release: ReleaseNamingFacts{OriginalTitle: tc.title}, Rt: tc.rt}
		result := resolveSingle(t, subtitleModeResolver{}, ResolverInput{Facts: ctx})
		if result.State != ResolverStateValue {
			t.Fatalf("%q: state = %q, want value", tc.name, result.State)
		}
		if got := result.Value.(subtitleModeValue).SubtitleMode; got != tc.want {
			t.Errorf("%q: mode = %q, want %q", tc.name, got, tc.want)
		}
	}
	if result := resolveSingle(t, subtitleModeResolver{}, ResolverInput{Facts: NamingContext{Rt: templatefacts.Values{"SubFlag": "soft"}}}); len(result.Evidence) != 1 ||
		result.Evidence[0].RuleID != "subtitle_mode_from_rt_sub_flag" || result.Evidence[0].InputPath != "Rt.SubFlag" {
		t.Fatalf("evidence = %+v", result.Evidence)
	}
	if result := resolveSingle(t, subtitleModeResolver{}, ResolverInput{Facts: NamingContext{}}); len(result.Evidence) != 0 {
		t.Fatalf("no flags carry no evidence: %+v", result.Evidence)
	}
}

func TestEffectiveTitleResolver(t *testing.T) {
	// The chain is override, provider, tmdb_cn, bgm_cn, bgm, base; the resolver
	// names the frozen fact the winning step read.
	cases := []struct {
		name   string
		anime  AnimeNamingFacts
		title  string
		source string
		path   string
	}{
		{"override", AnimeNamingFacts{Title: "Anime", TitleOverride: "Override", ProviderTitle: "Provider"}, "Override", "override", "Anime.TitleOverride"},
		{"provider", AnimeNamingFacts{Title: "Anime", ProviderTitle: "Provider", TMDB: TMDBNamingFacts{TitleCN: "TMDB CN"}}, "Provider", "provider", "Anime.ProviderTitle"},
		{"tmdb_cn", AnimeNamingFacts{Title: "Anime", TMDB: TMDBNamingFacts{TitleCN: "TMDB CN"}, BGM: BGMNamingFacts{TitleCN: "BGM CN", Title: "BGM Raw"}}, "TMDB CN", "tmdb_cn", "Anime.TMDB.TitleCN"},
		{"bgm_cn", AnimeNamingFacts{Title: "Anime", BGM: BGMNamingFacts{TitleCN: "BGM CN", Title: "BGM Raw"}}, "BGM CN", "bgm_cn", "Anime.BGM.TitleCN"},
		{"bgm", AnimeNamingFacts{Title: "Anime", BGM: BGMNamingFacts{Title: "BGM Raw"}}, "BGM Raw", "bgm", "Anime.BGM.Title"},
		{"base", AnimeNamingFacts{Title: "Anime"}, "Anime", "base", "Anime.Title"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := resolveSingle(t, effectiveTitleResolver{}, ResolverInput{Facts: NamingContext{Anime: tc.anime}})
			value, ok := result.Value.(effectiveTitleValue)
			if !ok || value.EffectiveTitle != tc.title || value.EffectiveTitleSource != tc.source {
				t.Fatalf("value = %+v, want %q/%q", result.Value, tc.title, tc.source)
			}
			if len(result.Evidence) != 1 || result.Evidence[0].RuleID != tc.source || result.Evidence[0].InputPath != tc.path {
				t.Fatalf("evidence = %+v, want %q at %q", result.Evidence, tc.source, tc.path)
			}
		})
	}

	result := resolveSingle(t, effectiveTitleResolver{}, ResolverInput{})
	if result.State != ResolverStateNull || len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != "effective_title_missing" {
		t.Fatalf("unexpected empty result: %+v", result)
	}
}

func TestSeasonFolderResolver(t *testing.T) {
	input := ResolverInput{Facts: NamingContext{Anime: AnimeNamingFacts{TMDB: TMDBNamingFacts{Season: 2}}}}
	result := resolveSingle(t, seasonFolderResolver{}, input)
	if result.State != ResolverStateValue || result.Value.(seasonFolderValue).SeasonFolder != "Season 02" {
		t.Fatalf("unexpected result: %+v", result)
	}
	result = resolveSingle(t, seasonFolderResolver{}, ResolverInput{})
	if result.State != ResolverStateNull {
		t.Fatalf("season 0 should stay null: %+v", result)
	}
}

func TestEffectiveMediaKindResolver(t *testing.T) {
	result := resolveSingle(t, effectiveMediaKindResolver{}, ResolverInput{})
	if result.Value.(mediaKindValue).EffectiveMediaKind != "tv" {
		t.Errorf("default kind = %v", result.Value)
	}
	movie := ResolverInput{Facts: NamingContext{Anime: AnimeNamingFacts{TMDB: TMDBNamingFacts{MediaKind: "movie"}}}}
	if got := resolveSingle(t, effectiveMediaKindResolver{}, movie).Value.(mediaKindValue).EffectiveMediaKind; got != "movie" {
		t.Errorf("tmdb movie kind = %q", got)
	}
	fileMovie := ResolverInput{Facts: NamingContext{File: FileNamingFacts{Kind: "movie"}}}
	if got := resolveSingle(t, effectiveMediaKindResolver{}, fileMovie).Value.(mediaKindValue).EffectiveMediaKind; got != "movie" {
		t.Errorf("file movie kind = %q", got)
	}
}

func TestResolveAllFillsContextAndVersions(t *testing.T) {
	input := ResolverInput{
		Facts: NamingContext{
			Anime:   AnimeNamingFacts{Title: "Silent Witch", TMDB: TMDBNamingFacts{Season: 1}},
			Release: ReleaseNamingFacts{OriginalTitle: "[SampleGroup] Silent Witch - 07 [WebRip 1080p][CHS]", FrozenEpisode: &FrozenEpisodeNamingFacts{Raw: "07", Number: "7", Display: "7"}},
			File:    FileNamingFacts{OriginalName: "show.mkv", Basename: "show", Extension: "mkv", Kind: "video"},
			Source:  SourceNamingFacts{GroupName: "SampleGroup"},
		},
	}
	resolved := DefaultRegistry().ResolveAll(input)
	ctx := resolved.Context
	if ctx.Parsed.Episode.Number != 7 || ctx.Parsed.Episode.Display != "7" {
		t.Errorf("episode = %+v", ctx.Parsed.Episode)
	}
	if ctx.Parsed.Language.Code != "sc" || len(ctx.Parsed.Language.BCP47) != 1 || ctx.Parsed.Language.BCP47[0] != "zh-Hans" {
		t.Errorf("language = %+v", ctx.Parsed.Language)
	}
	if ctx.Release.SubtitleMode != "unknown" {
		t.Errorf("subtitle mode = %q", ctx.Release.SubtitleMode)
	}
	if ctx.Derived.EffectiveTitle != "Silent Witch" || ctx.Derived.EffectiveTitleSource != "base" {
		t.Errorf("effective title = %+v", ctx.Derived)
	}
	if ctx.Derived.SeasonFolder != "Season 01" {
		t.Errorf("season folder = %q", ctx.Derived.SeasonFolder)
	}
	if ctx.Derived.EffectiveMediaKind != "tv" {
		t.Errorf("media kind = %q", ctx.Derived.EffectiveMediaKind)
	}
	if len(resolved.Results) != 7 {
		t.Fatalf("results = %d, want 7", len(resolved.Results))
	}
	// parsed.subtitle must run after parsed.language in topological order.
	subtitleIndex, languageIndex := -1, -1
	for index, result := range resolved.Results {
		switch result.Key {
		case "parsed.subtitle":
			subtitleIndex = index
		case "parsed.language":
			languageIndex = index
		}
	}
	if subtitleIndex < 0 || languageIndex < 0 || subtitleIndex <= languageIndex {
		t.Errorf("subtitle must resolve after language: subtitle=%d language=%d", subtitleIndex, languageIndex)
	}
}

func TestSummarizeResolverValueAndEvidence(t *testing.T) {
	input := episodeInput("[SampleGroup] Title - 07 [WebRip]", &FrozenEpisodeNamingFacts{Raw: "07", Number: "7", Display: "7"})
	resolved := DefaultRegistry().ResolveAll(input)
	var episodeResult *ResolverResult
	for index := range resolved.Results {
		if resolved.Results[index].Key == "parsed.episode" {
			episodeResult = &resolved.Results[index]
		}
	}
	if episodeResult == nil {
		t.Fatal("missing episode result")
	}
	summary := SummarizeResolverValue(*episodeResult)
	if !strings.Contains(summary, "number=7") {
		t.Errorf("value summary = %q", summary)
	}
	evidence := SummarizeResolverEvidence(episodeResult.Evidence)
	if !strings.Contains(evidence, "Release.FrozenEpisode") {
		t.Errorf("evidence summary = %q", evidence)
	}
	null := ResolverResult{Key: "x", State: ResolverStateNull}
	if SummarizeResolverValue(null) != "" || SummarizeResolverEvidence(nil) != "" {
		t.Error("null/empty summaries must be empty")
	}
}
