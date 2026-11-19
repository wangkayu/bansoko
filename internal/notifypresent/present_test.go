package notifypresent

import (
	"strings"
	"testing"

	"bansoko/internal/templatecatalog/notification"
)

func TestTelegramUsesPhotoWhenTheCaptionFits(t *testing.T) {
	message := Message{
		Title: "Title", Body: "Body",
		Image: &Image{Source: "mikan", URL: "https://mikanani.me/1.jpg"},
	}
	presentation := Telegram(message)
	if presentation.Mode != TelegramModePhoto || presentation.ImageOmitted || presentation.Truncated {
		t.Fatalf("presentation = %+v", presentation)
	}
	if presentation.Text != "<b>Title</b>\n\nBody" {
		t.Fatalf("text = %q", presentation.Text)
	}
	if presentation.TextUnits != TelegramTextUnits(presentation.Text) {
		t.Fatalf("units = %d, want %d", presentation.TextUnits, TelegramTextUnits(presentation.Text))
	}
}

func TestTelegramDropsTheImageWhenTheCaptionIsTooLong(t *testing.T) {
	long := strings.Repeat("界", TelegramCaptionLimit)
	message := Message{
		Title: "Title", Body: long,
		Image: &Image{Source: "mikan", URL: "https://mikanani.me/1.jpg"},
		Parts: []notification.BodyPart{{Text: long}},
	}
	presentation := Telegram(message)
	if presentation.Mode != TelegramModeMessage || !presentation.ImageOmitted || presentation.Truncated {
		t.Fatalf("presentation = %+v (units %d)", presentation, presentation.TextUnits)
	}
	if presentation.TextUnits > TelegramTextLimit {
		t.Fatalf("text has %d units", presentation.TextUnits)
	}
}

func TestTelegramTruncationKeepsLinksAndBudgetsUtf16(t *testing.T) {
	body := strings.Repeat(`<&>"'`, 5000)
	message := Message{
		Title: "Title", Body: body,
		Parts: []notification.BodyPart{
			{Text: body},
			{Text: "Bangumi", Link: &notification.Link{Provider: "bgm", Label: "Bangumi", URL: "https://bgm.tv/subject/123"}},
		},
	}
	presentation := Telegram(message)
	if presentation.Mode != TelegramModeMessage || presentation.ImageOmitted || !presentation.Truncated {
		t.Fatalf("presentation = %+v", presentation)
	}
	if presentation.TextUnits > TelegramTextLimit {
		t.Fatalf("text has %d units", presentation.TextUnits)
	}
	if got := TelegramHTML(Message{Title: presentation.Title, Parts: presentation.Parts}); got != presentation.Text || notification.PlainText(presentation.Parts) == body {
		t.Fatal("visible preview must use exactly the fitted parts used on the wire")
	}
	for _, want := range []string{"&amp;", "Bangumi", "<b>"} {
		if !strings.Contains(presentation.Text, want) {
			t.Fatalf("text lost %q: %q", want, presentation.Text)
		}
	}
}

func TestFeishuKeepsLinksInlineAndAppendsTheImageLink(t *testing.T) {
	message := Message{
		Title: "Title", Body: "First\nBangumi",
		Parts: []notification.BodyPart{
			{Text: "First\n"},
			{Text: "Bangumi", Link: &notification.Link{Provider: "bgm", Label: "Bangumi", URL: "https://bgm.tv/subject/123"}},
		},
		Image: &Image{Source: "tmdb", URL: "https://image.tmdb.org/1.jpg"},
	}
	presentation := Feishu(message)
	if presentation.Image == nil || presentation.Image.Label != "TMDB" || presentation.Image.URL != "https://image.tmdb.org/1.jpg" {
		t.Fatalf("image = %+v", presentation.Image)
	}
	if presentation.ImageOmitted {
		t.Fatal("image should not be omitted")
	}
	if len(presentation.Content) != 3 {
		t.Fatalf("content = %+v", presentation.Content)
	}
	if presentation.Content[1][0].Tag != "a" || presentation.Content[1][0].Href != "https://bgm.tv/subject/123" {
		t.Fatalf("inline link = %+v", presentation.Content[1])
	}
	last := presentation.Content[2][0]
	if last.Tag != "a" || last.Text != "TMDB" || last.Href != "https://image.tmdb.org/1.jpg" {
		t.Fatalf("image paragraph = %+v", last)
	}
}

func TestFeishuOmitsAnImageWithoutURL(t *testing.T) {
	presentation := Feishu(Message{
		Title: "Title", Body: "Body",
		Parts: []notification.BodyPart{{Text: "Body"}},
		Image: &Image{Source: "automatic", Placeholder: true},
	})
	if presentation.Image != nil || !presentation.ImageOmitted {
		t.Fatalf("presentation = %+v", presentation)
	}
	for _, paragraph := range presentation.Content {
		for _, element := range paragraph {
			if element.Tag == "a" {
				t.Fatalf("an unavailable image must not add a link paragraph: %+v", presentation.Content)
			}
		}
	}
}

func TestWebhookCarriesTheTypedMessage(t *testing.T) {
	presentation := Webhook(Message{
		Title: "Title", Body: "Body",
		Image: &Image{Source: "mikan", URL: "https://mikanani.me/1.jpg"},
		Links: []notification.Link{{Provider: "bgm", Label: "Bangumi", URL: "https://bgm.tv/subject/123"}},
	})
	message := presentation.Message
	if message.Title != "Title" || message.Body != "Body" {
		t.Fatalf("message = %+v", message)
	}
	if message.Image == nil || message.Image.Source != "mikan" || message.Image.URL != "https://mikanani.me/1.jpg" || message.Image.Placeholder {
		t.Fatalf("image = %+v", message.Image)
	}
	if len(message.Links) != 1 || message.Links[0].Provider != "bgm" {
		t.Fatalf("links = %+v", message.Links)
	}
}
