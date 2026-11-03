package application

import (
	"bansoko/internal/templatecatalog/notification"
	"context"
	"strings"
	"testing"

	"bansoko/internal/namingvars"
	"bansoko/internal/postprocessing/naming"
)

func TestNotificationCatalogCompleteness(t *testing.T) {
	service := NewService()
	ctx := context.Background()

	for _, trigger := range []NotificationTriggerKind{
		NotificationTriggerKindDownloadSubmitted,
		NotificationTriggerKindFileOperationCompleted,
		NotificationTriggerKindFileOperationFailed,
	} {
		catalog, err := service.GetNotificationCatalog(ctx, string(trigger), "")
		if err != nil {
			t.Fatalf("GetNotificationCatalog(%s) returned error: %v", trigger, err)
		}
		if catalog.LanguageVersion != NotificationTemplateCatalogLanguageVersionBansokoTemplate1 {
			t.Errorf("language version = %q, want bansoko-template-1", catalog.LanguageVersion)
		}
		if catalog.CatalogVersion != NotificationTemplateCatalogCatalogVersionNotificationCatalogV1 {
			t.Errorf("catalog version = %q, want notification-catalog-v1", catalog.CatalogVersion)
		}
		if catalog.TriggerKind != trigger {
			t.Errorf("trigger kind = %q, want %q", catalog.TriggerKind, trigger)
		}
		if len(catalog.Variables) == 0 {
			t.Errorf("trigger %s has no variables", trigger)
		}
		names := variableNames(catalog.Variables)
		for _, want := range []string{"anime.title", "release.title", "event.type"} {
			if !names[want] {
				t.Errorf("trigger %s missing variable %q", trigger, want)
			}
		}
		if catalog.DefaultTemplate.TitleTemplate == "" || catalog.DefaultTemplate.BodyTemplate == "" {
			t.Errorf("trigger %s has empty default templates", trigger)
		}
		if len(catalog.Sample.Values) == 0 {
			t.Errorf("trigger %s has no sample facts", trigger)
		}
		assertFunctions(t, catalog.Functions)
	}

	// Trigger-specific subsets.
	downloadCatalog, _ := service.GetNotificationCatalog(ctx, string(NotificationTriggerKindDownloadSubmitted), "")
	if !variableNames(downloadCatalog.Variables)["download.task"] {
		t.Error("download_submitted missing download.task")
	}
	fileCatalog, _ := service.GetNotificationCatalog(ctx, string(NotificationTriggerKindFileOperationCompleted), "")
	fileNames := variableNames(fileCatalog.Variables)
	for _, want := range []string{"out.name", "out.path", "op.kind"} {
		if !fileNames[want] {
			t.Errorf("file_operation_completed missing variable %q", want)
		}
	}

	// An unknown catalog version is rejected.
	if _, err := service.GetNotificationCatalog(ctx, string(NotificationTriggerKindFileOperationCompleted), "notification-catalog-v0"); err == nil {
		t.Error("expected error for an unsupported catalog version")
	}
}

func TestNotificationCatalogUnknownTrigger(t *testing.T) {
	service := NewService()
	if _, err := service.GetNotificationCatalog(context.Background(), "not_a_trigger", ""); err == nil {
		t.Fatal("expected error for unknown trigger kind")
	}
}

