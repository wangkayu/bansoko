package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// Feishu custom-bot webhooks cannot upload image bytes or reference an image
// URL natively. The channel-neutral image is therefore represented as a link;
// metadata links use Feishu's native post elements rather than flattened text.
func (h *PluginDeliverHandler) buildFeishuRequest(ctx context.Context, message outboundMessage, secretValue string) (*http.Request, error) {
	var secret struct {
		WebhookURL *string `json:"webhook_url"`
	}
	if json.Unmarshal([]byte(secretValue), &secret) != nil || secret.WebhookURL == nil {
		return nil, errors.New("feishu secret missing webhook_url")
	}
	endpoint, err := parseHTTPSURL(*secret.WebhookURL)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{
		"msg_type": "post",
		"content": map[string]any{"post": map[string]any{"zh_cn": map[string]any{
			"title": message.Title, "content": feishuPostContent(message),
		}}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("feishu request is invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

type feishuPostElement struct {
	Tag  string `json:"tag"`
	Text string `json:"text"`
	Href string `json:"href,omitempty"`
}

func feishuImageLabel(source string) string {
	switch source {
	case "mikan":
		return "Mikan"
	case "anibt":
		return "AniBT"
	case "bgm":
		return "Bangumi"
	case "tmdb":
		return "TMDB"
	default:
		return "Image"
	}
}

func feishuPostContent(message outboundMessage) [][]feishuPostElement {
	lines := make([][]feishuPostElement, 0, len(message.Links)+2)
	for _, line := range strings.Split(message.Body, "\n") {
		if line == "" {
			line = " "
		}
		lines = append(lines, []feishuPostElement{{Tag: "text", Text: line}})
	}
	if message.Image != nil && message.Image.URL != "" {
		lines = append(lines, []feishuPostElement{{Tag: "a", Text: feishuImageLabel(message.Image.Source), Href: message.Image.URL}})
	}
	if len(message.Links) > 0 {
		elements := make([]feishuPostElement, 0, len(message.Links)*2-1)
		for index, link := range message.Links {
			if index > 0 {
				elements = append(elements, feishuPostElement{Tag: "text", Text: " · "})
			}
			elements = append(elements, feishuPostElement{Tag: "a", Text: link.Label, Href: link.URL})
		}
		lines = append(lines, elements)
	}
	return lines
}
