package application

import (
	"encoding/json"
	"testing"
	"time"

	"bansoko/internal/templatecatalog/notification"
)

func TestWebhookEnvelopeCarriesTypedAttachments(t *testing.T) {
	message := outboundMessage{
		Title: "Title", Body: "Body",
		Image: &outboundImage{Source: "mikan", URL: "https://mikanani.me/images/1.jpg"},
		Links: []notification.Link{{Provider: "bgm", Label: "Bangumi", URL: "https://bgm.tv/subject/123"}},
	}
	raw, err := buildWebhookEnvelope("delivery-1", notification.TriggerDownloadSubmitted, 1, time.Unix(1767225600, 0), message, []byte(`{"schema_version":1,"trigger_kind":"download_submitted","facts":{"anime.title":"Show"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["version"]; present {
		t.Fatalf("webhook envelope must not carry a version: %s", raw)
	}
	var envelope webhookEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Message.Title != "Title" || envelope.Message.Body != "Body" {
		t.Fatalf("envelope = %+v", envelope)
	}
	if envelope.Message.Image == nil || envelope.Message.Image.Source != "mikan" || envelope.Message.Image.URL != message.Image.URL || envelope.Message.Image.Placeholder {
		t.Fatalf("image = %+v", envelope.Message.Image)
	}
	if len(envelope.Message.Links) != 1 || envelope.Message.Links[0].URL != "https://bgm.tv/subject/123" {
		t.Fatalf("links = %+v", envelope.Message.Links)
	}
}

func TestFeishuPostContentUsesNativeMetadataLinks(t *testing.T) {
	message := outboundMessage{
		Title: "Title", Body: "First\n\nSecond",
		Links: []notification.Link{
			{Provider: "bgm", Label: "Bangumi", URL: "https://bgm.tv/subject/123"},
			{Provider: "tmdb", Label: "TMDB", URL: "https://www.themoviedb.org/tv/456"},
		},
	}
	content := feishuPostContent(message)
	if len(content) != 4 || content[1][0].Text != " " {
		t.Fatalf("content = %+v", content)
	}
	links := content[3]
	if len(links) != 3 || links[0].Tag != "a" || links[0].Href != message.Links[0].URL || links[2].Href != message.Links[1].URL {
		t.Fatalf("links = %+v", links)
	}
}