func TestNamingCatalogCompleteness(t *testing.T) {
	service := NewService()
	catalog, err := service.GetNamingCatalog(context.Background(), "")
	if err != nil {
		t.Fatalf("GetNamingCatalog returned error: %v", err)
	}
	paths := make(map[string]bool, len(catalog.Fields))
	for _, field := range catalog.Fields {
		paths[field.Path] = true
		if field.Source == "" {
			t.Errorf("field %q has empty source", field.Path)
		}
	}
	for _, want := range []string{"anime.title", "ep.display", "file.ext", "media.resolution", "rt.title", "anibt.language", "bgm.title", "tmdb.original_title", "torrent.size", "torrent.hash_v1"} {
		if !paths[want] {
			t.Errorf("naming catalog missing field %q", want)
		}
	}
	if paths["torrent.name"] || paths["out.path"] {
		t.Error("catalog advertises a fact naming cannot read")
	}
	if catalog.Language != "js-naming-1" {
		t.Errorf("catalog language = %q", catalog.Language)
	}
	if _, err := service.GetNamingCatalog(context.Background(), "js-naming-2"); err == nil {
		t.Error("an unknown naming language must be refused")
	}
	if len(catalog.Samples) != 3 {
		t.Fatalf("sample count = %d, want 3", len(catalog.Samples))
	}
	refs := map[string]bool{}
	for _, sample := range catalog.Samples {
		refs[sample.Ref] = true
	}
	if !refs["sample-tv"] || !refs["sample-movie"] || !refs["sample-tv-subtitle"] {
		t.Errorf("unexpected sample refs: %+v", catalog.Samples)
	}
	if len(catalog.Functions) != 20 {
		t.Fatalf("function count = %d, want 20", len(catalog.Functions))
	}
	for _, function := range catalog.Functions {
		if function.Signature == "" || function.Description == "" {
			t.Errorf("function %q has empty signature or description", function.Name)
		}
		if len(function.Arguments) == 0 {
			t.Errorf("function %q has no argument signature", function.Name)
		}
	}
	// The shorthand list is the engine's own table, not a second copy: the
	// editor shows exactly what the compiler expands.
	if len(catalog.Shorthands) != len(naming.Shorthands) {
		t.Fatalf("shorthand count = %d, want %d", len(catalog.Shorthands), len(naming.Shorthands))
	}
	for index, shorthand := range catalog.Shorthands {
		want := naming.Shorthands[index]
		if shorthand.Name != want.Name || shorthand.Expansion != want.Expansion || shorthand.Example != want.Example {
			t.Fatalf("shorthand %d = %+v, want %+v", index, shorthand, want)
		}
		if shorthand.Expansion == "" || shorthand.Example == "" {
			t.Errorf("shorthand %q is documented without an expansion or example", shorthand.Name)
		}
	}
}

func TestPreviewNotificationNormal(t *testing.T) {
	service := NewService()
	preview, err := service.PreviewNotification(context.Background(), NotificationTemplatePreviewInput{
		BodyTemplate:    "{{release.title}}\n字幕组：{{defaultIfEmpty(anime.group, '未知')}}",
		CatalogVersion:  NotificationTemplatePreviewInputCatalogVersionNotificationCatalogV1,
		LanguageVersion: NotificationTemplatePreviewInputLanguageVersionBansokoTemplate1,
		TitleTemplate:   "已加入下载：{{anime.title}}",
		TriggerKind:     NotificationTriggerKindDownloadSubmitted,
	})
	if err != nil {
		t.Fatalf("PreviewNotification returned error: %v", err)
	}
	if !preview.Valid {
		t.Fatalf("expected valid preview, got errors: %+v", preview.Errors)
	}
	if preview.Title != "已加入下载：进击的巨人" {
		t.Errorf("title = %q, want the registry sample title", preview.Title)
	}
	wantBody := "[示例字幕组] 进击的巨人 - 07 [WebRip 1080p HEVC-10bit AAC][CHS]\n字幕组：示例字幕组"
	if preview.Body != wantBody {
		t.Errorf("body = %q, want %q", preview.Body, wantBody)
	}
	if len(preview.Warnings) != 0 {
		t.Errorf("expected no warnings, got %+v", preview.Warnings)
	}
}

