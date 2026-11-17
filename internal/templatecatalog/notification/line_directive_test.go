package notification

import (
	"strings"
	"testing"
)

func renderBody(t *testing.T, trigger, body string, facts map[string]any) Message {
	t.Helper()
	message, ok := RenderMessage(trigger, "标题", body, facts)
	if !ok {
		t.Fatalf("unknown trigger %s", trigger)
	}
	return message
}

// showIf gates the rest of its own line: any non-empty argument shows it, all
// empty arguments remove the line and its line break, and the directive itself
// never appears in the text.
func TestShowIfKeepsItsLineWhenAnyArgumentHasValue(t *testing.T) {
	body := "{{showIf(media.resolution, media.video_codec)}}画面：{{media.resolution}} / {{media.video_codec}}"
	message := renderBody(t, TriggerFileOperationCompleted, body, map[string]any{"media.resolution": "1080p"})
	if !message.Valid() {
		t.Fatalf("render errors = %+v", message.Errors)
	}
	if message.Body != "画面：1080p / " {
		t.Fatalf("body = %q", message.Body)
	}
	if strings.Contains(message.Body, showIfFunction) {
		t.Fatalf("the directive leaked into the text: %q", message.Body)
	}
}

func TestShowIfDropsTheWholeLineWhenEveryArgumentIsEmpty(t *testing.T) {
	facts := map[string]any{}
	bodies := map[string]string{
		"middle line":       "第一行\n{{showIf(media.resolution, media.video_codec)}}画面：{{media.resolution}}\n第三行",
		"middle line, crlf": "第一行\r\n{{showIf(media.resolution)}}画面：{{media.resolution}}\r\n第三行",
		"final line":        "第一行\n{{showIf(media.resolution)}}画面：{{media.resolution}}",
		"final line, break": "第一行\n{{showIf(media.resolution)}}画面：{{media.resolution}}\n",
		"only line":         "{{showIf(media.resolution)}}画面：{{media.resolution}}",
	}
	wants := map[string]string{
		"middle line":       "第一行\n第三行",
		"middle line, crlf": "第一行\r\n第三行",
		"final line":        "第一行",
		"final line, break": "第一行",
		"only line":         "",
	}
	for name, body := range bodies {
		message := renderBody(t, TriggerFileOperationCompleted, body, facts)
		if !message.Valid() {
			t.Fatalf("%s: render errors = %+v", name, message.Errors)
		}
		if message.Body != wants[name] {
			t.Fatalf("%s: body = %q, want %q", name, message.Body, wants[name])
		}
		if len(message.Warnings) != 0 {
			t.Fatalf("%s: a dropped line warned about its empty gate: %+v", name, message.Warnings)
		}
	}
}

// A dropped line that holds a whole field hides the label with the value, and
// a shown line still reports the empty variables it renders.
func TestShowIfHidesLabelsAndKeepsWarnings(t *testing.T) {
	hidden := renderBody(t, TriggerDownloadSubmitted, "开头\n{{showIf(bgm.id)}}BGM：{{bgm.id}}\n结尾", map[string]any{})
	if hidden.Body != "开头\n结尾" {
		t.Fatalf("hidden body = %q", hidden.Body)
	}
	if len(hidden.Warnings) != 0 {
		t.Fatalf("hidden warnings = %+v", hidden.Warnings)
	}
	shown := renderBody(t, TriggerDownloadSubmitted, "开头\n{{showIf(bgm.id)}}BGM：{{bgm.id}}\n结尾", map[string]any{"bgm.id": "123"})
	if shown.Body != "开头\nBGM：123\n结尾" {
		t.Fatalf("shown body = %q", shown.Body)
	}
	partial := renderBody(t, TriggerFileOperationCompleted, "{{showIf(release.title, media.resolution)}}画面：{{media.resolution}}", map[string]any{"release.title": "示例"})
	if partial.Body != "画面：" {
		t.Fatalf("partial body = %q", partial.Body)
	}
	if len(partial.Warnings) != 1 || partial.Warnings[0].Variable != "media.resolution" {
		t.Fatalf("partial warnings = %+v", partial.Warnings)
	}
}

