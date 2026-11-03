package naming

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func compileOrFail(t *testing.T, engine *Engine, kind TemplateKind, source string) CompiledTemplate {
	t.Helper()
	compiled, err := engine.Compile(kind, source)
	if err != nil {
		t.Fatalf("compile %q: %v", source, err)
	}
	return compiled
}

func sampleContext(t *testing.T, ref string) NamingContext {
	t.Helper()
	ctx, ok := ResolveSampleContext(ref)
	if !ok {
		t.Fatalf("sample %q not found", ref)
	}
	return ctx
}

func assertErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %s, got nil", code)
	}
	var namingErr *Error
	if !errors.As(err, &namingErr) {
		t.Fatalf("expected typed naming error, got %T: %v", err, err)
	}
	if namingErr.Code != code {
		t.Fatalf("error code = %q, want %q (%v)", namingErr.Code, code, err)
	}
}

func TestCompileRulesRejectsUnsupportedLanguage(t *testing.T) {
	engine := NewEngine()
	for _, language := range []string{"", "go-template-naming-1", "js-naming-3"} {
		rules := DefaultNamingRules()
		rules.Language = language
		_, err := engine.CompileRules(rules)
		assertErrorCode(t, err, ErrCodeTemplateCompileError)
	}
}

func TestRenderRejectsUnsupportedCompiledLanguage(t *testing.T) {
	engine := NewEngine()
	compiled := compileOrFail(t, engine, KindVideoFilename, "fixed.mkv")
	compiled.Language = ""
	_, err := engine.Render(compiled, NamingContext{})
	assertErrorCode(t, err, ErrCodeTemplateCompileError)
}

func TestCompileRejectsUnboundedStringOperations(t *testing.T) {
	engine := NewEngine()
	for _, source := range []string{
		"${'x'.repeat(1000000000)}",
		"${'x'.padStart(1000000000, 'a')}",
		"${Array(1000000000).join('x')}",
	} {
		_, err := engine.Compile(KindVideoFilename, source)
		assertErrorCode(t, err, ErrCodeTemplateCompileError)
	}
}

func TestRenderRejectsOversizedContextAndCollections(t *testing.T) {
	engine := NewEngine()
	compiled := compileOrFail(t, engine, KindVideoFilename, "${join(['a','b'], ',')}.mkv")
	ctx := NamingContext{}
	if _, err := engine.Render(compiled, ctx); err != nil {
		t.Fatal(err)
	}
	ctx.Anime.Title = strings.Repeat("x", MaxContextBytes)
	_, err := engine.Render(compiled, ctx)
	assertErrorCode(t, err, ErrCodeTemplateCompileError)
}

// A stored value is the template body: the engine supplies the backticks, so
// parentheses in a folder name stay text and a nested literal inside ${...}
// still works.
func TestTemplateBodyIsTheTextBetweenTheBackticks(t *testing.T) {
	engine := NewEngine()
	ctx := NamingContext{
		Anime:   AnimeNamingFacts{Title: "Show (2024)"},
		Derived: DerivedNamingVariables{EffectiveMediaKind: "tv"},
		File:    FileNamingFacts{Extension: "mkv"},
	}
	for source, want := range map[string]string{
		"${anime.kind} - ${anime.title}":                       "tv - Show (2024)",
		"Show (2024).${file.ext}":                              "Show (2024).mkv",
		"a\\`b.${file.ext}":                                    "a`b.mkv",
		"${file.ext}${anime.title ? ` [${anime.title}]` : ''}": "mkv [Show (2024)]",
	} {
		rendered, err := engine.Render(compileOrFail(t, engine, KindVideoFilename, source), ctx)
		if err != nil || rendered != want {
			t.Fatalf("%q rendered %q, %v; want %q", source, rendered, err, want)
		}
	}
	// The old stored form wrapped the body in backticks itself. It is not
	// recognised any more: a bare backtick outside ${...} is refused.
	for _, source := range []string{"`${anime.title}.${file.ext}`", "`Show.mkv`", "a`b", "`${anime.title}` + `x`"} {
		_, err := engine.Compile(KindVideoFilename, source)
		var namingErr *Error
		if !errors.As(err, &namingErr) || namingErr.Code != ErrCodeTemplateCompileError {
			t.Fatalf("%q compiled, err = %v; want %s", source, err, ErrCodeTemplateCompileError)
		}
	}
}

