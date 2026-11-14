package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"unicode/utf16"

	"bansoko/internal/templatecatalog/notification"
)

const (
	telegramCaptionLimit = 1024
	telegramTextLimit    = 4096
)

type telegramRequestMode struct {
	Method string
	Image  string
	Source string
}

func (m telegramRequestMode) String() string {
	if m.Image == "" {
		return m.Method
	}
	return m.Method + ":" + m.Image
}

func (h *PluginDeliverHandler) buildTelegramMessageRequest(ctx context.Context, row deliveryRow, message outboundMessage, secretValue string) (*http.Request, telegramRequestMode, error) {
	chatID, token, err := telegramCredentials(row, secretValue)
	if err != nil {
		return nil, telegramRequestMode{}, err
	}
	fullHTML := renderTelegramHTML(message)
	if message.Image != nil && telegramTextLength(fullHTML) <= telegramCaptionLimit {
		mode := telegramRequestMode{Method: "sendPhoto", Image: "url", Source: message.Image.Source}
		if message.Image.Placeholder {
			mode.Image = "placeholder"
		}
		req, err := buildTelegramPhotoRequest(ctx, chatID, token, fullHTML, message.Image)
		return req, mode, err
	}
	mode := telegramRequestMode{Method: "sendMessage"}
	req, err := buildTelegramHTMLMessageRequest(ctx, chatID, token, fitTelegramHTML(message, telegramTextLimit))
	return req, mode, err
}

func telegramCredentials(row deliveryRow, secretValue string) (chatID, token string, err error) {
	var config struct {
		ChatID string `json:"chat_id"`
	}
	if json.Unmarshal(row.adapterConfigJSON, &config) != nil || config.ChatID == "" {
		return "", "", errors.New("telegram config missing chat_id")
	}
	var secret struct {
		BotToken *string `json:"bot_token"`
	}
	if json.Unmarshal([]byte(secretValue), &secret) != nil || secret.BotToken == nil || *secret.BotToken == "" {
		return "", "", errors.New("telegram secret missing bot_token")
	}
	return config.ChatID, *secret.BotToken, nil
}

func buildTelegramPhotoRequest(ctx context.Context, chatID, token, caption string, image *outboundImage) (*http.Request, error) {
	if image == nil {
		return nil, errors.New("telegram photo is missing")
	}
	if image.URL != "" && !image.Placeholder {
		if !validNotificationImageURL(image.URL) {
			return nil, errors.New("telegram image URL is invalid")
		}
		payload, _ := json.Marshal(map[string]string{
			"chat_id": chatID, "photo": image.URL, "caption": caption, "parse_mode": "HTML",
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+token+"/sendPhoto", bytes.NewReader(payload))
		if err != nil {
			return nil, errors.New("telegram request is invalid")
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	}
	if len(image.Data) == 0 {
		return nil, errors.New("telegram image bytes are empty")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range map[string]string{"chat_id": chatID, "caption": caption, "parse_mode": "HTML"} {
		if err := writer.WriteField(name, value); err != nil {
			return nil, errors.New("telegram multipart request is invalid")
		}
	}
	part, err := writer.CreateFormFile("photo", "notification.jpg")
	if err != nil {
		return nil, errors.New("telegram multipart request is invalid")
	}
	if _, err := part.Write(image.Data); err != nil {
		return nil, errors.New("telegram multipart request is invalid")
	}
	if err := writer.Close(); err != nil {
		return nil, errors.New("telegram multipart request is invalid")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+token+"/sendPhoto", &body)
	if err != nil {
		return nil, errors.New("telegram request is invalid")
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req, nil
}

func buildTelegramHTMLMessageRequest(ctx context.Context, chatID, token, text string) (*http.Request, error) {
	payload, _ := json.Marshal(map[string]string{
		"chat_id": chatID, "text": text, "parse_mode": "HTML",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+token+"/sendMessage", bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("telegram request is invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func renderTelegramHTML(message outboundMessage) string {
	var builder strings.Builder
	if message.Title != "" {
		builder.WriteString("<b>")
		builder.WriteString(html.EscapeString(message.Title))
		builder.WriteString("</b>")
	}
	if message.Body != "" {
		if builder.Len() > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString(html.EscapeString(message.Body))
	}
	appendTelegramLinks(&builder, message.Links)
	return builder.String()
}

func appendTelegramLinks(builder *strings.Builder, links []notification.Link) {
	if len(links) == 0 {
		return
	}
	if builder.Len() > 0 {
		builder.WriteString("\n\n")
	}
	for index, link := range links {
		if index > 0 {
			builder.WriteString(" · ")
		}
		builder.WriteString(`<a href="`)
		builder.WriteString(html.EscapeString(link.URL))
		builder.WriteString(`">`)
		builder.WriteString(html.EscapeString(link.Label))
		builder.WriteString("</a>")
	}
}

func fitTelegramHTML(message outboundMessage, limit int) string {
	full := renderTelegramHTML(message)
	if telegramTextLength(full) <= limit {
		return full
	}
	message.Title = truncateTelegramHTMLText(message.Title, min(256, limit/4))
	var links strings.Builder
	appendTelegramLinks(&links, message.Links)
	reserved := telegramTextLength(html.EscapeString(message.Title)) + len("<b></b>") + telegramTextLength(links.String()) + 4
	bodyBudget := max(0, limit-reserved)
	message.Body = truncateTelegramHTMLText(message.Body, bodyBudget)
	return renderTelegramHTML(message)
}

// truncateTelegramHTMLText budgets the escaped representation that Telegram
// actually receives, not the shorter plain text before HTML escaping.
func truncateTelegramHTMLText(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if telegramTextLength(html.EscapeString(value)) <= limit {
		return value
	}
	var builder strings.Builder
	units := 0
	for _, r := range value {
		width := telegramTextLength(html.EscapeString(string(r)))
		if units+width+1 > limit {
			break
		}
		builder.WriteRune(r)
		units += width
	}
	builder.WriteRune('…')
	return builder.String()
}

func telegramTextLength(value string) int {
	return len(utf16.Encode([]rune(value)))
}

func telegramResponseImageRejected(status int, body []byte) bool {
	if status != http.StatusBadRequest {
		return false
	}
	var response struct {
		OK          *bool  `json:"ok"`
		Description string `json:"description"`
	}
	if json.Unmarshal(body, &response) != nil || response.OK == nil || *response.OK {
		return false
	}
	description := strings.ToLower(response.Description)
	for _, marker := range []string{
		"wrong file identifier/http url specified",
		"failed to get http url content",
		"wrong type of the web page content",
		"photo_invalid_dimensions",
		"image_process_failed",
		"invalid image",
	} {
		if strings.Contains(description, marker) {
			return true
		}
	}
	return false
}

func telegramMultipartPhoto(req *http.Request) ([]byte, error) {
	if req == nil || !strings.HasPrefix(req.Header.Get("Content-Type"), "multipart/form-data") {
		return nil, errors.New("not a telegram multipart photo request")
	}
	if err := req.ParseMultipartForm(3 << 20); err != nil {
		return nil, err
	}
	file, _, err := req.FormFile("photo")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return nil, fmt.Errorf("invalid telegram photo bytes")
	}
	return data, nil
}