func TestPreviewNotificationUnknownVariable(t *testing.T) {
	service := NewService()
	preview, err := service.PreviewNotification(context.Background(), NotificationTemplatePreviewInput{
		BodyTemplate:    "{{release.title}}",
		CatalogVersion:  NotificationTemplatePreviewInputCatalogVersionNotificationCatalogV1,
		LanguageVersion: NotificationTemplatePreviewInputLanguageVersionBansokoTemplate1,
		TitleTemplate:   "{{anime.unknown_field}}",
		TriggerKind:     NotificationTriggerKindDownloadSubmitted,
	})
	if err != nil {
		t.Fatalf("PreviewNotification returned error: %v", err)
	}
	if preview.Valid {
		t.Fatal("expected invalid preview for unknown variable")
	}
	if len(preview.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d: %+v", len(preview.Errors), preview.Errors)
	}
	err0 := preview.Errors[0]
	if err0.Code != "invalid_expression" {
		t.Errorf("error code = %q, want invalid_expression", err0.Code)
	}
	if err0.Field == nil || *err0.Field != TemplateSourceErrorFieldTitleTemplate {
		t.Errorf("error field = %v, want title_template", err0.Field)
	}
	if err0.Start != 0 || err0.End == 0 {
		t.Errorf("error span = [%d,%d], want non-empty source span", err0.Start, err0.End)
	}
}

// Every catalog variable has a real sample: a preview that uses all of them
// renders with no null warnings.
func TestPreviewNotificationSamplesEveryVariable(t *testing.T) {
	service := NewService()
	for _, trigger := range []NotificationTriggerKind{NotificationTriggerKindDownloadSubmitted, NotificationTriggerKindFileOperationCompleted, NotificationTriggerKindFileOperationFailed} {
		variables, _ := notification.Variables(string(trigger))
		body := ""
		for _, variable := range variables {
			if variable.SampleValue == "" {
				t.Fatalf("%s: %s has no sample value", trigger, variable.Name)
			}
			body += variable.Name + "={{" + variable.Name + "}}\n"
		}
		preview, err := service.PreviewNotification(context.Background(), NotificationTemplatePreviewInput{
			BodyTemplate: body, TitleTemplate: "{{event.type}}", TriggerKind: trigger,
			CatalogVersion:  NotificationTemplatePreviewInputCatalogVersionNotificationCatalogV1,
			LanguageVersion: NotificationTemplatePreviewInputLanguageVersionBansokoTemplate1,
		})
		if err != nil || !preview.Valid || len(preview.Warnings) != 0 {
			t.Fatalf("%s: preview err=%v errors=%+v warnings=%+v", trigger, err, preview.Errors, preview.Warnings)
		}
	}
}

func TestPreviewNotificationInvalidSyntax(t *testing.T) {
	service := NewService()
	preview, err := service.PreviewNotification(context.Background(), NotificationTemplatePreviewInput{
		BodyTemplate:    "{{release.title}}",
		CatalogVersion:  NotificationTemplatePreviewInputCatalogVersionNotificationCatalogV1,
		LanguageVersion: NotificationTemplatePreviewInputLanguageVersionBansokoTemplate1,
		TitleTemplate:   "{{anime.title",
		TriggerKind:     NotificationTriggerKindDownloadSubmitted,
	})
	if err != nil {
		t.Fatalf("PreviewNotification returned error: %v", err)
	}
	if preview.Valid {
		t.Fatal("expected invalid preview for unterminated expression")
	}
	if len(preview.Errors) != 1 || preview.Errors[0].Code != "unterminated_expression" {
		t.Fatalf("expected unterminated_expression error, got %+v", preview.Errors)
	}
}

func TestPreviewNotificationUnsupportedVersion(t *testing.T) {
	service := NewService()
	_, err := service.PreviewNotification(context.Background(), NotificationTemplatePreviewInput{
		BodyTemplate:    "x",
		CatalogVersion:  NotificationTemplatePreviewInputCatalogVersionNotificationCatalogV1,
		LanguageVersion: "wrong-version",
		TitleTemplate:   "x",
		TriggerKind:     NotificationTriggerKindDownloadSubmitted,
	})
	if err == nil {
		t.Fatal("expected error for unsupported language version")
	}
}

func namingPreviewInput(kind NamingTemplatePreviewInputTemplateKind, source string) NamingTemplatePreviewInput {
	return NamingTemplatePreviewInput{
		TemplateKind:   kind,
		TemplateSource: source,
	}
}

