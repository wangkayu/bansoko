package notification

import (
	"strings"
	"testing"
)

func TestSemanticAttachmentsAreNotTextFacts(t *testing.T) {
	facts := map[string]any{
		"anime.title": "<Show & Co>", "release.title": "Episode 1",
		"bgm.id": "123", "tmdb.id": "456", "tmdb.kind": "tv",
	}
	body := "{{image}}\n{{release.title}}\n{{link(bgm.link, 'Bangumi')}}\n{{link(tmdb.link, 'TMDB')}}"
	message, ok := RenderMessage(TriggerDownloadSubmitted, "{{anime.title}}", body, facts)
	if !ok || !message.Valid() || len(message.Warnings) != 0 {
		t.Fatalf("semantic render = %+v", message)
	}
	if message.Body != "Episode 1" || strings.Contains(message.Body, "http") {
		t.Fatalf("semantic directives leaked into text: %q", message.Body)
	}
	attachments, ok := SemanticAttachments(TriggerDownloadSubmitted, "{{anime.title}}", body, facts)
	if !ok || !attachments.Image || len(attachments.Links) != 2 {
		t.Fatalf("attachments = %+v", attachments)
	}
	if attachments.Links[0].URL != "https://bgm.tv/subject/123" || attachments.Links[1].URL != "https://www.themoviedb.org/tv/456" {
		t.Fatalf("links = %+v", attachments.Links)
	}
}

func TestSemanticDirectiveLinesKeepSourceOffsetsAndAllowWhitespace(t *testing.T) {
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
	if message.Body != "Show" {
		t.Fatalf("whitespace directives left blank lines: %q", message.Body)
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
