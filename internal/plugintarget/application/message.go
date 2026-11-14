package application

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/templatecatalog/notification"
)

// outboundMessage is the channel-neutral delivery IR. Templates produce text
// plus semantic attachments; only a channel renderer turns these values into
// Telegram HTML, a multipart photo, or another provider's native shape.
type outboundMessage struct {
	Title string
	Body  string
	Image *outboundImage
	Links []notification.Link
}

type outboundImage struct {
	Source      string
	URL         string
	Data        []byte
	ContentType string
	Placeholder bool
}

//go:embed assets/notification_placeholder.jpg
var notificationPlaceholderJPEG []byte

func placeholderNotificationImage() *outboundImage {
	return &outboundImage{
		Source: "placeholder", Data: notificationPlaceholderJPEG,
		ContentType: "image/jpeg", Placeholder: true,
	}
}

func (h *PluginDeliverHandler) resolveOutboundMessage(ctx context.Context, row deliveryRow, title, body string) (outboundMessage, error) {
	message := outboundMessage{Title: title, Body: body}
	facts, err := notificationFactsForDelivery(row)
	if err != nil {
		return message, err
	}
	attachments, ok := notification.SemanticAttachments(row.triggerKind, row.titleTemplate, row.bodyTemplate, facts)
	if !ok {
		return message, errors.New("notification trigger is invalid")
	}
	message.Links = attachments.Links
	if !attachments.Image {
		return message, nil
	}

	request := plugintargetport.NotificationImageRequest{
		ManagedAnimeID: row.managedAnimeID.String,
		ReleaseSource:  notificationFactString(facts, "release.src"),
		BGMID:          notificationFactString(facts, "bgm.id"),
		TMDBID:         notificationFactString(facts, "tmdb.id"),
		TMDBKind:       notificationFactString(facts, "tmdb.kind"),
	}
	if h.imageResolver != nil {
		resolved, resolveErr := h.imageResolver.ResolveNotificationImage(ctx, request)
		if resolveErr == nil && validNotificationImageURL(resolved.URL) {
			message.Image = &outboundImage{Source: resolved.Source, URL: resolved.URL}
			return message, nil
		}
	}
	message.Image = placeholderNotificationImage()
	return message, nil
}

func notificationFactsForDelivery(row deliveryRow) (map[string]any, error) {
	if row.deliveryKind == "test" {
		return notification.Sample(row.triggerKind), nil
	}
	var envelope struct {
		Facts json.RawMessage `json:"facts"`
	}
	if err := json.Unmarshal(row.payload, &envelope); err != nil {
		return nil, errors.New("decode notification payload facts")
	}
	return decodeBusinessFacts(envelope.Facts)
}

func notificationFactString(facts map[string]any, name string) string {
	value, _ := notification.LookupFact(facts, name)
	return strings.TrimSpace(notification.FormatFact(value))
}

func validNotificationImageURL(raw string) bool {
	if raw == "" || len(raw) > 2048 {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.User == nil && parsed.Hostname() != "" && (parsed.Scheme == "https" || parsed.Scheme == "http")
}