func TestDefaultRulesRenderSampleTV(t *testing.T) {
	engine := NewEngine()
	compiled, err := engine.CompileRules(DefaultNamingRules())
	if err != nil {
		t.Fatal(err)
	}
	ctx := sampleContext(t, "sample-tv")
	directory, filename, err := compiled.RenderVideo(engine, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if directory != "/media/TV/进击的巨人 (2025)/Season 01" {
		t.Fatalf("unexpected directory: %q", directory)
	}
	wantFilename := "进击的巨人 S01E07.mkv"
	if filename != wantFilename {
		t.Fatalf("filename = %q, want %q", filename, wantFilename)
	}
	// The default suffix is the subtitle's one BCP 47 tag (sub.lang_tag):
	// the file's own tag first, else the release title's rt.sub_lang (SC
	// here). Two subtitles of one release keep distinct suffixes when their
	// own tags differ.
	facts := compileOrFail(t, engine, KindSubtitleSuffix, "${sub.lang}|${sub.langs}|${sub.lang_tag}|${sub.lang_source}")
	for _, tc := range []struct{ name, suffix, facts string }{
		{"[FLsnow][Precure][36][TVer_1080p].chs&jpn.ass", "zh-Hans", "JPN+SC|JPN,SC|zh-Hans|file"},
		{"[FLsnow][Precure][36][TVer_1080p].cht&jpn.ass", "zh-Hant", "JPN+TC|JPN,TC|zh-Hant|file"},
		{"Show - 07.eng.ass", "en", "ENG|ENG|en|file"},
		// zh-Hans is not in the reltitle vocabulary: the release value stands.
		{"Show - 07.zh-Hans.ass", "zh-Hans", "SC|SC|zh-Hans|release"},
		{"Show - 07.ass", "zh-Hans", "SC|SC|zh-Hans|release"},
	} {
		subtitle := ctx
		subtitle.File = FileNamingFacts{OriginalName: tc.name, Basename: strings.TrimSuffix(tc.name, ".ass"), Extension: "ass", Kind: "subtitle"}
		resolved := DefaultRegistry().ResolveAll(ResolverInput{Facts: subtitle})
		suffix, err := compiled.RenderSubtitleSuffix(engine, resolved.Context)
		if err != nil {
			t.Fatal(err)
		}
		if suffix != tc.suffix {
			t.Fatalf("default suffix of %q = %q, want %q", tc.name, suffix, tc.suffix)
		}
		if got, err := engine.Render(facts, resolved.Context); err != nil || got != tc.facts {
			t.Fatalf("sub.* of %q = %q, %v; want %q", tc.name, got, err, tc.facts)
		}
	}
	// A video has no subtitle language.
	if suffix, err := compiled.RenderSubtitleSuffix(engine, ctx); err != nil || suffix != "" {
		t.Fatalf("default suffix of a video = %q, %v", suffix, err)
	}
}

func TestDefaultRulesRenderSampleMovie(t *testing.T) {
	engine := NewEngine()
	compiled, err := engine.CompileRules(DefaultNamingRules())
	if err != nil {
		t.Fatal(err)
	}
	ctx := sampleContext(t, "sample-movie")
	directory, filename, err := compiled.RenderVideo(engine, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if directory != "/media/Movie/进击的巨人 剧场版 (2022)" {
		t.Fatalf("unexpected directory: %q", directory)
	}
	wantFilename := "[示例字幕组] 进击的巨人 剧场版 (WebRip 1920x1080 AVC AAC MP4).mp4"
	if filename != wantFilename {
		t.Fatalf("filename = %q, want %q", filename, wantFilename)
	}
	// This release names no subtitle language and the subtitle's own tag
	// none either: the suffix renders empty (the subtitle follows the video
	// basename) and the empty fact is reported, never guessed.
	subtitle := ctx
	subtitle.File = FileNamingFacts{OriginalName: "movie.ass", Basename: "movie", Extension: "ass", Kind: "subtitle"}
	resolved := DefaultRegistry().ResolveAll(ResolverInput{Facts: subtitle})
	suffix, err := compiled.RenderSubtitleSuffixDetailed(engine, resolved.Context)
	if err != nil || suffix.Value != "" || strings.Join(suffix.EmptyFacts, ",") != "sub.lang_tag" {
		t.Fatalf("default suffix without any subtitle language = %+v, %v", suffix, err)
	}
}

func TestRenderVideoBranchSelection(t *testing.T) {
	engine := NewEngine()
	rules := NamingRules{
		Language: Language,
		TV:       NamingVariant{DirectoryTemplate: "/shows", VideoFilenameTemplate: "${anime.title}.tv.${file.ext}"},
		Movie:    NamingVariant{DirectoryTemplate: "/films", VideoFilenameTemplate: "${anime.title}.movie.${file.ext}"},
	}
	compiled, err := engine.CompileRules(rules)
	if err != nil {
		t.Fatal(err)
	}
	base := NamingContext{Anime: AnimeNamingFacts{Title: "Show"}, File: FileNamingFacts{Extension: "mkv"}}

	movie := base
	movie.Derived.EffectiveMediaKind = "movie"
	directory, filename, err := compiled.RenderVideo(engine, movie)
	if err != nil {
		t.Fatal(err)
	}
	if directory != "/films" || filename != "Show.movie.mkv" {
		t.Fatalf("movie branch = %q/%q", directory, filename)
	}

	tv := base
	tv.Derived.EffectiveMediaKind = "tv"
	directory, filename, err = compiled.RenderVideo(engine, tv)
	if err != nil {
		t.Fatal(err)
	}
	if directory != "/shows" || filename != "Show.tv.mkv" {
		t.Fatalf("tv branch = %q/%q", directory, filename)
	}

	empty := base
	directory, filename, err = compiled.RenderVideo(engine, empty)
	if err != nil {
		t.Fatal(err)
	}
	if directory != "/shows" || filename != "Show.tv.mkv" {
		t.Fatalf("empty kind must select tv branch = %q/%q", directory, filename)
	}

	if compiled.SubtitleSuffix != nil {
		t.Fatal("empty subtitle suffix template must not compile a suffix")
	}
	suffix, err := compiled.RenderSubtitleSuffix(engine, base)
	if err != nil || suffix != "" {
		t.Fatalf("nil suffix template must render empty: %q, %v", suffix, err)
	}
}

// The naming catalog lists tmdb.title_cn and tmdb.original_title but never
// tmdb.summary: a multi-paragraph overview in a path is structurally wrong
// (owner ruling 2026-10-22), so the save-time compile refuses the reference as
// an unknown leaf while the notification catalog projects it.
func TestCompileRejectsTMDBSummaryReference(t *testing.T) {
	engine := NewEngine()
	for _, source := range []string{"${tmdb.summary}.mkv", "${Anime.Title} - ${tmdb.summary}"} {
		_, err := engine.Compile(KindVideoFilename, source)
		assertErrorCode(t, err, ErrCodeTemplateCompileError)
	}
}

func TestCompileRejectsUnsafeOrUnknownTemplates(t *testing.T) {
	engine := NewEngine()
	cases := []struct {
		name   string
		source string
		code   string
	}{
		{"unknown function", "${frobnicate(Anime.Title)}", ErrCodeTemplateCompileError},
		{"unknown field", "${Anime.Nonexistent}.mkv", ErrCodeTemplateCompileError},
		{"unknown nested field", "${Derived.Nope}", ErrCodeTemplateCompileError},
		{"forbidden host", "${require(\"fs\")}", ErrCodeTemplateCompileError},
		{"oversize template", strings.Repeat("a", MaxTemplateBytes+1), ErrCodeTemplateLimitExceeded},
		{"invalid regex literal argument", `${extract(File.OriginalName, "([")}`, ErrCodeTemplateCompileError},
		{"oversize regex literal argument", `${match(File.OriginalName, "` + strings.Repeat("a", MaxRegexBytes+1) + `")}`, ErrCodeTemplateLimitExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := engine.Compile(KindVideoFilename, tc.source)
			assertErrorCode(t, err, tc.code)
		})
	}

	// A template may throw whatever it likes. That is a failure of one concrete
	// render, not a compile error: compilation guarantees syntax and static
	// references, and the same template can succeed for another file.
	throwing, err := engine.Compile(KindVideoFilename, "${(() => { throw new Error('template recursion'); })()}")
	if err != nil {
		t.Fatalf("a syntactically valid template must compile: %v", err)
	}
	if _, err := engine.Render(throwing, NamingContext{}); err == nil {
		t.Fatal("a throwing template must fail its render")
	} else {
		assertErrorCode(t, err, ErrCodeTemplateCompileError)
	}
}

func TestRenderRejectsUnsafeTargets(t *testing.T) {
	engine := NewEngine()
	cases := []struct {
		name   string
		kind   TemplateKind
		source string
		code   string
	}{
		{"empty filename", KindVideoFilename, "${anime.title ?? ''}", ErrCodeNamingTargetInvalid},
		{"directory traversal", KindVideoFilename, "../${file.ext}", ErrCodeNamingTargetInvalid},
		{"home prefix directory", KindDirectory, `~/media`, ErrCodeNamingTargetInvalid},
		{"filename with separator", KindVideoFilename, `a/b.mkv`, ErrCodeNamingTargetInvalid},
		{"suffix with separator", KindSubtitleSuffix, `zh/CHS`, ErrCodeSubtitleSuffixInvalid},
		{"suffix traversal", KindSubtitleSuffix, `..`, ErrCodeSubtitleSuffixInvalid},
		{"suffix leading dot", KindSubtitleSuffix, `.zh`, ErrCodeSubtitleSuffixInvalid},
		{"suffix overlong", KindSubtitleSuffix, `${padLeft("x", 300, "a")}`, ErrCodeSubtitleSuffixInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			compiled := compileOrFail(t, engine, tc.kind, tc.source)
			ctx := NamingContext{File: FileNamingFacts{Extension: "mkv"}}
			_, err := engine.Render(compiled, ctx)
			assertErrorCode(t, err, tc.code)
		})
	}

	// Absolute rendered directories are allowed: the deployment owns the
	// read/write boundary and the app does not authorize locations.
	compiled := compileOrFail(t, engine, KindDirectory, `/abs/path`)
	rendered, err := engine.Render(compiled, NamingContext{})
	if err != nil || rendered != "/abs/path" {
		t.Fatalf("absolute directory target rejected: %q err=%v", rendered, err)
	}
}