// This pins the template editor's fixed sample-tv preview display and provenance; it is not a production preview-builder contract.
func TestPreviewNamingVideoFilename(t *testing.T) {
	service := NewService()
	preview, err := service.PreviewNaming(context.Background(), namingPreviewInput(
		NamingTemplatePreviewInputTemplateKindVideoFilename,
		"${sanitize(anime.title)} - E${pad(ep.display, 2)}.${file.ext}",
	))
	if err != nil {
		t.Fatalf("PreviewNaming returned error: %v", err)
	}
	if !preview.Valid {
		t.Fatalf("expected valid preview, got errors: %+v", preview.Errors)
	}
	want := "进击的巨人 - E07.mkv"
	if preview.Rendered != want {
		t.Errorf("rendered = %q, want %q", preview.Rendered, want)
	}
	if preview.ComposedTarget == nil || *preview.ComposedTarget != "/media/TV/进击的巨人 (2025)/Season 01/"+want {
		t.Errorf("composed_target = %v", preview.ComposedTarget)
	}
	if preview.ResolvedValues == nil {
		t.Fatal("expected resolved_values block")
	}
	byKey := map[string]NamingResolvedValue{}
	for _, value := range *preview.ResolvedValues {
		byKey[value.Key] = value
	}
	for _, key := range []string{
		"parsed.episode", "parsed.language", "parsed.subtitle", "release.subtitle_mode",
		"derived.effective_title", "derived.season_folder", "derived.effective_media_kind",
	} {
		if _, ok := byKey[key]; !ok {
			t.Errorf("resolved_values misses key %q", key)
		}
	}
	episode := byKey["parsed.episode"]
	if episode.State != NamingResolvedValueStateValue {
		t.Errorf("episode state = %q, want value", episode.State)
	}
	if episode.Value == nil || !strings.Contains(*episode.Value, "number=7") {
		t.Errorf("episode value summary = %v", episode.Value)
	}
	if episode.Evidence == nil || !strings.Contains(*episode.Evidence, "Release.FrozenEpisode") {
		t.Errorf("episode evidence summary = %v", episode.Evidence)
	}
	if episode.Source == nil || *episode.Source != "Release.FrozenEpisode" {
		t.Errorf("episode source = %v", episode.Source)
	}
	language := byKey["parsed.language"]
	if language.State != NamingResolvedValueStateValue || language.Value == nil || !strings.Contains(*language.Value, "sc") {
		t.Errorf("language resolved value = %+v", language)
	}
}

func TestPreviewNamingDirectory(t *testing.T) {
	service := NewService()
	preview, err := service.PreviewNaming(context.Background(), namingPreviewInput(
		NamingTemplatePreviewInputTemplateKindDirectory,
		"/library/${sanitize(anime.title)}/${defaultValue('Season 01', anime.season_folder)}",
	))
	if err != nil {
		t.Fatalf("PreviewNaming returned error: %v", err)
	}
	if !preview.Valid {
		t.Fatalf("expected valid preview, got errors: %+v", preview.Errors)
	}
	if preview.Rendered != "/library/进击的巨人/Season 01" {
		t.Errorf("rendered = %q", preview.Rendered)
	}
	if preview.ComposedTarget != nil {
		t.Errorf("directory preview must not compose a target: %v", preview.ComposedTarget)
	}
}

func TestPreviewNamingSubtitleSuffix(t *testing.T) {
	service := NewService()
	preview, err := service.PreviewNaming(context.Background(), namingPreviewInput(
		NamingTemplatePreviewInputTemplateKindSubtitleSuffix,
		"${defaultValue('und', release.lang)}",
	))
	if err != nil {
		t.Fatalf("PreviewNaming returned error: %v", err)
	}
	if !preview.Valid {
		t.Fatalf("expected valid preview, got errors: %+v", preview.Errors)
	}
	if preview.Rendered != "sc" {
		t.Errorf("rendered = %q, want sc", preview.Rendered)
	}
	if preview.LockedDirectory == nil || *preview.LockedDirectory != "/media/TV/进击的巨人 (2025)/Season 01" {
		t.Errorf("locked_directory = %v", preview.LockedDirectory)
	}
	// The default rules render the released name from the library layout only;
	// the bound video's media facts play no part in it.
	wantBasename := "进击的巨人 S01E07"
	if preview.LockedBasename == nil || *preview.LockedBasename != wantBasename {
		t.Errorf("locked_basename = %v", strOrEmpty(preview.LockedBasename))
	}
	if preview.LockedExtension == nil || *preview.LockedExtension != "mkv" {
		t.Errorf("locked_extension = %v", preview.LockedExtension)
	}
	wantTarget := "/media/TV/进击的巨人 (2025)/Season 01/" + wantBasename + ".sc.mkv"
	if preview.ComposedTarget == nil || *preview.ComposedTarget != wantTarget {
		t.Errorf("composed_target = %v, want %q", strOrEmpty(preview.ComposedTarget), wantTarget)
	}
}

