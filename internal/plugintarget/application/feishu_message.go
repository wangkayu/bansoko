package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"bansoko/internal/notifypresent"
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
	presentation := notifypresent.Feishu(presentMessage(message))
	payload, _ := json.Marshal(map[string]any{
		"msg_type": "post",
		"content": map[string]any{"post": map[string]any{"zh_cn": map[string]any{
			"title": presentation.Title, "content": presentation.Content,
		}}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("feishu request is invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}