// The execution budget covers the template itself, not the engine's own setup.
// Building the context object and installing its access guards is sized by the
// declared fields; charging that to the 50ms budget made a loaded CI host report
// TEMPLATE_TIMEOUT for a template that never ran. The budget must still stop a
// template that does not terminate.
func TestRenderTimeoutCoversTheTemplateNotItsContextSetup(t *testing.T) {
	engine := NewEngineWithLimits(1, 20*time.Millisecond)
	compiled := compileOrFail(t, engine, KindVideoFilename, "${(function(){ while(true){} })()}")
	_, err := engine.Render(compiled, NamingContext{})
	assertErrorCode(t, err, ErrCodeTemplateTimeout)
}

func TestRenderEnforcesOutputLimit(t *testing.T) {
	engine := NewEngine()
	// Width comes from the context so the zero-context trial render passes.
	compiled := compileOrFail(t, engine, KindSubtitleSuffix, `${padLeft("x", file.size, "a")}`)
	ctx := NamingContext{}
	ctx.File.SizeBytes = MaxOutputBytes + 1
	_, err := engine.Render(compiled, ctx)
	assertErrorCode(t, err, ErrCodeTemplateLimitExceeded)
}

func TestEmptyDirectoryIsRejected(t *testing.T) {
	engine := NewEngine()
	// A rendered target is always absolute, so an empty directory cannot be a
	// valid placement choice; there is no implicit library root to fall back to.
	compiled := compileOrFail(t, engine, KindDirectory, "${anime.season_folder ?? ''}")
	_, err := engine.Render(compiled, NamingContext{})
	assertErrorCode(t, err, ErrCodeNamingTargetNotAbsolute)
}