func TestPreviewNamingSubtitleSuffixLockedSample(t *testing.T) {
	service := NewService()
	subtitleRef := "sample-tv-subtitle"
	newInput := func(source string) NamingTemplatePreviewInput {
		input := namingPreviewInput(NamingTemplatePreviewInputTemplateKindSubtitleSuffix, source)
		input.SampleRef = &subtitleRef
		return input
	}
	wantDirectory := "/media/TV/进击的巨人 (2025)/Season 01"
	wantBasename := "进击的巨人 S01E07"

	preview, err := service.PreviewNaming(context.Background(), newInput(
		"${defaultValue('und', file.sub.lang)}",
	))
	if err != nil {
		t.Fatalf("PreviewNaming returned error: %v", err)
	}
	if !preview.Valid {
		t.Fatalf("expected valid preview, got errors: %+v", preview.Errors)
	}
	if preview.Rendered != "zh-Hans" {
		t.Errorf("rendered = %q, want zh-Hans", preview.Rendered)
	}
	if preview.LockedDirectory == nil || *preview.LockedDirectory != wantDirectory {
		t.Errorf("locked_directory = %v, want %q", preview.LockedDirectory, wantDirectory)
	}
	if preview.LockedBasename == nil || *preview.LockedBasename != wantBasename {
		t.Errorf("locked_basename = %v, want %q", preview.LockedBasename, wantBasename)
	}
	if preview.LockedExtension == nil || *preview.LockedExtension != "mkv" {
		t.Errorf("locked_extension = %v, want mkv (the bound video extension)", preview.LockedExtension)
	}
	wantTarget := wantDirectory + "/" + wantBasename + ".zh-Hans.ass"
	if preview.ComposedTarget == nil || *preview.ComposedTarget != wantTarget {
		t.Errorf("composed_target = %v, want %q", strOrEmpty(preview.ComposedTarget), wantTarget)
	}

	// Subtitle flags are visible to the template.
	flagPreview, err := service.PreviewNaming(context.Background(), newInput(
		"${file.sub.forced ? 'forced' : ''}",
	))
	if err != nil {
		t.Fatalf("PreviewNaming returned error: %v", err)
	}
	if !flagPreview.Valid || flagPreview.Rendered != "forced" {
		t.Errorf("flag preview rendered = %q, errors %+v", flagPreview.Rendered, flagPreview.Errors)
	}

	// An empty suffix is legal and composes without a stray separator dot.
	emptyPreview, err := service.PreviewNaming(context.Background(), newInput(
		"${file.sub.sdh ? 'sdh' : ''}",
	))
	if err != nil {
		t.Fatalf("PreviewNaming returned error: %v", err)
	}
	if !emptyPreview.Valid || emptyPreview.Rendered != "" {
		t.Errorf("empty suffix preview rendered = %q, errors %+v", emptyPreview.Rendered, emptyPreview.Errors)
	}
	wantEmptyTarget := wantDirectory + "/" + wantBasename + ".ass"
	if emptyPreview.ComposedTarget == nil || *emptyPreview.ComposedTarget != wantEmptyTarget {
		t.Errorf("empty suffix composed_target = %v, want %q", emptyPreview.ComposedTarget, wantEmptyTarget)
	}

	// An unsafe suffix is rejected by the engine validator.
	unsafePreview, err := service.PreviewNaming(context.Background(), newInput(`../escape`))
	if err != nil {
		t.Fatalf("PreviewNaming returned error: %v", err)
	}
	if unsafePreview.Valid {
		t.Fatal("expected invalid preview for unsafe suffix")
	}
	if len(unsafePreview.Errors) != 1 || unsafePreview.Errors[0].Code != "SUBTITLE_SUFFIX_INVALID" {
		t.Errorf("unsafe suffix errors = %+v", unsafePreview.Errors)
	}
}

