package application

import (
	"bansoko/internal/notifypresent"
	"bansoko/internal/templatecatalog/notification"
	"context"
	"strings"
	"testing"
	"unicode/utf8"

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
		catalog, err := service.GetNotificationCatalog(ctx, string(trigger), "", "")
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
	downloadCatalog, _ := service.GetNotificationCatalog(ctx, string(NotificationTriggerKindDownloadSubmitted), "", "")
	if !variableNames(downloadCatalog.Variables)["download.task"] {
		t.Error("download_submitted missing download.task")
	}
	fileCatalog, _ := service.GetNotificationCatalog(ctx, string(NotificationTriggerKindFileOperationCompleted), "", "")
	fileNames := variableNames(fileCatalog.Variables)
	for _, want := range []string{"out.name", "out.path", "op.kind"} {
		if !fileNames[want] {
			t.Errorf("file_operation_completed missing variable %q", want)
		}
	}

	// An unknown catalog version is rejected.
	if _, err := service.GetNotificationCatalog(ctx, string(NotificationTriggerKindFileOperationCompleted), "notification-catalog-v0", ""); err == nil {
		t.Error("expected error for an unsupported catalog version")
	}
}

func TestNotificationCatalogLocaleDefaults(t *testing.T) {
	service := NewService()
	ctx := context.Background()

	zh, err := service.GetNotificationCatalog(ctx, string(NotificationTriggerKindDownloadSubmitted), "", "")
	if err != nil {
		t.Fatalf("zh catalog returned error: %v", err)
	}
	en, err := service.GetNotificationCatalog(ctx, string(NotificationTriggerKindDownloadSubmitted), "", notification.LocaleEN)
	if err != nil {
		t.Fatalf("en catalog returned error: %v", err)
	}
	wantTitle, wantBody := notification.DefaultsFor(string(NotificationTriggerKindDownloadSubmitted), notification.LocaleEN)
	if en.DefaultTemplate.TitleTemplate != wantTitle || en.DefaultTemplate.BodyTemplate != wantBody {
		t.Errorf("en default = %q / %q, want the English system default", en.DefaultTemplate.TitleTemplate, en.DefaultTemplate.BodyTemplate)
	}
	if zh.DefaultTemplate.TitleTemplate == en.DefaultTemplate.TitleTemplate {
		t.Error("zh-CN and en defaults are identical")
	}
	if _, err := service.GetNotificationCatalog(ctx, string(NotificationTriggerKindDownloadSubmitted), "", "fr"); err == nil {
		t.Error("expected error for an unsupported locale")
	}
}