func TestRelativeDirectoryIsRejected(t *testing.T) {
	engine := NewEngine()
	compiled := compileOrFail(t, engine, KindDirectory, "shows/${anime.title ?? 'x'}")
	_, err := engine.Render(compiled, NamingContext{})
	assertErrorCode(t, err, ErrCodeNamingTargetNotAbsolute)
}

func TestExtractRealWorldEpisodePattern(t *testing.T) {
	engine := NewEngine()
	compiled := compileOrFail(t, engine, KindVideoFilename,
		"${sanitize(anime.title)} - E${pad(extract(file.name, \"(?i)EP?(\\\\d+(?:\\\\.\\\\d+)?)\"), 2)}.${file.ext}")
	ctx := NamingContext{
		File:  FileNamingFacts{OriginalName: "[Group] Show EP07.5 [1080p].mkv", Extension: "mkv"},
		Anime: AnimeNamingFacts{Title: "Show"},
	}
	filename, err := engine.Render(compiled, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if filename != "Show - E07.5.mkv" {
		t.Fatalf("filename = %q", filename)
	}
}

func TestCompileRulesRejectsBrokenBranch(t *testing.T) {
	engine := NewEngine()
	rules := DefaultNamingRules()
	rules.Movie.VideoFilenameTemplate = "${Movie.Nope}"
	_, err := engine.CompileRules(rules)
	assertErrorCode(t, err, ErrCodeTemplateCompileError)
}

func TestCatalogDocumentCoversFunctionsAndSamples(t *testing.T) {
	doc := GenerateCatalogDocument()
	if len(doc.Functions) != 20 {
		t.Fatalf("function count = %d, want 20", len(doc.Functions))
	}
	if len(doc.Samples) != 3 ||
		doc.Samples[0].Ref != "sample-tv" ||
		doc.Samples[1].Ref != "sample-movie" ||
		doc.Samples[2].Ref != "sample-tv-subtitle" {
		t.Fatalf("unexpected samples: %+v", doc.Samples)
	}
	seenRefs := make(map[string]bool, len(doc.Samples))
	for _, sample := range doc.Samples {
		if seenRefs[sample.Ref] {
			t.Fatalf("duplicate sample ref %q", sample.Ref)
		}
		seenRefs[sample.Ref] = true
	}

	subtitleSample := doc.Samples[2]
	if subtitleSample.Locked == nil {
		t.Fatalf("subtitle sample %q must carry a locked bound-video target", subtitleSample.Ref)
	}
	if subtitleSample.Context.File.Kind != "subtitle" {
		t.Fatalf("subtitle sample file kind = %q, want subtitle", subtitleSample.Context.File.Kind)
	}

	// The subtitle sample's locked target is fixed redacted data; prove it
	// stays identical to the default-rules rendering of the bound sample-tv
	// video so the previewed locked segments match execution.
	// The probe renders run against the generous test budget, not the 50ms
	// production watchdog: a loaded CI host can starve a single render past
	// the production margin (e.g. rt.source against sample-tv-subtitle at
	// timeout), and the catalog proof must not be a wall-clock flake.
	engine := NewEngineWithLimits(4, 2*time.Second)
	compiledRules, err := engine.CompileRules(DefaultNamingRules())
	if err != nil {
		t.Fatal(err)
	}
	lockedDirectory, lockedFilename, err := compiledRules.RenderVideo(engine, sampleContext(t, "sample-tv"))
	if err != nil {
		t.Fatal(err)
	}
	locked := subtitleSample.Locked
	if locked.Directory != lockedDirectory {
		t.Fatalf("locked directory = %q, want rendered %q", locked.Directory, lockedDirectory)
	}
	if locked.Basename != strings.TrimSuffix(lockedFilename, "."+locked.Extension) {
		t.Fatalf("locked basename = %q, want rendered %q minus .%s", locked.Basename, lockedFilename, locked.Extension)
	}
	if locked.Extension != "mkv" {
		t.Fatalf("locked extension = %q, want mkv", locked.Extension)
	}

	// The subtitle sample resolves its Parsed.Subtitle facts through the same
	// registry execution uses.
	subtitleCtx := sampleContext(t, "sample-tv-subtitle")
	if subtitleCtx.Parsed.Subtitle.Format != "ass" {
		t.Fatalf("subtitle format = %q, want ass", subtitleCtx.Parsed.Subtitle.Format)
	}
	if subtitleCtx.Parsed.Subtitle.PrimaryLanguage != "zh-Hans" {
		t.Fatalf("subtitle primary language = %q, want zh-Hans", subtitleCtx.Parsed.Subtitle.PrimaryLanguage)
	}
	if !subtitleCtx.Parsed.Subtitle.Flags.Forced {
		t.Fatalf("subtitle forced flag = false, want true")
	}
	// Every documented field path must resolve through a render so the
	// catalog cannot drift from the context schema.
	for _, sample := range doc.Samples {
		resolvedSample, ok := ResolveSampleContext(sample.Ref)
		if !ok {
			t.Fatalf("sample %q cannot be resolved", sample.Ref)
		}
		for _, field := range doc.Fields {
			// Optional absent fields need an explicit catalog-probe fallback;
			// the render still proves the field is declared by the schema.
			compiled, err := engine.Compile(KindDirectory, "/probe/x${"+field.Path+" ?? ''}")
			if err != nil {
				t.Fatalf("catalog field %q does not compile: %v", field.Path, err)
			}
			if _, err := engine.Render(compiled, resolvedSample); err != nil {
				t.Fatalf("catalog field %q does not render against %s: %v", field.Path, sample.Ref, err)
			}
		}
	}
}

// TestCompiledRequiredFactsIgnoreConditionalReads pins which facts a template
// demands. Only a fact read where an empty value changes the rendered name is
// required; the shipped templates read the resolution, codec, language and
// group tags inside conditionals, so their absence is the template's own
// handling, not missing evidence. A maintenance re-render refuses a name that
// would silently drop a required fact, so over-reporting here disables
// path-diff and alignment for every file whose optional facts are empty.
func TestCompiledRequiredFactsIgnoreConditionalReads(t *testing.T) {
	engine := NewEngine()
	cases := []struct {
		name   string
		source string
		want   []string
	}{
		{"unconditional read is required", "${media.video_codec}", []string{"media.video_codec"}},
		{"conditional read is optional", "${media.video_codec ? ` [${media.video_codec}]` : ''}", nil},
		{"short circuit read is optional", "${release.lang || 'und'}", nil},
		{"defaultValue argument is optional", "${defaultValue('und', release.lang)}", nil},
		{"read outside a conditional stays required", "${ep.display}${media.video_codec ? ` [${media.video_codec}]` : ''}", []string{"ep.display"}},
		{"defaultValue fallback is written unconditionally", "${defaultValue(media.resolution, media.video_codec)}", []string{"media.resolution"}},
		{"coalesce arguments are all fallbacks", "${coalesce(tmdb.id, media.container)}", nil},
		{"a bare logical and has no fallback", "${media.resolution && media.video_codec}", []string{"media.resolution", "media.video_codec"}},
		{"a branch guarded by its own test stays optional", "${media.video_codec ? ` [${media.video_codec}]` : ''}", nil},
		{"a fact the test does not guard stays required", "${release.lang ? ` [${media.container}]` : ''}", []string{"media.container"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			compiled := compileOrFail(t, engine, KindVideoFilename, testCase.source)
			if strings.Join(compiled.RequiredFacts, ",") != strings.Join(testCase.want, ",") {
				t.Fatalf("RequiredFacts = %v, want %v", compiled.RequiredFacts, testCase.want)
			}
		})
	}

	// The rule set the product ships must not require a media tag fact (the
	// library layout reads kind, title, year, season and episode only).
	compiled, err := engine.CompileRules(DefaultNamingRules())
	if err != nil {
		t.Fatalf("compile default rules: %v", err)
	}
	got := strings.Join(compiled.TV.RequiredFacts(), ",")
	if got != "anime.kind,anime.season_folder,anime.title,ep.number,file.ext" {
		t.Fatalf("default TV required facts = %v", compiled.TV.RequiredFacts())
	}
	if strings.Contains(got, "video_codec") || strings.Contains(got, "lang") || strings.Contains(got, "group") || strings.Contains(got, "resolution") {
		t.Fatalf("default rules must not require media or language tags, got %v", compiled.TV.RequiredFacts())
	}
}

// A preview renders execution-phase facts as placeholders and never reports
// them missing; release-phase facts render their value.
func TestPreviewRendersExecutionFactsAsPlaceholders(t *testing.T) {
	ctx, _ := ResolveSampleContext("sample-tv")
	compiled, err := NewEngine().Compile(KindVideoFilename, "${anime.title} [${media.resolution}] ${media.video_codec}.${file.ext}")
	if err != nil {
		t.Fatal(err)
	}
	got, err := NewEngine().Preview().Render(compiled, ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := ctx.Derived.EffectiveTitle + " [⟨media.resolution⟩] ⟨media.video_codec⟩.mkv"
	if got != want {
		t.Fatalf("preview = %q, want %q", got, want)
	}
	// An ordinary render reads the inspected values.
	rendered, err := NewEngine().RenderDetailed(compiled, ctx)
	if err != nil || rendered.Value != ctx.Derived.EffectiveTitle+" [1080p] HEVC.mkv" || len(rendered.EmptyFacts) != 0 {
		t.Fatalf("ordinary render = %+v, %v", rendered, err)
	}
	// A preview never reports an execution fact empty, even without a value.
	ctx.File.Media = MediaNamingFacts{}
	if preview, err := NewEngine().Preview().RenderDetailed(compiled, ctx); err != nil || len(preview.EmptyFacts) != 0 {
		t.Fatalf("preview without media facts = %+v, %v", preview, err)
	}
}
