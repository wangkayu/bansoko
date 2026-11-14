package application

import (
	"context"
	"io"
	"mime"
	"net/http"
	"strings"
)

const maxTelegramImageBytes = 2 << 20

// sendTelegramMessage changes representation only after Telegram has definitely
// rejected the image. Unknown outcomes, 429 and 5xx return immediately: trying
// another representation in those cases could duplicate an accepted message.
func (h *PluginDeliverHandler) sendTelegramMessage(ctx context.Context, req *http.Request, mode telegramRequestMode, row deliveryRow, message outboundMessage, secretValue string, redactor secretRedactor) (attemptResult, error) {
	modes := make([]string, 0, 3)
	for {
		modes = append(modes, mode.String())
		result, sendErr := h.sendRequest(req, row.adapterKind, redactor)
		result.telegramModes = append([]string(nil), modes...)
		if result.state == "delivered" || !result.telegramImageRejected || mode.Method != "sendPhoto" {
			return result, sendErr
		}

		var nextImage *outboundImage
		switch mode.Image {
		case "url":
			nextImage = h.downloadTelegramImage(ctx, message.Image)
			if nextImage == nil {
				nextImage = placeholderNotificationImage()
			}
		case "multipart":
			nextImage = placeholderNotificationImage()
		case "placeholder":
			chatID, token, err := telegramCredentials(row, secretValue)
			if err != nil {
				return telegramFallbackBuildFailure(modes), nil
			}
			next, err := buildTelegramHTMLMessageRequest(ctx, chatID, token, fitTelegramHTML(message, telegramTextLimit))
			if err != nil {
				return telegramFallbackBuildFailure(modes), nil
			}
			req, mode = next, telegramRequestMode{Method: "sendMessage"}
			continue
		default:
			return result, sendErr
		}

		chatID, token, err := telegramCredentials(row, secretValue)
		if err != nil {
			return telegramFallbackBuildFailure(modes), nil
		}
		next, err := buildTelegramPhotoRequest(ctx, chatID, token, fitTelegramHTML(message, telegramCaptionLimit), nextImage)
		if err != nil {
			return telegramFallbackBuildFailure(modes), nil
		}
		imageMode := "multipart"
		if nextImage.Placeholder {
			imageMode = "placeholder"
		}
		req, mode = next, telegramRequestMode{Method: "sendPhoto", Image: imageMode, Source: nextImage.Source}
	}
}

func telegramFallbackBuildFailure(modes []string) attemptResult {
	return attemptResult{
		state: "failed", errorCode: "request_build_failed", err: "telegram fallback request could not be built",
		telegramModes: append([]string(nil), modes...),
	}
}

// downloadTelegramImage is used only after Telegram's definite URL rejection.
// The safe transport, byte cap and image content allowlist keep this fallback
// from becoming an arbitrary URL fetcher.
func (h *PluginDeliverHandler) downloadTelegramImage(ctx context.Context, image *outboundImage) *outboundImage {
	if image == nil || !validNotificationImageURL(image.URL) {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, image.URL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "image/jpeg,image/png")
	client := h.imageClient
	if client == nil {
		client = newSafeHTTPClient(safeHTTPHooks{})
	}
	resp, err := client.Do(req)
	if err != nil || resp == nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxTelegramImageBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxTelegramImageBytes {
		return nil
	}
	contentType := ""
	if parsed, _, parseErr := mime.ParseMediaType(resp.Header.Get("Content-Type")); parseErr == nil {
		contentType = strings.ToLower(parsed)
	}
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	if contentType != "image/jpeg" && contentType != "image/png" {
		return nil
	}
	return &outboundImage{Source: image.Source, Data: data, ContentType: contentType}
}

func telegramAttemptEvidence(result attemptResult) map[string]any {
	if len(result.telegramModes) == 0 {
		return nil
	}
	return map[string]any{"telegram_requests": result.telegramModes}
}
