package notification

import (
	"strings"
	"testing"
)

func TestSemanticAttachmentsStayAtTheirTemplatePosition(t *testing.T) {
	facts := map[string]any{
		"anime.title": "<Show & Co>", "release.title": "Episode 1",
		"bgm.id": "123", "tmdb.id": "456", "tmdb.kind": "tv",
	}
	body := "{{image}}\n{{release.title}}\n{{link(bgm.link, 'Bangumi')}}\n{{link(tmdb.link, 'TMDB')}}"
	message, ok := RenderMessage(TriggerDownloadSubmitted, "{{anime.title}}", body, facts)
	if !ok || !message.Valid() || len(message.Warnings) != 0 {
		t.Fatalf("semantic render = %+v", message)
	}
	if message.Body != "Episode 1\nBangumi\nTMDB" || strings.Contains(message.Body, "http") {
		t.Fatalf("semantic render text = %q", message.Body)
	}
	if len(message.BodyParts) != 5 || message.BodyParts[0].Link != nil || message.BodyParts[2].Link == nil || message.BodyParts[4].Link == nil {
		t.Fatalf("body parts = %+v", message.BodyParts)
	}
	if Links(message.BodyParts)[0].URL != "https://bgm.tv/subject/123" || Links(message.BodyParts)[1].URL != "https://www.themoviedb.org/tv/456" {
		t.Fatalf("inline links = %+v", Links(message.BodyParts))
	}
	attachments, ok := SemanticAttachments(TriggerDownloadSubmitted, "{{anime.title}}", body, facts)
	if !ok || !attachments.Image || len(attachments.Links) != 2 {
		t.Fatalf("attachments = %+v", attachments)
	}
	if attachments.Links[0].URL != "https://bgm.tv/subject/123" || attachments.Links[1].URL != "https://www.themoviedb.org/tv/456" {
		t.Fatalf("links = %+v", attachments.Links)
	}
}

func TestMultilineSemanticLinkFollowsLineGate(t *testing.T) {
	const body = "{{showIf(anime.group)}}{{link(\n  bgm.link, 'Bangumi'\n)}}\n{{link(\n  tmdb.link, 'TMDB'\n)}}"
	facts := map[string]any{"bgm.id": "123", "tmdb.id": "456", "tmdb.kind": "tv"}
	for _, group := range []string{"", "Example Group"} {
		facts["anime.group"] = group
		message, ok := RenderMessage(TriggerDownloadSubmitted, "Title", body, facts)
		if !ok || !message.Valid() {
			t.Fatalf("group %q: invalid custom template: %+v", group, message.Errors)
		}
		attachments, ok := SemanticAttachments(TriggerDownloadSubmitted, "Title", body, facts)
		want := []string{"TMDB"}
		if group != "" {
			want = []string{"Bangumi", "TMDB"}
		}
		if !ok || message.Body != strings.Join(want, "\n") || len(attachments.Links) != len(want) {
			t.Fatalf("group %q: body %q, attachments %+v", group, message.Body, attachments)
		}
		for i, label := range want {
			if attachments.Links[i].Label != label {
				t.Fatalf("group %q: links = %+v", group, attachments.Links)
			}
		}
	}
}

func TestImageDirectiveLinesKeepSourceOffsetsAndDisappear(t *testing.T) {
	source := "{{ image }}\n{{anime.title}}\n{{unknown}}\n{{ link(bgm.link, 'Bangumi') }}"
	errors, ok := Compile(TriggerDownloadSubmitted, "title", source)
	if !ok || len(errors) != 1 {
		t.Fatalf("compile errors = %+v", errors)
	}
	start := strings.Index(source, "{{unknown}}")
	if errors[0].Start != start || errors[0].End != start+len("{{unknown}}") {
		t.Fatalf("error span = %d..%d, want %d..%d", errors[0].Start, errors[0].End, start, start+len("{{unknown}}"))
	}
	message, _ := RenderMessage(TriggerDownloadSubmitted, "title", "{{ image }}\n{{anime.title}}\n{{ link(bgm.link, 'Bangumi') }}", map[string]any{"anime.title": "Show", "bgm.id": "123"})
	if message.Body != "Show\nBangumi" {
		t.Fatalf("image line left a blank line or link moved: %q", message.Body)
	}
}