func TestPreviewNamingSampleSelection(t *testing.T) {
	service := NewService()
	movieRef := "sample-movie"
	input := namingPreviewInput(NamingTemplatePreviewInputTemplateKindVideoFilename, "${sanitize(anime.title)}.${file.ext}")
	input.SampleRef = &movieRef
	preview, err := service.PreviewNaming(context.Background(), input)
	if err != nil {
		t.Fatalf("PreviewNaming returned error: %v", err)
	}
	if !preview.Valid || preview.Rendered != "进击的巨人 剧场版.mp4" {
		t.Errorf("movie sample render = %q, errors %+v", preview.Rendered, preview.Errors)
	}

	unknownRef := "sample-nope"
	input.SampleRef = &unknownRef
	if _, err := service.PreviewNaming(context.Background(), input); err == nil {
		t.Fatal("expected error for unknown sample ref")
	}
}

func TestPreviewNamingUnknownField(t *testing.T) {
	service := NewService()
	preview, err := service.PreviewNaming(context.Background(), namingPreviewInput(
		NamingTemplatePreviewInputTemplateKindVideoFilename, "${Anime.Bogus}.mkv",
	))
	if err != nil {
		t.Fatalf("PreviewNaming returned error: %v", err)
	}
	if preview.Valid {
		t.Fatal("expected invalid preview for unknown field")
	}
	if len(preview.Errors) != 1 || preview.Errors[0].Code != "TEMPLATE_COMPILE_ERROR" {
		t.Fatalf("expected TEMPLATE_COMPILE_ERROR, got %+v", preview.Errors)
	}
	if preview.Errors[0].Field == nil || *preview.Errors[0].Field != TemplateSourceErrorFieldTemplateSource {
		t.Errorf("error field = %v, want template_source", preview.Errors[0].Field)
	}
}

func TestPreviewNamingUnknownFunction(t *testing.T) {
	service := NewService()
	preview, err := service.PreviewNaming(context.Background(), namingPreviewInput(
		NamingTemplatePreviewInputTemplateKindVideoFilename, "${notAFunction(Anime.Title)}",
	))
	if err != nil {
		t.Fatalf("PreviewNaming returned error: %v", err)
	}
	if preview.Valid {
		t.Fatal("expected invalid preview for unknown function")
	}
	if len(preview.Errors) != 1 || preview.Errors[0].Code != "TEMPLATE_COMPILE_ERROR" {
		t.Fatalf("expected TEMPLATE_COMPILE_ERROR, got %+v", preview.Errors)
	}
}

func TestPreviewNamingUnsafeTarget(t *testing.T) {
	service := NewService()
	preview, err := service.PreviewNaming(context.Background(), namingPreviewInput(
		NamingTemplatePreviewInputTemplateKindVideoFilename, `../escape.mkv`,
	))
	if err != nil {
		t.Fatalf("PreviewNaming returned error: %v", err)
	}
	if preview.Valid {
		t.Fatal("expected invalid preview for unsafe target")
	}
	if len(preview.Errors) != 1 || preview.Errors[0].Code != "NAMING_TARGET_INVALID" {
		t.Fatalf("expected NAMING_TARGET_INVALID, got %+v", preview.Errors)
	}
}

// --- helpers ---

func variableNames(variables []TemplateVariable) map[string]bool {
	names := make(map[string]bool, len(variables))
	for _, variable := range variables {
		names[variable.Name] = true
	}
	return names
}

