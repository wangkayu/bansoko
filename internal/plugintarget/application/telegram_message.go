package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"bansoko/internal/notifypresent"
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
	presentation := notifypresent.Telegram(presentMessage(message))
	if presentation.Mode == notifypresent.TelegramModePhoto {
		mode := telegramRequestMode{Method: "sendPhoto", Image: "url", Source: message.Image.Source}
		if message.Image.Placeholder {
			mode.Image = "placeholder"
		}
		req, err := buildTelegramPhotoRequest(ctx, chatID, token, presentation.Text, message.Image)
		return req, mode, err
	}
	mode := telegramRequestMode{Method: "sendMessage"}
	req, err := buildTelegramHTMLMessageRequest(ctx, chatID, token, presentation.Text)
	return req, mode, err
}

// presentMessage maps the delivery IR onto the shared channel presentation
// input. outboundImage.Data stays local to delivery; presentation never needs
// image bytes.
func presentMessage(message outboundMessage) notifypresent.Message {
	presented := notifypresent.Message{Title: message.Title, Body: message.Body, Parts: message.Parts, Links: message.Links}
	if message.Image != nil {
		presented.Image = &notifypresent.Image{Source: message.Image.Source, URL: message.Image.URL, Placeholder: message.Image.Placeholder}
	}
	return presented
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