// A gated line that is also a standalone list keeps the list rules: the prefix
// repeats per item and an empty list drops the line even when the gate passes.
func TestShowIfComposesWithAStandaloneListLine(t *testing.T) {
	body := "{{showIf(out.subtitles, release.title)}}字幕：{{out.subtitles}}"
	empty := renderBody(t, TriggerFileOperationCompleted, body, map[string]any{
		"release.title": "示例", "out.subtitles": []string{},
	})
	if empty.Body != "" {
		t.Fatalf("empty list body = %q", empty.Body)
	}
	listed := renderBody(t, TriggerFileOperationCompleted, body, map[string]any{
		"release.title": "示例", "out.subtitles": []string{"a.ass", "b.ass"},
	})
	if listed.Body != "字幕：a.ass\n字幕：b.ass" {
		t.Fatalf("listed body = %q", listed.Body)
	}
}

// An image directive produces a message-level attachment, never text: a gated
// line that carries only images leaves no blank line behind. A link line is
// text in place, so it keeps its line.
func TestShowIfLeavesNoBlankLineForImageOnlyLines(t *testing.T) {
	body := "开头\n{{showIf(bgm.id)}}{{image}}\n结尾"
	for _, facts := range []map[string]any{{}, {"bgm.id": "123"}} {
		message := renderBody(t, TriggerDownloadSubmitted, body, facts)
		if message.Body != "开头\n结尾" {
			t.Fatalf("body with facts %v = %q", facts, message.Body)
		}
	}
}

func TestShowIfRendersGatedLinkLinesInPlace(t *testing.T) {
	body := "开头\n{{showIf(bgm.link)}}{{link(bgm.link, 'Bangumi')}}\n结尾"
	hidden := renderBody(t, TriggerDownloadSubmitted, body, map[string]any{})
	if hidden.Body != "开头\n结尾" {
		t.Fatalf("hidden body = %q", hidden.Body)
	}
	shown := renderBody(t, TriggerDownloadSubmitted, body, map[string]any{"bgm.id": "123"})
	if shown.Body != "开头\nBangumi\n结尾" {
		t.Fatalf("shown body = %q", shown.Body)
	}
	if len(Links(shown.BodyParts)) != 1 {
		t.Fatalf("shown parts = %+v", shown.BodyParts)
	}
}

func TestShowIfPlacementIsValidated(t *testing.T) {
	for _, test := range []struct{ name, body, message string }{
		{"mid line", "前缀 {{showIf(anime.title)}}", "showIf 只能写在行首。"},
		{"twice on a line", "{{showIf(anime.title)}}{{showIf(anime.title)}}", "每行只能有一个 showIf。"},
		{"nested in a call", "{{coalesce(showIf(anime.title), 'x')}}", "showIf 只能单独写在行首。"},
		{"nested in showIf", "{{showIf(showIf(anime.title))}}", "showIf 不能作为参数使用。"},
		{"over two lines", "{{showIf(anime.title,\nrelease.title)}}x", "showIf 必须写在一行内。"},
	} {
		errors, ok := Compile(TriggerDownloadSubmitted, "标题", test.body)
		if !ok || len(errors) != 1 || errors[0].Message != test.message {
			t.Fatalf("%s: errors = %+v", test.name, errors)
		}
	}
}

// Compile still validates everything a gate would hide at delivery.
func TestCompileValidatesInsideAndAfterAGatedLine(t *testing.T) {
	errors, ok := Compile(TriggerDownloadSubmitted, "标题", "{{showIf(anime.title)}}x{{nope.var}}\n{{showIf(nope.other)}}y")
	if !ok || len(errors) != 2 {
		t.Fatalf("errors = %+v", errors)
	}
	if !strings.Contains(errors[0].Message, "nope.var") || !strings.Contains(errors[1].Message, "nope.other") {
		t.Fatalf("errors = %+v", errors)
	}
}

func TestShowIfGatesTheTitleToo(t *testing.T) {
	message, ok := RenderMessage(TriggerDownloadSubmitted, "{{showIf(anime.title)}}{{anime.title}}", "正文", map[string]any{})
	if !ok || !message.Valid() || message.Title != "" || message.Body != "正文" {
		t.Fatalf("render = %+v", message)
	}
}