func TestSemanticAttachmentsRejectUnsafeURLsAndMissingMetadata(t *testing.T) {
	if errors, ok := Compile(TriggerDownloadSubmitted, "title", "{{image.url}}"); !ok || len(errors) != 1 {
		t.Fatalf("image.url compile errors = %+v", errors)
	}
	for _, source := range []string{
		"{{link('https://example.com/?passkey=private', 'Bad')}}",
		"{{link('https://user:pass@example.com/', 'Bad')}}",
		"{{image('http://example.com/a.jpg')}}",
		"{{image('https://example.com/a.jpg?token=private')}}",
		"{{image('https://127.0.0.1/a.jpg')}}",
		"{{image('https://metadata.google.internal/a.jpg')}}",
		"{{link(anime.title, 'Bad')}}",
		"{{link(bgm.link, anime.title)}}",
	} {
		if errors, _ := Compile(TriggerDownloadSubmitted, "title", source); len(errors) != 1 {
			t.Errorf("unsafe directive compiled: %s: %+v", source, errors)
		}
	}
	attachments, ok := SemanticAttachments(TriggerDownloadSubmitted, "title", "{{image}}\n{{link(bgm.link, 'Bangumi')}}\n{{link(tmdb.link, 'TMDB')}}", map[string]any{
		"bgm.id": "javascript:alert(1)", "tmdb.id": "42/../../x", "tmdb.kind": "movie",
	})
	if !ok || !attachments.Image || len(attachments.Links) != 0 {
		t.Fatalf("unsafe facts produced links: %+v", attachments)
	}
	variables, _ := Variables(TriggerDownloadSubmitted)
	names := make(map[string]bool)
	for _, variable := range variables {
		names[variable.Name] = true
	}
	for _, name := range append(append([]string{ImageVariable}, LinkVariables...), ImageVariables...) {
		if !names[name] {
			t.Errorf("semantic variable missing from catalog: %s", name)
		}
	}
	if names["link"] {
		t.Fatal("legacy link variable is still exposed")
	}
}

func TestExplicitProviderImageAndCustomAttachments(t *testing.T) {
	body := "{{release.title}}\n{{image(bgm.image)}}\n{{link('https://example.com/release', '自定义文字')}}"
	message, _ := RenderMessage(TriggerDownloadSubmitted, "title", body, Sample(TriggerDownloadSubmitted))
	if !message.Valid() || message.Body == "" {
		t.Fatalf("attachment-only body = %+v", message)
	}
	attachments, _ := SemanticAttachments(TriggerDownloadSubmitted, "title", body, nil)
	if !attachments.Image || attachments.ImageSource != "bgm" || len(attachments.Links) != 1 || attachments.Links[0].Label != "自定义文字" {
		t.Fatalf("attachments = %+v", attachments)
	}
	custom, _ := SemanticAttachments(TriggerDownloadSubmitted, "title", "{{image('https://example.com/cover.jpg')}}", nil)
	if custom.ImageSource != "custom" || custom.ImageURL != "https://example.com/cover.jpg" {
		t.Fatalf("custom image = %+v", custom)
	}
}