func TestNotificationCatalogUnknownTrigger(t *testing.T) {
	service := NewService()
	if _, err := service.GetNotificationCatalog(context.Background(), "not_a_trigger", "", ""); err == nil {
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
	for _, want := range []string{"a.title", "ep.end", "f.ext", "m.resolution", "rt.title", "anibt.lang", "bgm.title", "tmdb.title", "frt.group"} {
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
	if len(catalog.Functions) != len(naming.GenerateCatalogDocument().Functions) {
		t.Fatalf("function count = %d, want %d", len(catalog.Functions), len(naming.GenerateCatalogDocument().Functions))
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
	if preview.Title != "已加入下载：葬送的芙莉莲" {
		t.Errorf("title = %q, want the registry sample title", preview.Title)
	}
	wantBody := "[示例字幕组] 葬送的芙莉莲 - 07 [WebRip 1080p HEVC-10bit AAC][CHS]\n字幕组：示例字幕组"
	if preview.Body != wantBody {
		t.Errorf("body = %q, want %q", preview.Body, wantBody)
	}
	if len(preview.Warnings) != 0 {
		t.Errorf("expected no warnings, got %+v", preview.Warnings)
	}
}

func TestRenderNotificationTestMarkerKeepsTitleWithinRenderedBound(t *testing.T) {
	service := NewService()
	trigger := string(NotificationTriggerKindDownloadSubmitted)
	catalog := notification.CatalogVersion
	const marker = "[TEST] "
	limit := notification.MaxRenderedTitleChars - utf8.RuneCountInString(marker)

	t.Run("ASCII maximum", func(t *testing.T) {
		raw := strings.Repeat("a", notification.MaxRenderedTitleChars)
		title, body, err := service.RenderNotification(raw, "body", trigger, catalog)
		if err != nil {
			t.Fatal(err)
		}
		if body != "body" {
			t.Fatalf("body = %q", body)
		}
		if title != marker+strings.Repeat("a", limit) {
			t.Fatalf("title = %q (runes %d), want marker plus %d a runes", title, utf8.RuneCountInString(title), limit)
		}
		if got := utf8.RuneCountInString(title); got != notification.MaxRenderedTitleChars {
			t.Fatalf("title runes = %d, want %d", got, notification.MaxRenderedTitleChars)
		}
	})

	t.Run("multibyte maximum truncates on a rune boundary", func(t *testing.T) {
		sample := notification.Sample(trigger)
		release, _ := sample["release.title"].(string)
		releaseRunes := []rune(release)
		if len(releaseRunes) == 0 {
			t.Fatal("sample release title is empty")
		}
		// Leave at least the marker width of CJK filler after the repeats so the
		// retained last rune is multibyte: a byte truncation would split it.
		repeats := (notification.MaxRenderedTitleChars - len(marker)) / len(releaseRunes)
		filler := notification.MaxRenderedTitleChars - repeats*len(releaseRunes)
		template := strings.Repeat("{{release.title}}", repeats) + strings.Repeat("界", filler)
		if len(template) > 4096 {
			t.Fatalf("test template is %d bytes, above the source bound", len(template))
		}
		rendered, ok := notification.RenderMessage(trigger, template, "body", sample)
		if !ok || !rendered.Valid() {
			t.Fatalf("unmarked render = %+v %+v", rendered, rendered.Errors)
		}
		if got := utf8.RuneCountInString(rendered.Title); got != notification.MaxRenderedTitleChars {
			t.Fatalf("unmarked title runes = %d, want %d", got, notification.MaxRenderedTitleChars)
		}
		title, body, err := service.RenderNotification(template, "body", trigger, catalog)
		if err != nil {
			t.Fatal(err)
		}
		want := marker + string([]rune(rendered.Title)[:limit])
		if body != "body" || title != want {
			t.Fatalf("title = %q (runes %d), want %q", title, utf8.RuneCountInString(title), want)
		}
		if !utf8.ValidString(title) || utf8.RuneCountInString(title) != notification.MaxRenderedTitleChars {
			t.Fatalf("marked title is not a %d-rune valid string", notification.MaxRenderedTitleChars)
		}
		if last := []rune(title)[len([]rune(title))-1]; last < utf8.RuneSelf {
			t.Fatalf("truncation did not retain the multibyte boundary rune: last = %q", last)
		}
	})

	t.Run("empty title keeps the bare marker", func(t *testing.T) {
		title, body, err := service.RenderNotification("", "body", trigger, catalog)
		if err != nil {
			t.Fatal(err)
		}
		if title != "[TEST]" || body != "body" {
			t.Fatalf("title = %q body = %q", title, body)
		}
	})

	t.Run("oversized pre-marker title is still rejected", func(t *testing.T) {
		sample := notification.Sample(trigger)
		release, _ := sample["release.title"].(string)
		repeats := notification.MaxRenderedTitleChars/utf8.RuneCountInString(release) + 2
		if _, _, err := service.RenderNotification(strings.Repeat("{{release.title}}", repeats), "body", trigger, catalog); err == nil {
			t.Fatal("oversized rendered title was accepted")
		}
	})
}

func TestPreviewNotificationReturnsStructuredAttachments(t *testing.T) {
	service := NewService()
	preview, err := service.PreviewNotification(context.Background(), NotificationTemplatePreviewInput{
		BodyTemplate:    "{{release.title}}\n{{image(bgm.image)}}\n{{link(download.link, '下载')}}\n{{button(release.link, '发布页')}}",
		CatalogVersion:  NotificationTemplatePreviewInputCatalogVersionNotificationCatalogV1,
		LanguageVersion: NotificationTemplatePreviewInputLanguageVersionBansokoTemplate1,
		TitleTemplate:   "{{anime.title}}",
		TriggerKind:     NotificationTriggerKindDownloadSubmitted,
	})
	if err != nil || !preview.Valid {
		t.Fatalf("preview err=%v errors=%+v", err, preview.Errors)
	}
	if preview.Attachments.Image == nil || preview.Attachments.Image.Source != "bgm" || preview.Attachments.Image.URL != nil {
		t.Fatalf("image attachment = %+v", preview.Attachments.Image)
	}
	if len(preview.Attachments.Links) != 1 || preview.Attachments.Links[0].Provider != "download" || preview.Attachments.Links[0].Label != "下载" {
		t.Fatalf("link attachments = %+v", preview.Attachments.Links)
	}
	// The body keeps the link at its template position: the image line is gone
	// and the link is the last body part, not a footer attachment.
	if len(preview.BodyParts) != 3 || preview.BodyParts[0].Link != nil || preview.BodyParts[2].Link == nil || preview.BodyParts[2].Link.Provider != "download" {
		t.Fatalf("body parts = %+v", preview.BodyParts)
	}
	if len(preview.Attachments.Buttons) != 1 || preview.Attachments.Buttons[0].Provider != "release" || preview.Attachments.Buttons[0].Label != "发布页" {
		t.Fatalf("button attachments = %+v", preview.Attachments.Buttons)
	}
	if len(preview.Presentation.Telegram.Buttons) != 1 || preview.Presentation.Telegram.Buttons[0] != preview.Attachments.Buttons[0] || strings.Contains(preview.Presentation.Telegram.Text, "发布页") {
		t.Fatalf("telegram presentation = %+v", preview.Presentation.Telegram)
	}
	if !strings.HasSuffix(preview.Body, "下载") {
		t.Fatalf("body = %q", preview.Body)
	}
	// The preview carries the same channel presentation production sends: a
	// short message with an image is a Telegram photo, and Feishu can only
	// offer the image as a link when a URL exists (the sample has none).
	if preview.Presentation.Telegram.Mode != notifypresent.TelegramModePhoto || preview.Presentation.Telegram.ImageOmitted {
		t.Fatalf("telegram presentation = %+v", preview.Presentation.Telegram)
	}
	if !preview.Presentation.Feishu.ImageOmitted || preview.Presentation.Feishu.Image != nil {
		t.Fatalf("feishu presentation = %+v", preview.Presentation.Feishu)
	}
	if preview.Presentation.Webhook.Message.Image == nil || preview.Presentation.Webhook.Message.Image.Source != "bgm" {
		t.Fatalf("webhook presentation = %+v", preview.Presentation.Webhook.Message)
	}
	if len(preview.Presentation.Webhook.Message.Links) != 1 || preview.Presentation.Webhook.Message.Links[0].Provider != "download" {
		t.Fatalf("webhook links = %+v", preview.Presentation.Webhook.Message.Links)
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

// Every text fact has a real sample: a preview that uses all catalog entries
// renders with no null warnings. Image entries are attachment directives and
// deliberately have no text sample.
func TestPreviewNotificationSamplesEveryVariable(t *testing.T) {
	service := NewService()
	for _, trigger := range []NotificationTriggerKind{NotificationTriggerKindDownloadSubmitted, NotificationTriggerKindFileOperationCompleted, NotificationTriggerKindFileOperationFailed} {
		variables, _ := notification.Variables(string(trigger))
		body := ""
		for _, variable := range variables {
			if variable.SampleValue == "" && variable.Name != notification.ImageVariable && !strings.HasSuffix(variable.Name, ".image") {
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

// A gated attachment-only line with an invalid argument was accepted by v1:
// the line is dropped and the invalid directive is reported as a warning, not
// an error. The preview stays valid.
func TestPreviewNotificationReportsGatedAttachmentAsWarning(t *testing.T) {
	service := NewService()
	preview, err := service.PreviewNotification(context.Background(), NotificationTemplatePreviewInput{
		BodyTemplate:    "Text\n{{showIf(anime.title)}}{{image(bgm.id)}}",
		CatalogVersion:  NotificationTemplatePreviewInputCatalogVersionNotificationCatalogV1,
		LanguageVersion: NotificationTemplatePreviewInputLanguageVersionBansokoTemplate1,
		TitleTemplate:   "{{anime.title}}",
		TriggerKind:     NotificationTriggerKindDownloadSubmitted,
	})
	if err != nil || !preview.Valid {
		t.Fatalf("preview=%+v error=%v", preview, err)
	}
	if len(preview.Errors) != 0 {
		t.Fatalf("a gated invalid attachment must not be an error: %+v", preview.Errors)
	}
	if len(preview.Warnings) != 1 || preview.Warnings[0].Code != "attachment_without_effect" {
		t.Fatalf("expected an attachment_without_effect warning, got %+v", preview.Warnings)
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
	want := "葬送的芙莉莲 - E07.mkv"
	if preview.Rendered != want {
		t.Errorf("rendered = %q, want %q", preview.Rendered, want)
	}
	if preview.ComposedTarget == nil || *preview.ComposedTarget != "/media/TV/葬送的芙莉莲 (2023)/Season 01/"+want {
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
	if preview.Rendered != "/library/葬送的芙莉莲/Season 01" {
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
	if preview.LockedDirectory == nil || *preview.LockedDirectory != "/media/TV/葬送的芙莉莲 (2023)/Season 01" {
		t.Errorf("locked_directory = %v", preview.LockedDirectory)
	}
	// The default rules render the released name from the library layout only;
	// the bound video's media facts play no part in it.
	wantBasename := "葬送的芙莉莲 S01E07"
	if preview.LockedBasename == nil || *preview.LockedBasename != wantBasename {
		t.Errorf("locked_basename = %v", strOrEmpty(preview.LockedBasename))
	}
	if preview.LockedExtension == nil || *preview.LockedExtension != "mkv" {
		t.Errorf("locked_extension = %v", preview.LockedExtension)
	}
	wantTarget := "/media/TV/葬送的芙莉莲 (2023)/Season 01/" + wantBasename + ".sc.mkv"
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
	wantDirectory := "/media/TV/葬送的芙莉莲 (2023)/Season 01"
	wantBasename := "葬送的芙莉莲 S01E07"

	preview, err := service.PreviewNaming(context.Background(), newInput(
		"${defaultValue('und', file.sub.lang_tag)}",
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
		"replace": true, "if": true, "eq": true, "showIf": true, "link": true, "button": true, "image": true,
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

func TestPreviewNamingValueNamespaces(t *testing.T) {
	service := NewService()
	preview, err := service.PreviewNaming(context.Background(), namingPreviewInput(
		NamingTemplatePreviewInputTemplateKindVideoFilename,
		"[${rt.group}] ${rt.title} [${rt.lang}][${anibt.lang}].${file.ext}",
	))
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Valid || preview.Rendered != "[示例字幕组] 葬送的芙莉莲 [SC][CHS].mkv" {
		t.Fatalf("namespace preview = %q, %+v", preview.Rendered, preview.Errors)
	}
	movie := "sample-movie"
	input := namingPreviewInput(NamingTemplatePreviewInputTemplateKindVideoFilename, "${anibt.lang}.${file.ext}")
	input.SampleRef = &movie
	preview, err = service.PreviewNaming(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Valid || len(preview.Errors) != 1 || preview.Errors[0].Code != "NAMING_FACT_UNDEFINED" || strings.Join(preview.EmptyFacts, ",") != "anibt.lang" {
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
	for _, path := range []string{"rt.title", "rt.group", "rt.res", "rt.source", "rt.vc", "rt.ac", "rt.lang", "anibt.res", "anibt.subtitle", "anibt.lang", "anibt.format", "anibt.tag"} {
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
