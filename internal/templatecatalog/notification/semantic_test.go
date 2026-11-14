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
	message, ok := RenderMessage(TriggerDownloadSubmitted, "{{anime.title}}", "{{image}}\n{{release.title}}\n{{link}}", facts)
	if !ok || !message.Valid() || len(message.Warnings) != 0 {
		t.Fatalf("semantic render = %+v", message)
	}
	if message.Body != "Episode 1" || strings.Contains(message.Body, "http") {
		t.Fatalf("semantic directives leaked into text: %q", message.Body)
	}
	attachments, ok := SemanticAttachments(TriggerDownloadSubmitted, "{{anime.title}}", "{{image}}\n{{release.title}}\n{{link}}", facts)
	if !ok || !attachments.Image || !attachments.Link || len(attachments.Links) != 2 {
		t.Fatalf("attachments = %+v", attachments)
	}
	if attachments.Links[0].URL != "https://bgm.tv/subject/123" || attachments.Links[1].URL != "https://www.themoviedb.org/tv/456" {
		t.Fatalf("links = %+v", attachments.Links)
	}
}

func TestSemanticDirectiveLinesKeepSourceOffsetsAndAllowWhitespace(t *testing.T) {
	source := "{{ image }}\n{{anime.title}}\n{{unknown}}\n{{ link }}"
	errors, ok := Compile(TriggerDownloadSubmitted, "title", source)
	if !ok || len(errors) != 1 {
		t.Fatalf("compile errors = %+v", errors)
	}
	start := strings.Index(source, "{{unknown}}")
	if errors[0].Start != start || errors[0].End != start+len("{{unknown}}") {
		t.Fatalf("error span = %d..%d, want %d..%d", errors[0].Start, errors[0].End, start, start+len("{{unknown}}"))
	}
	message, _ := RenderMessage(TriggerDownloadSubmitted, "title", "{{ image }}\n{{anime.title}}\n{{ link }}", map[string]any{"anime.title": "Show"})
	if message.Body != "Show" {
		t.Fatalf("whitespace directives left blank lines: %q", message.Body)
	}
}

func TestSemanticAttachmentsRejectArbitraryURLsAndMissingMetadata(t *testing.T) {
	if errors, ok := Compile(TriggerDownloadSubmitted, "title", "{{image.url}}"); !ok || len(errors) != 1 {
		t.Fatalf("image.url compile errors = %+v", errors)
	}
	attachments, ok := SemanticAttachments(TriggerDownloadSubmitted, "title", "{{image}}\n{{link}}", map[string]any{
		"bgm.id": "javascript:alert(1)", "tmdb.id": "42/../../x", "tmdb.kind": "movie",
	})
	if !ok || !attachments.Image || !attachments.Link || len(attachments.Links) != 0 {
		t.Fatalf("unsafe facts produced links: %+v", attachments)
	}
	variables, _ := Variables(TriggerDownloadSubmitted)
	if len(variables) < 2 || variables[len(variables)-2].Name != ImageVariable || variables[len(variables)-1].Name != LinkVariable {
		t.Fatalf("semantic variables missing from catalog: %+v", variables[len(variables)-2:])
	}
}