func TestTitleLinkKeepsOnlyItsLabelAndNeverBecomesAnAttachment(t *testing.T) {
	facts := map[string]any{"anime.title": "Show", "bgm.id": "123"}
	const title = "{{link(bgm.link, 'Bangumi')}}"
	const body = "{{link(bgm.link, 'Body')}}"
	message, ok := RenderMessage(TriggerDownloadSubmitted, title, body, facts)
	if !ok || !message.Valid() || message.Title != "Bangumi" {
		t.Fatalf("title link render = %+v", message)
	}
	found := false
	for _, warning := range message.Warnings {
		found = found || warning.Code == "link_in_title"
	}
	if !found {
		t.Fatalf("title link loss is not reported: %+v", message.Warnings)
	}
	attachments, ok := SemanticAttachments(TriggerDownloadSubmitted, title, body, facts)
	if !ok || len(attachments.Links) != 1 || attachments.Links[0].Label != "Body" {
		t.Fatalf("title link became an attachment: %+v", attachments.Links)
	}
}

func TestBareProviderImageResolvesLikeTheImageDirective(t *testing.T) {
	facts := Sample(TriggerDownloadSubmitted)
	const body = "{{bgm.image}}\n{{anime.title}}"
	message, ok := RenderMessage(TriggerDownloadSubmitted, "title", body, facts)
	if !ok || !message.Valid() || message.Body != FormatFact(facts["anime.title"]) {
		t.Fatalf("bare provider image render = %+v", message)
	}
	attachments, ok := SemanticAttachments(TriggerDownloadSubmitted, "title", body, facts)
	if !ok || !attachments.Image || attachments.ImageSource != "bgm" {
		t.Fatalf("bare provider image attachment = %+v", attachments)
	}
	automatic, _ := SemanticAttachments(TriggerDownloadSubmitted, "title", "{{image}}", facts)
	if !automatic.Image || automatic.ImageSource != "" {
		t.Fatalf("automatic image changed = %+v", automatic)
	}
}

// Every advertised provider image alias means exactly what the explicit
// image(...) call means: same attachment, same text (none).
func TestEveryBareProviderImageAliasMatchesItsExplicitForm(t *testing.T) {
	for _, trigger := range Triggers {
		facts := Sample(trigger)
		for _, name := range ImageVariables {
			bare, ok := RenderMessage(trigger, "title", "{{"+name+"}}", facts)
			explicit, _ := RenderMessage(trigger, "title", "{{image("+name+")}}", facts)
			if !ok || !bare.Valid() || !explicit.Valid() || strings.TrimSpace(bare.Body) != "" ||
				!bare.Attachments.Image || bare.Attachments.ImageSource != explicit.Attachments.ImageSource {
				t.Fatalf("%s / %s bare=%+v explicit=%+v", trigger, name, bare, explicit)
			}
		}
	}
	// A frozen binding for a poster does not make the same event's binding a
	// text fact or add a link to the body.
	if errors, _ := Compile(TriggerDownloadFailed, "title", "{{bgm.link}}"); len(errors) != 1 {
		t.Fatalf("download_failed exposed an image binding as a link: %+v", errors)
	}
}

func TestStandaloneSubtitleListRepeatsLabelAndOmitsEmptyLine(t *testing.T) {
	body := "文件：{{out.name}}\n字幕：{{out.subtitles}}\n目录：{{out.dir}}"
	facts := Sample(TriggerFileOperationCompleted)
	facts["out.subtitles"] = []any{"Show S01E01.ja.ass", "Show S01E01.zh-Hans.ass"}
	message, _ := RenderMessage(TriggerFileOperationCompleted, "title", body, facts)
	if !message.Valid() || !strings.Contains(message.Body, "字幕：Show S01E01.ja.ass\n字幕：Show S01E01.zh-Hans.ass\n目录：") {
		t.Fatalf("subtitle list = %q / %+v", message.Body, message.Errors)
	}
	facts["out.subtitles"] = []any{}
	message, _ = RenderMessage(TriggerFileOperationCompleted, "title", body, facts)
	if !message.Valid() || strings.Contains(message.Body, "字幕：") || !strings.Contains(message.Body, "文件：") || !strings.Contains(message.Body, "目录：") {
		t.Fatalf("empty subtitle list = %q / %+v", message.Body, message.Errors)
	}
}