func assertFunctions(t *testing.T, functions []TemplateFunction) {
	t.Helper()
	want := map[string]bool{
		"coalesce": true, "defaultIfEmpty": true, "lower": true, "upper": true,
		"replace": true, "if": true, "eq": true,
	}
	if len(functions) != len(want) {
		t.Errorf("function count = %d, want %d", len(functions), len(want))
	}
	for _, function := range functions {
		if !want[string(function.Name)] {
			t.Errorf("unexpected function %q in allowlist", function.Name)
		}
		if function.Description == "" {
			t.Errorf("function %q has empty description", function.Name)
		}
		if len(function.Arguments) == 0 {
			t.Errorf("function %q has no argument signature", function.Name)
		}
		delete(want, string(function.Name))
	}
	for name := range want {
		t.Errorf("missing function %q from allowlist", name)
	}
}

// The preview must render the same context a real naming operation would: a
// template that reads ${vars...} would otherwise compile (the compiler sees the
// active snapshot) and then fail only in the preview.
func TestPreviewNamingInjectsTheActiveVarsSnapshot(t *testing.T) {
	if err := namingvars.SetActive(namingvars.Vars{"anime": {"group": "S01E07"}}, "sha256:preview-test"); err != nil {
		t.Fatal(err)
	}
	service := NewService()
	preview, err := service.PreviewNaming(context.Background(), namingPreviewInput(
		NamingTemplatePreviewInputTemplateKindVideoFilename,
		"${vars.anime.group}.${file.ext}",
	))
	if err != nil {
		t.Fatalf("PreviewNaming returned error: %v", err)
	}
	if !preview.Valid {
		t.Fatalf("expected a valid preview for a declared naming variable, got %+v", preview.Errors)
	}
	if preview.Rendered == "" || !strings.Contains(preview.Rendered, "S01E07") {
		t.Fatalf("rendered = %q, want the injected variable value", preview.Rendered)
	}
}

func TestPreviewNamingValueNamespaces(t *testing.T) {
	service := NewService()
	preview, err := service.PreviewNaming(context.Background(), namingPreviewInput(
		NamingTemplatePreviewInputTemplateKindVideoFilename,
		"[${rt.subgroup}] ${rt.title} [${rt.sub_lang}][${anibt.language}].${file.ext}",
	))
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Valid || preview.Rendered != "[示例字幕组] 进击的巨人 [SC][CHS].mkv" {
		t.Fatalf("namespace preview = %q, %+v", preview.Rendered, preview.Errors)
	}
	movie := "sample-movie"
	input := namingPreviewInput(NamingTemplatePreviewInputTemplateKindVideoFilename, "${anibt.language}.${file.ext}")
	input.SampleRef = &movie
	preview, err = service.PreviewNaming(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Valid || len(preview.Errors) != 1 || preview.Errors[0].Code != "NAMING_FACT_UNDEFINED" || strings.Join(preview.EmptyFacts, ",") != "anibt.language" {
		t.Fatalf("non-AniBT sample preview = %+v", preview)
	}
	// A missing value that leaves a valid name renders empty and is listed.
	input = namingPreviewInput(NamingTemplatePreviewInputTemplateKindVideoFilename, "${anime.title} [${anime.title_override}].${file.ext}")
	input.SampleRef = &movie
	preview, err = service.PreviewNaming(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Valid || strings.Contains(preview.Rendered, "[") || strings.Join(preview.EmptyFacts, ",") != "anime.title_override" {
		t.Fatalf("empty-value preview = %q %v %+v", preview.Rendered, preview.EmptyFacts, preview.Errors)
	}
	catalog, err := service.GetNamingCatalog(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, field := range catalog.Fields {
		paths[field.Path] = field.Nullable
	}
	for _, path := range []string{"rt.title", "rt.subgroup", "rt.resolution", "rt.source", "rt.codec", "rt.sub_lang", "anibt.resolution", "anibt.subtitle", "anibt.language", "anibt.format", "anibt.tag"} {
		if nullable, ok := paths[path]; !ok || !nullable {
			t.Fatalf("catalog field %s present=%v nullable=%v", path, ok, nullable)
		}
	}
}

func strOrEmpty(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}
